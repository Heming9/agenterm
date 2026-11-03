#!/usr/bin/env python3
"""Build Client CLI downloads and merge native P2P outputs into components.zip."""
import argparse
import concurrent.futures
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import zipfile

from build_p2p_cli import merge_assets


def native_env():
    return {k: v for k, v in os.environ.items() if k not in ('GOOS', 'GOARCH', 'GOARM', 'GOAMD64', 'CGO_ENABLED')}


def build_clients(root, destination):
    def build(target):
        goos, arch = target
        platform = 'macos' if goos == 'darwin' else goos
        name = f"agenterm-client-cli-{platform}-{arch}" + ('.exe' if goos == 'windows' else '')
        print('Building ' + name, flush=True)
        subprocess.run(['go', 'build', '-trimpath', '-gcflags=all=-l', '-ldflags', '-s -w',
                        '-o', str(destination / name), './cmd/agenterm-client-cli'], cwd=root,
                       env=dict(native_env(), GOOS=goos, GOARCH=arch, CGO_ENABLED='0'), check=True)
    targets = [(platform, arch) for platform in ('darwin', 'linux', 'windows') for arch in ('amd64', 'arm64')]
    with concurrent.futures.ThreadPoolExecutor(max_workers=3) as executor:
        list(executor.map(build, targets))


def reuse_p2p(bundle, directory):
    # Only select manifest-named basenames; never extract arbitrary ZIP paths.
    with zipfile.ZipFile(bundle) as archive:
        raw = archive.read('p2p-cli/manifest.json')
        manifest = json.loads(raw)
        for artifact in manifest['artifacts']:
            name = artifact['file']
            if Path(name).name != name or '\\' in name:
                raise ValueError('Invalid P2P filename')
            (directory / name).write_bytes(archive.read('p2p-cli/' + name))
        (directory / 'manifest.json').write_bytes(raw)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, help='Defaults to the archive embedded by ordinary Go builds')
    parser.add_argument('--client-dir', type=Path, help='Reuse six prebuilt raw Client CLI executables')
    parser.add_argument('--p2p-assets', nargs='+', type=Path, help='Native build outputs; omitted reuses the tracked bundle')
    parser.add_argument('--download-output', type=Path, help='Also keep the exact public CLI downloads here')
    args = parser.parse_args()
    root = Path(__file__).resolve().parent.parent
    embedded = root / 'internal/components/assets/components.zip'
    output = args.output.resolve() if args.output else embedded
    output.parent.mkdir(parents=True, exist_ok=True)
    bootstrap = not embedded.exists()
    if bootstrap:
        embedded.parent.mkdir(parents=True, exist_ok=True)
        with zipfile.ZipFile(embedded, 'w'):
            pass
    try:
        with tempfile.TemporaryDirectory(prefix='agenterm-components-') as temporary:
            work = Path(temporary)
            raw, peers, merged, downloads = (work / name for name in ('raw', 'peers', 'merged', 'downloads'))
            for path in (raw, peers, merged, downloads):
                path.mkdir()
            if args.p2p_assets:
                merge_assets(args.p2p_assets, peers)
            else:
                reuse_p2p(embedded, peers)
            merge_assets([peers], merged)
            clients = args.client_dir.resolve() if args.client_dir else raw
            if args.client_dir is None:
                build_clients(root, clients)
            descriptor, name = tempfile.mkstemp(prefix='.components-', suffix='.zip', dir=output.parent)
            os.close(descriptor)
            pending = Path(name)
            try:
                subprocess.run(['go', 'run', './cmd/agenterm-pack', '-input', str(clients),
                                '-output', str(downloads), '-p2p-assets', str(merged), '-bundle', str(pending)],
                               cwd=root, env=native_env(), check=True)
                pending.replace(output)
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
