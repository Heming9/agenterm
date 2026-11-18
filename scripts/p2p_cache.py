"""Reuse verified native P2P artifacts for the declared version and target."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import sys

from build_p2p_cli import TARGETS, binary_name, verify_binary
from client_bundle_provenance import P2P_MANIFEST, package_version


def identity(root, target):
    if target not in TARGETS:
        raise ValueError('Unsupported native target')
    return dict(schema=2, target=target, version=package_version(root, P2P_MANIFEST))


def cache_key(value):
    return 'p2p-native-v2-{target}-v{version}'.format(**value)


def checked_assets(root, directory, target):
    manifest = json.loads((directory / 'manifest.json').read_text())
    version = package_version(root, P2P_MANIFEST)
    system, arch = TARGETS[target]
    name = binary_name(system, arch)
    if not isinstance(manifest, dict) or manifest.get('version') != version:
        raise ValueError('Native artifact version or inventory differs')
    artifacts = manifest.get('artifacts')
    if not isinstance(artifacts, list) or len(artifacts) != 1 or not isinstance(artifacts[0], dict):
        raise ValueError('Native artifact version or inventory differs')
    artifact = artifacts[0]
    if (artifact.get('platform'), artifact.get('arch'), artifact.get('file')) != (system, arch, name):
        raise ValueError('Native artifact target differs')
    path = directory / name
    if path.is_symlink():
        raise ValueError('Native cache must contain a regular executable')
    payload = path.read_bytes()
    if hashlib.sha256(payload).hexdigest() != artifact.get('sha256'):
        raise ValueError('Native cache checksum differs')
    verify_binary(payload, system, arch)
    return name, payload, manifest


def copy_assets(directory, name, payload, manifest):
    directory.mkdir(parents=True, exist_ok=True)
    (directory / name).write_bytes(payload)
    (directory / name).chmod(0o755)
    (directory / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')


def store(root, directory, assets, value):
    name, payload, manifest = checked_assets(root, assets, value['target'])
    copy_assets(directory / 'assets', name, payload, manifest)
    (directory / 'identity.json').write_text(json.dumps(value, sort_keys=True) + '\n')


def restore(root, directory, assets, binary, value):
    try:
        if json.loads((directory / 'identity.json').read_text()) != value:
            raise ValueError('Native cache version or target differs')
        name, payload, manifest = checked_assets(root, directory / 'assets', value['target'])
    except (OSError, ValueError, KeyError, TypeError) as error:
        print(f'Native cache rejected; rebuilding: {error}', file=sys.stderr)
        return False
    # Validate everything before making the cached bytes available to tests.
    copy_assets(assets, name, payload, manifest)
    binary.parent.mkdir(parents=True, exist_ok=True)
    binary.write_bytes(payload)
    binary.chmod(0o755)
    return True


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=('key', 'restore', 'store'))
    parser.add_argument('--root', type=Path, default=Path('.'))
    parser.add_argument('--target', required=True, choices=sorted(TARGETS))
    parser.add_argument('--rust', help=argparse.SUPPRESS)
    parser.add_argument('--runner', help=argparse.SUPPRESS)
    parser.add_argument('--cache-dir', type=Path, default=Path('.p2p-native-cache'))
    parser.add_argument('--assets', type=Path, default=Path('dist/p2p-assets'))
    parser.add_argument('--binary', type=Path)
    args = parser.parse_args()
    value = identity(args.root.resolve(), args.target)
    if args.command == 'key':
        print(cache_key(value))
    elif args.command == 'store':
        store(args.root, args.cache_dir, args.assets, value)
    else:
        if args.binary is None:
            parser.error('--binary is required to restore a native executable')
        usable = restore(args.root, args.cache_dir, args.assets, args.binary, value)
        output = f'usable={str(usable).lower()}\n'
        if os.environ.get('GITHUB_OUTPUT'):
            with open(os.environ['GITHUB_OUTPUT'], 'a') as stream:
                stream.write(output)
        print(output, end='')


if __name__ == '__main__':
    main()
