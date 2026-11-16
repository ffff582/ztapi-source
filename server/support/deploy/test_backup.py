import gzip
from contextlib import closing
import hashlib
import importlib.util
from pathlib import Path
import sqlite3
import shutil
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('support_backup', Path(__file__).with_name('backup.py'))
backup = importlib.util.module_from_spec(spec)
spec.loader.exec_module(backup)


class BackupTest(unittest.TestCase):
    def setUp(self):
        self.lock_folder = tempfile.TemporaryDirectory()
        self.lock_patch = patch.object(backup, 'BACKUP_LOCK_PATH', Path(self.lock_folder.name) / 'shared.lock')
        self.lock_patch.start()

    def tearDown(self):
        self.lock_patch.stop()
        self.lock_folder.cleanup()

    def test_shared_lock_blocks_two_store_backups_before_disk_admission(self):
        with tempfile.TemporaryDirectory() as folder:
            with backup.backup_lock(backup.BACKUP_LOCK_PATH):
                for name in ['site', 'telegram']:
                    with self.assertRaises(RuntimeError):
                        backup.main(Path(folder) / name)
            self.assertEqual(list(Path(folder).iterdir()), [])

    def test_wal_snapshot_and_sha256(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            (root / 'data').mkdir()
            conn = sqlite3.connect(root / 'data/support.db')
            try:
                conn.execute('PRAGMA journal_mode=WAL')
                conn.execute('CREATE TABLE messages (content TEXT)')
                conn.execute('INSERT INTO messages VALUES (?)', ('private fixture',))
                conn.commit()
                result = backup.main(root)
                archive = Path(result['backup'])
                self.assertEqual(hashlib.sha256(archive.read_bytes()).hexdigest(), result['sha256'])
                self.assertIn(result['sha256'], archive.with_suffix('.gz.sha256').read_text())
                restored = root / 'restored.db'
                restored.write_bytes(gzip.decompress(archive.read_bytes()))
                with closing(sqlite3.connect(restored)) as db:
                    self.assertEqual(db.execute('SELECT content FROM messages').fetchall(), [('private fixture',)])
            finally:
                conn.close()

    def test_missing_source_does_not_create_empty_database(self):
        with tempfile.TemporaryDirectory() as folder:
            with self.assertRaises(sqlite3.OperationalError):
                backup.main(Path(folder))
            self.assertFalse((Path(folder) / 'data/support.db').exists())

    def test_low_disk_and_backup_quota_stop_before_snapshot(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            (root / 'data').mkdir()
            with closing(sqlite3.connect(root / 'data/support.db')) as db:
                db.execute('CREATE TABLE messages (content TEXT)')
            for kwargs in [dict(free_bytes=0, existing_bytes=0), dict(free_bytes=20 << 30, existing_bytes=2 << 30)]:
                with self.assertRaises(RuntimeError):
                    backup.backup_headroom(4096, **kwargs)
            with patch.object(backup.shutil, 'disk_usage', return_value=shutil.disk_usage(root)._replace(free=0)):
                with self.assertRaises(RuntimeError):
                    backup.main(root)
            self.assertEqual(list((root / 'backups').glob('*.sqlite*')), [])

    def test_compression_failure_removes_only_new_artifacts(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            (root / 'data').mkdir()
            with closing(sqlite3.connect(root / 'data/support.db')) as db:
                db.execute('CREATE TABLE messages (content TEXT)')
            with patch.object(backup.gzip, 'open', side_effect=OSError('fixture compression failure')):
                with self.assertRaises(OSError):
                    backup.main(root)
            self.assertEqual(list((root / 'backups').iterdir()), [])
            self.assertTrue((root / 'data/support.db').exists())

    def test_busy_backup_has_a_deadline_and_cleans_partial_snapshot(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            (root / 'data').mkdir()
            with closing(sqlite3.connect(root / 'data/support.db')) as db:
                db.execute('CREATE TABLE messages (content TEXT)')
            with patch.object(backup.time, 'monotonic', side_effect=[0, 121]):
                with self.assertRaises(RuntimeError):
                    backup.main(root)
            self.assertEqual(list((root / 'backups').iterdir()), [])


if __name__ == '__main__':
    unittest.main()
