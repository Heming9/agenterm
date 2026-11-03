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
import shutil
import struct
import subprocess
import tarfile
import tempfile
import tomllib
import zipfile


MAX_DOWNLOAD_BYTES = 2 * 1024 * 1024
PLATFORMS = {(system, arch) for system in ('darwin', 'linux', 'windows') for arch in ('amd64', 'arm64')}
TARGETS = {
    'x86_64-apple-darwin': ('darwin', 'amd64'),
    'aarch64-apple-darwin': ('darwin', 'arm64'),
    'x86_64-unknown-linux-musl': ('linux', 'amd64'),
    'aarch64-unknown-linux-musl': ('linux', 'arm64'),
    'x86_64-pc-windows-msvc': ('windows', 'amd64'),
    'aarch64-pc-windows-msvc': ('windows', 'arm64'),
}


def archive_name(system, arch):
    if (system, arch) not in PLATFORMS:
        raise ValueError('Unsupported component platform')
    platform_name = 'macos' if system == 'darwin' else system
    return f'p2p-cli-{platform_name}-{arch}' + ('.zip' if system == 'windows' else '.tar.gz')


def verify_binary(data, system, arch):
    """Reject mislabeled cross builds before they enter a distribution."""
    actual = None
    if len(data) >= 64 and data[:6] == b'\x7fELF\x02\x01':
        actual = ('linux', {62: 'amd64', 183: 'arm64'}.get(struct.unpack_from('<H', data, 18)[0]))
        offset = struct.unpack_from('<Q', data, 32)[0]
        entry_size, count = struct.unpack_from('<HH', data, 54)
        if entry_size < 56 or not 0 < count <= 1024 or offset + entry_size * count > len(data):
            raise ValueError('Invalid ELF program headers')
        if any(struct.unpack_from('<I', data, offset + i * entry_size)[0] == 3 for i in range(count)):
            raise ValueError('Linux downloads must be statically linked (no PT_INTERP)')
    elif len(data) >= 8 and data[:4] == b'\xcf\xfa\xed\xfe':
        actual = ('darwin', {0x1000007: 'amd64', 0x100000c: 'arm64'}.get(struct.unpack_from('<I', data, 4)[0]))
    elif len(data) >= 64 and data[:2] == b'MZ':
        offset = struct.unpack_from('<I', data, 60)[0]
        if offset + 6 <= len(data) and data[offset:offset + 4] == b'PE\0\0':
            actual = ('windows', {0x8664: 'amd64', 0xaa64: 'arm64'}.get(struct.unpack_from('<H', data, offset + 4)[0]))
    if actual != (system, arch):
        raise ValueError(f'Executable platform {actual} does not match {system}/{arch}')


def check_download_size(os_name, size):
    if os_name in ("darwin", "windows") and size > MAX_DOWNLOAD_BYTES:
        raise SystemExit(f"p2p-cli download is {size} bytes, exceeding the 2 MiB limit")


def fit_download_size(system, payload):
    # Intel macOS needs a stronger DEFLATE encoder to retain the 2 MiB budget.
    # This changes only the gzip encoding; downloaded bytes remain standard gzip.
    if system == 'darwin' and len(payload) > MAX_DOWNLOAD_BYTES:
        encoder = shutil.which('advdef')
        if encoder is None:
            raise SystemExit('Install advancecomp (brew install advancecomp) to fit the macOS download within 2 MiB')
        with tempfile.TemporaryDirectory(prefix='p2p-compress-') as directory:
            archive = Path(directory) / 'p2p-cli.tar.gz'
            archive.write_bytes(payload)
            subprocess.run([encoder, '-q', '-z', '-4', '-i', '15', str(archive)], check=True)
            candidate = archive.read_bytes()
            if gzip.decompress(candidate) != gzip.decompress(payload):
                raise ValueError('Recompression changed the archive contents')
            payload = candidate
    check_download_size(system, len(payload))
    return payload


def merge_assets(directories, output, require_all=False):
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
            if name != archive_name(artifact['platform'], artifact['arch']):
                raise SystemExit("Invalid component archive name")
            source = directory / name
            check_download_size(artifact["platform"], source.stat().st_size)
            if hashlib.sha256(source.read_bytes()).hexdigest() != artifact["sha256"]:
                raise SystemExit("Component archive checksum mismatch")
            key = artifact["platform"], artifact["arch"]
            if key in entries:
                raise SystemExit("Duplicate component platform")
            entries[key] = (artifact, source)
    if not entries:
        raise SystemExit('No component archives to merge')
    if require_all and entries.keys() != PLATFORMS:
        missing = ', '.join(f'{system}/{arch}' for system, arch in sorted(PLATFORMS - entries.keys()))
        raise SystemExit('Missing P2P platforms: ' + missing)
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
    parser.add_argument('--target', choices=sorted(TARGETS), help='Explicit Rust target for native or cross builds')
    parser.add_argument("--merge", nargs="+", metavar="ASSET_DIRECTORY", help="Combine verified native build outputs")
    parser.add_argument('--require-all', action='store_true', help='Reject a merged distribution missing any of the six platforms')
    args = parser.parse_args()
    if args.merge:
        if args.target or args.binary:
            parser.error('--merge cannot be combined with --target or --binary')
        merge_assets(args.merge, Path(args.output).resolve(), require_all=args.require_all)
        return
    if args.require_all:
        parser.error('--require-all requires --merge')
    root = Path(__file__).resolve().parent.parent
    component = root / "tools/p2p-cli"
    os_name = {"Darwin": "darwin", "Linux": "linux", "Windows": "windows"}[platform.system()]
    arch = {"x86_64": "amd64", "AMD64": "amd64", "aarch64": "arm64", "arm64": "arm64", "ARM64": "arm64"}[platform.machine()]
    if args.target:
        os_name, arch = TARGETS[args.target]
    elif os_name == 'linux':
        args.target = next(target for target, identity in TARGETS.items() if identity == (os_name, arch))
    version = tomllib.loads((component / "Cargo.toml").read_text())["package"]["version"]
    exe = "p2p-cli.exe" if os_name == "windows" else "p2p-cli"
    if args.binary:
        binary = Path(args.binary).resolve()
    else:
        command = [os.environ.get('CARGO', 'cargo'), 'build', '--release', '--locked', '--manifest-path', str(component / 'Cargo.toml')]
        if args.target:
            command += ['--target', args.target]
        env = os.environ.copy()
        if os_name == 'windows':
            env['RUSTFLAGS'] = env.get('RUSTFLAGS', '') + ' -C target-feature=+crt-static'
        subprocess.run(command, env=env, check=True)
        target_dir = Path(os.environ.get('CARGO_TARGET_DIR', str(component / 'target'))).resolve()
        binary = target_dir / args.target / 'release' / exe if args.target else target_dir / 'release' / exe
    data = binary.read_bytes()
    verify_binary(data, os_name, arch)
    output = Path(args.output).resolve()
    output.mkdir(parents=True, exist_ok=True)
    name = archive_name(os_name, arch)
    if os_name == "windows":
        blob = io.BytesIO()
        with zipfile.ZipFile(blob, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as archive:
            entry = zipfile.ZipInfo(exe, (1980, 1, 1, 0, 0, 0))
            entry.compress_type = zipfile.ZIP_DEFLATED
            entry.external_attr = 0o100755 << 16
            archive.writestr(entry, data, compresslevel=9)
        payload = blob.getvalue()
    else:
        blob = io.BytesIO()
        with gzip.GzipFile(fileobj=blob, mode="wb", filename="", mtime=0, compresslevel=9) as compressed:
            with tarfile.open(fileobj=compressed, mode="w") as archive:
                entry = tarfile.TarInfo(exe)
                entry.size, entry.mode = len(data), 0o755
                archive.addfile(entry, io.BytesIO(data))
        payload = blob.getvalue()
    payload = fit_download_size(os_name, payload)
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
