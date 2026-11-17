import importlib.util
from pathlib import Path
import unittest
import tempfile
import sqlite3
import json
from unittest.mock import patch

spec=importlib.util.spec_from_file_location('experience_release',Path(__file__).with_name('experience.py'))
experience=importlib.util.module_from_spec(spec)
spec.loader.exec_module(experience)

class ExperienceReleaseTest(unittest.TestCase):
    def test_live_validation_requires_five_actual_meaningful_samples(self):
        self.assertFalse(experience.valid_live_result({'cases':[],'usages':[]},0))
        self.assertFalse(experience.valid_live_result({'cases':[{'checks':{}}]*5,'usages':[{}]*5},0))

    def test_missing_receipts_cannot_be_recorded_as_zero_calls(self):
        with tempfile.TemporaryDirectory() as folder:
            stage=Path(folder)
            (stage/'live-result.json').write_text(json.dumps({'container_exit_code':137,'usages':[],'report_missing':True}))
            with patch.object(experience,'STAGE',stage),patch.object(experience,'load_payload',return_value=({'commit':'a'*40},{})),patch.object(experience,'check_core'),patch.object(experience,'check_known_support'),patch.object(experience.r,'json_save') as save:
                with self.assertRaisesRegex(RuntimeError,'usage_evidence_missing'):
                    experience.record_costs()
                save.assert_not_called()

    def test_rollback_does_not_stop_unowned_name_collision(self):
        with tempfile.TemporaryDirectory() as folder:
            with patch.object(experience,'STAGE',Path(folder)),patch.object(experience.r,'docker',return_value=b'foreign-id') as docker,patch.object(experience.r,'inspect',return_value={'Id':'foreign-id','Config':{'Labels':{}}}),patch.object(experience.r,'replace_inside'),patch.object(experience.r,'json_save'):
                experience.rollback(b'original',None)
            self.assertFalse(any(args.args and args.args[0] in ['stop','update'] for args in docker.call_args_list))

    def test_routes_replace_only_support_and_keep_finance_and_models(self):
        original='''server { server_name ztapi.vip www.ztapi.vip;
add_header X-ZTAPI-UI-Source "https://github.com/ffff582/ztapi-source/tree/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" always;
set $ztapi_support http://ztapi-support-20261102:3012;
location /v1/ { proxy_pass http://server:3000; }
}
server { server_name admin.ztapi.vip;
add_header X-ZTAPI-UI-Source "https://github.com/ffff582/ztapi-source/tree/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" always;
set $ztapi_support http://ztapi-support-20261102:3012;
set $ztapi_support http://ztapi-support-20261102:3012;
set $ztapi_finance http://ztapi-finance-support-20261102-live:3011;
}'''
        text=experience.nginx_candidate(original,'b'*40)
        self.assertEqual(text.count('http://'+experience.NEW_SITE+':3012'),3)
        self.assertIn('http://ztapi-finance-support-20261102-live:3011',text)
        self.assertIn('location /v1/ { proxy_pass http://server:3000; }',text)
        self.assertEqual(text.count('location ^~ /internal/ { return 404; }'),2)

    def test_bot_arguments_have_no_customer_db_or_session_credentials(self):
        args=experience.bot_args('owned-image',Path('/private/data'),Path('/private/config.json'),False)
        joined=' '.join(args)
        self.assertIn('TELEGRAM_SUPPORT_ENABLED=false',joined)
        self.assertNotIn('ztapi_data',joined)
        self.assertNotIn('SUPPORT_READ_DSN',joined)
        self.assertNotIn('ZTAPI_SESSION_SIGNING_KEY',joined)
        self.assertNotIn('/root/.ssh',joined)

    def test_importer_is_offline_and_requires_stopped_receiver(self):
        with patch.object(experience.r,'inspect',return_value={'State':{'Running':True}}),patch.object(experience.r,'docker') as docker:
            with self.assertRaises(RuntimeError):
                experience.run_import('fixture-image',Path('/protected/manifest.json'),'a'*64,841126452)
            docker.assert_not_called()
        with patch.object(experience.r,'inspect',return_value={'State':{'Running':False}}),patch.object(experience.r,'docker',return_value=b'{"imported":5}') as docker:
            experience.run_import('fixture-image',Path('/protected/manifest.json'),'a'*64,841126452)
            args=' '.join(docker.call_args.args)
            self.assertIn('--network none',args)
            self.assertNotIn('ztapi_data',args)
            self.assertNotIn('SESSION_SIGNING_KEY',args)

    def test_failure_resumes_old_bot_without_restarting_main_or_finance(self):
        with patch.object(experience.r,'docker') as docker:
            experience.resume_old_bot()
            self.assertEqual(docker.call_args.args,('start',experience.OLD_BOT))
            self.assertNotIn('ztapi-server-1',str(docker.call_args_list))
            self.assertNotIn('ztapi-finance',str(docker.call_args_list))

    def test_unrecognized_route_baseline_stops(self):
        with self.assertRaises(RuntimeError):experience.nginx_candidate('server {}','a'*40)

    def test_rollback_suspends_private_owner_replies_but_keeps_operational_alerts(self):
        with tempfile.TemporaryDirectory() as folder:
            data=Path(folder)
            db=sqlite3.connect(data/'support.db')
            db.execute('CREATE TABLE telegram_support_outbox (chat_id INTEGER,kind TEXT,state TEXT)')
            db.executemany('INSERT INTO telegram_support_outbox VALUES (?,?,?)',[(841126452,'reply','pending'),(42,'notice','pending'),(841126452,'alert','pending'),(-100,'reply','pending')]);db.commit()
            with patch.object(experience,'BOT_DATA',data),patch.object(experience,'stop_owned_container',return_value=True,create=True),patch.object(experience.r,'docker',return_value=b''),patch.object(experience.r,'replace_inside'),patch.object(experience.r,'json_save'):
                experience.rollback(b'original',None)
            states=db.execute('SELECT state FROM telegram_support_outbox ORDER BY rowid').fetchall();db.close()
            self.assertEqual(states,[('upgrade_suspended',),('upgrade_suspended',),('pending',),('pending',)])

if __name__=='__main__':unittest.main()
