import contextlib
import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import release


ROOT = Path(__file__).resolve().parents[2]


class ReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.original = Path.cwd()
        os.chdir(self.temp.name)
        self.addCleanup(os.chdir, self.original)
        Path("internal/version").mkdir(parents=True)
        Path("internal/version/version.go").write_text('const Current = "0.2.27"\n')
        Path("cmd/agenterm-client-gui").mkdir(parents=True)
        Path("cmd/agenterm-client-gui/FyneApp.toml").write_text('Version = "0.2.27"\n')
        self.env = patch.dict(os.environ, {"GH_REPO": "test/repo", "GITHUB_OUTPUT": "outputs",
                                         "GITHUB_REF_TYPE": "branch", "GITHUB_STEP_SUMMARY": "summary"})
        self.env.start()
        self.addCleanup(self.env.stop)
        self.base = Path("fixture")
        self.base.mkdir()
        for component in release.COMPONENTS:
            for name in release.assets(component):
                (self.base / name).write_text("original bytes: " + name)
        (self.base / release.MANIFEST).write_text(json.dumps({
            "schema": 1, "release_tag": "v0.2.26", "components": {
                c: {"version": "0.2.26", "source_tag": "v0.2.26"}
                for c in release.COMPONENTS
            },
        }))
        self.metadata = {"tag_name": "v0.2.26", "draft": False,
                         "published_at": "2026-11-01T10:20:42Z", "assets": []}
        self.refresh_digests()
        self.releases = [self.metadata]
        self.tag_commit = "commit"
        self.tags = ""
        self.downloads = []

    def refresh_digests(self):
        self.metadata["assets"] = [
            {"name": path.name, "digest": "sha256:" + hashlib.sha256(path.read_bytes()).hexdigest()}
            for path in sorted(self.base.iterdir()) if path.is_file()
        ]

    def mock_run(self, *args):
        if args[:3] == ("git", "rev-parse", "HEAD"):
            return "commit"
        if args[:3] == ("git", "tag", "--list"):
            return self.tags
        if args[:2] == ("git", "rev-list"):
            return self.tag_commit
        if args[:2] == ("gh", "api"):
            return json.dumps([self.releases])
        self.fail(f"Unexpected command: {args}")

    def mock_download(self, tag, directory, names):
        self.assertEqual(tag, "v0.2.26")
        if not names:
            return
        directory.mkdir(exist_ok=True)
        self.downloads.extend(name for name in names if name != release.MANIFEST)
        for name in names:
            shutil.copyfile(self.base / name, directory / name)

    def prepare(self, scope, base="", compatibility=True, is_ancestor=True):
        with patch.object(release, "run", self.mock_run), \
             patch.object(release, "download", self.mock_download), \
             patch.object(release, "ancestor", return_value=is_ancestor), \
             patch.object(release, "compatible", return_value=compatibility), \
             contextlib.redirect_stdout(io.StringIO()):
            release.prepare(scope, base)
        return json.loads(Path("release-plan/release-manifest.json").read_text())

    def test_server_only_reuses_complete_clients(self):
        manifest = self.prepare("server")
        self.assertEqual(manifest["components"]["server"]["version"], "0.2.27")
        self.assertEqual(manifest["components"]["cli"]["version"], "0.2.26")
        self.assertEqual(manifest["components"]["gui"]["version"], "0.2.26")
        self.assertIn("cli=false\nserver=true\ngui=false", Path("outputs").read_text())
        self.assertEqual(set(self.downloads), {*release.assets("cli"), *release.assets("gui")})

    def test_cli_also_builds_server(self):
        self.prepare("cli")
        self.assertIn("cli=true\nserver=true\ngui=false", Path("outputs").read_text())
        self.assertEqual(set(self.downloads), {*release.assets("gui")})

    def test_gui_only_reuses_server_and_cli(self):
        self.prepare("gui")
        self.assertIn("cli=false\nserver=false\ngui=true", Path("outputs").read_text())
        self.assertEqual(set(self.downloads), {*release.assets("cli"), *release.assets("server")})

    def test_client_rebuilds_everything_without_downloads(self):
        self.prepare("client")
        self.assertIn("cli=true\nserver=true\ngui=true", Path("outputs").read_text())
        self.assertEqual(self.downloads, [])

    def test_no_base_falls_back_to_full_build(self):
        self.releases = []
        self.prepare("server")
        self.assertIn("cli=true\nserver=true\ngui=true", Path("outputs").read_text())

    def test_old_asset_layout_falls_back_before_downloading_programs(self):
        for scope in ("server", "cli", "gui"):
            with self.subTest(scope=scope):
                self.metadata["assets"] = [a for a in self.metadata["assets"]
                                           if a["name"] == release.MANIFEST or a["name"].startswith("agenterm-server-")]
                self.prepare(scope)
                self.assertIn("cli=true\nserver=true\ngui=true", Path("outputs").read_text())
                self.assertEqual(self.downloads, [])
                shutil.rmtree("release-plan")

    def test_incompatible_base_falls_back_before_downloading_programs(self):
        self.prepare("server", compatibility=False)
        self.assertIn("cli=true\nserver=true\ngui=true", Path("outputs").read_text())
        self.assertEqual(self.downloads, [])

    def test_explicit_missing_or_unrelated_base_fails(self):
        for base, is_ancestor in [("v0.2.25", True), ("v0.2.26", False)]:
            with self.subTest(base=base), self.assertRaisesRegex(ValueError, "ancestor"):
                self.prepare("server", base, is_ancestor=is_ancestor)

    def test_existing_release_is_not_modified(self):
        self.releases.append({"tag_name": "v0.2.27"})
        with self.assertRaisesRegex(ValueError, "already exists"):
            self.prepare("all")

    def test_tag_must_match_source_commit(self):
        self.tags, self.tag_commit = "v0.2.27", "other"
        with self.assertRaisesRegex(ValueError, "different commit"):
            self.prepare("all")

    def test_push_tag_must_match_version(self):
        with patch.dict(os.environ, {"GITHUB_REF_TYPE": "tag", "GITHUB_REF_NAME": "v0.2.28"}):
            with self.assertRaisesRegex(ValueError, "must match"):
                self.prepare("all")

    def test_missing_or_corrupt_reused_asset_fails(self):
        (self.base / release.assets("cli")[0]).write_text("corrupted")
        with self.assertRaisesRegex(ValueError, "checksum mismatch"):
            self.prepare("server")

    def test_missing_reused_asset_fails(self):
        (self.base / release.assets("gui")[0]).unlink()
        with self.assertRaises(FileNotFoundError):
            self.prepare("server")

    def test_gui_version_must_match_when_built(self):
        Path("cmd/agenterm-client-gui/FyneApp.toml").write_text('Version = "0.2.26"\n')
        with self.assertRaisesRegex(ValueError, "GUI version"):
            self.prepare("all")

    def test_transitive_reuse_preserves_original_versions(self):
        components = {c: {"version": "0.2.24", "source_tag": "v0.2.24"} for c in release.COMPONENTS}
        (self.base / release.MANIFEST).write_text(json.dumps({
            "schema": 1, "release_tag": "v0.2.26", "components": components}))
        self.refresh_digests()
        manifest = self.prepare("server")
        self.assertEqual(manifest["components"]["cli"], components["cli"])
        self.assertEqual(manifest["components"]["gui"], components["gui"])

    def test_manifest_must_match_github_digest(self):
        (self.base / release.MANIFEST).write_text("{}")
        self.refresh_digests()
        (self.base / release.MANIFEST).write_text('{"changed": true}')
        with self.assertRaisesRegex(ValueError, "checksum mismatch"):
            self.prepare("server")

    def test_missing_github_digest_rejects_reuse(self):
        name = release.assets("cli")[0]
        asset = next(a for a in self.metadata["assets"] if a["name"] == name)
        asset["digest"] = None
        with self.assertRaisesRegex(ValueError, "Missing GitHub SHA256 digest"):
            self.prepare("server")

    def test_only_valid_github_sha256_digests_are_used(self):
        valid = "a" * 64
        for value in [None, "", "sha256:invalid", "sha256:" + "g" * 64, "sha512:" + valid]:
            with self.subTest(digest=value):
                self.assertEqual(release.asset_digests([{"name": "client", "digest": value}]), {})
        self.assertEqual(release.asset_digests([{"name": "client", "digest": "sha256:" + valid}]), {"client": valid})

    def test_legacy_checksum_file_is_not_downloaded(self):
        (self.base / "SHA256SUMS").write_text("obsolete checksum file")
        self.refresh_digests()
        self.prepare("cli")
        self.assertEqual(set(self.downloads), {*release.assets("gui")})

    def test_full_build_needs_no_reused_files(self):
        self.prepare("all")
        self.assertEqual(list(Path("release-plan/reused").iterdir()), [])
        directory = Path("artifacts")
        directory.mkdir()
        for component in release.COMPONENTS:
            for name in release.assets(component):
                (directory / name).write_text("new bytes: " + name)
        release.finalize(directory, Path("release-plan"))
        self.assertEqual(len(list(directory.iterdir())), 22)

    def test_obsolete_release_attachments_are_rejected(self):
        self.prepare("all")
        directory = Path("artifacts")
        directory.mkdir()
        for component in release.COMPONENTS:
            for name in release.assets(component):
                (directory / name).write_text("new bytes: " + name)
        for name in ["install.sh", "install.ps1", "SHA256SUMS"]:
            with self.subTest(attachment=name):
                (directory / name).write_text("obsolete")
                with self.assertRaisesRegex(ValueError, "Release assets differ"):
                    release.finalize(directory, Path("release-plan"))
                (directory / name).unlink()

    def test_complete_release_and_reused_digests(self):
        self.prepare("server")
        directory = Path("artifacts")
        shutil.copytree("release-plan/reused", directory)
        for name in release.assets("server"):
            (directory / name).write_text("new bytes: " + name)
        release.finalize(directory, Path("release-plan"))
        hashes = json.loads((Path("release-plan") / release.REUSED_DIGESTS).read_text())
        self.assertEqual(len(list(directory.iterdir())), 22)  # 21 program files and the manifest.
        self.assertEqual(set(hashes), {*release.assets("cli"), *release.assets("gui")})
        release.verify(directory, list(hashes), hashes)
        for name in ["install.sh", "install.ps1", "SHA256SUMS", release.REUSED_DIGESTS]:
            self.assertFalse((directory / name).exists())
        (directory / release.assets("gui")[0]).write_text("changed after reuse")
        with self.assertRaisesRegex(ValueError, "checksum mismatch"):
            release.finalize(directory, Path("release-plan"))

    def test_incomplete_release_is_not_published(self):
        self.prepare("all")
        Path("artifacts").mkdir()
        with self.assertRaisesRegex(ValueError, "Release assets differ"):
            release.finalize(Path("artifacts"), Path("release-plan"))


class CompatibilityTests(unittest.TestCase):
    def test_real_connection_policy(self):
        for left, right, allowed in [
            ("v0.2.27", "0.2.24", True), ("v0.3.0", "0.2.27", False),
            ("v1.4.0", "1.2.0", True), ("v2.0.0", "1.4.0", False),
            ("v0.0.2", "0.0.1", False), ("v0.3.0-rc.2", "0.3.0-rc.1", False),
            ("v0.3.0-rc.1", "0.3.0-rc.1", True), ("v0.2.27", "garbage", False),
        ]:
            with self.subTest(left=left, right=right):
                result = subprocess.run(["go", "run", "./scripts/release/check-compat.go", left, right],
                                        cwd=ROOT, capture_output=True)
                self.assertEqual(result.returncode == 0, allowed, result.stderr.decode())


if __name__ == "__main__":
    unittest.main()
