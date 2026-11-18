import hashlib
import json
from pathlib import Path
import tempfile
import unittest

from client_bundle_provenance import CLIENT_BINARIES, build_record, validate, verify_builds


def fixture(root):
    for component, version in (('agenterm-cli', '1.2.3'), ('p2p-cli', '0.1.0')):
        path = root / 'components' / component / 'Cargo.toml'
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(f'[package]\nversion = "{version}"\n')


class ClientProvenanceTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        fixture(self.root)
        self.manifest = dict(schema=3, client_version='1.2.3', p2p_version='0.1.0')

    def test_bundle_requires_both_declared_versions(self):
        validate(self.root, self.manifest)
        with self.assertRaisesRegex(ValueError, 'version'):
            validate(self.root, dict(self.manifest, client_version='0.20.6'))
        with self.assertRaisesRegex(ValueError, 'p2p_version'):
            validate(self.root, dict(self.manifest, p2p_version='0.1.9'))

    def test_prebuilt_clients_must_match_declared_version_and_checksum(self):
        for name in CLIENT_BINARIES:
            binary = self.root / name
            binary.write_bytes(b'original CLI')
            build_record(binary).write_text(json.dumps(dict(
                version='1.2.3',
                sha256=hashlib.sha256(binary.read_bytes()).hexdigest())))
        verify_builds(self.root, self.root)
        binary.write_bytes(b'wrong binary')
        with self.assertRaisesRegex(ValueError, 'checksum'):
            verify_builds(self.root, self.root)
        binary.write_bytes(b'original CLI')
        (self.root / 'components/agenterm-cli/Cargo.toml').write_text('[package]\nversion = "1.2.4"\n')
        with self.assertRaisesRegex(ValueError, 'version'):
            verify_builds(self.root, self.root)
