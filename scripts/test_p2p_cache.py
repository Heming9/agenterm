import contextlib
import hashlib
import io
import json
import os
from pathlib import Path
import struct
import subprocess
import tempfile
import unittest

import p2p_cache as cache


class NativeCacheTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        files = {
            'tools/p2p-cli/Cargo.toml': '[package]\nversion="0.1.1"\n',
            'tools/p2p-cli/Cargo.lock': '# dependencies\n',
            'tools/p2p-cli/src/main.rs': 'fn main() {}\n',
            'tools/p2p-cli/daemon_e2e.py': '# native validation\n',
            'scripts/build_p2p_cli.py': '# packaging\n',
            'scripts/p2p_cache.py': '# cache contract\n',
            '.github/workflows/p2p-cli.yml': '# toolchain and flags\n',
        }
        for name, content in files.items():
            path = self.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(content)
        subprocess.run(['git', 'init', '-q'], cwd=self.root, check=True)
        subprocess.run(['git', '-c', 'core.autocrlf=false', 'add', '.'], cwd=self.root, check=True)
        self.target = 'x86_64-apple-darwin'
        self.value = cache.identity(self.root, self.target, '1.99.1', 'macos-15-intel')
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

    def test_go_changes_and_untracked_build_outputs_keep_the_native_key(self):
        go = self.root / 'internal/client/client.go'
        go.parent.mkdir(parents=True)
        go.write_text('package client\n')
        subprocess.run(['git', '-c', 'core.autocrlf=false', 'add', 'internal/client/client.go'], cwd=self.root, check=True)
        target = self.root / 'tools/p2p-cli/target/output'
        target.parent.mkdir(parents=True)
        target.write_bytes(b'compiled object')
        self.assertEqual(self.value, cache.identity(self.root, self.target, '1.99.1', 'macos-15-intel'))

    def test_native_sources_lockfile_packaging_and_validation_changes_invalidate(self):
        for name in ('tools/p2p-cli/src/main.rs', 'tools/p2p-cli/Cargo.lock',
                     'tools/p2p-cli/daemon_e2e.py', 'scripts/build_p2p_cli.py',
                     'scripts/p2p_cache.py', '.github/workflows/p2p-cli.yml'):
            with self.subTest(file=name):
                path = self.root / name
                original = path.read_bytes()
                path.write_bytes(original + b'changed')
                self.assertNotEqual(self.value, cache.identity(self.root, self.target, '1.99.1', 'macos-15-intel'))
                path.write_bytes(original)

    def test_toolchain_target_and_runner_separate_cache_entries(self):
        keys = {cache.cache_key(self.value)}
        keys.add(cache.cache_key(cache.identity(self.root, self.target, '1.100.0', 'macos-15-intel')))
        keys.add(cache.cache_key(cache.identity(self.root, 'aarch64-apple-darwin', '1.99.1', 'macos-15-intel')))
        keys.add(cache.cache_key(cache.identity(self.root, self.target, '1.99.1', 'macos-15')))
        self.assertEqual(len(keys), 4)

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

    def test_source_mismatch_rejects_before_copying(self):
        cache.store(self.root, self.directory, self.source, self.value)
        source = self.root / 'tools/p2p-cli/src/main.rs'
        source.write_text('fn main() { panic!() }')
        changed = cache.identity(self.root, self.target, '1.99.1', 'macos-15-intel')
        self.assertFalse(self.restore(changed))
        self.assertFalse(self.binary.exists())

    def test_other_toolchain_or_runner_cannot_restore_the_component(self):
        cache.store(self.root, self.directory, self.source, self.value)
        for field, changed in (('rust', '1.100.0'), ('runner', 'macos-15'),
                               ('target', 'aarch64-apple-darwin')):
            with self.subTest(field=field):
                self.assertFalse(self.restore(dict(self.value, **{field: changed})))
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
