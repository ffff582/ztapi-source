import importlib.util
import io
import json
from pathlib import Path
import sqlite3
import unittest
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location("image_partial_finance", Path(__file__).with_name("image_partial_finance.py"))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class FinanceReleaseTests(unittest.TestCase):
    def test_finance_uses_same_revision_by_default(self):
        self.assertEqual(m.finance_revision({"commit": "a" * 40}, None), "a" * 40)

    def test_finance_hotfix_requires_its_own_exact_commit_ci(self):
        build = {"commit": "a" * 40}
        artifact = {"commit": "b" * 40, "parent": "a" * 40, "ci_passed": True, "ci_commit": "b" * 40}
        self.assertEqual(m.finance_revision(build, artifact), "b" * 40)
        for key, value in [("commit", "not-a-sha"), ("parent", "c" * 40), ("ci_passed", False), ("ci_commit", "c" * 40)]:
            with self.subTest(key=key), self.assertRaises(ValueError):
                m.finance_revision(build, {**artifact, key: value})

    def test_finance_runtime_image_returns_verified_immutable_id(self):
        image_id = "sha256:" + "1" * 64
        runtime = Mock()
        runtime.inspect.return_value = {"Id": image_id, "Config": {"Labels": {"org.opencontainers.image.revision": "b" * 40}}}
        self.assertEqual(m.finance_runtime_image(runtime, "finance:hotfix", "b" * 40), image_id)
        runtime.inspect.assert_called_once_with("finance:hotfix")

    def test_finance_runtime_image_rejects_mismatched_or_missing_revision(self):
        for labels in ({"org.opencontainers.image.revision": "a" * 40}, {}):
            with self.subTest(labels=labels):
                runtime = Mock()
                runtime.inspect.return_value = {"Id": "sha256:" + "1" * 64, "Config": {"Labels": labels}}
                with self.assertRaisesRegex(ValueError, "^finance_runtime_revision_mismatch$"):
                    m.finance_runtime_image(runtime, "finance:hotfix", "b" * 40)

    def test_finance_runtime_image_rejects_nonimmutable_ids(self):
        for image_id in ("finance:hotfix", "1" * 64, "sha256:" + "1" * 63, "sha256:" + "G" * 64, ""):
            with self.subTest(image_id=image_id):
                runtime = Mock()
                runtime.inspect.return_value = {"Id": image_id, "Config": {"Labels": {"org.opencontainers.image.revision": "b" * 40}}}
                with self.assertRaisesRegex(ValueError, "^finance_runtime_digest_required$"):
                    m.finance_runtime_image(runtime, "finance:hotfix", "b" * 40)

    def test_finance_handoff_pins_both_creations_when_tag_is_retargeted(self):
        main_commit, finance_commit = "a" * 40, "b" * 40
        verified_id, retargeted_id = "sha256:" + "1" * 64, "sha256:" + "2" * 64
        image_tag = "finance:hotfix"
        root = Path("/opt/ztapi/releases/image-partial-20261109-r3")
        source, backup = sqlite3.connect(":memory:"), sqlite3.connect(":memory:")
        self.addCleanup(source.close)
        self.addCleanup(backup.close)
        source.execute("CREATE TABLE financial_facts(id INTEGER,kind TEXT,ref TEXT,previous_id INTEGER,payload TEXT,payload_hash TEXT,operator_id INTEGER,created_at TEXT)")
        source.execute("INSERT INTO financial_facts VALUES(1,'funding','ledger:1',0,'{}','fixture-hash',1,'2026-11-09')")
        source.commit()
        old = {"Id": "old-id", "Config": {"User": "ztapi"},
               "Mounts": [{"Source": "/mock-finance", "Destination": "/data"}],
               "NetworkSettings": {"Networks": {"ztapi_app": {"IPAddress": "192.0.2.1"}}}, "State": {"Pid": 1}}
        image = {"Id": verified_id, "Config": {"Labels": {"org.opencontainers.image.revision": finance_commit}}}
        containers = {m.CANONICAL: old, "old-id": old,
                      "main": {"Id": "main-id", "Config": {"Env": ["ZTAPI_SOURCE_COMMIT=" + main_commit]}}}
        runtime = Mock(MAIN="main", NGINX="nginx", CONF="/mock.conf")
        runtime.inspect.side_effect = lambda name: image if name == image_tag else containers[name]
        runtime.envmap.side_effect = lambda value: dict(item.split("=", 1) for item in value["Config"]["Env"])
        runtime.clone.side_effect = lambda old, image, commit, temporary: {"Image": image, "Env": ["ZTAPI_SOURCE_COMMIT=" + commit], "HostConfig": {}}
        runtime.docker.side_effect = lambda *args, **kwargs: (b"set $ztapi_finance http://ztapi-finance-support-20261102-live:3011;\n" if args[0] == "exec" else b"")
        runtime.install_nginx.return_value = []
        runtime.nginx_workers.return_value = []
        runtime.run.return_value = b"sl local_address rem_address st\n"
        created_images = []

        def create(name, config):
            actual_image = image["Id"] if config["Image"] == image_tag else config["Image"]
            created_images.append(actual_image)
            cid = "candidate-id" if name == m.TEMPORARY else "current-id"
            containers[cid] = containers[name] = {"Id": cid, "Image": actual_image, "Config": config,
                                                "NetworkSettings": old["NetworkSettings"], "State": {"Pid": 1}}
            return cid

        def retarget_tag(*args):
            # Retag after candidate readiness, before the final creation.
            image["Id"] = retargeted_id

        def open_response(req, timeout):
            if req.full_url.endswith("/healthz"):
                value = {"ok": True}
            else:
                value = {"success": True, "data": {"source_commit": finance_commit, "test_costs": [
                    {"fact_id": fact_id, "status": "estimate", "evidence_type": "quote"} for fact_id in (412, 413, 414, 415)]}}
            response = io.BytesIO(json.dumps(value).encode())
            response.status = 200
            return response

        runtime.create.side_effect = create
        runtime.set_restart_policy.side_effect = retarget_tag
        opener = Mock()
        opener.open.side_effect = open_response
        baseline = json.dumps({"containers": {m.CANONICAL: {"id": "old-id"}}}).encode()
        artifact = {"commit": finance_commit, "parent": main_commit, "ci_passed": True,
                    "ci_commit": finance_commit, "image": image_tag}
        with (
            patch.object(m.Path, "read_bytes", return_value=baseline),
            patch.object(m.sqlite3, "connect", side_effect=lambda path, **kwargs: source if str(path).startswith("file:") else backup),
            patch.object(m.os, "chmod"),
            patch.object(m.request, "build_opener", return_value=opener),
            patch.object(m.time, "sleep"),
        ):
            result = m.run_finance(runtime, root, {"commit": main_commit}, "synthetic-test-token", finance_build=artifact)
        self.assertTrue(result["passed"])
        self.assertEqual(image["Id"], retargeted_id)
        self.assertEqual([call.args[0] for call in runtime.create.call_args_list], [m.TEMPORARY, m.CANONICAL])
        self.assertEqual([call.args[1]["Image"] for call in runtime.create.call_args_list], [verified_id, verified_id])
        self.assertEqual(created_images, [verified_id, verified_id])
        self.assertEqual(result["source_commit"], finance_commit)
        self.assertEqual(result["main_source_commit"], main_commit)

    def test_finance_target_switch_is_exact_and_compareable(self):
        raw = b"set $ztapi_finance http://ztapi-finance-support-20261102-live:3011;\nset $model http://server:3000;\n"
        updated = m.finance_target(raw, "ztapi-image-finance-candidate-20261109-r3")
        self.assertIn(b"http://ztapi-image-finance-candidate-20261109-r3:3011", updated)
        self.assertIn(b"set $model http://server:3000", updated)
        with self.assertRaises(ValueError):
            m.finance_target(raw + raw, "ztapi-image-finance-candidate-20261109-r3")

    def test_only_finance_listener_connections_block_finance_drain(self):
        raw = "sl local_address rem_address st\n0: 00000000:0BC3 00000000:0000 0A\n1: 0100007F:0BC3 0100007F:1234 01\n2: 0100007F:1234 0100007F:0CEA 01\n"
        self.assertEqual(m.finance_clients(raw), 1)
        with self.assertRaises(ValueError):
            m.finance_clients("header\nbroken")


if __name__ == "__main__":
    unittest.main()
