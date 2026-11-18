import contextlib
import hashlib
import io
import json
import os
from pathlib import Path
import struct
import tempfile
import unittest

import p2p_cache as cache


class NativeCacheTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        files = {
            'components/p2p-cli/Cargo.toml': '[package]\nversion="0.1.1"\n',
            'components/p2p-cli/src/main.rs': 'fn main() {}\n',
        }
        for name, content in files.items():
            path = self.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(content)
        self.target = 'x86_64-apple-darwin'
        self.value = cache.identity(self.root, self.target)
        self.directory = self.root / 'cache'
        self.source = self.root / 'source'
        self.assets = self.root / 'restored'
        self.binary = self.root / 'target/release/p2p-cli'
        self.name = 'p2p-cli-macos-amd64'
        self.payload = struct.pack('<II', 0xfeedfacf, 0x1000007) + b'native fixture'
        self.source.mkdir()
        (self.source / self.name).write_bytes(self.payload)
        self.manifest = dict(version='0.1.1', artifacts=[dict(platform='darwin', arch='amd64',
                             file=self.name, sha256=hashlib.sha256(self.payload).hexdigest())])
        (self.source / 'manifest.json').write_text(json.dumps(self.manifest))

    def restore(self, value=None):
        with contextlib.redirect_stderr(io.StringIO()):
            return cache.restore(self.root, self.directory, self.assets, self.binary, value or self.value)

    def test_source_changes_keep_the_native_key_until_the_version_changes(self):
        (self.root / 'components/p2p-cli/src/main.rs').write_text('fn main() { panic!() }')
        self.assertEqual(self.value, cache.identity(self.root, self.target))
        (self.root / 'components/p2p-cli/Cargo.toml').write_text('[package]\nversion="0.1.2"\n')
        self.assertNotEqual(cache.cache_key(self.value), cache.cache_key(cache.identity(self.root, self.target)))

    def test_validated_component_round_trip_preserves_manifest_bytes_and_execution(self):
        cache.store(self.root, self.directory, self.source, self.value)
        self.assertTrue(self.restore())
        self.assertEqual(self.binary.read_bytes(), self.payload)
        self.assertEqual((self.assets / self.name).read_bytes(), self.payload)
        self.assertEqual(json.loads((self.assets / 'manifest.json').read_text()), self.manifest)
        if os.name != 'nt':
            self.assertTrue(os.access(self.binary, os.X_OK))

    def test_missing_cache_falls_back_without_writing_any_executable(self):
        self.assertFalse(self.restore())
        self.assertFalse(self.assets.exists())
        self.assertFalse(self.binary.exists())

    def test_version_mismatch_rejects_before_copying(self):
        cache.store(self.root, self.directory, self.source, self.value)
        (self.root / 'components/p2p-cli/Cargo.toml').write_text('[package]\nversion="0.1.2"\n')
        changed = cache.identity(self.root, self.target)
        self.assertFalse(self.restore(changed))
        self.assertFalse(self.binary.exists())

    def test_other_target_cannot_restore_the_component(self):
        cache.store(self.root, self.directory, self.source, self.value)
        changed = cache.identity(self.root, 'aarch64-apple-darwin')
        self.assertNotEqual(cache.cache_key(self.value), cache.cache_key(changed))
        self.assertFalse(self.restore(changed))
        self.assertFalse(self.binary.exists())

    def test_corrupt_binary_rejects_before_copying(self):
        cache.store(self.root, self.directory, self.source, self.value)
        (self.directory / 'assets' / self.name).write_bytes(b'corrupt')
        self.assertFalse(self.restore())
        self.assertFalse(self.binary.exists())

    def test_checksum_cannot_hide_a_wrong_executable_architecture(self):
        cache.store(self.root, self.directory, self.source, self.value)
        payload = struct.pack('<II', 0xfeedfacf, 0x100000c) + b'wrong arch'
        (self.directory / 'assets' / self.name).write_bytes(payload)
        self.manifest['artifacts'][0]['sha256'] = hashlib.sha256(payload).hexdigest()
        (self.directory / 'assets/manifest.json').write_text(json.dumps(self.manifest))
        self.assertFalse(self.restore())
        self.assertFalse(self.binary.exists())

    def test_bad_manifest_falls_back_without_writing(self):
        cache.store(self.root, self.directory, self.source, self.value)
        path = self.directory / 'assets/manifest.json'
        for invalid in ([], {'version': '0.1.0'}, {'version': '0.1.1', 'artifacts': ['invalid']}):
            with self.subTest(manifest=invalid):
                path.write_text(json.dumps(invalid))
                self.assertFalse(self.restore())
                self.assertFalse(self.binary.exists())


if __name__ == '__main__':
    unittest.main()
