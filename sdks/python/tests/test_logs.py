"""Tests for application logs (SPEC "Application logs").

Run from sdks/python:  python -m unittest discover -s tests -v
"""

from __future__ import annotations

import asyncio
import json
import logging
import os
import sqlite3
import sys
import tempfile
import time
import unittest
from contextlib import closing
from datetime import datetime, timedelta, timezone
from types import SimpleNamespace

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "src"))

import visionary  # noqa: E402
from visionary import _context  # noqa: E402
from visionary import _logs as L  # noqa: E402
from visionary._agent import Agent  # noqa: E402
from visionary._config import Config, from_env  # noqa: E402
from visionary._loghandler import LogHandler, _KeepOutput, capture_logging, line_from_record  # noqa: E402
from visionary._storage import LOG_COLUMNS, SQLBackend, TABLES  # noqa: E402

quiet = logging.getLogger("visionary.test")
quiet.addHandler(logging.NullHandler())
quiet.propagate = False


def ts(delta: timedelta) -> str:
    return (datetime.now(timezone.utc) + delta).strftime("%Y-%m-%dT%H:%M:%S.000Z")


class FakeWriter:
    def __init__(self):
        self.lines = []

    def log(self, line):
        self.lines.append(line)


class FakeBackend:
    def __init__(self, rows=None):
        self.rows = rows

    def read_settings(self, prefix="profile"):
        if self.rows is None:
            raise RuntimeError("no such table: visionary_settings")
        return self.rows


def pipeline(rows=None, service="api"):
    """A pipeline with settings read from rows, queuing on a FakeWriter."""
    p = L.LogPipeline(FakeBackend(rows), FakeWriter(), quiet, service)
    if rows is not None:
        p.poll()
    return p


class LogTestCase(unittest.TestCase):
    """Logs through a real logging.Logger whose only handler is a LogHandler
    bound to a pipeline."""

    def setUp(self):
        self.p = pipeline(self.rows())
        self.log = logging.getLogger("app.test.%s" % id(self))
        self.log.handlers = [LogHandler(agent=SimpleNamespace(logs=self.p))]
        self.log.setLevel(1)
        self.log.propagate = False

    def rows(self):
        return None

    def queued(self):
        out, self.p.writer.lines = self.p.writer.lines, []
        return out

    def messages(self):
        lines = sorted(self.queued(), key=lambda l: l.created_at)
        return ",".join(l.message for l in lines)

    def serve(self, route, status=200, took=0.0, user="", log=None):
        """One request: log inside it, then end it the way an integration does."""
        info = _context.RequestInfo("req-1", "GET", route)
        ann, token = _context.begin(None, info, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
        try:
            if user:
                visionary.set_user(user)
            (log or self.three_lines)()
        finally:
            _context.end(token)
        self.p.finish(ann, route, status >= 500 or ann.failed, took, "")
        return ann

    def three_lines(self):
        self.log.debug("debug line")
        time.sleep(0.002)
        self.log.info("info line")
        time.sleep(0.002)
        self.log.warning("warn line")


class SettingsTest(unittest.TestCase):
    def test_defaults_and_parsing(self):
        until = ts(timedelta(hours=1))
        past = ts(timedelta(hours=-1))
        ov = json.dumps([
            {"route": "/checkout", "level": "debug", "until": until},
            {"user": "42", "level": "trace"},
            {"route": "/health", "level": "warn"},
            {"route": "/admin/*", "level": "debug"},
            {"route": "/old", "level": "debug", "until": past},
            {"service": "other", "level": "debug"},
            {"level": "nonsense"},
            {"route": "/bad", "level": "debug", "until": "tomorrow"},
        ])
        s = L.parse_log_settings([
            ("logs.level", "warning"), ("logs.overrides", ov), ("logs.rate", "-1"),
            ("logs.slow_ms", "250"), ("logs.buffer", "nope"), ("logs.enabled", "false"),
        ], 0)
        self.assertEqual(s.level, L.WARN)
        self.assertEqual(s.rate, 6000)
        self.assertAlmostEqual(s.slow_s, 0.25)
        self.assertTrue(s.buffer)
        self.assertFalse(s.enabled)
        self.assertEqual(len(s.overrides), 6)
        now = time.time()
        for route, user, want in [
            ("/checkout", "", L.DEBUG), ("/health", "", L.WARN), ("/admin/users", "", L.DEBUG),
            ("/old", "", L.WARN), ("/any", "42", L.TRACE), ("/health", "42", L.TRACE), ("/any", "", L.WARN),
        ]:
            self.assertEqual(s.floor("api", route, user, now), want, (route, user))
        self.assertEqual(s.floor("other", "", "", now), L.DEBUG)
        self.assertEqual(s.hold_below("api", now), L.WARN)

        d = L.LogSettings()
        self.assertEqual((d.enabled, d.level, d.buffer, d.buffer_level, d.slow_s, d.rate),
                         (True, L.INFO, True, L.DEBUG, 1.0, 6000))
        self.assertEqual(L.parse_log_settings([], L.WARN).level, L.WARN)
        self.assertEqual(L.parse_log_settings([("logs.overrides", "not json"), ("logs.drop", "{}")]).overrides, [])

    def test_failed_read_keeps_previous(self):
        p = pipeline([("logs.level", "error")])
        self.assertEqual(p.settings.level, L.ERROR)
        p._backend.rows = None
        p.poll()
        self.assertEqual(p.settings.level, L.ERROR)

    def test_env(self):
        cfg = from_env({"VISIONARY_LOGS": "false", "VISIONARY_LOG_LEVEL": "debug"})
        self.assertFalse(cfg.logs)
        self.assertEqual(cfg.log_level, "debug")
        self.assertTrue(from_env({}).logs)
        self.assertEqual(from_env({}).log_level, "info")


class DropTest(unittest.TestCase):
    def test_dropped(self):
        drops = json.dumps([{"contains": "healthcheck"}, {"logger": "noisy", "level": "info"},
                            {"service": "web", "level": "debug"}, {}])
        s = L.parse_log_settings([("logs.drop", drops)])
        self.assertEqual(len(s.drops), 3)
        for service, logger, msg, sev, want in [
            ("api", "", "healthcheck ok", L.ERROR, True),
            ("api", "noisy", "x", L.INFO, True),
            ("api", "noisy", "x", L.WARN, False),
            ("web", "", "x", L.DEBUG, True),
            ("web", "", "x", L.INFO, False),
            ("api", "", "x", L.TRACE, False),
        ]:
            self.assertEqual(s.dropped(service, logger, msg, sev), want, (service, logger, msg, sev))


class LevelTest(unittest.TestCase):
    def test_parse_level(self):
        for name, want in {"WARNING": L.WARN, "crit": L.FATAL, "notice": L.INFO, "Severe": L.ERROR,
                           "": 0, "loud": 0}.items():
            self.assertEqual(L.parse_level(name), want, name)
        self.assertEqual(L.level_name(0), "info")
        self.assertEqual(L.level_name(L.FATAL), "fatal")

    def test_python_levels(self):
        for levelno, want in [(0, L.TRACE), (5, L.TRACE), (9, L.TRACE), (10, L.DEBUG), (15, L.DEBUG),
                              (20, L.INFO), (25, L.INFO), (30, L.WARN), (40, L.ERROR), (45, L.ERROR),
                              (50, L.FATAL), (60, L.FATAL)]:
            self.assertEqual(L.python_severity(levelno), want, levelno)

    def test_text_level(self):
        for line, want in {
            "ERROR something": L.ERROR,
            "2026-10-28 WARN disk": L.WARN,
            "level=debug msg=x": L.DEBUG,
            "[info] started": L.INFO,
            "time=x level=WARN msg=y": L.WARN,
            "INFO handled": L.INFO,
            "payment failed": 0,
            "user error rate is low but ok": 0,
            "error: connection refused": L.ERROR,
            "Error handling request": 0,
            "10:03:44 [ERROR] x": L.ERROR,
            "2026/10/28 10:03:44 FATAL x": L.FATAL,
            "<warning> x": L.WARN,
        }.items():
            self.assertEqual(L.text_level(line), want, line)

    def test_strip_level_word(self):
        for line, want in {
            "INFO started": "started",
            "WARNING: disk": "disk",
            "ERROR": "ERROR",
            "Error handling": "Error handling",
            "[warn] slow": "[warn] slow",
            "WARN": "WARN",
            "DEBUG   spaced x": "  spaced x",
        }.items():
            sev = L.text_level(line) or L.INFO
            self.assertEqual(L.strip_level_word(line, sev), want, line)


class SecretsTest(unittest.TestCase):
    def test_scrub(self):
        text = ('login password=hunter2 token: "abc def" Authorization: Bearer abcdefghijkl key '
                'AKIAABCDEFGHIJKLMNOP jwt eyJhbGciOiJ.eyJzdWIiOiIx.c2lnbmF0dXJl -----BEGIN RSA PRIVATE KEY-----\n'
                'MIIE\n-----END RSA PRIVATE KEY----- done')
        got = L.scrub(text)
        for leak in ("hunter2", "abc def", "abcdefghijkl", "AKIAABCDEFGHIJKLMNOP", "eyJhbGciOiJ", "MIIE"):
            self.assertNotIn(leak, got)
        self.assertIn("password=[REDACTED]", got)
        self.assertIn("Bearer [REDACTED]", got)
        self.assertTrue(got.endswith(" done"))
        self.assertEqual(L.scrub("plain text"), "plain text")

    def test_encode_attrs(self):
        s = L.encode_attrs({"user.password": "x", "n": 1, "note": "token=abc", "ssn": "1"}, L.secret_keys(["SSN"]))
        self.assertIn('"user.password":"[REDACTED]"', s)
        self.assertIn('"note":"token=[REDACTED]"', s)
        self.assertIn('"ssn":"[REDACTED]"', s)

    def test_attrs_truncation(self):
        big = {"k" * (i + 1): "v" * 2000 for i in range(40)}
        s = L.encode_attrs(big)
        self.assertLessEqual(len(s.encode()), L.MAX_ATTRS)
        decoded = json.loads(s)
        self.assertIs(decoded.pop("_truncated"), True)
        self.assertTrue(decoded)
        self.assertTrue(all(v == "v" * 1024 + "…" for v in decoded.values()))

        many = {"x" * 10 + "%05d" % i: "value" for i in range(2000)}
        s = L.encode_attrs(many)
        self.assertLessEqual(len(s.encode()), L.MAX_ATTRS)
        decoded = json.loads(s)
        self.assertIs(decoded["_truncated"], True)
        kept = sorted(k for k in decoded if k != "_truncated")
        self.assertEqual(kept, sorted(many)[:len(kept)])

    def test_cut(self):
        self.assertEqual(L._cut("héllo", 2), "h")
        self.assertEqual(L._cut("abc", 5), "abc")


class RequestLinesTest(LogTestCase):
    def test_debug_kept_only_for_failed_requests(self):
        self.serve("/ok")
        self.assertEqual(self.messages(), "info line,warn line")
        self.serve("/fail", status=500)
        got = self.queued()
        self.assertEqual(",".join(l.message for l in sorted(got, key=lambda l: l.created_at)),
                         "debug line,info line,warn line")
        for line in got:
            self.assertEqual(line.request_id, "req-1")
            self.assertEqual(line.trace_id, "4bf92f3577b34da6a3ce929d0e0e4736")

    def test_captured_error_counts_as_failed(self):
        def log():
            self.three_lines()
            try:
                raise ValueError("handled")
            except ValueError:
                visionary.capture_exception()

        self.serve("/handled", status=200, log=log)
        self.assertEqual(self.messages(), "debug line,info line,warn line")

    def test_more_than_500_held_drop_the_oldest(self):
        def log():
            for i in range(510):
                self.log.debug("d%d", i)

        ann = self.serve("/fail", status=500, log=log)
        got = self.queued()
        self.assertEqual(len(got), L.MAX_HELD)
        self.assertEqual(got[0].message, "d10")
        self.assertIsNone(ann.logs)

    def test_lines_after_the_request_are_decided_at_once(self):
        ann = self.serve("/done", user="7")
        self.queued()
        token = _context._current.set(ann)
        try:
            self.log.debug("late debug")
            self.log.info("late info")
        finally:
            _context._current.reset(token)
        got = self.queued()
        self.assertEqual([l.message for l in got], ["late info"])
        self.assertEqual(got[0].user_id, "7")

    def test_outside_requests(self):
        self.log.debug("startup debug")
        self.log.error("boom", extra={"err": "disk full", "request_id": "abc", "nested": {"n": 3}})
        got = self.queued()
        self.assertEqual([l.message for l in got], ["boom"])
        self.assertEqual(got[0].request_id, "abc")
        self.assertEqual(got[0].attrs, {"err": "disk full", "nested.n": 3})

    def test_sdk_messages_are_skipped(self):
        self.log.error("visionary: write failed")
        logging.getLogger("visionary").error("internal")
        self.assertEqual(self.queued(), [])


class SlowTest(LogTestCase):
    def rows(self):
        return [("logs.slow_ms", "20")]

    def test_slow_request_keeps_debug(self):
        self.serve("/slow", took=0.03)
        self.assertEqual(self.messages(), "debug line,info line,warn line")
        self.serve("/fast", took=0.01)
        self.assertEqual(self.messages(), "info line,warn line")


class BufferOffTest(LogTestCase):
    def rows(self):
        return [("logs.slow_ms", "20"), ("logs.buffer", "false")]

    def test_buffer_off(self):
        self.serve("/slow", status=500, took=0.03)
        self.assertEqual(self.messages(), "info line,warn line")


class OverrideTest(LogTestCase):
    def rows(self):
        return [("logs.overrides", json.dumps([
            {"route": "/checkout", "level": "debug", "until": ts(timedelta(hours=1))},
            {"user": "42", "level": "trace"},
            {"route": "/health", "level": "warn"},
            {"route": "/old", "level": "debug", "until": ts(timedelta(hours=-1))},
            {"service": "other", "level": "debug"},
        ]))]

    def test_route_override(self):
        self.serve("/checkout")
        self.assertEqual(self.messages(), "debug line,info line,warn line")

    def test_quieter_route(self):
        self.serve("/health")
        self.assertEqual(self.messages(), "warn line")

    def test_expired_override(self):
        self.serve("/old")
        self.assertEqual(self.messages(), "info line,warn line")

    def test_user_override(self):
        def log():
            self.log.log(5, "trace line")
            time.sleep(0.002)
            self.three_lines()

        self.serve("/any", user="42", log=log)
        got = sorted(self.queued(), key=lambda l: l.created_at)
        self.assertEqual([l.message for l in got], ["trace line", "debug line", "info line", "warn line"])
        self.assertEqual(got[0].user_id, "42")


class DropAndRateTest(LogTestCase):
    def rows(self):
        drops = json.dumps([{"contains": "healthcheck"}, {"logger": "noisy", "level": "info"}])
        return [("logs.drop", drops), ("logs.rate", "3")]

    def test_drops_and_rate(self):
        self.log.info("healthcheck ok")
        self.log.info("chatty", extra={"logger": "noisy"})
        self.log.warning("kept", extra={"logger": "noisy"})
        for _ in range(5):
            self.log.info("flood")
        self.log.error("still kept")
        self.assertEqual(self.messages(), "kept,flood,flood,still kept")

    def test_rate_warning_line(self):
        p = self.p
        minute = 60 * 1000
        for i in range(6):
            p.emit(L.LogLine(minute + i * 0.001, L.INFO, "x"))
        p.emit(L.LogLine(minute + 60, L.INFO, "next minute"))
        got = self.queued()
        self.assertEqual([l.message for l in got][:3], ["x", "x", "x"])
        warn = got[3]
        self.assertEqual((warn.sev, warn.logger), (L.WARN, "visionary"))
        self.assertEqual(warn.message, "visionary: 3 log lines dropped over the cap of 3 a minute (Settings → Logs)")
        self.assertEqual(got[4].message, "next minute")

    def test_errors_pass_up_to_ten_times(self):
        for _ in range(35):
            self.p.emit(L.LogLine(120.0, L.ERROR, "e"))
        self.assertEqual(len(self.queued()), 30)


class RecordTest(unittest.TestCase):
    def record(self, logger, *args, **kwargs):
        got = []

        class Grab(logging.Handler):
            def emit(self, record):
                got.append(record)

        logger.handlers = [Grab()]
        logger.propagate = False
        logger.setLevel(1)
        logger.log(*args, **kwargs)
        return got[0]

    def test_code_location(self):
        lg = logging.getLogger("app.record")
        extra = {"when": datetime(2026, 10, 28, tzinfo=timezone.utc), "obj": object, "flag": True}
        r = self.record(lg, logging.INFO, "hello %s", "world", extra=extra)
        line = line_from_record(r)
        self.assertEqual(line.message, "hello world")
        self.assertEqual(line.logger, "app.record")
        self.assertEqual(line.func, "record")
        self.assertGreater(line.line, 0)
        self.assertTrue(line.file.replace("\\", "/").endswith("tests/test_logs.py"))
        self.assertEqual(line.attrs["when"], "2026-10-28T00:00:00Z")
        self.assertIs(line.attrs["flag"], True)
        self.assertIsInstance(line.attrs["obj"], str)
        w = L.LogWriter(Config(), None, quiet, "i-1")
        w._namer = __import__("visionary._profiler", fromlist=["FrameNamer"]).FrameNamer(
            cwd=os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..")))
        row = dict(zip(LOG_COLUMNS, w.row(line)))
        self.assertEqual(row["code_file"], "tests/test_logs.py")
        self.assertEqual(row["code_func"], "record")

    def test_exception_text(self):
        lg = logging.getLogger("app.exc")
        try:
            raise KeyError("missing")
        except KeyError:
            r = self.record(lg, logging.ERROR, "lookup failed", exc_info=True)
        line = line_from_record(r)
        self.assertTrue(line.message.startswith("lookup failed\nTraceback (most recent call last):"))
        self.assertIn("KeyError: 'missing'", line.message)
        self.assertEqual(line.sev, L.ERROR)


class RowTest(unittest.TestCase):
    def test_row_matches_columns(self):
        w = L.LogWriter(Config(service="api", version="v2"), None, quiet, "i-1")
        line = L.LogLine(time.time(), L.WARN, "hi password=x", "app", {"a": 1})
        line.line = 3
        row = w.row(line)
        self.assertEqual(len(row), len(LOG_COLUMNS))
        self.assertEqual(TABLES["logs"], ("app_logs", LOG_COLUMNS))
        r = dict(zip(LOG_COLUMNS, row))
        self.assertEqual((r["service"], r["instance"], r["version"], r["source"]), ("api", "i-1", "v2", "python"))
        self.assertEqual((r["level"], r["severity"], r["code_line"], r["container"]), ("warn", L.WARN, 3, None))
        self.assertEqual(r["message"], "hi password=[REDACTED]")
        self.assertEqual(r["attrs"], '{"a":1}')
        self.assertEqual(r["size"], len(r["message"]) + len(r["attrs"]))

    def test_long_message_is_cut(self):
        w = L.LogWriter(Config(), None, quiet, "i-1")
        row = dict(zip(LOG_COLUMNS, w.row(L.LogLine(0.0, L.INFO, "é" * 5000))))
        self.assertEqual(len(row["message"].encode()), L.MAX_MESSAGE)
        self.assertIsNone(row["attrs"])

    def test_trace_parent(self):
        self.assertEqual(_context.trace_parent_id("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"),
                         "4bf92f3577b34da6a3ce929d0e0e4736")
        for bad in ("", None, "00-xyz-1-1", "00-00000000000000000000000000000000-00f067aa0ba902b7-01"):
            self.assertEqual(_context.trace_parent_id(bad), "")


class CaptureLoggingTest(unittest.TestCase):
    def setUp(self):
        root = logging.getLogger()
        self.saved = (root.level, list(root.handlers))
        self.addCleanup(self.restore)

    def restore(self):
        root = logging.getLogger()
        root.setLevel(self.saved[0])
        root.handlers = self.saved[1]

    def test_keeps_what_the_app_prints(self):
        root = logging.getLogger()
        out = []

        class Collect(logging.Handler):
            def emit(self, record):
                out.append(record.getMessage())

        root.handlers = [Collect()]
        root.setLevel(logging.WARNING)
        verbose = logging.getLogger("app.verbose")
        verbose.setLevel(logging.DEBUG)
        p = pipeline([("logs.level", "debug")])
        h = capture_logging(agent=SimpleNamespace(logs=p))
        self.addCleanup(verbose.setLevel, logging.NOTSET)
        self.assertIs(capture_logging(), h)
        self.assertEqual(root.level, logging.DEBUG)

        logging.getLogger("app.plain").info("plain info")
        logging.getLogger("app.plain").warning("plain warning")
        verbose.debug("verbose debug")
        self.assertEqual(out, ["plain warning", "verbose debug"])
        self.assertEqual([l.message for l in p.writer.lines], ["plain info", "plain warning", "verbose debug"])
        self.assertTrue(any(isinstance(f, _KeepOutput) for f in root.handlers[0].filters))

    def test_last_resort_still_prints(self):
        root = logging.getLogger()
        root.handlers = []
        root.setLevel(logging.WARNING)
        printed = []

        class Resort(logging.Handler):
            def emit(self, record):
                printed.append(record.getMessage())

        saved, logging.lastResort = logging.lastResort, Resort(logging.WARNING)
        self.addCleanup(setattr, logging, "lastResort", saved)
        capture_logging(agent=SimpleNamespace(logs=pipeline()))
        logging.getLogger("app.bare").info("quiet")
        logging.getLogger("app.bare").warning("loud")
        self.assertEqual(printed, ["loud"])


class AgentTest(unittest.TestCase):
    def test_lines_reach_app_logs(self):
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "v.db")
            cfg = Config(connection_factory=lambda: sqlite3.connect(path, timeout=10, check_same_thread=False),
                         dialect="sqlite", system_metrics=False, profile=False, service="api")
            agent = Agent(cfg).start()
            self.assertIs(agent.logs, L.active())
            lg = logging.getLogger("app.agent")
            lg.handlers = [LogHandler()]
            lg.setLevel(logging.DEBUG)
            lg.propagate = False

            from visionary.integrations.asgi import VisionaryASGIMiddleware

            async def app(scope, receive, send):
                visionary.set_user(9)
                lg.debug("held")
                lg.info("handled %s", scope["path"], extra={"order": {"id": 7}})
                await send({"type": "http.response.start", "status": 500, "headers": []})
                await send({"type": "http.response.body", "body": b"x"})

            async def receive():
                return {"type": "http.request", "body": b""}

            async def send(message):
                pass

            scope = {"type": "http", "method": "GET", "path": "/orders", "headers": [
                (b"traceparent", b"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")]}
            asyncio.run(VisionaryASGIMiddleware(app, agent)(scope, receive, send))
            agent.close()
            with closing(sqlite3.connect(path)) as conn:
                rows = conn.execute("SELECT message, level, user_id, trace_id, attrs, source, request_id "
                                    "FROM app_logs ORDER BY created_at, id").fetchall()
            msgs = {r[0]: r for r in rows}
            self.assertEqual(set(msgs), {"held", "handled /orders"})
            r = msgs["handled /orders"]
            self.assertEqual(r[1:6], ("info", "9", "4bf92f3577b34da6a3ce929d0e0e4736", '{"order.id":7}', "python"))
            self.assertTrue(r[6])
            self.assertIsNone(L.active())


if __name__ == "__main__":
    unittest.main()
