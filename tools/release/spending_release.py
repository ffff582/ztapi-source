"""Independent spending-sidecar installer. Importing this module deploys nothing.

Only prepare() and activate_route() perform side effects, on an explicitly supplied
new stage. The parent owns main-API rollout, backups, source publication and drain.
"""
from contextlib import contextmanager
import functools
import hashlib
import importlib.util
import ipaddress
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import sqlite3
import stat
import tempfile
import time
from urllib import error, request

RELEASE = 'spending-20261102-r1'
SOURCE_TAG = 'production-claude-spend-20261102-r1'
SERVICE = 'ztapi-spending-20261102-r1'
READ_USER = 'ztapi_spending_20261102_r1'
MAIN, NGINX, MYSQL = 'ztapi-server-1', 'ztapi-nginx-1', 'ztapi-mysql-1'
SUPPORT = 'ztapi-support-experience-20261102'
BOT = 'ztapi-telegram-support-experience-20261102'
NETWORK = 'ztapi_app'
DATA_NETWORK = 'ztapi_data'
EGRESS_NETWORK = 'ztapi_egress'
SERVICE_NETWORKS = {NETWORK, DATA_NETWORK, EGRESS_NETWORK}
DATA = '/opt/ztapi-spending/data'
BINDINGS = '/opt/ztapi-support/data'
BOT_CONFIG = '/opt/ztapi-support/experience-20261102/secrets/bot-production.json'
CONF = '/etc/nginx/conf.d/default.conf'
EDGE_LOCK = '/run/ztapi-traffic-hold-r6/owner.lock'
BEGIN = '# BEGIN ' + RELEASE
END = '# END ' + RELEASE

# Reviewed ceiling only. Actual grants come exclusively from the frozen JSON
# exported by spending.RequiredSourceColumns(), also used by Source.Preflight.
_BASE_COLUMNS = {
    'users': 'id,status,deleted_at,quota',
    'ztapi_request_settlements': 'id,user_id,operation_id,request_id,status,public_model,price_snapshot_json,usage_json,charge_dimensions_json,missing_dimensions_json,reserved_quota,charged_quota,refunded_quota,updated_at',
    'ztapi_settlement_log_outboxes': 'operation_id,created_at,log_id',
    'balance_ledgers': 'id,user_id,request_id,source_type,idempotency_key,delta,created_at',
    'ztapi_supplier_refund_charges': 'id,settlement_id,user_id,request_id,billing_proof_id,billing_operation_id,original_ledger_id,charged_quota,refunded_quota,price_snapshot_json',
    'ztapi_supplier_refunds': 'id,charge_id,request_id,status,ledger_id,refunded_quota',
    'logs': 'id,user_id,type,created_at,request_id,quota',
}


class ReleaseError(RuntimeError):
    pass


def require(ok, code):
    if not ok:
        raise ReleaseError(code)


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def encoded(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':')).encode()


def guarded(fn):
    @functools.wraps(fn)
    def call(*args, **kwargs):
        try:
            return fn(*args, **kwargs)
        except ReleaseError:
            raise
        except Exception as exc:
            raise ReleaseError('operation_failed:' + sha((type(exc).__name__ + str(exc)).encode())) from None
    return call


def strict_json(raw):
    def pairs(items):
        result = {}
        for key, value in items:
            require(key not in result, 'duplicate_json_member')
            result[key] = value
        return result
    return json.loads(raw, object_pairs_hook=pairs)


def telegram_projection(raw):
    config = strict_json(raw)
    token = config.get('bot_token') if isinstance(config, dict) else None
    require(isinstance(token, str) and re.fullmatch('[0-9]{1,20}:[A-Za-z0-9_-]{20,100}', token), 'bot_token_unavailable')
    return encoded({'bot_token': token})


def _validate_columns(value):
    require(isinstance(value, dict) and set(value) == set(GRANTS), 'source_projection_unknown')
    result = {}
    for table, cols in value.items():
        require(isinstance(cols, (list, tuple)) and cols and all(isinstance(c, str) for c in cols), 'source_projection_unknown')
        floor = set(_BASE_COLUMNS[table].split(','))
        if table == 'ztapi_request_settlements':
            floor -= {'updated_at', 'charge_dimensions_json'}
        require(floor <= set(cols) <= set(GRANTS[table]) and len(cols) == len(set(cols)), 'source_projection_unknown')
        cols = tuple(sorted(cols))
        result[table] = cols
    return result


GRANTS = {table: tuple(csv.split(',')) for table, csv in _BASE_COLUMNS.items()}
GRANTS['ztapi_request_settlements'] += ('created_at',)


@guarded
def source_grants(path):
    path = Path(path)
    require(path.is_file() and not path.is_symlink() and path.stat().st_size <= 65536, 'invalid_columns_artifact')
    return _validate_columns(strict_json(path.read_bytes()))


@guarded
def sql_identity(database, user, host):
    require(isinstance(database, str) and re.fullmatch('[A-Za-z][A-Za-z0-9_]{0,63}', database)
            and database.lower() not in ('mysql', 'sys', 'information_schema', 'performance_schema'), 'invalid_database')
    require(user == READ_USER, 'invalid_reader_user')
    net = ipaddress.IPv4Network(host, strict=True)
    require(net.is_private and 16 <= net.prefixlen <= 30
            and host == str(net.network_address) + '/' + str(net.netmask), 'invalid_reader_host')
    return "'" + user + "'@'" + host + "'"


def grant_sql(database, user, host, columns=None):
    account = sql_identity(database, user, host)
    columns = _validate_columns(columns)
    return '\n'.join('GRANT SELECT (' + ','.join('`' + col + '`' for col in cols) +
                     ') ON `' + database + '`.`' + table + '` TO ' + account + ';'
                     for table, cols in sorted(columns.items()))


def verify_grants(rows, database, user, host, columns=None):
    account = sql_identity(database, user, host)
    columns = _validate_columns(columns)
    found, usages = {}, 0
    for row in rows:
        row = row.strip().rstrip(';')
        if row == 'GRANT USAGE ON *.* TO ' + account:
            usages += 1
            continue
        match = re.fullmatch(r'GRANT SELECT\s*\(([^)]+)\) ON `([A-Za-z0-9_]+)`\.`([a-z_]+)` TO ' + re.escape(account), row)
        require(match is not None, 'reader_grants_not_minimal')
        csv, db, table = match.groups()
        cols = [part.strip().strip('`') for part in csv.split(',')]
        require(db == database and table in columns and table not in found
                and len(cols) == len(set(cols)) and set(cols) == set(columns[table]), 'reader_grants_not_minimal')
        found[table] = cols
    require(usages == 1 and set(found) == set(columns), 'reader_grants_not_minimal')


def protected_save(path, value, replace=False, mode=0o600):
    raw = value if isinstance(value, bytes) else encoded(value)
    require(not path.is_symlink(), 'unsafe_stage_file')
    if path.exists():
        require(path.is_file(), 'unsafe_stage_file')
        if os.name == 'posix':
            st = path.stat()
            require(st.st_uid == 0 and stat.S_IMODE(st.st_mode) in (0o600, 0o555), 'unsafe_stage_permissions')
        if path.read_bytes() == raw:
            return
        require(replace, 'immutable_stage_conflict')
    fd, temporary = tempfile.mkstemp(prefix='.spending-', dir=path.parent)
    try:
        with os.fdopen(fd, 'wb') as stream:
            stream.write(raw)
            stream.flush()
            os.fsync(stream.fileno())
        os.chmod(temporary, mode)
        os.replace(temporary, path)
        if os.name == 'posix':
            fd = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
            try:
                os.fsync(fd)
            finally:
                os.close(fd)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def _read(path):
    require(path.is_file() and not path.is_symlink(), 'unsafe_stage_file')
    if os.name == 'posix':
        require(path.stat().st_uid == 0 and stat.S_IMODE(path.stat().st_mode) == 0o600, 'unsafe_stage_permissions')
    return strict_json(path.read_bytes())


@contextmanager
def stage_lock(stage, backend):
    stage = Path(stage)
    require(stage.is_absolute(), 'absolute_stage_required')
    require(not any(p.is_symlink() for p in (stage, *stage.parents)), 'unsafe_stage_directory')
    if stage.exists():
        require(stage.is_dir(), 'unsafe_stage_directory')
        require(not any(stage.iterdir()) or (stage / 'stage.json').is_file(), 'unowned_stage')
    else:
        stage.mkdir(mode=0o700)
    backend.root_preflight(stage)
    marker = {'release': RELEASE, 'service': SERVICE}
    protected_save(stage / 'stage.json', marker)
    with file_lock(stage / '.owner.lock'):
        yield stage


@contextmanager
def file_lock(path):
    require(not path.is_symlink(), 'unsafe_lock')
    fd = os.open(path, os.O_CREAT | os.O_RDWR | getattr(os, 'O_NOFOLLOW', 0), 0o600)
    try:
        if os.name == 'posix':
            import fcntl
            st = os.fstat(fd)
            require(stat.S_ISREG(st.st_mode) and st.st_uid == 0 and stat.S_IMODE(st.st_mode) == 0o600, 'unsafe_lock')
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        yield
    finally:
        os.close(fd)


def check_capacity(capacity, binary_size):
    require(capacity['memory_available'] >= 512 << 20 and
            capacity['disk_available'] >= max(256 << 20, binary_size * 3) and
            all(capacity[k] is True for k in ('memory_limit', 'cpu_limit', 'pids_limit')), 'insufficient_resources')


def envmap(container):
    items = container['Config'].get('Env') or []
    require(all(isinstance(x, str) and '=' in x for x in items), 'invalid_container_env')
    result = dict(x.split('=', 1) for x in items)
    require(len(result) == len(items), 'duplicate_container_env')
    return result


def pins(baseline, backend):
    expected = baseline['containers']
    require({MAIN, NGINX, MYSQL, SUPPORT, BOT} <= set(expected), 'baseline_service_missing')
    current = {}
    for name, pin in expected.items():
        require(re.fullmatch('[a-zA-Z0-9_.-]+', name) is not None, 'invalid_container_name')
        c = backend.inspect(name)
        require(c['Id'] == pin['id'] and c['State']['Running']
                and c['State']['StartedAt'] == pin['started_at'], 'service_identity_changed')
        current[name] = c
    require(sha(backend.nginx_read()) == baseline['nginx_sha256'], 'nginx_baseline_changed')
    require(current[SUPPORT]['Config'].get('User') == '10001:10001', 'support_user_changed')
    require(not envmap(current[MAIN]).get('LOG_SQL_DSN'), 'separate_log_database_not_supported')
    require(not current[MYSQL]['HostConfig'].get('PortBindings'), 'mysql_published_ports')
    require(all(NETWORK in current[n]['NetworkSettings']['Networks'] for n in (MAIN, NGINX))
            and DATA_NETWORK in current[MYSQL]['NetworkSettings']['Networks']
            and SERVICE_NETWORKS <= set(current[SUPPORT]['NetworkSettings']['Networks']), 'service_network_changed')
    mounts = current[SUPPORT]['Mounts']
    require(sum(m.get('Type') == 'bind' and m.get('Source') == BINDINGS and m.get('Destination') == '/data'
                for m in mounts) == 1, 'support_binding_mount_changed')
    require(sum(m.get('Type') == 'bind' and m.get('Source') == BOT_CONFIG and m.get('RW') is False
                for m in current[BOT]['Mounts']) == 1, 'bot_readonly_config_changed')
    return current


def verify_networks(networks, expected=None):
    require(set(networks) == SERVICE_NETWORKS and all(n.get('Driver') == 'bridge' for n in networks.values()),
            'unsupported_spending_network')
    require(all(networks[n].get('Internal') is True for n in (NETWORK, DATA_NETWORK))
            and networks[EGRESS_NETWORK].get('Internal') is False, 'egress_network_policy_changed')
    if expected is not None:
        require(isinstance(expected, dict) and set(expected) == SERVICE_NETWORKS, 'invalid_network_baseline')
        def normalized(config):
            require(isinstance(config, list) and all(isinstance(c, dict) for c in config), 'invalid_network_ipam')
            return sorted(encoded({k: v for k, v in c.items() if v not in (None, '', {}, [])}) for c in config)
        for name in sorted(SERVICE_NETWORKS):
            live, pin = networks[name], expected[name]
            require(live['Id'] == pin['id'] and live['Driver'] == pin['driver'] and live['Internal'] is pin['internal']
                    and normalized(live['IPAM']['Config']) == normalized(pin['ipam']), 'spending_network_identity_changed')


def _account_host(baseline, backend):
    settings = baseline['spending']
    require(settings['db_network'] == DATA_NETWORK, 'invalid_database_network')
    net = ipaddress.IPv4Network(settings['db_subnet'], strict=True)
    require(str(net) == backend.subnet(DATA_NETWORK, baseline.get('spending_networks')), 'database_subnet_changed')
    return str(net.network_address) + '/' + str(net.netmask)


def ensure_reader(stage, baseline, backend, mysql_id, columns):
    columns = _validate_columns(columns)
    database, host = baseline['spending']['database'], _account_host(baseline, backend)
    account = sql_identity(database, READ_USER, host)
    path = stage / 'reader.private.json'
    owned = stage / 'reader-owned.json'
    rows = backend.admin_sql("SELECT Host,max_user_connections FROM mysql.user WHERE User='" + READ_USER + "' ORDER BY Host;").decode().splitlines()
    identity = {'release': RELEASE, 'user': READ_USER, 'host': host, 'database': database, 'mysql_id': mysql_id,
                'columns_sha256': sha(encoded(columns))}
    if rows:
        require(path.exists() and owned.exists(), 'existing_user_unowned')
        credentials = _read(path)
        require(_read(owned) == identity and all(credentials.get(k) == v for k, v in identity.items()), 'existing_user_unowned')
        require(rows == [host + '\t2'], 'reader_identity_changed')
    else:
        require(not path.exists() and not owned.exists(), 'reader_creation_incomplete')
        credentials = {**identity, 'password': secrets.token_hex(32)}
        protected_save(path, credentials)
        query = 'CREATE USER ' + account + " IDENTIFIED BY '" + credentials['password'] + "' WITH MAX_USER_CONNECTIONS 2;\n"
        backend.admin_sql(query + grant_sql(database, READ_USER, host, columns))
        protected_save(owned, identity)
    require(re.fullmatch('[a-f0-9]{64}', credentials['password']) is not None, 'invalid_reader_credential')
    verify_grants(backend.admin_sql('SHOW GRANTS FOR ' + account + ';').decode().splitlines(), database, READ_USER, host, columns)
    require(backend.reader_probe(credentials, columns) == READ_USER + '@' + host, 'reader_auth_identity_failed')
    return credentials


def reader_dsn(credentials):
    sql_identity(credentials['database'], credentials['user'], credentials['host'])
    require(re.fullmatch('[a-f0-9]{64}', credentials['password']) is not None, 'invalid_reader_credential')
    return credentials['user'] + ':' + credentials['password'] + '@tcp(' + MYSQL + ':3306)/' + credentials['database'] + '?parseTime=true&timeout=3s&readTimeout=3s&writeTimeout=3s'


@guarded
def container_spec(image, signing_key, credentials, stage, commit, base_env):
    require(re.fullmatch('sha256:[a-f0-9]{64}', image) is not None, 'unpinned_spending_image')
    require(isinstance(signing_key, str) and len(signing_key) >= 32 and '\n' not in signing_key and '\0' not in signing_key, 'session_key_unavailable')
    env = {**validated_image_env(base_env),
           'PATH': base_env.get('PATH', '/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin'),
           'LANG': base_env.get('LANG', 'C.UTF-8'), 'TZ': 'Asia/Shanghai', 'GIN_MODE': 'release', 'PORT': '3014',
           'ZTAPI_SOURCE_COMMIT': commit, 'ZTAPI_RELEASE_VERSION': RELEASE, 'ZTAPI_SOURCE_TAG': SOURCE_TAG,
           'SPENDING_READ_DSN': reader_dsn(credentials), 'SPENDING_READ_DRIVER': 'mysql',
           'SPENDING_QUOTA_PER_UNIT': '500000', 'SPENDING_PUBLIC_ORIGIN': 'https://ztapi.vip',
           'SPENDING_LISTEN': '0.0.0.0:3014', 'SPENDING_STORE': '/data/spending.db',
           'SPENDING_BINDING_STORE': '/bindings/support.db', 'SPENDING_TELEGRAM_CONFIG': '/run/secrets/spending-telegram.json',
           'ZTAPI_SESSION_SIGNING_KEY': signing_key, 'GOMAXPROCS': '1', 'GOMEMLIMIT': '192MiB'}
    return {'Image': image, 'User': '10001:10001', 'WorkingDir': '/tmp',
            'Entrypoint': ['/usr/local/bin/ztapi-spending'], 'Cmd': [], 'Healthcheck': {'Test': ['NONE']},
            'Env': [key + '=' + value for key, value in sorted(env.items())],
            'Labels': {'ztapi.spending.release': RELEASE, 'org.opencontainers.image.revision': commit},
            'ExposedPorts': {'3014/tcp': {}},
            'HostConfig': {'Binds': [DATA + ':/data:rw', BINDINGS + ':/bindings:ro',
                                    str(stage / 'telegram.json') + ':/run/secrets/spending-telegram.json:ro'],
                           'ReadonlyRootfs': True, 'Privileged': False, 'CapDrop': ['ALL'], 'CapAdd': [],
                           'SecurityOpt': ['no-new-privileges:true'], 'Tmpfs': {'/tmp': 'rw,noexec,nosuid,nodev,size=16m,mode=1777'},
                           'Memory': 256 << 20, 'MemorySwap': 256 << 20, 'NanoCpus': 500000000, 'PidsLimit': 64,
                           'PortBindings': {}, 'PublishAllPorts': False, 'NetworkMode': NETWORK,
                           'RestartPolicy': {'Name': 'unless-stopped', 'MaximumRetryCount': 0},
                           'LogConfig': {'Type': 'json-file', 'Config': {'max-size': '5m', 'max-file': '2'}}},
            'NetworkingConfig': {'EndpointsConfig': {n: {'Aliases': [SERVICE]} for n in sorted(SERVICE_NETWORKS)}}}


def verify_service(actual, desired):
    require(actual['Image'] == desired['Image'], 'spending_service_drift')
    config, host = actual['Config'], actual['HostConfig']
    for key in ('User', 'WorkingDir', 'Entrypoint', 'Labels', 'Healthcheck'):
        require(config.get(key) == desired[key], 'spending_service_drift')
    require((config.get('Cmd') or []) == desired['Cmd'] and sorted(config.get('Env') or []) == sorted(desired['Env']), 'spending_service_drift')
    for key, value in desired['HostConfig'].items():
        require((host.get(key) or ([] if isinstance(value, list) else {} if isinstance(value, dict) else host.get(key))) == value,
                'spending_service_drift')
    require(not any(host.get(k) for k in ('Devices', 'DeviceRequests', 'VolumesFrom', 'Links', 'ExtraHosts', 'PidMode'))
            and host.get('IpcMode', 'private') in ('private', ''), 'spending_service_drift')
    require(set(actual['NetworkSettings']['Networks']) == SERVICE_NETWORKS, 'spending_network_drift')
    expected_mounts = {}
    for bind in desired['HostConfig']['Binds']:
        source, destination, mode = bind.rsplit(':', 2)
        expected_mounts[destination] = (source, mode == 'rw')
    found = {}
    for mount in actual.get('Mounts') or []:
        if mount.get('Type') == 'tmpfs' and mount.get('Destination') == '/tmp':
            continue
        destination = mount.get('Destination')
        require(mount.get('Type') == 'bind' and destination not in found, 'spending_mount_drift')
        found[destination] = (mount.get('Source'), mount.get('RW'))
    require(found == expected_mounts, 'spending_mount_drift')


def validate_binary(build):
    require(re.fullmatch('[a-f0-9]{40}', build['commit']) is not None, 'invalid_source_commit')
    artifact = build['spending_binary']
    path = Path(artifact['path'])
    require(path.is_absolute() and path.is_file() and not path.is_symlink(), 'invalid_binary_path')
    require(64 <= path.stat().st_size <= 256 << 20, 'invalid_binary_size')
    raw = path.read_bytes()
    require(re.fullmatch('[a-f0-9]{64}', artifact['sha256']) is not None and sha(raw) == artifact['sha256'], 'spending_binary_hash_mismatch')
    require(len(raw) >= 64 and raw[:6] == b'\x7fELF\x02\x01' and raw[18:20] == b'\x3e\x00', 'linux_amd64_elf_required')
    return raw


def validate_columns(build):
    artifact = build.get('spending_columns')
    require(isinstance(artifact, dict), 'spending_columns_artifact_required')
    path = Path(artifact['path'])
    require(path.is_absolute() and path.is_file() and not path.is_symlink(), 'spending_columns_artifact_required')
    require(0 < path.stat().st_size <= 65536, 'invalid_columns_artifact')
    raw = path.read_bytes()
    require(re.fullmatch('[a-f0-9]{64}', artifact['sha256']) is not None
            and sha(raw) == artifact['sha256'], 'spending_columns_hash_mismatch')
    return raw, _validate_columns(strict_json(raw))


def validated_image_env(env):
    safe = {'PATH', 'LANG', 'TZ', 'GIN_MODE', 'PORT', 'ZTAPI_SOURCE_COMMIT', 'ZTAPI_RELEASE_VERSION', 'ZTAPI_SOURCE_TAG'}
    require(isinstance(env, dict) and set(env) <= safe, 'unsafe_base_image_defaults')
    require(all(isinstance(v, str) and 0 < len(v) <= 1024 and '\0' not in v and '\n' not in v for v in env.values()),
            'unsafe_base_image_defaults')
    return dict(env)


def build_spending_image(stage, original, build, raw, columns_raw, columns, backend):
    base = original['Image']
    require(re.fullmatch('sha256:[a-f0-9]{64}', base) is not None, 'unpinned_main_image')
    image = backend.inspect(base)
    base_config = image['Config']
    base_env = validated_image_env(envmap(image))
    require(not base_config.get('OnBuild')
            and set(base_config.get('Volumes') or {}) <= {'/data'}, 'unsafe_base_image_defaults')
    context = stage / 'spending-build'
    require(not context.is_symlink(), 'unsafe_build_context')
    context.mkdir(mode=0o700, exist_ok=True)
    require(set(p.name for p in context.iterdir()) <= {'Dockerfile', 'ztapi-spending'}, 'nonbinary_build_context')
    recipe = ('FROM ' + base + '\nCOPY ztapi-spending /usr/local/bin/ztapi-spending\n'
              'ENTRYPOINT ["/usr/local/bin/ztapi-spending"]\nCMD []\nUSER 10001:10001\nWORKDIR /tmp\n'
              'HEALTHCHECK NONE\nLABEL org.opencontainers.image.revision="' + build['commit'] + '"\n').encode()
    protected_save(context / 'Dockerfile', recipe)
    protected_save(context / 'ztapi-spending', raw, mode=0o555)
    protected_save(stage / 'spending-columns.json', columns_raw)
    manifest = {'release': RELEASE, 'commit': build['commit'], 'base_image': base,
                'select_grants': columns, 'safe_base_env': base_env,
                'artifacts': {'spending-build/Dockerfile': sha(recipe), 'spending-build/ztapi-spending': sha(raw),
                              'spending-columns.json': sha(columns_raw)}}
    # A build interrupted before its receipt can be repeated without touching DB users.
    tag = 'ztapi-spending:' + RELEASE + '-' + sha(encoded(manifest))[:16]
    image_id = backend.build_image(context, tag)
    require(re.fullmatch('sha256:[a-f0-9]{64}', image_id) is not None, 'unpinned_spending_image')
    manifest['image'] = image_id
    protected_save(stage / 'build.json', manifest)
    return image_id


def check_default_off(path):
    require(path.is_file() and not path.is_symlink(), 'spending_store_missing')
    db = sqlite3.connect(path.resolve().as_uri() + '?mode=ro', uri=True, timeout=3)
    try:
        for table in ('spending_preferences', 'spending_alerts'):
            require(db.execute('SELECT COUNT(*) FROM ' + table).fetchone()[0] == 0, 'store_not_empty')
    finally:
        db.close()
    return {'default_off': True, 'customer_writes': 0, 'telegram_calls': 0, 'model_calls': 0}


_LEXER = re.compile(rb'\s+|\#[^\n]*|"(?:\\.|[^"\\])*"|\x27(?:\\.|[^\x27\\])*\x27|[{};]|[^\s{};#"\x27]+')


def _nginx_tree(raw):
    require(isinstance(raw, bytes) and b'\0' not in raw and len(raw) <= 1 << 20, 'invalid_nginx_config')
    stack, pending, nodes, end = [], [], [], 0
    for match in _LEXER.finditer(raw):
        require(match.start() == end, 'unknown_nginx_syntax')
        end = match.end()
        token = match.group()
        if token.isspace() or token.startswith(b'#'):
            continue
        if token == b'{':
            require(pending, 'unknown_nginx_syntax')
            node = {'head': [x[0] for x in pending], 'start': pending[0][1],
                    'parent': stack[-1] if stack else None, 'directives': []}
            nodes.append(node)
            stack.append(node)
            pending = []
        elif token == b'}':
            require(stack and not pending, 'unknown_nginx_syntax')
            stack.pop()['end'] = match.end()
        elif token == b';':
            require(pending, 'unknown_nginx_syntax')
            if stack:
                stack[-1]['directives'].append([x[0].strip(b'"\x27') for x in pending])
            pending = []
        else:
            pending.append((token, match.start()))
    require(end == len(raw) and not stack and not pending, 'unknown_nginx_syntax')
    return nodes


def nginx_without_spending(raw):
    begin, end = BEGIN.encode(), END.encode()
    if begin not in raw and end not in raw:
        require(b'spending' not in raw.lower(), 'unknown_spending_marker')
        return raw
    require(raw.count(begin) == raw.count(end) == 1, 'unknown_spending_marker')
    start = raw.rfind(b'\n', 0, raw.index(begin)) + 1
    finish = raw.find(b'\n', raw.index(end))
    require(finish >= 0 and not raw[start:raw.index(begin)].strip(), 'unknown_spending_marker')
    base = raw[:start] + raw[finish + 1:]
    require(b'spending' not in base.lower(), 'unknown_spending_marker')
    require(_insert_route(base) == raw, 'spending_route_modified')
    return base


def _insert_route(raw):
    nodes = _nginx_tree(raw)
    anchors = [n for n in nodes if n['head'] in ([b'location', b'^~', b'/api/support/'], [b'location', b'/api/support/'])]
    require(len(anchors) == 1, 'support_anchor_not_unique')
    anchor = anchors[0]
    server = anchor['parent']
    require(server and server['head'] == [b'server'] and server['parent'] is None, 'support_anchor_not_public')
    ds = server['directives']
    require(any(d[0] == b'server_name' and b'ztapi.vip' in d[1:] for d in ds)
            and any(d[0] == b'listen' and b'ssl' in d[1:] and any(x == b'443' or x.endswith(b':443') for x in d[1:]) for d in ds),
            'support_anchor_not_public')
    start = raw.rfind(b'\n', 0, anchor['start']) + 1
    indent = raw[start:anchor['start']]
    require(not indent.strip(), 'support_anchor_not_line_aligned')
    lines = [BEGIN, 'location ^~ /api/spending/ {', '    proxy_pass http://' + SERVICE + ':3014;',
             '    proxy_http_version 1.1;', '    proxy_set_header Host $host;', '    proxy_set_header Connection "";',
             '    proxy_connect_timeout 3s;', '    proxy_read_timeout 10s;', '    proxy_send_timeout 10s;',
             '    client_max_body_size 8k;', '    add_header Cache-Control "no-store" always;', '}', END]
    insertion = b''.join(indent + line.encode() + b'\n' for line in lines)
    result = raw[:start] + insertion + raw[start:]
    _nginx_tree(result)
    return result


def nginx_with_spending(raw):
    return _insert_route(nginx_without_spending(raw))


@guarded
def prepare(baseline, build, *, stage, backend=None):
    backend = backend if backend is not None else LinuxHost(stage)
    with stage_lock(stage, backend) as stage:
        current = pins(baseline, backend)
        original = backend.nginx_read()
        require(nginx_without_spending(original) == original, 'route_already_present')
        nginx_with_spending(original)
        prior = backend.optional(SERVICE)
        created_path = stage / 'created.json'
        if prior is not None:
            require(created_path.exists() and _read(created_path).get('container') == prior['Id'], 'existing_service_unowned')
        else:
            require(not created_path.exists(), 'created_service_missing')
        raw = validate_binary(build)
        columns_raw, columns = validate_columns(build)
        check_capacity(backend.capacity(stage), len(raw))
        # Validate all nonsecret inputs before granting anything or creating data.
        sql_identity(baseline['spending']['database'], READ_USER, _account_host(baseline, backend))
        signing_key = envmap(current[SUPPORT]).get('ZTAPI_SESSION_SIGNING_KEY', '')
        require(len(signing_key) >= 32, 'session_key_unavailable')
        projection = telegram_projection(backend.read_bot_config())
        protected_save(stage / 'nginx-before.conf', original)
        image = build_spending_image(stage, current[MAIN], build, raw, columns_raw, columns, backend)
        backend.ensure_store(stage)
        credentials = ensure_reader(stage, baseline, backend, current[MYSQL]['Id'], columns)
        backend.project_telegram(stage, projection)
        desired = container_spec(image, signing_key, credentials, stage, build['commit'], _read(stage / 'build.json')['safe_base_env'])
        fingerprint = sha(encoded(desired))
        prior = backend.optional(SERVICE)
        if prior is not None:
            require(created_path.exists() and _read(created_path) == {'container': prior['Id'], 'spec_sha256': fingerprint}, 'existing_service_unowned')
            verify_service(prior, desired)
            cid = prior['Id']
        else:
            require(not created_path.exists(), 'created_service_missing')
            pins(baseline, backend)
            cid = backend.create(SERVICE, desired)
            protected_save(created_path, {'container': cid, 'spec_sha256': fingerprint})
        backend.start(cid)
        backend.healthy(cid)
        verify_service(backend.inspect(SERVICE), desired)
        backend.check_default_off()
        pins(baseline, backend)
        result = {'prepared': True, 'service': SERVICE, 'container': cid, 'image': image,
                  'build_sha256': sha((stage / 'build.json').read_bytes()), 'nginx_sha256': sha(original),
                  'spec_sha256': fingerprint, 'commit': build['commit'],
                  'telegram_sha256': sha(projection),
                  'default_off': True, 'customer_writes': 0, 'telegram_calls': 0, 'model_calls': 0}
        protected_save(stage / 'prepared.json', result)
        return result


@guarded
def activate_route(baseline, *, stage, backend=None):
    backend = backend if backend is not None else LinuxHost(stage)
    with stage_lock(stage, backend) as stage, backend.edge_lock():
        prepared = _read(stage / 'prepared.json')
        require(prepared['prepared'] and prepared['service'] == SERVICE, 'not_prepared')
        manifest = _read(stage / 'build.json')
        require(sha((stage / 'build.json').read_bytes()) == prepared['build_sha256'], 'build_manifest_changed')
        require(set(manifest['artifacts']) == {'spending-build/Dockerfile', 'spending-build/ztapi-spending', 'spending-columns.json'},
                'invalid_artifact_manifest')
        for name, expected in manifest['artifacts'].items():
            path = stage / name
            require(path.is_file() and not path.is_symlink() and sha(path.read_bytes()) == expected, 'artifact_hash_mismatch')
        columns = source_grants(stage / 'spending-columns.json')
        require(columns == _validate_columns(manifest['select_grants']), 'source_projection_drift')
        original = (stage / 'nginx-before.conf').read_bytes()
        require(sha(original) == baseline['nginx_sha256'] == prepared['nginx_sha256'], 'route_baseline_changed')
        pins(baseline, backend)
        credentials = _read(stage / 'reader.private.json')
        support = backend.inspect(SUPPORT)
        desired = container_spec(prepared['image'], envmap(support).get('ZTAPI_SESSION_SIGNING_KEY', ''), credentials, stage,
                                 manifest['commit'], manifest['safe_base_env'])
        require(sha(encoded(desired)) == prepared['spec_sha256'], 'prepared_service_changed')
        actual = backend.inspect(SERVICE)
        require(actual['Id'] == prepared['container'] and actual['State']['Running'], 'prepared_service_changed')
        verify_service(actual, desired)
        backend.healthy(prepared['container'])
        require(sha(telegram_projection(backend.read_bot_config())) == prepared['telegram_sha256'], 'telegram_config_changed')
        require(sha((stage / 'telegram.json').read_bytes()) == prepared['telegram_sha256'], 'telegram_config_changed')
        backend.ensure_store(stage)
        backend.check_default_off()
        ensure_reader(stage, baseline, backend, backend.inspect(MYSQL)['Id'], columns)
        proposed = nginx_with_spending(original)
        pins(baseline, backend)
        backend.install_route(original, proposed, stage)
        result = {'activated': True, 'service': SERVICE, 'container': prepared['container'],
                  'nginx_sha256': sha(proposed), 'reload_only': True, 'customer_writes': 0, 'model_calls': 0, 'telegram_calls': 0}
        protected_save(stage / 'route-active.json', result)
        return result


class LinuxHost:
    """Narrow host adapter. No production action occurs until a public call."""
    def __init__(self, stage):
        helper = Path(__file__).with_name('longtext_security.py')
        spec = importlib.util.spec_from_file_location('_spending_private_longtext', helper)
        self.old = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.old)
        # A private module instance: never mutate a parent's imported helper.
        self.old.STAGE = Path(stage)

    def inspect(self, name):
        return self.old.inspect(name)

    def optional(self, name):
        return self.old.optional_container(name)

    def nginx_read(self):
        return self.old.docker('exec', NGINX, 'cat', CONF)

    def root_preflight(self, stage):
        require(os.name == 'posix' and os.geteuid() == 0, 'root_linux_required')
        st = stage.stat()
        require(st.st_uid == 0 and stat.S_IMODE(st.st_mode) == 0o700, 'unsafe_stage_permissions')

    def capacity(self, stage):
        info = self.old.api('GET', '/info')
        meminfo = dict(line.split(':', 1) for line in Path('/proc/meminfo').read_text().splitlines())
        parent = Path(DATA).parent
        while not parent.exists():
            parent = parent.parent
        require(not any(p.is_symlink() for p in (parent, *parent.parents)), 'unsafe_store_parent')
        return {'memory_available': int(meminfo['MemAvailable'].split()[0]) * 1024,
                'disk_available': min(shutil.disk_usage(stage).free, shutil.disk_usage(parent).free),
                'memory_limit': info.get('MemoryLimit') is True, 'cpu_limit': info.get('CpuCfsQuota') is True,
                'pids_limit': info.get('PidsLimit') is True}

    def subnet(self, name, expected=None):
        require(name == DATA_NETWORK, 'invalid_database_network')
        networks = {n: self.old.api('GET', '/networks/' + n) for n in sorted(SERVICE_NETWORKS)}
        verify_networks(networks, expected)
        network = networks[name]
        subnets = [c['Subnet'] for c in network['IPAM']['Config'] if ':' not in c.get('Subnet', '')]
        require(len(subnets) == 1, 'ambiguous_spending_subnet')
        return subnets[0]

    def read_bot_config(self):
        path = Path(BOT_CONFIG)
        require(not any(p.is_symlink() for p in (path, *path.parents)), 'unsafe_bot_config')
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
        try:
            st = os.fstat(fd)
            require(stat.S_ISREG(st.st_mode) and st.st_size <= 65536 and not stat.S_IMODE(st.st_mode) & 0o077, 'unsafe_bot_config')
            return os.read(fd, 65537)
        finally:
            os.close(fd)

    def admin_sql(self, query):
        command = 'test -n "$MYSQL_ROOT_PASSWORD" && MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysql --user=root --batch --raw --skip-column-names --connect-timeout=3'
        return self.old.docker('exec', '-i', MYSQL, 'sh', '-c', command, data=query.encode(), timeout=30)

    def reader_probe(self, credentials, grants):
        sql_identity(credentials['database'], credentials['user'], credentials['host'])
        ip = str(ipaddress.IPv4Address(self.inspect(MYSQL)['NetworkSettings']['Networks'][DATA_NETWORK]['IPAddress']))
        query = 'SELECT CURRENT_USER();\n' + '\n'.join('SELECT ' + ','.join('`' + c + '`' for c in cols) +
                ' FROM `' + credentials['database'] + '`.`' + table + '` LIMIT 0;' for table, cols in sorted(grants.items()))
        command = 'IFS= read -r MYSQL_PWD; export MYSQL_PWD; exec mysql --user="$1" --host="$2" --batch --raw --skip-column-names --connect-timeout=3'
        raw = self.old.docker('exec', '-i', MYSQL, 'sh', '-c', command, '_', READ_USER, ip,
                              data=(credentials['password'] + '\n' + query).encode(), timeout=30)
        return raw.decode().strip()

    def build_image(self, context, tag):
        self.old.docker('build', '--network=none', '--pull=false', '-t', tag, str(context), timeout=180)
        return self.inspect(tag)['Id']

    def ensure_store(self, stage):
        parent, data = Path(DATA).parent, Path(DATA)
        require(not any(p.is_symlink() for p in (data, *data.parents)), 'unsafe_store_directory')
        if not parent.exists():
            parent.mkdir(mode=0o700)
        require(parent.is_dir() and parent.stat().st_uid == 0 and not parent.stat().st_mode & 0o022, 'unsafe_store_parent')
        receipt = stage / 'store-owned.json'
        if data.exists():
            require(receipt.exists() and data.is_dir(), 'existing_store_unowned')
            proof = _read(receipt)
            st = data.stat()
            require(proof == {'path': DATA, 'device': st.st_dev, 'inode': st.st_ino}, 'store_identity_changed')
        else:
            require(not receipt.exists(), 'owned_store_missing')
            data.mkdir(mode=0o700)
            os.chown(data, 10001, 10001)
            st = data.stat()
            proof = {'path': DATA, 'device': st.st_dev, 'inode': st.st_ino}
            protected_save(receipt, proof)
        st = data.stat()
        require(st.st_uid == st.st_gid == 10001 and stat.S_IMODE(st.st_mode) == 0o700, 'unsafe_store_permissions')
        binding = Path(BINDINGS) / 'support.db'
        require(binding.is_file() and not any(p.is_symlink() for p in (binding, *binding.parents)), 'binding_store_missing')
        require(not (data / 'spending.db').exists() or not os.path.samefile(data / 'spending.db', binding), 'store_binding_overlap')
        return proof

    def project_telegram(self, stage, raw):
        path = stage / 'telegram.json'
        if path.exists():
            require(not path.is_symlink() and path.is_file() and path.read_bytes() == raw, 'telegram_projection_changed')
        else:
            protected_save(path, raw)
            os.chown(path, 10001, 10001)
            os.chmod(path, 0o400)
        st = path.stat()
        require(st.st_uid == st.st_gid == 10001 and stat.S_IMODE(st.st_mode) == 0o400, 'unsafe_telegram_projection')

    def create(self, name, desired):
        require(name == SERVICE, 'unowned_service_create')
        primary = dict(desired)
        primary['NetworkingConfig'] = {'EndpointsConfig': {NETWORK: desired['NetworkingConfig']['EndpointsConfig'][NETWORK]}}
        cid = self.old.api('POST', '/containers/create?name=' + SERVICE, primary)['Id']
        protected_save(self.old.STAGE / 'created.json', {'container': cid, 'spec_sha256': sha(encoded(desired))})
        for network in sorted(SERVICE_NETWORKS - {NETWORK}):
            self.old.api('POST', '/networks/' + network + '/connect',
                         {'Container': cid, 'EndpointConfig': desired['NetworkingConfig']['EndpointsConfig'][network]})
        return cid

    def start(self, cid):
        if not self.inspect(cid)['State']['Running']:
            self.old.docker('start', cid)

    def healthy(self, cid):
        opener = request.build_opener(request.ProxyHandler({}))
        def status(path):
            c = self.inspect(cid)
            require(c['State']['Running'], 'spending_not_running')
            ip = str(ipaddress.IPv4Address(c['NetworkSettings']['Networks'][NETWORK]['IPAddress']))
            url = 'http://' + ip + ':3014' + path
            try:
                with opener.open(url, timeout=3) as response:
                    return response.status, response.read(4097)
            except error.HTTPError as exc:
                return exc.code, exc.read(4097)
        for _ in range(15):
            try:
                health, body = status('/healthz')
                if health == 200 and len(body) <= 4096 and strict_json(body) == {'ok': True}:
                    require(status('/api/spending/config')[0] == 401, 'unauthenticated_spending_not_rejected')
                    return
            except (OSError, ValueError):
                pass
            time.sleep(1)
        raise ReleaseError('spending_health_timeout')

    def check_default_off(self):
        return check_default_off(Path(DATA) / 'spending.db')

    @contextmanager
    def edge_lock(self):
        require(Path(EDGE_LOCK).parent.is_dir(), 'parent_edge_lock_missing')
        with file_lock(Path(EDGE_LOCK)):
            yield

    def install_route(self, expected, desired, stage):
        require(self.nginx_read() == expected, 'route_cas_failed')
        proposal = stage / 'spending-nginx-test.conf'
        wrapped = b'pid /tmp/spending-nginx-test.pid; events {} http { include /etc/nginx/mime.types;\n' + desired + b'\n}\n'
        protected_save(proposal, wrapped)
        remote = '/tmp/spending-nginx-' + sha(desired) + '.conf'
        self.old.docker('cp', str(proposal), NGINX + ':' + remote)
        self.old.docker('exec', NGINX, 'nginx', '-t', '-c', remote)
        workers = self.old.nginx_workers()
        # The global edge lock and second comparison cover concurrent release writers.
        command = ('set -eu; test "$(sha256sum "$1" | cut -d " " -f1)" = "$2"; '
                   'test ! -e "$3"; umask 077; cat > "$3"; '
                   'test "$(sha256sum "$3" | cut -d " " -f1)" = "$4"; '
                   'test "$(sha256sum "$1" | cut -d " " -f1)" = "$2"; mv "$3" "$1"')
        def cas(old, new, suffix):
            temporary = '/etc/nginx/conf.d/.spending-' + suffix + '-' + secrets.token_hex(8)
            self.old.docker('exec', '-i', NGINX, 'sh', '-c', command, '_', CONF, sha(old), temporary, sha(new), data=new)
        cas(expected, desired, 'next')
        try:
            self.old.docker('exec', NGINX, 'nginx', '-t')
            self.old.docker('exec', NGINX, 'nginx', '-s', 'reload')
            self.old.wait_reload(workers)
            require(self.nginx_read() == desired, 'route_post_reload_drift')
        except BaseException:
            # Never overwrite an operator edit while recovering from a failed reload.
            require(self.nginx_read() == desired, 'route_rollback_cas_failed')
            cas(desired, expected, 'restore')
            self.old.docker('exec', NGINX, 'nginx', '-t')
            before = self.old.nginx_workers()
            self.old.docker('exec', NGINX, 'nginx', '-s', 'reload')
            self.old.wait_reload(before)
            raise
