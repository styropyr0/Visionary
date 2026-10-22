"""Tests for work a request starts in the background (SPEC "Work a request
starts in the background").

Run from sdks/python:  python -m unittest discover -s tests -v
"""

from __future__ import annotations

import asyncio
import contextvars
import os
import sys
import threading
import time
import unittest
from concurrent.futures import ThreadPoolExecutor
from types import SimpleNamespace

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "src"))

import visionary  # noqa: E402
from visionary import _context, _spans  # noqa: E402


class FakeAgent:
    """What spans need from an agent: config, spans_on and record_span."""

    def __init__(self, threshold_ms: float = 100.0, span_max: int = 100) -> None:
        self.config = SimpleNamespace(span_threshold_ms=threshold_ms, span_max=span_max)
        self.spans_on = True
        self.recorded = []
        self._lock = threading.Lock()

    def record_span(self, span):
        with self._lock:
            self.recorded.append(span)


class BackgroundTestCase(unittest.TestCase):
    threshold_ms = 100.0
    span_max = 100

    def setUp(self):
        self.agent = FakeAgent(self.threshold_ms, self.span_max)
        _spans.set_active(self.agent)
        self.addCleanup(_spans.clear_active, self.agent)

    def begin_request(self, rid="req-1"):
        """A logged request's trace and annotations in the current context;
        the returned function ends the request (the integration's job)."""
        trace = _spans.Trace(rid, time.perf_counter(), self.span_max)
        ann, token = _context.begin(trace)
        return trace, lambda: _context.end(token)


class LateSpanTest(BackgroundTestCase):
    span_max = 3

    def test_late_spans_keep_request_id_and_offset(self):
        trace, end = self.begin_request("req-late")
        try:
            sp = _spans.start("db", "SELECT users")
        finally:
            end()
        trace.close()  # the request is logged
        time.sleep(0.02)
        sp.finish()
        self.assertEqual(self.agent.recorded, [sp])
        self.assertEqual(sp.request_id, "req-late")
        self.assertIsNotNone(sp.offset_ms)
        self.assertGreaterEqual(sp.offset_ms, 0.0)
        self.assertLess(sp.duration_ms, self.threshold_ms)

    def test_late_span_started_after_the_response_keeps_its_offset(self):
        trace, end = self.begin_request()
        ctx = contextvars.copy_context()
        end()
        trace.close()
        time.sleep(0.03)
        sp = ctx.run(_spans.start, "http", "POST api.example.com")
        sp.finish()
        self.assertEqual(sp.request_id, "req-1")
        self.assertGreaterEqual(sp.offset_ms, 30.0)
        self.assertEqual(self.agent.recorded, [sp])

    def test_late_spans_below_threshold_are_capped(self):
        trace, end = self.begin_request()
        try:
            fast = [_spans.start("db", "SELECT a") for _ in range(5)]
            slow = _spans.start("db", "SELECT slow")
        finally:
            end()
        trace.close()
        for sp in fast:
            sp.finish()
        slow._t0 -= 0.5  # took 500 ms
        slow.finish()
        # The first span_max (3) late spans are stored whatever their
        # duration; beyond that only the one over the threshold.
        self.assertEqual(self.agent.recorded, fast[:3] + [slow])
        self.assertGreaterEqual(slow.duration_ms, self.threshold_ms)

    def test_late_cap_is_separate_from_the_request_cap(self):
        trace, end = self.begin_request()
        try:
            in_request = [_spans.start("db", "SELECT a") for _ in range(3)]
            for sp in in_request:
                sp.finish()
            late = _spans.start("db", "SELECT b")
        finally:
            end()
        self.assertEqual([s.name for s in trace.close()], ["SELECT a"] * 3)
        late.finish()
        self.assertEqual(self.agent.recorded, [late])

    def test_late_spans_do_not_change_the_totals(self):
        trace, end = self.begin_request()
        try:
            _spans.start("db", "SELECT a").finish()
            _spans.start("http", "GET example.com").finish()
            late_db = _spans.start("db", "SELECT b")
            late_http = _spans.start("http", "GET example.com")
            with visionary.background_span("in request"):
                pass
        finally:
            end()
        trace.close()
        totals = (trace.db_count, trace.db_ms, trace.http_count, trace.http_ms)
        late_db.finish()
        late_http.finish()
        self.assertEqual((trace.db_count, trace.db_ms, trace.http_count, trace.http_ms), totals)
        self.assertEqual((trace.db_count, trace.http_count), (1, 1))

    def test_spans_outside_a_request_still_need_the_threshold(self):
        _spans.start("db", "SELECT fast").finish()
        self.assertEqual(self.agent.recorded, [])
        sp = _spans.start("db", "SELECT slow")
        sp._t0 -= 0.2
        sp.finish()
        self.assertEqual(self.agent.recorded, [sp])
        self.assertIsNone(sp.request_id)
        self.assertIsNone(sp.offset_ms)


def _background_spans(agent):
    return [s for s in agent.recorded if s.kind == "background"]


class BackgroundHelperTest(BackgroundTestCase):
    def test_background_records_a_span_with_offset_name_and_status(self):
        seen = {}
        trace, end = self.begin_request("req-bg")
        try:
            time.sleep(0.02)

            def work(a, b=0):
                seen["ann"] = _context.current()
                seen["sum"] = a + b
                time.sleep(0.05)
                _spans.start("db", "SELECT inner").finish()

            thread = visionary.background("send welcome email", work, 1, b=2)
        finally:
            end()
        trace.close()  # the response goes before the work is done
        self.assertIsInstance(thread, threading.Thread)
        self.assertFalse(thread.daemon)
        thread.join(5)
        self.assertEqual(seen["sum"], 3)
        self.assertIs(seen["ann"].trace, trace)
        bg = _background_spans(self.agent)
        self.assertEqual(len(bg), 1)
        sp = bg[0]
        self.assertEqual((sp.kind, sp.name, sp.status, sp.error), ("background", "send welcome email", "ok", None))
        self.assertEqual(sp.request_id, "req-bg")
        self.assertGreaterEqual(sp.offset_ms, 20.0)
        self.assertGreaterEqual(sp.duration_ms, 50.0)
        inner = [s for s in self.agent.recorded if s.kind == "db"]
        self.assertEqual(len(inner), 1)
        self.assertEqual(inner[0].request_id, "req-bg")
        self.assertGreaterEqual(inner[0].offset_ms, sp.offset_ms)

    def test_background_marks_a_failure(self):
        hooked = []
        old = threading.excepthook
        threading.excepthook = lambda args: hooked.append(args.exc_value)
        self.addCleanup(setattr, threading, "excepthook", old)
        trace, end = self.begin_request()
        try:
            def work():
                raise ValueError("smtp down")

            visionary.background("send welcome email", work).join(5)
        finally:
            end()
        (sp,) = trace.close()  # finished inside the request: kept with it
        self.assertEqual((sp.kind, sp.name, sp.status, sp.error),
                         ("background", "send welcome email", "error", "smtp down"))
        self.assertEqual(len(hooked), 1)  # the exception still reaches excepthook
        self.assertIsInstance(hooked[0], ValueError)

    def test_background_failure_after_the_response(self):
        old = threading.excepthook
        threading.excepthook = lambda args: None
        self.addCleanup(setattr, threading, "excepthook", old)
        trace, end = self.begin_request()
        gate = threading.Event()
        try:
            def work():
                gate.wait(5)
                raise RuntimeError("smtp down")

            thread = visionary.background("send welcome email", work)
        finally:
            end()
        trace.close()
        gate.set()
        thread.join(5)
        (sp,) = _background_spans(self.agent)
        self.assertEqual((sp.status, sp.error, sp.request_id), ("error", "smtp down", "req-1"))

    def test_background_finished_inside_the_request_is_kept_with_it(self):
        trace, end = self.begin_request()
        try:
            visionary.background("warm cache", lambda: None).join(5)
        finally:
            end()
        kept = trace.close()
        self.assertEqual([(s.kind, s.name) for s in kept], [("background", "warm cache")])
        self.assertEqual((trace.db_count, trace.http_count), (0, 0))
        self.assertEqual(self.agent.recorded, [])

    def test_background_outside_a_request_needs_the_threshold(self):
        visionary.background("quick", lambda: None).join(5)
        self.assertEqual(self.agent.recorded, [])
        visionary.background("slow", time.sleep, 0.12).join(5)
        (sp,) = self.agent.recorded
        self.assertEqual((sp.kind, sp.name, sp.request_id, sp.offset_ms), ("background", "slow", None, None))

    def test_background_span_in_a_thread_pool(self):
        trace, end = self.begin_request("req-pool")
        gate = threading.Event()

        def job():
            with visionary.background_span("resize avatar"):
                gate.wait(5)
                _spans.start("db", "UPDATE users").finish()
            return _context.current().trace

        with ThreadPoolExecutor(max_workers=1) as pool:
            try:
                ctx = contextvars.copy_context()
                future = pool.submit(ctx.run, job)
            finally:
                end()
            trace.close()
            gate.set()
            self.assertIs(future.result(5), trace)
        kinds = sorted((s.kind, s.name, s.request_id) for s in self.agent.recorded)
        self.assertEqual(kinds, [("background", "resize avatar", "req-pool"), ("db", "UPDATE users", "req-pool")])

    def test_background_span_marks_an_exception_and_reraises(self):
        trace, end = self.begin_request()
        ctx = contextvars.copy_context()
        end()
        trace.close()

        def job():
            with visionary.background_span("sync"):
                raise KeyError("x")

        with self.assertRaises(KeyError):
            ctx.run(job)
        (sp,) = self.agent.recorded
        self.assertEqual((sp.status, sp.error, sp.request_id), ("error", "'x'", "req-1"))

    def test_background_span_with_asyncio_create_task(self):
        async def main():
            trace, end = self.begin_request("req-async")

            async def work():
                async with visionary.background_span("notify"):
                    await asyncio.sleep(0.01)

            task = asyncio.create_task(work())  # copies the context
            end()
            trace.close()
            await task

        asyncio.run(main())
        (sp,) = self.agent.recorded
        self.assertEqual((sp.kind, sp.name, sp.request_id), ("background", "notify", "req-async"))


if __name__ == "__main__":
    unittest.main()
