#!/usr/bin/env python3
"""Consistent private-finance backup; never touches the customer database."""
import datetime
from contextlib import closing
import gzip
import hashlib
import json
import os
from pathlib import Path
import shutil
import sqlite3
import subprocess
import urllib.request
import uuid


def digest(stream):
    value = hashlib.sha256()
    for block in iter(lambda: stream.read(1024 * 1024), b''):
        value.update(block)
    return value.hexdigest()


def main(root=None):
    os.umask(0o077)
    root = Path(root or '/opt/ztapi-finance')
    backups = root / 'backups'
    backups.mkdir(parents=True, exist_ok=True)
    stamp = datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%SZ')
    snapshot = backups / ('finance-' + stamp + '-' + uuid.uuid4().hex[:8] + '.sqlite')
    with closing(sqlite3.connect('file:' + str(root / 'data/finance.db') + '?mode=ro', uri=True)) as source:
        with closing(sqlite3.connect(snapshot)) as target:
            source.backup(target)
            if target.execute('PRAGMA integrity_check').fetchall() != [('ok',)]:
                raise RuntimeError('SQLite integrity check failed')
    archive = snapshot.with_suffix('.sqlite.gz')
    with snapshot.open('rb') as source, gzip.open(archive, 'wb') as target:
        shutil.copyfileobj(source, target)
    with snapshot.open('rb') as source, gzip.open(archive, 'rb') as checked:
        if digest(source) != digest(checked):
            raise RuntimeError('Compressed backup verification failed')
    if os.name == 'posix':
        subprocess.run(['gunzip', '-t', str(archive)], check=True)
    with archive.open('rb') as source:
        checksum = digest(source)
    archive.with_suffix('.gz.sha256').write_text(checksum + '  ' + archive.name + '\n')
    snapshot.unlink()
    print(json.dumps({'backup': str(archive), 'sha256': checksum, 'verified': True}))


def alert():
    try:
        container = json.loads(subprocess.check_output(['docker', 'inspect', 'ztapi-server-1']))[0]
        env = dict(item.split('=', 1) for item in container['Config']['Env'])
        token = env.get('ZTAPI_HEALTH_TELEGRAM_BOT_TOKEN', '')
        chat = env.get('ZTAPI_HEALTH_TELEGRAM_CHAT_ID', '')
        if not token or not chat:
            return
        body = json.dumps({'chat_id': chat, 'text': '[ZTAPI] Independent finance backup failed. Check the finance backup log; customer balances were not modified.'}).encode()
        req = urllib.request.Request('https://api.telegram.org/bot' + token + '/sendMessage', data=body, headers={'Content-Type': 'application/json'})
        with urllib.request.urlopen(req, timeout=10):
            pass
    except Exception:
        print('Finance backup Telegram alert failed; inspect local backup log.')


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        print('Finance backup failed: ' + type(error).__name__)
        alert()
        raise SystemExit(1)
