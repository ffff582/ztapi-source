"""Pinned, bounded rolling rollout; never fences traffic or calls a model."""
import copy
from collections import Counter
from decimal import Decimal, InvalidOperation
import gzip
import hashlib
import http.client
import ipaddress
import json
import os
from pathlib import Path
import re
import socket
import stat
import subprocess
import sys
import tarfile
import tempfile
import threading
import time
from urllib import request, error, parse

STAGE = Path(__file__).resolve().parent
RELEASE = 'longtext-security-20261102-r3'
MAIN = 'ztapi-server-1'
NGINX = 'ztapi-nginx-1'
CANDIDATE = 'ztapi-longtext-security-candidate-r3'
PREVIOUS = 'ztapi-server-before-longtext-security-r3'
CONF = '/etc/nginx/conf.d/default.conf'
APP_NETWORK = 'ztapi_app'
DRAIN_TIMEOUT = 600


def require(ok, reason):
    if not ok:
        raise RuntimeError(reason)


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def run(args, data=None, timeout=90, combined=False):
    p = subprocess.run(args, input=data, capture_output=True, timeout=timeout)
    require(p.returncode == 0, 'command_failed:' + args[0] + ':' + sha(p.stderr))
    return p.stdout + p.stderr if combined else p.stdout


def docker(*args, **kwargs):
    return run(['docker', *args], **kwargs)


def inspect(name):
    return json.loads(docker('inspect', name))[0]


def save(name, value, replace=False):
    raw = value if isinstance(value, bytes) else json.dumps(value, sort_keys=True).encode()
    path = STAGE / name
    require(path.parent == STAGE and not path.is_symlink(), 'unsafe_receipt_path')
    if path.exists():
        require(path.is_file(), 'unsafe_receipt_type')
        previous = path.read_bytes()
        if previous == raw:
            return
        require(replace, 'immutable_receipt_conflict:' + name)
    fd, temporary = tempfile.mkstemp(prefix='.receipt-', dir=STAGE)
    try:
        with os.fdopen(fd, 'wb') as stream:
            stream.write(raw)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
        if hasattr(os, 'O_DIRECTORY'):
            directory = os.open(STAGE, os.O_RDONLY | os.O_DIRECTORY)
            try:
                os.fsync(directory)
            finally:
                os.close(directory)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def envmap(d):
    return dict(x.split('=', 1) for x in d['Config'].get('Env', []) if '=' in x)


def clone(original, image, commit, temporary):
    config = copy.deepcopy(original['Config'])
    host = copy.deepcopy(original['HostConfig'])
    env = envmap(original)
    env.update(ZTAPI_SOURCE_COMMIT=commit, ZTAPI_RELEASE_VERSION=RELEASE, ZTAPI_SOURCE_TAG='production-' + RELEASE,
               ZTAPI_SKIP_STARTUP_FX_REFRESH='true')
    if temporary:
        env['NODE_TYPE'] = 'slave'
        host['PortBindings'] = {}
        host['RestartPolicy'] = {'Name': 'no'}
    config.update(Image=image, Env=[k + '=' + v for k, v in env.items()])
    config['Labels'] = {**(config.get('Labels') or {}), 'ztapi.security.release': RELEASE}
    if temporary:
        config['Labels'] = {k: v for k, v in config['Labels'].items() if not k.startswith('com.docker.compose.')}
        config['Hostname'] = CANDIDATE
    endpoints = {}
    for name, network in original['NetworkSettings']['Networks'].items():
        endpoints[name] = {'Aliases': []} if temporary else network_endpoint(network)
    return {**config, 'HostConfig': host, 'NetworkingConfig': {'EndpointsConfig': endpoints}}


def network_endpoint(network):
    ipam = dict(network.get('IPAMConfig') or {})
    for key, source in [('IPv4Address', 'IPAddress'), ('IPv6Address', 'GlobalIPv6Address')]:
        if network.get(source):
            ipam[key] = network[source]
    return {'Aliases': network.get('Aliases') or [], 'IPAMConfig': ipam,
            **({'DriverOpts': network['DriverOpts']} if network.get('DriverOpts') else {})}


class UnixHTTP(http.client.HTTPConnection):
    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect('/var/run/docker.sock')


def api(method, path, value=None, missing=False):
    c = UnixHTTP('localhost', timeout=90)
    try:
        data = json.dumps(value).encode() if value is not None else None
        c.request(method, '/v1.44' + path, data, {'Content-Type': 'application/json'})
        response = c.getresponse()
        raw = response.read()
        if missing and response.status == 404:
            return None
        require(200 <= response.status < 300, 'docker_api_status:' + str(response.status))
        return json.loads(raw) if raw else None
    finally:
        c.close()


def create(name, value):
    require(value['Labels'].get('ztapi.security.release') == RELEASE, 'unowned_create')
    value = copy.deepcopy(value)
    endpoints = value['NetworkingConfig']['EndpointsConfig']
    primary = value['HostConfig']['NetworkMode']
    require(primary in endpoints, 'primary_network_missing')
    value['NetworkingConfig']['EndpointsConfig'] = {primary: endpoints[primary]}
    cid = api('POST', '/containers/create?name=' + name, value)['Id']
    save('created-' + name + '.json', {'id': cid, 'name': name}, replace=True)
    for network, endpoint in endpoints.items():
        if network != primary:
            api('POST', '/networks/' + network + '/connect', {'Container': cid, 'EndpointConfig': endpoint})
    return cid


def optional_container(name):
    return api('GET', '/containers/' + name + '/json', missing=True)


def sql(query):
    require(not re.search(r'\b(update|delete|insert|alter|drop|truncate)\b', query, re.I), 'readonly_sql_required')
    command = 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysql -uroot --batch --raw --skip-column-names "$MYSQL_DATABASE"'
    return docker('exec', '-i', 'ztapi-mysql-1', 'sh', '-c', command,
                  data=('START TRANSACTION READ ONLY;' + query + ';COMMIT;').encode())


def business_digests():
    query = "SELECT JSON_ARRAY(id,type,status,models,`group`,base_url,SHA2(COALESCE(ztapi_key_ciphertext,''),256),SHA2(COALESCE(`key`,''),256),model_mapping,header_override,param_override) FROM channels ORDER BY id"
    result = {'channels': sha(sql(query))}
    for table in ['ztapi_model_configs', 'ztapi_model_price_sources', 'ztapi_model_publication_snapshots']:
        result[table] = sha(sql('SELECT * FROM ' + table + ' ORDER BY id'))
    return result


def startup_preflight(container):
    env = envmap(container)
    require(env.get('ZTAPI_SKIP_STARTUP_FX_REFRESH') == 'true', 'startup_fx_skip_required')
    require(not env.get('LOG_SQL_DSN'), 'separate_log_database_proof_not_implemented')
    try:
        frequency = int(env.get('CHANNEL_TEST_FREQUENCY') or '0')
    except ValueError:
        raise RuntimeError('invalid_channel_test_frequency') from None
    require(frequency <= 0, 'automatic_channel_tests_enabled')
    require(not env.get('CHANNEL_UPDATE_FREQUENCY'), 'automatic_channel_updates_configured')
    for name, default in [('ZTAPI_HEALTH_SYNTHETIC_PROBES_ENABLED', 'false'), ('ZTAPI_HEALTH_VERIFICATION_PROBES_ENABLED', 'true')]:
        require(env.get(name, default).strip().lower() in ('false', '0', 'f'), 'paid_health_probes_not_disabled')
    require(sql("SELECT COUNT(*) FROM options WHERE `key`='ZTAPIFXInitialRepricingAt'").strip() == b'1', 'repricing_marker_required')
    try:
        rate = Decimal(sql('SELECT upstream_cny_per_usd FROM ztapi_fx_policies ORDER BY id DESC LIMIT 1').decode().strip())
    except (InvalidOperation, UnicodeError):
        raise RuntimeError('positive_current_fx_rate_required') from None
    require(rate.is_finite() and rate > 0, 'positive_current_fx_rate_required')
    require(sql("SELECT COUNT(*) FROM options WHERE `key`='monitor_setting' OR `key` LIKE 'monitor_setting.%'").strip() == b'0', 'monitor_settings_changed_from_disabled_default')
    require(sql('SELECT COUNT(*) FROM channels WHERE type=57 AND status IN (1,3)').strip() == b'0', 'codex_credential_refresh_channels_present')
    query = "SELECT COUNT(*) FROM channels WHERE status=1 AND JSON_UNQUOTE(JSON_EXTRACT(CASE WHEN JSON_VALID(settings) THEN settings ELSE '{}' END,'$.upstream_model_update_auto_sync_enabled'))='true'"
    require(sql(query).strip() == b'0', 'upstream_model_autosync_enabled')


def isolated_tests(image, build):
    image_env = envmap(inspect(image))
    require(not any(re.search(r'(?i)(secret|password|api.?key|token|dsn|credential|conn_string)', key) for key in image_env), 'image_embeds_credential_environment')
    filters = {
        'common-security.test': 'TestMaskSensitiveInfoRemovesCredentialShapes|TestLocalLogPreview|TestCredential|TestSecretBoundary|TestBusinessAPIError',
        'types-security.test': 'TestPublicStream|TestLocalErrorWrapper',
    }
    results = {}
    for name, pattern in filters.items():
        expected = build['test_binaries'][name]
        if isinstance(expected, dict):
            expected = expected['sha256']
        binary = STAGE / name
        require(re.fullmatch('[a-f0-9]{64}', expected) is not None and sha(binary.read_bytes()) == expected, 'security_test_binary_hash_mismatch')
        os.chmod(binary, 0o555)
        output = docker('run', '--rm', '--network=none', '--read-only', '--cap-drop=ALL',
                        '--security-opt=no-new-privileges', '--entrypoint', '/security.test',
                        '--mount', 'type=bind,src=' + str(binary) + ',dst=/security.test,readonly',
                        image, '-test.run=' + pattern, '-test.count=1', '-test.timeout=90s', timeout=110, combined=True)
        require(b'PASS' in output and b'no tests to run' not in output.lower(), 'security_test_did_not_pass')
        results[name] = {'sha256': expected, 'passed': True, 'output_sha256': sha(output), 'filter': pattern}
    save('security-tests.json', results)
    return results


class AvailabilityMonitor:
    def __init__(self, bindings, duration=DRAIN_TIMEOUT):
        require(0 < duration <= DRAIN_TIMEOUT, 'invalid_monitor_duration')
        self.bindings = bindings
        self.deadline = time.monotonic() + duration
        self.stopped = threading.Event()
        self.lock = threading.Lock()
        self.counts = Counter()
        self.failure = False
        self.thread = None

    def record(self, endpoint, status):
        status = status.decode().strip() if isinstance(status, bytes) else str(status)
        if not re.fullmatch('[0-9]{3}', status):
            status = '000'
        with self.lock:
            self.counts[endpoint + ':' + status] += 1
            self.failure = self.failure or status != '200'

    def probe(self):
        try:
            status = run(['curl', '--noproxy', '*', '--silent', '--show-error', '--output', '/dev/null', '--write-out', '%{http_code}',
                          '--max-time', '5', '--connect-timeout', '2', '--resolve', 'ztapi.vip:443:127.0.0.1',
                          'https://ztapi.vip/api/status'], timeout=7)
        except Exception:
            status = b'000'
        self.record('public', status)
        for item in self.bindings:
            try:
                if 'pid' in item:
                    mappings = []
                    for line in hosts_path(item['id']).read_bytes().splitlines():
                        fields = line.partition(b'#')[0].split()
                        if len(fields) > 1 and MAIN.encode() in fields[1:]:
                            mappings.append(str(ipaddress.IPv4Address(fields[0].decode())))
                    require(len(set(mappings)) <= 1, 'ambiguous_catalog_hosts')
                    target = mappings[0] if mappings else item['original_target']
                    status = run(['nsenter', '--target', str(item['pid']), '--net', 'curl', '--noproxy', '*', '--silent', '--show-error',
                                  '--output', '/dev/null', '--write-out', '%{http_code}', '--max-time', '5',
                                  '--connect-timeout', '2', '--resolve', MAIN + ':3000:' + target,
                                  'http://' + MAIN + ':3000/api/pricing'], timeout=7)
                else:
                    command = "wget -S -O /dev/null -T 5 http://" + MAIN + ":3000/api/pricing 2>&1 | awk '/HTTP\\/[0-9.]+ [0-9][0-9][0-9]/ {code=$2} END {print code ? code : \"000\"}'"
                    status = docker('exec', item['id'], 'sh', '-c', command, timeout=7)
            except Exception:
                status = b'000'
            self.record('catalog-' + item['id'][:12], status)

    def check(self):
        with self.lock:
            require(not self.failure, 'availability_monitor_failed')
        require(time.monotonic() < self.deadline, 'activation_deadline_exceeded')

    def snapshot(self):
        with self.lock:
            return dict(self.counts)

    def start(self):
        self.probe()
        self.check()
        def loop():
            while not self.stopped.wait(2):
                self.probe()
                if time.monotonic() >= self.deadline:
                    with self.lock:
                        self.failure = True
                    return
        self.thread = threading.Thread(target=loop, name='rollout-availability', daemon=True)
        self.thread.start()
        return self

    def close(self):
        self.stopped.set()
        if self.thread is not None:
            self.thread.join(timeout=25)
            require(not self.thread.is_alive(), 'availability_monitor_did_not_exit')


def validate_static(build):
    expected = build['public_files']
    require('index.html' in expected and len(expected) <= 5000, 'invalid_static_manifest')
    found = set()
    size = 0
    with tarfile.open(STAGE / 'public-dist.tar.gz', 'r:gz') as archive:
        for member in archive:
            name = member.name.removeprefix('./')
            require(member.isdir() or member.isfile(), 'unsupported_static_archive_entry')
            require(re.fullmatch(r'(?:[A-Za-z0-9_.-]+/)*[A-Za-z0-9_.-]+/?', name) is not None and '..' not in name.split('/'), 'unsafe_static_path')
            if member.isdir():
                continue
            require(name in expected and name not in found, 'unexpected_static_archive_file')
            require(re.fullmatch('[a-f0-9]{64}', expected[name]) is not None, 'invalid_static_digest')
            size += member.size
            require(0 <= member.size <= 64 << 20 and size <= 256 << 20, 'static_archive_too_large')
            with archive.extractfile(member) as stream:
                digest = hashlib.sha256()
                for block in iter(lambda: stream.read(1 << 20), b''):
                    digest.update(block)
                require(digest.hexdigest() == expected[name], 'static_file_hash_mismatch:' + name)
            found.add(name)
    require(found == set(expected), 'static_archive_missing_files')


def publish_static(build, old_index):
    validate_static(build)
    tag = sha((STAGE / 'public-dist.tar.gz').read_bytes())
    remote = '/tmp/ztapi-security-public-' + tag[:16]
    archive = remote + '.tar.gz'
    docker('cp', str(STAGE / 'public-dist.tar.gz'), NGINX + ':' + archive)
    require(docker('exec', NGINX, 'sha256sum', archive).split()[0].decode() == tag, 'uploaded_archive_hash_mismatch')
    docker('exec', NGINX, 'sh', '-c', 'mkdir -p "$1" && tar -xzf "$2" -C "$1"', '_', remote, archive)
    for name, digest in build['public_files'].items():
        require(docker('exec', NGINX, 'sha256sum', remote + '/' + name).split()[0].decode() == digest, 'staged_static_hash_mismatch')
    require(docker('exec', NGINX, 'cat', '/usr/share/nginx/public/index.html') == old_index, 'public_index_compare_and_swap_failed')
    try:
        for name, digest in build['public_files'].items():
            if name == 'index.html':
                continue
            destination = '/usr/share/nginx/public/' + name
            existing = docker('exec', NGINX, 'sh', '-c', 'if test -f "$1"; then sha256sum "$1"; fi', '_', destination)
            require(not existing or existing.split()[0].decode() == digest, 'existing_static_asset_conflict:' + name)
            docker('exec', NGINX, 'sh', '-c', 'mkdir -p "$(dirname "$2")" && cp "$1" "$2"', '_', remote + '/' + name, destination)
            require(docker('exec', NGINX, 'sha256sum', destination).split()[0].decode() == digest, 'live_static_hash_mismatch')
        docker('exec', NGINX, 'sh', '-c', 'cp "$1/index.html" /usr/share/nginx/public/.index-security-next && mv /usr/share/nginx/public/.index-security-next /usr/share/nginx/public/index.html', '_', remote)
        require(sha(docker('exec', NGINX, 'cat', '/usr/share/nginx/public/index.html')) == build['public_files']['index.html'], 'public_index_mismatch')
    except BaseException:
        restore_index(old_index, build['public_files']['index.html'])
        raise


def restore_index(original, published_digest):
    current = docker('exec', NGINX, 'cat', '/usr/share/nginx/public/index.html')
    if current == original:
        return
    require(sha(current) == published_digest, 'public_index_rollback_compare_and_swap_failed')
    docker('exec', '-i', NGINX, 'sh', '-c', 'cat > /usr/share/nginx/public/.index-security-rollback && mv /usr/share/nginx/public/.index-security-rollback /usr/share/nginx/public/index.html', data=original)
    require(docker('exec', NGINX, 'cat', '/usr/share/nginx/public/index.html') == original, 'public_index_restore_unverified')


def mapped_hosts(original, nginx_ip):
    address = str(ipaddress.IPv4Address(nginx_ip)).encode()
    result = []
    for line in original.splitlines(keepends=True):
        body, separator, comment = line.partition(b'#')
        words = body.split()
        if len(words) < 2 or MAIN.encode() not in words[1:]:
            result.append(line)
            continue
        aliases = [word for word in words[1:] if word != MAIN.encode()]
        if aliases:
            result.append(words[0] + b'\t' + b' '.join(aliases) + (b' #' + comment.rstrip(b'\r\n') if separator else b'') + b'\n')
        elif separator:
            result.append(b'#' + comment.rstrip(b'\r\n') + b'\n')
    raw = b''.join(result)
    return raw + (b'\n' if raw and not raw.endswith(b'\n') else b'') + address + b'\t' + MAIN.encode() + b' # ' + RELEASE.encode() + b'\n'


def hosts_path(cid):
    container = inspect(cid)
    require(container['State']['Running'] and container['State']['Pid'] > 0, 'catalog_consumer_not_running')
    path = Path(container['HostsPath'])
    require(path.is_absolute() and path.is_file() and not path.is_symlink(), 'unsafe_docker_hosts_file')
    return path


def hosts_cas(cid, expected, desired):
    path = hosts_path(cid)
    descriptor = os.open(path, os.O_RDWR | getattr(os, 'O_NOFOLLOW', 0))
    with os.fdopen(descriptor, 'r+b') as stream:
        require(stat.S_ISREG(os.fstat(stream.fileno()).st_mode), 'unsafe_docker_hosts_file')
        if os.name == 'posix':
            import fcntl
            fcntl.flock(stream, fcntl.LOCK_EX)
        current = stream.read()
        if current == desired:
            return
        require(current == expected, 'sidecar_hosts_compare_and_swap_failed')
        stream.seek(0)
        require(stream.write(desired) == len(desired), 'sidecar_hosts_short_write')
        stream.truncate()
        stream.flush()
        os.fsync(stream.fileno())
        stream.seek(0)
        require(stream.read() == desired, 'sidecar_hosts_write_unverified')


def catalog_bindings(baseline):
    result = []
    for name in baseline['containers']:
        container = inspect(name)
        urls = [value for key, value in envmap(container).items()
                if key in ('SUPPORT_CATALOG_URL', 'TELEGRAM_SUPPORT_CATALOG_URL')]
        if not urls:
            continue
        require(all(value == 'http://' + MAIN + ':3000/api/pricing' for value in urls), 'unreviewed_direct_catalog_url')
        cid = container['Id']
        path = STAGE / ('hosts-' + cid + '.original')
        raw = path.read_bytes() if path.exists() else hosts_path(cid).read_bytes()
        save(path.name, raw)
        result.append({'id': cid, 'ip': container['NetworkSettings']['Networks'][APP_NETWORK]['IPAddress'], 'original': raw,
                       'pid': container['State']['Pid'], 'original_target': inspect(MAIN)['NetworkSettings']['Networks'][APP_NETWORK]['IPAddress']})
    require(len(result) == 2 and len({item['ip'] for item in result}) == 2, 'two_direct_catalog_consumers_required')
    return result


def catalog_hosts(bindings, nginx_ip, restore=False):
    for binding in bindings:
        original = binding['original']
        mapped = mapped_hosts(original, nginx_ip)
        hosts_cas(binding['id'], mapped if restore else original, original if restore else mapped)


def catalog_config(original, target, nginx_ip, bindings):
    require(re.fullmatch(r'[a-z0-9-]+', target) is not None, 'invalid_backend_name')
    address = str(ipaddress.IPv4Address(nginx_ip))
    allow = ''.join('        allow ' + str(ipaddress.IPv4Address(item['ip'])) + ';\n' for item in bindings)
    return original + ('\n# ' + RELEASE + ' catalog bridge\nserver {\n    listen ' + address + ':3000;\n'
        '    server_name _;\n    location = /api/pricing {\n' + allow +
        '        deny all;\n        limit_except GET { deny all; }\n'
        '        proxy_pass http://' + target + ':3000/api/pricing;\n'
        '        proxy_http_version 1.1;\n        proxy_set_header Connection close;\n'
        '    }\n    location / { return 404; }\n}\n').encode()


def handoff_config(original, target, commit):
    require(re.fullmatch(r'[a-z0-9-]+', target) is not None, 'invalid_backend_name')
    require(b'http://server:3000' in original, 'main_upstream_missing')
    result = original.replace(b'http://server:3000', ('http://' + target + ':3000').encode())
    for header in [b'X-ZTAPI-Source', b'X-ZTAPI-UI-Source']:
        pattern = rb'(add_header ' + header + rb' "https://github.com/ffff582/ztapi-source/tree/)[a-f0-9]{40}'
        result = re.sub(pattern, lambda m: m.group(1) + commit.encode(), result)
    return result


def nginx_workers():
    rows = docker('top', NGINX, '-eo', 'pid,args').decode().splitlines()
    return [int(x.split()[0]) for x in rows[1:] if 'nginx: worker process' in x]


def install_nginx(raw, expected):
    current = docker('exec', NGINX, 'cat', CONF)
    require(current in (expected, raw), 'nginx_compare_and_swap_failed')
    test = b'pid /tmp/ztapi-security-test.pid; events {} http { include /etc/nginx/mime.types;\n' + raw + b'\n}\n'
    save('nginx-proposal-' + sha(raw)[:12] + '.conf', test)
    path = str(STAGE / ('nginx-proposal-' + sha(raw)[:12] + '.conf'))
    docker('cp', path, NGINX + ':/tmp/ztapi-security-test.conf')
    docker('exec', NGINX, 'nginx', '-t', '-c', '/tmp/ztapi-security-test.conf')
    workers = nginx_workers()
    command = 'set -eu; test "$(sha256sum "$1" | cut -d " " -f1)" = "$2"; cat > /etc/nginx/conf.d/.security-next; mv /etc/nginx/conf.d/.security-next "$1"'
    try:
        docker('exec', '-i', NGINX, 'sh', '-c', command, '_', CONF, sha(current), data=raw)
        docker('exec', NGINX, 'nginx', '-t')
        docker('exec', NGINX, 'nginx', '-s', 'reload')
        wait_reload(workers)
    except BaseException:
        docker('exec', '-i', NGINX, 'sh', '-c', 'cat > /etc/nginx/conf.d/.security-restore && mv /etc/nginx/conf.d/.security-restore ' + CONF, data=current)
        docker('exec', NGINX, 'nginx', '-t')
        docker('exec', NGINX, 'nginx', '-s', 'reload')
        raise
    save('nginx-installed-' + sha(raw)[:12] + '.json', {'sha256': sha(raw), 'retired_workers': workers, 'reload_confirmed': True}, replace=True)
    return workers


def wait_reload(previous):
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        if set(nginx_workers()) - set(previous):
            return
        time.sleep(0.2)
    raise RuntimeError('nginx_new_workers_not_observed')


def drained(clients, upstreams, workers_done, flushed, quiet_seconds, interval):
    return not clients and not upstreams and workers_done and flushed and quiet_seconds >= 2 * interval


def count_sockets(raw, table, providers, infrastructure=None):
    infrastructure = infrastructure or set()
    clients = upstreams = 0
    for row in raw.splitlines()[1:]:
        parts = row.split()
        require(len(parts) >= 4, 'invalid_socket_row')
        # TIME_WAIT and closed/listening sockets carry no in-process work.
        if parts[3] in ('06', '07', '0A'):
            continue
        local, remote = parts[1], parts[2]
        if int(local.rsplit(':', 1)[1], 16) == 3000:
            clients += 1
            continue
        packed = bytes.fromhex(remote.split(':')[0])
        address = ipaddress.ip_address(packed[::-1] if table == 'tcp' else b''.join(packed[i:i+4][::-1] for i in range(0, 16, 4)))
        if address.version == 6:
            address = address.ipv4_mapped or address
        peer = (str(address), int(remote.rsplit(':', 1)[1], 16))
        # Unknown private peers remain work, including a just-disabled channel.
        # Exclude only exact inspected DB/cache endpoints, never whole ports.
        if str(address) in providers or (not address.is_loopback and not address.is_unspecified and peer not in infrastructure):
            upstreams += 1
    return clients, upstreams


def upstream_addresses():
    hosts = set()
    for row in sql("SELECT DISTINCT base_url FROM channels WHERE status = 1 AND base_url IS NOT NULL AND base_url <> ''").decode().splitlines():
        host = parse.urlsplit(row if '://' in row else 'https://' + row).hostname
        require(host is not None, 'invalid_provider_host')
        try:
            hosts.add(str(ipaddress.ip_address(host)))
        except ValueError:
            for address in socket.getaddrinfo(host, None, type=socket.SOCK_STREAM):
                hosts.add(str(ipaddress.ip_address(address[4][0])))
    return hosts


def infrastructure_peers():
    result = set()
    for name, port in [('ztapi-mysql-1', 3306), ('ztapi-redis-1', 6379)]:
        networks = inspect(name)['NetworkSettings']['Networks']
        addresses = {entry[key] for entry in networks.values() for key in ('IPAddress', 'GlobalIPv6Address') if entry.get(key)}
        require(addresses, 'database_network_identity_missing')
        result.update((str(ipaddress.ip_address(address)), port) for address in addresses)
    return result


def settlement_scope(cid):
    recorded_at = time.time()
    started = inspect(cid)['State']['StartedAt'][:19].replace('T', ' ')
    require(re.fullmatch(r'\d{4}-\d\d-\d\d \d\d:\d\d:\d\d', started) is not None, 'invalid_container_start_time')
    maximum = int(sql('SELECT COALESCE(MAX(id),0) FROM ztapi_request_settlements').strip())
    raw = sql("SELECT COALESCE(JSON_ARRAYAGG(id),JSON_ARRAY()) FROM ztapi_request_settlements WHERE status='reserved' AND id <= " + str(maximum))
    reserved = json.loads(raw)
    require(len(reserved) <= 10000 and all(isinstance(value, int) and 0 < value <= maximum for value in reserved), 'unbounded_active_settlement_scope')
    return {'max_id': maximum, 'started_at': started, 'reserved_ids': reserved, 'recorded_at': recorded_at}


def settlements_idle(scope):
    maximum = int(scope['max_id'])
    started = scope['started_at']
    require(maximum >= 0 and re.fullmatch(r'\d{4}-\d\d-\d\d \d\d:\d\d:\d\d', started) is not None, 'invalid_settlement_scope')
    query = "SELECT COUNT(*) FROM ztapi_request_settlements WHERE status = 'reserved' AND id <= " + str(maximum)
    return sql(query).strip() == b'0'


def settlement_logs_consistent(scope):
    identifiers = scope.get('reserved_ids', [])
    require(len(identifiers) <= 10000 and all(isinstance(value, int) and value > 0 for value in identifiers), 'invalid_settlement_ids')
    if not identifiers:
        return True
    query = "SELECT COUNT(*) FROM ztapi_request_settlements s LEFT JOIN ztapi_settlement_log_outboxes o ON o.operation_id=s.operation_id "
    query += "LEFT JOIN ztapi_settlement_log_receipts r ON r.operation_id=s.operation_id LEFT JOIN logs l ON l.id=o.log_id "
    query += "WHERE s.id IN (" + ','.join(str(value) for value in identifiers) + ") AND s.status = 'settled' AND (o.operation_id IS NULL OR o.channel_stats_applied <> 1 OR "
    query += "(o.delivered_at > 0 AND (r.log_id IS NULL OR r.log_id <> o.log_id OR r.payload_sha256 <> o.payload_sha256 OR l.id IS NULL OR l.type <> 2 OR l.quota <> s.charged_quota OR l.request_id <> s.request_id)))"
    return sql(query).strip() == b'0'


def statistics_readback(scope):
    identifiers = scope.get('reserved_ids', [])
    if not identifiers:
        return None
    require(len(identifiers) <= 10000 and all(isinstance(value, int) and value > 0 for value in identifiers), 'invalid_settlement_ids')
    query = 'SELECT COALESCE(SUM(q.count),0),COALESCE(SUM(q.quota),0),COALESCE(SUM(q.token_used),0) FROM quota_data q WHERE EXISTS ('
    query += 'SELECT 1 FROM ztapi_request_settlements s WHERE s.id IN (' + ','.join(str(value) for value in identifiers)
    query += ') AND s.user_id=q.user_id AND s.public_model=q.model_name AND q.created_at >= UNIX_TIMESTAMP(s.created_at) DIV 3600 * 3600)'
    values = [int(value) for value in sql(query).split()]
    require(len(values) == 3 and min(values) >= 0, 'invalid_statistics_readback')
    return dict(zip(('count', 'quota', 'tokens'), values))


def database_errors(text):
    return bool(re.search(r'(?i)(\[error\]|\[fatal\]|gorm.*error|Error \d+|SQLSTATE|bad connection|invalid connection|connection is already closed|broken pipe|increaseQuotaData error:|failed to batch update|database is locked|deadlock|connection refused|constraint failed|duplicate entry|failed to (?:insert|create)|cannot (?:insert|create))', text))


def drain_settings(cid):
    env = envmap(inspect(cid))
    batch = env.get('BATCH_UPDATE_ENABLED', 'false') == 'true'
    interval = int(env.get('BATCH_UPDATE_INTERVAL', '5'))
    require(0 < interval <= 30, 'unreviewed_batch_interval')
    rows = sql("SELECT `key`,value FROM options WHERE `key` IN ('DataExportEnabled','DataExportInterval')").decode().splitlines()
    options = dict(row.split('\t', 1) for row in rows)
    enabled = options.get('DataExportEnabled', 'true') == 'true'
    period = int(options.get('DataExportInterval', '5')) * 60
    require(not enabled or 0 < period < DRAIN_TIMEOUT - 30, 'unreviewed_statistics_interval')
    return batch, interval, enabled, period


def wait_drain(cid, workers, timeout=DRAIN_TIMEOUT, scope=None, providers=None):
    require(0 < timeout <= DRAIN_TIMEOUT, 'invalid_drain_timeout')
    deadline = time.monotonic() + timeout
    quiet = None
    scope = scope if scope is not None else settlement_scope(cid)
    phase_since = str(scope.get('recorded_at', time.time()))
    log_since = phase_since
    providers = providers if providers is not None else upstream_addresses()
    infrastructure = infrastructure_peers()
    batch, interval, stats_enabled, _ = drain_settings(cid)
    stats_before = statistics_readback(scope) if stats_enabled else None
    batch_window = 2 * interval if batch else 10
    while time.monotonic() < deadline:
        pid = inspect(cid)['State']['Pid']
        require(pid > 0, 'draining_container_not_running')
        clients = upstreams = 0
        for table in ['tcp', 'tcp6']:
            current_clients, current_upstreams = count_sockets((Path('/proc') / str(pid) / 'net' / table).read_text(), table, providers, infrastructure)
            clients += current_clients
            upstreams += current_upstreams
        workers_done = not any(Path('/proc/' + str(p)).exists() for p in workers)
        empty = not clients and not upstreams and workers_done
        if empty and quiet is None:
            quiet = time.monotonic()
            log_since = str(time.time())
        elif not empty:
            quiet = None
        phase_logs = docker('logs', '--since', phase_since, cid, combined=True).decode(errors='replace')
        logs = phase_logs if log_since == phase_since else docker('logs', '--since', log_since, cid, combined=True).decode(errors='replace')
        flushed = not stats_enabled or '\u4fdd\u5b58\u6570\u636e\u770b\u677f\u6570\u636e\u6210\u529f\uff0c\u5171\u4fdd\u5b58' in logs
        healthy = not database_errors(phase_logs)
        seconds = time.monotonic() - quiet if quiet is not None else 0
        if not healthy:
            raise RuntimeError('database_error_during_drain')
        idle = empty and seconds >= batch_window and settlements_idle(scope) and settlement_logs_consistent(scope)
        if idle and flushed:
            stats_after = statistics_readback(scope) if stats_enabled else None
            require(stats_before is None or all(stats_after[key] >= value for key, value in stats_before.items()), 'statistics_readback_regressed')
            return {'client_sockets': 0, 'upstream_sockets': 0, 'workers_drained': True, 'statistics_flushed': True,
                    'batch_enabled': batch, 'settlement_scope': scope, 'scoped_reservations_active': 0, 'quiet_seconds': round(seconds, 1),
                    'financial_proof': 'terminal_pre_cut_reservations_and_durable_settlement_outbox',
                    'statistics_proof': 'post_quiet_flush_no_database_errors_and_scoped_nondecreasing_readback_not_per_request_commit_ack',
                    'statistics_readback': stats_after}
        print(json.dumps({'phase': 'drain', 'client_sockets': clients, 'upstream_sockets': upstreams, 'workers_done': workers_done, 'statistics_flushed': flushed}), flush=True)
        time.sleep(5)
    raise RuntimeError('bounded_drain_timeout_no_force_stop')


def http(cid, path):
    ip = inspect(cid)['NetworkSettings']['Networks']['ztapi_app']['IPAddress']
    opener = request.build_opener(request.ProxyHandler({}))
    try:
        with opener.open('http://' + ip + ':3000' + path, timeout=5) as response:
            return response.status, response.read(1 << 20)
    except error.HTTPError as e:
        return e.code, e.read(1 << 20)


def wait_healthy(cid):
    for _ in range(45):
        try:
            status, body = http(cid, '/api/status')
            if status == 200 and json.loads(body).get('success'):
                require(http(cid, '/v1/models')[0] == 401, 'unauthenticated_api_not_rejected')
                return
        except (OSError, ValueError):
            pass
        time.sleep(2)
    raise RuntimeError('candidate_health_timeout')


def pins(baseline, nginx=True, main=True):
    for name, expected in baseline['containers'].items():
        if name == MAIN and not main:
            continue
        current = inspect(name)
        require(current['Id'] == expected['id'] and current['State']['Running'] and current['State']['StartedAt'] == expected['started_at'], 'service_identity_changed:' + name)
    if nginx:
        require(sha(docker('exec', NGINX, 'cat', CONF)) == baseline['nginx_sha256'], 'nginx_baseline_changed')
    require(business_digests() == baseline['unchanged_business_digests'], 'price_channel_key_or_publication_changed')


def backup():
    command = 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysqldump -uroot --single-transaction --quick --no-tablespaces --routines --triggers "$MYSQL_DATABASE"'
    raw = docker('exec', 'ztapi-mysql-1', 'timeout', '-k', '5s', '90s', 'sh', '-c', command, timeout=100)
    packed = gzip.compress(raw)
    save('mysql-before.sql.gz', packed)
    require(gzip.decompress(packed) == raw, 'backup_roundtrip_failed')
    run(['gunzip', '-t', str(STAGE / 'mysql-before.sql.gz')])
    return {'sha256': sha(packed), 'gunzip_verified': True}


def build_image(original, build):
    binary = STAGE / 'ztapi-linux-amd64'
    require(sha(binary.read_bytes()) == build['binary_sha256'], 'binary_hash_mismatch')
    os.chmod(binary, 0o555)
    image = 'ztapi-main:longtext-security-r3'
    recipe = ('FROM ' + original['Image'] + '\nCOPY ztapi-linux-amd64 /usr/local/bin/ztapi\nLABEL org.opencontainers.image.revision="' + build['commit'] + '"\n').encode()
    save('main.Dockerfile', recipe)
    save('.dockerignore', b'**\n!main.Dockerfile\n!ztapi-linux-amd64\n')
    docker('build', '--network=none', '--pull=false', '-f', str(STAGE / 'main.Dockerfile'), '-t', image, str(STAGE), timeout=180)
    return image


def prepare(baseline, build):
    pins(baseline)
    original = inspect(MAIN)
    startup_preflight({'Config': clone(original, original['Image'], build['commit'], False)})
    validate_static(build)
    require(not original['HostConfig'].get('PortBindings'), 'main_published_ports_not_reviewed')
    save('original-main.private.json', original)
    save('nginx-original.conf', docker('exec', NGINX, 'cat', CONF))
    save('public-index-before.html', docker('exec', NGINX, 'cat', '/usr/share/nginx/public/index.html'))
    proof = backup()
    image = build_image(original, build)
    security_tests = isolated_tests(image, build)
    bindings = catalog_bindings(baseline)
    run(['nsenter', '--version'])
    run(['curl', '--version'])
    availability = AvailabilityMonitor(bindings)
    availability.probe()
    availability.check()
    save('availability-preflight.json', availability.snapshot(), replace=True)
    candidate = create(CANDIDATE, clone(original, image, build['commit'], temporary=True))
    docker('start', candidate)
    wait_healthy(candidate)
    startup_preflight(inspect(candidate))
    pins(baseline)
    result = {'prepared': True, 'candidate': candidate, 'image': inspect(image)['Id'], 'backup': proof, 'paid_calls': 0, 'security_tests': security_tests}
    save('prepared.json', result)
    return result


def remaining(monitor):
    monitor.check()
    return min(DRAIN_TIMEOUT, max(1, int(monitor.deadline - time.monotonic())))


def stop_drained(cid, monitor):
    monitor.check()
    # -1 never escalates TERM to KILL; a CLI timeout is an operator recovery gate.
    docker('stop', '--time=-1', cid, timeout=30)


def set_restart_policy(cid, policy):
    policy = copy.deepcopy(policy)
    policy.setdefault('MaximumRetryCount', 0)
    require(policy.get('Name') in ('no', 'always', 'unless-stopped', 'on-failure') and
            type(policy['MaximumRetryCount']) is int and policy['MaximumRetryCount'] >= 0, 'invalid_restart_policy')
    current = inspect(cid)['HostConfig'].get('RestartPolicy') or {'Name': 'no', 'MaximumRetryCount': 0}
    if current == policy:
        return
    api('POST', '/containers/' + cid + '/update', {'RestartPolicy': policy})
    require(inspect(cid)['HostConfig']['RestartPolicy'] == policy, 'restart_policy_update_unverified')


def candidate_recovery_policy(cid):
    candidate = inspect(cid)
    require(candidate['Config'].get('Labels', {}).get('ztapi.security.release') == RELEASE and
            envmap(candidate).get('NODE_TYPE') == 'slave', 'unowned_or_non_slave_recovery_candidate')
    path = STAGE / 'candidate-restart-policy.json'
    if path.exists():
        saved = json.loads(path.read_bytes())
        require(saved['candidate'] == cid, 'candidate_restart_policy_identity_changed')
    else:
        saved = {'candidate': cid, 'restart_policy': candidate['HostConfig'].get('RestartPolicy') or {'Name': 'no', 'MaximumRetryCount': 0}}
        save(path.name, saved)
    set_restart_policy(cid, {'Name': 'unless-stopped', 'MaximumRetryCount': 0})
    return saved['restart_policy']


def restore_original(original):
    current = inspect(original['Id'])
    if not current['State']['Running']:
        require(envmap(original).get('ZTAPI_SKIP_STARTUP_FX_REFRESH') == 'true', 'legacy_primary_startup_fx_unprotected')
    attached = current['NetworkSettings']['Networks']
    for name, endpoint in original['NetworkSettings']['Networks'].items():
        if name in attached:
            if not current['State']['Running']:
                # Docker clears live IP fields on stop. Reconnect from the
                # saved endpoint rather than treating empty fields as drift.
                docker('network', 'disconnect', name, original['Id'])
            else:
                require(attached[name].get('IPAddress') == endpoint.get('IPAddress') and set(attached[name].get('Aliases') or []) >= set(endpoint.get('Aliases') or []), 'old_network_endpoint_changed')
                continue
        if not current['State']['Running']:
            api('POST', '/networks/' + name + '/connect', {'Container': original['Id'], 'EndpointConfig': network_endpoint(endpoint)})
        else:
            raise RuntimeError('running_old_primary_missing_network')
    if current.get('Name') != '/' + MAIN:
        require(optional_container(MAIN) is None, 'canonical_name_still_owned')
        docker('rename', original['Id'], MAIN)
    if not current['State']['Running']:
        docker('start', original['Id'])
    wait_healthy(original['Id'])
    restored = inspect(original['Id'])['NetworkSettings']['Networks']
    for name, endpoint in original['NetworkSettings']['Networks'].items():
        require(name in restored and restored[name].get('IPAddress') == endpoint.get('IPAddress') and
                set(restored[name].get('Aliases') or []) >= set(endpoint.get('Aliases') or []), 'restored_network_unverified')
    set_restart_policy(original['Id'], original['HostConfig'].get('RestartPolicy') or {'Name': 'no', 'MaximumRetryCount': 0})


def recover(state, monitor, cause):
    original, candidate = state['original'], state['candidate']
    result = {'previous': original['Id'], 'candidate': candidate, 'primary': state.get('primary'),
              'cause_type': type(cause).__name__, 'cause_sha256': sha(str(cause).encode()), 'database_restored': False,
              'availability_recovered': False, 'full_old_master_restored': False}
    try:
        details = inspect(candidate)
        require(details['Config'].get('Labels', {}).get('ztapi.security.release') == RELEASE and
                envmap(details).get('NODE_TYPE') == 'slave', 'unowned_or_non_slave_recovery_candidate')
        if not details['State']['Running']:
            docker('start', candidate)
        wait_healthy(candidate)
        current_config = docker('exec', NGINX, 'cat', CONF)
        require(current_config in state['known_configs'], 'nginx_changed_outside_rollout')
        candidate_policy = candidate_recovery_policy(candidate)
        workers = install_nginx(state['candidate_config'], current_config)
        catalog_hosts(state['bindings'], state['nginx_ip'])
        result['candidate_serving'] = True
        result.update(availability_recovered=True, candidate_restart_policy='unless-stopped')
        save('operator-recovery-required.json', result, replace=True)
        restore_index(state['old_index'], state['build']['public_files']['index.html'])
        primary = state.get('primary')
        if primary is None:
            possible = optional_container(MAIN)
            if possible is not None and possible['Id'] != original['Id']:
                require(possible['Config'].get('Labels', {}).get('ztapi.security.release') == RELEASE, 'unowned_partial_primary')
                primary = possible['Id']
                state['primary'] = primary
        result['primary'] = primary
        old = inspect(original['Id'])
        if not old['State']['Running']:
            require(envmap(original).get('ZTAPI_SKIP_STARTUP_FX_REFRESH') == 'true', 'legacy_primary_startup_fx_unprotected')
        monitor.check()
        if primary is not None:
            new = inspect(primary)
            if new['State']['Running']:
                if state.get('primary_exposed'):
                    wait_drain(primary, workers, scope=state['primary_scope'], providers=state['providers'], timeout=remaining(monitor))
                stop_drained(primary, monitor)
            for name in inspect(primary)['NetworkSettings']['Networks']:
                docker('network', 'disconnect', name, primary)
            if inspect(primary).get('Name') == '/' + MAIN:
                docker('rename', primary, MAIN + '-failed-' + primary[:12])
        restore_original(original)
        catalog_hosts(state['bindings'], state['nginx_ip'], restore=True)
        current_config = docker('exec', NGINX, 'cat', CONF)
        install_nginx(state['old_config'], current_config)
        set_restart_policy(candidate, candidate_policy)
        result.update(rolled_back=True, old_primary_healthy=True, candidate_preserved=True, candidate_serving=False,
                      full_old_master_restored=True, candidate_restart_policy=candidate_policy['Name'])
    except BaseException as recovery_error:
        reason = str(recovery_error)
        result.update(rolled_back=False, recovery_type=type(recovery_error).__name__, recovery_sha256=sha(reason.encode()),
                      recovery_reason=reason if re.fullmatch('[a-z_]+', reason) else 'external_operation_failed')
    save('operator-recovery-required.json', result, replace=True)
    return result


def activate(baseline, build):
    pins(baseline)
    prepared = json.loads((STAGE / 'prepared.json').read_bytes())
    original = json.loads((STAGE / 'original-main.private.json').read_bytes())
    old_config = (STAGE / 'nginx-original.conf').read_bytes()
    old_index = (STAGE / 'public-index-before.html').read_bytes()
    candidate = prepared['candidate']
    startup_preflight(inspect(candidate))
    planned_primary = clone(original, prepared['image'], build['commit'], temporary=False)
    startup_preflight({'Config': planned_primary})
    validate_static(build)
    bindings = catalog_bindings(baseline)
    nginx_ip = inspect(NGINX)['NetworkSettings']['Networks'][APP_NETWORK]['IPAddress']
    candidate_config = catalog_config(handoff_config(old_config, CANDIDATE, build['commit']), CANDIDATE, nginx_ip, bindings)
    canonical = handoff_config(old_config, 'server', build['commit'])
    primary_config = catalog_config(canonical, 'server', nginx_ip, bindings)
    state = {'original': original, 'candidate': candidate, 'primary': None, 'primary_exposed': False,
             'candidate_config': candidate_config, 'old_config': old_config, 'old_index': old_index,
             'known_configs': (old_config, candidate_config, primary_config, canonical),
             'bindings': bindings, 'nginx_ip': nginx_ip, 'build': build, 'providers': upstream_addresses()}
    monitor = AvailabilityMonitor(bindings)
    try:
        monitor.start()
        wait_healthy(candidate)
        old_scope = settlement_scope(original['Id'])
        first_workers = install_nginx(candidate_config, old_config)
        catalog_hosts(bindings, nginx_ip)
        monitor.probe()
        monitor.check()
        save('handoff.json', {'candidate': candidate, 'nginx_sha256': sha(candidate_config), 'old_workers': first_workers}, replace=True)
        first_drain = wait_drain(original['Id'], first_workers, scope=old_scope, providers=state['providers'], timeout=remaining(monitor))
        pins(baseline, nginx=False)
        require(inspect(MAIN)['Id'] == original['Id'], 'main_identity_changed')
        startup_preflight(inspect(candidate))
        save('retire-old-intent.json', {'old': original['Id'], 'networks': original['NetworkSettings']['Networks']})
        save('old-restart-policy.json', {'old': original['Id'], 'restart_policy': original['HostConfig'].get('RestartPolicy') or {'Name': 'no', 'MaximumRetryCount': 0}})
        monitor.check()
        set_restart_policy(original['Id'], {'Name': 'no', 'MaximumRetryCount': 0})
        stop_drained(original['Id'], monitor)
        docker('rename', original['Id'], PREVIOUS)
        for name in original['NetworkSettings']['Networks']:
            docker('network', 'disconnect', name, original['Id'])
        state['primary'] = create(MAIN, planned_primary)
        startup_preflight(inspect(state['primary']))
        docker('start', state['primary'])
        wait_healthy(state['primary'])
        pins(baseline, nginx=False, main=False)
        state['primary_scope'] = settlement_scope(state['primary'])
        state['primary_exposed'] = True
        catalog_hosts(bindings, nginx_ip, restore=True)
        monitor.probe()
        monitor.check()
        candidate_scope = settlement_scope(candidate)
        second_workers = install_nginx(primary_config, candidate_config)
        second_drain = wait_drain(candidate, second_workers, scope=candidate_scope, providers=state['providers'], timeout=remaining(monitor))
        publish_static(build, old_index)
        require(sha(docker('exec', NGINX, 'cat', '/usr/share/nginx/admin/index.html')) == baseline['indexes']['admin'], 'admin_bundle_changed')
        install_nginx(canonical, primary_config)
        pins(baseline, nginx=False, main=False)
        monitor.probe()
        monitor.check()
        stop_drained(candidate, monitor)
        result = {'deployed': True, 'source_commit': build['commit'], 'main': state['primary'], 'image': prepared['image'],
                  'first_drain': first_drain, 'second_drain': second_drain, 'paid_calls': 0,
                  'business_digests': business_digests(), 'preserved_old_container': original['Id'], 'availability': monitor.snapshot()}
        save('published.json', result, replace=True)
        return result
    except BaseException as cause:
        recover(state, monitor, cause)
        raise
    finally:
        monitor.close()
        save('availability.json', monitor.snapshot(), replace=True)


if __name__ == '__main__':
    import fcntl
    # A single lock covers all edge writers; no firewall or traffic fence changes.
    lock = os.open('/run/ztapi-traffic-hold-r6/owner.lock', os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    baseline = json.loads((STAGE / 'baseline.json').read_bytes())
    build = json.loads((STAGE / 'build.json').read_bytes())
    action = {'prepare': prepare, 'activate': activate}[sys.argv[1]]
    print(json.dumps(action(baseline, build)), flush=True)
