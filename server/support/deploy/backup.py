#!/usr/bin/env python3
"""Private support backup; chat contents never enter alerts or stdout."""
from contextlib import closing, contextmanager
from datetime import datetime, timezone
import gzip
import hashlib
import json
import os
from pathlib import Path
import shutil
import sqlite3
import stat
import subprocess
import sys
import time
import urllib.request
import uuid

BACKUP_LOCK_PATH = Path('/var/lock/ztapi-support-backup.lock')


@contextmanager
def backup_lock(path):
    fd = os.open(path, os.O_CREAT | os.O_RDWR | getattr(os, 'O_NOFOLLOW', 0), 0o600)
    locked = False
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
            raise RuntimeError('invalid shared support backup lock')
        if info.st_size == 0:
            os.write(fd, b'0')
        os.lseek(fd, 0, os.SEEK_SET)
        try:
            if os.name == 'posix':
                import fcntl
                fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            else:
                import msvcrt
                msvcrt.locking(fd, msvcrt.LK_NBLCK, 1)
        except OSError:
            raise RuntimeError('another support store backup owns disk reservation') from None
        locked = True
        yield
    finally:
        if locked:
            if os.name == 'posix':
                fcntl.flock(fd, fcntl.LOCK_UN)
            else:
                os.lseek(fd, 0, os.SEEK_SET)
                msvcrt.locking(fd, msvcrt.LK_UNLCK, 1)
        os.close(fd)


def check_deadline(deadline):
    if deadline is not None and time.monotonic() >= deadline:
        raise RuntimeError('support backup deadline exceeded')


def digest(stream, deadline=None):
    value = hashlib.sha256()
    for block in iter(lambda: stream.read(1024 * 1024), b''):
        check_deadline(deadline)
        value.update(block)
    return value.hexdigest()


def backup_headroom(database_bytes, free_bytes, existing_bytes):
    staging_bytes = 2 * database_bytes + (16 << 20)
    if free_bytes < (1 << 30) + staging_bytes or existing_bytes + staging_bytes > 2 << 30:
        raise RuntimeError('support backup storage headroom exhausted')


def main(root=Path('/opt/ztapi-support')):
    with backup_lock(BACKUP_LOCK_PATH):
        return snapshot(root)


def snapshot(root):
    os.umask(0o077)
    root = Path(root)
    backups = root / 'backups'
    backups.mkdir(mode=0o700, parents=True, exist_ok=True)
    name = 'support-' + datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%SZ') + '-' + uuid.uuid4().hex[:8]
    snapshot = backups / (name + '.sqlite')
    archive = snapshot.with_suffix('.sqlite.gz')
    checksum_file = archive.with_suffix('.gz.sha256')
    with closing(sqlite3.connect('file:' + str(root / 'data/support.db') + '?mode=ro', uri=True)) as source:
        size = source.execute('PRAGMA page_count').fetchone()[0] * source.execute('PRAGMA page_size').fetchone()[0]
        entries = list(backups.iterdir())
        if any(not p.is_file() or p.is_symlink() for p in entries):
            raise RuntimeError('unexpected support backup entry')
        backup_headroom(size, shutil.disk_usage(backups).free, sum(p.stat().st_size for p in entries))
        if any(path.exists() for path in (snapshot, archive, checksum_file)):
            raise RuntimeError('support backup identity collision')
        try:
            fd = os.open(snapshot, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
            os.close(fd)
            with closing(sqlite3.connect(snapshot)) as target:
                deadline = time.monotonic() + 120
                def progress(status, remaining, total):
                    check_deadline(deadline)
                source.backup(target, pages=256, progress=progress, sleep=0.05)
                if target.execute('PRAGMA integrity_check').fetchall() != [('ok',)]:
                    raise RuntimeError('support backup integrity failed')
            with snapshot.open('rb') as original, gzip.open(archive, 'wb') as target:
                for chunk in iter(lambda: original.read(1024 * 1024), b''):
                    check_deadline(deadline)
                    target.write(chunk)
            with snapshot.open('rb') as original, gzip.open(archive, 'rb') as target:
                if digest(original, deadline) != digest(target, deadline):
                    raise RuntimeError('support backup content verification failed')
            if os.name == 'posix':
                check_deadline(deadline)
                subprocess.run(['gunzip', '-t', str(archive)], check=True, capture_output=True, timeout=max(1, deadline-time.monotonic()))
            with archive.open('rb') as stream:
                checksum = digest(stream, deadline)
            checksum_file.write_text(checksum + '  ' + archive.name + '\n')
            for path in (archive, checksum_file):
                with path.open('r+b') as stream:
                    os.fsync(stream.fileno())
            snapshot.unlink()
        except Exception:
            for path in (snapshot, archive, checksum_file):
                path.unlink(missing_ok=True)
            raise
    if os.name == 'posix':
        fd = os.open(backups, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)
    return {'verified': True, 'sha256': checksum, 'backup': str(archive)}


def alert():
    try:
        container = json.loads(subprocess.check_output(['docker', 'inspect', 'ztapi-server-1']))[0]
        env = dict(item.split('=', 1) for item in container['Config']['Env'])
        key, chat = env.get('ZTAPI_HEALTH_TELEGRAM_BOT_TOKEN'), env.get('ZTAPI_HEALTH_TELEGRAM_CHAT_ID')
        if not key or not chat:
            raise RuntimeError('backup alert configuration missing')
        body = json.dumps({'chat_id': chat, 'text': '[ZTAPI] Private support backup failed. Check /var/log/ztapi-support-backup.log. No customer balance was changed.'}).encode()
        req = urllib.request.Request('https://api.telegram.org/bot' + key + '/sendMessage', data=body, headers={'Content-Type': 'application/json'})
        with urllib.request.urlopen(req, timeout=10):
            pass
    except Exception:
        print('Support backup alert failed; inspect the local backup log.')


if __name__ == '__main__':
    try:
        if sys.argv[1:] == ['--telegram']:
            selected = Path('/opt/ztapi-telegram-support')
        elif not sys.argv[1:]:
            selected = Path('/opt/ztapi-support')
        else:
            raise RuntimeError('invalid support backup action')
        print(json.dumps(main(selected)))
    except Exception as error:
        print('Support backup failed: ' + type(error).__name__)
        alert()
        raise SystemExit(1)
