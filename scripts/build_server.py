#!/usr/bin/env python3
"""Build the single Server executable embedding the unified components.tar.xz."""
import argparse
from contextlib import contextmanager
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import tempfile
import time



def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def read_manifest(bundle):
    with tarfile.open(bundle, 'r:xz') as archive:
        entries = archive.getmembers()
        names = {entry.name for entry in entries}
        if len(entries) != 13 or len(names) != len(entries):
            raise ValueError('Invalid or duplicate components tar entries')
        for entry in entries:
            if not entry.isfile() or entry.linkname or not 0 < entry.size <= 64 << 20:
                raise ValueError('Invalid component tar entry')
        if archive.getmember('manifest.json').size > 64 << 10:
            raise ValueError('Components manifest too large')
        manifest = json.loads(archive.extractfile('manifest.json').read())
        if manifest['schema'] != 2 or names != set(manifest['files']) | {'manifest.json'}:
            raise ValueError('Invalid unified components manifest')
        if not re.fullmatch(r'[0-9A-Za-z][0-9A-Za-z._-]{0,63}', manifest['p2p_version']):
            raise ValueError('Invalid P2P version')
        total = 0
        for name, info in manifest['files'].items():
            native = re.fullmatch(r'(client-cli/agenterm-client-cli|p2p-cli/p2p-cli)-(macos|linux|windows)-(amd64|arm64)(\.exe)?', name)
            if not native or ('-windows-' in name) != name.endswith('.exe'):
                raise ValueError('Invalid component path')
            entry = archive.getmember(name)
            if type(info['bytes']) is not int or not 0 < info['bytes'] <= 64 << 20 or entry.size != info['bytes']:
                raise ValueError('Invalid component entry')
            total += info['bytes']
            if total > 512 << 20 or not re.fullmatch(r'[0-9a-f]{64}', info['sha256']):
                raise ValueError('Invalid component size or checksum')
            if hashlib.sha256(archive.extractfile(entry).read()).hexdigest() != info['sha256']:
                raise ValueError('Component checksum mismatch')
    return manifest


@contextmanager
def cache_lock(path):
    with path.open('a+b') as lock:
        if os.name == 'nt':
            import msvcrt
            lock.seek(0, os.SEEK_END)
            if lock.tell() == 0:
                lock.write(b'\0')
                lock.flush()
            lock.seek(0)
            msvcrt.locking(lock.fileno(), msvcrt.LK_LOCK, 1)
        else:
            import fcntl
            fcntl.flock(lock.fileno(), fcntl.LOCK_EX)
        try:
            yield
        finally:
            if os.name == 'nt':
                lock.seek(0)
                msvcrt.locking(lock.fileno(), msvcrt.LK_UNLCK, 1)
            else:
                fcntl.flock(lock.fileno(), fcntl.LOCK_UN)


def prepare(root, cache):
    manifest = read_manifest(root / 'internal/components/assets/components.tar.xz')
    cache.mkdir(parents=True, exist_ok=True, mode=0o700)
    cache.chmod(0o700)
    # Protect publication, prepared download copies and the auxiliary raw CLI
    # as one operation; a parallel build must never remove files being copied.
    with cache_lock(cache / '.lock'):
        return prepare_locked(root, cache, manifest)


def prepare_locked(root, cache, manifest):
    bundle = root / 'internal/components/assets/components.tar.xz'
    cached = cache / 'components' / digest(bundle)
    expected = manifest['files']
    hit = cached.is_dir() and all((cached / name).is_file() and digest(cached / name) == info['sha256']
                                  for name, info in expected.items())
    sums = ''.join(f"{info['sha256']}  {name.split('/')[1]}\n" for name, info in sorted(expected.items()) if name.startswith('client-cli/'))
    hit = hit and (cached / 'client-cli/SHA256SUMS').is_file() and (cached / 'client-cli/SHA256SUMS').read_text() == sums
    if not hit:
        # Reuse the runtime's bounded extraction and raw metadata validation.
        if cached.exists():
            shutil.rmtree(cached)
        code_root = Path(__file__).resolve().parent.parent
        env = {k: v for k, v in os.environ.items() if k not in ('GOOS', 'GOARCH', 'GOARM', 'GOAMD64', 'CGO_ENABLED')}
        subprocess.run(['go', 'run', './cmd/agenterm-pack', '-prepare-bundle', str(bundle),
                        '-cache-dir', str(cache)], cwd=code_root, env=env, check=True, stdout=subprocess.DEVNULL)
    downloads = root / 'internal/installer/assets/downloads'
    downloads.mkdir(parents=True, exist_ok=True)
    for obsolete in downloads.iterdir():
        if obsolete.is_file() and re.fullmatch(r'agenterm-client-cli-(macos|linux|windows)-(amd64|arm64)\.(tar\.xz|tar\.gz|zip)', obsolete.name):
            obsolete.unlink()
    for path in (cached / 'client-cli').iterdir():
        shutil.copy2(path, downloads / path.name)
    cli = cached / 'agenterm-client-cli-linux-amd64'
    expected_cli = manifest['files']['client-cli/' + cli.name]['sha256']
    if not cli.is_file() or digest(cli) != expected_cli:
        hit = False
        with tarfile.open(bundle, 'r:xz') as archive:
            data = archive.extractfile('client-cli/' + cli.name).read()
        descriptor, name = tempfile.mkstemp(prefix='.linux-cli-', dir=cached)
        pending = Path(name)
        try:
            with os.fdopen(descriptor, 'wb') as output:
                output.write(data)
            pending.chmod(0o755)
            pending.replace(cli)
        finally:
            pending.unlink(missing_ok=True)
    return downloads, cli, hit


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path)
    parser.add_argument('--prepare-only', action='store_true')
    parser.add_argument('--linux-cli-output', type=Path)
    parser.add_argument('--components-xz', type=Path, help='Use this prepared unified archive before building')
    parser.add_argument('--cache-dir', type=Path, default=Path(os.environ.get('XDG_CACHE_HOME', str(Path.home() / '.cache'))) / 'agenterm/components')
    parser.add_argument('--goos', default=os.environ.get('GOOS'))
    parser.add_argument('--goarch', default=os.environ.get('GOARCH'))
    args = parser.parse_args()
    if not args.prepare_only and not args.output:
        parser.error('--output is required unless --prepare-only is set')
    root = Path(__file__).resolve().parent.parent
    started = time.monotonic()
    if args.components_xz:
        embedded = root / 'internal/components/assets/components.tar.xz'
        if args.components_xz.resolve() != embedded:
            read_manifest(args.components_xz)
            descriptor, name = tempfile.mkstemp(prefix='.components-', dir=embedded.parent)
            os.close(descriptor)
            pending = Path(name)
            try:
                shutil.copyfile(args.components_xz, pending)
                pending.replace(embedded)
            finally:
                pending.unlink(missing_ok=True)
    downloads, cli, hit = prepare(root, args.cache_dir.resolve())
    prepared = time.monotonic()
    if args.linux_cli_output:
        shutil.copy2(cli, args.linux_cli_output)
        args.linux_cli_output.chmod(0o755)
    if not args.prepare_only:
        env = {k: v for k, v in os.environ.items() if k not in ('GOOS', 'GOARCH', 'GOARM', 'GOAMD64', 'CGO_ENABLED')}
        env['CGO_ENABLED'] = '0'
        for key, value in (('GOOS', args.goos), ('GOARCH', args.goarch)):
            if value:
                env[key] = value
        args.output.resolve().parent.mkdir(parents=True, exist_ok=True)
        subprocess.run(['go', 'build', '-trimpath', '-gcflags=all=-l', '-ldflags', '-s -w',
                        '-o', str(args.output.resolve()), './cmd/agenterm-server'], cwd=root, env=env, check=True)
    print(json.dumps(dict(downloads=str(downloads), components=str(root / 'internal/components/assets/components.tar.xz'),
                          cache_hit=hit, prepare_seconds=round(prepared-started, 3),
                          build_seconds=round(time.monotonic()-prepared, 3))), flush=True)


if __name__ == '__main__':
    main()
