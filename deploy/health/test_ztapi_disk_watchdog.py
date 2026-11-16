import importlib.util
from pathlib import Path
import sys
import unittest
from unittest.mock import patch
from types import SimpleNamespace

spec = importlib.util.spec_from_file_location("disk_watchdog", Path(__file__).with_name("ztapi-health-watchdog.py"))
watchdog = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = watchdog
spec.loader.exec_module(watchdog)


class DiskWatchdogTests(unittest.TestCase):
    def test_threshold_and_available_capacity(self):
        with patch.object(watchdog.shutil, "disk_usage", return_value=SimpleNamespace(total=1000, used=799, free=201)):
            self.assertEqual(watchdog.disk_condition(), "ok")
        with patch.object(watchdog.shutil, "disk_usage", return_value=SimpleNamespace(total=1000, used=800, free=200)):
            self.assertEqual(watchdog.disk_condition(), "disk_usage_high")
        self.assertIn("disk_usage_high", watchdog.CONDITIONS)

    def test_drill_cannot_be_misread_as_a_live_outage(self):
        text = watchdog.telegram_text({"condition": "disk_usage_high", "test_mode": True, "actual_percent": 52})
        self.assertIn("演练", text)
        self.assertIn("52", text)
        self.assertIn("不是实际故障", text)


if __name__ == "__main__":
    unittest.main()
