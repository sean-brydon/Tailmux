#!/usr/bin/env python3
"""Offline installer integration tests. Network requests are replaced with fixtures."""
import hashlib
import io
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest

INSTALLER = Path(__file__).resolve().parents[1] / 'install.sh'

class InstallerTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='tailmux installer ')
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.mock = self.root / 'mock'
        self.mock.mkdir()
        self.fixtures = self.root / 'fixtures'
        self.fixtures.mkdir()
        self.dest = self.root / 'installed binaries'
        self.env = dict(os.environ, PATH=str(self.mock) + os.pathsep + os.environ['PATH'],
                        FIXTURES=str(self.fixtures), MOCK_OS='Linux', MOCK_ARCH='x86_64')
        self.write_exe('uname', '#!/bin/sh\ncase "$1" in -s) echo "$MOCK_OS";; -m) echo "$MOCK_ARCH";; esac\n')
        self.write_exe('curl', '''#!/usr/bin/env python3
import os, pathlib, shutil, sys
args=sys.argv[1:]
url=next(x for x in args if x.startswith('https://'))
if url.endswith('/releases/latest'):
 print('https://github.com/sean-brydon/Tailmux/releases/tag/v0.1.0', end='')
 sys.exit(0)
if '/releases/download/v0.1.0/' not in url: sys.exit(22)
source=pathlib.Path(os.environ['FIXTURES'])/url.rsplit('/',1)[-1]
if not source.exists(): sys.exit(22)
shutil.copyfile(source, args[args.index('-o')+1])
''')
        self.binary = b'#!/bin/sh\nprintf "tailmux v0.1.0\\n"\n'
        self.make_archive()

    def write_exe(self, name, text):
        p=self.mock/name
        p.write_text(text)
        p.chmod(0o755)

    def make_archive(self, asset='tailmux_linux_amd64.tar.gz', symlink=False):
        archive=self.fixtures/asset
        with tarfile.open(archive,'w:gz') as tar:
            member=tarfile.TarInfo('tailmux')
            member.mode=0o755
            if symlink:
                member.type=tarfile.SYMTYPE
                member.linkname='/bin/sh'
                tar.addfile(member)
            else:
                member.size=len(self.binary)
                tar.addfile(member,io.BytesIO(self.binary))
        digest=hashlib.sha256(archive.read_bytes()).hexdigest()
        (self.fixtures/'checksums.txt').write_text(f'{digest}  {asset}\n')

    def install(self, *args):
        return subprocess.run(['sh',str(INSTALLER),'--bin-dir',str(self.dest),*args],
                              env=self.env,text=True,capture_output=True,timeout=10)

    def test_pinned_release_and_path_with_spaces(self):
        r=self.install('--version','v0.1.0')
        self.assertEqual(r.returncode,0,r.stderr)
        self.assertEqual((self.dest/'tailmux').read_bytes(),self.binary)
        self.assertTrue(os.access(self.dest/'tailmux',os.X_OK))
        self.assertEqual(subprocess.check_output([str(self.dest/'tailmux')],text=True),'tailmux v0.1.0\n')

    def test_latest_resolves_to_specific_tag(self):
        r=self.install()
        self.assertEqual(r.returncode,0,r.stderr)
        self.assertIn('v0.1.0',r.stdout)

    def test_macos_arm64(self):
        self.env.update(MOCK_OS='Darwin',MOCK_ARCH='arm64')
        self.make_archive('tailmux_darwin_arm64.tar.gz')
        r=self.install('--version','v0.1.0')
        self.assertEqual(r.returncode,0,r.stderr)

    def test_checksum_mismatch_preserves_existing_binary(self):
        self.dest.mkdir()
        old=self.dest/'tailmux'
        old.write_bytes(b'existing installation')
        (self.fixtures/'tailmux_linux_amd64.tar.gz').write_bytes(b'tampered')
        r=self.install('--version','v0.1.0')
        self.assertNotEqual(r.returncode,0)
        self.assertIn('checksum mismatch',r.stderr)
        self.assertEqual(old.read_bytes(),b'existing installation')

    def test_duplicate_checksum_rejected(self):
        p=self.fixtures/'checksums.txt'
        p.write_text(p.read_text()*2)
        r=self.install('--version','v0.1.0')
        self.assertNotEqual(r.returncode,0)
        self.assertFalse(self.dest.exists())

    def test_missing_asset(self):
        (self.fixtures/'tailmux_linux_amd64.tar.gz').unlink()
        r=self.install('--version','v0.1.0')
        self.assertNotEqual(r.returncode,0)
        self.assertFalse(self.dest.exists())

    def test_symlink_is_not_installed(self):
        self.make_archive(symlink=True)
        r=self.install('--version','v0.1.0')
        self.assertNotEqual(r.returncode,0)
        self.assertIn('regular file',r.stderr)

    def test_invalid_inputs(self):
        for value in ['../main','v0.1.0;id','']:
            r=self.install('--version',value)
            self.assertNotEqual(r.returncode,0)
        self.env['MOCK_OS']='Windows'
        r=self.install('--version','v0.1.0')
        self.assertNotEqual(r.returncode,0)
        self.assertFalse(self.dest.exists())

if __name__=='__main__': unittest.main(verbosity=2)
