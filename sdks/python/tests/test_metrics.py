"""Tests for custom metrics (SPEC "Custom metrics").

Run from sdks/python:  python -m unittest discover -s tests -v
"""

from __future__ import annotations

import os
import sqlite3
import sys
import tempfile
import unittest
from contextlib import closing

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "src"))

import visionary  # noqa: E402
from visionary import _metrics as M  # noqa: E402
from visionary._agent import Agent  # noqa: E402
from visionary._config import Config, from_env  # noqa: E402
from visionary._storage import METRIC_COLUMNS, TABLES  # noqa: E402


class MetricsTestCase(unittest.TestCase):
    def setUp(self):
        saved = M.registry
        M.registry = M.Registry()

        def restore():
            M.registry = saved

        self.addCleanup(restore)

    def collect(self):
        rows, dropped = M.registry.collect(0)
        return {(r.name, r.kind, r.labels): r for r in rows}, dropped


class SeriesTest(unittest.TestCase):
    def test_reference_series(self):
        self.assertEqual(M.series_id("orders_placed", {"region": "eu"}), ('{"region":"eu"}', "da4f5c6244c4f3e4"))

    def test_canonical_labels(self):
        text, _ = M.series_id("m", {"b": "1", "a": 'q"\\\n<é>', " ": "x", " c ": ""})
        self.assertEqual(text, '{"a":"q\\"\\\\\\u000a<é>","b":"1","c":""}')
        self.assertEqual(M.series_id("m", {})[0], "{}")
        many = {"k%02d" % i: "v" for i in range(30)}
        text, _ = M.series_id("m", many)
        self.assertEqual(text.count(":"), 20)
        self.assertIn('"k19"', text)
        self.assertNotIn('"k20"', text)

    def test_limits(self):
        text, _ = M.series_id("m", {"k" * 150: "é" * 150})
        key, value = text[1:-1].split(":")
        self.assertEqual(len(key.strip('"').encode()), 100)
        self.assertEqual(len(value.strip('"').encode()), 200)
        self.assertEqual(len(M.clean_name("  " + "x" * 300).encode()), 200)

    def test_number_format(self):
        self.assertEqual(M.encode_buckets([0.0, 2.5, 1e21, 1e-7, 0.00001], [1, 2, 3, 4, 5, 6]),
                         '{"bounds":[0,2.5,1e+21,1e-7,0.00001],"counts":[1,2,3,4,5,6]}')


class AggregateTest(MetricsTestCase):
    def test_counter(self):
        c = visionary.counter("orders", region="eu")
        c.add(2)
        c.inc()
        for bad in (0, -1, float("nan"), float("inf"), "x"):
            c.add(bad)
        visionary.counter("orders", {"region": "eu"}).add(1)
        visionary.counter("  ").add(1)
        rows, _ = self.collect()
        self.assertEqual(rows[("orders", "counter", '{"region":"eu"}')].value, 4.0)
        self.assertEqual(len(rows), 1)
        rows, _ = self.collect()
        self.assertEqual(rows, {})

    def test_gauge(self):
        g = visionary.gauge("depth")
        g.add(3)
        g.add(-1)
        g.set(float("nan"))
        rows, _ = self.collect()
        self.assertEqual(rows[("depth", "gauge", "{}")].value, 2.0)
        rows, _ = self.collect()
        self.assertEqual(rows[("depth", "gauge", "{}")].value, 2.0)

    def test_kinds_are_separate(self):
        visionary.counter("x").add(1)
        visionary.gauge("x").set(5)
        rows, _ = self.collect()
        self.assertEqual({k[1] for k in rows}, {"counter", "gauge"})

    def test_histogram(self):
        h = visionary.histogram("took_ms")
        for v in (0, 5, 6, 20000, float("inf")):
            h.observe(v)
        r = self.collect()[0][("took_ms", "histogram", "{}")]
        self.assertEqual(r.histogram[:4], (4, 20011.0, 0.0, 20000.0))
        self.assertEqual(r.value, 20011.0 / 4)
        self.assertEqual(r.histogram[4], '{"bounds":[0,5,10,25,50,75,100,250,500,750,1000,2500,5000,7500,10000],'
                                         '"counts":[1,1,1,0,0,0,0,0,0,0,0,0,0,0,0,1]}')
        self.assertEqual(self.collect()[0], {})

    def test_describe(self):
        visionary.describe_metric("size", unit="bytes", buckets=[10, 1, 10])
        visionary.histogram("size").observe(3)
        r = self.collect()[0][("size", "histogram", "{}")]
        self.assertEqual(r.unit, "bytes")
        self.assertEqual(r.histogram[4], '{"bounds":[1,10],"counts":[0,1,0]}')

    def test_series_cap(self):
        M.registry.max_series = 2
        for i in range(5):
            visionary.counter("by_user", user=str(i)).add(1)
        visionary.counter("by_user", user="0").add(1)
        rows, dropped = self.collect()
        self.assertEqual(len(rows), 2)
        self.assertEqual(dropped, {"by_user": 3})
        self.assertEqual(self.collect()[1], {})


class EnvTest(unittest.TestCase):
    def test_env(self):
        cfg = from_env({"VISIONARY_METRICS": "false", "VISIONARY_METRICS_INTERVAL": "1m",
                        "VISIONARY_METRICS_MAX_SERIES": "7"})
        self.assertEqual((cfg.metrics, cfg.metrics_interval, cfg.metrics_max_series), (False, 60.0, 7))
        cfg = from_env({"VISIONARY_METRICS_INTERVAL": "5"})
        self.assertEqual((cfg.metrics, cfg.metrics_interval, cfg.metrics_max_series), (True, 5.0, 1000))


class AgentTest(MetricsTestCase):
    def test_rows_reach_metrics(self):
        self.assertEqual(TABLES["metrics"], ("metrics", METRIC_COLUMNS))
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "v.db")
            cfg = Config(connection_factory=lambda: sqlite3.connect(path, timeout=10, check_same_thread=False),
                         dialect="sqlite", system_metrics=False, profile=False, logs=False, log_requests=False,
                         spans=False, errors=False, service="shop", instance="i1", metrics_interval=3600)
            agent = Agent(cfg).start()
            with closing(sqlite3.connect(path)) as conn:
                none = conn.execute("SELECT name FROM sqlite_master WHERE name = 'metrics'").fetchall()
            self.assertEqual(none, [])
            visionary.counter("orders_placed", region="eu").add(3)
            visionary.histogram("took_ms").observe(7)
            agent.close()
            with closing(sqlite3.connect(path)) as conn:
                rows = conn.execute("SELECT service, instance, name, kind, labels, series, value, count, buckets "
                                    "FROM metrics ORDER BY name").fetchall()
        self.assertEqual(rows[0], ("shop", "i1", "orders_placed", "counter", '{"region":"eu"}', "da4f5c6244c4f3e4",
                                   3.0, None, None))
        self.assertEqual(rows[1][2:4] + rows[1][6:8], ("took_ms", "histogram", 7.0, 1))
        self.assertTrue(rows[1][8].startswith('{"bounds":[0,5,10,'))


if __name__ == "__main__":
    unittest.main()
