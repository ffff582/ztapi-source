import importlib.util
import copy
import io
import json
from pathlib import Path
import tempfile
import time
import unittest
from contextlib import ExitStack
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('release', Path(__file__).with_name('longtext_security.py'))
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


class OfflineTestCase(unittest.TestCase):
    def setUp(self):
        for name in ('run', 'api'):
            guard = patch.object(release, name, side_effect=AssertionError('unmocked external operation: ' + name))
            guard.start()
            self.addCleanup(guard.stop)


class RolloutTests(OfflineTestCase):
    def test_both_roles_skip_only_startup_fx_and_candidate_drops_compose_ownership(self):
        original = {
            'Config': {'Env': ['NODE_TYPE=master'], 'Labels': {'com.docker.compose.service': 'server', 'owner': 'keep'}},
            'HostConfig': {'NetworkMode': 'app'},
            'NetworkSettings': {'Networks': {'app': {'IPAddress': '172.30.0.3', 'Aliases': ['server']}}},
        }
        for temporary in (False, True):
            cloned = release.clone(original, 'new', 'a' * 40, temporary)
            self.assertIn('ZTAPI_SKIP_STARTUP_FX_REFRESH=true', cloned['Env'])
            self.assertIn('ZTAPI_SOURCE_TAG=production-' + release.RELEASE, cloned['Env'])
            if temporary:
                self.assertNotIn('com.docker.compose.service', cloned['Labels'])
            self.assertEqual(cloned['Labels']['owner'], 'keep')

    def test_receipts_can_be_reused_but_not_silently_overwritten(self):
        with tempfile.TemporaryDirectory() as root, patch.object(release, 'STAGE', Path(root)), patch.object(release.os, 'O_NOFOLLOW', 0, create=True):
            release.save('proof.json', {'phase': 'ready'})
            release.save('proof.json', {'phase': 'ready'})
            with self.assertRaises(RuntimeError):
                release.save('proof.json', {'phase': 'different'})

    def test_internal_catalog_listener_is_ip_bound_exact_path_and_allowlisted(self):
        bindings = [{'ip': '172.30.0.5'}, {'ip': '172.30.0.6'}]
        config = release.catalog_config(b'proxy_pass http://candidate:3000;\n', 'candidate', '172.30.0.2', bindings)
        self.assertIn(b'listen 172.30.0.2:3000;', config)
        self.assertIn(b'location = /api/pricing', config)
        self.assertIn(b'allow 172.30.0.5;', config)
        self.assertIn(b'allow 172.30.0.6;', config)
        self.assertIn(b'deny all;', config)
        self.assertIn(b'location / { return 404; }', config)
        self.assertNotIn(b'listen 3000;', config)

    def test_hosts_mapping_preserves_other_aliases_and_original_can_be_restored_exactly(self):
        original = b'127.0.0.1\tlocalhost\n172.30.0.3\tztapi-server-1 other # keep\n'
        mapped = release.mapped_hosts(original, '172.30.0.2')
        self.assertIn(b'172.30.0.3\tother # keep\n', mapped)
        self.assertIn(b'172.30.0.2\tztapi-server-1', mapped)
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / 'hosts'
            path.write_bytes(original)
            with patch.object(release, 'hosts_path', return_value=path, create=True):
                release.hosts_cas('sidecar', original, mapped)
                release.hosts_cas('sidecar', mapped, original)
                self.assertEqual(path.read_bytes(), original)
                path.write_bytes(b'operator changed this')
                with self.assertRaises(RuntimeError):
                    release.hosts_cas('sidecar', original, mapped)
                self.assertEqual(path.read_bytes(), b'operator changed this')

    def test_socket_guard_includes_non_established_clients_and_private_provider_ports(self):
        def row(local, remote, state):
            return f'0: {local} {remote} {state} 0 0 0 0 0 0\n'
        # LISTEN/TIME_WAIT are not active work; CLOSE_WAIT/SYN_SENT/FIN_WAIT are.
        table = 'header\n' + row('03001EAC:0BB8', '05001EAC:C350', '08')
        table += row('03001EAC:0BB8', '00000000:0000', '0A')
        table += row('03001EAC:C351', '07001EAC:20FB', '02')
        table += row('03001EAC:C352', '08080808:04D2', '04')
        table += row('03001EAC:C353', '07001EAC:20FB', '06')
        self.assertEqual(release.count_sockets(table, 'tcp', {'172.30.0.7'}), (1, 2))

    def test_disabled_channel_dns_is_not_consulted_but_active_private_addresses_are_kept(self):
        def sql(query):
            if 'status = 1' in query:
                return b'http://172.30.0.7:8443\nhttps://active.example\n'
            return b'https://retired-docker.invalid\n'
        def resolve(host, *args, **kwargs):
            if host != 'active.example':
                raise release.socket.gaierror('retired DNS unavailable')
            return [(release.socket.AF_INET, release.socket.SOCK_STREAM, 6, '', ('10.2.3.4', 0))]
        with patch.object(release, 'sql', side_effect=sql), patch.object(release.socket, 'getaddrinfo', side_effect=resolve):
            self.assertEqual(release.upstream_addresses(), {'172.30.0.7', '10.2.3.4'})

    def test_unknown_private_upstream_is_counted_without_counting_known_db_redis(self):
        raw = 'header\n0: 03001EAC:C350 07001EAC:1F90 01\n1: 03001EAC:C351 09001EAC:0CEA 01\n'
        infrastructure = {('172.30.0.9', 3306)}
        self.assertEqual(release.count_sockets(raw, 'tcp', set(), infrastructure), (0, 1))

    def test_readonly_active_guard_tracks_only_pre_cut_reserved_rows_not_legacy_pending(self):
        queries = []
        def sql(query):
            queries.append(query)
            return b'0\n'
        with patch.object(release, 'sql', side_effect=sql):
            self.assertTrue(release.settlements_idle({'max_id': 12, 'started_at': '2026-11-02 00:00:00'}))
        self.assertIn("status = 'reserved'", queries[0])
        self.assertIn('id <= 12', queries[0])
        self.assertNotIn("status = 'pending'", queries[0])

    def test_preflight_accepts_positive_pboc_rate_and_requires_startup_skip(self):
        answers = iter((b'1\n', b'7.1200000000\n', b'0\n', b'0\n', b'0\n'))
        with patch.object(release, 'sql', side_effect=lambda _: next(answers)):
            release.startup_preflight({'Config': {'Env': ['ZTAPI_SKIP_STARTUP_FX_REFRESH=true', 'ZTAPI_HEALTH_VERIFICATION_PROBES_ENABLED=false']}})
        with self.assertRaises(RuntimeError):
            release.startup_preflight({'Config': {'Env': []}})

    def test_preflight_rejects_automatic_work_without_changing_environment(self):
        safe = ['ZTAPI_SKIP_STARTUP_FX_REFRESH=true', 'ZTAPI_HEALTH_VERIFICATION_PROBES_ENABLED=false']
        for extra in ('CHANNEL_TEST_FREQUENCY=1', 'CHANNEL_UPDATE_FREQUENCY=0', 'ZTAPI_HEALTH_SYNTHETIC_PROBES_ENABLED=true', 'ZTAPI_HEALTH_VERIFICATION_PROBES_ENABLED=true'):
            container = {'Config': {'Env': [value for value in safe if value.split('=')[0] != extra.split('=')[0]] + [extra]}}
            before = copy.deepcopy(container)
            with patch.object(release, 'sql', side_effect=[b'1', b'7.1', b'0', b'0', b'0']), self.assertRaises(RuntimeError, msg=extra):
                release.startup_preflight(container)
            self.assertEqual(container, before)
        for changed in range(3):
            values = [b'1', b'7.1', b'0', b'0', b'0']
            values[changed + 2] = b'1'
            with patch.object(release, 'sql', side_effect=values), self.assertRaises(RuntimeError):
                release.startup_preflight({'Config': {'Env': safe[:]}})

    def test_candidate_restart_promotion_refuses_foreign_or_master_container(self):
        for config in ({'Env': ['NODE_TYPE=slave'], 'Labels': {}}, {'Env': ['NODE_TYPE=master'], 'Labels': {'ztapi.security.release': release.RELEASE}}):
            with patch.object(release, 'inspect', return_value={'Config': config}), patch.object(release, 'api') as api, self.assertRaises(RuntimeError):
                release.candidate_recovery_policy('foreign')
            api.assert_not_called()

    def test_static_archive_verifies_every_manifest_file_before_live_changes(self):
        import tarfile
        files = {'index.html': b'index', 'assets/app-123.js': b'javascript'}
        with tempfile.TemporaryDirectory() as root, patch.object(release, 'STAGE', Path(root)):
            with tarfile.open(Path(root) / 'public-dist.tar.gz', 'w:gz') as archive:
                for name, raw in files.items():
                    info = tarfile.TarInfo(name)
                    info.size = len(raw)
                    archive.addfile(info, io.BytesIO(raw))
            build = {'public_files': {name: release.sha(raw) for name, raw in files.items()}}
            release.validate_static(build)
            build['public_files']['assets/app-123.js'] = '0' * 64
            with patch.object(release, 'docker') as docker, self.assertRaises(RuntimeError):
                release.validate_static(build)
            docker.assert_not_called()

    def test_static_verification_on_python_310_without_file_digest(self):
        import tarfile
        raw = b'portable static verification'
        with tempfile.TemporaryDirectory() as root, patch.object(release, 'STAGE', Path(root)), patch.dict(release.hashlib.__dict__):
            release.hashlib.__dict__.pop('file_digest', None)
            with tarfile.open(Path(root) / 'public-dist.tar.gz', 'w:gz') as archive:
                info = tarfile.TarInfo('index.html')
                info.size = len(raw)
                archive.addfile(info, io.BytesIO(raw))
            build = {'public_files': {'index.html': release.sha(raw)}}
            release.validate_static(build)
            build['public_files']['index.html'] = '0' * 64
            with self.assertRaisesRegex(RuntimeError, 'static_file_hash_mismatch'):
                release.validate_static(build)

    def test_image_build_is_legacy_compatible_and_excludes_private_context(self):
        raw = b'offline verified binary fixture'
        with tempfile.TemporaryDirectory() as root, patch.object(release, 'STAGE', Path(root)), patch.object(release, 'docker', return_value=b'') as docker:
            (Path(root) / 'ztapi-linux-amd64').write_bytes(raw)
            image = release.build_image({'Image': 'sha256:' + 'a' * 64}, {'binary_sha256': release.sha(raw), 'commit': 'b' * 40})
            recipe = (Path(root) / 'main.Dockerfile').read_text()
            self.assertIn('COPY ztapi-linux-amd64 /usr/local/bin/ztapi\n', recipe)
            self.assertNotIn('--chmod', recipe)
            self.assertNotIn('RUN ', recipe)
            self.assertEqual((Path(root) / '.dockerignore').read_text(), '**\n!main.Dockerfile\n!ztapi-linux-amd64\n')
            self.assertEqual(docker.call_args.args, ('build', '--network=none', '--pull=false', '-f', str(Path(root) / 'main.Dockerfile'), '-t', image, str(Path(root))))

    def test_isolated_tests_have_no_runtime_secrets_data_or_network(self):
        calls = []
        with tempfile.TemporaryDirectory() as root, patch.object(release, 'STAGE', Path(root)):
            binaries = {}
            for name in ('common-security.test', 'types-security.test'):
                (Path(root) / name).write_bytes(name.encode())
                binaries[name] = release.sha(name.encode())
            with patch.object(release, 'inspect', return_value={'Config': {'Env': ['PATH=/bin']}}), patch.object(release, 'docker', side_effect=lambda *a, **k: calls.append(a) or b'PASS\n'):
                release.isolated_tests('sha256:image', {'test_binaries': binaries})
        self.assertEqual(len(calls), 2)
        for args in calls:
            self.assertEqual(args[:2], ('run', '--rm'))
            for flag in ('--read-only', '--cap-drop=ALL', '--security-opt=no-new-privileges'):
                self.assertIn(flag, args)
            self.assertIn('--network=none', args)
            self.assertNotIn('--env', args)
            self.assertNotIn('--env-file', args)
            self.assertEqual(args.count('--mount'), 1)
            self.assertIn('--entrypoint', args)
            self.assertTrue(any('dst=/security.test,readonly' in value for value in args))

    def test_monitor_failure_blocks_stop_without_cancelling_requests(self):
        monitor = release.AvailabilityMonitor([], duration=600)
        with patch.object(release, 'run', return_value=b'503'), patch.object(release, 'docker') as docker:
            monitor.probe()
            with self.assertRaises(RuntimeError):
                monitor.check()
            docker.assert_not_called()
        self.assertEqual(monitor.snapshot()['public:503'], 1)

    def test_monitor_public_probe_bypasses_host_proxy_settings(self):
        with patch.object(release, 'run', return_value=b'200') as run:
            release.AvailabilityMonitor([]).probe()
        self.assertIn('--noproxy', run.call_args.args[0])

    def test_monitor_catalog_probe_uses_hostname_inside_sidecar(self):
        with patch.object(release, 'run', return_value=b'200'), patch.object(release, 'docker', return_value=b'200') as docker:
            monitor = release.AvailabilityMonitor([{'id': 'support'}])
            monitor.probe()
            monitor.check()
        command = ' '.join(str(value) for value in docker.call_args.args)
        self.assertIn('http://ztapi-server-1:3000/api/pricing', command)
        self.assertNotIn('http://172.', command)

    def test_scratch_sidecar_probe_uses_its_network_namespace_and_verified_hosts(self):
        binding = {'id': 'support', 'pid': 123, 'original_target': '172.30.0.3'}
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / 'hosts'
            path.write_bytes(b'172.30.0.2 ztapi-server-1\n')
            with patch.object(release, 'hosts_path', return_value=path, create=True), patch.object(release, 'run', return_value=b'200') as run, patch.object(release, 'docker') as docker:
                monitor = release.AvailabilityMonitor([binding])
                monitor.probe()
                monitor.check()
            command = run.call_args.args[0]
            self.assertEqual(command[:4], ['nsenter', '--target', '123', '--net'])
            self.assertIn('ztapi-server-1:3000:172.30.0.2', command)
            docker.assert_not_called()

    def test_database_error_guard_catches_driver_and_create_failures(self):
        for text in ('driver: bad connection', 'sql: connection is already closed', 'Error 1364: field has no default', '[error] failed to Create', 'invalid connection', 'database is locked', 'UNIQUE constraint failed: quota_data.id', 'failed to insert quota_data'):
            self.assertTrue(release.database_errors(text), text)
        self.assertFalse(release.database_errors('record not found\n[rows:0] SELECT 1'))

    def test_scoped_settlement_log_check_does_not_require_legacy_pending_delivery(self):
        with patch.object(release, 'sql', return_value=b'0\n') as sql:
            self.assertTrue(release.settlement_logs_consistent({'reserved_ids': [11, 12]}))
        query = sql.call_args.args[0]
        self.assertIn('s.id IN (11,12)', query)
        self.assertIn("s.status = 'settled'", query)
        self.assertIn('ztapi_settlement_log_outboxes', query)
        self.assertIn('l.quota <> s.charged_quota', query)

    def test_statistics_readback_is_scoped_readonly_and_not_a_balance_mutation(self):
        with patch.object(release, 'sql', return_value=b'3\t10\t20\n') as sql:
            self.assertEqual(release.statistics_readback({'reserved_ids': [11]}), {'count': 3, 'quota': 10, 'tokens': 20})
        self.assertIn('FROM quota_data q', sql.call_args.args[0])
        self.assertIn('s.id IN (11)', sql.call_args.args[0])

    def test_create_saves_partial_container_id_before_connecting_any_extra_network(self):
        responses = iter(({'Id': 'partial-primary'}, RuntimeError('connect failed')))
        def api(*args, **kwargs):
            value = next(responses)
            if isinstance(value, Exception):
                raise value
            return value
        value = {'Labels': {'ztapi.security.release': release.RELEASE}, 'HostConfig': {'NetworkMode': 'app'},
                 'NetworkingConfig': {'EndpointsConfig': {'app': {}, 'data': {}}}}
        with tempfile.TemporaryDirectory() as root, patch.object(release, 'STAGE', Path(root)), patch.object(release, 'api', side_effect=api):
            with self.assertRaises(RuntimeError):
                release.create(release.MAIN, value)
            receipt = json.loads((Path(root) / ('created-' + release.MAIN + '.json')).read_bytes())
            self.assertEqual(receipt['id'], 'partial-primary')

    def test_reinstall_same_nginx_proposal_is_reusable_and_drift_is_rejected(self):
        current = [b'original']
        def docker(*args, **kwargs):
            if args[:3] == ('exec', release.NGINX, 'cat'):
                return current[0]
            if kwargs.get('data') is not None:
                current[0] = kwargs['data']
            return b''
        with tempfile.TemporaryDirectory() as root, patch.object(release, 'STAGE', Path(root)), patch.object(release, 'docker', side_effect=docker), patch.object(release, 'nginx_workers', return_value=[10]), patch.object(release, 'wait_reload'):
            release.install_nginx(b'candidate', b'original')
            release.install_nginx(b'original', b'candidate')
            release.install_nginx(b'candidate', b'original')
            current[0] = b'operator changes'
            with self.assertRaises(RuntimeError):
                release.install_nginx(b'candidate', b'original')
            self.assertEqual(current[0], b'operator changes')

    def test_drain_waits_for_five_minute_stats_with_batch_disabled_and_combines_stderr(self):
        clock = [0.0]
        calls = []
        class Proc:
            def __truediv__(self, part):
                return self
            def read_text(self):
                return 'header\n'
            def exists(self):
                return False
        def logs(*args, **kwargs):
            calls.append((args, kwargs))
            return '\u4fdd\u5b58\u6570\u636e\u770b\u677f\u6570\u636e\u6210\u529f\uff0c\u5171\u4fdd\u5b58'.encode() if clock[0] >= 300 else b''
        with patch.object(release, 'Path', return_value=Proc()), patch.object(release, 'inspect', return_value={'State': {'Pid': 10}}), patch.object(release, 'infrastructure_peers', return_value=set(), create=True), patch.object(release, 'drain_settings', return_value=(False, 5, True, 300)), patch.object(release, 'settlements_idle', return_value=True), patch.object(release, 'settlement_logs_consistent', return_value=True), patch.object(release, 'docker', side_effect=logs), patch.object(release.time, 'monotonic', side_effect=lambda: clock[0]), patch.object(release.time, 'time', side_effect=lambda: clock[0] + 1000), patch.object(release.time, 'sleep', side_effect=lambda seconds: clock.__setitem__(0, clock[0] + seconds)), patch('builtins.print'):
            result = release.wait_drain('old', [11], scope={'max_id': 1}, providers=set())
        self.assertFalse(result['batch_enabled'])
        self.assertGreaterEqual(result['quiet_seconds'], 300)
        self.assertTrue(all(kwargs['combined'] for _, kwargs in calls))

    def test_isolated_test_rejects_zero_selected_tests_and_embedded_image_credentials(self):
        with tempfile.TemporaryDirectory() as root, patch.object(release, 'STAGE', Path(root)):
            for name in ('common-security.test', 'types-security.test'):
                (Path(root) / name).write_bytes(b'binary')
            build = {'test_binaries': {name: release.sha(b'binary') for name in ('common-security.test', 'types-security.test')}}
            with patch.object(release, 'inspect', return_value={'Config': {'Env': ['PATH=/bin']}}), patch.object(release, 'docker', return_value=b'testing: warning: no tests to run\nPASS\n'), self.assertRaises(RuntimeError):
                release.isolated_tests('image', build)
            with patch.object(release, 'inspect', return_value={'Config': {'Env': ['API_KEY=synthetic-secret']}}), patch.object(release, 'docker') as docker, self.assertRaises(RuntimeError):
                release.isolated_tests('image', build)
            docker.assert_not_called()

    def test_candidate_has_no_master_jobs_aliases_or_published_ports(self):
        original = {
            'Config': {'Image': 'old', 'Env': ['SECRET=keep', 'NODE_TYPE=master'], 'Labels': {'owner': 'keep'}},
            'HostConfig': {'NetworkMode': 'ztapi_app', 'PortBindings': {'3000/tcp': [{'HostPort': '3000'}]}},
            'NetworkSettings': {'Networks': {'ztapi_app': {'IPAddress': '172.30.0.3', 'Aliases': ['server']}, 'ztapi_data': {'IPAddress': '172.31.0.3'}}},
        }
        candidate = release.clone(original, 'new', 'abc', temporary=True)
        self.assertIn('SECRET=keep', candidate['Env'])
        self.assertIn('NODE_TYPE=slave', candidate['Env'])
        self.assertEqual(candidate['HostConfig']['PortBindings'], {})
        self.assertEqual(candidate['NetworkingConfig']['EndpointsConfig']['ztapi_app'], {'Aliases': []})
        self.assertEqual(original['Config']['Image'], 'old')
        primary = release.clone(original, 'new', 'abc', temporary=False)
        self.assertIn('NODE_TYPE=master', primary['Env'])
        self.assertEqual(primary['NetworkingConfig']['EndpointsConfig']['ztapi_app']['IPAMConfig']['IPv4Address'], '172.30.0.3')

    def test_handoff_changes_only_main_backend_and_source_headers(self):
        original = b'proxy_pass http://server:3000;\nproxy_pass http://server:3000/v1/models;\nproxy_pass $support;\n'
        proposed = release.handoff_config(original, 'candidate', 'new')
        self.assertIn(b'proxy_pass http://candidate:3000/v1/models;', proposed)
        self.assertIn(b'proxy_pass $support;', proposed)
        self.assertNotIn(b'http://server:3000', proposed)
        with self.assertRaises(RuntimeError):
            release.handoff_config(original, 'bad;host', 'new')

    def test_drain_requires_both_sockets_and_retired_workers(self):
        self.assertFalse(release.drained(0, 0, False, True, 20, 5))
        self.assertFalse(release.drained(1, 0, True, True, 20, 5))
        self.assertFalse(release.drained(0, 0, True, False, 20, 5))
        self.assertFalse(release.drained(0, 0, True, True, 9, 5))
        self.assertTrue(release.drained(0, 0, True, True, 10, 5))


class ActivationTests(OfflineTestCase):
    def exercise(self, fault=None, old_fx_safe=True):
        events = []
        failure = [fault]
        def hit(point):
            if failure[0] == point:
                failure[0] = None
                raise RuntimeError('injected:' + point)
        original = {'Id': 'old', 'Name': '/' + release.MAIN, 'Image': 'sha256:old',
            'Config': {'Env': ['NODE_TYPE=master'] + (['ZTAPI_SKIP_STARTUP_FX_REFRESH=true'] if old_fx_safe else []), 'Labels': {}},
            'HostConfig': {'NetworkMode': 'ztapi_app', 'PortBindings': {}, 'RestartPolicy': {'Name': 'on-failure', 'MaximumRetryCount': 7}},
            'NetworkSettings': {'Networks': {name: {'IPAddress': ip, 'Aliases': ['server', release.MAIN]}
                for name, ip in [('ztapi_app', '172.30.0.3'), ('ztapi_data', '172.31.0.3'), ('ztapi_egress', '172.32.0.3')]}},
            'State': {'Running': True, 'StartedAt': '2026-11-02T00:00:00Z', 'Pid': 10}}
        containers = {'old': copy.deepcopy(original), 'candidate': {'Id': 'candidate', 'Name': '/' + release.CANDIDATE,
            'Config': {'Env': ['NODE_TYPE=slave', 'ZTAPI_SKIP_STARTUP_FX_REFRESH=true'], 'Labels': {'ztapi.security.release': release.RELEASE}},
            'HostConfig': {'RestartPolicy': {'Name': 'no', 'MaximumRetryCount': 0}}, 'NetworkSettings': {'Networks': {}}, 'State': {'Running': True, 'Pid': 11}}}
        old_config = b'proxy_pass http://server:3000;\n'
        config = [old_config]
        index = [b'old-index']
        bindings = [{'id': 'support', 'ip': '172.30.0.5', 'original': b'original-support'}, {'id': 'bot', 'ip': '172.30.0.6', 'original': b'original-bot'}]
        hosts_restored = [True]
        def find(name):
            for container in containers.values():
                if container['Id'] == name or container.get('Name') == '/' + name:
                    return container
            return None
        def inspect(name):
            if name == release.NGINX:
                return {'NetworkSettings': {'Networks': {'ztapi_app': {'IPAddress': '172.30.0.2'}}}}
            result = find(name)
            if result is None:
                raise RuntimeError('not found: ' + name)
            return copy.deepcopy(result)
        def docker(*args, **kwargs):
            if args[0] == 'stop':
                cid = args[-1]
                self.assertIn('--time=-1', args)
                events.append('stop:' + cid)
                containers[cid]['State']['Running'] = False
                for endpoint in containers[cid]['NetworkSettings']['Networks'].values():
                    endpoint['IPAddress'] = ''
                hit('stop-' + cid)
            elif args[0] == 'rename':
                cid, name = args[1:]
                containers[cid]['Name'] = '/' + name
                events.append('rename:' + cid)
                hit('rename-' + cid)
            elif args[0] == 'start':
                cid = args[1]
                events.append('start:' + cid)
                hit('start-' + cid)
                containers[cid]['State']['Running'] = True
            elif args[:2] == ('network', 'disconnect'):
                network, cid = args[2:]
                containers[cid]['NetworkSettings']['Networks'].pop(network, None)
                events.append('disconnect:' + cid + ':' + network)
                hit('disconnect-' + cid)
            elif args[:3] == ('exec', release.NGINX, 'cat'):
                if args[-1] == release.CONF:
                    return config[0]
                if args[-1] == '/usr/share/nginx/public/index.html':
                    return index[0]
                return b'admin-index'
            return b''
        def api(method, path, value=None, **kwargs):
            if path.startswith('/containers/') and path.endswith('/update'):
                cid = path.split('/')[2]
                containers[cid]['HostConfig']['RestartPolicy'] = copy.deepcopy(value['RestartPolicy'])
                events.append('policy:' + cid + ':' + value['RestartPolicy']['Name'])
                hit('policy-' + cid)
                return {}
            if '/connect' in path:
                network = path.split('/')[2]
                endpoint = value['EndpointConfig']
                containers[value['Container']]['NetworkSettings']['Networks'][network] = {
                    'IPAddress': endpoint['IPAMConfig']['IPv4Address'], 'Aliases': endpoint['Aliases']}
                events.append('connect:' + value['Container'] + ':' + network)
                return {}
            raise AssertionError((method, path))
        def create(name, value):
            containers['primary'] = {'Id': 'primary', 'Name': '/' + name,
                'Config': {'Env': value['Env'], 'Labels': value['Labels']}, 'HostConfig': value['HostConfig'],
                'NetworkSettings': {'Networks': copy.deepcopy(original['NetworkSettings']['Networks'])}, 'State': {'Running': False, 'Pid': 12}}
            events.append('create:primary')
            hit('create-primary')
            return 'primary'
        def healthy(cid):
            events.append('health:' + cid)
            hit('health-' + cid)
            self.assertTrue(containers[cid]['State']['Running'])
        def install(raw, expected):
            self.assertEqual(config[0], expected)
            config[0] = raw
            target = 'candidate' if ('http://' + release.CANDIDATE + ':3000').encode() in raw else 'primary'
            events.append('route:' + target)
            if failure[0] == 'nginx-drift':
                failure[0] = None
                config[0] = b'operator-edited-config'
                raise RuntimeError('concurrent operator edit')
            if target == 'primary' and 'primary' in containers and containers['primary']['State']['Running']:
                hit('route-primary')
            return [100]
        def drain(cid, workers, **kwargs):
            events.append('drain:' + cid)
            hit('drain-' + cid)
            if fault == 'recovery-drain' and cid == 'candidate':
                raise RuntimeError('initial candidate drain timeout')
            if fault == 'recovery-drain' and cid == 'primary':
                raise RuntimeError('new primary cannot drain')
            return {'drained': True}
        def hosts(*args, restore=False):
            events.append('hosts:restore' if restore else 'hosts:bridge')
            hosts_restored[0] = restore
            if restore:
                hit('hosts-restore')
        def publish(*args):
            index[0] = b'new-index'
            events.append('publish')
            hit('publish')
        def restore_index(*args):
            index[0] = b'old-index'
            events.append('index:restore')
        class Monitor:
            deadline = time.monotonic() + 600
            def start(self):
                return self
            def probe(self):
                pass
            def check(self):
                if fault == 'monitor':
                    raise RuntimeError('availability_monitor_failed')
            def close(self):
                events.append('monitor:closed')
            def snapshot(self):
                return {'public:200': 5}
        build = {'commit': 'a' * 40, 'public_files': {'index.html': release.sha(b'new-index')}}
        baseline = {'containers': {}, 'indexes': {'admin': release.sha(b'admin-index')}}
        with tempfile.TemporaryDirectory() as root, ExitStack() as stack:
            stack.enter_context(patch.object(release, 'STAGE', Path(root)))
            release.save('prepared.json', {'candidate': 'candidate', 'image': 'sha256:new'})
            release.save('original-main.private.json', original)
            release.save('nginx-original.conf', old_config)
            release.save('public-index-before.html', b'old-index')
            for name, value in {'inspect': inspect, 'docker': docker, 'api': api, 'optional_container': find,
                'create': create, 'wait_healthy': healthy, 'install_nginx': install, 'wait_drain': drain,
                'catalog_hosts': hosts, 'publish_static': publish, 'restore_index': restore_index}.items():
                stack.enter_context(patch.object(release, name, side_effect=value))
            for name in ('pins', 'startup_preflight', 'validate_static'):
                stack.enter_context(patch.object(release, name))
            stack.enter_context(patch.object(release, 'catalog_bindings', return_value=bindings))
            stack.enter_context(patch.object(release, 'upstream_addresses', return_value=set()))
            stack.enter_context(patch.object(release, 'settlement_scope', return_value={'max_id': 12, 'started_at': '2026-11-02 00:00:00'}))
            stack.enter_context(patch.object(release, 'business_digests', return_value={}))
            stack.enter_context(patch.object(release, 'AvailabilityMonitor', return_value=Monitor()))
            error = None
            try:
                release.activate(baseline, build)
            except Exception as exc:
                error = exc
            receipts = {path.name: json.loads(path.read_bytes()) for path in Path(root).glob('*.json') if 'recovery' in path.name or 'restart-policy' in path.name}
        return events, containers, config[0], index[0], hosts_restored[0], error, receipts

    def test_success_keeps_catalog_bridge_until_primary_healthy_and_drains_before_stops(self):
        events, containers, config, index, restored, error, _ = self.exercise()
        self.assertIsNone(error)
        self.assertEqual(index, b'new-index')
        self.assertTrue(restored)
        self.assertLess(events.index('health:primary'), events.index('hosts:restore'))
        self.assertLess(events.index('drain:old'), events.index('stop:old'))
        self.assertLess(events.index('drain:candidate'), events.index('stop:candidate'))
        self.assertLess(events.index('publish'), events.index('stop:candidate'))
        self.assertTrue(containers['primary']['State']['Running'])

    def test_retired_backup_cannot_autostart_and_original_policy_is_saved(self):
        events, containers, _, _, _, error, receipts = self.exercise()
        self.assertIsNone(error)
        self.assertEqual(containers['old']['HostConfig']['RestartPolicy']['Name'], 'no')
        self.assertEqual(receipts['old-restart-policy.json']['restart_policy'], {'Name': 'on-failure', 'MaximumRetryCount': 7})
        self.assertLess(events.index('drain:old'), events.index('policy:old:no'))
        self.assertLess(events.index('policy:old:no'), events.index('stop:old'))
        self.assertNotIn('policy:candidate:unless-stopped', events)

    def test_operator_fallback_survives_reboot_without_restarting_legacy_master(self):
        _, containers, _, _, _, error, receipts = self.exercise('start-primary', old_fx_safe=False)
        self.assertIsNotNone(error)
        self.assertEqual(containers['old']['HostConfig']['RestartPolicy']['Name'], 'no')
        self.assertEqual(containers['candidate']['HostConfig']['RestartPolicy']['Name'], 'unless-stopped')
        self.assertTrue(receipts['operator-recovery-required.json']['candidate_serving'])
        self.assertFalse(receipts['operator-recovery-required.json']['rolled_back'])

    def test_running_original_recovery_restores_its_exact_restart_policy(self):
        _, containers, _, _, _, error, _ = self.exercise('policy-old', old_fx_safe=False)
        self.assertIsNotNone(error)
        self.assertTrue(containers['old']['State']['Running'])
        self.assertEqual(containers['old']['HostConfig']['RestartPolicy'], {'Name': 'on-failure', 'MaximumRetryCount': 7})
        self.assertEqual(containers['candidate']['HostConfig']['RestartPolicy']['Name'], 'no')

    def test_each_mutating_boundary_recovers_old_without_database_restore(self):
        for fault in ('stop-old', 'rename-old', 'disconnect-old', 'create-primary', 'start-primary', 'health-primary', 'route-primary', 'drain-candidate', 'hosts-restore', 'publish', 'stop-candidate'):
            with self.subTest(fault=fault):
                events, containers, config, index, restored, error, receipts = self.exercise(fault)
                self.assertIsNotNone(error)
                self.assertTrue(containers['candidate']['State']['Running'])
                self.assertTrue(containers['old']['State']['Running'])
                self.assertEqual(containers['old']['Name'], '/' + release.MAIN)
                self.assertEqual(set(containers['old']['NetworkSettings']['Networks']), {'ztapi_app', 'ztapi_data', 'ztapi_egress'})
                self.assertTrue(restored)
                self.assertEqual(index, b'old-index')
                self.assertTrue(receipts)
                if 'stop:primary' in events:
                    self.assertIn('route:candidate', events[:events.index('stop:primary')])

    def test_recovery_drain_timeout_never_stops_primary(self):
        events, containers, config, _, _, error, receipts = self.exercise('recovery-drain')
        self.assertIsNotNone(error)
        self.assertNotIn('stop:primary', events)
        self.assertTrue(containers['candidate']['State']['Running'])
        self.assertIn(release.CANDIDATE.encode(), config)
        self.assertTrue(receipts)

    def test_catalog_exposure_also_requires_primary_drain_before_rollback_stop(self):
        events, _, _, _, _, error, _ = self.exercise('hosts-restore')
        self.assertIsNotNone(error)
        self.assertIn('drain:primary', events)
        self.assertLess(events.index('drain:primary'), events.index('stop:primary'))

    def test_monitor_failure_prevents_any_primary_stop(self):
        events, _, _, _, _, error, _ = self.exercise('monitor')
        self.assertIsNotNone(error)
        self.assertNotIn('stop:old', events)
        self.assertNotIn('stop:primary', events)
        self.assertIn('monitor:closed', events)

    def test_unsafe_legacy_fx_restart_leaves_candidate_serving_and_records_blocker(self):
        events, containers, config, _, _, error, receipts = self.exercise('start-primary', old_fx_safe=False)
        self.assertIsNotNone(error)
        self.assertNotIn('start:old', events)
        self.assertTrue(containers['candidate']['State']['Running'])
        self.assertIn(release.CANDIDATE.encode(), config)
        self.assertTrue(receipts)

    def test_recovery_does_not_clobber_unknown_nginx_changes(self):
        events, _, config, _, _, error, receipts = self.exercise('nginx-drift')
        self.assertIsNotNone(error)
        self.assertEqual(config, b'operator-edited-config')
        self.assertNotIn('stop:old', events)
        self.assertTrue(receipts)


if __name__ == '__main__':
    unittest.main()
