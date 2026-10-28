from __future__ import annotations

import logging
import time
from typing import Any, Callable, Iterable, Iterator, Optional

from .. import _agent, _context, _errors, _profiler
from .._agent import Agent
from .._entry import BodyCapture, Entry

logger = logging.getLogger("visionary")


def resolve_agent(agent: Optional[Agent]) -> Agent:
    """The given agent, else the one from visionary.init() (initialising it
    from the environment if nobody has)."""
    if agent is not None:
        return agent
    return _agent.get_agent() or _agent.init()


def finish(agent: Agent, entry: Entry, t0: float, status: int, trace: Any = None, ann: Any = None) -> None:
    """Close out a logged request: duration, status, call totals and spans,
    then queue it, and decide the log lines it held (``ann``)."""
    entry.status = status
    entry.duration = time.perf_counter() - t0
    _profiler.request_finished(entry.method, entry.route or entry.path, entry.duration)
    agent.finish_logs(ann, entry.route, status, entry.duration, entry.user_id)
    try:
        agent.finish_trace(entry, trace)
        agent.record(entry)
    except Exception:
        logger.exception("visionary: dropping request log entry")


def finish_unlogged(method: str, route: str, t0: float, agent: Optional[Agent] = None, ann: Any = None,
                    status: int = 200, pattern: str = "") -> None:
    """Close out a request that gets no api_logs row: the CPU profile's
    per-route time, and the log lines it held (``ann``; ``pattern`` is its
    route pattern, "" when none)."""
    seconds = time.perf_counter() - t0
    _profiler.request_finished(method, route, seconds)
    if agent is not None:
        agent.finish_logs(ann, pattern, status, seconds)


def report_unhandled(agent: Agent, exc: BaseException, info: Any = None) -> None:
    """Record an exception escaping the app (handled = 0) once, however many
    hooks see it. The caller re-raises it."""
    if not isinstance(exc, Exception):
        return
    _context.mark_failed()
    if agent.errors_on and _errors.mark_seen(exc):
        agent.capture_error(exc, False, info)


class CountingIterator:
    """Wraps a streaming response body: passes chunks through untouched,
    counts and caps a copy, and calls ``done`` once when the stream ends or
    is closed."""

    def __init__(self, inner: Iterable[Any], capture: BodyCapture, done: Callable[[], None]) -> None:
        self._inner = inner
        self._it: Iterator[Any] = iter(inner)
        self._capture = capture
        self._done: Optional[Callable[[], None]] = done

    def __iter__(self) -> "CountingIterator":
        return self

    def __next__(self) -> Any:
        try:
            chunk = next(self._it)
        except StopIteration:
            self._finish()
            raise
        except BaseException:
            self._finish()
            raise
        self._capture.feed(chunk.encode("utf-8") if isinstance(chunk, str) else bytes(chunk))
        return chunk

    def close(self) -> None:
        try:
            close = getattr(self._inner, "close", None)
            if close is not None:
                close()
        finally:
            self._finish()

    def _finish(self) -> None:
        done, self._done = self._done, None
        if done is not None:
            try:
                done()
            except Exception:
                logger.exception("visionary: finishing streamed request failed")
