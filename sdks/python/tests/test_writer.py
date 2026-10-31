from __future__ import annotations

import logging
import os
import shutil
import sys
import tempfile
import threading
import time
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "src"))

from visionary import Config  # noqa: E402
from visionary import _logs as L  # noqa: E402
from visionary._storage import (  # noqa: E402
    LOG_COLUMNS, MAX_PARAMS, TABLES, D1Backend, SQLBackend, insert_rows_sql, insert_statements, open_backend,
)
from visionary._writer import RequestWriter, WriterStats  # noqa: E402

quiet = logging.getLogger("visionary.test.writer")
quiet.addHandler(logging.NullHandler())
quiet.propagate = False


def log_row(i, attrs=None):
    return ("2026-01-01T00:00:00.000Z", "api", "i-1", None, "python", None, None, "info", 9, None,
            "line %d" % i, attrs, None, None, None, None, None, None, None, None, 6)


class StatementTest(unittest.TestCase):
    def test_one_statement_when_it_fits(self):
        rows = [log_row(i) for i in range(1200)]
        stmts = insert_statements("app_logs", LOG_COLUMNS, rows, "?")
        self.assertEqual(len(stmts), 1)
        sql, params, n = stmts[0]
        self.assertEqual(n, 1200)
        self.assertEqual(len(params), 1200 * len(LOG_COLUMNS))
        self.assertEqual(sql.count("?"), len(params))

    def test_split_at_the_parameter_limit(self):
        rows = [log_row(i) for i in range(3000)]
        stmts = insert_statements("app_logs", LOG_COLUMNS, rows, "%s")
        per = MAX_PARAMS // len(LOG_COLUMNS)
        self.assertEqual([n for _, _, n in stmts], [per, per, 3000 - 2 * per])
        for sql, params, n in stmts:
            self.assertLessEqual(len(params), MAX_PARAMS)
            self.assertEqual(sql.count("%s"), len(params))
        self.assertEqual(stmts[0][1][:len(LOG_COLUMNS)], list(rows[0]))

    def test_numbered_placeholders(self):
        self.assertEqual(insert_rows_sql("t", ("a", "b"), 2, "$"), "INSERT INTO t (a, b) VALUES ($1, $2), ($3, $4)")
        self.assertEqual(insert_rows_sql("t", ("a", "b"), 2, "?"), "INSERT INTO t (a, b) VALUES (?, ?), (?, ?)")


class SQLiteTest(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.url = "sqlite:///" + os.path.join(self.dir, "v.db").replace("\\", "/")
        self.backend = open_backend(Config(database_url=self.url))

    def tearDown(self):
        self.backend.close()
        shutil.rmtree(self.dir, ignore_errors=True)

    def query(self, sql):
        def read(conn):
            return conn.execute(sql).fetchall()

        return self.backend._run(read)

    def test_wal_and_busy_timeout(self):
        self.assertEqual(self.query("PRAGMA journal_mode")[0][0], "wal")
        self.assertEqual(self.query("PRAGMA busy_timeout")[0][0], 5000)

    def test_rows_split_and_nulls(self):
        self.backend.create_schema("logs")
        self.backend._max_params = 100
        rows = [log_row(i, '{"k":1}' if i % 2 else None) for i in range(25)]
        self.backend.insert_many([("logs", rows)])
        self.assertEqual(self.query("SELECT count(*) FROM app_logs")[0][0], 25)
        self.assertEqual(self.query("SELECT count(*) FROM app_logs WHERE attrs IS NULL AND version IS NULL")[0][0], 13)
        self.assertEqual(self.query("SELECT message FROM app_logs ORDER BY id DESC LIMIT 1")[0][0], "line 24")


class FakeD1:
    def __init__(self):
        self.calls = []

    def batch(self, statements):
        self.calls.append(statements)


class D1Test(unittest.TestCase):
    def test_one_insert_per_row_with_string_params(self):
        client = FakeD1()
        D1Backend(client).insert_many([("logs", [log_row(1), log_row(2)])])
        self.assertEqual(len(client.calls), 1)
        stmts = client.calls[0]
        self.assertEqual(len(stmts), 2)
        sql, params = stmts[0]
        self.assertTrue(sql.startswith("INSERT INTO app_logs ("))
        self.assertIn("NULLIF(?, '')", sql)
        self.assertEqual(sql.count("?"), len(LOG_COLUMNS))
        self.assertEqual(params[3], "")
        self.assertEqual(params[10], "line 1")

    def test_batches_stay_at_fifty(self):
        w = L.LogWriter(Config(batch_size=500), D1Backend(FakeD1()), quiet, "i-1")
        self.assertEqual((w._batch_size, w._flushers), (50, 1))


class FakeBackend:
    def __init__(self, dialect, delay=0.0, fail=False):
        self.dialect = dialect
        self.delay = delay
        self.fail = fail
        self.rows = 0
        self.busy = 0
        self.most = 0
        self.lock = threading.Lock()

    def create_schema(self, table):
        pass

    def insert_many(self, groups):
        with self.lock:
            self.busy += 1
            self.most = max(self.most, self.busy)
        time.sleep(self.delay)
        with self.lock:
            self.busy -= 1
            if self.fail:
                raise RuntimeError("down")
            self.rows += sum(len(r) for _, r in groups)


class WriterTest(unittest.TestCase):
    def line(self, i):
        return L.LogLine(time.time(), L.INFO, "line %d" % i)

    def test_batch_sizes(self):
        self.assertEqual(L.LogWriter(Config(), FakeBackend("sqlite"), quiet, "i")._batch_size, 500)
        self.assertEqual(L.LogWriter(Config(batch_size=2000), FakeBackend("sqlite"), quiet, "i")._batch_size, 2000)
        self.assertEqual(L.LogWriter(Config(batch_size=5000), FakeBackend("sqlite"), quiet, "i")._batch_size, 500)
        self.assertEqual(L.LogWriter(Config(), FakeBackend("sqlite"), quiet, "i")._flushers, 1)
        self.assertEqual(L.LogWriter(Config(), FakeBackend("postgres"), quiet, "i")._flushers, 4)

    def test_parallel_batches_on_postgres(self):
        backend = FakeBackend("postgres", delay=0.05)
        w = L.LogWriter(Config(batch_size=10), backend, quiet, "i-1", queue_size=10000)
        w.start()
        for i in range(400):
            w.log(self.line(i))
        self.assertTrue(w.close(10))
        self.assertEqual(backend.rows, 400)
        self.assertGreater(backend.most, 1)
        self.assertLessEqual(backend.most, 4)
        self.assertEqual(w.stats(), WriterStats(0, 400, 0))

    def test_stats_count_drops(self):
        w = L.LogWriter(Config(), FakeBackend("sqlite"), quiet, "i-1", queue_size=10)
        for i in range(15):
            w.log(self.line(i))
        self.assertEqual(w.stats(), WriterStats(10, 0, 5))
        w.start()
        self.assertTrue(w.close(10))
        self.assertEqual(w.stats(), WriterStats(0, 10, 5))

    def test_failed_batch_counts_as_dropped(self):
        w = L.LogWriter(Config(), FakeBackend("sqlite", fail=True), quiet, "i-1")
        sleep, time.sleep = time.sleep, lambda s: None
        try:
            w._write([self.line(1), self.line(2)])
        finally:
            time.sleep = sleep
        self.assertEqual(w.stats(), WriterStats(0, 0, 2))

    def test_agent_sums_writers(self):
        from visionary import Agent

        a = Agent(Config(system_metrics=False, profile=False), backend=FakeBackend("sqlite"))
        a._requests.log(object())
        a._logs.writer.log(self.line(1))
        self.assertEqual(a.writer_stats(), WriterStats(2, 0, 0))


class TableTest(unittest.TestCase):
    def test_request_writer_rows_fit_tables(self):
        w = RequestWriter(Config(), FakeBackend("sqlite"), quiet, "i-1")
        for table, rows in w._rows([]):
            self.assertIn(table, TABLES)


if __name__ == "__main__":
    unittest.main()
