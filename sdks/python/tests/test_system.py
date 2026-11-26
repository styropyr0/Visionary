"""Tests for the default instance and the system metrics switch (SPEC "Service
and version", "The switch in Settings").

Run from sdks/python:  python -m unittest discover -s tests -v
"""

from __future__ import annotations

import logging
import os
import sys
import unittest
from types import SimpleNamespace

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "src"))

from visionary import _system as S  # noqa: E402
from visionary._agent import default_instance  # noqa: E402
from visionary._storage import Backend  # noqa: E402

quiet = logging.getLogger("visionary.test")
quiet.addHandler(logging.NullHandler())
quiet.propagate = False


class FakeBackend(Backend):
    dialect = "sqlite"

    def __init__(self):
        self.err = None
        self.value = None
        self.names = []

    def read_setting(self, name):
        self.names.append(name)
        if self.err is not None:
            raise self.err
        return self.value


class DefaultInstanceTest(unittest.TestCase):
    def test_default_instance(self):
        cases = [
            ("web-1", "api", "web-1"),
            ("a1b2c3d4e5f6", "api", "api"),
            ("a1b2c3d4e5f6", "", "python-app"),
            ("ab12" * 16, "", "python-app"),
            ("", "api", "api"),
            ("A1B2C3D4E5F6", "", "A1B2C3D4E5F6"),
            ("a1b2c3d4e5f", "", "a1b2c3d4e5f"),
            ("deadbeefcafe-2", "api", "deadbeefcafe-2"),
        ]
        for host, service, want in cases:
            self.assertEqual(default_instance(host, service), want, (host, service))


class SwitchTest(unittest.TestCase):
    def test_switch(self):
        b = FakeBackend()
        b.err = RuntimeError("no such table: visionary_settings")
        cfg = SimpleNamespace(system_interval=15.0, db_stats=None, disk_path="", service="api", version="")
        r = S.Recorder(cfg, "i", b, S.HttpCounters(), quiet)
        r._prime()

        r.read_switch()
        self.assertEqual(b.names, [S.SETTING])
        self.assertFalse(r.off, "no table: on")
        r._buf.append(r.sample())
        b.err, b.value = None, " FALSE "
        r.read_switch()
        self.assertTrue(r.off)
        self.assertEqual(r._buf, [])
        b.err = RuntimeError("database is locked")
        r.read_switch()
        self.assertTrue(r.off, "read error: stays off")
        b.err, b.value = None, None
        r.read_switch()
        self.assertFalse(r.off, "row deleted: on")
        b.value = "false"
        r.read_switch()
        self.assertTrue(r.off)
        b.value = "true"
        r.read_switch()
        self.assertFalse(r.off, "true: on")


if __name__ == "__main__":
    unittest.main()
