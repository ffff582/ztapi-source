import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("image_partial_finance", Path(__file__).with_name("image_partial_finance.py"))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class FinanceReleaseTests(unittest.TestCase):
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
