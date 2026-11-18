"""Check that the bundled clients came from the current client inputs."""
import hashlib
import json
from pathlib import Path
import re


def source_digest(root):
    client = root / 'components/agenterm-cli'
    paths = [client / 'Cargo.toml', client / 'Cargo.lock', client / 'build.rs',
             root / 'internal/i18n/messages.json', root / 'internal/theme/theme.json']
    paths += sorted((client / 'src').rglob('*.rs'))
    h = hashlib.sha256()
    for path in sorted(paths):
        name = path.relative_to(root).as_posix().encode()
        data = path.read_bytes()
        h.update(len(name).to_bytes(4, 'big') + name + len(data).to_bytes(8, 'big') + data)
    return h.hexdigest()


def current_version(root):
    text = (root / 'internal/version/version.go').read_text()
    match = re.search(r'^const Current = "(\d+)\.(\d+)\.(\d+)"$', text, re.MULTILINE)
    if not match:
        raise ValueError('Cannot determine release version')
    return match.group(0).split('"')[1]


def validate(root, manifest):
    lock_path = root / 'internal/components/assets/client-provenance.json'
    lock = json.loads(lock_path.read_text())
    expected = current_version(root)
    actual = lock['client_version']
    if not re.fullmatch(r'\d+\.\d+\.\d+', actual) or actual.split('.')[:2] != expected.split('.')[:2]:
        raise ValueError(f'Bundled client {actual} is incompatible with server {expected}')
    if source_digest(root) != lock['source_sha256']:
        raise ValueError('Bundled clients are stale: client build inputs changed')
    hashes = {k: v['sha256'] for k, v in manifest['files'].items() if k.startswith('client-cli/')}
    if len(hashes) != 6 or hashes != lock['files']:
        raise ValueError('Bundled client hashes do not match client provenance')
