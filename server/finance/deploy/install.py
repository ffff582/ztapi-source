#!/usr/bin/env python3
"""Install the approved sidecar without restarting the customer API."""
import base64
import hashlib
import hmac
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import subprocess
import sys
import time
import urllib.request

ROOT = Path('/opt/ztapi-finance')
HOST_NGINX_CONFIG = Path('/opt/ztapi/deploy/nginx/ztapi.conf')
EXPECTED_MAIN = '4cf62d8290444ea140284ca5d81619a05e1a54b5e5cd5ccfa6ce42227a0d865d'
EXPECTED_IMAGE = 'sha256:d0c124fd99cbb2af804c1bd3e65cfba0fb2fce7ae51ba63361961d6304d258ee'
ORIGIN = 'https://admin.ztapi.vip'
PROXY = '''
    location ^~ /api/admin/finance-reporting/ {
        resolver 127.0.0.11 valid=30s ipv6=off;
        set $ztapi_finance http://ztapi-finance:3011;
        proxy_pass $ztapi_finance;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto https;
        proxy_read_timeout 12s;
        client_max_body_size 8k;
    }
'''


def run(args, **kwargs):
    try:
        return subprocess.run(args, check=True, capture_output=True, **kwargs)
    except subprocess.CalledProcessError:
        print('Checked command failed: ' + ' '.join(args[:3]), file=sys.stderr)
        raise


def inspect(name):
    return json.loads(run(['docker', 'inspect', name]).stdout)[0]


def environment(container):
    return dict(item.split('=', 1) for item in container['Config']['Env'])


def sql(statement):
    return run(['docker', 'exec', '-i', 'ztapi-mysql-1', 'sh', '-c', 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysql -uroot --batch --skip-column-names'], input=statement.encode()).stdout.decode()


def token(key, user):
    encode = lambda value: base64.urlsafe_b64encode(json.dumps(value, separators=(',', ':')).encode()).decode().rstrip('=')
    now = int(time.time())
    body = encode({'alg': 'HS256', 'typ': 'JWT'}) + '.' + encode({'iss': 'ztapi', 'sub': str(user), 'iat': now, 'exp': now + 120, 'jti': secrets.token_hex(16)})
    signature = base64.urlsafe_b64encode(hmac.new(key.encode(), body.encode(), hashlib.sha256).digest()).decode().rstrip('=')
    return body + '.' + signature


def fetch(path, key, user=1, host='admin.ztapi.vip'):
    auth = token(key, user)
    return run(['curl', '--silent', '--show-error', '--noproxy', '*', '--max-time', '15', '--resolve', host + ':443:127.0.0.1', '-H', 'Authorization: Bearer ' + auth, '-H', 'New-Api-User: ' + str(user), '-w', '\n%{http_code}', 'https://' + host + path]).stdout.decode().rsplit('\n', 1)


def prepare_environment(root, settings, database, commit, resume):
    if not re.fullmatch('[A-Za-z0-9_-]+', database):
        raise RuntimeError('Unsupported source database identifier')
    reader = 'ztapi_finance_ro_20261026'
    grants = {
        'users': 'id,username,role,status,deleted_at',
        'balance_ledgers': 'id,user_id,delta,balance_before,balance_after,source_type,request_id,idempotency_key,created_at',
        'ztapi_request_settlements': 'id,user_id,request_id,operation_id,status,reserved_quota,charged_quota,final_attempt,created_at',
        'ztapi_settlement_log_outboxes': 'operation_id,created_at',
        'ztapi_request_attempts': 'settlement_id,attempt,channel_id,created_at',
        'top_ups': 'id,user_id,trade_no,status,complete_time',
        'usdt_top_up_orders': 'top_up_id,status,credit_units,pay_amount_micros',
        'channels': 'id,name',
        'ztapi_supplier_refund_charges': 'id,request_id,user_id,original_ledger_id,charged_quota,billing_proof_id,channel_id,billing_operation_id,created_at',
        'ztapi_supplier_refunds': 'ledger_id,charge_id,refunded_quota,status',
    }
    env_file = root / 'finance.env'
    if env_file.exists():
        if not resume:
            raise RuntimeError('Prepared finance stage exists; explicit resume required')
        if os.name == 'posix' and (env_file.stat().st_uid != 0 or env_file.stat().st_mode & 0o077):
            raise RuntimeError('Prepared finance environment permissions are unsafe')
        previous = dict(line.split('=', 1) for line in env_file.read_text().splitlines())
        dsn = previous.get('FINANCE_READ_DSN', '')
        if previous.get('FINANCE_READ_DRIVER') != 'mysql' or previous.get('FINANCE_ADMIN_ORIGIN') != ORIGIN or previous.get('ZTAPI_SESSION_SIGNING_KEY') != settings['ZTAPI_SESSION_SIGNING_KEY'] or not re.fullmatch('[a-f0-9]{40}', previous.get('FINANCE_SOURCE_COMMIT', '')) or not re.fullmatch(reader + r':[a-f0-9]{48}@tcp\(mysql:3306\)/' + re.escape(database) + r'\?.+', dsn):
            raise RuntimeError('Prepared finance stage identity does not match')
    else:
        if resume:
            raise RuntimeError('No owned stage available to resume')
        password = secrets.token_hex(24)
        create = "CREATE USER '" + reader + "'@'%' IDENTIFIED BY '" + password + "';\n"
        create += ''.join('GRANT SELECT (' + fields + ') ON `' + database + '`.`' + table + "` TO '" + reader + "'@'%';\n" for table, fields in grants.items())
        sql(create)
        dsn = reader + ':' + password + '@tcp(mysql:3306)/' + database + '?charset=utf8mb4&parseTime=true&loc=UTC&timeout=5s&readTimeout=5s&writeTimeout=5s'
    saved_grants = sql("SHOW GRANTS FOR '" + reader + "'@'%';")
    if re.search(r'\b(INSERT|UPDATE|DELETE|CREATE|ALTER|DROP|ALL PRIVILEGES)\b', saved_grants, re.I):
        raise RuntimeError('Unexpected finance reader privilege')
    (root / 'reader-grants.txt').write_text(saved_grants)
    env = {
        'FINANCE_READ_DRIVER': 'mysql',
        'FINANCE_READ_DSN': dsn,
        'FINANCE_STORE': '/data/finance.db',
        'FINANCE_ADMIN_ORIGIN': ORIGIN,
        'FINANCE_LISTEN': ':3011',
        'FINANCE_SOURCE_COMMIT': commit,
        'ZTAPI_SESSION_SIGNING_KEY': settings['ZTAPI_SESSION_SIGNING_KEY'],
        'GOMEMLIMIT': '160MiB',
        'GOMAXPROCS': '2',
    }
    if any('\n' in value or '\r' in value for value in env.values()):
        raise RuntimeError('Unsupported environment value')
    env_file.write_text(''.join(key + '=' + value + '\n' for key, value in env.items()))
    os.chmod(env_file, 0o600)
    return env


def main():
    os.umask(0o077)
    commit, expected_hash = sys.argv[1:3]
    if not re.fullmatch('[a-f0-9]{40}', commit) or not re.fullmatch('[a-f0-9]{64}', expected_hash) or len(sys.argv) > 4 or len(sys.argv) == 4 and sys.argv[3] != '--resume-prepared':
        raise RuntimeError('Invalid release identity')
    release = Path(__file__).resolve().parent
    binary = release / 'ztapi-finance'
    if hashlib.sha256(binary.read_bytes()).hexdigest() != expected_hash:
        raise RuntimeError('Finance binary checksum mismatch')
    before = inspect('ztapi-server-1')
    if before['Id'] != EXPECTED_MAIN or before['Image'] != EXPECTED_IMAGE:
        raise RuntimeError('Main API baseline changed; refusing deployment')
    if run(['docker', 'ps', '-a', '--filter', 'name=^/ztapi-finance$', '--format', '{{.ID}}']).stdout.strip():
        raise RuntimeError('Finance container already exists; explicit upgrade needed')
    settings = environment(before)
    database = environment(inspect('ztapi-mysql-1'))['MYSQL_DATABASE']
    prepare_environment(ROOT, settings, database, commit, len(sys.argv) == 4)
    data = ROOT / 'data'
    data.mkdir(parents=True, exist_ok=True)
    os.chown(data, 10001, 10001)
    os.chmod(data, 0o700)
    os.chmod(binary, 0o555)
    (release / '.dockerignore').write_text('*\n!Dockerfile\n!ztapi-finance\n')
    (release / 'Dockerfile').write_text('FROM scratch\nCOPY ztapi-finance /ztapi-finance\nUSER 10001:10001\nLABEL org.opencontainers.image.revision="' + commit + '" org.opencontainers.image.source="https://github.com/ffff582/ztapi-source" org.opencontainers.image.licenses="AGPL-3.0-or-later"\nENTRYPOINT ["/ztapi-finance"]\n')
    image = 'ztapi-finance:' + commit[:12]
    run(['docker', 'build', '--network=none', '-t', image, str(release)])
    run(['docker', 'create', '--name', 'ztapi-finance', '--network', 'ztapi_app', '--restart', 'unless-stopped', '--read-only', '--cap-drop=ALL', '--security-opt', 'no-new-privileges', '--memory', '256m', '--cpus', '0.5', '--pids-limit', '64', '--tmpfs', '/tmp:rw,noexec,nosuid,size=16m', '--env-file', str(ROOT / 'finance.env'), '--mount', 'type=bind,src=' + str(data) + ',dst=/data', image])
    run(['docker', 'network', 'connect', 'ztapi_data', 'ztapi-finance'])
    run(['docker', 'start', 'ztapi-finance'])
    health_url = 'http://' + inspect('ztapi-finance')['NetworkSettings']['Networks']['ztapi_app']['IPAddress'] + ':3011/healthz'
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    for _ in range(30):
        try:
            with opener.open(health_url, timeout=2) as response:
                if json.load(response).get('ok'):
                    break
        except Exception:
            pass
        time.sleep(1)
    else:
        raise RuntimeError('Finance health check failed')
    old = ROOT / ('backups/prepublish-' + commit[:12])
    old.mkdir(parents=True, exist_ok=False)
    runtime_config = run(['docker', 'exec', 'ztapi-nginx-1', 'cat', '/etc/nginx/conf.d/default.conf']).stdout.decode()
    (old / 'default.conf').write_text(runtime_config)
    host_config = HOST_NGINX_CONFIG
    shutil.copy2(host_config, old / 'host-ztapi.conf')
    run(['docker', 'cp', 'ztapi-nginx-1:/usr/share/nginx/admin/index.html', str(old / 'index.html')])
    (old / 'baseline.json').write_text(json.dumps({'api_id': before['Id'], 'api_started_at': before['State']['StartedAt'], 'api_image': before['Image'], 'finance_source': commit}))

    def add_proxy(config):
        needle = '    server_name admin.ztapi.vip;'
        if config.count(needle) != 1 or '/api/admin/finance-reporting/' in config:
            raise RuntimeError('Unexpected Nginx finance baseline')
        return config.replace(needle, needle + '\n' + PROXY, 1)

    candidate = old / 'default.next.conf'
    candidate.write_text(add_proxy(runtime_config))
    try:
        run(['docker', 'cp', str(candidate), 'ztapi-nginx-1:/etc/nginx/conf.d/default.conf'])
        run(['docker', 'exec', 'ztapi-nginx-1', 'nginx', '-t'])
        host_config.write_text(add_proxy(host_config.read_text()))
        run(['docker', 'exec', 'ztapi-nginx-1', 'nginx', '-s', 'reload'])
        body, status = fetch('/api/admin/finance-reporting/report?month=2026-09', settings['ZTAPI_SESSION_SIGNING_KEY'])
        parsed = json.loads(body)
        if status != '200' or not parsed.get('success') or parsed['data'].get('source_commit') != commit:
            raise RuntimeError('Finance authenticated live smoke failed')
        report = parsed['data']
        if report['confirmed'] or report['gross_profit'] is not None:
            raise RuntimeError('Unseeded finance unexpectedly claims confirmed profit')
        user_id = int(sql('SELECT id FROM `' + database + '`.users WHERE role=1 AND status=1 AND deleted_at IS NULL ORDER BY id LIMIT 1;').strip())
        _, denied = fetch('/api/admin/finance-reporting/report?month=2026-09', settings['ZTAPI_SESSION_SIGNING_KEY'], user=user_id)
        if denied != '403':
            raise RuntimeError('Ordinary user finance authorization failed')
        for item in (release / 'dist').iterdir():
            if item.name != 'index.html':
                run(['docker', 'cp', str(item), 'ztapi-nginx-1:/usr/share/nginx/admin/'])
        run(['docker', 'cp', str(release / 'dist/index.html'), 'ztapi-nginx-1:/usr/share/nginx/admin/index.finance-next.html'])
        run(['docker', 'exec', 'ztapi-nginx-1', 'mv', '/usr/share/nginx/admin/index.finance-next.html', '/usr/share/nginx/admin/index.html'])
        after = inspect('ztapi-server-1')
        if after['Id'] != before['Id'] or after['State']['StartedAt'] != before['State']['StartedAt'] or after['Image'] != before['Image']:
            raise RuntimeError('Main API identity changed unexpectedly')
        public = run(['curl', '--fail', '--silent', '--show-error', '--max-time', '15', '--resolve', 'ztapi.vip:443:127.0.0.1', 'https://ztapi.vip/api/status']).stdout
        if not json.loads(public).get('success'):
            raise RuntimeError('Public API health failed')
        link = ROOT / 'current'
        if link.is_symlink() or link.exists():
            raise RuntimeError('Finance release link already exists')
        link.symlink_to(release, target_is_directory=True)
        local_minutes = (19 * 60 + 47 + time.localtime().tm_gmtoff // 60) % (24 * 60)
        hour, minute = divmod(local_minutes, 60)
        Path('/etc/cron.d/ztapi-finance-backup').write_text('SHELL=/bin/sh\nPATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin\n' + str(minute) + ' ' + str(hour) + ' * * * root /usr/bin/python3 /opt/ztapi-finance/current/backup.py >> /var/log/ztapi-finance-backup.log 2>&1\n')
        os.chmod('/etc/cron.d/ztapi-finance-backup', 0o644)
        backup = run(['python3', str(release / 'backup.py')]).stdout.decode().strip()
        receipt = {'deployed': True, 'finance_commit': commit, 'main_api_unchanged': True, 'main_started_at': after['State']['StartedAt'], 'ordinary_user_http': denied, 'finance_http': status, 'historical_profit_pending': True, 'manual_rows': len(report['manual_funding']), 'actual_cost_is_unknown': report['actual_cost'] is None, 'paid_model_calls': 0, 'backup': json.loads(backup)}
        (release / 'deployment-receipt.json').write_text(json.dumps(receipt, indent=2))
        print(json.dumps(receipt))
    except Exception:
        run(['docker', 'cp', str(old / 'default.conf'), 'ztapi-nginx-1:/etc/nginx/conf.d/default.conf'])
        shutil.copy2(old / 'host-ztapi.conf', host_config)
        run(['docker', 'cp', str(old / 'index.html'), 'ztapi-nginx-1:/usr/share/nginx/admin/index.html'])
        run(['docker', 'exec', 'ztapi-nginx-1', 'nginx', '-t'])
        run(['docker', 'exec', 'ztapi-nginx-1', 'nginx', '-s', 'reload'])
        raise


if __name__ == '__main__':
    try:
        main()
    except subprocess.CalledProcessError:
        print('Finance deployment failed at a checked subprocess; no credentials are printed.', file=sys.stderr)
        raise SystemExit(1)
    except Exception as error:
        print('Finance deployment stopped: ' + str(error), file=sys.stderr)
        raise SystemExit(1)
