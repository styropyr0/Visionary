from __future__ import annotations

import logging
import time
from typing import Any, Callable, Iterable, Iterator, Optional

from .. import _agent
from .._agent import Agent
from .._entry import BodyCapture, Entry

logger = logging.getLogger("visionary")


def resolve_agent(agent: Optional[Agent]) -> Agent:
    """The given agent, else the one from visionary.init() (initialising it
    from the environment if nobody has)."""
    if agent is not None:
        return agent
    return _agent.get_agent() or _agent.init()


def finish(agent: Agent, entry: Entry, t0: float, status: int) -> None:
    """Close out a logged request: duration, status, queue it."""
    entry.status = status
    entry.duration = time.perf_counter() - t0
    try:
        agent.record(entry)
    except Exception:
        logger.exception("visionary: dropping request log entry")


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
