import gzip
from concurrent.futures import ThreadPoolExecutor
import hashlib
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
import zipfile

import build_components
import build_server


class ComponentBuildTests(unittest.TestCase):
    def fixture(self, path, mutation=None):
        stream = io.BytesIO()
        with tarfile.open(fileobj=stream, mode='w') as archive:
            entry = tarfile.TarInfo('agenterm-client-cli-linux-amd64')
            entry.size = 3
            archive.addfile(entry, io.BytesIO(b'cli'))
        linux = gzip.compress(stream.getvalue(), mtime=0)
        native = b'native p2p'
        peers = dict(version='0.1.0', artifacts=[dict(platform='darwin', arch='arm64',
                     file='p2p-cli-macos-arm64.tar.gz', sha256=hashlib.sha256(native).hexdigest())])
        checksums = f'{hashlib.sha256(b"cli").hexdigest()}  agenterm-client-cli-linux-amd64\n'.encode()
        files = {'client-cli/SHA256SUMS': checksums, 'p2p-cli/manifest.json': json.dumps(peers).encode(),
                 'p2p-cli/p2p-cli-macos-arm64.tar.gz': native}
        for platform in ('macos', 'linux', 'windows'):
            for arch in ('amd64', 'arm64'):
                name = f'client-cli/agenterm-client-cli-{platform}-{arch}' + ('.zip' if platform == 'windows' else '.tar.gz')
                files[name] = linux if name.endswith('linux-amd64.tar.gz') else b'archive'
        if mutation == 'traversal':
            files['client-cli/../../outside'] = b'outside'
        manifest = dict(schema=1, files={name: dict(bytes=len(data), sha256=hashlib.sha256(data).hexdigest())
                                        for name, data in files.items()})
        if mutation == 'hash':
            manifest['files']['client-cli/SHA256SUMS']['sha256'] = '0' * 64
        if mutation == 'size':
            manifest['files']['client-cli/SHA256SUMS']['bytes'] += 1
        path.parent.mkdir(parents=True, exist_ok=True)
        with zipfile.ZipFile(path, 'w') as archive:
            for name, data in dict(files, **{'manifest.json': json.dumps(manifest).encode()}).items():
                entry = zipfile.ZipInfo(name)
                entry.external_attr = 0o100644 << 16
                archive.writestr(entry, data)
        return files

    def test_prepare_reuses_exact_archives_and_recovers_a_corrupt_build_cache(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td) / 'repo'
            files = self.fixture(root / 'internal/components/assets/components.zip')
            cache = Path(td) / 'cache'
            downloads, cli, hit = build_server.prepare(root, cache)
            self.assertFalse(hit)
            self.assertEqual(cli.read_bytes(), b'cli')
            for name, data in files.items():
                if name.startswith('client-cli/'):
                    self.assertEqual((downloads / name.split('/')[1]).read_bytes(), data)
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
            files = self.fixture(root / 'internal/components/assets/components.zip')
            with ThreadPoolExecutor(max_workers=8) as executor:
                results = list(executor.map(lambda _: build_server.prepare(root, cache), range(16)))
            self.assertEqual(sum(not result[2] for result in results), 1)
            for downloads, cli, _ in results:
                self.assertEqual(cli.read_bytes(), b'cli')
                for name, data in files.items():
                    if name.startswith('client-cli/'):
                        self.assertEqual((downloads / name.split('/')[1]).read_bytes(), data)

    def test_invalid_bundles_are_rejected_before_cache_creation(self):
        for mutation in ('traversal', 'hash', 'size'):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as td:
                root = Path(td) / 'repo'
                self.fixture(root / 'internal/components/assets/components.zip', mutation)
                cache = Path(td) / 'cache'
                with self.assertRaises(ValueError):
                    build_server.prepare(root, cache)
                self.assertFalse(cache.exists())

    def test_regeneration_reuses_native_p2p_bytes(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            files = self.fixture(root / 'components.zip')
            peers = root / 'peers'
            peers.mkdir()
            build_components.reuse_p2p(root / 'components.zip', peers)
            self.assertEqual((peers / 'p2p-cli-macos-arm64.tar.gz').read_bytes(), files['p2p-cli/p2p-cli-macos-arm64.tar.gz'])


if __name__ == '__main__':
    unittest.main()
