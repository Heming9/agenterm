import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


SCRIPT = Path(__file__).with_name('install_linux_musl.sh').resolve()


@unittest.skipIf(os.name == 'nt', 'requires bash')
class InstallLinuxMuslTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.bin = self.root / 'bin'
        self.bin.mkdir()
        for name in ('tee', 'touch', 'uname'):
            (self.bin / name).symlink_to(shutil.which(name))
        self.apt = self.root / 'apt'
        (self.apt / 'sources.list.d').mkdir(parents=True)
        self.calls = self.root / 'calls'
        self.stub('dpkg', 'echo "${TEST_ARCH:-amd64}"')
        self.stub('getent', 'exit "${TEST_DNS_EXIT:-0}"')
        self.stub('timeout', 'shift; exec "$@"')
        self.stub('sudo', 'exec "$@"')
        self.stub('apt-get', '''
echo "$*" >> "$TEST_CALLS"
if [ "${TEST_FAIL_FIRST:-0}" = 1 ] && [ ! -f "$TEST_FIRST_FAILED" ]; then
  touch "$TEST_FIRST_FAILED"
  exit 1
fi
case "$*" in
  *' install '*)
    if [ "${TEST_FAIL_INSTALL:-0}" = 1 ] && [ ! -f "$TEST_FIRST_FAILED" ]; then
      touch "$TEST_FIRST_FAILED"
      exit 1
    fi ;;
esac
exit "${TEST_APT_EXIT:-0}"
''')

    def stub(self, name, body):
        path = self.bin / name
        path.write_text('#!/bin/sh\n' + body + '\n')
        path.chmod(0o755)

    def run_script(self, **values):
        env = dict(os.environ, PATH=str(self.bin),
                   AGENTERM_APT_ETC=str(self.apt), TEST_CALLS=str(self.calls),
                   TEST_FIRST_FAILED=str(self.root / 'failed'), **values)
        return subprocess.run(['/bin/bash', str(SCRIPT)], env=env, capture_output=True, text=True)

    def test_normal_archive_is_used_when_available(self):
        mirrors = self.apt / 'apt-mirrors.txt'
        mirrors.write_text('http://azure.archive.ubuntu.com/ubuntu\n')
        result = self.run_script()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('azure.archive.ubuntu.com', mirrors.read_text())
        self.assertEqual(len(self.calls.read_text().splitlines()), 2)

    def test_failed_apt_update_retries_with_mirror(self):
        mirrors = self.apt / 'apt-mirrors.txt'
        mirrors.write_text('http://azure.archive.ubuntu.com/ubuntu\n')
        result = self.run_script(TEST_FAIL_FIRST='1')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(mirrors.read_text().strip(), 'https://mirrors.ustc.edu.cn/ubuntu')
        self.assertEqual(len(self.calls.read_text().splitlines()), 3)

    def test_arm_dns_failure_updates_direct_source(self):
        source = self.apt / 'sources.list.d/ubuntu.sources'
        source.write_text('URIs: http://ports.ubuntu.com/ubuntu-ports\n')
        # GNU sed is required by the Ubuntu runner; this test also works on macOS.
        self.stub('sed', '''
if [ "$1" = -i ]; then shift; fi
if [ "$(uname -s)" = Darwin ]; then exec /usr/bin/sed -i '' "$@"; fi
exec /usr/bin/sed -i "$@"
''')
        result = self.run_script(TEST_ARCH='arm64', TEST_DNS_EXIT='1')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(source.read_text().strip(), 'URIs: https://mirrors.ustc.edu.cn/ubuntu-ports')

    def test_failed_package_download_retries_with_mirror(self):
        mirrors = self.apt / 'apt-mirrors.txt'
        mirrors.write_text('original\n')
        result = self.run_script(TEST_FAIL_INSTALL='1')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(mirrors.read_text().strip(), 'https://mirrors.ustc.edu.cn/ubuntu')
        self.assertEqual(len(self.calls.read_text().splitlines()), 4)

    def test_fallback_failure_is_not_ignored(self):
        (self.apt / 'apt-mirrors.txt').write_text('original\n')
        result = self.run_script(TEST_DNS_EXIT='1', TEST_APT_EXIT='1')
        self.assertNotEqual(result.returncode, 0)


if __name__ == '__main__':
    unittest.main()
