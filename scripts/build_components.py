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


def native_env():
    return {k: v for k, v in os.environ.items() if k not in ('GOOS', 'GOARCH', 'GOARM', 'GOAMD64', 'CGO_ENABLED')}


def reuse_p2p(bundle, directory):
    # Copy verified raw native binaries; never extract arbitrary tar paths.
    from build_server import read_manifest
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


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, help='Defaults to the archive embedded by ordinary Go builds')
    parser.add_argument('--client-dir', required=True, type=Path, help='Directory containing all six native Rust CLI executables (scripts/build_client.py)')
    parser.add_argument('--client-version', help='Version of client binaries (defaults to Server version; compatible patch versions allowed)')
    parser.add_argument('--p2p-assets', nargs='+', type=Path, help='Native build outputs; omitted reuses the tracked bundle')
    parser.add_argument('--download-output', type=Path, help='Also keep the exact public CLI downloads here')
    args = parser.parse_args()
    root = Path(__file__).resolve().parent.parent
    embedded = root / 'internal/components/assets/components.tar.xz'
    output = args.output.resolve() if args.output else embedded
    from client_bundle_provenance import current_version, source_digest
    client_version = args.client_version or current_version(root)
    import re
    if not re.fullmatch(r'\d+\.\d+\.\d+', client_version) or client_version.split('.')[:2] != current_version(root).split('.')[:2]:
        raise ValueError('Client version is incompatible with Server version')
    for path in args.client_dir.glob('agenterm-client-cli-*'):
        if path.is_file() and client_version.encode() not in path.read_bytes():
            raise ValueError(f'Client version marker missing in {path.name}')
    output.parent.mkdir(parents=True, exist_ok=True)
    bootstrap = not embedded.exists()
    if bootstrap:
        embedded.parent.mkdir(parents=True, exist_ok=True)
        embedded.touch()
    try:
        with tempfile.TemporaryDirectory(prefix='agenterm-components-') as temporary:
            work = Path(temporary)
            peers, merged, downloads = (work / name for name in ('peers', 'merged', 'downloads'))
            for path in (peers, merged, downloads):
                path.mkdir()
            if args.p2p_assets:
                merge_assets(args.p2p_assets, peers)
            else:
                reuse_p2p(embedded, peers)
            merge_assets([peers], merged, require_all=True)
            clients = args.client_dir.resolve()
            descriptor, name = tempfile.mkstemp(prefix='.components-', suffix='.tar.xz', dir=output.parent)
            os.close(descriptor)
            pending = Path(name)
            try:
                subprocess.run(['go', 'run', './cmd/agenterm-pack', '-input', str(clients),
                                '-output', str(downloads), '-p2p-assets', str(merged), '-bundle', str(pending)],
                               cwd=root, env=native_env(), check=True)
                # Use liblzma's stronger build-time encoder for the solid bundle.
                # Runtime downloads keep the shared Go encoding pinned by its manifest.
                raw_tar = lzma.decompress(pending.read_bytes())
                pending.write_bytes(lzma.compress(raw_tar, preset=9))
                pending.replace(output)
                if output == embedded:
                    from build_server import read_manifest
                    manifest = read_manifest(output)
                    files = {k: v['sha256'] for k, v in manifest['files'].items() if k.startswith('client-cli/')}
                    lock = dict(client_version=client_version, source_sha256=source_digest(root), files=files)
                    (embedded.parent / 'client-provenance.json').write_text(json.dumps(lock, indent=2, sort_keys=True) + '\n')
            finally:
                pending.unlink(missing_ok=True)
            if args.download_output:
                args.download_output.mkdir(parents=True, exist_ok=True)
                for path in downloads.iterdir():
                    shutil.copy2(path, args.download_output / path.name)
        print(json.dumps(dict(components=str(output), bytes=output.stat().st_size)), flush=True)
    except BaseException:
        if bootstrap:
            embedded.unlink(missing_ok=True)
        raise


if __name__ == '__main__':
    main()
