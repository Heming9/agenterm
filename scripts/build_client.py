#!/usr/bin/env python3
"""Build a Rust CLI for a native runner using the public download filename."""
import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess


TARGETS = {
    'x86_64-apple-darwin': ('macos', 'amd64'),
    'aarch64-apple-darwin': ('macos', 'arm64'),
    'x86_64-unknown-linux-gnu': ('linux', 'amd64'),
    'aarch64-unknown-linux-gnu': ('linux', 'arm64'),
    'x86_64-pc-windows-msvc': ('windows', 'amd64'),
    'aarch64-pc-windows-msvc': ('windows', 'arm64'),
}


def binary_name(target):
    system, arch = TARGETS[target]
    return f'agenterm-client-cli-{system}-{arch}' + ('.exe' if system == 'windows' else '')


def build(root, output, target):
    component = root / 'components/agenterm-cli'
    manifest = component / 'Cargo.toml'
    env = os.environ.copy()
    if TARGETS[target][0] == 'windows':
        # Keep downloads independent of separately installed MSVC runtimes.
        flags = env.get('CARGO_ENCODED_RUSTFLAGS')
        if flags is None:
            import shlex
            flags = '\x1f'.join(shlex.split(env.get('RUSTFLAGS', '')))
        env['CARGO_ENCODED_RUSTFLAGS'] = '\x1f'.join(filter(None, (
            flags, '-C', 'target-feature=+crt-static')))
    subprocess.run(['cargo', 'build', '--release', '--locked', '--target', target,
                    '--manifest-path', str(manifest)], cwd=root, env=env, check=True)
    metadata = json.loads(subprocess.check_output([
        'cargo', 'metadata', '--no-deps', '--format-version', '1',
        '--manifest-path', str(manifest)], cwd=root, env=env, text=True))
    extension = '.exe' if TARGETS[target][0] == 'windows' else ''
    source = Path(metadata['target_directory']) / target / 'release' / ('agenterm-cli' + extension)
    output.mkdir(parents=True, exist_ok=True)
    destination = output / binary_name(target)
    shutil.copy2(source, destination)
    return destination


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--target', choices=TARGETS, help='Rust target; defaults to the rustc host')
    parser.add_argument('--output', type=Path, default=Path('dist'))
    args = parser.parse_args()
    target = args.target
    if target is None:
        target = next(line.removeprefix('host: ') for line in
                      subprocess.check_output(['rustc', '-vV'], text=True).splitlines()
                      if line.startswith('host: '))
    if target not in TARGETS:
        parser.error(f'Unsupported CLI target: {target}')
    root = Path(__file__).resolve().parent.parent
    destination = build(root, args.output.resolve(), target)
    print(json.dumps(dict(file=str(destination), bytes=destination.stat().st_size)))


if __name__ == '__main__':
    main()
