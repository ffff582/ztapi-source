"""Scoped image release using the existing audited drain and recovery path."""
import copy
from decimal import Decimal
import importlib.util
import json
import os
from pathlib import Path
import re
import sys


PARTIAL_SCOPE = [
    {"image_operation": "generation", "size": "1024x1024", "quality": "low", "resolution": "1K"},
    {"image_operation": "generation", "size": "1152x2048", "quality": "low", "resolution": "2K"},
    {"image_operation": "edit", "size": "1024x1024", "quality": "low", "resolution": "1K"},
]
RELEASE = "image-partial-20261109-r3"


def require(ok, reason):
    if not ok:
        raise ValueError(reason)


def validate_partial_scope(value):
    require(value == PARTIAL_SCOPE, "only_three_previously_accepted_selectors_authorized")


def validate_budget(value):
    require(Decimal(value["total_u"]) == Decimal("10"), "original_budget_changed")
    require(Decimal(value["unknown_reservations_u"]) == Decimal("9.14416403"), "unknown_reservations_changed")
    require(Decimal(value["unreserved_u"]) == Decimal("0.85583597"), "unreserved_budget_changed")
    require(value["new_paid_calls"] == 0, "new_paid_calls_not_authorized_by_this_partial_release")


def validate_partial_acceptance(build, proof):
    require(build["tests_passed"] and proof["passed"], "build_or_acceptance_incomplete")
    require(re.fullmatch("[a-f0-9]{40}", build["commit"]) is not None and proof["commit"] == build["commit"], "acceptance_commit_mismatch")
    validate_partial_scope(proof["scope"])
    require(proof["paid_calls"] == 0 and proof["full_http_router_passed"], "isolated_router_acceptance_required")
    require(proof["original_accepted_settlements"] == [3758, 3759, 3761], "original_real_acceptance_evidence_changed")
    require(proof["paid_4k_acceptance_passed"] is False, "partial_release_must_not_certify_4k")


def classify_partial_enable(current, reviewed_retail, frozen_cost, journal_exists):
    require(current["source"] == 1873 and json.loads(current["channels"]) == [1] and current["cost"] == frozen_cost, "enable_source_or_route_changed")
    if current["version"] == 27 and current["snapshot"] == 1631 and not current.get("retail"):
        return "publish"
    require(journal_exists and current["version"] == 28 and current["snapshot"] > 1631 and current.get("retail") == reviewed_retail, "unjournaled_or_different_partial_publication")
    return "resume"


def patch_application_clone(value, stage, commit):
    value = copy.deepcopy(value)
    env = dict(item.split("=", 1) for item in value["Env"])
    env["ZTAPI_SOURCE_TAG"] = "production-" + commit
    env["ZTAPI_REFERRAL_COST_ATTESTATION_FILE"] = "/run/ztapi-referral-cost/attestation.json"
    value["Env"] = [key + "=" + item for key, item in env.items()]
    host = value["HostConfig"]
    host["LogConfig"] = {"Type": "json-file", "Config": {"max-size": "50m", "max-file": "3"}}
    host["Binds"] = [item for item in host.get("Binds", []) if item.split(":")[1] != "/run/ztapi-referral-cost"]
    host["Binds"].append((stage / "cost").as_posix() + ":/run/ztapi-referral-cost:ro")
    return value


def run_release(mode):
    import fcntl

    require(os.geteuid() == 0 and mode in ("prepare", "activate"), "root_and_reviewed_mode_required")
    root = Path(__file__).resolve().parent
    require(root == Path("/opt/ztapi/releases/" + RELEASE) and not root.is_symlink(), "unexpected_stage")
    load = lambda name: json.loads((root / name).read_bytes())
    before, build, ci = load("baseline-before-migration.json"), load("build.json"), load("ci-passed.json")
    validate_budget(before["budget"])
    require(build["tests_passed"] and ci["passed"] and build["commit"] == ci["commit"], "same_commit_ci_required")
    require(ci["production_tag"] == "production-" + build["commit"], "immutable_production_tag_required")
    require(build["parent"] == before["source_commit"] == "15dbdc443fd4a86f08c2cf442821364d4b18f152", "production_parent_changed")
    spec = importlib.util.spec_from_file_location("rolling", "/opt/ztapi/releases/claude-spend-20261102-r1/longtext_security.py")
    r = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(r)
    r.STAGE, r.RELEASE = root, RELEASE
    r.CANDIDATE = "ztapi-image-partial-candidate-20261109-r3"
    r.PREVIOUS = "ztapi-server-before-image-partial-20261109-r3"
    base_clone, base_backup = r.clone, r.backup
    r.clone = lambda original, image, commit, temporary: patch_application_clone(base_clone(original, image, commit, temporary), root, commit)

    def build_image(original, metadata):
        for name, digest in metadata["binaries"].items():
            require(r.sha((root / name).read_bytes()) == digest, "binary_hash_mismatch")
            os.chmod(root / name, 0o555)
        allowed = ["Dockerfile.runtime", "ztapi-linux-amd64", "ztapi-finance", "ztapi-referral-migrate", "LICENSE", "NOTICE", "THIRD-PARTY-LICENSES.md"]
        r.save(".dockerignore", ("**\n" + "\n".join("!" + name for name in allowed) + "\n").encode())
        name = "ztapi-main:" + RELEASE
        output = r.docker("build", "--network=none", "--pull=false", "--build-arg", "SOURCE_COMMIT=" + metadata["commit"], "-f", str(root / "Dockerfile.runtime"), "-t", name, str(root), timeout=300, combined=True)
        r.save("runtime-image-build.log", output)
        details = r.inspect(name)
        require(details["Config"]["User"] == "ztapi", "non_root_runtime_required")
        return details["Id"]

    def isolated_tests(image, metadata):
        filters = {
            "image-service.test": "^TestZTAPIImageSale|^TestIOCopyBytesGracefully",
            "image-model.test": "^TestZTAPIImageSale",
            "image-openai.test": "^TestOpenaiImageManaged",
            "image-router.test": "^TestZTAPIImageSalePartialHTTP",
        }
        results = {}
        for name, pattern in filters.items():
            file = root / name
            require(r.sha(file.read_bytes()) == metadata["test_binaries"][name], "test_binary_hash_mismatch")
            os.chmod(file, 0o555)
            output = r.docker("run", "--rm", "--network=none", "--read-only", "--user=65532:65532", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--memory=1g", "--cpus=1", "--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=256m,mode=1777", "--mount", "type=bind,src=" + str(file) + ",dst=/tool.test,readonly", "--entrypoint", "/tool.test", image, "-test.v", "-test.count=1", "-test.timeout=180s", "-test.run=" + pattern, timeout=200, combined=True)
            require(b"PASS" in output and b"no tests to run" not in output.lower(), "isolated_test_did_not_run_or_pass")
            r.save(name + ".linux.log", output)
            results[name] = {"passed": True, "sha256": metadata["test_binaries"][name], "filter": pattern}
        proof = {"passed": True, "commit": metadata["commit"], "scope": PARTIAL_SCOPE, "paid_calls": 0,
                 "full_http_router_passed": results["image-router.test"]["passed"],
                 "original_accepted_settlements": [3758, 3759, 3761], "paid_4k_acceptance_passed": False,
                 "tests": results}
        validate_partial_acceptance(metadata, proof)
        r.save("isolated-partial-acceptance.json", proof)
        return results

    r.build_image, r.isolated_tests = build_image, isolated_tests
    lock = os.open("/run/ztapi-traffic-hold-r6/owner.lock", os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    if mode == "prepare":
        r.pins(before)
        backup = base_backup()
        columns = [line.split("\t")[0] for line in r.sql("SHOW COLUMNS FROM ztapi_model_publication_snapshots").decode().splitlines()]
        require(columns == before["publication_columns"] and "image_sale_contract_json" not in columns, "publication_schema_drift")
        require(all(re.fullmatch("[a-z0-9_]+", column) for column in columns), "invalid_legacy_column")
        legacy_query = "SELECT " + ",".join("`" + column + "`" for column in columns) + " FROM ztapi_model_publication_snapshots ORDER BY id"
        legacy_hash = r.sha(r.sql(legacy_query))
        command = 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysql -uroot --batch --raw --skip-column-names "$MYSQL_DATABASE"'
        ddl = b"SET SESSION lock_wait_timeout=5; ALTER TABLE ztapi_model_publication_snapshots ADD COLUMN image_sale_contract_json TEXT NULL, ALGORITHM=INPLACE, LOCK=NONE;"
        r.docker("exec", "-i", "ztapi-mysql-1", "sh", "-c", command, data=ddl, timeout=45)
        require(r.sha(r.sql(legacy_query)) == legacy_hash, "legacy_publications_changed_during_additive_migration")
        require(r.sql("SELECT COUNT(*) FROM ztapi_model_publication_snapshots WHERE image_sale_contract_json IS NOT NULL").strip() == b"0", "historical_picture_contract_backfill_forbidden")
        r.save("schema-migration.json", {"nullable_column_added": True, "legacy_rows_unchanged": True, "legacy_columns_sha256": legacy_hash, "no_backfill": True, "mysql_backup": backup})
        baseline = copy.deepcopy(before)
        baseline["unchanged_business_digests"] = r.business_digests()
        r.save("baseline.json", baseline)
        r.backup = lambda: backup
        return r.prepare(baseline, build)
    baseline = load("baseline.json")
    validate_partial_acceptance(build, load("isolated-partial-acceptance.json"))
    accepted = load("candidate-acceptance.json")
    require(accepted["passed"] and accepted["commit"] == build["commit"] and accepted["paid_calls"] == 0, "candidate_api_and_frontend_acceptance_required")
    require(r.sha((root / "cost" / "attestation.json").read_bytes()) == accepted["proof_sha256"], "candidate_cost_proof_changed")
    return r.activate(baseline, build)


if __name__ == "__main__":
    print(json.dumps(run_release(sys.argv[1])))
