#!/usr/bin/env python3
"""Build/package native p2p-cli archives for the unified Server component bundle."""
import argparse
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import platform
import re
import shutil
import subprocess
import tarfile
import tempfile
import tomllib
import zipfile


MAX_DOWNLOAD_BYTES = 2 * 1024 * 1024


def check_download_size(os_name, size):
    if os_name in ("darwin", "windows") and size > MAX_DOWNLOAD_BYTES:
        raise SystemExit(f"p2p-cli download is {size} bytes, exceeding the 2 MiB limit")


def merge_assets(directories, output):
    entries = {}
    version = None
    for directory in directories:
        directory = Path(directory).resolve()
        manifest = json.loads((directory / "manifest.json").read_text())
        if version is not None and version != manifest["version"]:
            raise SystemExit("Refusing to merge different component versions")
        version = manifest["version"]
        for artifact in manifest["artifacts"]:
            name = artifact["file"]
            if not re.fullmatch(r"p2p-cli-(macos|linux)-(amd64|arm64)\.tar\.gz|p2p-cli-windows-(amd64|arm64)\.zip", name):
                raise SystemExit("Invalid component archive name")
            source = directory / name
            check_download_size(artifact["platform"], source.stat().st_size)
            if hashlib.sha256(source.read_bytes()).hexdigest() != artifact["sha256"]:
                raise SystemExit("Component archive checksum mismatch")
            key = artifact["platform"], artifact["arch"]
            if key in entries:
                raise SystemExit("Duplicate component platform")
            entries[key] = (artifact, source)
    output.mkdir(parents=True, exist_ok=True)
    for artifact, source in entries.values():
        shutil.copyfile(source, output / artifact["file"])
    manifest = dict(version=version, artifacts=[entries[key][0] for key in sorted(entries)])
    (output / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    print(json.dumps(dict(manifest=str(output / "manifest.json"), platforms=len(entries))))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", default="dist/p2p-assets")
    parser.add_argument("--binary", help="Package an already built native executable")
    parser.add_argument("--merge", nargs="+", metavar="ASSET_DIRECTORY", help="Combine verified native build outputs")
    args = parser.parse_args()
    if args.merge:
        merge_assets(args.merge, Path(args.output).resolve())
        return
    root = Path(__file__).resolve().parent.parent
    component = root / "tools/p2p-cli"
    os_name = {"Darwin": "darwin", "Linux": "linux", "Windows": "windows"}[platform.system()]
    arch = {"x86_64": "amd64", "AMD64": "amd64", "aarch64": "arm64", "arm64": "arm64", "ARM64": "arm64"}[platform.machine()]
    version = tomllib.loads((component / "Cargo.toml").read_text())["package"]["version"]
    exe = "p2p-cli.exe" if os_name == "windows" else "p2p-cli"
    if args.binary:
        binary = Path(args.binary).resolve()
    else:
        subprocess.run([os.environ.get("CARGO", "cargo"), "build", "--release", "--locked", "--manifest-path", str(component / "Cargo.toml")], check=True)
        binary = component / "target/release" / exe
    data = binary.read_bytes()
    output = Path(args.output).resolve()
    output.mkdir(parents=True, exist_ok=True)
    name = f'p2p-cli-{"macos" if os_name == "darwin" else os_name}-{arch}'
    if os_name == "windows":
        name += ".zip"
        blob = io.BytesIO()
        with zipfile.ZipFile(blob, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as archive:
            entry = zipfile.ZipInfo(exe, (1980, 1, 1, 0, 0, 0))
            entry.compress_type = zipfile.ZIP_DEFLATED
            entry.external_attr = 0o100755 << 16
            archive.writestr(entry, data, compresslevel=9)
        payload = blob.getvalue()
    else:
        name += ".tar.gz"
        blob = io.BytesIO()
        with gzip.GzipFile(fileobj=blob, mode="wb", filename="", mtime=0, compresslevel=9) as compressed:
            with tarfile.open(fileobj=compressed, mode="w") as archive:
                entry = tarfile.TarInfo(exe)
                entry.size, entry.mode = len(data), 0o755
                archive.addfile(entry, io.BytesIO(data))
        payload = blob.getvalue()
    check_download_size(os_name, len(payload))
    def publish(path, content):
        with tempfile.NamedTemporaryFile(dir=output, delete=False) as file:
            file.write(content)
            temp = Path(file.name)
        temp.replace(path)
    manifest_path = output / "manifest.json"
    manifest = json.loads(manifest_path.read_text()) if manifest_path.exists() else dict(version=version, artifacts=[])
    if manifest["version"] != version:
        raise SystemExit("Refusing to mix component versions in one manifest")
    publish(output / name, payload)
    manifest["artifacts"] = [a for a in manifest["artifacts"] if (a["platform"], a["arch"]) != (os_name, arch)]
    manifest["artifacts"].append(dict(platform=os_name, arch=arch, file=name, sha256=hashlib.sha256(payload).hexdigest()))
    manifest["artifacts"].sort(key=lambda a: (a["platform"], a["arch"]))
    publish(manifest_path, (json.dumps(manifest, indent=2) + "\n").encode())
    print(json.dumps(dict(manifest=str(manifest_path), archive=name, bytes=len(payload), binary_bytes=len(data))))


if __name__ == "__main__":
    main()
