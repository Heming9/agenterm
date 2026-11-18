import json
from pathlib import Path
import tempfile
import unittest

from client_bundle_provenance import source_digest, validate


class ClientProvenanceTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        for name in ('components/agenterm-cli/Cargo.toml', 'components/agenterm-cli/Cargo.lock',
                     'components/agenterm-cli/build.rs', 'components/agenterm-cli/src/main.rs',
                     'internal/i18n/messages.json', 'internal/theme/theme.json', 'internal/version/version.go'):
            path = self.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text('const Current = "0.20.7"\n' if name.endswith('version.go') else name)
        self.path = self.root / 'internal/components/assets/client-provenance.json'
        self.path.parent.mkdir(parents=True)
        self.files = {f'client-cli/{i}': {'sha256': str(i)} for i in range(6)}
        self.lock = {'client_version': '0.20.6', 'source_sha256': source_digest(self.root),
                     'files': {k: v['sha256'] for k, v in self.files.items()}}
        self.save()

    def save(self):
        self.path.write_text(json.dumps(self.lock))

    def check(self):
        validate(self.root, {'files': self.files})

    def test_patch_reuse_and_changed_source(self):
        self.check()
        (self.root / 'components/agenterm-cli/src/main.rs').write_text('changed')
        with self.assertRaisesRegex(ValueError, 'stale'):
            self.check()

    def test_mismatch_or_incompatible(self):
        self.files['client-cli/0']['sha256'] = 'bad'
        with self.assertRaisesRegex(ValueError, 'hashes'):
            self.check()
        self.lock['client_version'] = '0.10.17'
        self.save()
        with self.assertRaisesRegex(ValueError, 'incompatible'):
            self.check()


if __name__ == '__main__':
    unittest.main()
