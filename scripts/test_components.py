from concurrent.futures import ThreadPoolExecutor
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest

import build_components
import build_server


class ComponentBuildTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temporary = tempfile.TemporaryDirectory(prefix='component-test-packer-')
        cls.packer = Path(cls.temporary.name) / ('packer.exe' if os.name == 'nt' else 'packer')
        subprocess.run(['go', 'build', '-o', str(cls.packer), './cmd/agenterm-pack'],
                       cwd=Path(__file__).resolve().parent.parent, check=True)

    @classmethod
    def tearDownClass(cls):
        cls.temporary.cleanup()

    def fixture(self, path, mutation=None):
        raw, peers = path.parent / 'raw', path.parent / 'peers'
        raw.mkdir(parents=True)
        peers.mkdir()
        artifacts = []
        for platform in ('macos', 'linux', 'windows'):
            for arch in ('amd64', 'arm64'):
                extension = '.exe' if platform == 'windows' else ''
                (raw / f'agenterm-client-cli-{platform}-{arch}{extension}').write_bytes(b'cli')
                name = f'p2p-cli-{platform}-{arch}{extension}'
                (peers / name).write_bytes(b'native')
                artifacts.append(dict(platform='darwin' if platform == 'macos' else platform, arch=arch,
                                      file=name, sha256=build_server.digest(peers / name)))
        (peers / 'manifest.json').write_text(json.dumps(dict(version='0.1.0', artifacts=artifacts)))
        subprocess.run([str(self.packer), '-input', str(raw), '-p2p-assets', str(peers),
                        '-bundle', str(path), '-output', str(path.parent / 'downloads')], check=True)
        if mutation:
            with tarfile.open(path, 'r:xz') as archive:
                files = {entry.name: archive.extractfile(entry).read() for entry in archive}
            manifest = json.loads(files['manifest.json'])
            if mutation == 'traversal':
                name = 'client-cli/../../outside'
                files[name] = files.pop('client-cli/agenterm-client-cli-linux-amd64')
            elif mutation == 'hash':
                manifest['files']['client-cli/agenterm-client-cli-linux-amd64']['sha256'] = '0' * 64
            elif mutation == 'size':
                manifest['files']['client-cli/agenterm-client-cli-linux-amd64']['bytes'] += 1
            files['manifest.json'] = json.dumps(manifest).encode()
            with tarfile.open(path, 'w:xz') as archive:
                for name, data in files.items():
                    entry = tarfile.TarInfo(name)
                    entry.size = len(data)
                    archive.addfile(entry, io.BytesIO(data))
        return path.parent / 'downloads'

    def test_prepare_reuses_exact_archives_and_recovers_a_corrupt_build_cache(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td) / 'repo'
            expected = self.fixture(root / 'internal/components/assets/components.tar.xz')
            cache = Path(td) / 'cache'
            downloads, cli, hit = build_server.prepare(root, cache)
            self.assertFalse(hit)
            self.assertEqual(cli.read_bytes(), b'cli')
            self.assertEqual((downloads / 'SHA256SUMS').read_bytes(), (expected / 'SHA256SUMS').read_bytes())
            self.assertTrue(all(not path.name.endswith(('.tar.xz','.tar.gz','.zip')) for path in downloads.iterdir()))
            self.assertTrue(build_server.prepare(root, cache)[2])
            cli.write_bytes(b'tampered raw CLI')
            self.assertFalse(build_server.prepare(root, cache)[2])
            self.assertEqual(cli.read_bytes(), b'cli')
            (cli.parent / 'client-cli/SHA256SUMS').write_bytes(b'bad')
            self.assertFalse(build_server.prepare(root, cache)[2])
            self.assertEqual(cli.read_bytes(), b'cli')

    def test_parallel_builds_publish_a_consistent_cache_and_downloads(self):
        with tempfile.TemporaryDirectory() as td:
            root, cache = Path(td) / 'repo', Path(td) / 'cache'
            expected = self.fixture(root / 'internal/components/assets/components.tar.xz')
            with ThreadPoolExecutor(max_workers=8) as executor:
                results = list(executor.map(lambda _: build_server.prepare(root, cache), range(16)))
            self.assertEqual(sum(not result[2] for result in results), 1)
            for downloads, cli, _ in results:
                self.assertEqual(cli.read_bytes(), b'cli')
                self.assertEqual((downloads / 'SHA256SUMS').read_bytes(), (expected / 'SHA256SUMS').read_bytes())

    def test_invalid_bundles_are_rejected_before_cache_creation(self):
        for mutation in ('traversal', 'hash', 'size'):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as td:
                root = Path(td) / 'repo'
                self.fixture(root / 'internal/components/assets/components.tar.xz', mutation)
                cache = Path(td) / 'cache'
                with self.assertRaises(ValueError):
                    build_server.prepare(root, cache)
                self.assertFalse(cache.exists())

    def test_regeneration_reuses_native_p2p_binaries(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            self.fixture(root / 'components.tar.xz')
            peers = root / 'reused'
            peers.mkdir()
            build_components.reuse_p2p(root / 'components.tar.xz', peers)
            manifest = json.loads((peers / 'manifest.json').read_text())
            self.assertEqual(len(manifest['artifacts']), 6)
            for artifact in manifest['artifacts']:
                path = peers / artifact['file']
                self.assertEqual(build_server.digest(path), artifact['sha256'])
                self.assertEqual(path.read_bytes(), b'native')


if __name__ == '__main__':
    unittest.main()
