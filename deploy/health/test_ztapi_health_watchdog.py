"""Offline only: every Docker/HTTP call is injected; no daemon or network needed."""

import importlib.util
import json
import os
from pathlib import Path
import stat
import sys
import tempfile
import threading
import time
from types import SimpleNamespace
import unittest
from unittest.mock import patch


SCRIPT = Path(__file__).with_name("ztapi-health-watchdog.py")
SPEC = importlib.util.spec_from_file_location("ztapi_health_watchdog", SCRIPT)
watchdog = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = watchdog
SPEC.loader.exec_module(watchdog)


class WatchdogTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.env = self.root / "health.env"
        self.state = self.root / "state"
        self.secret = "https://alerts.invalid/hook?token=PRIVATE_WEBHOOK_SECRET"
        self.configure(self.secret)
        self.now = 2000000000
        self.output = []
        self.commands = []
        self.requests = []
        self.heartbeat = self.now
        self.worker_code = "worker_ready"
        self.dead = None
        self.db_failure = False
        self.http_status = 204

    def configure(self, recipient):
        self.env.write_text(
            "ZTAPI_HEALTH_PROBE_KEY=PRIVATE_PROBE_KEY\n"
            f"ZTAPI_HEALTH_ALERT_WEBHOOK_URL='{recipient}'\n",
            encoding="utf-8",
        )
        self.env.chmod(0o600)

    def docker(self, argv, timeout):
        self.commands.append(argv)
        self.assertLessEqual(timeout, 5)
        self.assertEqual(argv[:2], ["/usr/bin/docker", "--host=unix:///var/run/docker.sock"])
        self.assertNotIn(self.secret, repr(argv))
        self.assertNotIn("PRIVATE_PROBE_KEY", repr(argv))
        if "inspect" in argv:
            return watchdog.CommandResult(0, b"false\n" if argv[-1] == self.dead else b"true\n")
        self.assertIn("exec", argv)
        self.assertIn("MYSQL_PWD", argv[-1])
        self.assertIn(watchdog.HEARTBEAT_SQL, argv[-1])
        self.assertNotIn("-p\"", argv[-1])
        if self.db_failure:
            return watchdog.CommandResult(1, b"PRIVATE_SQL_ERROR")
        return watchdog.CommandResult(0, f"{self.heartbeat}\t{self.worker_code}\n".encode())

    def send(self, url, payload, incident_key, timeout):
        self.assertEqual(url, self.secret)
        self.assertLessEqual(timeout, 8)
        self.requests.append((payload, incident_key))
        self.assertNotIn("PRIVATE_", json.dumps(payload))
        return self.http_status

    def run_tick(self):
        return watchdog.run_once(
            self.env, self.state, command=self.docker, sender=self.send,
            clock=lambda: self.now, log=self.output.append,
        )

    def persisted(self):
        return json.loads((self.state / "state.json").read_text(encoding="utf-8"))

    def test_fresh_heartbeat_never_sends(self):
        self.assertEqual(self.run_tick(), 0)
        self.assertEqual(len(self.commands), 3)
        self.assertFalse(self.requests)
        self.assertEqual(self.persisted()["condition"], "ok")

    def test_exact_threshold_then_stale_dedup_and_recovery(self):
        self.heartbeat = self.now - 900
        self.assertEqual(self.run_tick(), 0)
        self.heartbeat -= 1
        self.assertEqual(self.run_tick(), 1)
        self.assertEqual(len(self.requests), 1)
        state = self.persisted()
        self.assertEqual(state["condition"], "worker_stale")
        self.assertEqual(state["delivery"], "accepted_unverified")
        first_key = self.requests[0][1]
        self.now += 60
        self.assertEqual(self.run_tick(), 1)
        self.assertEqual(len(self.requests), 1)
        self.heartbeat = self.now
        self.assertEqual(self.run_tick(), 0)
        self.heartbeat -= 901
        self.run_tick()
        self.assertEqual(len(self.requests), 2)
        self.assertNotEqual(first_key, self.requests[-1][1])

    def test_dead_container_and_db_failure_are_sanitized(self):
        self.dead = "ztapi-server-1"
        self.run_tick()
        self.assertEqual(self.persisted()["condition"], "server_container_dead")
        self.dead = "ztapi-mysql-1"
        self.run_tick()
        self.assertEqual(self.persisted()["condition"], "database_container_dead")
        self.dead = None
        self.db_failure = True
        self.run_tick()
        self.assertEqual(self.persisted()["condition"], "database_query_failed")
        self.assertNotIn("PRIVATE_SQL_ERROR", repr(self.output))

    def test_missing_recipient_stays_undelivered_and_later_retries(self):
        self.configure("")
        self.heartbeat -= 901
        self.assertEqual(self.run_tick(), 1)
        self.assertFalse(self.requests)
        self.assertEqual(self.persisted()["delivery"], "undelivered")
        self.assertIn("alert_undelivered_missing_recipient", repr(self.output))
        self.configure(self.secret)
        self.run_tick()
        self.assertEqual(len(self.requests), 1)

    def test_retry_backoff_and_uncertain_restart_use_same_key(self):
        self.heartbeat -= 901
        self.http_status = None
        self.run_tick()
        state = self.persisted()
        key = self.requests[0][1]
        self.assertEqual(state["next_attempt_at"], self.now + 60)
        self.assertEqual(state["delivery"], "pending")
        self.now += 59
        self.run_tick()
        self.assertEqual(len(self.requests), 1)
        self.now += 1
        self.http_status = 503
        self.run_tick()
        self.assertEqual(self.persisted()["next_attempt_at"], self.now + 120)
        self.assertEqual(self.requests[-1][1], key)
        self.now += 120
        self.http_status = 204
        self.run_tick()
        self.assertEqual(self.persisted()["delivery"], "accepted_unverified")
        self.assertEqual(self.requests[-1][1], key)

    def test_pending_outage_survives_recovery_without_losing_retry(self):
        self.heartbeat -= 901
        self.http_status = None
        self.run_tick()
        key = self.requests[0][1]
        self.now += 60
        self.heartbeat = self.now
        self.http_status = 204
        self.run_tick()
        self.assertEqual(len(self.requests), 2)
        self.assertEqual(self.requests[-1][1], key)
        self.assertEqual(self.requests[-1][0]["condition"], "worker_stale")
        self.assertEqual(self.requests[-1][0]["current_condition"], "ok")
        self.assertEqual(self.persisted()["delivery"], "accepted_unverified")
        self.run_tick()
        self.assertEqual(self.persisted()["condition"], "ok")

    def test_unix_security_policy_checks_owner_mode_and_links_offline(self):
        with patch.object(watchdog.os, "name", "posix"), patch.object(watchdog.os, "geteuid", return_value=0, create=True):
            for mode, uid, links in ((stat.S_IFREG | 0o644, 0, 1),
                                     (stat.S_IFREG | 0o600, 1000, 1),
                                     (stat.S_IFREG | 0o600, 0, 2),
                                     (stat.S_IFLNK | 0o600, 0, 1)):
                with self.assertRaises(ValueError):
                    watchdog.check_private(SimpleNamespace(st_mode=mode, st_uid=uid, st_nlink=links))
            watchdog.check_private(SimpleNamespace(st_mode=stat.S_IFREG | 0o600, st_uid=0, st_nlink=1))
            watchdog.check_private(SimpleNamespace(st_mode=stat.S_IFDIR | 0o700, st_uid=0, st_nlink=2), directory=True)
    def test_crash_after_acceptance_retries_ambiguity(self):
        self.heartbeat -= 901
        save = watchdog.write_state
        writes = 0

        def fail_final_save(*args):
            nonlocal writes
            writes += 1
            if writes == 2:
                raise OSError("PRIVATE_FILESYSTEM_ERROR")
            return save(*args)

        with patch.object(watchdog, "write_state", side_effect=fail_final_save):
            self.assertEqual(self.run_tick(), 2)
        self.assertEqual(self.persisted()["delivery"], "sending")
        key = self.requests[0][1]
        self.now += 60
        self.run_tick()
        self.assertEqual(self.requests[-1][1], key)
        self.assertEqual(len(self.requests), 2)
        self.assertNotIn("PRIVATE_FILESYSTEM_ERROR", repr(self.output))

    def test_dotenv_is_literal_and_secrets_never_persist(self):
        marker = self.root / "must-not-exist"
        self.env.write_text(
            f"IGNORED=$(touch {marker})\n"
            f"ZTAPI_HEALTH_ALERT_WEBHOOK_URL='{self.secret}'\n", encoding="utf-8",
        )
        self.heartbeat -= 901
        self.run_tick()
        self.assertFalse(marker.exists())
        self.assertNotIn(self.secret, (self.state / "state.json").read_text())
        self.assertNotIn("PRIVATE_", repr(self.output))
        self.configure("http://insecure.invalid/PRIVATE_SECRET")
        self.requests.clear()
        self.run_tick()
        self.assertFalse(self.requests)
        self.assertIn("alert_undelivered_invalid_recipient", repr(self.output))

    def test_bad_heartbeat_and_bounded_docker_output(self):
        for heartbeat in ["PRIVATE_SQL_ERROR", self.now + 121, -1]:
            self.heartbeat = heartbeat
            self.run_tick()
            self.assertEqual(self.persisted()["condition"], "heartbeat_invalid")
        self.worker_code = "PRIVATE_WORKER_CODE"
        self.heartbeat = self.now
        self.run_tick()
        self.assertNotIn("PRIVATE_", repr(self.output))
        self.assertEqual(watchdog.run_command(
            [sys.executable, "-c", "print('x' * 100000)"], 2
        ).returncode, -1)
        self.assertEqual(watchdog.run_command(
            [sys.executable, "-c", "import time; time.sleep(5)"], 0.1
        ).returncode, -1)

    def test_state_corruption_and_overlap_never_send(self):
        self.run_tick()
        original = "PRIVATE_CORRUPT_STATE"
        (self.state / "state.json").write_text(original)
        self.heartbeat -= 901
        self.assertEqual(self.run_tick(), 2)
        self.assertFalse(self.requests)
        self.assertEqual((self.state / "state.json").read_text(), original)
        (self.state / "state.json").write_text(json.dumps(watchdog.empty_state()))
        with watchdog.state_lock(self.state):
            self.assertEqual(self.run_tick(), 0)
        self.assertFalse(self.requests)
        self.assertNotIn(original, repr(self.output))

    def test_https_has_no_redirects_and_does_not_read_private_body(self):
        class FakeConnection:
            status = 302

            def __init__(self, *args, **kwargs):
                pass

            def request(self, method, path, body, headers):
                self.headers = headers

            def getresponse(self):
                return self

            def read(self, *args):
                raise AssertionError("must not read webhook response body")

            def close(self):
                pass

        with patch.object(watchdog.http.client, "HTTPSConnection", FakeConnection):
            self.assertEqual(watchdog.send_webhook(self.secret, {"event": "test"}, "fixed-id", 1), 302)

    def test_http_wall_timeout_is_uncertain_and_config_is_bounded(self):
        release = threading.Event()

        class SlowConnection:
            def __init__(self, *args, **kwargs):
                pass

            def request(self, *args, **kwargs):
                pass

            def getresponse(self):
                release.wait(2)
                return SimpleNamespace(status=204)

            def close(self):
                pass

        try:
            with patch.object(watchdog.http.client, "HTTPSConnection", SlowConnection):
                start = time.monotonic()
                self.assertIsNone(watchdog.send_webhook(self.secret, {}, "same-id", 0.02))
                self.assertLess(time.monotonic() - start, 1)
        finally:
            release.set()
        self.env.write_text("x" * 65537, encoding="utf-8")
        self.assertEqual(self.run_tick(), 2)
        self.assertFalse(self.commands)
        self.assertFalse(self.requests)

    def test_query_is_fixed_read_only_and_recipient_not_in_argv(self):
        self.assertTrue(watchdog.HEARTBEAT_SQL.startswith("SELECT "))
        self.assertNotIn(";", watchdog.HEARTBEAT_SQL)
        self.assertNotIn("ZTAPI_HEALTH_ALERT_WEBHOOK_URL", watchdog.MYSQL_READ)
        self.assertNotIn("ZTAPI_HEALTH_PROBE_KEY", watchdog.MYSQL_READ)
        self.env.write_text("ZTAPI_HEALTH_WATCHDOG_SERVER_CONTAINER='--privileged'\n", encoding="utf-8")
        self.assertEqual(self.run_tick(), 2)
        self.assertFalse(self.commands)

    @unittest.skipUnless(os.name == "posix", "Unix ownership/mode enforcement")
    def test_insecure_config_and_symlink_state_rejected(self):
        self.env.chmod(0o644)
        self.assertEqual(self.run_tick(), 2)
        self.assertFalse(self.commands)
        self.assertFalse(self.requests)
        self.env.chmod(0o600)
        outside = self.root / "outside"
        outside.write_text("unchanged")
        self.state.mkdir(mode=0o700, exist_ok=True)
        (self.state / "state.json").symlink_to(outside)
        self.assertEqual(self.run_tick(), 2)
        self.assertEqual(outside.read_text(), "unchanged")

    def test_systemd_is_external_bounded_and_not_enabled_by_tests(self):
        service = SCRIPT.with_suffix(".service").read_text()
        timer = SCRIPT.with_suffix(".timer").read_text()
        self.assertIn("Type=oneshot", service)
        self.assertIn("TimeoutStartSec=45s", service)
        self.assertIn("StateDirectory=ztapi-health-watchdog", service)
        self.assertNotIn("EnvironmentFile=", service)
        self.assertNotIn("Restart=always", service)
        self.assertIn("OnUnitActiveSec=1min", timer)
        self.assertIn("AccuracySec=1s", timer)


if __name__ == "__main__":
    unittest.main()
