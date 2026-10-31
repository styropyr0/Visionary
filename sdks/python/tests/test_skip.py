"""Tests for the request skip list (SPEC "Skip list").

Run from sdks/python:  python -m unittest discover -s tests -v
"""

from __future__ import annotations

import json
import logging
import os
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "src"))

from visionary import _skip as S  # noqa: E402
from visionary._agent import Agent  # noqa: E402
from visionary._config import Config  # noqa: E402
from visionary._storage import Backend  # noqa: E402

quiet = logging.getLogger("visionary.test")
quiet.addHandler(logging.NullHandler())
quiet.propagate = False


class FakeBackend(Backend):
    dialect = "sqlite"

    def __init__(self, rows=None):
        self.rows = rows
        self.prefixes = []

    def read_settings(self, prefix="profile"):
        self.prefixes.append(prefix)
        if self.rows is None:
            raise RuntimeError("no such table: visionary_settings")
        return self.rows


class ParseTest(unittest.TestCase):
    def test_parse(self):
        self.assertEqual(S.parse('[" /health ", "", "  ", "/static/*"]'), ["/health", "/static/*"])
        for bad in ("", "nope", "{}", '"/health"', "null", '["/a", 1]'):
            self.assertEqual(S.parse(bad), [], bad)


class MatchTest(unittest.TestCase):
    def test_match(self):
        lst = ["/health", "/static/*"]
        self.assertTrue(S.match(lst, "/health"))
        self.assertFalse(S.match(lst, "/health/"))
        self.assertFalse(S.match(lst, "/Health"))
        self.assertTrue(S.match(lst, "/static/app.js"))
        self.assertTrue(S.match(lst, "/static/"))
        self.assertFalse(S.match(lst, "/static"))
        self.assertTrue(S.match(["*"], "/anything"))
        self.assertFalse(S.match([], "/health"))


class PollTest(unittest.TestCase):
    def test_failed_read_keeps_last_list(self):
        b = FakeBackend([("requests.other", "x"), ("requests.skip", json.dumps(["/health"]))])
        s = S.SkipList(b, quiet)
        s.poll()
        self.assertEqual(b.prefixes, ["requests"])
        self.assertTrue(s.skipped("/health"))
        b.rows = None
        s.poll()
        self.assertTrue(s.skipped("/health"))
        b.rows = [("requests.skip", "not json")]
        s.poll()
        self.assertFalse(s.skipped("/health"))

    def test_no_table_skips_nothing(self):
        s = S.SkipList(FakeBackend(), quiet)
        s.poll()
        self.assertFalse(s.skipped("/health"))


class AgentTest(unittest.TestCase):
    def test_should_log(self):
        b = FakeBackend([("requests.skip", json.dumps(["/api/health", "/api/static/*"]))])
        agent = Agent(Config(system_metrics=False, profile=False, logs=False, path_prefix="/api"), backend=b)
        agent._skip.poll()
        self.assertTrue(agent.should_log("/api/orders"))
        self.assertFalse(agent.should_log("/api/health"))
        self.assertFalse(agent.should_log("/api/static/app.js"))
        self.assertFalse(agent.should_log("/other"))

    def test_off_without_request_logging(self):
        agent = Agent(Config(log_requests=False, system_metrics=False, profile=False, logs=False),
                      backend=FakeBackend([]))
        self.assertIsNone(agent._skip)


if __name__ == "__main__":
    unittest.main()
