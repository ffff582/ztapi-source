"""Offline Telegram contract tests; all HTTP and Docker boundaries are faked."""

import json
from pathlib import Path
import socket
import ssl
import tempfile
import threading
import time
import unittest
from unittest.mock import patch

from test_ztapi_health_watchdog import watchdog


TOKEN = "123456789:" + "A" * 35
CHAT = "-1001234567890"
WEBHOOK = "https://alerts.invalid/PRIVATE_WEBHOOK_SECRET"


class TelegramTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.env = self.root / "health.env"
        self.state = self.root / "state"
        self.now = 2000000000
        self.healthy = False
        self.logs = []
        self.commands = []
        self.requests = []
        self.webhooks = []
        self.connections = []
        self.read_limits = []
        self.status = 200
        self.body = json.dumps({
            "ok": True, "result": {
                "message_id": 123, "date": self.now,
                "chat": {"id": int(CHAT), "type": "supergroup"},
                "text": "PRIVATE_RESPONSE " + TOKEN,
            },
        }).encode()
        self.error = None
        self.configure()
        owner = self

        class Connection:
            def __init__(self, host, port=None, **kwargs):
                owner.connections.append((host, port, kwargs))

            def request(self, method, path, body, headers):
                # The incident and retry schedule must be durable before I/O.
                owner.requests.append((method, path, json.loads(body), headers,
                                       owner.persisted()))
                if owner.error is not None:
                    raise owner.error

            def getresponse(self):
                self.status = owner.status
                return self

            def read(self, limit):
                owner.read_limits.append(limit)
                return owner.body[:limit]

            def close(self):
                pass

        mock = patch.object(watchdog.http.client, "HTTPSConnection", Connection)
        mock.start()
        self.addCleanup(mock.stop)
        for target in ("connect", "connect_ex"):
            mock = patch.object(socket.socket, target,
                                side_effect=AssertionError("network forbidden"))
            mock.start()
            self.addCleanup(mock.stop)

    def configure(self, token=TOKEN, chat=CHAT, webhook=WEBHOOK):
        fields = {
            "ZTAPI_HEALTH_ALERT_WEBHOOK_URL": webhook,
            "ZTAPI_HEALTH_TELEGRAM_BOT_TOKEN": token,
            "ZTAPI_HEALTH_TELEGRAM_CHAT_ID": chat,
        }
        self.env.write_text("".join(
            f"{key}='{value}'\n" for key, value in fields.items() if value is not None
        ), encoding="utf-8")
        self.env.chmod(0o600)

    def persisted(self):
        return json.loads((self.state / "state.json").read_text(encoding="utf-8"))

    def docker(self, argv, timeout):
        self.commands.append(argv)
        if "inspect" in argv:
            return watchdog.CommandResult(0, b"true\n" if self.healthy else b"false\n")
        return watchdog.CommandResult(0, f"{self.now}\tworker_ready\n".encode())

    def webhook(self, *args):
        self.webhooks.append(args)
        return 204

    def run_tick(self):
        return watchdog.run_once(
            self.env, self.state, command=self.docker, sender=self.webhook,
            clock=lambda: self.now, log=self.logs.append,
        )

    def assert_redacted(self):
        evidence = json.dumps(self.logs) + repr(self.commands)
        if self.state.exists():
            evidence += "".join(path.read_text(encoding="utf-8")
                                for path in self.state.iterdir() if path.name != "lock")
        for private in (TOKEN, CHAT, WEBHOOK, "api.telegram.org", "PRIVATE_", "sendMessage"):
            self.assertNotIn(private, evidence)

    def test_telegram_success_uses_fixed_https_and_persists_before_sending(self):
        self.assertEqual(self.run_tick(), 1)
        self.assertEqual(len(self.requests), 1)
        self.assertFalse(self.webhooks)
        host, port, options = self.connections[0]
        self.assertEqual((host, port), ("api.telegram.org", 443))
        self.assertLessEqual(options["timeout"], 8)
        self.assertEqual(options["context"].verify_mode, ssl.CERT_REQUIRED)
        self.assertTrue(options["context"].check_hostname)
        method, path, payload, headers, intent = self.requests[0]
        self.assertEqual((method, path), ("POST", f"/bot{TOKEN}/sendMessage"))
        self.assertEqual(headers["Content-Type"], "application/json")
        self.assertEqual(payload["chat_id"], CHAT)
        self.assertNotIn("parse_mode", payload)
        text = payload["text"]
        self.assertIsInstance(text, str)
        self.assertLessEqual(len(text), 4096)
        self.assertIn("server_container_dead", text)
        self.assertIn(intent["incident_id"], text)
        self.assertNotIn(TOKEN, text)
        self.assertNotIn(WEBHOOK, text)
        self.assertEqual(intent["delivery"], "sending")
        self.assertEqual(intent["attempts"], 1)
        self.assertEqual(intent["next_attempt_at"], self.now + 60)
        self.assertEqual(self.persisted()["delivery"], "accepted_unverified")
        self.assertTrue(self.read_limits)
        self.assertLessEqual(max(self.read_limits), 65537)
        self.now += 60
        self.run_tick()
        self.assertEqual(len(self.requests), 1)
        self.assert_redacted()

    def test_webhook_fallback_requires_both_telegram_fields_absent(self):
        self.configure(token=None, chat=None)
        self.assertEqual(self.run_tick(), 1)
        self.assertEqual(len(self.webhooks), 1)
        self.assertEqual(self.webhooks[0][0], WEBHOOK)
        self.assertFalse(self.requests)
        self.assertEqual(self.persisted()["delivery"], "accepted_unverified")

    def test_partial_empty_and_invalid_configuration_never_falls_back(self):
        for token, chat in (
            (TOKEN, None), (None, CHAT), ("", ""), ("", CHAT), (TOKEN, ""),
            ("invalid", CHAT), ("123:short", CHAT), (TOKEN + "/evil", CHAT),
            (TOKEN + "?query", CHAT), (" " + TOKEN, CHAT),
            (TOKEN, "https://other.invalid/PRIVATE_CHAT"), (TOKEN, "0"),
            (TOKEN, "-0"), (TOKEN, "1.5"), (TOKEN, " 123"),
            (TOKEN, "1\t2"), (TOKEN, "123/evil"), (TOKEN, "@"),
            (TOKEN, "999999999999999999999999999999"),
        ):
            for healthy in (False, True):
                with self.subTest(token=token, chat=chat, healthy=healthy):
                    self.healthy = healthy
                    self.configure(token, chat)
                    self.assertEqual(self.run_tick(), 2)
                    self.assertFalse(self.commands)
                    self.assertFalse(self.requests)
                    self.assertFalse(self.webhooks)
                    self.assertFalse(self.state.exists())
                    self.assert_redacted()

    def test_duplicate_telegram_key_is_a_configuration_error(self):
        with self.env.open("a", encoding="utf-8") as handle:
            handle.write(f"ZTAPI_HEALTH_TELEGRAM_CHAT_ID='{CHAT}'\n")
        self.assertEqual(self.run_tick(), 2)
        self.assertFalse(self.commands)
        self.assertFalse(self.webhooks)
        self.assertFalse(self.requests)

    def test_valid_telegram_ignores_invalid_webhook_and_accepts_channel_username(self):
        self.configure(chat="@health_alerts", webhook="http://invalid/PRIVATE_URL")
        self.run_tick()
        self.assertEqual(self.persisted()["delivery"], "accepted_unverified")
        self.assertEqual(self.requests[0][2]["chat_id"], "@health_alerts")
        self.assertFalse(self.webhooks)

    def test_healthy_telegram_reports_enabled_without_sending(self):
        self.configure(webhook=None)
        self.healthy = True
        self.assertEqual(self.run_tick(), 0)
        self.assertEqual(json.loads(self.logs[-1])["code"], "watchdog_ok")
        self.assertFalse(self.requests)

    def test_only_strict_success_json_and_positive_integer_message_id_are_accepted(self):
        bodies = [
            b'{"ok":false,"description":"PRIVATE_RESPONSE"}',
            b"PRIVATE_MALFORMED", b"", b"[]", b"null", b"true", b"42",
            b'{"ok":true}', b'{"ok":true,"result":null}',
            b'{"ok":true,"result":[]}',
            b'{"ok":1,"result":{"message_id":123}}',
            b'{"ok":"true","result":{"message_id":123}}',
            b'{"result":{"message_id":123}}',
        ] + [json.dumps({"ok": True, "result": {"message_id": value}}).encode()
             for value in (None, 0, -1, True, False, "123", 1.5, [], {})]
        incident = None
        for body in bodies:
            with self.subTest(body=body):
                self.body = body
                self.assertEqual(self.run_tick(), 1)
                state = self.persisted()
                self.assertEqual(state["delivery"], "pending")
                self.assertEqual(len(self.requests), state["attempts"])
                incident = incident or state["incident_id"]
                self.assertEqual(state["incident_id"], incident)
                self.assertFalse(self.webhooks)
                self.assert_redacted()
                self.now = state["next_attempt_at"]

    def test_429_and_redirects_retry_without_fallback_or_following_location(self):
        for status in (199, 301, 302, 303, 307, 308, 400, 401, 429, 500):
            with self.subTest(status=status):
                self.status = status
                before = len(self.requests)
                self.run_tick()
                self.assertEqual(self.persisted()["delivery"], "pending")
                self.assertEqual(len(self.requests), before + 1)
                self.assertEqual(len(self.connections), len(self.requests))
                self.assertFalse(self.read_limits)
                self.assertFalse(self.webhooks)
                self.assert_redacted()
                self.now = self.persisted()["next_attempt_at"]

    def test_nonstandard_and_ambiguous_json_is_not_acceptance(self):
        for body in (
            b'{"ok":true,"result":{"message_id":123},"extra":NaN}',
            b'{"ok":true,"result":{"message_id":123},"extra":Infinity}',
            b'{"ok":true,"result":{"message_id":123},"extra":-Infinity}',
            b'{"ok":false,"ok":true,"result":{"message_id":123}}',
            b'{"ok":true,"result":{"message_id":0,"message_id":123}}',
        ):
            with self.subTest(body=body):
                self.body = body
                self.run_tick()
                self.assertEqual(self.persisted()["delivery"], "pending")
                self.now = self.persisted()["next_attempt_at"]

    def test_malformed_utf8_and_oversized_response_stay_pending(self):
        for body in (b'\xff', b" " * 65536 + self.body):
            with self.subTest(size=len(body)):
                self.body = body
                self.run_tick()
                self.assertEqual(self.persisted()["delivery"], "pending")
                self.assertLessEqual(max(self.read_limits), 65537)
                self.now = self.persisted()["next_attempt_at"]

    def test_transport_exceptions_are_redacted_and_retried_after_recovery(self):
        for error in (TimeoutError, OSError, ssl.SSLError):
            with self.subTest(error=error):
                self.error = error(f"PRIVATE_TRANSPORT https://api.telegram.org/bot{TOKEN}/sendMessage")
                self.run_tick()
                state = self.persisted()
                self.assertEqual(state["delivery"], "pending")
                count = len(self.requests)
                self.now = state["next_attempt_at"] - 1
                self.run_tick()
                self.assertEqual(len(self.requests), count)
                self.now += 1
                self.assert_redacted()
        self.error = None
        self.healthy = True
        self.run_tick()
        accepted = self.persisted()
        self.assertEqual(accepted["delivery"], "accepted_unverified")
        self.assertEqual(accepted["incident_id"], state["incident_id"])
        self.assertIn('"current_condition":"ok"', self.requests[-1][2]["text"])
        self.run_tick()
        self.assertEqual(self.persisted()["condition"], "ok")
        self.assertFalse(self.webhooks)
        self.assert_redacted()

    def test_body_read_wall_timeout_is_uncertain_and_redacted(self):
        release = threading.Event()
        finished = threading.Event()
        owner = self

        class SlowConnection:
            def __init__(self, *args, **kwargs):
                pass

            def request(self, *args):
                pass

            def getresponse(self):
                self.status = 200
                return self

            def read(self, limit):
                release.wait(2)
                return owner.body

            def close(self):
                finished.set()

        try:
            with patch.object(watchdog.http.client, "HTTPSConnection", SlowConnection), \
                    patch.object(watchdog, "HTTP_TIMEOUT", 0.02):
                start = time.monotonic()
                self.run_tick()
                self.assertLess(time.monotonic() - start, 1)
                self.assertEqual(self.persisted()["delivery"], "pending")
        finally:
            release.set()
            self.assertTrue(finished.wait(2))
        self.assert_redacted()

    def test_crash_after_telegram_acceptance_preserves_durable_retry(self):
        write_state = watchdog.write_state

        def fail_final_save(directory, state):
            if state["delivery"] == "accepted_unverified":
                raise OSError("PRIVATE_SAVE " + TOKEN)
            write_state(directory, state)

        with patch.object(watchdog, "write_state", side_effect=fail_final_save):
            self.assertEqual(self.run_tick(), 2)
        sending = self.persisted()
        self.assertEqual(sending["delivery"], "sending")
        self.now = sending["next_attempt_at"]
        self.run_tick()
        self.assertEqual(len(self.requests), 2)
        self.assertEqual(self.persisted()["incident_id"], sending["incident_id"])
        self.assertEqual(self.persisted()["delivery"], "accepted_unverified")
        self.assert_redacted()

    def test_destination_change_resends_and_invalid_config_preserves_pending_state(self):
        self.status = 429
        self.run_tick()
        pending_bytes = (self.state / "state.json").read_bytes()
        pending = self.persisted()
        self.configure(chat=None)
        self.assertEqual(self.run_tick(), 2)
        self.assertEqual((self.state / "state.json").read_bytes(), pending_bytes)
        self.assertFalse(self.webhooks)
        self.configure(chat="123456789")
        self.status = 200
        self.run_tick()
        self.assertEqual(len(self.requests), 2)
        self.assertEqual(self.persisted()["attempts"], 1)
        self.assertEqual(self.persisted()["incident_id"], pending["incident_id"])
        self.assertNotEqual(self.persisted()["destination_id"], pending["destination_id"])
        self.assertEqual(self.persisted()["delivery"], "accepted_unverified")


if __name__ == "__main__":
    unittest.main()
