import os
from pathlib import Path
import tempfile
import time
import unittest

import ztapi_cleanup as cleanup


class CleanupTests(unittest.TestCase):
    def test_running_mount_and_latest_releases_are_protected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for index in range(7):
                release = root / str(index)
                release.mkdir()
                (release / "build-cache").mkdir()
                (release / "build-cache" / "large").write_bytes(b"test")
                os.utime(release, (100 + index, 100 + index))
            containers = [{"Name": "/ztapi-server-1", "State": {"Running": True}, "Mounts": [{"Source": str(root / "0" / "proof.json")}], "Config": {"Labels": {}}}]
            paths = cleanup.release_candidates(root, containers)
            self.assertEqual(paths, [root / "1" / "build-cache"])

    def test_symlink_and_credentials_are_never_followed(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            outside = root / "outside"
            outside.mkdir()
            (outside / "key.env").write_text("protected")
            releases = root / "releases"
            releases.mkdir()
            try:
                (releases / "bad").symlink_to(outside, target_is_directory=True)
            except OSError:
                self.skipTest("symlink creation unavailable")
            self.assertEqual(cleanup.release_candidates(releases, []), [])
            self.assertFalse(cleanup.safe_target(releases / "bad", [releases]))

    def test_only_old_stopped_ztapi_candidates_are_removable(self):
        now = time.time()
        stopped = {"Running": False, "FinishedAt": "2020-01-01T00:00:00Z"}
        rows = [{"Name": "/ztapi-test-candidate-old", "State": stopped}, {"Name": "/ztapi-mysql-1", "State": stopped}, {"Name": "/deploy-api-1", "State": stopped}, {"Name": "/ztapi-test-candidate-running", "State": {"Running": True}}]
        self.assertEqual(cleanup.stopped_candidates(rows, now), ["ztapi-test-candidate-old"])

    def test_temporary_directory_with_a_key_is_retained(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            old = root / "ztapi-old"
            old.mkdir()
            (old / "credentials.env").write_text("do-not-delete")
            os.utime(old, (100, 100))
            self.assertEqual(cleanup.temporary_candidates(root, [], time.time()), [])


if __name__ == "__main__":
    unittest.main()
