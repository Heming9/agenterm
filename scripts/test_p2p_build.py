import contextlib
import hashlib
import io
import json
from pathlib import Path
import struct
import tempfile
import unittest

import build_p2p_cli as build


class P2PBuildTests(unittest.TestCase):
    def binary(self, system, arch):
        data = bytearray(128)
        if system == 'linux':
            data[:6] = b'\x7fELF\x02\x01'
            struct.pack_into('<H', data, 18, 62 if arch == 'amd64' else 183)
            struct.pack_into('<Q', data, 32, 64)
            struct.pack_into('<HH', data, 54, 56, 1)
            struct.pack_into('<I', data, 64, 1)
        elif system == 'darwin':
            data[:4] = b'\xcf\xfa\xed\xfe'
            struct.pack_into('<I', data, 4, 0x1000007 if arch == 'amd64' else 0x100000c)
        else:
            data[:2] = b'MZ'
            struct.pack_into('<I', data, 60, 64)
            data[64:68] = b'PE\0\0'
            struct.pack_into('<H', data, 68, 0x8664 if arch == 'amd64' else 0xaa64)
        return bytes(data)

    def test_executable_architecture_is_checked_for_every_platform(self):
        for system, arch in sorted(build.PLATFORMS):
            with self.subTest(system=system, arch=arch):
                data = self.binary(system, arch)
                build.verify_binary(data, system, arch)
                with self.assertRaises(ValueError):
                    build.verify_binary(data, system, 'arm64' if arch == 'amd64' else 'amd64')
                with self.assertRaises(ValueError):
                    build.verify_binary(data, 'linux' if system != 'linux' else 'darwin', arch)
        for data in (b'', b'MZ', b'\x7fELF', b'not an executable'):
            with self.assertRaises(ValueError):
                build.verify_binary(data, 'windows', 'arm64')

    def test_linux_download_rejects_dynamic_linker_and_truncated_headers(self):
        data = bytearray(self.binary('linux', 'amd64'))
        struct.pack_into('<I', data, 64, 3)
        with self.assertRaisesRegex(ValueError, 'statically linked'):
            build.verify_binary(data, 'linux', 'amd64')
        with self.assertRaisesRegex(ValueError, 'program headers'):
            build.verify_binary(data[:64], 'linux', 'amd64')

    def assets(self, directory, platforms):
        directory.mkdir()
        artifacts = []
        for system, arch in sorted(platforms):
            name = build.archive_name(system, arch)
            payload = self.binary(system, arch)
            (directory / name).write_bytes(payload)
            artifacts.append(dict(platform=system, arch=arch, file=name, sha256=hashlib.sha256(payload).hexdigest()))
        (directory / 'manifest.json').write_text(json.dumps(dict(version='test-1', artifacts=artifacts)))

    def test_complete_distribution_rejects_missing_platform_before_writing(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source, output = root / 'source', root / 'merged'
            self.assets(source, build.PLATFORMS - {('windows', 'arm64')})
            with self.assertRaisesRegex(SystemExit, 'windows/arm64'):
                build.merge_assets([source], output, require_all=True)
            self.assertFalse(output.exists())

    def test_merge_preserves_all_six_archives(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            sources = [root / 'unix', root / 'windows']
            self.assets(sources[0], {p for p in build.PLATFORMS if p[0] != 'windows'})
            self.assets(sources[1], {p for p in build.PLATFORMS if p[0] == 'windows'})
            output = root / 'merged'
            with contextlib.redirect_stdout(io.StringIO()):
                build.merge_assets(sources, output, require_all=True)
            manifest = json.loads((output / 'manifest.json').read_text())
            self.assertEqual({(a['platform'], a['arch']) for a in manifest['artifacts']}, build.PLATFORMS)
            for source in sources:
                for path in source.iterdir():
                    if path.name != 'manifest.json':
                        self.assertEqual((output / path.name).read_bytes(), path.read_bytes())

    def test_merge_rejects_mislabeled_archives_and_oversized_downloads(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = root / 'source'
            self.assets(source, {('windows', 'arm64')})
            path = source / 'manifest.json'
            manifest = json.loads(path.read_text())
            manifest['artifacts'][0]['arch'] = 'amd64'
            path.write_text(json.dumps(manifest))
            with self.assertRaisesRegex(SystemExit, 'archive name'):
                build.merge_assets([source], root / 'merged')
        for system in ('darwin', 'windows'):
            build.check_download_size(system, build.MAX_DOWNLOAD_BYTES)
            with self.assertRaises(SystemExit):
                build.check_download_size(system, build.MAX_DOWNLOAD_BYTES + 1)


if __name__ == '__main__':
    unittest.main()
