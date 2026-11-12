import importlib.util
from pathlib import Path
import unittest
import tempfile
import json
import os
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('support_release', Path(__file__).with_name('release.py'))
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


class ReleaseTest(unittest.TestCase):
    def test_public_ca_bundle_is_readable_by_nonroot_under_private_umask(self):
        with tempfile.TemporaryDirectory() as folder:
            source, destination = Path(folder) / 'source.crt', Path(folder) / 'image.crt'
            source.write_bytes(b'public-ca-fixture')
            previous = os.umask(0o077)
            try:
                release.copy_public_ca(source, destination)
            finally:
                os.umask(previous)
            self.assertEqual(destination.read_bytes(), source.read_bytes())
            if os.name != 'nt':
                self.assertEqual(destination.stat().st_mode & 0o777, 0o644)

    def test_failed_activation_stops_enabled_bot_and_keeps_finance_cost_reader(self):
        metadata = {'Config': {'Labels': {'ztapi.support.release': '20261102'}, 'Env': ['TELEGRAM_SUPPORT_ENABLED=true']}, 'State': {'Running': True}}
        original = b'set $ztapi_finance http://ztapi-finance:3011;'
        with patch.object(release, 'inspect', return_value=metadata), patch.object(release, 'docker') as docker, \
                patch.object(release, 'replace_inside') as replace, patch.object(release, 'json_save') as saved:
            release.rollback_activation(original, {'public': b'old public'}, release.FINANCE, 'telegram_start')
            self.assertIn(('stop', '--time', '75', release.TELEGRAM), [c.args for c in docker.call_args_list])
            config = [c.args[1] for c in replace.call_args_list if c.args[0] == release.CONF][0]
            self.assertIn(('http://' + release.FINANCE + ':3011').encode(), config)
            self.assertNotIn(b'/api/support/', config)
            self.assertEqual(saved.call_args.args[0], 'rolled-back.json')

    def test_finance_candidate_is_readonly_but_live_preserves_original_policy(self):
        original = {'FINANCE_READ_DSN': 'fixture-only', 'FINANCE_REPORT_ONLY': 'false'}
        self.assertEqual(release.finance_environment(original, 'a' * 40, candidate=True)['FINANCE_REPORT_ONLY'], 'true')
        self.assertEqual(release.finance_environment(original, 'a' * 40, candidate=False)['FINANCE_REPORT_ONLY'], 'false')
        self.assertNotIn('FINANCE_REPORT_ONLY', release.finance_environment({}, 'a' * 40, candidate=False))
        self.assertEqual(original['FINANCE_REPORT_ONLY'], 'false')

    def test_nginx_change_does_not_redirect_model_traffic_or_open_public_admin(self):
        original = '''server { server_name ztapi.vip www.ztapi.vip;
    location ^~ /api/admin/ { return 404; }
    location /v1/ { proxy_pass http://server:3000; }
}
server { server_name admin.ztapi.vip;
    set $ztapi_finance http://ztapi-finance:3011;
    location /api/ { proxy_pass http://server:3000; }
}'''
        updated = release.nginx_candidate(original, 'a' * 40)
        self.assertEqual(updated.count('location ^~ /api/support/'), 2)
        self.assertEqual(updated.count('location ^~ /api/admin/support/'), 1)
        public, admin = updated.split('server { server_name admin.ztapi.vip;')
        self.assertNotIn('location ^~ /api/admin/support/', public)
        self.assertIn('location ^~ /api/admin/ { return 404; }', public)
        self.assertIn('location /v1/ { proxy_pass http://server:3000; }', updated)
        self.assertIn('http://' + release.FINANCE_LIVE + ':3011', admin)
        self.assertEqual(updated.count('proxy_next_upstream off;'), 3)

    def test_unknown_nginx_baseline_is_rejected(self):
        with self.assertRaises(RuntimeError):
            release.nginx_candidate('server {}', 'a' * 40)

    def test_telegram_candidate_has_no_main_database_network_or_session_credentials(self):
        with patch.object(release, 'docker') as docker, patch.object(release, 'wait_health'):
            release.create_telegram('fixture-owned-image', False)
            calls = [call.args for call in docker.call_args_list]
            create = calls[0]
            self.assertIn(str(release.TELEGRAM_ROOT / 'candidate.env'), create)
            self.assertNotIn('ztapi_data', str(calls))
            self.assertNotIn('ZTAPI_SESSION_SIGNING_KEY', str(calls))
            self.assertNotIn('SUPPORT_READ_DSN', str(calls))
            self.assertNotIn('/opt/ztapi-finance', str(calls))
            self.assertIn(('network', 'connect', 'ztapi_egress', release.TELEGRAM), calls)

    def test_initial_backup_failure_does_not_activate_routes_or_pages(self):
        with tempfile.TemporaryDirectory() as folder:
            stage = Path(folder)
            root = stage / 'private'
            root.mkdir()
            (stage / 'acceptance.json').write_text(json.dumps({'passed': True}))
            (stage / 'browser-qa.json').write_text(json.dumps({'passed': True}))
            (stage / 'backup.py').write_text('# fixture')
            with patch.object(release, 'STAGE', stage), patch.object(release, 'ROOT', root), \
                    patch.object(release, 'validate_payload', return_value=({'commit': 'a' * 40}, {})), \
                    patch.object(release, 'check_baseline'), patch.object(release, 'docker', return_value=b'original config') as docker, \
                    patch.object(release, 'rollback_activation') as rollback, \
                    patch.object(release, 'run', side_effect=OSError('fixture backup failed')):
                with self.assertRaises(OSError):
                    release.activate()
                rollback.assert_called_once_with(b'original config', {}, release.FINANCE, 'backups')
                self.assertEqual(docker.call_count, 1)
                self.assertEqual(docker.call_args.args, ('exec', release.NGINX, 'cat', release.CONF))

    def test_finance_creation_failure_is_protected_and_does_not_stop_fallback_reader(self):
        with tempfile.TemporaryDirectory() as folder:
            stage = Path(folder)
            for name in ['acceptance.json', 'browser-qa.json']:
                (stage / name).write_text(json.dumps({'passed': True}))
            with patch.object(release, 'STAGE', stage), \
                    patch.object(release, 'validate_payload', return_value=({'commit': 'a' * 40}, {})), \
                    patch.object(release, 'check_baseline'), patch.object(release, 'install_backups'), \
                    patch.object(release, 'stage_static_entries', return_value={'public': b'old'}), \
                    patch.object(release, 'docker', return_value=b'original') as docker, \
                    patch.object(release, 'create_support'), \
                    patch.object(release, 'create_finance', side_effect=RuntimeError('fixture finance create failure')), \
                    patch.object(release, 'rollback_activation') as rollback:
                with self.assertRaises(RuntimeError):
                    release.activate()
                rollback.assert_called_once_with(b'original', {'public': b'old'}, release.FINANCE, 'finance_runtime')
                self.assertFalse(any(c.args and c.args[0] in ['stop', 'rename'] and release.FINANCE in c.args for c in docker.call_args_list))


if __name__ == '__main__':
    unittest.main()
