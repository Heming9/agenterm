"""Plan selective releases and verify reused assets. Uses only Python's stdlib."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess


COMPONENTS = ("cli", "server", "gui")
SCOPES = {
    "all": set(COMPONENTS),
    "server": {"server"},
    "client": set(COMPONENTS),  # Shared client code affects CLI and GUI.
    "cli": {"cli", "server"},  # Server embeds CLI binaries.
    "gui": {"gui"},
}
MANIFEST = "release-manifest.json"
REUSED_DIGESTS = "reused-digests.json"


def assets(component):
    if component == "gui":
        return ["agenterm-client-gui-macos-arm64.zip", "agenterm-client-gui-windows-amd64.exe",
                "agenterm-client-gui-windows-amd64.zip"]
    names = ["components.zip"] if component == "server" else []
    for platform in ("macos", "linux", "windows"):
        for arch in ("amd64", "arm64"):
            prefix = "agenterm-client-cli" if component == "cli" else "agenterm-server"
            stem = f"{prefix}-{platform}-{arch}"
            names.append(stem + (".exe" if platform == "windows" else ""))
            if component == "cli":
                names.append(stem + (".zip" if platform == "windows" else ".tar.gz"))
    return names


def run(*args):
    return subprocess.check_output(args, text=True).strip()


def valid_tag(tag):
    if not re.fullmatch(r"v[0-9][0-9A-Za-z._+-]*", tag):
        raise ValueError(f"Invalid release tag: {tag!r}")
    return tag


def digest(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def asset_digests(release_assets):
    result = {}
    for asset in release_assets:
        match = re.fullmatch(r"sha256:([a-f0-9]{64})", asset.get("digest") or "")
        if match:
            result[asset["name"]] = match[1]
    return result


def verify(directory, names, hashes):
    for name in names:
        if name not in hashes:
            raise ValueError(f"Missing GitHub SHA256 digest: {name}")
        path = directory / name
        if not path.is_file() or hashes.get(name) != digest(path):
            raise ValueError(f"Missing asset or checksum mismatch: {name}")


def base_components(directory, tag, hashes):
    if not (directory / MANIFEST).exists():
        # Releases before selective builds compiled every program at the tag.
        return {c: {"version": tag[1:], "source_tag": tag} for c in COMPONENTS}
    verify(directory, [MANIFEST], hashes)
    manifest = json.loads((directory / MANIFEST).read_text())
    if manifest["schema"] != 1 or manifest["release_tag"] != tag:
        raise ValueError("Unsupported or mismatched release manifest")
    components = manifest["components"]
    for component in COMPONENTS:
        valid_tag(components[component]["source_tag"])
        valid_tag("v" + components[component]["version"])
    return components


def compatible(left, right):
    return subprocess.run(
        ["go", "run", "./scripts/release/check-compat.go", left, right],
        check=False,
    ).returncode == 0


def select_builds(scope, tag, previous):
    selected = SCOPES[scope].copy()
    if previous is None:
        return set(COMPONENTS)
    # Check actual binary versions, including assets reused over multiple releases.
    for component in set(COMPONENTS) - selected:
        if not compatible(tag, previous[component]["version"]):
            print(f"{component} is incompatible with {tag}; rebuilding all programs")
            return set(COMPONENTS)
    return selected


def download(tag, directory, names):
    if not names:
        return
    directory.mkdir(parents=True, exist_ok=True)
    args = ["gh", "release", "download", tag, "--dir", str(directory)]
    for name in names:
        args.extend(["--pattern", name])
    run(*args)


def ancestor(tag):
    return subprocess.run(["git", "merge-base", "--is-ancestor", tag, "HEAD"],
                          stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0


def prepare(scope, requested_base):
    source = Path("internal/version/version.go").read_text()
    tag = valid_tag("v" + re.search(r'^const Current = "([^"]+)"$', source, re.M)[1])
    sha = run("git", "rev-parse", "HEAD")
    if os.environ.get("GITHUB_REF_TYPE") == "tag" and os.environ["GITHUB_REF_NAME"] != tag:
        raise ValueError("Release tag must match internal/version.Current")
    existing = run("git", "tag", "--list", tag)
    if existing and run("git", "rev-list", "-n", "1", tag) != sha:
        raise ValueError("Release tag points to a different commit")
    releases = json.loads(run("gh", "api", "--paginate", "--slurp",
                             f"repos/{os.environ['GH_REPO']}/releases?per_page=100"))
    releases = [release for page in releases for release in page]
    if any(release["tag_name"] == tag for release in releases):
        raise ValueError(f"Release {tag} already exists (including drafts)")
    base = None
    if SCOPES[scope] != set(COMPONENTS):
        candidates = sorted((r for r in releases if not r["draft"] and r["tag_name"] != tag),
                            key=lambda r: r["published_at"], reverse=True)
        for release in candidates:
            candidate = release["tag_name"]
            if requested_base and candidate != requested_base:
                continue
            if re.fullmatch(r"v[0-9][0-9A-Za-z._+-]*", candidate) and ancestor(candidate):
                base = release
                break
        if requested_base and base is None:
            raise ValueError("Base must be a published release whose tag is an ancestor of HEAD")
    previous = None
    cache, output = Path("release-base"), Path("release-plan")
    output.mkdir()
    (output / "reused").mkdir()
    hashes = {}
    if base:
        hashes = asset_digests(base["assets"])
        names = []
        if any(asset["name"] == MANIFEST for asset in base["assets"]):
            names.append(MANIFEST)
        download(base["tag_name"], cache, names)
        previous = base_components(cache, base["tag_name"], hashes)
    selected = select_builds(scope, tag, previous)
    # Naming/layout migrations require rebuilding instead of reusing binaries
    # or archives whose embedded names still follow the old release layout.
    if base and any(name not in {asset["name"] for asset in base["assets"]}
                    for component in set(COMPONENTS) - selected for name in assets(component)):
        print("Base release uses a different asset layout; rebuilding all programs")
        selected = set(COMPONENTS)
    if "gui" in selected:
        config = Path("cmd/agenterm-client-gui/FyneApp.toml").read_text()
        gui_version = re.search(r'^Version = "([^"]+)"$', config, re.M)[1]
        if "v" + gui_version != tag:
            raise ValueError("GUI version must match internal/version.Current")
    manifest = {"schema": 1, "release_tag": tag, "commit": sha, "scope": scope,
                "base_release": base["tag_name"] if base else None, "components": {}}
    reused = []
    for component in COMPONENTS:
        if component in selected:
            manifest["components"][component] = {"version": tag[1:], "source_tag": tag}
        else:
            manifest["components"][component] = previous[component]
            reused.extend(assets(component))
    if reused:
        download(base["tag_name"], cache, reused)
        verify(cache, reused, hashes)
        for name in reused:
            shutil.copyfile(cache / name, output / "reused" / name)
    (output / MANIFEST).write_text(json.dumps(manifest, indent=2) + "\n")
    # Retain GitHub's expected digests for the publish job, not as release assets.
    (output / REUSED_DIGESTS).write_text(json.dumps({name: hashes[name] for name in reused}) + "\n")
    notes = [f"Agenterm {tag} preview release.", "",
             "| Program | Action | Binary version | Source tag |",
             "| --- | --- | --- | --- |"]
    for component, info in manifest["components"].items():
        action = "Built" if component in selected else "Reused"
        notes.append(f"| {component} | {action} | {info['version']} | {info['source_tag']} |")
    notes += ["", "Reused programs retain their original version. Installation scripts and download checksums are served by the Server."]
    (output / "notes.md").write_text("\n".join(notes) + "\n")
    if os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(os.environ["GITHUB_STEP_SUMMARY"], "a") as stream:
            stream.write("\n".join(notes) + "\n")
    with open(os.environ.get("GITHUB_OUTPUT", os.devnull), "a") as stream:
        stream.write(f"tag={tag}\nsha={sha}\n")
        for component in COMPONENTS:
            stream.write(f"{component}={str(component in selected).lower()}\n")


def finalize(directory, plan):
    manifest = json.loads((plan / MANIFEST).read_text())
    # Ensure artifacts advertised as reused are still byte-for-byte identical.
    reused_hashes = json.loads((plan / REUSED_DIGESTS).read_text())
    verify(directory, list(reused_hashes), reused_hashes)
    shutil.copyfile(plan / MANIFEST, directory / MANIFEST)
    expected = {MANIFEST}
    for component in COMPONENTS:
        expected.update(assets(component))
    actual = {path.name for path in directory.iterdir()}
    if actual != expected:
        raise ValueError(f"Release assets differ: missing={expected - actual}, extra={actual - expected}")
    for name in expected:
        if (directory / name).stat().st_size == 0:
            raise ValueError(f"Empty release asset: {name}")
    print(f"Verified {manifest['release_tag']}: {len(expected)} assets")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)
    planner = subparsers.add_parser("prepare")
    planner.add_argument("--scope", choices=SCOPES, default="all")
    planner.add_argument("--base", default="")
    publisher = subparsers.add_parser("finalize")
    publisher.add_argument("--directory", type=Path, default=Path("artifacts"))
    publisher.add_argument("--plan", type=Path, default=Path("release-plan"))
    args = parser.parse_args()
    if args.command == "prepare":
        prepare(args.scope, args.base)
    else:
        finalize(args.directory, args.plan)
