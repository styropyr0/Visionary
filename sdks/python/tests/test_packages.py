"""Tests for package inventory (SPEC "Packages")

Run from sdks/python:  python -m unittest discover -s tests -v
"""

from __future__ import annotations

import logging
import os
import sqlite3
import sys
import tempfile
import unittest
from contextlib import closing
from unittest import mock

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "src"))

from visionary import _packages as P  # noqa: E402
from visionary._config import from_env  # noqa: E402
from visionary._storage import SQLBackend  # noqa: E402

quiet = logging.getLogger("visionary.test")
quiet.addHandler(logging.NullHandler())
quiet.propagate = False


class _Dist:
    def __init__(self, name, version):
        self.metadata = {"Name": name}
        self.version = version


class PackagesTest(unittest.TestCase):
    def test_rows_skip_empty_and_duplicates(self):
        rows = P.package_rows([("requests", "2.32.3"), ("requests", "2.32.3"), ("broken", ""), ("", "1.0")],
                              "2026-01-01T00:00:00.000Z", None, "i", "abc")
        self.assertEqual(rows, [("2026-01-01T00:00:00.000Z", None, "i", "abc", "PyPI", "requests", "2.32.3")])

    def test_rows_cap(self):
        many = [("p%d" % i, "1.0") for i in range(6000)]
        self.assertEqual(len(P.package_rows(many, "", None, "i", None)), P.MAX_PACKAGES)

    def test_distributions_read_metadata(self):
        with mock.patch.object(P.metadata, "distributions", return_value=[_Dist("Flask", "3.0.3"), _Dist(None, "1")]):
            self.assertEqual(P.distributions(), [("Flask", "3.0.3"), ("", "1")])

    def test_run_writes_one_report(self):
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "v.db")
            backend = SQLBackend("sqlite", lambda: sqlite3.connect(path))
            report = P.PackageReport(backend, quiet, "api", "i", "")
            fake = [_Dist("Flask", "3.0.3"), _Dist("Werkzeug", "3.0.4")]
            with mock.patch.object(P.metadata, "distributions", return_value=fake):
                report.run()
            backend.close()
            with closing(sqlite3.connect(path)) as db:
                got = db.execute("SELECT service, instance, version, ecosystem, name, pkg_version, created_at "
                                 "FROM app_packages ORDER BY name").fetchall()
            self.assertEqual([r[:6] for r in got], [("api", "i", None, "PyPI", "Flask", "3.0.3"),
                                                    ("api", "i", None, "PyPI", "Werkzeug", "3.0.4")])
            self.assertEqual(len({r[6] for r in got}), 1)

    def test_env_turns_it_off(self):
        self.assertTrue(from_env({}).packages)
        self.assertFalse(from_env({"VISIONARY_PACKAGES": "false"}).packages)


if __name__ == "__main__":
    unittest.main()
