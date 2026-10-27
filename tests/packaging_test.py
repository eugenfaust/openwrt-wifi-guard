#!/usr/bin/env python3
"""Execute package hooks in a private sandbox; never call host init scripts."""
import io
import os
from pathlib import Path
import re
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]


def archive_bytes(archive, name):
    member = archive.extractfile(name)
    assert member is not None, f'missing package member: {name}'
    return member.read()


def postinst(package):
    text = (ROOT / 'packaging/openwrt' / package / 'Makefile').read_text()
    match = re.search(r'^define Package/' + re.escape(package) + r'/postinst\n(.*?)^endef$', text, re.M | re.S)
    if not match:
        raise AssertionError('package postinst not found')
    source = match.group(1).replace('$$', '$')
    # Optionally exercise exactly the hook emitted by the official SDK, not
    # just its Makefile source. Never extract package paths onto the host.
    if directory := os.environ.get('WIFI_GUARD_TEST_IPKS'):
        version_match = re.search(r'^PKG_VERSION:=(.+)$', text, re.M)
        release_match = re.search(r'^PKG_RELEASE:=(.+)$', text, re.M)
        assert version_match and release_match, 'missing package version'
        version, release = version_match.group(1), release_match.group(1)
        arch = 'all' if package.startswith('luci-') else 'aarch64_cortex-a53'
        path = Path(directory) / f'{package}_{version}-r{release}_{arch}.ipk'
        with tarfile.open(path) as ipk:
            with tarfile.open(fileobj=io.BytesIO(archive_bytes(ipk, './control.tar.gz'))) as control:
                metadata = archive_bytes(control, './control').decode()
                assert f'Source: packaging/openwrt/{package}\n' in metadata
                assert f'Version: {version}-r{release}\n' in metadata
                assert f'Architecture: {arch}\n' in metadata
                built = archive_bytes(control, './postinst-pkg').decode()
                assert built.strip() == source.strip(), 'SDK hook differs from Makefile'
                subprocess.run(['sh', '-n'], input=built, text=True, check=True)
                return built
    return source


class PackageHooks(unittest.TestCase):
    def run_luci(self, offline=False, failure=False):
        with tempfile.TemporaryDirectory(prefix='wifi-guard-package-') as directory:
            root = Path(directory)
            cache = root / 'tmp'
            cache.mkdir()
            for name in ('luci-indexcache', 'luci-indexcache.test', 'unrelated'):
                (cache / name).write_text('preserve unless LuCI cache')
            (cache / 'luci-modulecache').mkdir()
            (cache / 'luci-modulecache' / 'module').write_text('cached')
            log = root / 'rpcd.log'
            rpcd = root / 'rpcd'
            rpcd.write_text('#!/bin/sh\nprintf "%s\\n" "$*" >> "$HOOK_LOG"\nexit "${HOOK_FAILURE:-0}"\n')
            rpcd.chmod(0o700)
            source = postinst('luci-app-wifi-guard')
            self.assertIn('/etc/init.d/rpcd reload', source)
            # Substitute only the fixed production paths before executing the
            # real hook; an unexpected service/path is a test failure.
            paths = re.findall(r'/(?:etc|tmp|usr|sbin|lib)/[^\s;]+', source)
            self.assertEqual(set(paths), {
                '/etc/init.d/rpcd', '/tmp/luci-indexcache',
                '/tmp/luci-indexcache.*', '/tmp/luci-modulecache'})
            replacements = {
                '/etc/init.d/rpcd': str(rpcd),
                '/tmp/luci-indexcache': str(cache / 'luci-indexcache'),
                '/tmp/luci-indexcache.*': str(cache / 'luci-indexcache.*'),
                '/tmp/luci-modulecache': str(cache / 'luci-modulecache'),
            }
            # Replace only original tokens in one pass; inserted sandbox paths
            # can themselves contain /tmp/ and must not be rewritten again.
            source = re.sub(r'/(?:etc|tmp|usr|sbin|lib)/[^\s;]+',
                            lambda match: replacements[match.group(0)], source)
            env = dict(os.environ, IPKG_INSTROOT=str(root / 'image') if offline else '',
                       HOOK_LOG=str(log), HOOK_FAILURE='1' if failure else '0')
            result = subprocess.run(['sh', '-c', source], env=env, capture_output=True, text=True, timeout=5)
            self.assertEqual((cache / 'unrelated').read_text(), 'preserve unless LuCI cache')
            if offline:
                self.assertFalse(log.exists())
                self.assertTrue((cache / 'luci-indexcache').exists())
                self.assertTrue((cache / 'luci-indexcache.test').exists())
                self.assertTrue((cache / 'luci-modulecache' / 'module').exists())
            else:
                self.assertEqual(log.read_text(), 'reload\n')
                self.assertFalse((cache / 'luci-indexcache').exists())
                self.assertFalse((cache / 'luci-indexcache.test').exists())
                self.assertFalse((cache / 'luci-modulecache').exists())
            if not offline and not failure:
                # Run again in the same sandbox: caches are already absent.
                repeated = subprocess.run(['sh', '-c', source], env=env,
                                          capture_output=True, text=True, timeout=5)
                self.assertEqual(repeated.returncode, 0, repeated.stderr)
                self.assertEqual(log.read_text(), 'reload\nreload\n')
                self.assertEqual((cache / 'unrelated').read_text(), 'preserve unless LuCI cache')
            return result

    def test_live_install_and_repeat_refresh(self):
        self.assertEqual(self.run_luci().returncode, 0)

    def test_tmp_parent_paths_are_not_substituted_twice(self):
        # Reproduce a runner's /tmp paths without writing to the host /tmp.
        with tempfile.TemporaryDirectory(prefix='wifi-guard-runner-') as directory:
            parent = Path(directory) / 'tmp'
            parent.mkdir()
            with patch('tempfile.tempdir', str(parent)):
                self.assertEqual(self.run_luci().returncode, 0)
                self.assertNotEqual(self.run_luci(failure=True).returncode, 0)
                self.assertEqual(self.run_luci(offline=True).returncode, 0)

    def test_image_install_never_touches_host(self):
        self.assertEqual(self.run_luci(offline=True).returncode, 0)

    def test_reload_failure_is_visible_and_explains_gui_recovery(self):
        result = self.run_luci(failure=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('System > Startup', result.stderr)
        self.assertIn('rpcd', result.stderr)

    def test_package_dependencies_and_service_scope(self):
        luci = (ROOT / 'packaging/openwrt/luci-app-wifi-guard/Makefile').read_text()
        self.assertIn('DEPENDS:=+wifi-guard +luci-base +rpcd', luci)
        daemon = postinst('wifi-guard')
        self.assertNotRegex(daemon, r'/(?:etc/init.d|sbin)/')
        self.assertIn('IPKG_INSTROOT', daemon)


if __name__ == '__main__':
    unittest.main()
