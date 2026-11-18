"""Tests for rate limits (SPEC "Rate limits"), including the shared cases in
sdks/go/ratelimit/testdata/cases.json that every SDK runs.

Run from sdks/python:  python -m unittest discover -s tests -v
"""

from __future__ import annotations

import asyncio
import json
import logging
import os
import sys
import threading
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "src"))

import visionary  # noqa: E402
from visionary import _ratelimit as R  # noqa: E402
from visionary._agent import Agent  # noqa: E402
from visionary._config import Config, from_env  # noqa: E402
from visionary._storage import RATE_LIMIT_COLUMNS, REQUEST_COLUMNS, TABLES, Backend, D1Backend  # noqa: E402

CASES = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "go", "ratelimit", "testdata",
                     "cases.json")

quiet = logging.getLogger("visionary.test")
quiet.addHandler(logging.NullHandler())
quiet.propagate = False

try:
    import flask  # noqa: F401
except ImportError:
    flask = None

try:
    import django  # noqa: F401
except ImportError:
    django = None


def step_request(s):
    headers = s.get("headers") or {}
    query = s.get("query") or {}

    def header(name):
        for k, v in headers.items():
            if k.lower() == name.lower():
                return v
        return ""

    return R.Request(s.get("method") or "GET", s.get("path") or "/", s.get("remote_ip") or "203.0.113.1", header,
                     lambda name: query.get(name, ""))


class SharedCasesTest(unittest.TestCase):
    def test_cases(self):
        with open(CASES, encoding="utf-8") as f:
            cases = json.load(f)
        self.assertEqual(len(cases), 25)
        for c in cases:
            with self.subTest(c["name"]):
                self.run_case(c)

    def run_case(self, c):
        lim = R.Limiter(c.get("service") or "")
        records = []
        lim._trace = lambda rid, outcome: records.append("%s:%s" % (rid, outcome))
        lim.load(R.parse(json.dumps(c["config"])))
        for i, s in enumerate(c["steps"]):
            records.clear()
            req = step_request(s)
            st = lim.check_request(req, s["t"])
            u = s.get("user") or {}
            lim.check_user(st, req, R.User(u.get("id", ""), u.get("role", "")), s.get("attrs"), s["t"])
            d = st.denial
            want = s["expect"]
            self.assertEqual(d.status if d else 0, want["status"], "step %d" % i)
            if d is not None:
                if "rule" in want:
                    self.assertEqual(d.rule_id, want["rule"], "step %d" % i)
                if "retry_after" in want:
                    self.assertEqual(d.retry_after, want["retry_after"], "step %d" % i)
                if "body" in want:
                    self.assertEqual(d.body.decode("utf-8"), want["body"], "step %d" % i)
                if "content_type" in want:
                    self.assertEqual(d.content_type, want["content_type"], "step %d" % i)
            self.assertEqual(records, s.get("records") or [], "step %d" % i)


def req(ip="1.1.1.1", method="GET", path="/", header=None):
    return R.Request(method, path, ip, header)


class LimiterTest(unittest.TestCase):
    def test_user_check_runs_once(self):
        lim = R.Limiter()
        lim.load(R.parse('{"rules":[{"id":"u","action":"limit","key":"user","limit":1,"window_s":60}]}'))
        r = req()
        st = lim.check_request(r, 6000)
        self.assertIsNone(lim.check_user(st, r, R.User("a"), None, 6000))
        self.assertIsNone(lim.check_user(st, r, R.User("a"), None, 6000))
        st = lim.check_request(r, 6000)
        self.assertIsNotNone(lim.check_user(st, r, R.User("a"), None, 6000))

    def test_counters_survive_reload_unless_shape_changes(self):
        lim = R.Limiter()
        rule = {"id": "r", "action": "limit", "limit": 1, "window_s": 60, "algorithm": "fixed"}
        lim.load(R.parse(json.dumps({"rules": [rule]})))
        lim.check_request(req(), 6000)
        rule["name"] = "renamed"
        lim.load(R.parse(json.dumps({"rules": [rule]})))
        self.assertIsNotNone(lim.check_request(req(), 6000).denial)
        rule["window_s"] = 30
        lim.load(R.parse(json.dumps({"rules": [rule]})))
        self.assertIsNone(lim.check_request(req(), 6000).denial)

    def test_records_aggregate_and_redact(self):
        lim = R.Limiter("api")
        lim.load(R.parse('{"rules":[{"id":"k","name":"Keys","action":"limit","key":"header:X-Api-Key",'
                         '"limit":0,"window_s":60}]}'))
        r = req(method="post", path="/v1/x", header=lambda n: "secret-value" if n == "x-api-key" else "")
        for _ in range(3):
            lim.check_request(r, 6000)
        ev = lim.drain()
        self.assertEqual(len(ev), 1)
        e = ev[0]
        self.assertEqual((e.hits, e.outcome, e.method, e.rule_name), (3, "limited", "POST", "Keys"))
        self.assertTrue(e.key_value.startswith("sha256:"))
        self.assertEqual(len(e.key_value), 19)
        self.assertEqual(lim.drain(), [])

    def test_aggregates_overflow_into_other(self):
        lim = R.Limiter()
        lim.load(R.parse('{"rules":[{"id":"b","action":"block"}]}'))
        for i in range(R.MAX_AGGREGATES + 5):
            lim.check_request(req("10.0.%d.%d" % (i // 250, i % 250)), 1)
        ev = lim.drain()
        self.assertEqual(len(ev), R.MAX_AGGREGATES + 1)
        self.assertEqual((ev[-1].key_value, ev[-1].ip, ev[-1].hits), ("(other)", "", 5))

    def test_counter_cap_drops_idle_then_all(self):
        lim = R.Limiter()
        lim.load(R.parse('{"rules":[{"id":"c","action":"limit","limit":5,"window_s":1,"algorithm":"fixed"}]}'))
        for i in range(R.MAX_COUNTERS):
            lim.check_request(req("k%d" % i), 100)
        r = lim._set.by_id["c"]
        self.assertEqual(len(r.counters.m), R.MAX_COUNTERS)
        lim.check_request(req("new"), 105)
        self.assertEqual(len(r.counters.m), 1)

    def test_parse_keeps_good_rules(self):
        c = R.parse('{"rules":[{"id":"a","action":"block","limit":"x"},{"id":"b","action":"block"}]}')
        self.assertEqual([r["id"] for r in c["rules"]], ["b"])
        with self.assertRaises(ValueError):
            R.parse('{"rules":')

    def test_glob(self):
        for p, s, want in [("/a*", "/a", True), ("/a*", "/a/b/c", True), ("*", "/", True), ("/a/*/c", "/a/b/c", True),
                           ("/a/*/c", "/a/b/d", False), ("/a", "/a/", False), ("*.json", "/x/y.json", True),
                           ("/a**b", "/ab", True)]:
            self.assertEqual(R.glob(p, s), want, (p, s))

    def test_fails_open(self):
        lim = R.Limiter()
        lim.load(R.parse('{"rules":[{"id":"h","action":"block","match":{"header:X":[]}}]}'))

        def boom(name):
            raise RuntimeError("bad header")

        st = lim.check_request(req(header=boom), 1)
        self.assertIsNone(st.denial)


class FakeStore(Backend):
    """Answers the two settings reads and keeps what is inserted."""

    dialect = "sqlite"

    def __init__(self, settings=None):
        self.settings = dict(settings or {})
        self.reads = []
        self.fail_reads = False
        self.inserted = {}
        self.lock = threading.Lock()

    def create_schema(self, table):
        pass

    def read_settings(self, prefix="profile"):
        raise RuntimeError("no such table: visionary_settings")

    def read_setting(self, name):
        with self.lock:
            self.reads.append(name)
        if self.fail_reads:
            raise RuntimeError("down")
        return self.settings.get(name)

    def insert_many(self, groups):
        with self.lock:
            for table, rows in groups:
                self.inserted.setdefault(table, []).extend(rows)

    def rows(self, table):
        with self.lock:
            return list(self.inserted.get(table, []))


class PollTest(unittest.TestCase):
    def test_config_read_only_when_version_changes(self):
        store = FakeStore({"ratelimit.version": "1", "ratelimit.config": '{"rules":[{"id":"b","action":"block"}]}'})
        rl = R.RateLimits(store, quiet, "", "i-1", [])
        rl.poll()
        rl.poll()
        self.assertEqual(store.reads, ["ratelimit.version", "ratelimit.config", "ratelimit.version"])
        self.assertTrue(rl.limiter.active())
        store.settings["ratelimit.version"] = "2"
        store.settings["ratelimit.config"] = '{"rules":[]}'
        store.reads.clear()
        rl.poll()
        self.assertEqual(store.reads, ["ratelimit.version", "ratelimit.config"])
        self.assertFalse(rl.limiter.active())

    def test_failures_keep_rules_and_missing_row_clears(self):
        store = FakeStore({"ratelimit.version": "1", "ratelimit.config": '{"rules":[{"id":"b","action":"block"}]}'})
        rl = R.RateLimits(store, quiet, "", "i-1", [])
        rl.poll()
        store.fail_reads = True
        rl.poll()
        self.assertTrue(rl.limiter.active())
        store.fail_reads = False
        store.settings = {"ratelimit.version": "2", "ratelimit.config": "{not json"}
        with self.assertLogs(quiet, "WARNING") as logs:
            rl.poll()
            rl.poll()
        self.assertEqual(len(logs.records), 1)
        self.assertTrue(rl.limiter.active())
        store.settings = {}
        rl.poll()
        self.assertFalse(rl.limiter.active())

    def test_env_turns_it_off(self):
        self.assertTrue(from_env({}).rate_limit)
        cfg = from_env({"VISIONARY_RATE_LIMIT": "false"})
        self.assertFalse(cfg.rate_limit)
        agent = Agent(Config(rate_limit=False, system_metrics=False, profile=False, logs=False), backend=FakeStore())
        self.assertIsNone(agent.rate_limits)

    def test_sqlite_reads_rules_and_writes_events(self):
        import sqlite3
        import tempfile
        from contextlib import closing

        from visionary._storage import SQLBackend

        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "v.db")
            with closing(sqlite3.connect(path)) as conn:
                cols = ", ".join("%s TEXT" % c for c in RATE_LIMIT_COLUMNS[:-1])
                conn.execute("CREATE TABLE rate_limit_events (id INTEGER PRIMARY KEY, %s, hits INTEGER)" % cols)
                conn.execute("CREATE TABLE visionary_settings (name TEXT PRIMARY KEY, value TEXT)")
                conn.executemany("INSERT INTO visionary_settings VALUES (?, ?)", [
                    ("ratelimit.version", "1"),
                    ("ratelimit.config", '{"rules":[{"id":"b","action":"block","ips":["::ffff:10.0.0.0/104"]}]}')])
                conn.commit()
            backend = SQLBackend("sqlite", lambda: sqlite3.connect(path))
            rl = R.RateLimits(backend, quiet, "", "i-1", [])
            rl.poll()
            r = req("10.1.2.3", path="/x")
            self.assertEqual(rl.limiter.check_request(r, 1700000000).denial.status, 403)
            self.assertEqual(rl.limiter.check_request(r, 1700000001).denial.status, 403)
            rl.flush()
            backend.close()
            with closing(sqlite3.connect(path)) as conn:
                rows = conn.execute("SELECT created_at, service, instance, rule_id, outcome, key_value, ip, path, hits "
                                    "FROM rate_limit_events").fetchall()
            self.assertEqual(rows, [("2023-11-14T22:13:20.000Z", None, "i-1", "b", "blocked", None, "10.1.2.3", "/x",
                                     2)])

    def test_d1_insert_uses_nullif(self):
        self.assertEqual(TABLES["ratelimit"], ("rate_limit_events", RATE_LIMIT_COLUMNS))
        sql = D1Backend(object())._sql["ratelimit"]
        self.assertEqual(sql, "INSERT INTO rate_limit_events (%s) VALUES (%s)" % (
            ", ".join(RATE_LIMIT_COLUMNS), ", ".join(["NULLIF(?, '')"] * 13)))


CONFIG = json.dumps({"rules": [
    {"id": "ip1", "name": "One a minute", "action": "limit", "key": "ip", "limit": 1, "window_s": 60,
     "algorithm": "fixed", "paths": ["/limited"]},
    {"id": "plan", "action": "limit", "key": "user", "limit": 0, "window_s": 30, "match": {"attr:plan": ["free"]}},
]})
LIMITED_BODY = b'{"error":{"code":"rate_limited","key":"ip1","message":"Too many requests"}}'
PLAN_BODY = b'{"error":{"code":"rate_limited","key":"plan","message":"Too many requests"}}'


class IntegrationCase(unittest.TestCase):
    def setUp(self):
        self.store = FakeStore({"ratelimit.version": "v1", "ratelimit.config": CONFIG})
        self.agent = Agent(Config(instance="i-1", service="api", system_metrics=False, profile=False, logs=False,
                                  metrics=False, spans=False, errors=False), backend=self.store).start()
        self.agent.rate_limits.poll()
        self.assertTrue(self.agent.rate_limits.limiter.active())
        self.ran = []

    def tearDown(self):
        self.agent.close()

    def view(self, path, plan):
        self.ran.append(path)
        if plan:
            visionary.set_user("u1")
            visionary.check_rate_limit({"plan": plan})
            self.ran.append("after check")

    def assertRetry(self, value):
        # a fixed window on the wall clock: up to the window's end
        self.assertTrue(1 <= int(value) <= 60, value)

    def check_events(self, ip):
        self.agent.close()
        events = self.store.rows("ratelimit")
        self.assertEqual(len(events), 2)
        cols = [dict(zip(RATE_LIMIT_COLUMNS, e)) for e in events]
        for c in cols:
            self.assertRegex(c.pop("created_at"), r"^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$")
        self.assertEqual(cols[0], {"service": "api", "instance": "i-1", "rule_id": "ip1", "rule_name": "One a minute",
                                   "outcome": "limited", "key_value": ip, "ip": ip, "country": None, "method": "GET",
                                   "path": "/limited", "user_id": None, "hits": 1})
        self.assertEqual(cols[1], {"service": "api", "instance": "i-1", "rule_id": "plan", "rule_name": "plan",
                                   "outcome": "limited", "key_value": "u1", "ip": ip, "country": None,
                                   "method": "GET", "path": "/me", "user_id": "u1", "hits": 1})
        logged = {(r[REQUEST_COLUMNS.index("path")], r[REQUEST_COLUMNS.index("status")])
                  for r in self.store.rows("requests")}
        self.assertIn(("/limited", 429), logged)
        self.assertIn(("/me", 429), logged)
        errors = [r for r in self.store.rows("requests") if r[REQUEST_COLUMNS.index("error_code")] == "rate_limited"]
        self.assertEqual(len(errors), 2)


class ASGITest(IntegrationCase):
    def call(self, path, plan="", raise_after_start=False):
        from visionary.integrations.asgi import VisionaryASGIMiddleware

        async def app(scope, receive, send):
            if raise_after_start:
                await send({"type": "http.response.start", "status": 200, "headers": []})
            self.view(scope["path"], plan)
            await send({"type": "http.response.start", "status": 200, "headers": [(b"content-type", b"text/plain")]})
            await send({"type": "http.response.body", "body": b"ok"})

        out = {"headers": {}, "body": b""}

        async def receive():
            return {"type": "http.request", "body": b""}

        async def send(message):
            if message["type"] == "http.response.start" and "status" not in out:
                out["status"] = message["status"]
                out["headers"] = {k.decode(): v.decode() for k, v in message.get("headers", [])}
            elif message["type"] == "http.response.body":
                out["body"] += message.get("body", b"")

        scope = {"type": "http", "method": "GET", "path": path, "query_string": b"", "headers": [],
                 "client": ("198.51.100.4", 5000)}
        asyncio.run(VisionaryASGIMiddleware(app, self.agent)(scope, receive, send))
        return out

    def test_limits(self):
        self.assertEqual(self.call("/limited")["status"], 200)
        r = self.call("/limited")
        self.assertEqual((r["status"], r["headers"]["content-type"], r["body"]), (429, "application/json", LIMITED_BODY))
        self.assertRetry(r["headers"]["retry-after"])
        self.assertEqual(self.ran, ["/limited"])
        self.assertEqual(self.call("/other")["status"], 200)
        self.assertEqual(self.call("/me", plan="pro")["status"], 200)
        r = self.call("/me", plan="free")
        self.assertEqual((r["status"], r["headers"]["retry-after"], r["body"]), (429, "30", PLAN_BODY))
        self.assertEqual(self.ran, ["/limited", "/other", "/me", "after check", "/me"])
        self.check_events("198.51.100.4")

    def test_raised_after_start_is_swallowed(self):
        r = self.call("/me", plan="free", raise_after_start=True)
        self.assertEqual(r["status"], 200)

    def test_outside_a_request_does_nothing(self):
        visionary.check_rate_limit({"plan": "free"})


@unittest.skipIf(flask is None, "flask not installed")
class FlaskTest(IntegrationCase):
    def setUp(self):
        super().setUp()
        from flask import Flask, request

        from visionary.integrations.flask import Visionary

        app = Flask(__name__)
        Visionary(app, agent=self.agent)

        @app.route("/<path:p>")
        def any_path(p):
            self.view(request.path, request.args.get("plan", ""))
            return "ok"

        self.client = app.test_client()

    def test_limits(self):
        c = self.client
        self.assertEqual(c.get("/limited").status_code, 200)
        r = c.get("/limited")
        self.assertEqual((r.status_code, r.headers["Content-Type"], r.data), (429, "application/json", LIMITED_BODY))
        self.assertRetry(r.headers["Retry-After"])
        self.assertEqual(self.ran, ["/limited"])
        self.assertEqual(c.get("/me?plan=pro").status_code, 200)
        r = c.get("/me?plan=free")
        self.assertEqual((r.status_code, r.headers["Retry-After"], r.data), (429, "30", PLAN_BODY))
        self.assertEqual(self.ran, ["/limited", "/me", "after check", "/me"])
        self.check_events("127.0.0.1")


_django_ready = False


def _django_setup():
    global _django_ready
    if _django_ready:
        return
    from django.conf import settings

    settings.configure(DEBUG=False, SECRET_KEY="test", ALLOWED_HOSTS=["*"], ROOT_URLCONF=__name__,
                       MIDDLEWARE=[__name__ + ".DjangoMiddleware", __name__ + ".DjangoAuthMiddleware"],
                       INSTALLED_APPS=[], DATABASES={})
    django.setup()
    _django_ready = True


_django_case = None
urlpatterns = []

if django is not None:
    from django.http import HttpResponse
    from django.urls import re_path

    from visionary.integrations.django import VisionaryMiddleware

    class DjangoMiddleware(VisionaryMiddleware):
        def __init__(self, get_response):
            super().__init__(get_response, agent=_django_case.agent)

    class DjangoAuthMiddleware:
        """Calls the user check from a middleware, as auth code would."""

        def __init__(self, get_response):
            self.get_response = get_response

        def __call__(self, request):
            if request.GET.get("mw"):
                visionary.set_user("u1")
                visionary.check_rate_limit({"plan": request.GET["mw"]})
            return self.get_response(request)

    def _django_view(request, p=""):
        _django_case.view(request.path, request.GET.get("plan", ""))
        return HttpResponse("ok")

    urlpatterns = [re_path(r"^(?P<p>.*)$", _django_view)]


@unittest.skipIf(django is None, "django not installed")
class DjangoTest(IntegrationCase):
    def setUp(self):
        global _django_case
        super().setUp()
        _django_case = self
        _django_setup()
        from django.test import Client

        self.client = Client(raise_request_exception=False)

    def test_limits(self):
        c = self.client
        self.assertEqual(c.get("/limited").status_code, 200)
        r = c.get("/limited")
        self.assertEqual((r.status_code, r["Content-Type"], r.content), (429, "application/json", LIMITED_BODY))
        self.assertRetry(r["Retry-After"])
        self.assertEqual(self.ran, ["/limited"])
        self.assertEqual(c.get("/me?plan=pro").status_code, 200)
        r = c.get("/me?plan=free")
        self.assertEqual((r.status_code, r["Retry-After"], r.content), (429, "30", PLAN_BODY))
        self.assertEqual(self.ran, ["/limited", "/me", "after check", "/me"])
        self.check_events("127.0.0.1")

    def test_raised_in_a_middleware(self):
        r = self.client.get("/x?mw=free")
        self.assertEqual((r.status_code, r["Retry-After"], r.content), (429, "30", PLAN_BODY))
        self.assertEqual(self.ran, [])


if __name__ == "__main__":
    unittest.main()
