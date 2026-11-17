"""Finance-only compatibility handoff before enabling partial image sales."""
import copy
import hashlib
import json
import os
from pathlib import Path
import re
import sqlite3
import time
from urllib import request


CANONICAL = "ztapi-finance-support-20261102-live"
TEMPORARY = "ztapi-image-finance-candidate-20261109-r3"


def require(ok, reason):
    if not ok:
        raise ValueError(reason)


def finance_target(raw, target):
    old = b"http://" + CANONICAL.encode() + b":3011"
    require(raw.count(old) == 1, "finance_upstream_identity_changed")
    require(target.replace("-", "").isalnum(), "invalid_finance_target")
    return raw.replace(old, b"http://" + target.encode() + b":3011")


def finance_clients(raw):
    count = 0
    for row in raw.splitlines()[1:]:
        fields = row.split()
        require(len(fields) >= 4, "invalid_finance_socket_row")
        if fields[3] not in ("06", "07", "0A") and int(fields[1].rsplit(":", 1)[1], 16) == 3011:
            count += 1
    return count


def finance_revision(build, artifact):
    if artifact is None:
        return build["commit"]
    require(re.fullmatch("[a-f0-9]{40}", artifact["commit"]) is not None, "invalid_finance_commit")
    require(artifact["parent"] == build["commit"] and artifact["ci_passed"] and artifact["ci_commit"] == artifact["commit"], "finance_hotfix_exact_commit_ci_required")
    return artifact["commit"]


def finance_runtime_image(r, image, commit):
    details = r.inspect(image)
    require(details["Config"].get("Labels", {}).get("org.opencontainers.image.revision") == commit, "finance_runtime_revision_mismatch")
    require(re.fullmatch("sha256:[a-f0-9]{64}", details["Id"]) is not None, "finance_runtime_digest_required")
    return details["Id"]


def run_finance(r, root, build, access, finance_build=None):
    require(root == Path("/opt/ztapi/releases/image-partial-20261109-r3"), "unexpected_finance_stage")
    before = json.loads((root / "baseline-before-migration.json").read_bytes())
    old = r.inspect(CANONICAL)
    require(old["Id"] == before["containers"][CANONICAL]["id"], "finance_baseline_changed")
    main = r.inspect(r.MAIN)
    require(r.envmap(main).get("ZTAPI_SOURCE_COMMIT") == build["commit"], "new_main_must_be_healthy_before_finance_handoff")
    source_commit = finance_revision(build, finance_build)
    old_config = r.docker("exec", r.NGINX, "cat", r.CONF)
    candidate_config = finance_target(old_config, TEMPORARY)
    r.save("original-finance-hotfix.private.json" if finance_build else "original-finance.private.json", old)
    data = next(item["Source"] for item in old["Mounts"] if item["Destination"] == "/data")
    store = Path(data) / "finance.db"

    def identity(maximum=None):
        with sqlite3.connect("file:" + str(store) + "?mode=ro", uri=True) as db:
            if maximum is None:
                maximum = db.execute("SELECT COALESCE(MAX(id),0) FROM financial_facts").fetchone()[0]
            rows = db.execute("SELECT id,kind,ref,previous_id,payload,payload_hash,operator_id,created_at FROM financial_facts WHERE id<=? ORDER BY id", (maximum,)).fetchall()
        return {"maximum_id": maximum, "count": len(rows), "sha256": hashlib.sha256(json.dumps(rows, default=str).encode()).hexdigest()}

    facts = identity()
    with sqlite3.connect("file:" + str(store) + "?mode=ro", uri=True) as source:
        with sqlite3.connect(root / ("finance-before-hotfix.db" if finance_build else "finance-before.db")) as target:
            source.backup(target)
            require(target.execute("PRAGMA integrity_check").fetchone() == ("ok",), "finance_backup_integrity_failed")
    os.chmod(root / ("finance-before-hotfix.db" if finance_build else "finance-before.db"), 0o600)
    opener = request.build_opener(request.ProxyHandler({}))

    def call(cid, path, authenticated=True):
        ip = r.inspect(cid)["NetworkSettings"]["Networks"]["ztapi_app"]["IPAddress"]
        headers = {"Cache-Control": "no-cache"}
        if authenticated:
            headers.update({"Origin": "https://admin.ztapi.vip", "Authorization": "Bearer " + access, "New-Api-User": "1"})
        with opener.open(request.Request("http://" + ip + ":3011" + path, headers=headers), timeout=20) as response:
            require(response.status == 200, "finance_read_failed")
            return json.load(response)

    def report(cid):
        result = call(cid, "/api/admin/finance-reporting/report?month=2026-11")
        require(result.get("success"), "finance_report_unavailable")
        return result["data"]

    def expected_tests(value):
        result = [item for item in value["test_costs"] if item["fact_id"] in (412, 413, 414, 415)]
        require(len(result) == 4 and all(item["status"] == "estimate" and item["evidence_type"] == "quote" for item in result), "existing_test_estimates_changed")
        return result

    previous_tests = expected_tests(report(old["Id"]))
    image = finance_build["image"] if finance_build else json.loads((root / "published.json").read_bytes())["image"]
    image = finance_runtime_image(r, image, source_commit)

    def configuration(temporary):
        value = r.clone(old, image, build["commit"], temporary)
        env = dict(item.split("=", 1) for item in value["Env"])
        env["FINANCE_SOURCE_COMMIT"] = source_commit
        value["Env"] = [key + "=" + item for key, item in env.items()]
        value["Entrypoint"], value["Cmd"] = ["/usr/local/bin/ztapi-finance"], None
        value["User"], value["WorkingDir"] = old["Config"]["User"], "/data"
        value["HostConfig"]["LogConfig"] = {"Type": "json-file", "Config": {"max-size": "50m", "max-file": "3"}}
        value["Healthcheck"] = {"Test": ["CMD-SHELL", "wget -q -O - http://127.0.0.1:3011/healthz >/dev/null || exit 1"], "Interval": 10000000000, "Timeout": 3000000000, "Retries": 3}
        return value

    def ready(cid):
        for _ in range(35):
            try:
                health = call(cid, "/healthz", False)
                current = report(cid)
                if health.get("ok") and current["source_commit"] == source_commit:
                    require(expected_tests(current) == previous_tests, "finance_test_facts_readback_changed")
                    return
            except (OSError, ValueError):
                pass
            time.sleep(1)
        raise ValueError("new_finance_not_ready")

    def drain(cid, workers):
        deadline, quiet = time.monotonic() + 120, 0
        while time.monotonic() < deadline:
            pid = r.inspect(cid)["State"]["Pid"]
            sockets = sum(finance_clients(r.run(["nsenter", "-t", str(pid), "-n", "cat", "/proc/net/" + table]).decode()) for table in ("tcp", "tcp6"))
            retired = not (set(r.nginx_workers()) & set(workers))
            quiet = quiet + 1 if not sockets and retired else 0
            if quiet >= 3:
                return {"clients": 0, "retired_nginx_workers": True, "quiet_checks": quiet}
            time.sleep(1)
        raise ValueError("finance_connections_not_drained_no_force_stop")

    temporary = r.create(TEMPORARY, configuration(True))
    r.docker("start", temporary)
    ready(temporary)
    require(identity(facts["maximum_id"]) == facts, "private_finance_history_changed")
    workers = r.install_nginx(candidate_config, old_config)
    first_drain = drain(old["Id"], workers)
    r.set_restart_policy(old["Id"], {"Name": "no", "MaximumRetryCount": 0})
    r.docker("stop", "--time=-1", old["Id"], timeout=25)
    r.docker("rename", old["Id"], "ztapi-finance-before-image-partial-20261109-r3")
    for network in old["NetworkSettings"]["Networks"]:
        r.docker("network", "disconnect", network, old["Id"])
    current = r.create(CANONICAL, configuration(False))
    r.docker("start", current)
    ready(current)
    workers = r.install_nginx(old_config, candidate_config)
    second_drain = drain(temporary, workers)
    r.docker("stop", "--time=-1", temporary, timeout=25)
    require(identity(facts["maximum_id"]) == facts, "private_finance_facts_changed_during_handoff")
    require(r.inspect(r.MAIN)["Id"] == main["Id"], "finance_handoff_restarted_main")
    result = {"passed": True, "source_commit": source_commit, "main_source_commit": build["commit"], "container": current, "previous": old["Id"],
              "first_drain": first_drain, "second_drain": second_drain, "existing_facts_unchanged": True,
              "facts_identity": facts, "test_fact_ids": [412, 413, 414, 415], "main_unchanged": True,
              "minimum_compatible_reader": source_commit, "rollback_requires_v2_reader": True, "paid_calls": 0}
    r.save("finance-compatibility.json", result)
    return result
