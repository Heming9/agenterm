#!/usr/bin/env python3
"""Pack Rust CLI executables and native P2P binaries into components.tar.xz."""
import argparse
import json
import lzma
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import tarfile
import hashlib

from build_p2p_cli import merge_assets
from build_server import read_manifest
from client_bundle_provenance import BUNDLE_PATH, CLIENT_BINARIES, P2P_MANIFEST, build_record, package_version, verify_builds


def native_env():
    return {k: v for k, v in os.environ.items() if k not in ('GOOS', 'GOARCH', 'GOARM', 'GOAMD64', 'CGO_ENABLED')}


def reuse_p2p(bundle, directory):
    # Copy verified raw native binaries; never extract arbitrary tar paths.
    from build_p2p_cli import binary_name
    manifest = read_manifest(bundle)
    artifacts = []
    with tarfile.open(bundle, 'r:xz') as archive:
        for system in ('darwin', 'linux', 'windows'):
            for arch in ('amd64', 'arm64'):
                name = binary_name(system, arch)
                data = archive.extractfile('p2p-cli/' + name).read()
                (directory / name).write_bytes(data)
                artifacts.append(dict(platform=system, arch=arch, file=name, sha256=hashlib.sha256(data).hexdigest()))
    (directory / 'manifest.json').write_text(json.dumps(dict(version=manifest['p2p_version'], artifacts=artifacts), indent=2) + '\n')


def matches_bundle(manifest, client_version, p2p_version, hashes):
    if (manifest['schema'], manifest.get('client_version'), manifest['p2p_version']) != (3, client_version, p2p_version):
        return False
    return {name: entry['sha256'] for name, entry in manifest['files'].items()} == hashes


def reusable_bundle(path, client_version, p2p_version, hashes):
    try:
        manifest = read_manifest(path)
    except (OSError, ValueError, KeyError, tarfile.TarError, lzma.LZMAError):
        return False
    return matches_bundle(manifest, client_version, p2p_version, hashes)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, help='Defaults to the archive embedded by ordinary Go builds')
    parser.add_argument('--client-dir', required=True, type=Path, help='Directory containing all six native Rust CLI executables (scripts/build_client.py)')
    parser.add_argument('--p2p-assets', nargs='+', type=Path, help='Native build outputs; omitted reuses the tracked bundle')
    parser.add_argument('--download-output', type=Path, help='Also keep the exact public CLI downloads here')
    args = parser.parse_args()
    root = Path(__file__).resolve().parent.parent
    embedded = root / BUNDLE_PATH
    output = args.output.resolve() if args.output else embedded
    p2p_version = package_version(root, P2P_MANIFEST)
    output.parent.mkdir(parents=True, exist_ok=True)
    bootstrap = not embedded.exists()
    if bootstrap:
        embedded.parent.mkdir(parents=True, exist_ok=True)
        embedded.touch()
    try:
        with tempfile.TemporaryDirectory(prefix='agenterm-components-') as temporary:
            work = Path(temporary)
            peers = work / 'peers'
            clients = work / 'clients'
            clients.mkdir()
            for name in CLIENT_BINARIES:
                binary = args.client_dir / name
                shutil.copyfile(binary, clients / name)
                shutil.copyfile(build_record(binary), build_record(clients / name))
            client_version, hashes = verify_builds(root, clients)
            if args.p2p_assets:
                merge_assets(args.p2p_assets, peers, require_all=True)
            else:
                peers.mkdir()
                reuse_p2p(embedded, peers)
            p2p = json.loads((peers / 'manifest.json').read_text())
            if p2p['version'] != p2p_version:
                raise ValueError(f'P2P CLI {p2p_version} is required. Build it and pass --p2p-assets')
            hashes.update(('p2p-cli/' + asset['file'], asset['sha256']) for asset in p2p['artifacts'])
            reuse = reusable_bundle(output, client_version, p2p_version, hashes)
            packer = ['go', 'run', './cmd/agenterm-pack', '-input', str(clients)]
            if not reuse:
                descriptor, name = tempfile.mkstemp(prefix='.components-', suffix='.tar.xz', dir=output.parent)
                os.close(descriptor)
                pending = Path(name)
                try:
                    subprocess.run([*packer, '-output', '', '-p2p-assets', str(peers), '-bundle', str(pending),
                                    '-client-version', client_version], cwd=root, env=native_env(), check=True)
                    # Use liblzma's stronger build-time encoder for the solid bundle.
                    raw_tar = lzma.decompress(pending.read_bytes())
                    pending.write_bytes(lzma.compress(raw_tar, preset=9))
                    if not matches_bundle(read_manifest(pending), client_version, p2p_version, hashes):
                        raise ValueError('Component binaries changed during packing')
                    pending.replace(output)
                finally:
                    pending.unlink(missing_ok=True)
            if args.download_output:
                subprocess.run([*packer, '-bundle', '', '-output', str(args.download_output.resolve())],
                               cwd=root, env=native_env(), check=True)
        print(json.dumps(dict(components=str(output), bytes=output.stat().st_size, reused=reuse)), flush=True)
    except BaseException:
        if bootstrap:
            embedded.unlink(missing_ok=True)
        raise


if __name__ == '__main__':
    main()
