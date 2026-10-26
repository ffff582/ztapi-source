import contextlib
import gzip
import importlib.util
import io
from pathlib import Path
import sqlite3
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('finance_backup', Path(__file__).with_name('backup.py'))
backup = importlib.util.module_from_spec(spec)
spec.loader.exec_module(backup)


class BackupTests(unittest.TestCase):
    def test_failed_fsync_never_deletes_the_sqlite_recovery_snapshot(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            (root / 'data').mkdir()
            db = sqlite3.connect(root / 'data/finance.db')
            db.execute('CREATE TABLE facts(id INTEGER)')
            db.close()
            with patch.object(backup.os, 'fsync', side_effect=OSError('simulated durability failure')):
                with self.assertRaises(OSError), contextlib.redirect_stdout(io.StringIO()):
                    backup.main(root)
            self.assertEqual(len(list((root / 'backups').glob('*.sqlite'))), 1)

    def test_consistent_backup_retains_all_financial_records_and_checksum(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            (root / 'data').mkdir()
            with sqlite3.connect(root / 'data/finance.db') as db:
                db.execute('CREATE TABLE facts(id INTEGER PRIMARY KEY, amount TEXT)')
                db.execute("INSERT INTO facts VALUES(1,'20.000000')")
            db.close()
            with contextlib.redirect_stdout(io.StringIO()):
                backup.main(root)
            archives = list((root / 'backups').glob('*.gz'))
            self.assertEqual(len(archives), 1)
            archive = archives[0]
            with archive.open('rb') as stream:
                self.assertEqual(archive.with_suffix('.gz.sha256').read_text().split()[0], backup.digest(stream))
            restored = root / 'restored.sqlite'
            with gzip.open(archive, 'rb') as stream:
                restored.write_bytes(stream.read())
            with sqlite3.connect(restored) as db:
                self.assertEqual(db.execute('SELECT amount FROM facts WHERE id=1').fetchone(), ('20.000000',))
            db.close()


if __name__ == '__main__':
    unittest.main()
