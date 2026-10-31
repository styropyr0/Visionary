from __future__ import annotations

import asyncio
import http.server
import logging
import os
import sqlite3
import sys
import tempfile
import threading
import time
import unittest
from contextlib import closing
from types import SimpleNamespace

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "src"))

import visionary  # noqa: E402
from visionary import _context, _http, _spans, _tracecontext  # noqa: E402
from visionary._agent import Agent  # noqa: E402
from visionary._config import Config  # noqa: E402
from visionary._storage import SQLBackend  # noqa: E402
from visionary._tracecontext import parse_traceparent, request_span  # noqa: E402

TRACE = "4bf92f3577b34da6a3ce929d0e0e4736"
PARENT = "00f067aa0ba902b7"
HEADER = "00-%s-%s-01" % (TRACE, PARENT)

try:
    import requests  # noqa: F401
except ImportError:
    requests = None


class FakeAgent:
    def __init__(self) -> None:
        self.config = SimpleNamespace(span_threshold_ms=0.0, span_max=100)
        self.spans_on = True
        self.recorded = []

    def record_span(self, span):
        self.recorded.append(span)


class ParseTest(unittest.TestCase):
    def test_valid(self):
        sc = parse_traceparent(HEADER)
        self.assertEqual((sc.trace_id, sc.span_id, sc.flags), (TRACE, PARENT, "01"))
        sc = parse_traceparent("01-%s-%s-00-extra" % (TRACE, PARENT))
        self.assertEqual((sc.trace_id, sc.flags), (TRACE, "00"))

    def test_invalid(self):
        for bad in ["", "garbage", HEADER + "-x", "ff-%s-%s-01" % (TRACE, PARENT),
                    "00-%s-%s-01" % ("0" * 32, PARENT), "00-%s-%s-01" % (TRACE, "0" * 16),
                    "00-%s-%s-01" % (TRACE.upper(), PARENT), "00-%s-%s-1" % (TRACE, PARENT),
                    "00-%s-%s-01" % (TRACE[:31], PARENT), "0-%s-%s-01" % (TRACE, PARENT),
                    "00-%s-%s-0g" % (TRACE, PARENT)]:
            self.assertIsNone(parse_traceparent(bad), bad)

    def test_request_continues_or_starts(self):
        sc, parent = request_span(HEADER, "k=v")
        self.assertEqual((sc.trace_id, parent, sc.flags, sc.state), (TRACE, PARENT, "01", "k=v"))
        self.assertEqual(len(sc.span_id), 16)
        self.assertNotEqual(sc.span_id, PARENT)
        sc, parent = request_span("bogus", "k=v")
        self.assertEqual((parent, sc.flags, sc.state, len(sc.trace_id)), ("", "01", "", 32))
        self.assertNotEqual(sc.trace_id, TRACE)

    def test_ids(self):
        ids = {_tracecontext.new_span_id() for _ in range(100)}
        self.assertEqual(len(ids), 100)
        for i in ids:
            self.assertEqual(len(i), 16)
            self.assertTrue(i.strip("0"))


class ParentageTest(unittest.TestCase):
    def setUp(self):
        self.agent = FakeAgent()
        _spans.set_active(self.agent)
        self.addCleanup(_spans.clear_active, self.agent)

    def test_request_custom_http(self):
        trace = _spans.Trace("req-1", time.perf_counter(), 100)
        ann, token = _context.begin(trace, None, HEADER, "k=v")
        try:
            with visionary.span("custom", "build report"):
                http = _spans.start("http", "GET api")
                headers = _http._trace_headers(http, {})
                http.finish()
            db = _spans.start("db", "SELECT 1")
            db.finish()
        finally:
            _context.end(token)
        spans = {s.name: s for s in trace.close()}
        custom, http, db = spans["build report"], spans["GET api"], spans["SELECT 1"]
        self.assertEqual(ann.parent_span_id, PARENT)
        self.assertEqual(ann.trace_id, TRACE)
        for s in (custom, http, db):
            self.assertEqual(s.trace_id, TRACE)
        self.assertEqual(custom.parent_span_id, ann.span.span_id)
        self.assertEqual(http.parent_span_id, custom.span_id)
        self.assertEqual(db.parent_span_id, ann.span.span_id)
        self.assertEqual(headers, {"traceparent": "00-%s-%s-01" % (TRACE, http.span_id), "tracestate": "k=v"})
        self.assertIsNone(_tracecontext.current_span())

    def test_outside_a_trace_starts_one(self):
        s = _spans.start("http", "GET api")
        self.assertIsNone(s.parent_span_id)
        self.assertEqual(len(s.trace_id), 32)
        self.assertEqual(_http._trace_headers(s, {})["traceparent"], "00-%s-%s-01" % (s.trace_id, s.span_id))

    def test_app_traceparent_is_kept(self):
        s = _spans.start("http", "GET api")
        self.assertIsNone(_http._trace_headers(s, {"TraceParent": "x"}))

    def test_spans_off_still_propagate(self):
        _spans.clear_active(self.agent)
        ann, token = _context.begin(None, None, HEADER)
        try:
            self.assertIsNone(_spans.start("http", "GET api"))
            got = _http._trace_headers(None, {})
        finally:
            _context.end(token)
        self.assertEqual(got, {"traceparent": "00-%s-%s-01" % (TRACE, ann.span.span_id)})
        self.assertIsNone(_http._trace_headers(None, {}))

    def test_background_keeps_the_trace(self):
        trace = _spans.Trace("req-2", time.perf_counter(), 100)
        ann, token = _context.begin(trace, None, HEADER)
        inner = []
        try:
            t = visionary.background("send", lambda: inner.append(_spans.start("db", "INSERT mails")))
        finally:
            _context.end(token)
        t.join()
        inner[0].finish()
        spans = {s.name: s for s in trace.close() + self.agent.recorded}
        bg = spans["send"]
        self.assertEqual(bg.trace_id, TRACE)
        self.assertEqual(bg.parent_span_id, ann.span.span_id)
        self.assertEqual(inner[0].trace_id, TRACE)
        self.assertEqual(inner[0].parent_span_id, bg.span_id)


class _Echo(http.server.BaseHTTPRequestHandler):
    seen = []

    def do_GET(self):
        self.seen.append(self.headers.get("traceparent"))
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b"ok")

    def log_message(self, *args):
        pass


class AgentTest(unittest.TestCase):
    @unittest.skipIf(requests is None, "needs requests")
    def test_rows_carry_the_trace(self):
        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), _Echo)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        self.addCleanup(server.shutdown)
        url = "http://127.0.0.1:%d/x" % server.server_address[1]
        quiet = logging.getLogger("visionary")
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "v.db")
            cfg = Config(connection_factory=lambda: sqlite3.connect(path, timeout=10, check_same_thread=False),
                         dialect="sqlite", system_metrics=False, profile=False, logs=False, metrics=False,
                         instrument_http=True, span_threshold_ms=0)
            level = quiet.level
            quiet.setLevel(logging.WARNING)
            agent = Agent(cfg).start()

            from visionary.integrations.asgi import VisionaryASGIMiddleware

            async def app(scope, receive, send):
                with visionary.span("custom", "call out"):
                    requests.get(url, timeout=5)
                await send({"type": "http.response.start", "status": 200, "headers": []})
                await send({"type": "http.response.body", "body": b"x"})

            async def receive():
                return {"type": "http.request", "body": b""}

            async def send(message):
                pass

            scope = {"type": "http", "method": "GET", "path": "/a", "headers": [(b"traceparent", HEADER.encode())]}
            asyncio.run(VisionaryASGIMiddleware(app, agent)(scope, receive, send))
            agent.close()
            quiet.setLevel(level)
            with closing(sqlite3.connect(path)) as conn:
                req = conn.execute("SELECT trace_id, span_id, parent_span_id FROM api_logs").fetchone()
                spans = {r[0]: r[1:] for r in conn.execute(
                    "SELECT kind, trace_id, span_id, parent_span_id FROM spans").fetchall()}
                idx = {r[0] for r in conn.execute("SELECT name FROM sqlite_master WHERE type = 'index'")}
        self.assertEqual((req[0], req[2]), (TRACE, PARENT))
        self.assertEqual(spans["custom"], (TRACE, spans["custom"][1], req[1]))
        self.assertEqual(spans["http"][2], spans["custom"][1])
        self.assertEqual(_Echo.seen[-1], "00-%s-%s-01" % (TRACE, spans["http"][1]))
        self.assertTrue({"idx_api_logs_trace_id", "idx_spans_trace_id"} <= idx)


class MigrationTest(unittest.TestCase):
    def test_spans_table_gains_trace_columns(self):
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "v.db")
            b = SQLBackend("sqlite", lambda: sqlite3.connect(path))
            b.create_schema("telemetry")
            b.create_schema("telemetry")
            b.close()
            with closing(sqlite3.connect(path)) as conn:
                cols = {r[1] for r in conn.execute("PRAGMA table_info(spans)")}
                idx = {r[0] for r in conn.execute("SELECT name FROM sqlite_master WHERE type = 'index'")}
        self.assertTrue({"trace_id", "span_id", "parent_span_id"} <= cols)
        self.assertIn("idx_spans_trace_id", idx)


if __name__ == "__main__":
    unittest.main()
