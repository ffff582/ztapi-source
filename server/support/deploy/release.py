#!/usr/bin/env python3
"""Sidecar-only support release. Never recreates the main API or Nginx."""
import base64
import copy
from contextlib import closing
import gzip
import hashlib
import hmac
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import sqlite3
import subprocess
import sys
import tarfile
import time
from urllib import request, error

STAGE = Path(__file__).resolve().parent
ROOT = Path('/opt/ztapi-support')
TELEGRAM_ROOT = Path('/opt/ztapi-telegram-support')
MAIN = 'ztapi-server-1'
NGINX = 'ztapi-nginx-1'
FINANCE = 'ztapi-finance-support-20261102'
FINANCE_LIVE = FINANCE + '-live'
SUPPORT = 'ztapi-support-20261102'
TELEGRAM = 'ztapi-telegram-support-20261102'
CONF = '/etc/nginx/conf.d/default.conf'


def require(ok, reason):
    if not ok:
        raise RuntimeError(reason)


def run(args, data=None, timeout=90):
    p = subprocess.run(args, input=data, capture_output=True, timeout=timeout)
    require(p.returncode == 0, 'command_failed:' + args[0] + ':' + hashlib.sha256(p.stderr).hexdigest()[:16])
    return p.stdout


def docker(*args, **kwargs):
    return run(['docker', *args], **kwargs)


def inspect(name):
    return json.loads(docker('inspect', name))[0]


def envmap(container):
    return dict(x.split('=', 1) for x in container['Config']['Env'])


def finance_environment(original, commit, candidate):
    settings = dict(original)
    settings.update({'FINANCE_SOURCE_COMMIT': commit, 'FINANCE_SUPPORT_STORE': '/support/support.db', 'FINANCE_TELEGRAM_SUPPORT_STORE': '/telegram-support/support.db'})
    if candidate:
        settings['FINANCE_REPORT_ONLY'] = 'true'
    return settings


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def copy_public_ca(source, destination):
    shutil.copyfile(source, destination)
    os.chmod(destination, 0o644)


def save(path, data, mode=0o600):
    fd = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY | getattr(os, 'O_NOFOLLOW', 0), mode)
    with os.fdopen(fd, 'wb') as stream:
        stream.write(data)
        stream.flush()
        os.fsync(stream.fileno())


def json_save(name, value):
    save(STAGE / name, json.dumps(value, sort_keys=True).encode())


def write_env(path, values):
    require(all(isinstance(v, str) and not any(c in v for c in '\r\n\x00') for v in values.values()), 'invalid_environment')
    save(path, ''.join(k + '=' + v + '\n' for k, v in values.items()).encode())


def read_env(path):
    return dict(line.split('=', 1) for line in path.read_text().splitlines() if line)


def identity(container):
    return {k: container[k] for k in ['Id', 'Image']} | {'StartedAt': container['State']['StartedAt']}


def check_baseline(baseline, check_config=True):
    for name in [MAIN, NGINX, 'ztapi-finance']:
        current = inspect(name)
        expected = baseline['containers'][name]
        require(current['Id'] == expected['id'] and current['Image'] == expected['image'] and current['State']['StartedAt'] == expected['started_at'], 'runtime_baseline_changed:' + name)
    if check_config:
        require(digest(docker('exec', NGINX, 'cat', CONF)) == baseline['nginx_sha256'], 'nginx_baseline_changed')
        require(digest(docker('exec', NGINX, 'cat', '/usr/share/nginx/public/index.html')) == '30c7cc24bc6d8a837de325e007601cec39cb81d78118e24a2ba3bb5f950c4b10', 'public_R3_baseline_changed')
        require(digest(docker('exec', NGINX, 'cat', '/usr/share/nginx/admin/index.html')) == 'f189bc73179b409abb54c11a502c24a6b320ad8f98669e912e3a59f64f1aeb6a', 'admin_baseline_changed')


def jwt(key, uid):
    encode = lambda v: base64.urlsafe_b64encode(json.dumps(v, separators=(',', ':')).encode()).decode().rstrip('=')
    now = int(time.time())
    body = encode({'alg': 'HS256', 'typ': 'JWT'}) + '.' + encode({'iss': 'ztapi', 'sub': str(uid), 'iat': now, 'exp': now + 120, 'jti': secrets.token_hex(16)})
    return body + '.' + base64.urlsafe_b64encode(hmac.new(key.encode(), body.encode(), hashlib.sha256).digest()).decode().rstrip('=')


def fetch(url, key=None, uid=1, data=None, origin='https://ztapi.vip', timeout=65):
    headers = {'User-Agent': 'ZTAPI-Support-Acceptance/1.0', 'Accept': 'application/json'}
    if key:
        headers.update({'Authorization': 'Bearer ' + jwt(key, uid), 'New-Api-User': str(uid)})
    if data is not None:
        headers.update({'Content-Type': 'application/json', 'Origin': origin})
    req = request.Request(url, data=json.dumps(data).encode() if data is not None else None, headers=headers)
    opener = request.build_opener(request.ProxyHandler({}))
    def parsed(raw):
        try:
            return json.loads(raw)
        except (ValueError, UnicodeDecodeError):
            return None
    try:
        with opener.open(req, timeout=timeout) as response:
            return response.status, parsed(response.read(4 << 20))
    except error.HTTPError as e:
        return e.code, parsed(e.read(4 << 20))


def address(name, port):
    return 'http://' + inspect(name)['NetworkSettings']['Networks']['ztapi_app']['IPAddress'] + ':' + str(port)


def wait_health(name, port):
    for _ in range(30):
        try:
            if fetch(address(name, port) + '/healthz', timeout=3)[0] == 200:
                return
        except Exception:
            pass
        time.sleep(1)
    raise RuntimeError('sidecar_health_failed:' + name)


def mysql(statement):
    return docker('exec', '-i', 'ztapi-mysql-1', 'sh', '-c', 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysql -uroot --batch --raw --skip-column-names', data=statement.encode(), timeout=20).decode()


def provision(settings, quote, commit):
    from cryptography.hazmat.primitives.ciphers.aead import AESGCM
    ROOT.mkdir(mode=0o700)
    for directory in ['data', 'secrets', 'backups']:
        (ROOT / directory).mkdir(mode=0o700)
    os.chown(ROOT / 'data', 10001, 10001)
    cipher = mysql('START TRANSACTION READ ONLY; SELECT ztapi_key_ciphertext FROM ztapi.channels WHERE id=8 AND status=1; COMMIT;').strip()
    require(cipher.startswith('ztapi-key:v1:'), 'support_authorized_channel_unavailable')
    raw = cipher[len('ztapi-key:v1:'):]
    raw = base64.urlsafe_b64decode(raw + '=' * (-len(raw) % 4))
    secret = AESGCM(hashlib.sha256(settings['ZTAPI_UPSTREAM_MASTER_KEY'].encode()).digest()).decrypt(raw[:12], raw[12:], b'ztapi-channel-key:v1').decode()
    require(quote['group_multiplier'] == '0.6' and quote['cash_per_credit'] == '0.5' and quote['row']['model_name'] == 'deepseek-v4.1-flash', 'quote_basis_mismatch')
    provider = dict(endpoint='https://www.xcloudtoken.com/v1/chat/completions', api_key=secret, model='deepseek-v4.1-flash', channel_id=8, supplier='xcloud', group='国产分组', group_multiplier='0.6', cash_per_credit='0.5', input_credit_per_million='0.14', output_credit_per_million='0.5', cache_read_credit_per_million='0.003', quote_evidence='xcloud-public:' + quote['row']['pricing_version'] + ';sha256:' + quote['sha256'] + ';group=domestic:0.6;cash_per_credit=0.5;uncached_input=0.14;output=0.5;cache_read=0.003;unit=credit_per_million;recharge_100U_for_200_credit', vision_enabled=True)
    save(ROOT / 'secrets/provider.json', json.dumps(provider).encode(), 0o400)
    os.chown(ROOT / 'secrets/provider.json', 10001, 10001)
    os.chmod(ROOT / 'secrets', 0o711)
    reader, password = 'ztapi_support_ro_20261102', secrets.token_hex(24)
    grants = {
        'users': 'id,role,status,deleted_at',
        'ztapi_health_requests': 'execution_id,request_id,user_id,public_model,stream,started_at',
        'ztapi_health_events': 'id,execution_id,request_id,reason,http_status,result_valid,outcome',
        'ztapi_request_settlements': 'id,request_id,user_id,public_model,status,reserved_quota,charged_quota,refunded_quota,dispatched,usage_json,missing_dimensions_json,created_at',
        'ztapi_supplier_refund_charges': 'id,settlement_id,request_id,user_id,attempt,billing_proof_id,original_ledger_id,charged_quota,refunded_quota',
        'ztapi_supplier_refunds': 'request_id,charge_id,status,ledger_id,refunded_quota',
    }
    statement = "CREATE USER '" + reader + "'@'%' IDENTIFIED BY '" + password + "';\n"
    statement += ''.join('GRANT SELECT (' + columns + ') ON ztapi.' + table + " TO '" + reader + "'@'%';\n" for table, columns in grants.items())
    mysql(statement)
    actual = mysql("SHOW GRANTS FOR '" + reader + "'@'%';")
    require(not re.search(r'\b(INSERT|UPDATE|DELETE|ALTER|CREATE|DROP|ALL PRIVILEGES)\b', actual, re.I), 'support_reader_privilege_violation')
    save(ROOT / 'reader-grants.txt', actual.encode())
    common = {'ZTAPI_SESSION_SIGNING_KEY': settings['ZTAPI_SESSION_SIGNING_KEY'], 'SUPPORT_READ_DSN': reader + ':' + password + '@tcp(mysql:3306)/ztapi?charset=utf8mb4&parseTime=true&loc=Asia%2FShanghai&timeout=3s&readTimeout=3s&writeTimeout=3s', 'SUPPORT_STORE': '/data/support.db', 'SUPPORT_PROVIDER_CONFIG': '/run/secrets/provider.json', 'SUPPORT_PUBLIC_ORIGIN': 'https://ztapi.vip', 'SUPPORT_ADMIN_ORIGIN': 'https://admin.ztapi.vip', 'SUPPORT_CATALOG_URL': 'http://ztapi-server-1:3000/api/pricing', 'SUPPORT_ENABLED': 'true', 'SUPPORT_LISTEN': ':3012', 'SUPPORT_ENVIRONMENT': 'acceptance', 'GOMEMLIMIT': '160MiB', 'GOMAXPROCS': '2', 'SUPPORT_SOURCE_COMMIT': commit}
    write_env(ROOT / 'support-acceptance.env', common)
    write_env(ROOT / 'support.env', {**common, 'SUPPORT_ENVIRONMENT': 'production'})
    # The same bot continues sending operational alerts through the unchanged
    # main API. This process receives group support only and has no main DB/JWT.
    bot_token, owner = settings.get('ZTAPI_HEALTH_TELEGRAM_BOT_TOKEN', ''), settings.get('ZTAPI_HEALTH_TELEGRAM_CHAT_ID', '')
    require(re.fullmatch('[1-9][0-9]{0,19}', owner) is not None, 'existing_alert_recipient_is_not_private')
    class NoRedirect(request.HTTPRedirectHandler):
        def redirect_request(self, req, fp, code, msg, headers, newurl):
            return None
    opener = request.build_opener(request.ProxyHandler({}), NoRedirect())
    def telegram_api(method, data):
        req = request.Request('https://api.telegram.org/bot' + bot_token + '/' + method, data=json.dumps(data).encode(), headers={'Content-Type': 'application/json'})
        with opener.open(req, timeout=15) as response:
            value = json.loads(response.read(65536))
            require(value.get('ok') is True, 'telegram_identity_unavailable')
            return value['result']
    me, chat, webhook = telegram_api('getMe', {}), telegram_api('getChat', {'chat_id': int(owner)}), telegram_api('getWebhookInfo', {})
    require(me.get('is_bot') is True and me.get('username') == 'ztapi_bot' and chat.get('type') == 'private' and chat.get('id') == int(owner) and not webhook.get('url'), 'telegram_private_owner_or_receiver_mismatch')
    TELEGRAM_ROOT.mkdir(mode=0o700)
    for directory in ['data', 'secrets', 'backups']:
        (TELEGRAM_ROOT / directory).mkdir(mode=0o700)
    os.chown(TELEGRAM_ROOT / 'data', 10001, 10001)
    bot_config = {'bot_token': bot_token, 'owner_id': int(owner), 'bot_id': me['id'], 'bot_username': me['username'], 'provider': provider}
    save(TELEGRAM_ROOT / 'secrets/config.json', json.dumps(bot_config).encode(), 0o400)
    os.chown(TELEGRAM_ROOT / 'secrets/config.json', 10001, 10001)
    os.chmod(TELEGRAM_ROOT / 'secrets', 0o711)
    bot_env = {'TELEGRAM_SUPPORT_CONFIG': '/run/secrets/telegram-support.json', 'TELEGRAM_SUPPORT_STORE': '/data/support.db', 'TELEGRAM_SUPPORT_CATALOG_URL': 'http://ztapi-server-1:3000/api/pricing', 'TELEGRAM_SUPPORT_LISTEN': ':3013', 'TELEGRAM_SUPPORT_ENABLED': 'false', 'GOMEMLIMIT': '160MiB', 'GOMAXPROCS': '2'}
    write_env(TELEGRAM_ROOT / 'candidate.env', bot_env)
    write_env(TELEGRAM_ROOT / 'production.env', {**bot_env, 'TELEGRAM_SUPPORT_ENABLED': 'true'})


def create_support(image, acceptance):
    docker('create', '--name', SUPPORT, '--label', 'ztapi.support.release=20261102', '--network', 'ztapi_app', '--restart', 'unless-stopped', '--read-only', '--cap-drop=ALL', '--security-opt', 'no-new-privileges', '--memory', '256m', '--cpus', '0.5', '--pids-limit', '64', '--tmpfs', '/tmp:rw,noexec,nosuid,size=16m', '--env-file', str(ROOT / ('support-acceptance.env' if acceptance else 'support.env')), '--mount', 'type=bind,src=' + str(ROOT / 'data') + ',dst=/data', '--mount', 'type=bind,src=' + str(ROOT / 'secrets/provider.json') + ',dst=/run/secrets/provider.json,readonly', image)
    for network in ['ztapi_data', 'ztapi_egress']:
        docker('network', 'connect', network, SUPPORT)
    docker('start', SUPPORT)
    wait_health(SUPPORT, 3012)


def create_telegram(image, enabled):
    docker('create', '--name', TELEGRAM, '--label', 'ztapi.support.release=20261102', '--network', 'ztapi_app', '--restart', 'unless-stopped', '--read-only', '--cap-drop=ALL', '--security-opt', 'no-new-privileges', '--memory', '256m', '--cpus', '0.5', '--pids-limit', '64', '--tmpfs', '/tmp:rw,noexec,nosuid,size=16m', '--env-file', str(TELEGRAM_ROOT / ('production.env' if enabled else 'candidate.env')), '--mount', 'type=bind,src=' + str(TELEGRAM_ROOT / 'data') + ',dst=/data', '--mount', 'type=bind,src=' + str(TELEGRAM_ROOT / 'secrets/config.json') + ',dst=/run/secrets/telegram-support.json,readonly', image)
    docker('network', 'connect', 'ztapi_egress', TELEGRAM)
    docker('start', TELEGRAM)
    wait_health(TELEGRAM, 3013)


def nginx_candidate(original, commit):
    require(re.fullmatch('[a-f0-9]{40}', commit) is not None, 'invalid_commit')
    public, admin = 'server_name ztapi.vip www.ztapi.vip;', 'server_name admin.ztapi.vip;'
    require(original.count(public) == 1 and original.count(admin) == 1 and original.count('http://ztapi-finance:3011') == 1, 'unknown_nginx_baseline')
    def route(prefix):
        return '''
    location ^~ ''' + prefix + ''' {
        resolver 127.0.0.11 valid=30s ipv6=off;
        set $ztapi_support http://''' + SUPPORT + ''':3012;
        proxy_pass $ztapi_support;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-ZTAPI-Internal-Acceptance "";
        proxy_connect_timeout 2s;
        proxy_read_timeout 65s;
        proxy_next_upstream off;
        proxy_cache off;
        client_max_body_size 5m;
    }
'''
    source = '\n    add_header X-ZTAPI-UI-Source "https://github.com/ffff582/ztapi-source/tree/' + commit + '" always;\n'
    result = original.replace(public, public + source + route('/api/support/'))
    result = result.replace(admin, admin + source + route('/api/support/') + route('/api/admin/support/'))
    return result.replace('http://ztapi-finance:3011', 'http://' + FINANCE_LIVE + ':3011')


def validate_payload():
    manifest = json.loads((STAGE / 'payload.json').read_text())
    for name, expected in manifest['files'].items():
        require('/' not in name and name not in ['.', '..'], 'invalid_payload_path')
        require(not (STAGE / name).is_symlink() and digest((STAGE / name).read_bytes()) == expected, 'payload_integrity_failed:' + name)
    return json.loads((STAGE / 'build.json').read_text()), json.loads((STAGE / 'live-baseline.json').read_text())


def prepare():
    build, baseline = validate_payload()
    check_baseline(baseline)
    require(not ROOT.exists() and not TELEGRAM_ROOT.exists(), 'support_stage_already_exists')
    require(inspect(MAIN)['State']['Health']['Status'] == 'healthy', 'main_api_unhealthy')
    mainenv, oldfinance = envmap(inspect(MAIN)), inspect('ztapi-finance')
    provision(mainenv, json.loads((STAGE / 'quote.json').read_text()), build['commit'])
    for kind in ['support', 'finance', 'telegram-support']:
        directory = STAGE / ('image-' + kind)
        directory.mkdir(mode=0o700)
        shutil.copyfile(STAGE / ('ztapi-' + kind), directory / ('ztapi-' + kind))
        os.chmod(directory / ('ztapi-' + kind), 0o755)
        cert = 'COPY ca-certificates.crt /etc/ssl/certs/ca-certificates.crt\n' if kind != 'finance' else ''
        if cert:
            copy_public_ca('/etc/ssl/certs/ca-certificates.crt', directory / 'ca-certificates.crt')
        (directory / 'Dockerfile').write_text('FROM scratch\n' + cert + 'COPY ztapi-' + kind + ' /ztapi-' + kind + '\nUSER 10001:10001\nLABEL org.opencontainers.image.revision="' + build['commit'] + '" org.opencontainers.image.source="https://github.com/ffff582/ztapi-source" org.opencontainers.image.licenses="AGPL-3.0-or-later"\nENTRYPOINT ["/ztapi-' + kind + '"]\n')
        docker('build', '-q', '-t', 'ztapi-' + kind + ':support-' + build['commit'][:12], str(directory), timeout=180)
    create_support('ztapi-support:support-' + build['commit'][:12], True)
    create_telegram('ztapi-telegram-support:support-' + build['commit'][:12], False)
    key = mainenv['ZTAPI_SESSION_SIGNING_KEY']
    require(fetch(address(SUPPORT, 3012) + '/api/support/config', key)[0] == 200, 'support_auth_failed')
    require(fetch(address(SUPPORT, 3012) + '/api/support/config')[0] == 401, 'support_unauthenticated_access')
    # Clone the finance store for candidate migration/compatibility verification.
    clone = STAGE / 'finance-candidate'
    clone.mkdir(mode=0o700)
    with closing(sqlite3.connect('file:/opt/ztapi-finance/data/finance.db?mode=ro', uri=True)) as source:
        with closing(sqlite3.connect(clone / 'finance.db')) as target:
            source.backup(target)
            require(target.execute('PRAGMA integrity_check').fetchall() == [('ok',)], 'finance_snapshot_invalid')
    os.chown(clone, 10001, 10001)
    os.chown(clone / 'finance.db', 10001, 10001)
    write_env(ROOT / 'finance-candidate.env', finance_environment(envmap(oldfinance), build['commit'], True))
    write_env(ROOT / 'finance.env', finance_environment(envmap(oldfinance), build['commit'], False))
    create_finance(build, clone, candidate=True)
    status, report = fetch(address(FINANCE, 3011) + '/api/admin/finance-reporting/report?month=2026-11', key)
    require(status == 200 and report['data']['support_source_status'] == 'available', 'finance_support_reader_failed')
    check_baseline(baseline)
    json_save('prepared.json', {'prepared': True, 'main_api_unchanged': True, 'support': identity(inspect(SUPPORT)), 'finance': identity(inspect(FINANCE)), 'finance_support_source': 'available', 'paid_calls': 0})


def create_finance(build, data, candidate=False, name=FINANCE):
    docker('create', '--name', name, '--label', 'ztapi.support.release=20261102', '--network', 'ztapi_app', '--restart', 'unless-stopped', '--read-only', '--cap-drop=ALL', '--security-opt', 'no-new-privileges', '--memory', '256m', '--cpus', '0.5', '--pids-limit', '64', '--tmpfs', '/tmp:rw,noexec,nosuid,size=16m', '--env-file', str(ROOT / ('finance-candidate.env' if candidate else 'finance.env')), '--mount', 'type=bind,src=' + str(data) + ',dst=/data', '--mount', 'type=bind,src=' + str(ROOT / 'data') + ',dst=/support,readonly', '--mount', 'type=bind,src=' + str(TELEGRAM_ROOT / 'data') + ',dst=/telegram-support,readonly', 'ztapi-finance:support-' + build['commit'][:12])
    docker('network', 'connect', 'ztapi_data', name)
    docker('start', name)
    wait_health(name, 3011)


def static_candidate(kind):
    target = STAGE / (kind + '-candidate')
    target.mkdir(mode=0o700)
    with tarfile.open(STAGE / (kind + '-dist.tar.gz'), 'r:gz') as archive:
        members = archive.getmembers()
        require(len(members) < 10000 and sum(m.size for m in members) < 128 << 20, 'static_archive_too_large')
        for member in members:
            path = Path(member.name)
            require(not path.is_absolute() and '..' not in path.parts and not member.issym() and not member.islnk() and (member.isfile() or member.isdir()), 'invalid_static_archive')
            destination = target / path
            require(destination.resolve().is_relative_to(target.resolve()), 'static_path_escape')
            if member.isdir():
                destination.mkdir(parents=True, exist_ok=True)
            else:
                destination.parent.mkdir(parents=True, exist_ok=True)
                save(destination, archive.extractfile(member).read())
    return target


def replace_inside(path, data, expected=None):
    if expected is not None:
        require(digest(docker('exec', NGINX, 'cat', path)) == expected, 'replacement_baseline_changed')
    local = STAGE / ('replacement-' + secrets.token_hex(6))
    save(local, data)
    temporary = path + '.support-20261102-new'
    docker('cp', str(local), NGINX + ':' + temporary)
    docker('exec', NGINX, 'chmod', '644', temporary)
    require(docker('exec', NGINX, 'sha256sum', temporary).decode().split()[0] == digest(data), 'replacement_copy_integrity_failed')
    if expected is not None:
        require(digest(docker('exec', NGINX, 'cat', path)) == expected, 'replacement_changed_during_stage')
    docker('exec', NGINX, 'mv', temporary, path)


def rollback_activation(original, entries, finance_target, phase):
    bot_stopped, stop_error = True, None
    try:
        present = docker('ps', '-a', '--filter', 'name=^/' + TELEGRAM + '$', '--format', '{{.ID}}').strip()
        if present:
            bot = inspect(TELEGRAM)
            if envmap(bot).get('TELEGRAM_SUPPORT_ENABLED') == 'true':
                require(bot['Config'].get('Labels', {}).get('ztapi.support.release') == '20261102', 'unowned_telegram_stop_refused')
                docker('update', '--restart=no', TELEGRAM)
                docker('stop', '--time', '75', TELEGRAM)
    except Exception as e:
        bot_stopped, stop_error = False, type(e).__name__
    rollback = original.replace(b'http://ztapi-finance:3011', ('http://' + finance_target + ':3011').encode())
    replace_inside(CONF, rollback)
    docker('exec', NGINX, 'nginx', '-t')
    docker('exec', NGINX, 'nginx', '-s', 'reload')
    roots = {'public': '/usr/share/nginx/public', 'admin': '/usr/share/nginx/admin'}
    for kind, entry in entries.items():
        replace_inside(roots[kind] + '/index.html', entry)
    json_save('rolled-back.json', {'published': False, 'phase': phase, 'website_restored': True, 'finance_reader_retained': finance_target, 'telegram_inference_stopped': bot_stopped, 'stop_error_type': stop_error, 'private_cost_receipts_preserved': True})
    require(bot_stopped, 'telegram_rollback_requires_operator_check')


def verify_group_ready():
    for _ in range(45):
        try:
            with closing(sqlite3.connect('file:' + str(TELEGRAM_ROOT / 'data/support.db') + '?mode=ro', uri=True)) as db:
                enabled = db.execute('SELECT enabled FROM telegram_support_groups WHERE chat_id=?', (-1004401851162,)).fetchone()
                sent = db.execute("SELECT COUNT(*) FROM telegram_support_outbox WHERE chat_id=? AND kind='control' AND state='sent' AND sent_message_id>0", (-1004401851162,)).fetchone()[0]
                if enabled == (1,) and sent > 0:
                    return {'group_id': -1004401851162, 'enabled': True, 'confirmation_delivered': True}
        except sqlite3.Error:
            pass
        time.sleep(2)
    raise RuntimeError('telegram_group_enable_or_confirmation_not_observed')


def install_backups():
    shutil.copyfile(STAGE / 'backup.py', ROOT / 'backup.py')
    os.chmod(ROOT / 'backup.py', 0o700)
    cron = Path('/etc/cron.d/ztapi-support-backup')
    require(not cron.exists(), 'support_cron_already_exists')
    run(['python3', '-B', str(ROOT / 'backup.py')])
    run(['python3', '-B', str(ROOT / 'backup.py'), '--telegram'])
    save(cron, b'SHELL=/bin/sh\nPATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin\n17 19 * * * root /usr/bin/python3 -B /opt/ztapi-support/backup.py >> /var/log/ztapi-support-backup.log 2>&1\n27 19 * * * root /usr/bin/python3 -B /opt/ztapi-support/backup.py --telegram >> /var/log/ztapi-support-backup.log 2>&1\n', 0o644)


def stage_static_entries():
    entries = {}
    for kind, root in [('public', '/usr/share/nginx/public'), ('admin', '/usr/share/nginx/admin')]:
        entries[kind] = docker('exec', NGINX, 'cat', root + '/index.html')
        save(STAGE / (kind + '-before-index.html'), entries[kind])
        raw = docker('cp', NGINX + ':' + root + '/.', '-')
        packed = gzip.compress(raw)
        save(STAGE / (kind + '-before.tar.gz'), packed)
        save(STAGE / (kind + '-before.tar.gz.sha256'), (digest(packed) + '\n').encode())
        candidate = static_candidate(kind)
        for path in candidate.rglob('*'):
            if not path.is_file() or path.name == 'index.html':
                continue
            relative = path.relative_to(candidate).as_posix()
            docker('exec', NGINX, 'mkdir', '-p', str(Path(root + '/' + relative).parent))
            probe = subprocess.run(['docker', 'exec', NGINX, 'cat', root + '/' + relative], capture_output=True)
            if probe.returncode == 0:
                require(digest(probe.stdout) == digest(path.read_bytes()), 'static_asset_collision:' + relative)
            else:
                docker('cp', str(path), NGINX + ':' + root + '/' + relative)
                docker('exec', NGINX, 'chmod', '644', root + '/' + relative)
            require(docker('exec', NGINX, 'sha256sum', root + '/' + relative).decode().split()[0] == digest(path.read_bytes()), 'static_copy_integrity_failed:' + relative)
    return entries


def activate():
    build, baseline = validate_payload()
    check_baseline(baseline)
    require(json.loads((STAGE / 'acceptance.json').read_text())['passed'] is True, 'paid_acceptance_not_passed')
    require(json.loads((STAGE / 'browser-qa.json').read_text())['passed'] is True, 'browser_acceptance_not_passed')
    original = docker('exec', NGINX, 'cat', CONF)
    save(STAGE / 'nginx-before.conf', original)
    entries, finance_target, phase = {}, FINANCE, 'backups'
    try:
        install_backups()
        phase = 'static_assets'
        entries = stage_static_entries()
        phase = 'support_runtime'
        docker('stop', '--time', '65', SUPPORT)
        docker('rename', SUPPORT, SUPPORT + '-acceptance')
        create_support('ztapi-support:support-' + build['commit'][:12], False)
        phase = 'finance_runtime'
        # Keep the already-verified dual-store reader alive until its live-store
        # replacement is healthy; it remains the rollback cost-reporting target.
        create_finance(build, Path('/opt/ztapi-finance/data'), name=FINANCE_LIVE)
        key = envmap(inspect(MAIN))['ZTAPI_SESSION_SIGNING_KEY']
        require(fetch(address(FINANCE_LIVE, 3011) + '/api/admin/finance-reporting/report?month=2026-11', key)[0] == 200, 'live_finance_precheck_failed')
        finance_target = FINANCE_LIVE
        phase = 'routing'
        newconf = nginx_candidate(original.decode(), build['commit']).encode()
        replace_inside(CONF, newconf, digest(original))
        docker('exec', NGINX, 'nginx', '-t')
        docker('exec', NGINX, 'nginx', '-s', 'reload')
        for kind, root in [('public', '/usr/share/nginx/public'), ('admin', '/usr/share/nginx/admin')]:
            replace_inside(root + '/index.html', (STAGE / (kind + '-candidate/index.html')).read_bytes(), digest(entries[kind]))
        require(fetch('https://ztapi.vip/api/support/config', key)[0] == 200, 'public_support_route_failed')
        require(fetch('https://admin.ztapi.vip/api/admin/support/tickets', key)[0] == 200, 'admin_support_route_failed')
        require(fetch('https://ztapi.vip/api/admin/support/tickets', key)[0] == 404, 'public_admin_boundary_failed')
        require(fetch('https://ztapi.vip/api/status')[0] == 200, 'main_api_postcheck_failed')
        require(fetch('https://admin.ztapi.vip/api/admin/finance-reporting/report?month=2026-11', key)[1]['data']['support_source_status'] == 'available', 'finance_route_postcheck_failed')
        phase = 'telegram_runtime'
        docker('stop', '--time', '75', TELEGRAM)
        docker('rename', TELEGRAM, TELEGRAM + '-candidate')
        create_telegram('ztapi-telegram-support:support-' + build['commit'][:12], True)
        group = verify_group_ready()
        phase = 'final_checks'
        check_baseline(baseline, False)
        json_save('published.json', {'published': True, 'commit': build['commit'], 'main_api_unchanged': True, 'nginx_not_restarted': True, 'old_finance_preserved': True, 'old_assets_retained': True, 'nginx_sha256': digest(newconf), 'support': identity(inspect(SUPPORT)), 'finance': identity(inspect(FINANCE_LIVE)), 'telegram': identity(inspect(TELEGRAM)), 'group': group, 'backup_verified': True})
    except Exception:
        rollback_activation(original, entries, finance_target, phase)
        raise


if __name__ == '__main__':
    os.umask(0o077)
    try:
        require(os.geteuid() == 0, 'root_release_required')
        require(len(sys.argv) == 2 and sys.argv[1] in ['--prepare', '--activate'], 'invalid_release_action')
        prepare() if sys.argv[1] == '--prepare' else activate()
        print(json.dumps({'ok': True, 'action': sys.argv[1]}))
    except Exception as e:
        print(json.dumps({'ok': False, 'error_type': type(e).__name__, 'reason': str(e) if isinstance(e, RuntimeError) else 'operation_failed'}))
        raise SystemExit(1)
