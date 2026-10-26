import hashlib
import importlib.util
import io
from pathlib import Path
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('finance_install', Path(__file__).with_name('install.py'))
install = importlib.util.module_from_spec(spec)
spec.loader.exec_module(install)


class InstallTests(unittest.TestCase):
    def test_first_nginx_copy_failure_restores_original_configuration(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            release = root / 'release'
            release.mkdir()
            (release / 'ztapi-finance').write_bytes(b'local-test-binary')
            host = root / 'host.conf'
            host.write_text('server {\n    server_name admin.ztapi.vip;\n}\n')
            restored = []

            def inspect(name):
                if name == 'ztapi-server-1':
                    return {'Id': install.EXPECTED_MAIN, 'Image': install.EXPECTED_IMAGE, 'State': {'StartedAt': 'unchanged'}, 'Config': {'Env': ['ZTAPI_SESSION_SIGNING_KEY=' + 'x' * 64]}}
                if name == 'ztapi-mysql-1':
                    return {'Config': {'Env': ['MYSQL_DATABASE=ztapi']}}
                return {'NetworkSettings': {'Networks': {'ztapi_app': {'IPAddress': '127.0.0.1'}}}}

            def run(args, **kwargs):
                if args[:4] == ['docker', 'exec', 'ztapi-nginx-1', 'cat']:
                    return SimpleNamespace(stdout=host.read_bytes())
                if args[:2] == ['docker', 'cp']:
                    if args[2].startswith('ztapi-nginx-1:'):
                        Path(args[3]).write_text('old admin index')
                    elif Path(args[2]).name == 'default.next.conf':
                        raise subprocess.CalledProcessError(1, args)
                    elif Path(args[2]).name == 'default.conf':
                        restored.append(args)
                return SimpleNamespace(stdout=b'')

            fake_opener = SimpleNamespace(open=lambda *args, **kwargs: io.BytesIO(b'{"ok":true}'))
            with patch.object(install, 'ROOT', root), patch.object(install, '__file__', str(release / 'install.py')), patch.object(install, 'HOST_NGINX_CONFIG', host, create=True), patch.object(install, 'inspect', inspect), patch.object(install, 'run', run), patch.object(install, 'sql', lambda _: 'GRANT SELECT ON ztapi.*'), patch.object(install.os, 'chown', lambda *args: None, create=True), patch.object(install.urllib.request, 'build_opener', lambda *args: fake_opener), patch.object(sys, 'argv', ['install.py', 'a' * 40, hashlib.sha256(b'local-test-binary').hexdigest()]):
                with self.assertRaises(subprocess.CalledProcessError):
                    install.main()
            self.assertEqual(len(restored), 1)


if __name__ == '__main__':
    unittest.main()
