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
                c: {"source_tag": "v0.2.26", **(
                    {"implementation": "rust", "version": "1.2.3"}
                    if c == "cli" else {"version": "0.2.26"})}
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

    def prepare(self, base="", cli_version="1.2.3", is_ancestor=True):
        with patch.object(release, "run", self.mock_run), \
             patch.object(release, "download", self.mock_download), \
             patch.object(release, "ancestor", return_value=is_ancestor), \
             patch.object(release, "package_version", return_value=cli_version), \
             contextlib.redirect_stdout(io.StringIO()):
            release.prepare(base)
        return json.loads(Path("release-plan/release-manifest.json").read_text())

    def test_published_inventory_contains_only_raw_programs(self):
        self.assertEqual(release.COMPONENTS, ("cli", "server"))
        names = [name for component in release.COMPONENTS for name in release.assets(component)]
        self.assertEqual(len(names), 10)
        self.assertEqual(len(release.assets("cli")), 6)
        self.assertEqual(set(release.assets("server")), {f"agenterm-server-{os}-{arch}"
                         for os in ("linux", "macos") for arch in ("amd64", "arm64")})
        self.assertEqual(len(set(names)), 10)
        self.assertFalse(any(name.endswith((".zip", ".tar.gz", ".tar.xz")) for name in names))

    def test_staging_excludes_generated_archives_and_checksums(self):
        for name in ("components.tar.xz", "agenterm-client-cli-linux-amd64.tar.xz", "agenterm-client-cli-windows-amd64.zip", "SHA256SUMS"):
            (self.base / name).write_text("internal build output")
        output = Path("release-files")
        release.stage(self.base, output, ("cli", "server"))
        self.assertEqual({path.name for path in output.iterdir()}, {*release.assets("cli"), *release.assets("server")})
        for path in output.iterdir():
            self.assertEqual(path.read_bytes(), (self.base / path.name).read_bytes())

    def test_staging_rejects_missing_or_empty_program_before_copying(self):
        name = release.assets("server")[0]
        for missing in (True, False):
            with self.subTest(missing=missing):
                path = self.base / name
                if missing:
                    path.unlink()
                else:
                    path.write_bytes(b"")
                output = Path("release-files")
                with self.assertRaisesRegex(ValueError, "Missing or empty executable"):
                    release.stage(self.base, output, ("cli", "server"))
                self.assertFalse(output.exists())

    def test_server_only_reuses_complete_clients(self):
        # Past releases also contained two Windows server assets.
        (self.base / "agenterm-server-windows-amd64.exe").write_text("old unsupported server")
        (self.base / "agenterm-server-windows-arm64.exe").write_text("old unsupported server")
        self.refresh_digests()
        manifest = self.prepare()
        self.assertEqual(manifest["components"]["server"]["version"], "0.2.27")
        self.assertEqual(manifest["components"]["cli"]["version"], "1.2.3")
        self.assertIn("cli=false\nserver=true", Path("outputs").read_text())
        self.assertEqual(set(self.downloads), set(release.assets("cli")))

    def test_no_base_falls_back_to_full_build(self):
        self.releases = []
        self.prepare()
        self.assertIn("cli=true\nserver=true", Path("outputs").read_text())

    def test_old_asset_layout_falls_back_before_downloading_programs(self):
        self.metadata["assets"] = [a for a in self.metadata["assets"]
                                   if a["name"] == release.MANIFEST or a["name"].startswith("agenterm-server-")]
        self.prepare()
        self.assertIn("cli=true\nserver=true", Path("outputs").read_text())
        self.assertEqual(self.downloads, [])

    def test_legacy_go_cli_forces_rust_rebuild(self):
        path = self.base / release.MANIFEST
        manifest = json.loads(path.read_text())
        del manifest["components"]["cli"]["implementation"]
        manifest["components"]["gui"] = {"version": "0.2.26", "source_tag": "v0.2.26"}
        path.write_text(json.dumps(manifest))
        self.refresh_digests()
        result = self.prepare()
        self.assertEqual(set(result["components"]), {"cli", "server"})
        self.assertEqual(result["components"]["cli"]["implementation"], "rust")
        self.assertIn("cli=true\nserver=true", Path("outputs").read_text())
        self.assertEqual(self.downloads, [])

    def test_changed_version_rebuilds_cli_before_downloading_programs(self):
        manifest = self.prepare(cli_version="1.2.4")
        self.assertEqual(manifest["components"]["cli"]["version"], "1.2.4")
        self.assertIn("cli=true\nserver=true", Path("outputs").read_text())
        self.assertEqual(self.downloads, [])

    def test_explicit_missing_or_unrelated_base_fails(self):
        for base, is_ancestor in [("v0.2.25", True), ("v0.2.26", False)]:
            with self.subTest(base=base), self.assertRaisesRegex(ValueError, "ancestor"):
                self.prepare(base, is_ancestor=is_ancestor)

    def test_existing_release_is_not_modified(self):
        self.releases.append({"tag_name": "v0.2.27"})
        with self.assertRaisesRegex(ValueError, "already exists"):
            self.prepare(cli_version="1.2.4")

    def test_tag_must_match_source_commit(self):
        self.tags, self.tag_commit = "v0.2.27", "other"
        with self.assertRaisesRegex(ValueError, "different commit"):
            self.prepare(cli_version="1.2.4")

    def test_push_tag_must_match_version(self):
        with patch.dict(os.environ, {"GITHUB_REF_TYPE": "tag", "GITHUB_REF_NAME": "v0.2.28"}):
            with self.assertRaisesRegex(ValueError, "must match"):
                self.prepare(cli_version="1.2.4")

    def test_missing_or_corrupt_reused_asset_fails(self):
        (self.base / release.assets("cli")[0]).write_text("corrupted")
        with self.assertRaisesRegex(ValueError, "checksum mismatch"):
            self.prepare()

    def test_missing_reused_asset_fails(self):
        (self.base / release.assets("cli")[0]).unlink()
        with self.assertRaises(FileNotFoundError):
            self.prepare()

    def test_transitive_reuse_preserves_original_versions(self):
        components = {c: {"source_tag": "v0.2.24", **(
            {"implementation": "rust", "version": "1.2.3"}
            if c == "cli" else {"version": "0.2.24"})} for c in release.COMPONENTS}
        (self.base / release.MANIFEST).write_text(json.dumps({
            "schema": 1, "release_tag": "v0.2.26", "components": components}))
        self.refresh_digests()
        manifest = self.prepare()
        self.assertEqual(manifest["components"]["cli"], components["cli"])

    def test_manifest_must_match_github_digest(self):
        (self.base / release.MANIFEST).write_text("{}")
        self.refresh_digests()
        (self.base / release.MANIFEST).write_text('{"changed": true}')
        with self.assertRaisesRegex(ValueError, "checksum mismatch"):
            self.prepare()

    def test_missing_github_digest_rejects_reuse(self):
        name = release.assets("cli")[0]
        asset = next(a for a in self.metadata["assets"] if a["name"] == name)
        asset["digest"] = None
        with self.assertRaisesRegex(ValueError, "Missing GitHub SHA256 digest"):
            self.prepare()

    def test_only_valid_github_sha256_digests_are_used(self):
        valid = "a" * 64
        for value in [None, "", "sha256:invalid", "sha256:" + "g" * 64, "sha512:" + valid]:
            with self.subTest(digest=value):
                self.assertEqual(release.asset_digests([{"name": "client", "digest": value}]), {})
        self.assertEqual(release.asset_digests([{"name": "client", "digest": "sha256:" + valid}]), {"client": valid})

    def test_legacy_checksum_file_is_not_downloaded(self):
        (self.base / "SHA256SUMS").write_text("obsolete checksum file")
        self.refresh_digests()
        self.prepare()
        self.assertEqual(set(self.downloads), set(release.assets("cli")))

    def test_full_build_needs_no_reused_files(self):
        self.prepare(cli_version="1.2.4")
        self.assertEqual(list(Path("release-plan/reused").iterdir()), [])
        directory = Path("artifacts")
        directory.mkdir()
        for component in release.COMPONENTS:
            for name in release.assets(component):
                (directory / name).write_text("new bytes: " + name)
        release.finalize(directory, Path("release-plan"))
        self.assertEqual(len(list(directory.iterdir())), 11)

    def test_obsolete_release_attachments_are_rejected(self):
        self.prepare(cli_version="1.2.4")
        directory = Path("artifacts")
        directory.mkdir()
        for component in release.COMPONENTS:
            for name in release.assets(component):
                (directory / name).write_text("new bytes: " + name)
        for name in ["agenterm-server-windows-amd64.exe", "agenterm-server-windows-arm64.exe",
                     "install.sh", "install.ps1", "SHA256SUMS", "components.tar.xz", "components.zip",
                     "agenterm-client-cli-linux-amd64.tar.xz", "agenterm-client-cli-macos-arm64.tar.gz",
                     "agenterm-client-cli-windows-amd64.zip", "agenterm-client-gui-macos-arm64",
                     "agenterm-client-gui-windows-amd64.exe", "agenterm-client-gui-macos-arm64.zip",
                     "agenterm-client-gui-windows-amd64.zip"]:
            with self.subTest(attachment=name):
                (directory / name).write_text("obsolete")
                with self.assertRaisesRegex(ValueError, "Release assets differ"):
                    release.finalize(directory, Path("release-plan"))
                (directory / name).unlink()

    def test_draft_repair_uses_original_server_inventory(self):
        self.prepare(cli_version="1.2.4")
        plan = Path("release-plan")
        manifest = json.loads((plan / release.MANIFEST).read_text())
        manifest["schema"] = 1
        (plan / release.MANIFEST).write_text(json.dumps(manifest))
        artifacts = Path("artifacts")
        shutil.copytree(self.base, artifacts)
        with self.assertRaisesRegex(ValueError, "Release assets differ"):
            release.finalize(artifacts, plan)
        for arch in ("amd64", "arm64"):
            (artifacts / f"agenterm-server-windows-{arch}.exe").write_text("legacy executable")
        release.finalize(artifacts, plan)

    def test_complete_release_and_reused_digests(self):
        self.prepare()
        directory = Path("artifacts")
        shutil.copytree("release-plan/reused", directory)
        for name in release.assets("server"):
            (directory / name).write_text("new bytes: " + name)
        release.finalize(directory, Path("release-plan"))
        hashes = json.loads((Path("release-plan") / release.REUSED_DIGESTS).read_text())
        self.assertEqual(len(list(directory.iterdir())), 11)
        self.assertEqual(set(hashes), set(release.assets("cli")))
        release.verify(directory, list(hashes), hashes)
        for name in ["install.sh", "install.ps1", "SHA256SUMS", release.REUSED_DIGESTS]:
            self.assertFalse((directory / name).exists())
        (directory / release.assets("cli")[0]).write_text("changed after reuse")
        with self.assertRaisesRegex(ValueError, "checksum mismatch"):
            release.finalize(directory, Path("release-plan"))

    def test_incomplete_release_is_not_published(self):
        self.prepare(cli_version="1.2.4")
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
