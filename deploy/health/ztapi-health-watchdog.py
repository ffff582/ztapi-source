#!/usr/bin/env python3
"""Independent, one-shot HOST watchdog. No inference, DB writes or recovery.

Install prerequisites (parent operator, not this script): Linux/systemd,
Python >= 3.9, /usr/bin/docker and a local Docker socket. The mysql container
must contain sh/mysql and MYSQL_USER, MYSQL_PASSWORD and MYSQL_DATABASE; the
worker-status migration must already exist. Credentials are never provisioned.
Install this file root-owned at:
  /usr/local/lib/ztapi-health-watchdog/ztapi-health-watchdog.py
Keep /etc/ztapi/health.env root-owned 0600 (0400 also works). Only literal
KEY=value / quoted values are read; no shell evaluation or variable expansion.
Recognized keys:
  ZTAPI_HEALTH_TELEGRAM_BOT_TOKEN (literal bot token)
  ZTAPI_HEALTH_TELEGRAM_CHAT_ID (nonzero numeric ID or @channel_username)
  ZTAPI_HEALTH_ALERT_WEBHOOK_URL (HTTPS fallback only if BOTH Telegram keys absent)
  ZTAPI_HEALTH_WATCHDOG_SERVER_CONTAINER (default ztapi-server-1)
  ZTAPI_HEALTH_WATCHDOG_MYSQL_CONTAINER (default ztapi-mysql-1)
The script never imports the file into its environment. State is root-owned
0700 under /var/lib/ztapi-health-watchdog. Install/enable the supplied timer
ONLY after recipient authorization and offline tests. No auto-install/enable.
Supplying either Telegram key requires both to be valid, even if empty. Telegram
uses only api.telegram.org HTTPS sendMessage, without redirects, and requires
2xx JSON ok:true with a positive integer message_id. No response is persisted.
API acceptance never verifies human receipt. An accepted send whose state commit
is lost is retried with the same incident ID (and webhook idempotency key).
Webhook receivers should honor that key. Telegram has no idempotency guarantee:
an uncertain send or lost state commit can result in duplicate messages.
One pending incident is coalesced across changing conditions and retained after
recovery until accepted; this bounds state without silently losing retries.
"""

import contextlib
from dataclasses import dataclass
import hashlib
import http.client
import json
import os
from pathlib import Path
import queue
import re
import ssl
import stat
import subprocess
import threading
import time
from urllib.parse import urlsplit
import uuid


ENV_PATH = Path("/etc/ztapi/health.env")
STATE_DIR = Path("/var/lib/ztapi-health-watchdog")
STALE_SECONDS = 900
DOCKER_TIMEOUT = 5
HTTP_TIMEOUT = 8
MAX_OUTPUT = 4096
MAX_TELEGRAM_RESPONSE = 65536
HEARTBEAT_SQL = (
    "SELECT /*+ MAX_EXECUTION_TIME(2000) */ updated_at, code "
    "FROM ztapi_health_worker_statuses WHERE component='worker' LIMIT 1"
)
# This fixed shell program runs INSIDE mysql, not on the host or dotenv file.
# Password bytes are expanded into MYSQL_PWD, never interpolated into argv.
MYSQL_READ = (
    'MYSQL_PWD="${MYSQL_PASSWORD:?}" exec mysql --protocol=TCP '
    '--host=127.0.0.1 --connect-timeout=3 --user="${MYSQL_USER:?}" '
    '--database="${MYSQL_DATABASE:?}" --batch --skip-column-names --raw '
    '--execute="' + HEARTBEAT_SQL + '"'
)
DOCKER = ["/usr/bin/docker", "--host=unix:///var/run/docker.sock"]
WORKER_CODES = {
    "worker_tick_running", "worker_ready", "worker_tick_error",
    "worker_waiting_first_tick",
}
CONDITIONS = {
    "ok", "server_container_dead", "server_container_unavailable",
    "database_container_dead", "database_container_unavailable",
    "database_query_failed", "heartbeat_missing", "heartbeat_invalid",
    "worker_stale", "worker_tick_error",
}


@dataclass(frozen=True)
class CommandResult:
    returncode: int
    stdout: bytes


class Busy(Exception):
    pass


def check_private(info, directory=False):
    kind_ok = stat.S_ISDIR(info.st_mode) if directory else stat.S_ISREG(info.st_mode)
    if not kind_ok or (not directory and info.st_nlink != 1):
        raise ValueError("unsafe file type")
    if os.name == "posix":
        if info.st_uid != os.geteuid() or stat.S_IMODE(info.st_mode) & 0o077:
            raise ValueError("unsafe owner or permissions")


def private_open(path, flags):
    # Reject links/FIFOs before opening as well as with O_NOFOLLOW on Linux.
    try:
        check_private(Path(path).lstat())
    except FileNotFoundError:
        pass
    flags |= getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_NONBLOCK", 0)
    fd = os.open(path, flags, 0o600)
    try:
        check_private(os.fstat(fd))
    except Exception:
        os.close(fd)
        raise
    return fd


def read_private(path, maximum):
    with os.fdopen(private_open(path, os.O_RDONLY), "rb") as handle:
        data = handle.read(maximum + 1)
    if len(data) > maximum:
        raise ValueError("file exceeds limit")
    return data


def load_config(path):
    keys = {
        "ZTAPI_HEALTH_ALERT_WEBHOOK_URL": "",
        # None distinguishes absence from explicitly supplied empty values.
        "ZTAPI_HEALTH_TELEGRAM_BOT_TOKEN": None,
        "ZTAPI_HEALTH_TELEGRAM_CHAT_ID": None,
        "ZTAPI_HEALTH_WATCHDOG_SERVER_CONTAINER": "ztapi-server-1",
        "ZTAPI_HEALTH_WATCHDOG_MYSQL_CONTAINER": "ztapi-mysql-1",
    }
    seen = set()
    for line in read_private(path, 65536).decode("utf-8-sig").splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        if line.startswith("export "):
            line = line[7:].lstrip()
        match = re.fullmatch(r"([A-Za-z_][A-Za-z0-9_]*)\s*=(.*)", line)
        if not match:
            raise ValueError("invalid literal environment file")
        key, value = match.groups()
        if key not in keys:
            continue
        if key in seen:
            raise ValueError("duplicate configuration key")
        seen.add(key)
        value = value.strip()
        if value.startswith(("'", '"')):
            quoted = re.fullmatch(r"(['\"])(.*?)\1\s*(?:#.*)?", value)
            if not quoted:
                raise ValueError("invalid quoted configuration")
            value = quoted.group(2)
        else:
            value = re.split(r"\s+#", value, maxsplit=1)[0].rstrip()
        keys[key] = value
    for key in ("ZTAPI_HEALTH_WATCHDOG_SERVER_CONTAINER", "ZTAPI_HEALTH_WATCHDOG_MYSQL_CONTAINER"):
        if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,127}", keys[key]):
            raise ValueError("invalid container identifier")
    token = keys["ZTAPI_HEALTH_TELEGRAM_BOT_TOKEN"]
    chat_id = keys["ZTAPI_HEALTH_TELEGRAM_CHAT_ID"]
    if (token is not None or chat_id is not None) and not valid_telegram(token, chat_id):
        raise ValueError("invalid Telegram configuration")
    return keys


def run_command(argv, timeout):
    """Bound both stdout memory and wall time; discard all raw stderr."""
    try:
        process = subprocess.Popen(
            argv, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL, shell=False,
            env={"PATH": "/usr/bin:/bin", "LANG": "C", "LC_ALL": "C"},
        )
    except Exception:
        return CommandResult(-1, b"")
    output = []

    def drain():
        try:
            data = process.stdout.read(MAX_OUTPUT + 1)
            output.append(data)
            if len(data) > MAX_OUTPUT:
                process.kill()
        except Exception:
            pass

    reader = threading.Thread(target=drain, daemon=True)
    reader.start()
    failed = False
    try:
        process.wait(timeout=timeout)
    except subprocess.TimeoutExpired:
        failed = True
        process.kill()
        process.wait(timeout=1)
    reader.join(timeout=1)
    process.stdout.close()
    if failed or reader.is_alive() or not output or len(output[0]) > MAX_OUTPUT:
        return CommandResult(-1, b"")
    return CommandResult(process.returncode, output[0])


def observe(config, command, now):
    for key, role in (("ZTAPI_HEALTH_WATCHDOG_SERVER_CONTAINER", "server"),
                      ("ZTAPI_HEALTH_WATCHDOG_MYSQL_CONTAINER", "database")):
        result = command(DOCKER + ["inspect", "--format", "{{.State.Running}}", config[key]], DOCKER_TIMEOUT)
        if result.returncode != 0 or result.stdout.strip() not in (b"true", b"false"):
            return role + "_container_unavailable", None, None
        if result.stdout.strip() == b"false":
            return role + "_container_dead", None, None
    result = command(DOCKER + ["exec", config["ZTAPI_HEALTH_WATCHDOG_MYSQL_CONTAINER"], "sh", "-c", MYSQL_READ], DOCKER_TIMEOUT)
    if result.returncode != 0:
        return "database_query_failed", None, None
    if not result.stdout.strip():
        return "heartbeat_missing", None, None
    if len(result.stdout) > 256:
        return "heartbeat_invalid", None, None
    try:
        timestamp, code = result.stdout.decode("ascii").strip().split("\t")
        if not re.fullmatch(r"[0-9]{1,12}", timestamp):
            raise ValueError("invalid timestamp")
        timestamp = int(timestamp)
        if code not in WORKER_CODES or timestamp <= 0 or timestamp > now + 120:
            raise ValueError("invalid heartbeat")
    except (UnicodeError, ValueError):
        return "heartbeat_invalid", None, None
    if now - timestamp > STALE_SECONDS:
        return "worker_stale", timestamp, code
    if code == "worker_tick_error":
        return "worker_tick_error", timestamp, code
    return "ok", timestamp, code


def valid_webhook(url):
    if not url or len(url) > 4096 or any(ord(ch) <= 32 or ord(ch) == 127 for ch in url):
        return False
    try:
        parsed = urlsplit(url)
        return (parsed.scheme == "https" and bool(parsed.hostname) and
                parsed.username is None and parsed.password is None and
                not parsed.fragment and (parsed.port is None or 1 <= parsed.port <= 65535))
    except ValueError:
        return False


def send_webhook(url, payload, incident_key, timeout):
    """HTTPS only; no proxies/redirects/body reads; bounded caller wall time.

    On timeout the daemon thread may have sent: return uncertainty, not failure
    proof. This one-shot process exits after state persistence; systemd is an
    additional 45-second process bound, not an automatic retry mechanism.
    """
    if not valid_webhook(url):
        return None
    results = queue.Queue(maxsize=1)

    def send():
        connection = None
        try:
            parsed = urlsplit(url)
            connection = http.client.HTTPSConnection(
                parsed.hostname, parsed.port or 443, timeout=timeout,
                context=ssl.create_default_context(),
            )
            path = (parsed.path or "/") + (("?" + parsed.query) if parsed.query else "")
            connection.request("POST", path, json.dumps(payload, separators=(",", ":")).encode(), {
                "Content-Type": "application/json", "Idempotency-Key": incident_key,
            })
            results.put(connection.getresponse().status)
        except Exception:
            results.put(None)
        finally:
            if connection is not None:
                try:
                    connection.close()
                except Exception:
                    pass

    threading.Thread(target=send, daemon=True).start()
    try:
        return results.get(timeout=timeout)
    except queue.Empty:
        return None


def valid_telegram(token, chat_id):
    if not isinstance(token, str) or not isinstance(chat_id, str):
        return False
    if not re.fullmatch(r"[1-9][0-9]{0,19}:[A-Za-z0-9_-]{35}", token):
        return False
    if re.fullmatch(r"-?[1-9][0-9]{0,15}", chat_id):
        return abs(int(chat_id)) < 2 ** 52
    return re.fullmatch(r"@[A-Za-z][A-Za-z0-9_]{4,31}", chat_id) is not None


def send_telegram(token, chat_id, payload, timeout):
    """Return only an API-confirmed message ID, or None for any uncertainty.

    Bound response memory and caller wall time, including DNS/TLS/body reads.
    Never expose request URLs, response bodies or exception details to callers.
    """
    if not valid_telegram(token, chat_id):
        return None
    results = queue.Queue(maxsize=1)

    def reject_constant(_):
        raise ValueError("invalid JSON constant")

    def unique_object(pairs):
        data = {}
        for name, value in pairs:
            if name in data:
                raise ValueError("duplicate JSON key")
            data[name] = value
        return data

    def send():
        connection = None
        message_id = None
        try:
            connection = http.client.HTTPSConnection(
                "api.telegram.org", 443, timeout=timeout,
                context=ssl.create_default_context(),
            )
            body = {"chat_id": chat_id, "text": json.dumps(payload, separators=(",", ":"))}
            connection.request("POST", "/bot" + token + "/sendMessage",
                               json.dumps(body, separators=(",", ":")).encode(),
                               {"Content-Type": "application/json"})
            response = connection.getresponse()
            if 200 <= response.status < 300:
                raw = response.read(MAX_TELEGRAM_RESPONSE + 1)
                if len(raw) <= MAX_TELEGRAM_RESPONSE:
                    data = json.loads(raw.decode("utf-8"), parse_constant=reject_constant,
                                      object_pairs_hook=unique_object)
                    if isinstance(data, dict) and data.get("ok") is True:
                        result = data.get("result")
                        candidate = result.get("message_id") if isinstance(result, dict) else None
                        if type(candidate) is int and candidate > 0:
                            message_id = candidate
        except Exception:
            pass
        finally:
            if connection is not None:
                try:
                    connection.close()
                except Exception:
                    pass
            results.put(message_id)

    threading.Thread(target=send, daemon=True).start()
    try:
        return results.get(timeout=timeout)
    except queue.Empty:
        return None


@contextlib.contextmanager
def state_lock(directory):
    directory.mkdir(mode=0o700, exist_ok=True)
    check_private(directory.lstat(), directory=True)
    fd = private_open(directory / "lock", os.O_RDWR | os.O_CREAT)
    acquired = False
    try:
        if os.name == "posix":
            import fcntl
            try:
                fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError:
                raise Busy() from None
        else:  # Portable offline tests; deployment is explicitly Linux.
            import msvcrt
            if os.fstat(fd).st_size == 0:
                os.write(fd, b"0")
            os.lseek(fd, 0, os.SEEK_SET)
            try:
                msvcrt.locking(fd, msvcrt.LK_NBLCK, 1)
            except OSError:
                raise Busy() from None
        acquired = True
        yield
    finally:
        if acquired:
            if os.name == "posix":
                fcntl.flock(fd, fcntl.LOCK_UN)
            else:
                os.lseek(fd, 0, os.SEEK_SET)
                msvcrt.locking(fd, msvcrt.LK_UNLCK, 1)
        os.close(fd)


def empty_state():
    return {"version": 1, "condition": "ok", "incident_id": "", "destination_id": "",
            "first_seen": 0, "last_seen": 0, "attempts": 0, "next_attempt_at": 0,
            "delivery": "none"}


def read_state(directory):
    try:
        data = json.loads(read_private(directory / "state.json", 16384))
    except FileNotFoundError:
        return empty_state()
    if not isinstance(data, dict) or set(data) != set(empty_state()) or data["version"] != 1:
        raise ValueError("invalid state schema")
    if data["condition"] not in CONDITIONS or data["delivery"] not in {"none", "pending", "sending", "undelivered", "accepted_unverified"}:
        raise ValueError("invalid state enum")
    for key in ("first_seen", "last_seen", "attempts", "next_attempt_at"):
        if type(data[key]) is not int or not 0 <= data[key] <= 253402300799:
            raise ValueError("invalid state integer")
    for key, size in (("incident_id", 32), ("destination_id", 64)):
        if not isinstance(data[key], str) or (data[key] and not re.fullmatch("[0-9a-f]{" + str(size) + "}", data[key])):
            raise ValueError("invalid state identifier")
    if data["condition"] != "ok" and not data["incident_id"]:
        raise ValueError("missing incident identifier")
    return data


def write_state(directory, state):
    path = directory / "state.next"
    with os.fdopen(private_open(path, os.O_WRONLY | os.O_CREAT), "wb") as handle:
        # Validate ownership/type before truncation, including stale temp files.
        handle.truncate(0)
        handle.write(json.dumps(state, sort_keys=True, separators=(",", ":")).encode())
        handle.flush()
        os.fsync(handle.fileno())
    os.replace(path, directory / "state.json")
    if os.name == "posix":
        fd = os.open(directory, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)


def report(log, code, condition="unknown"):
    # Only caller-owned fixed codes are logged. Never exceptions/argv/SQL/URLs.
    log(json.dumps({"code": code, "condition": condition}, separators=(",", ":")))


def run_once(env_path=ENV_PATH, state_dir=STATE_DIR, *, command=run_command,
             sender=send_webhook, telegram_sender=send_telegram, clock=time.time, log=print):
    try:
        config = load_config(Path(env_path))
    except Exception:
        report(log, "alert_undelivered_configuration_error")
        return 2
    try:
        with state_lock(Path(state_dir)):
            state = read_state(Path(state_dir))
            now = int(clock())
            condition, timestamp, worker_code = observe(config, command, now)
            url = config["ZTAPI_HEALTH_ALERT_WEBHOOK_URL"]
            token = config["ZTAPI_HEALTH_TELEGRAM_BOT_TOKEN"]
            chat_id = config["ZTAPI_HEALTH_TELEGRAM_CHAT_ID"]
            telegram = token is not None
            recipient_valid = telegram or valid_webhook(url)
            pending = state["condition"] != "ok" and state["delivery"] != "accepted_unverified"
            if condition == "ok" and not pending:
                state = empty_state()
                state["last_seen"] = now
                write_state(Path(state_dir), state)
                report(log, "watchdog_ok" if recipient_valid else "watchdog_ok_alerts_disabled", condition)
                return 0
            if state["condition"] != condition and not pending:
                state = empty_state()
                state.update(condition=condition, incident_id=uuid.uuid4().hex,
                             first_seen=now, delivery="pending")
            recipient = "telegram\0" + token + "\0" + chat_id if telegram else url
            destination = hashlib.sha256(recipient.encode()).hexdigest() if recipient else ""
            if state["destination_id"] != destination:
                state.update(destination_id=destination, attempts=0,
                             next_attempt_at=0, delivery="pending")
            state["last_seen"] = now
            if not recipient_valid:
                state["delivery"] = "undelivered"
                write_state(Path(state_dir), state)
                report(log, "alert_undelivered_missing_recipient" if not url else "alert_undelivered_invalid_recipient", condition)
                return 1
            if state["delivery"] == "accepted_unverified":
                write_state(Path(state_dir), state)
                report(log, "alert_already_accepted_unverified", condition)
                return 1
            if now < state["next_attempt_at"]:
                write_state(Path(state_dir), state)
                report(log, "alert_retry_pending_undelivered", condition)
                return 1
            state["attempts"] = min(state["attempts"] + 1, 1000000)
            state["next_attempt_at"] = now + min(3600, 60 * (2 ** min(state["attempts"] - 1, 6)))
            state["delivery"] = "sending"
            # Never send until the stable ID and retry intent are fsynced.
            write_state(Path(state_dir), state)
            payload = {
                "event": "ztapi.host_watchdog", "incident_id": state["incident_id"],
                "condition": state["condition"], "current_condition": condition,
                "observed_at": now, "first_seen_at": state["first_seen"],
                "worker_updated_at": timestamp, "worker_code": worker_code,
                "heartbeat_age_seconds": max(0, now - timestamp) if timestamp else None,
                "stale_threshold_seconds": STALE_SECONDS,
                "admin_status_path": "/api/models/ztapi/health/status",
            }
            key = "ztapi-watchdog-" + hashlib.sha256((state["incident_id"] + destination).encode()).hexdigest()
            try:
                status = (telegram_sender(token, chat_id, payload, HTTP_TIMEOUT) if telegram
                          else sender(url, payload, key, HTTP_TIMEOUT))
            except Exception:
                status = None
            accepted = type(status) is int and (status > 0 if telegram else 200 <= status < 300)
            state["delivery"] = "accepted_unverified" if accepted else "pending"
            write_state(Path(state_dir), state)
            accepted_code = "telegram_api_accepted_not_receipt" if telegram else "http_2xx_accepted_not_receipt"
            report(log, accepted_code if accepted else "alert_uncertain_retry_undelivered", condition)
            return 1
    except Busy:
        report(log, "watchdog_overlap_skipped")
        return 0
    except Exception:
        report(log, "watchdog_internal_error_undelivered")
        return 2


if __name__ == "__main__":
    try:
        exit_code = run_once()
    except (Exception, KeyboardInterrupt):
        try:
            report(print, "watchdog_internal_error_undelivered")
        except Exception:
            pass
        exit_code = 2
    raise SystemExit(exit_code)
