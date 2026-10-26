#!/usr/bin/env python3
"""Candidate-gated finance-only update; retains the previous container and data."""
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time
import urllib.request


def main():
    os.umask(0o077)
    commit, checksum = sys.argv[1:3]
    if not re.fullmatch('[a-f0-9]{40}', commit) or not re.fullmatch('[a-f0-9]{64}', checksum):
        raise RuntimeError('Invalid finance upgrade identity')
    release = Path(__file__).resolve().parent
    spec = importlib.util.spec_from_file_location('finance_installer', release / 'install.py')
    m = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(m)
    binary = release / 'ztapi-finance'
    if hashlib.sha256(binary.read_bytes()).hexdigest() != checksum:
        raise RuntimeError('Finance binary integrity check failed')
    before = m.inspect('ztapi-server-1')
    old = m.inspect('ztapi-finance')
    if before['Id'] != m.EXPECTED_MAIN or before['Image'] != m.EXPECTED_IMAGE or before['State']['Health']['Status'] != 'healthy':
        raise RuntimeError('Main API baseline changed')
    previous = m.environment(old).get('FINANCE_SOURCE_COMMIT')
    if previous != 'a1960cf2818078b55a6c0ae4e416aaf583d7e8bc' or old['State']['Running']:
        raise RuntimeError('Upgrade requires the explicitly paused previous finance release')
    settings = m.environment(before)
    database = m.environment(m.inspect('ztapi-mysql-1'))['MYSQL_DATABASE']
    (release / 'finance.env.before').write_bytes((m.ROOT / 'finance.env').read_bytes())
    m.run(['python3', str(release / 'backup.py')])
    m.prepare_environment(m.ROOT, settings, database, commit, True)
    os.chmod(binary, 0o555)
    (release / '.dockerignore').write_text('*\n!Dockerfile\n!ztapi-finance\n')
    (release / 'Dockerfile').write_text('FROM scratch\nCOPY ztapi-finance /ztapi-finance\nUSER 10001:10001\nLABEL org.opencontainers.image.revision="' + commit + '" org.opencontainers.image.source="https://github.com/ffff582/ztapi-source" org.opencontainers.image.licenses="AGPL-3.0-or-later"\nENTRYPOINT ["/ztapi-finance"]\n')
    image = 'ztapi-finance:' + commit[:12]
    m.run(['docker', 'build', '--network=none', '-t', image, str(release)])
    candidate = 'ztapi-finance-candidate'
    m.run(['docker', 'create', '--name', candidate, '--network', 'ztapi_app', '--restart', 'unless-stopped', '--read-only', '--cap-drop=ALL', '--security-opt', 'no-new-privileges', '--memory', '256m', '--cpus', '0.5', '--pids-limit', '64', '--tmpfs', '/tmp:rw,noexec,nosuid,size=16m', '--env-file', str(m.ROOT / 'finance.env'), '--mount', 'type=bind,src=' + str(m.ROOT / 'data') + ',dst=/data', image])
    m.run(['docker', 'network', 'connect', 'ztapi_data', candidate])
    m.run(['docker', 'start', candidate])
    address = 'http://' + m.inspect(candidate)['NetworkSettings']['Networks']['ztapi_app']['IPAddress'] + ':3011'
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    for _ in range(30):
        try:
            with opener.open(address + '/healthz', timeout=2) as response:
                if json.load(response).get('ok'):
                    break
        except Exception:
            pass
        time.sleep(1)
    else:
        raise RuntimeError('Finance candidate did not become healthy')
    measurements = []
    for month in ['2026-09', '2026-10']:
        request = urllib.request.Request(address + '/api/admin/finance-reporting/report?month=' + month, headers={'Authorization': 'Bearer ' + m.token(settings['ZTAPI_SESSION_SIGNING_KEY'], 1), 'New-Api-User': '1'})
        started = time.monotonic()
        with opener.open(request, timeout=8) as response:
            report = json.load(response)['data']
        elapsed = time.monotonic() - started
        if report['source_commit'] != commit or elapsed >= 5 or report['gross_profit'] is not None:
            raise RuntimeError('Full-month candidate acceptance failed')
        measurements.append({'month': month, 'elapsed_ms': round(elapsed * 1000, 2), 'issue_count': report['issue_count'], 'gross_profit': report['gross_profit']})
    m.run(['docker', 'rename', 'ztapi-finance', 'ztapi-finance-previous-' + previous[:12]])
    m.run(['docker', 'rename', candidate, 'ztapi-finance'])
    m.run(['docker', 'exec', 'ztapi-nginx-1', 'nginx', '-t'])
    m.run(['docker', 'exec', 'ztapi-nginx-1', 'nginx', '-s', 'reload'])
    for _ in range(5):
        body, status = m.fetch('/api/admin/finance-reporting/report?month=2026-10', settings['ZTAPI_SESSION_SIGNING_KEY'])
        if status == '200' and json.loads(body).get('data', {}).get('source_commit') == commit:
            break
        time.sleep(1)
    else:
        raise RuntimeError('Promoted finance route did not pass acceptance')
    after = m.inspect('ztapi-server-1')
    if before['Id'] != after['Id'] or before['State']['StartedAt'] != after['State']['StartedAt']:
        raise RuntimeError('Main API identity changed unexpectedly')
    current = m.ROOT / 'current'
    if not current.is_symlink() or current.resolve().name != previous:
        raise RuntimeError('Unexpected finance current-release link')
    next_link = m.ROOT / 'current.next'
    next_link.symlink_to(release, target_is_directory=True)
    os.replace(next_link, current)
    receipt = {'deployed': True, 'finance_commit': commit, 'main_api_unchanged': True, 'main_started_at': after['State']['StartedAt'], 'candidate_months': measurements, 'finance_http': status, 'paid_model_calls': 0}
    (release / 'deployment-receipt.json').write_text(json.dumps(receipt, indent=2))
    print(json.dumps(receipt))


if __name__ == '__main__':
    try:
        main()
    except subprocess.CalledProcessError:
        print('Finance-only upgrade stopped at a checked command.', file=sys.stderr)
        raise SystemExit(1)
    except Exception as error:
        print('Finance-only upgrade stopped: ' + str(error), file=sys.stderr)
        raise SystemExit(1)
