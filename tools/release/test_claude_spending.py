import copy
import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('feature_release', Path(__file__).with_name('claude_spending.py'))
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


class ReleaseBoundaryTests(unittest.TestCase):
    def test_linux_acceptance_precedes_creating_the_readonly_sidecar(self):
        calls = []
        with patch.object(release, 'prepare_main_stage', side_effect=lambda *_: calls.append('stage')), \
                patch.object(release.rolling, 'prepare', side_effect=lambda *_: calls.append('main') or {}), \
                patch.object(release.spending, 'prepare', side_effect=lambda *_, **__: calls.append('spending') or {}), \
                patch.object(release, 'save'):
            release.prepare({}, {'tests_passed': True, 'paid_model_calls': 0, 'commit': 'a' * 40})
        self.assertEqual(calls, ['stage', 'main', 'spending'])

    def test_route_drift_stops_before_rebasing_or_activating_main(self):
        with tempfile.TemporaryDirectory() as root, \
                patch.object(release, 'ROOT', Path(root)), \
                patch.object(release.spending, 'activate_route', return_value={'nginx_sha256': hashlib.sha256(b'owned-route').hexdigest()}), \
                patch.object(release.rolling, 'docker', return_value=b'foreign-edit'), \
                patch.object(release.rolling, 'inspect', return_value={}), \
                patch.object(release.rolling, 'activate') as activate, \
                patch.object(release.rolling, 'save') as save:
            (Path(root) / 'feature-prepared.json').write_text(json.dumps({'prepared': True, 'source_commit': 'a' * 40}))
            with self.assertRaisesRegex(RuntimeError, 'spending_route_drift'):
                release.activate({'containers': {}}, {'commit': 'a' * 40})
            save.assert_not_called()
            activate.assert_not_called()

    def test_main_configuration_never_uses_the_previous_release_stage(self):
        with tempfile.TemporaryDirectory() as root:
            release.configure_main(Path(root))
            self.assertEqual(release.rolling.STAGE, Path(root))
            self.assertEqual(release.rolling.RELEASE, 'claude-spend-20261102-r1')
            self.assertEqual(release.rolling.CANDIDATE, 'ztapi-claude-spend-candidate-r1')
            self.assertEqual(release.rolling.PREVIOUS, 'ztapi-server-before-claude-spend-r1')

    def test_nginx_rebase_preserves_all_existing_business_and_service_evidence(self):
        old = {'nginx_sha256': 'a' * 64, 'nginx_base64': 'old',
               'containers': {'ztapi-server-1': {'id': 'b' * 64, 'started_at': 'old'}},
               'main_secret_config_digest': 'c' * 64, 'unchanged_business_digests': {'channels': 'd' * 64}}
        before = copy.deepcopy(old)
        info = {'Id': 'e' * 64, 'Image': 'sha256:' + 'f' * 64,
                'Config': {'Labels': {'ztapi.spending.release': release.spending.RELEASE}},
                'State': {'Running': True, 'StartedAt': '2026-11-02T10:00:00Z'}}
        new = release.rebase_for_spending(old, b'new route', info)
        self.assertEqual(old, before)
        self.assertEqual(new['nginx_sha256'], hashlib.sha256(b'new route').hexdigest())
        self.assertEqual(new['main_secret_config_digest'], before['main_secret_config_digest'])
        self.assertEqual(new['unchanged_business_digests'], before['unchanged_business_digests'])
        self.assertEqual(new['containers']['ztapi-server-1'], before['containers']['ztapi-server-1'])
        self.assertEqual(new['containers'][release.SPENDING]['id'], info['Id'])

    def test_nginx_rebase_requires_a_running_owned_sidecar(self):
        with self.assertRaises(RuntimeError):
            release.rebase_for_spending({'containers': {}}, b'route', {'State': {'Running': False}})

    def test_main_image_context_contains_only_verified_binary_and_recipe(self):
        with tempfile.TemporaryDirectory() as root, patch.object(release.rolling, 'docker', return_value=b'') as docker:
            release.configure_main(Path(root))
            raw = b'fixture binary'
            (Path(root) / 'ztapi-linux-amd64').write_bytes(raw)
            (Path(root) / 'mysql-before.sql.gz').write_bytes(b'private backup must not be sent')
            original = {'Image': 'sha256:' + 'a' * 64, 'Config': {'Env': ['PROVIDER_API_KEY=private-fixture']}}
            build = {'binary_sha256': hashlib.sha256(raw).hexdigest(), 'commit': 'b' * 40}
            image = release.build_main_image(original, build)
            context = Path(root) / 'main-build'
            self.assertEqual({p.name for p in context.iterdir()}, {'Dockerfile', 'ztapi-linux-amd64'})
            recipe = (context / 'Dockerfile').read_text()
            self.assertNotIn('--chmod', recipe)
            self.assertNotIn('RUN ', recipe)
            self.assertNotIn('private-fixture', recipe)
            self.assertIn('COPY ztapi-linux-amd64 /usr/local/bin/ztapi', recipe)
            self.assertEqual(recipe.splitlines(), [
                'FROM sha256:' + 'a' * 64,
                'COPY ztapi-linux-amd64 /usr/local/bin/ztapi',
                'LABEL org.opencontainers.image.revision="' + 'b' * 40 + '"',
            ])
            self.assertEqual(docker.call_args.args, ('build', '--network=none', '--pull=false', '-f', str(context / 'Dockerfile'), '-t', image, str(context)))

    def test_bad_binary_cannot_build_an_image(self):
        with tempfile.TemporaryDirectory() as root, patch.object(release.rolling, 'docker') as docker:
            release.configure_main(Path(root))
            (Path(root) / 'ztapi-linux-amd64').write_bytes(b'bad')
            with self.assertRaises(RuntimeError):
                release.build_main_image({'Image': 'sha256:' + 'a' * 64}, {'binary_sha256': '0' * 64, 'commit': 'b' * 40})
            docker.assert_not_called()

    def test_controller_acceptance_is_scoped_offline_and_all_binaries_are_verified(self):
        with tempfile.TemporaryDirectory() as root, \
                patch.object(release, '_base_isolated_tests', return_value={}), \
                patch.object(release.rolling, 'docker', return_value=b'PASS\n') as docker:
            release.configure_main(Path(root))
            names = ['claude-stability.test', 'controller-stability.test', 'spending.test', 'spending-audit.test']
            build = {'test_binaries': {}}
            for name in names:
                raw = name.encode()
                (Path(root) / name).write_bytes(raw)
                build['test_binaries'][name] = hashlib.sha256(raw).hexdigest()
            result = release.isolated_tests('pinned-image', build)
            self.assertEqual(set(result), set(names))
            self.assertEqual(docker.call_count, 4)
            for call in docker.call_args_list:
                args = call.args
                self.assertIn('--network=none', args)
                self.assertIn('--read-only', args)
                self.assertIn('--cap-drop=ALL', args)
                self.assertIn('--memory=256m', args)
                self.assertNotIn('-e', args)
                self.assertNotIn('--env-file', args)
                scoped = '-test.run=^TestClaudeStability' in args
                self.assertEqual(scoped, any('controller-stability.test' in value for value in args))


if __name__ == '__main__':
    unittest.main()
