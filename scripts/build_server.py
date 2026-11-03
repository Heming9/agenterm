#!/usr/bin/env python3
"""Build the single Server executable embedding the unified components.zip."""
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
import zipfile


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def read_manifest(bundle):
    with zipfile.ZipFile(bundle) as archive:
        entries = archive.infolist()
        names = {entry.filename for entry in entries}
        if not 10 <= len(entries) <= 33 or len(names) != len(entries):
            raise ValueError('Invalid or duplicate components ZIP entries')
        if archive.getinfo('manifest.json').file_size > 64 << 10:
            raise ValueError('Components manifest too large')
        manifest = json.loads(archive.read('manifest.json'))
        if manifest['schema'] != 1 or names != set(manifest['files']) | {'manifest.json'}:
            raise ValueError('Invalid unified components manifest')
        total = 0
        for name, info in manifest['files'].items():
            special = name in ('client-cli/SHA256SUMS', 'p2p-cli/manifest.json')
            native = re.fullmatch(r'(client-cli/agenterm-client-cli|p2p-cli/p2p-cli)-(macos|linux|windows)-(amd64|arm64)\.(tar\.gz|zip)', name)
            if not special and (not native or ('-windows-' in name) != name.endswith('.zip')):
                raise ValueError('Invalid component path')
            entry = archive.getinfo(name)
            if type(info['bytes']) is not int or not 0 < info['bytes'] <= 64 << 20 or entry.file_size != info['bytes'] or ((entry.external_attr >> 16) & 0o170000) != 0o100000:
                raise ValueError('Invalid component entry')
            total += info['bytes']
            if total > 512 << 20 or not re.fullmatch(r'[0-9a-f]{64}', info['sha256']):
                raise ValueError('Invalid component size or checksum')
            if hashlib.sha256(archive.read(name)).hexdigest() != info['sha256']:
                raise ValueError('Component checksum mismatch')
        required = {'client-cli/SHA256SUMS', 'p2p-cli/manifest.json'}
        for platform in ('macos', 'linux', 'windows'):
            for arch in ('amd64', 'arm64'):
                required.add(f'client-cli/agenterm-client-cli-{platform}-{arch}' + ('.zip' if platform == 'windows' else '.tar.gz'))
        if not required <= names:
            raise ValueError('Component bundle is missing a platform')
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
    manifest = read_manifest(root / 'internal/components/assets/components.zip')
    cache.mkdir(parents=True, exist_ok=True)
    # Protect publication, prepared download copies and the auxiliary raw CLI
    # as one operation; a parallel build must never remove files being copied.
    with cache_lock(cache / '.lock'):
        return prepare_locked(root, cache, manifest)


def prepare_locked(root, cache, manifest):
    bundle = root / 'internal/components/assets/components.zip'
    with zipfile.ZipFile(bundle) as archive:
        entries = {entry.filename: entry for entry in archive.infolist()}
        cached = cache / digest(bundle)
        expected = manifest['files']
        hit = cached.is_dir() and all((cached / name).is_file() and digest(cached / name) == info['sha256']
                                      for name, info in expected.items())
        if not hit:
            cache.mkdir(parents=True, exist_ok=True)
            with tempfile.TemporaryDirectory(prefix='components-', dir=cache) as temporary:
                stage = Path(temporary) / 'ready'
                stage.mkdir()
                for name, info in expected.items():
                    path = stage / name
                    if not name.startswith(('client-cli/', 'p2p-cli/')) or not path.resolve().is_relative_to(stage.resolve()):
                        raise ValueError('Invalid component path')
                    entry = entries[name]
                    if entry.file_size != info['bytes'] or entry.file_size > 64 << 20 or ((entry.external_attr >> 16) & 0o170000) != 0o100000:
                        raise ValueError('Invalid component entry')
                    data = archive.read(name)
                    if hashlib.sha256(data).hexdigest() != info['sha256']:
                        raise ValueError('Component checksum mismatch')
                    path.parent.mkdir(parents=True, exist_ok=True)
                    path.write_bytes(data)
                if cached.exists():
                    shutil.rmtree(cached)
                try:
                    stage.rename(cached)
                except OSError:
                    if not cached.is_dir() or not all(digest(cached / n) == i['sha256'] for n, i in expected.items()):
                        raise
    # Keep the existing deployment helper's download-directory interface.
    # These are exact copies; the server embeds only components.zip.
    downloads = root / 'internal/installer/assets/downloads'
    downloads.mkdir(parents=True, exist_ok=True)
    for path in (cached / 'client-cli').iterdir():
        shutil.copy2(path, downloads / path.name)
    cli = cached / 'agenterm-client-cli-linux-amd64'
    checksums = dict((name, value) for value, name in
                     (line.split('  ', 1) for line in (cached / 'client-cli/SHA256SUMS').read_text().splitlines()))
    expected_cli = checksums.get(cli.name, '')
    if not re.fullmatch(r'[0-9a-f]{64}', expected_cli):
        raise ValueError('Missing raw Linux CLI checksum')
    if not cli.is_file() or digest(cli) != expected_cli:
        hit = False
        with tarfile.open(cached / 'client-cli/agenterm-client-cli-linux-amd64.tar.gz', 'r:gz') as archive:
            entry = archive.getmember(cli.name)
            if not entry.isfile() or not 0 < entry.size <= 64 << 20:
                raise ValueError('Invalid raw Linux CLI entry')
            data = archive.extractfile(entry).read()
            if hashlib.sha256(data).hexdigest() != expected_cli:
                raise ValueError('Raw Linux CLI checksum mismatch')
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
    parser.add_argument('--components-zip', type=Path, help='Use this prepared unified archive before building')
    parser.add_argument('--cache-dir', type=Path, default=Path(os.environ.get('XDG_CACHE_HOME', str(Path.home() / '.cache'))) / 'agenterm/components')
    parser.add_argument('--goos', default=os.environ.get('GOOS'))
    parser.add_argument('--goarch', default=os.environ.get('GOARCH'))
    args = parser.parse_args()
    if not args.prepare_only and not args.output:
        parser.error('--output is required unless --prepare-only is set')
    root = Path(__file__).resolve().parent.parent
    started = time.monotonic()
    if args.components_zip:
        embedded = root / 'internal/components/assets/components.zip'
        if args.components_zip.resolve() != embedded:
            read_manifest(args.components_zip)
            descriptor, name = tempfile.mkstemp(prefix='.components-', dir=embedded.parent)
            os.close(descriptor)
            pending = Path(name)
            try:
                shutil.copyfile(args.components_zip, pending)
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
    print(json.dumps(dict(downloads=str(downloads), components=str(root / 'internal/components/assets/components.zip'),
                          cache_hit=hit, prepare_seconds=round(prepared-started, 3),
                          build_seconds=round(time.monotonic()-prepared, 3))), flush=True)


if __name__ == '__main__':
    main()
