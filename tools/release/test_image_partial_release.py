import copy
import importlib.util
from pathlib import Path
import unittest


spec = importlib.util.spec_from_file_location("image_partial_release", Path(__file__).with_name("image_partial_release.py"))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class PartialImageReleaseTests(unittest.TestCase):
    def budget(self):
        return {"total_u": "10", "unknown_reservations_u": "9.14416403", "unreserved_u": "0.85583597", "new_paid_calls": 0}

    def test_only_three_accepted_selectors_can_be_released(self):
        m.validate_partial_scope(m.PARTIAL_SCOPE)
        for changed in [
            [*m.PARTIAL_SCOPE, {"image_operation": "generation", "size": "3840x2160", "quality": "low", "resolution": "4K"}],
            [*m.PARTIAL_SCOPE[:2], {"image_operation": "edit", "size": "1152x2048", "quality": "low", "resolution": "2K"}],
            m.PARTIAL_SCOPE[:2],
        ]:
            with self.assertRaises(ValueError):
                m.validate_partial_scope(changed)

    def test_unknown_reservations_cannot_be_released(self):
        m.validate_budget(self.budget())
        for key, value in [("unknown_reservations_u", "0.78960373"), ("unreserved_u", "9.21039627"), ("total_u", "11"), ("new_paid_calls", 1)]:
            bad = self.budget()
            bad[key] = value
            with self.assertRaises(ValueError):
                m.validate_budget(bad)

    def test_acceptance_requires_actual_fixture_router_and_matching_commit(self):
        build = {"commit": "a" * 40, "tests_passed": True}
        proof = {"passed": True, "commit": build["commit"], "scope": copy.deepcopy(m.PARTIAL_SCOPE),
                 "paid_calls": 0, "full_http_router_passed": True,
                 "original_accepted_settlements": [3758, 3759, 3761], "paid_4k_acceptance_passed": False}
        m.validate_partial_acceptance(build, proof)
        for key, value in [("commit", "b" * 40), ("full_http_router_passed", False), ("paid_calls", 1), ("original_accepted_settlements", [3758, 3759, 3760])]:
            bad = copy.deepcopy(proof)
            bad[key] = value
            with self.assertRaises(ValueError):
                m.validate_partial_acceptance(build, bad)

    def test_clone_replaces_only_cost_mount_and_preserves_secret_bindings(self):
        original = {"Env": ["ZTAPI_REFERRAL_WORKER_ENABLED=true", "OTHER_SETTING=unchanged"],
                    "HostConfig": {"Binds": ["/old/cost:/run/ztapi-referral-cost:ro", "/private/config:/run/private:ro"],
                                   "LogConfig": {"Type": "json-file", "Config": {"max-size": "50m", "max-file": "3"}}}}
        value = m.patch_application_clone(copy.deepcopy(original), Path("/opt/ztapi/releases/image-partial-20261109-r3"), "a" * 40)
        self.assertEqual(original["HostConfig"]["Binds"][0], "/old/cost:/run/ztapi-referral-cost:ro")
        self.assertEqual(value["HostConfig"]["Binds"], ["/private/config:/run/private:ro", "/opt/ztapi/releases/image-partial-20261109-r3/cost:/run/ztapi-referral-cost:ro"])
        self.assertIn("OTHER_SETTING=unchanged", value["Env"])
        self.assertIn("ZTAPI_SOURCE_TAG=production-" + "a" * 40, value["Env"])

    def test_enable_can_resume_only_the_exact_journaled_publication(self):
        current = {"version": 27, "snapshot": 1631, "source": 1873, "channels": "[1]", "cost": "frozen-cost", "retail": None}
        self.assertEqual(m.classify_partial_enable(current, "approved-partial", "frozen-cost", False), "publish")
        current.update(version=28, snapshot=1700, retail="approved-partial")
        with self.assertRaises(ValueError):
            m.classify_partial_enable(current, "approved-partial", "frozen-cost", False)
        self.assertEqual(m.classify_partial_enable(current, "approved-partial", "frozen-cost", True), "resume")
        current["retail"] = "different-policy"
        with self.assertRaises(ValueError):
            m.classify_partial_enable(current, "approved-partial", "frozen-cost", True)


if __name__ == "__main__":
    unittest.main()
