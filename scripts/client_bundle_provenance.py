"""Check Client CLI and P2P bundle versions and artifact checksums."""
import hashlib
import json
from pathlib import Path
import re
import tomllib


BUNDLE_PATH = Path('internal/components/assets/components.tar.xz')
CLIENT_MANIFEST = Path('components/agenterm-cli/Cargo.toml')
P2P_MANIFEST = Path('components/p2p-cli/Cargo.toml')
CLIENT_BINARIES = tuple(
    f"agenterm-client-cli-{system}-{arch}" + ('.exe' if system == 'windows' else '')
    for system in ('linux', 'macos', 'windows') for arch in ('amd64', 'arm64')
)


def package_version(root, manifest):
    version = tomllib.loads((root / manifest).read_text())['package']['version']
    if not re.fullmatch(r'[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.+-]+)?', version):
        raise ValueError(f'Invalid version in {manifest}: {version}')
    return version


def build_record(binary):
    return binary.with_name(binary.name + '.build.json')


def verify_builds(root, directory):
    version = package_version(root, CLIENT_MANIFEST)
    hashes = {}
    for name in CLIENT_BINARIES:
        binary = directory / name
        record = json.loads(build_record(binary).read_text())
        checksum = hashlib.sha256(binary.read_bytes()).hexdigest()
        if record.get('version') != version or record.get('sha256') != checksum:
            raise ValueError(f'{name} does not match the current CLI version or recorded checksum')
        hashes['client-cli/' + name] = checksum
    return version, hashes


def validate(root, manifest):
    if manifest.get('schema') != 3:
        raise ValueError('Client CLI build metadata is missing. Rebuild the component bundle')
    for component, source in (('client_version', CLIENT_MANIFEST), ('p2p_version', P2P_MANIFEST)):
        expected = package_version(root, source)
        if manifest.get(component) != expected:
            raise ValueError(f'Expected {component} {expected} from {source}. Rebuild the component bundle')
