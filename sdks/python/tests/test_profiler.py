"""Tests for CPU profiles (SPEC "CPU profiles").

Run from sdks/python:  python -m unittest discover -s tests -v
"""

from __future__ import annotations

import asyncio
import hashlib
import json
import logging
import os
import queue
import selectors
import socket
import sqlite3
import sys
import tempfile
import threading
import time
import unittest
from contextlib import closing

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "src"))

from visionary import _profiler as P  # noqa: E402
from visionary._agent import Agent  # noqa: E402
from visionary._config import Config, from_env  # noqa: E402
from visionary._entry import format_ts  # noqa: E402
from visionary._storage import SQLBackend  # noqa: E402
from visionary._writer import RequestWriter  # noqa: E402

SETTINGS_DDL = ("CREATE TABLE IF NOT EXISTS visionary_settings "
                "(name TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TEXT NOT NULL)")
PROFILES_DDL = ("CREATE TABLE IF NOT EXISTS profiles (id INTEGER PRIMARY KEY AUTOINCREMENT, created_at TEXT NOT NULL, "
                "service TEXT, instance TEXT NOT NULL, version TEXT, sdk TEXT, duration_ms REAL NOT NULL, "
                "samples INTEGER NOT NULL, routes TEXT, folded TEXT NOT NULL)")

quiet = logging.getLogger("visionary.test")
quiet.addHandler(logging.NullHandler())
quiet.propagate = False


class _Code:
    """Stands in for a code object in frame-naming tests."""

    def __init__(self, filename, name, qualname=None):
        self.co_filename = filename
        self.co_name = name
        if qualname is not None:
            self.co_qualname = qualname


class FrameNamingTest(unittest.TestCase):
    def namer(self):
        return P.FrameNamer(cwd="/srv/app", stdlib=["/usr/lib/python3.12"], sdk_dir="/opt/visionary")

    def test_app_code_is_relative_to_cwd(self):
        n = self.namer()
        self.assertEqual(n.name(_Code("/srv/app/shop/views.py", "checkout"), 42), "checkout shop/views.py:42")

    def test_site_and_dist_packages_are_lib(self):
        n = self.namer()
        self.assertEqual(n.file("/srv/app/.venv/lib/python3.12/site-packages/django/db/models/query.py"),
                         "lib:django/db/models/query.py")
        self.assertEqual(n.file("/usr/lib/python3/dist-packages/requests/sessions.py"), "lib:requests/sessions.py")
        self.assertEqual(n.file("/usr/lib/python3.12/site-packages/six.py"), "lib:six.py")

    def test_stdlib_is_std(self):
        n = self.namer()
        self.assertEqual(n.file("/usr/lib/python3.12/json/decoder.py"), "std:json/decoder.py")
        self.assertEqual(n.file("/usr/lib/python3.12/threading.py"), "std:threading.py")

    def test_sdk_is_lib_visionary(self):
        self.assertEqual(self.namer().file("/opt/visionary/integrations/asgi.py"), "lib:visionary/integrations/asgi.py")

    def test_frames_without_a_file(self):
        n = self.namer()
        self.assertEqual(n.name(_Code("<string>", "<module>"), 1), "<module>")
        self.assertEqual(n.name(_Code("", "f"), 1), "f")
        self.assertEqual(n.name(_Code("<frozen importlib._bootstrap>", "_load"), 7),
                         "_load std:importlib/_bootstrap.py:7")

    def test_other_absolute_paths_stay(self):
        self.assertEqual(self.namer().file("/opt/other/x.py"), "/opt/other/x.py")

    def test_qualname_and_semicolons(self):
        n = self.namer()
        self.assertEqual(n.name(_Code("/srv/app/a.py", "run", "Job.run"), 3), "Job.run a.py:3")
        self.assertEqual(n.name(_Code("/srv/app/a;b.py", "f"), 1), "f a,b.py:1")

    @unittest.skipUnless(os.name == "nt", "Windows paths")
    def test_windows_paths(self):
        n = P.FrameNamer(cwd="C:\\Work\\App", stdlib=["C:\\Python313\\Lib"], sdk_dir="C:\\x\\visionary")
        self.assertEqual(n.file("c:\\work\\app\\api\\views.py"), "api/views.py")
        self.assertEqual(n.file("C:\\Python313\\Lib\\site-packages\\flask\\app.py"), "lib:flask/app.py")
        self.assertEqual(n.file("C:\\Python313\\Lib\\json\\decoder.py"), "std:json/decoder.py")

    def test_real_frames(self):
        n = P.FrameNamer()
        here = n.name(sys._getframe().f_code, 10)
        self.assertTrue(here.startswith("FrameNamingTest.test_real_frames "), here)
        self.assertEqual(n.file(json.__file__.replace("__init__.py", "decoder.py")), "std:json/decoder.py")


class FoldTest(unittest.TestCase):
    def test_heaviest_first(self):
        text, n = P.fold({"a;b": 2, "a;c": 5, "d": 1})
        self.assertEqual(text, "a;c 5\na;b 2\nd 1\n")
        self.assertEqual(n, 8)

    def test_cap_sums_the_rest(self):
        lines = {"frame_%05d %s" % (i, "x" * 200): 1000 - (i % 900) for i in range(2000)}
        text, n = P.fold(lines)
        self.assertEqual(n, sum(lines.values()))
        body, last = text.rstrip("\n").rsplit("\n", 1)
        self.assertTrue(last.startswith("(other) "), last)
        self.assertLessEqual(len((body + "\n").encode("utf-8")), P.MAX_FOLDED)
        kept = sum(int(line.rsplit(" ", 1)[1]) for line in body.split("\n"))
        self.assertEqual(kept + int(last.split(" ")[1]), n)
        counts = [int(line.rsplit(" ", 1)[1]) for line in body.split("\n") if "(other)" not in line]
        self.assertEqual(counts, sorted(counts, reverse=True))

    def test_rolls_up_into_callers(self):
        # Stacks that don't fit are cut after the deepest frame they share
        # with a kept stack, so their time stays under the right caller.
        heavy = "serve;handler;" + "h" * (P.KEEP_WHOLE - 40)
        lines = {
            heavy: 100,
            "serve;handler;json;encode;" + "e" * 30000: 7,
            "serve;handler;json;encode;" + "f" * 30000: 5,
            "serve;handler": 4,  # leads into the kept stack: kept as it is
            "worker;loop;" + "w" * 30000: 3,
        }
        text, n = P.fold(lines)
        self.assertEqual(n, 119)
        for want in (heavy + " 100\n", "serve;handler;(other) 12\n", "serve;handler 4\n", "(other) 3\n"):
            self.assertIn(want, text)
        self.assertLessEqual(len(text.encode("utf-8")), P.MAX_FOLDED)


class SettingsTest(unittest.TestCase):
    def test_defaults(self):
        s = P.parse_settings([])
        self.assertFalse(s.enabled)
        self.assertEqual((s.services, s.interval_s, s.duration_ms, s.now), ([], 300.0, 10000.0, ""))

    def test_values(self):
        s = P.parse_settings([("profile.enabled", "true"), ("profile.services", '["api","worker"]'),
                              ("profile.interval_s", "60"), ("profile.duration_ms", "2500"),
                              ("profile.now", "2026-01-01T00:00:00.000Z")])
        self.assertTrue(s.enabled)
        self.assertEqual((s.services, s.interval_s, s.duration_ms), (["api", "worker"], 60.0, 2500.0))

    def test_bad_values_take_defaults(self):
        s = P.parse_settings([("profile.enabled", "yes please"), ("profile.services", "{"),
                              ("profile.interval_s", "-1"), ("profile.duration_ms", "nan")])
        self.assertFalse(s.enabled)
        self.assertEqual((s.services, s.interval_s, s.duration_ms), ([], 300.0, 10000.0))

    def test_services_filter(self):
        self.assertTrue(P.Settings(services=[]).includes("api"))
        self.assertTrue(P.Settings(services=[]).includes(""))
        self.assertTrue(P.Settings(services=["api"]).includes("api"))
        self.assertFalse(P.Settings(services=["api"]).includes("worker"))
        self.assertFalse(P.Settings(services=["api"]).includes(""))

    def test_fresh_now(self):
        start = 1_800_000_000.0
        ts = format_ts(start + 10)
        self.assertTrue(P.fresh_now(ts, start, start + 20))
        self.assertFalse(P.fresh_now(format_ts(start - 10), start, start + 20))  # before the SDK started
        self.assertFalse(P.fresh_now(ts, start, start + 10 + 301))  # older than 5 minutes
        self.assertFalse(P.fresh_now("not a time", start, start + 20))
        self.assertFalse(P.fresh_now("", start, start + 20))

    def test_parse_ts_formats(self):
        self.assertEqual(P.parse_ts("2026-10-22T10:00:00.500Z"), 1792663200.5)
        self.assertEqual(P.parse_ts("2026-10-22 10:00:00"), 1792663200.0)
        self.assertEqual(P.parse_ts("2026-10-22T12:00:00+02:00"), 1792663200.0)

    def test_profile_env_is_hard_off(self):
        self.assertTrue(from_env({}).profile)
        self.assertFalse(from_env({"VISIONARY_PROFILE": "false"}).profile)
        with tempfile.TemporaryDirectory() as d:
            url = "sqlite:///" + os.path.join(d, "v.db").replace("\\", "/")
            on = Agent(Config(database_url=url, system_metrics=False))
            off = Agent(Config(database_url=url, system_metrics=False, profile=False))
            self.assertIsNotNone(on._profiler)
            self.assertIsNone(off._profiler)
            on._backend.close()
            off._backend.close()


class _FakeBackend:
    def __init__(self, rows=None, fail=False):
        self.rows = rows or []
        self.fail = fail

    def read_settings(self):
        if self.fail:
            raise RuntimeError("no such table: visionary_settings")
        return self.rows


class ScheduleTest(unittest.TestCase):
    def profiler(self, rows, service="api"):
        p = P.Profiler(_FakeBackend(rows), lambda _: None, quiet, service)
        p.captured = 0

        def capture():
            p.captured += 1

        p.capture = capture
        return p

    def test_off_until_enabled(self):
        p = self.profiler([])
        now = time.time()
        p.poll(now)
        self.assertFalse(p.on)
        P.tally.request_started()
        p.turn(now + 10_000)
        self.assertEqual(p.captured, 0)

    def test_failed_read_is_off(self):
        p = P.Profiler(_FakeBackend(fail=True), lambda _: None, quiet)
        p.poll(time.time())
        self.assertFalse(p.on)

    def test_enabled_runs_every_interval_when_requests_arrive(self):
        p = self.profiler([("profile.enabled", "true"), ("profile.interval_s", "60")])
        now = time.time()
        p.poll(now)
        self.assertTrue(p.on)
        P.tally.request_started()
        p.turn(now + 30)
        self.assertEqual(p.captured, 0)  # not due yet
        p.turn(now + 61)
        self.assertEqual(p.captured, 1)
        p.turn(now + 122)  # no request since: skip the turn
        self.assertEqual(p.captured, 1)
        P.tally.request_started()
        p.turn(now + 183)
        self.assertEqual(p.captured, 2)

    def test_services_filter_excludes(self):
        p = self.profiler([("profile.enabled", "true"), ("profile.services", '["worker"]')])
        p.poll(time.time())
        self.assertFalse(p.on)
        unnamed = self.profiler([("profile.enabled", "true"), ("profile.services", '["worker"]')], service="")
        unnamed.poll(time.time())
        self.assertFalse(unnamed.on)

    def test_profile_now_one_off(self):
        p = self.profiler([])
        p.started_at = time.time() - 60
        now = time.time()
        p.backend_rows = p._backend.rows = [("profile.now", format_ts(now - 5))]
        p.poll(now)
        self.assertFalse(p.on)
        p.turn(now)
        self.assertEqual(p.captured, 1)
        p.poll(now + 30)  # same value: no second profile
        p.turn(now + 30)
        self.assertEqual(p.captured, 1)

    def test_profile_now_stale_or_filtered(self):
        p = self.profiler([])
        p.started_at = time.time()
        p._backend.rows = [("profile.now", format_ts(p.started_at - 1))]  # before start
        p.poll(time.time())
        self.assertFalse(p.one_off)
        q = self.profiler([("profile.services", '["worker"]')])
        q.started_at = time.time() - 60
        q._backend.rows.append(("profile.now", format_ts(time.time())))
        q.poll(time.time())
        self.assertFalse(q.one_off)

    def test_logs_on_and_off(self):
        p = self.profiler([("profile.enabled", "true")])
        with self.assertLogs(quiet, "INFO") as logs:
            p.poll(time.time())
            p._backend.rows = [("profile.enabled", "false")]
            p.poll(time.time())
        self.assertEqual(len(logs.records), 2)
        self.assertIn("on", logs.records[0].getMessage())
        self.assertIn("off", logs.records[1].getMessage())


def _frame_of(thread):
    return sys._current_frames()[thread.ident]


class CpuFilterTest(unittest.TestCase):
    def test_fake_clock(self):
        clock = {1: 0.0}
        f = P.CpuFilter(lambda k: clock[k])
        frame = sys._getframe()  # this frame is running, not blocked
        self.assertFalse(f.using_cpu(1, frame))  # first sight: no baseline yet
        clock[1] = 0.004
        self.assertTrue(f.using_cpu(1, frame))
        self.assertFalse(f.using_cpu(1, frame))  # no CPU since the last tick
        clock[1] = 0.009
        self.assertTrue(f.using_cpu(1, frame))

    def test_fake_clock_with_blocked_leaf(self):
        stop = threading.Event()
        t = threading.Thread(target=stop.wait, daemon=True)
        t.start()
        time.sleep(0.05)
        clock = {7: 0.0}
        f = P.CpuFilter(lambda k: clock[k])
        f.using_cpu(7, _frame_of(t))
        clock[7] = 1.0  # CPU advanced, but it is parked in a lock wait now
        self.assertFalse(f.using_cpu(7, _frame_of(t)))
        stop.set()

    def test_clock_failure_falls_back(self):
        def broken(_):
            raise OSError("gone")

        f = P.CpuFilter(broken)
        self.assertTrue(f.using_cpu(1, sys._getframe()))

    @unittest.skipUnless(sys.platform.startswith("linux"), "Linux thread CPU clocks")
    def test_linux_clock(self):
        clock = P.default_cpu_clock()
        self.assertIsNotNone(clock)
        a = clock(threading.get_native_id())
        hashlib.pbkdf2_hmac("sha256", b"x", b"y", 50000)
        self.assertGreater(clock(threading.get_native_id()), a)


class BlockingHeuristicTest(unittest.TestCase):
    def setUp(self):
        self.stop = threading.Event()
        self.threads = {}
        self.socks = []

    def tearDown(self):
        self.stop.set()
        for name in ("sleep", "select", "busy_py", "busy_c"):
            if name in self.threads:
                self.threads[name].join(1)
        for s in self.socks:
            try:
                s.close()
            except OSError:
                pass

    def spawn(self, name, fn):
        t = threading.Thread(target=fn, name=name, daemon=True)
        t.start()
        self.threads[name] = t

    def listener(self):
        s = socket.socket()
        s.bind(("127.0.0.1", 0))
        s.listen()
        self.socks.append(s)
        return s

    def test_idle_threads_are_blocked_and_busy_ones_are_not(self):
        stop = self.stop

        def sleeper():
            while not stop.is_set():
                time.sleep(0.05)

        # Connected here, so the thread does nothing but wait in recv: set up
        # in the thread, it can be caught between connecting and receiving.
        conn = socket.create_connection(self.listener().getsockname())
        self.socks.append(conn)

        def recv():
            try:
                conn.recv(10)
            except OSError:
                pass  # closed by tearDown

        def accept():
            try:
                self.listener().accept()
            except OSError:
                pass

        def selecting():
            srv = self.listener()
            with selectors.DefaultSelector() as sel:
                sel.register(srv, selectors.EVENT_READ)
                while not stop.is_set():
                    try:
                        sel.select(0.2)
                    except OSError:
                        return

        def loop_idle():
            loop = asyncio.new_event_loop()
            loop.run_until_complete(asyncio.sleep(30))

        def busy_py():
            x = 0
            while not stop.is_set():
                for i in range(5000):
                    x += i * i

        def busy_c():
            while not stop.is_set():
                hashlib.pbkdf2_hmac("sha256", b"pw", b"salt", 100000)

        self.spawn("sleep", sleeper)
        self.spawn("event", stop.wait)
        self.spawn("queue", queue.Queue().get)
        self.spawn("accept", accept)
        self.spawn("recv", recv)
        self.spawn("select", selecting)
        self.spawn("asyncio", loop_idle)
        self.spawn("busy_py", busy_py)
        self.spawn("busy_c", busy_c)
        idle = ["sleep", "event", "queue", "accept", "recv", "select", "asyncio"]
        # Let each thread get past its setup (imports, connecting) first. Setup
        # passes through blocking calls too (an event loop's socket pair
        # accepts), so wait until they look blocked several checks running.
        deadline = time.monotonic() + 5
        steady = 0
        while time.monotonic() < deadline and steady < 4:
            frames = sys._current_frames()
            steady = steady + 1 if all(P.blocked(frames[self.threads[n].ident]) for n in idle) else 0
            time.sleep(0.05)
        for _ in range(5):
            frames = sys._current_frames()
            for name in idle:
                f = frames[self.threads[name].ident]
                self.assertTrue(P.blocked(f), (name, f.f_code.co_name, P.callee_name(f.f_code, f.f_lasti)))
            self.assertFalse(P.blocked(frames[self.threads["busy_py"].ident]))
            time.sleep(0.02)
        hits = sum(1 for _ in range(20)
                   if P.callee_name(_frame_of(self.threads["busy_c"]).f_code,
                                    _frame_of(self.threads["busy_c"]).f_lasti) == "pbkdf2_hmac")
        self.assertGreater(hits, 10)


def _pbkdf2_worker(stop):
    while not stop.is_set():
        hashlib.pbkdf2_hmac("sha256", b"pw", b"salt", 50000)


class SamplerTest(unittest.TestCase):
    def test_counts_cpu_threads_only(self):
        stop = threading.Event()
        busy = threading.Thread(target=_pbkdf2_worker, args=(stop,), daemon=True)
        idle = threading.Thread(target=stop.wait, daemon=True)
        own = threading.Thread(target=stop.wait, name="visionary-requests", daemon=True)
        for t in (busy, idle, own):
            t.start()
        time.sleep(0.05)
        s = P.Sampler(P.FrameNamer(), P.CpuFilter(None))
        try:
            for _ in range(40):
                s.tick()
                time.sleep(0.005)
        finally:
            stop.set()
        folded, samples = s.result()
        self.assertGreaterEqual(samples, 40)
        self.assertRegex(folded, r"_pbkdf2_worker (tests/)?test_profiler\.py:\d+")
        self.assertNotIn("Event.wait", folded)  # idle thread and the SDK's own thread left out
        for line in folded.splitlines():
            stack, n = line.rsplit(" ", 1)
            self.assertTrue(int(n) > 0)
            self.assertTrue(stack.split(";")[0])
        self.assertIn("Thread.run std:threading.py;", folded)

    def test_capture_writes_a_profile(self):
        pushed = []
        p = P.Profiler(_FakeBackend(), pushed.append, quiet, "api")
        p.settings = P.Settings(enabled=True, duration_ms=400)
        stop = threading.Event()
        busy = threading.Thread(target=_pbkdf2_worker, args=(stop,), daemon=True)
        busy.start()

        def requests():
            for _ in range(3):
                time.sleep(0.05)
                P.request_finished("GET", "/hash", 0.0304)
            P.request_finished("POST", "/x", 0.0001)

        r = threading.Thread(target=requests)
        r.start()
        try:
            prof = p.capture()
        finally:
            stop.set()
            r.join()
        self.assertIsNotNone(prof)
        self.assertEqual(pushed, [prof])
        self.assertEqual(json.loads(prof.routes), {"GET /hash": 91, "POST /x": 1})
        self.assertGreaterEqual(prof.samples, P.MIN_SAMPLES)
        self.assertGreaterEqual(prof.duration_ms, 400)
        self.assertIn("_pbkdf2_worker", prof.folded)
        self.assertIsNone(P.tally._busy)  # stops counting after the profile

    def test_capture_dropped_without_finished_requests(self):
        pushed = []
        p = P.Profiler(_FakeBackend(), pushed.append, quiet)
        p.settings = P.Settings(enabled=True, duration_ms=200)
        self.assertIsNone(p.capture())
        self.assertEqual(pushed, [])


class StorageTest(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        self.path = os.path.join(self.dir.name, "v.db")
        self.backend = SQLBackend("sqlite", lambda: sqlite3.connect(self.path))

    def tearDown(self):
        self.backend.close()
        self.dir.cleanup()

    def test_read_settings(self):
        with self.assertRaises(Exception):
            self.backend.read_settings()
        with closing(sqlite3.connect(self.path)) as c, c:
            c.execute(SETTINGS_DDL)
            c.executemany("INSERT INTO visionary_settings VALUES (?, ?, ?)",
                          [("profile.enabled", "true", "x"), ("ui.hidden_pages", "[]", "x")])
        self.assertEqual(self.backend.read_settings(), [("profile.enabled", "true")])

    def test_profiles_written_apart_from_requests(self):
        cfg = Config(database_url="x", service="api", version="v1", log_requests=False)
        w = RequestWriter(cfg, self.backend, quiet, "host-1")
        prof = P.Profile(1_800_000_000.0, 1000.5, 42, '{"GET /": 3}', "main app.py:1 42\n")
        # No profiles table yet: the write fails on its own, nothing raises.
        w._write([prof])
        with closing(sqlite3.connect(self.path)) as c, c:
            c.execute(PROFILES_DDL)
        w._write([prof])
        with closing(sqlite3.connect(self.path)) as c, c:
            rows = c.execute("SELECT created_at, service, instance, version, sdk, duration_ms, samples, routes, folded "
                             "FROM profiles").fetchall()
        self.assertEqual(rows, [(format_ts(1_800_000_000.0), "api", "host-1", "v1", "python", 1000.5, 42,
                                 '{"GET /": 3}', "main app.py:1 42\n")])


class IntegrationTallyTest(unittest.TestCase):
    def test_asgi_counts_started_and_route_time(self):
        from visionary.integrations.asgi import VisionaryASGIMiddleware

        with tempfile.TemporaryDirectory() as d:
            url = "sqlite:///" + os.path.join(d, "v.db").replace("\\", "/")
            agent = Agent(Config(database_url=url, system_metrics=False, path_prefix="/api"))

            async def app(scope, receive, send):
                await send({"type": "http.response.start", "status": 200, "headers": []})
                await send({"type": "http.response.body", "body": b"ok"})

            mw = VisionaryASGIMiddleware(app, agent=agent)

            async def call(path):
                async def receive():
                    return {"type": "http.request", "body": b""}

                async def send(_):
                    pass

                await mw({"type": "http", "method": "GET", "path": path, "headers": [], "query_string": b""},
                         receive, send)

            before = P.tally.started
            P.tally.begin()
            try:
                asyncio.run(call("/api/items"))  # logged
                asyncio.run(call("/health"))  # outside the path prefix: still counted
            finally:
                busy = P.tally.end()
            agent._backend.close()
        self.assertEqual(P.tally.started - before, 2)
        self.assertEqual(sorted(busy), ["GET /api/items", "GET /health"])


if __name__ == "__main__":
    unittest.main()
