"""Retention is the Visionary server's job: the SDK purges only when asked.

Run from sdks/python:  python -m unittest discover -s tests -v
"""

from __future__ import annotations

import logging
import os
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "src"))

from visionary import Config, from_env  # noqa: E402
from visionary._writer import Retention  # noqa: E402


class RetentionDefaultTest(unittest.TestCase):
    def test_defaults_are_off(self):
        cfg = from_env({})
        self.assertEqual(cfg.log_retention_days, 0)
        self.assertEqual(cfg.system_retention_days, 0)
        self.assertEqual(Config().log_retention_days, 0)
        self.assertEqual(Config().system_retention_days, 0)

    def test_env_turns_it_on(self):
        cfg = from_env({"VISIONARY_LOG_RETENTION_DAYS": "90", "VISIONARY_SYSTEM_RETENTION_DAYS": "7"})
        self.assertEqual(cfg.log_retention_days, 90)
        self.assertEqual(cfg.system_retention_days, 7)

    def test_no_purge_thread_at_zero(self):
        log = logging.getLogger("visionary.test")
        off = Retention(None, log, {"requests": 0, "spans": 0, "errors": 0, "system": 0})
        self.assertFalse(off.needed)
        on = Retention(None, log, {"requests": 30, "system": 0})
        self.assertTrue(on.needed)


if __name__ == "__main__":
    unittest.main()
