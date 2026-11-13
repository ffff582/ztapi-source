"""Offline release fault tests. No Docker daemon, network or real secrets."""
import copy
from contextlib import contextmanager
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import sqlite3
import tempfile
import unittest
from unittest.mock import patch

MODULE = Path(__file__).with_name('spending_release.py')
spec = importlib.util.spec_from_file_location('spending_release_under_test', MODULE)
r = importlib.util.module_from_spec(spec) if MODULE.exists() else None
if r:
    spec.loader.exec_module(r)

PUBLIC = b'''server {
    listen 443 ssl;
    server_name ztapi.vip;
    add_header X-ZTAPI-Source "unchanged";
    location ^~ /api/support/ { proxy_pass http://support:3013; }
    location /api/ { proxy_pass http://server:3000; }
    location / { root /usr/share/nginx/public; }
}
server { listen 172.30.0.2:3000; location / { return 404; } }
'''


class ProductionShapeTests(unittest.TestCase):
    def test_new_route_keeps_inherited_source_and_hsts_headers(self):
        changed = r.nginx_with_spending(PUBLIC)
        node = next(n for n in r._nginx_tree(changed) if n['head'] == [b'location', b'^~', b'/api/spending/'])
        self.assertFalse(any(d[0] == b'add_header' for d in node['directives']))

    def test_exact_legacy_cache_header_route_can_be_safely_migrated(self):
        legacy = r._insert_route(PUBLIC, legacy_cache_header=True)
        self.assertIn(b'add_header Cache-Control', legacy)
        self.assertEqual(r.nginx_without_spending(legacy), PUBLIC)
        modern = r.nginx_with_spending(legacy)
        self.assertNotIn(b'add_header Cache-Control', modern)
        self.assertEqual(r.nginx_without_spending(modern), PUBLIC)

    def test_public_tls_route_does_not_treat_the_admin_support_route_as_duplicate(self):
        admin = b'''server {
    listen 443 ssl;
    server_name admin.ztapi.vip;
    location ^~ /api/support/ { proxy_pass http://support:3013; }
    location / { root /usr/share/nginx/admin; }
}
'''
        original = PUBLIC + admin
        changed = r.nginx_with_spending(original)
        self.assertEqual(changed.count(b'location ^~ /api/spending/'), 1)
        self.assertTrue(changed.endswith(admin))
        self.assertEqual(r.nginx_without_spending(changed), original)

    def test_two_public_tls_servers_still_fail_closed(self):
        with self.assertRaises(r.ReleaseError):
            r.nginx_with_spending(PUBLIC + PUBLIC)

    def test_mysql_backtick_account_grants_are_equivalent_but_not_broader(self):
        host = '172.31.0.0/255.255.0.0'
        quoted = r.sql_identity('app', r.READ_USER, host)
        actual = '`' + r.READ_USER + '`@`' + host + '`'
        rows = ['GRANT USAGE ON *.* TO ' + actual] + [
            line.rstrip(';').replace(quoted, actual)
            for line in r.grant_sql('app', r.READ_USER, host, r.GRANTS).splitlines()
        ]
        r.verify_grants(rows, 'app', r.READ_USER, host, r.GRANTS)
        with self.assertRaises(r.ReleaseError):
            r.verify_grants([line.replace(actual, '`' + r.READ_USER + '`@`%`') for line in rows],
                            'app', r.READ_USER, host, r.GRANTS)


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def container(name, env=()):
    return {'Id': digest(name.encode()), 'Image': 'sha256:' + 'a' * 64,
            'State': {'Running': True, 'StartedAt': 'fixture-start'},
            'Config': {'Env': list(env), 'User': '10001:10001'}, 'HostConfig': {},
            'Mounts': [], 'NetworkSettings': {'Networks': {'ztapi_app': {'IPAddress': '172.30.0.4'}}}}


class FakeHost:
    def __init__(self, root):
        self.root = root
        self.events = []
        self.nginx = PUBLIC
        self.fail_health = False
        self.cas_drift = False
        self.fail_validation = False
        self.users = []
        self.grants = []
        self.resources = {'memory_available': 1024 << 20, 'disk_available': 2048 << 20,
                          'memory_limit': True, 'cpu_limit': True, 'pids_limit': True}
        self.cs = {n: container(n) for n in (r.MAIN, r.NGINX, r.SUPPORT, r.BOT, r.MYSQL)}
        self.cs[r.MAIN]['Config']['Env'] = ['SQL_DSN=root:PRIVATE@tcp(mysql)/db', 'MODEL_KEY=PRIVATE']
        self.cs[r.SUPPORT]['Config']['Env'] = ['ZTAPI_SESSION_SIGNING_KEY=' + 's' * 48,
                                               'SQL_DSN=PRIVATE', 'SUPPORT_OWNER_KEY=PRIVATE']
        self.cs[r.SUPPORT]['Mounts'] = [{'Type': 'bind', 'Source': r.BINDINGS, 'Destination': '/data', 'RW': True}]
        self.cs[r.BOT]['Mounts'] = [{'Type': 'bind', 'Source': r.BOT_CONFIG,
                                    'Destination': '/run/secrets/bot.json', 'RW': False}]
        for name, c in self.cs.items():
            c['NetworkSettings']['Networks'] = {n: {'IPAddress': '172.30.0.4' if n == 'ztapi_app' else '172.31.0.4'}
                                               for n in ('ztapi_app', 'ztapi_data', 'ztapi_egress')}
        self.cs[r.MYSQL]['NetworkSettings']['Networks'] = {'ztapi_data': {'IPAddress': '172.31.0.3'}}
        self.cs[r.NGINX]['NetworkSettings']['Networks'] = {'ztapi_app': {'IPAddress': '172.30.0.2'}}
        self.image = {'Id': 'sha256:' + 'b' * 64, 'Config': {
            'Env': ['PATH=/usr/bin', 'GIN_MODE=release', 'PORT=3000', 'ZTAPI_SOURCE_COMMIT=' + 'a' * 40,
                    'ZTAPI_RELEASE_VERSION=old-main', 'ZTAPI_SOURCE_TAG=production-old-main'], 'Volumes': {}}}
        self.private = {'bot_token': '123456:fixture_token_abcdefghijklmnopqrstuvwxyz',
                        'owner_key': 'PRIVATE', 'provider_key': 'PRIVATE', 'bridge_key': 'PRIVATE'}
        self.data = root / 'data'
        self.networks = {n: {'Id': digest(n.encode()), 'Driver': 'bridge', 'Internal': n != 'ztapi_egress',
                             'IPAM': {'Config': [{'Subnet': '172.31.0.0/16' if n == 'ztapi_data' else '172.30.0.0/16'}]}}
                         for n in ('ztapi_app', 'ztapi_data', 'ztapi_egress')}

    def inspect(self, name):
        return copy.deepcopy(self.cs[name] if name in self.cs else self.image)

    def optional(self, name):
        return copy.deepcopy(self.cs.get(name))

    def nginx_read(self):
        return self.nginx

    def root_preflight(self, stage):
        self.events.append('root_preflight')

    def capacity(self, stage):
        return dict(self.resources)

    def subnet(self, name, expected=None):
        r.verify_networks(self.networks, expected)
        return '172.31.0.0/16' if name == 'ztapi_data' else '172.30.0.0/16'

    def read_bot_config(self):
        return json.dumps(self.private).encode()

    def admin_sql(self, query):
        self.events.append(('sql', query))
        if query.startswith('SELECT Host'):
            return ''.join(h + '\t2\n' for h in self.users).encode()
        if query.startswith('SHOW GRANTS'):
            return '\n'.join(self.grants).encode()
        if query.startswith('CREATE USER'):
            self.users = ['172.31.0.0/255.255.0.0']
            account = "'" + r.READ_USER + "'@'" + self.users[0] + "'"
            self.grants = ['GRANT USAGE ON *.* TO ' + account]
            self.grants += [x.rstrip(';') for x in query.splitlines() if x.startswith('GRANT ')]
            return b''
        raise AssertionError('unexpected SQL')

    def reader_probe(self, credentials, grants):
        self.events.append('reader_probe')
        return r.READ_USER + '@' + credentials['host']

    def build_image(self, context, tag):
        self.events.append(('build', sorted(p.name for p in context.iterdir())))
        return self.image['Id']

    def ensure_store(self, stage):
        self.data.mkdir(exist_ok=True)
        return {'path': r.DATA, 'device': 1, 'inode': 1}

    def project_telegram(self, stage, raw):
        r.protected_save(stage / 'telegram.json', raw)

    def create(self, name, desired):
        self.events.append('create')
        c = container(name)
        c['Image'] = desired['Image']
        c['Config'] = {k: copy.deepcopy(v) for k, v in desired.items()
                       if k not in ('HostConfig', 'NetworkingConfig')}
        c['HostConfig'] = copy.deepcopy(desired['HostConfig'])
        # Docker merges baked image defaults even with an explicit create Env.
        merged = dict(x.split('=', 1) for x in self.image['Config']['Env'])
        merged.update(x.split('=', 1) for x in c['Config']['Env'])
        c['Config']['Env'] = [k + '=' + v for k, v in merged.items()]
        labels = dict(self.image['Config'].get('Labels') or {})
        labels.update(c['Config']['Labels'])
        c['Config']['Labels'] = labels
        c['NetworkSettings'] = {'Networks': {n: {'IPAddress': '172.31.0.8'}
                                             for n in desired['NetworkingConfig']['EndpointsConfig']}}
        c['Mounts'] = []
        for bind in desired['HostConfig']['Binds']:
            source, destination, mode = bind.rsplit(':', 2)
            c['Mounts'].append({'Type': 'bind', 'Source': source, 'Destination': destination, 'RW': mode == 'rw'})
        self.cs[name] = c
        db = sqlite3.connect(self.data / 'spending.db')
        db.executescript('CREATE TABLE spending_preferences(enabled INTEGER, telegram INTEGER);'
                         'CREATE TABLE spending_alerts(id INTEGER);')
        db.close()
        return c['Id']

    def start(self, cid):
        self.events.append('start')

    def healthy(self, cid):
        self.events.append('health')
        if self.fail_health:
            raise r.ReleaseError('health_failed')

    def check_default_off(self):
        return r.check_default_off(self.data / 'spending.db')

    @contextmanager
    def edge_lock(self):
        yield

    def install_route(self, expected, desired, stage):
        self.events.append('nginx_validate')
        if self.fail_validation:
            raise r.ReleaseError('nginx_validation_failed')
        if self.cas_drift:
            self.nginx += b'# external edit\n'
        r.require(self.nginx == expected, 'route_cas_failed')
        self.nginx = desired
        self.events.append('nginx_reload')


class ReleaseTests(unittest.TestCase):
    def setUp(self):
        self.assertIsNotNone(r, 'spending release helper is missing')
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.stage = self.root / 'stage'
        self.host = FakeHost(self.root)
        self.baseline = {'containers': {n: {'id': c['Id'], 'started_at': c['State']['StartedAt']}
                                       for n, c in self.host.cs.items()},
                         'nginx_sha256': digest(PUBLIC),
                         'spending': {'database': 'ztapi', 'db_network': 'ztapi_data', 'db_subnet': '172.31.0.0/16'}}
        self.baseline['spending_networks'] = {n: {'id': info['Id'], 'driver': info['Driver'],
                                                'internal': info['Internal'], 'ipam': copy.deepcopy(info['IPAM']['Config'])}
                                               for n, info in self.host.networks.items()}
        elf = bytearray(64)
        elf[:6] = b'\x7fELF\x02\x01'
        elf[18:20] = (62).to_bytes(2, 'little')
        self.binary = self.root / 'spending-linux-amd64'
        self.binary.write_bytes(elf)
        self.columns_file = self.root / 'spending-columns.json'
        self.columns = {table: list(cols) for table, cols in r.GRANTS.items()}
        if 'created_at' in self.columns['ztapi_request_settlements']:
            self.columns['ztapi_request_settlements'].remove('created_at')
        self.columns_file.write_bytes(json.dumps(self.columns, sort_keys=True).encode())
        self.build = {'commit': 'c' * 40, 'spending_binary': {'path': str(self.binary), 'sha256': digest(elf)},
                      'spending_columns': {'path': str(self.columns_file), 'sha256': digest(self.columns_file.read_bytes())}}
        self.addCleanup(patch.stopall)
        patch.object(r.LinuxHost, '__init__', side_effect=AssertionError('real host forbidden')).start()

    def prepare(self):
        return r.prepare(self.baseline, self.build, stage=self.stage, backend=self.host)

    def activate(self):
        return r.activate_route(self.baseline, stage=self.stage, backend=self.host)

    def test_private_env_not_inherited_and_container_is_bounded(self):
        self.prepare()
        c = self.host.cs[r.SERVICE]
        env = dict(x.split('=', 1) for x in c['Config']['Env'])
        self.assertEqual(env['ZTAPI_SESSION_SIGNING_KEY'], 's' * 48)
        self.assertEqual(env['SPENDING_BINDING_STORE'], '/bindings/support.db')
        self.assertEqual(env['SPENDING_QUOTA_PER_UNIT'], '500000')
        self.assertFalse({'SQL_DSN', 'MYSQL_ROOT_PASSWORD', 'MODEL_KEY', 'SUPPORT_OWNER_KEY'} & env.keys())
        self.assertNotIn('PRIVATE', repr(c))
        self.assertEqual(c['Config']['User'], '10001:10001')
        h = c['HostConfig']
        self.assertTrue(h['ReadonlyRootfs'])
        self.assertEqual(h['CapDrop'], ['ALL'])
        self.assertIn('no-new-privileges:true', h['SecurityOpt'])
        self.assertLessEqual(h['Memory'], 256 << 20)
        self.assertLessEqual(h['NanoCpus'], 500000000)
        self.assertEqual(h['PortBindings'], {})
        self.assertIn(r.BINDINGS + ':/bindings:ro', h['Binds'])
        projected = json.loads((self.stage / 'telegram.json').read_bytes())
        self.assertEqual(set(projected), {'bot_token'})

    def test_readonly_grant_boundaries_and_exact_source_projection(self):
        self.prepare()
        grants = '\n'.join(self.host.grants)
        self.assertNotIn('content', grants)
        self.assertNotIn('`other`', grants)
        self.assertNotIn('tokens', grants)
        self.assertNotIn('WITH GRANT OPTION', grants)
        manifest = json.loads((self.stage / 'build.json').read_bytes())
        self.assertEqual(r.source_grants(self.columns_file), {k: tuple(v) for k, v in manifest['select_grants'].items()})
        self.assertEqual(set(r.GRANTS), {'users', 'logs', 'balance_ledgers', 'ztapi_request_settlements',
                                        'ztapi_settlement_log_outboxes', 'ztapi_supplier_refund_charges',
                                        'ztapi_supplier_refunds'})

    def test_existing_unknown_user_is_not_reset(self):
        self.host.users = ['172.31.0.0/255.255.0.0']
        with self.assertRaisesRegex(r.ReleaseError, 'existing_user_unowned'):
            self.prepare()
        self.assertFalse(any(e[0] == 'sql' and ('CREATE USER' in e[1] or 'GRANT ' in e[1])
                             for e in self.host.events if isinstance(e, tuple)))

    def test_owned_user_rerun_does_not_change_password_or_grants(self):
        self.prepare()
        before = (self.stage / 'reader.private.json').read_bytes()
        self.host.events.clear()
        self.prepare()
        self.assertEqual(before, (self.stage / 'reader.private.json').read_bytes())
        sql = '\n'.join(e[1] for e in self.host.events if isinstance(e, tuple) and e[0] == 'sql')
        self.assertNotIn('CREATE USER', sql)
        self.assertNotIn('ALTER', sql)
        self.assertNotIn('\nGRANT ', sql)

    def test_rejects_root_old_users_and_sql_injection(self):
        for user in ('root', 'support', 'ztapi_support', "x';DROP USER root;--"):
            with self.subTest(user=user), self.assertRaises(r.ReleaseError):
                r.grant_sql('ztapi', user, '172.30.0.0/255.255.0.0')
        for database in ('mysql', '*', 'ztapi`', 'ztapi;DROP', '../db'):
            with self.subTest(database=database), self.assertRaises(r.ReleaseError):
                r.grant_sql(database, r.READ_USER, '172.30.0.0/255.255.0.0')

    def test_broader_grants_and_foreign_hosts_fail_closed(self):
        self.prepare()
        for extra in ("GRANT SELECT ON `ztapi`.* TO 'x'@'%'", 'GRANT UPDATE ON *.* TO x',
                      'GRANT SELECT (`content`) ON `ztapi`.`logs` TO x'):
            with self.subTest(extra=extra), self.assertRaises(r.ReleaseError):
                r.verify_grants(self.host.grants + [extra], 'ztapi', r.READ_USER, self.host.users[0], self.columns)
        self.host.users.append('%')
        with self.assertRaises(r.ReleaseError):
            self.prepare()

    def test_unknown_markers_duplicate_anchor_and_wrong_public_server_rejected(self):
        for raw in (PUBLIC + b'# BEGIN spending-old\n', PUBLIC + PUBLIC,
                    PUBLIC.replace(b'ztapi.vip', b'internal.invalid'),
                    PUBLIC.replace(b'443 ssl', b'3013'),
                    PUBLIC + b'location /api/spending/ { return 200; }'):
            with self.subTest(raw=digest(raw)), self.assertRaises(r.ReleaseError):
                r.nginx_with_spending(raw)

    def test_route_addition_preserves_every_old_byte_and_is_idempotent(self):
        proposed = r.nginx_with_spending(PUBLIC)
        self.assertEqual(r.nginx_without_spending(proposed), PUBLIC)
        self.assertEqual(r.nginx_with_spending(proposed), proposed)
        self.assertEqual(proposed.count(b'proxy_pass http://' + r.SERVICE.encode() + b':3014;'), 1)
        self.assertIn(b'location ^~ /api/spending/', proposed)
        self.assertNotIn(b'location /healthz', proposed)

    def test_route_cas_failure_does_not_reload_or_clobber_drift(self):
        self.prepare()
        self.host.cas_drift = True
        with self.assertRaisesRegex(r.ReleaseError, 'route_cas_failed'):
            self.activate()
        self.assertEqual(self.host.nginx, PUBLIC + b'# external edit\n')
        self.assertNotIn('nginx_reload', self.host.events)

    def test_failed_health_means_no_nginx_mutation(self):
        self.prepare()
        self.host.events.clear()
        self.host.fail_health = True
        with self.assertRaises(r.ReleaseError):
            self.activate()
        self.assertNotIn('nginx_validate', self.host.events)
        self.assertEqual(self.host.nginx, PUBLIC)

    def test_prepare_failed_health_never_produces_ready_receipt(self):
        self.host.fail_health = True
        with self.assertRaises(r.ReleaseError):
            self.prepare()
        self.assertFalse((self.stage / 'prepared.json').exists())
        self.assertEqual(self.host.nginx, PUBLIC)

    def test_validation_failure_keeps_original(self):
        self.prepare()
        self.host.fail_validation = True
        with self.assertRaises(r.ReleaseError):
            self.activate()
        self.assertEqual(self.host.nginx, PUBLIC)

    def test_default_off_acceptance_is_readonly_and_uses_no_owner(self):
        self.prepare()
        path = self.host.data / 'spending.db'
        before = path.read_bytes()
        self.activate()
        self.assertEqual(path.read_bytes(), before)
        db = sqlite3.connect(path)
        self.assertEqual(db.execute('SELECT COUNT(*) FROM spending_preferences').fetchone()[0], 0)
        db.close()
        self.assertNotIn('getUpdates', repr(self.host.events))
        self.assertNotIn('sendMessage', repr(self.host.events))

    def test_existing_settings_are_not_rewritten_to_make_acceptance_pass(self):
        self.prepare()
        db = sqlite3.connect(self.host.data / 'spending.db')
        db.execute('INSERT INTO spending_preferences VALUES(1,1)')
        db.commit()
        db.close()
        with self.assertRaisesRegex(r.ReleaseError, 'store_not_empty'):
            self.activate()
        self.assertEqual(self.host.nginx, PUBLIC)

    def test_insufficient_resources_fail_before_account_or_service_creation(self):
        self.host.resources['memory_available'] = 128 << 20
        with self.assertRaisesRegex(r.ReleaseError, 'insufficient_resources'):
            self.prepare()
        self.assertNotIn('create', self.host.events)
        self.assertFalse(any(isinstance(e, tuple) and e[0] == 'sql' for e in self.host.events))

    def test_service_or_nginx_drift_fails_before_traffic(self):
        self.prepare()
        self.host.cs[r.SUPPORT]['State']['StartedAt'] = 'changed'
        with self.assertRaises(r.ReleaseError):
            self.activate()
        self.assertNotIn('nginx_reload', self.host.events)

    def test_binary_context_excludes_private_config_dump_and_old_builder_flag(self):
        self.prepare()
        context = self.stage / 'spending-build'
        self.assertEqual(set(p.name for p in context.iterdir()), {'Dockerfile', 'ztapi-spending'})
        recipe = (context / 'Dockerfile').read_text()
        self.assertIn('FROM sha256:' + 'a' * 64, recipe)
        self.assertNotIn('--chmod', recipe)
        self.assertIn('HEALTHCHECK NONE', recipe)
        manifest = json.loads((self.stage / 'build.json').read_bytes())
        for name, expected in manifest['artifacts'].items():
            self.assertEqual(digest((self.stage / name).read_bytes()), expected)
        self.assertNotIn('PRIVATE', json.dumps(manifest))
        self.assertFalse(any('private' in name or 'db' in name for name in manifest['artifacts']))

    def test_corrupt_binary_hash_fails_before_any_db_ddl(self):
        self.build['spending_binary']['sha256'] = '0' * 64
        with self.assertRaises(r.ReleaseError):
            self.prepare()
        self.assertFalse(any(isinstance(e, tuple) and e[0] == 'sql' for e in self.host.events))

    def test_unknown_stage_and_symlinked_private_output_rejected(self):
        self.stage.mkdir()
        (self.stage / 'unowned').write_text('preserve')
        with self.assertRaises(r.ReleaseError):
            self.prepare()
        self.assertEqual((self.stage / 'unowned').read_text(), 'preserve')

    def test_exception_and_stdout_never_contain_raw_provider_config(self):
        output = io.StringIO()
        with patch.object(self.host, 'admin_sql', side_effect=ValueError('mysql://root:PRIVATE@host')):
            with patch('sys.stdout', output), self.assertRaises(r.ReleaseError) as caught:
                self.prepare()
        self.assertNotIn('PRIVATE', str(caught.exception))
        self.assertRegex(str(caught.exception), r'^[a-z_]+(?::[a-f0-9]{64})?$')
        self.assertEqual(output.getvalue(), '')

    def test_frozen_json_columns_work_without_a_source_checkout(self):
        with patch.object(Path, 'read_text', side_effect=AssertionError('Go parsing forbidden')):
            self.prepare()
        manifest = json.loads((self.stage / 'build.json').read_bytes())
        self.assertIn('spending-columns.json', manifest['artifacts'])
        self.assertEqual(manifest['artifacts']['spending-columns.json'], digest(self.columns_file.read_bytes()))

    def test_unknown_projection_column_does_not_expand_grants(self):
        self.columns['logs'].append('content')
        self.columns_file.write_bytes(json.dumps(self.columns).encode())
        self.build['spending_columns']['sha256'] = digest(self.columns_file.read_bytes())
        with self.assertRaisesRegex(r.ReleaseError, 'source_projection_unknown'):
            self.prepare()
        self.assertFalse(self.host.users)

    def test_reviewed_optional_created_at_column_granted_only_when_listed(self):
        self.columns['ztapi_request_settlements'].append('created_at')
        self.columns_file.write_bytes(json.dumps(self.columns).encode())
        self.build['spending_columns']['sha256'] = digest(self.columns_file.read_bytes())
        self.prepare()
        row = next(row for row in self.host.grants if 'ON `ztapi`.`ztapi_request_settlements`' in row)
        self.assertIn('`created_at`', row)
        self.assertIn('`charge_dimensions_json`', row)

    def test_columns_manifest_hash_and_missing_manifest_fail_before_ddl(self):
        self.build['spending_columns']['sha256'] = '0' * 64
        with self.assertRaisesRegex(r.ReleaseError, 'spending_columns_hash_mismatch'):
            self.prepare()
        self.assertFalse(self.host.users)

    def test_column_grants_are_parsed_from_the_exact_bytes_whose_hash_was_verified(self):
        valid = self.columns_file.read_bytes()
        changed = copy.deepcopy(self.columns)
        changed['ztapi_request_settlements'].append('created_at')
        replacement = json.dumps(changed).encode()
        original = Path.read_bytes
        reads = []
        def read(path):
            if path == self.columns_file:
                reads.append(path)
                return valid if len(reads) == 1 else replacement
            return original(path)
        with patch.object(Path, 'read_bytes', autospec=True, side_effect=read):
            self.prepare()
        self.assertEqual(len(reads), 1)
        row = next(row for row in self.host.grants if 'ON `ztapi`.`ztapi_request_settlements`' in row)
        self.assertNotIn('`created_at`', row)

    def test_unneeded_optional_column_is_not_granted(self):
        self.prepare()
        row = next(row for row in self.host.grants if 'ON `ztapi`.`ztapi_request_settlements`' in row)
        self.assertNotIn('`created_at`', row)

    def test_docker_baked_safe_defaults_are_explicit_and_source_metadata_overridden(self):
        self.prepare()
        env = dict(x.split('=', 1) for x in self.host.cs[r.SERVICE]['Config']['Env'])
        self.assertEqual(env['GIN_MODE'], 'release')
        self.assertEqual(env['ZTAPI_SOURCE_COMMIT'], self.build['commit'])
        self.assertEqual(env['ZTAPI_RELEASE_VERSION'], r.RELEASE)
        self.assertEqual(env['ZTAPI_SOURCE_TAG'], 'production-claude-spend-20261102-r1')
        self.assertEqual(env['PORT'], '3014')

    def test_docker_baked_source_and_license_labels_are_preserved_and_checked(self):
        self.host.image['Config']['Labels'] = {
            'org.opencontainers.image.source': 'https://github.com/QuantumNous/new-api',
            'org.opencontainers.image.licenses': 'AGPL-3.0',
            'org.opencontainers.image.revision': 'a' * 40,
        }
        self.prepare()
        actual = self.host.cs[r.SERVICE]
        self.assertEqual(actual['Config']['Labels']['org.opencontainers.image.licenses'], 'AGPL-3.0')
        self.assertEqual(actual['Config']['Labels']['org.opencontainers.image.revision'], self.build['commit'])
        actual['Config']['Labels']['org.opencontainers.image.licenses'] = 'MIT'
        with self.assertRaisesRegex(r.ReleaseError, 'spending_service_drift'):
            self.activate()
        self.assertEqual(self.host.nginx, PUBLIC)

    def test_unknown_baked_labels_cannot_smuggle_compose_ownership(self):
        self.host.image['Config']['Labels'] = {'com.docker.compose.project': 'ztapi'}
        with self.assertRaisesRegex(r.ReleaseError, 'unsafe_base_image_labels'):
            self.prepare()
        self.assertFalse(self.host.users)

    def test_three_networks_and_reader_host_are_data_not_app_scoped(self):
        self.prepare()
        c = self.host.cs[r.SERVICE]
        self.assertEqual(set(c['NetworkSettings']['Networks']), {'ztapi_app', 'ztapi_data', 'ztapi_egress'})
        self.assertEqual(c['HostConfig']['PortBindings'], {})
        self.assertEqual(self.host.users, ['172.31.0.0/255.255.0.0'])
        self.assertIn('@tcp(ztapi-mysql-1:3306)', '\n'.join(c['Config']['Env']))

    def test_wrong_database_network_or_subnet_stops_before_role_creation(self):
        self.baseline['spending']['db_subnet'] = '172.30.0.0/16'
        with self.assertRaisesRegex(r.ReleaseError, 'database_subnet_changed'):
            self.prepare()
        self.assertFalse(self.host.users)

    def test_recreated_same_name_same_subnet_network_fails_identity_pin(self):
        self.host.networks['ztapi_data']['Id'] = '9' * 64
        with self.assertRaisesRegex(r.ReleaseError, 'spending_network_identity_changed'):
            self.prepare()
        self.assertFalse(self.host.users)
        self.assertEqual(self.host.nginx, PUBLIC)

    def test_private_baked_image_env_is_rejected_not_inherited(self):
        self.host.image['Config']['Env'].append('SQL_DSN=root:PRIVATE@mysql')
        with self.assertRaisesRegex(r.ReleaseError, 'unsafe_base_image_defaults'):
            self.prepare()
        self.assertFalse(self.host.users)

    def test_prepared_artifact_tamper_blocks_activation(self):
        self.prepare()
        binary = self.stage / 'spending-build' / 'ztapi-spending'
        binary.chmod(0o600)
        binary.write_bytes(b'tampered')
        with self.assertRaisesRegex(r.ReleaseError, 'artifact_hash_mismatch'):
            self.activate()
        self.assertEqual(self.host.nginx, PUBLIC)

    def test_changed_bot_config_blocks_activation_without_copying_other_fields(self):
        self.prepare()
        self.host.private['bot_token'] = '987654:other_fixture_token_abcdefghijklmnop'
        with self.assertRaisesRegex(r.ReleaseError, 'telegram_config_changed'):
            self.activate()
        self.assertNotIn('nginx_validate', self.host.events)

    def test_linux_adapter_auth_probe_requires_401_not_just_health(self):
        host = object.__new__(r.LinuxHost)
        host.inspect = lambda _: self.host.cs[r.SUPPORT]
        class Response:
            status = 200
            def __enter__(self):
                return self
            def __exit__(self, *args):
                return False
            def read(self, size):
                return b'{"ok":true}'
        opener = unittest.mock.Mock()
        opener.open.return_value = Response()
        with patch.object(r.request, 'build_opener', return_value=opener):
            with self.assertRaisesRegex(r.ReleaseError, 'unauthenticated_spending_not_rejected'):
                host.healthy('fixture-id')
        self.assertEqual(opener.open.call_count, 2)

    def test_unowned_existing_service_fails_before_any_account_or_store_creation(self):
        self.host.cs[r.SERVICE] = container(r.SERVICE)
        with self.assertRaisesRegex(r.ReleaseError, 'existing_service_unowned'):
            self.prepare()
        self.assertFalse(self.host.data.exists())
        self.assertFalse(self.host.users)

    def test_created_service_cannot_smuggle_an_extra_writable_mount(self):
        self.prepare()
        actual = self.host.cs[r.SERVICE]
        actual['Mounts'] = [{'Type': 'bind', 'Source': '/var/run/docker.sock',
                             'Destination': '/var/run/docker.sock', 'RW': True}]
        with self.assertRaisesRegex(r.ReleaseError, 'spending_mount_drift'):
            self.activate()
        self.assertEqual(self.host.nginx, PUBLIC)

    def test_missing_created_service_keeps_credentials_and_does_not_recreate(self):
        self.prepare()
        del self.host.cs[r.SERVICE]
        credentials = (self.stage / 'reader.private.json').read_bytes()
        with self.assertRaisesRegex(r.ReleaseError, 'created_service_missing'):
            self.prepare()
        self.assertEqual((self.stage / 'reader.private.json').read_bytes(), credentials)

    def test_partial_account_creation_has_no_reset_recovery(self):
        original = self.host.admin_sql
        def partial(query):
            if query.startswith('CREATE USER'):
                self.host.users = ['172.31.0.0/255.255.0.0']
                raise RuntimeError('connection lost after create')
            return original(query)
        with patch.object(self.host, 'admin_sql', side_effect=partial), self.assertRaises(r.ReleaseError):
            self.prepare()
        self.assertTrue((self.stage / 'reader.private.json').exists())
        self.assertFalse((self.stage / 'reader-owned.json').exists())
        with self.assertRaisesRegex(r.ReleaseError, 'existing_user_unowned'):
            self.prepare()

    def test_symlink_private_receipt_is_not_followed(self):
        self.stage.mkdir()
        target = self.root / 'must-preserve'
        target.write_text('unchanged')
        link = self.stage / 'stage.json'
        original = Path.is_symlink
        try:
            link.symlink_to(target)
        except OSError:
            link.write_text('simulated symlink')
        with patch.object(Path, 'is_symlink', autospec=True, side_effect=lambda p: p == link or original(p)):
            with self.assertRaises(r.ReleaseError):
                self.prepare()
        self.assertEqual(target.read_text(), 'unchanged')

    def test_legacy_helper_instance_never_changes_parent_stage(self):
        helper_spec = importlib.util.spec_from_file_location('parent_release_helper', MODULE.with_name('longtext_security.py'))
        parent = importlib.util.module_from_spec(helper_spec)
        helper_spec.loader.exec_module(parent)
        before = parent.STAGE
        host = object.__new__(r.LinuxHost)
        ORIGINAL_LINUX_INIT(host, self.stage)
        self.assertEqual(parent.STAGE, before)
        self.assertEqual(host.old.STAGE, self.stage)

    def test_linux_network_policy_keeps_app_and_data_internal_and_egress_external(self):
        host = object.__new__(r.LinuxHost)
        host.old = unittest.mock.Mock()
        networks = {n: {'Driver': 'bridge', 'Internal': n != 'ztapi_egress',
                        'IPAM': {'Config': [{'Subnet': '172.31.0.0/16'}]}}
                    for n in ('ztapi_app', 'ztapi_data', 'ztapi_egress')}
        host.old.api.side_effect = lambda method, path: copy.deepcopy(networks[path.rsplit('/', 1)[1]])
        self.assertEqual(host.subnet('ztapi_data'), '172.31.0.0/16')
        networks['ztapi_app']['Internal'] = False
        with self.assertRaisesRegex(r.ReleaseError, 'egress_network_policy_changed'):
            host.subnet('ztapi_data')

    def test_linux_legacy_create_records_identity_before_secondary_network_connect(self):
        self.prepare()
        manifest = json.loads((self.stage / 'build.json').read_bytes())
        credentials = json.loads((self.stage / 'reader.private.json').read_bytes())
        desired = r.container_spec(self.host.image['Id'], 's' * 48, credentials, self.stage,
                                   self.build['commit'], manifest['safe_base_env'])
        host = object.__new__(r.LinuxHost)
        host.old = unittest.mock.Mock()
        other_stage = self.root / 'create-adapter-stage'
        other_stage.mkdir()
        host.old.STAGE = other_stage
        calls = []
        def api(method, path, value):
            calls.append((method, path, copy.deepcopy(value)))
            if path.startswith('/containers/create'):
                return {'Id': 'f' * 64}
            proof = json.loads((other_stage / 'created.json').read_bytes())
            self.assertEqual(proof['container'], 'f' * 64)
            return None
        host.old.api.side_effect = api
        self.assertEqual(host.create(r.SERVICE, desired), 'f' * 64)
        self.assertEqual(set(calls[0][2]['NetworkingConfig']['EndpointsConfig']), {'ztapi_app'})
        self.assertEqual({path for _, path, _ in calls[1:]},
                         {'/networks/ztapi_data/connect', '/networks/ztapi_egress/connect'})


class NginxAdapterTests(unittest.TestCase):
    def setUp(self):
        self.assertIsNotNone(r)
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.stage = Path(self.temp.name)
        self.host = object.__new__(r.LinuxHost)
        self.old = unittest.mock.Mock()
        self.old.nginx_workers.return_value = [111]
        self.host.old = self.old
        self.current = PUBLIC
        self.commands = []
        self.fail_test = False
        self.fail_reload = False
        self.operator_edit = False
        self.old.docker.side_effect = self.docker

    def docker(self, *args, **kwargs):
        self.commands.append(args)
        if args[:4] == ('exec', r.NGINX, 'cat', r.CONF):
            return self.current
        if '-i' in args:
            expected = args[-3]
            r.require(digest(self.current) == expected, 'route_cas_failed')
            self.current = kwargs['data']
        elif args[:4] == ('exec', r.NGINX, 'nginx', '-t') and '-c' in args and self.fail_test:
            raise r.ReleaseError('nginx_validation_failed')
        elif args[:5] == ('exec', r.NGINX, 'nginx', '-s', 'reload') and self.fail_reload:
            self.fail_reload = False
            if self.operator_edit:
                self.current += b'# external edit\n'
            raise r.ReleaseError('nginx_reload_failed')
        return b''

    def test_real_adapter_static_test_failure_does_not_change_live_file(self):
        self.fail_test = True
        with self.assertRaisesRegex(r.ReleaseError, 'nginx_validation_failed'):
            self.host.install_route(PUBLIC, r.nginx_with_spending(PUBLIC), self.stage)
        self.assertEqual(self.current, PUBLIC)
        self.assertFalse(any('-i' in c for c in self.commands))

    def test_real_adapter_reload_failure_restores_only_its_own_bytes(self):
        self.fail_reload = True
        with self.assertRaisesRegex(r.ReleaseError, 'nginx_reload_failed'):
            self.host.install_route(PUBLIC, r.nginx_with_spending(PUBLIC), self.stage)
        self.assertEqual(self.current, PUBLIC)
        self.assertFalse(any('restart' in c or 'stop' in c for c in self.commands))
        self.assertEqual(sum('-i' in c for c in self.commands), 2)

    def test_real_adapter_rollback_does_not_clobber_an_operator_edit(self):
        self.fail_reload = self.operator_edit = True
        with self.assertRaisesRegex(r.ReleaseError, 'route_rollback_cas_failed'):
            self.host.install_route(PUBLIC, r.nginx_with_spending(PUBLIC), self.stage)
        self.assertTrue(self.current.endswith(b'# external edit\n'))
        self.assertEqual(sum('-i' in c for c in self.commands), 1)


ORIGINAL_LINUX_INIT = r.LinuxHost.__init__ if r else None


if __name__ == '__main__':
    unittest.main()
