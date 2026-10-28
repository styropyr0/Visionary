"""Public telemetry API: manual spans and handled errors."""

from __future__ import annotations

import contextvars
import sys
import threading
from typing import Any, Callable, Optional

from . import _agent, _context, _spans


class span:
    """Time a block as a span (sync or async)::

        with visionary.span("cache", "GET session") as s:
            value = redis.get(key)
            s.set_row_count(1)

    ``kind`` is ``db``, ``http``, ``cache``, ``queue``, ``custom`` or
    ``background`` (anything else becomes ``custom``). An exception leaving
    the block marks the span as failed and propagates unchanged. Does nothing
    when spans are off.
    """

    __slots__ = ("_args", "_span")

    def __init__(self, kind: str, name: str, target: Optional[str] = None, detail: Optional[str] = None) -> None:
        self._args = (kind, name, target, detail)
        self._span: Optional[_spans.Span] = None

    def set_error(self, error: Any) -> None:
        """Mark the span failed without raising (e.g. a cache miss you treat as an error)."""
        if self._span is not None:
            self._span.set_error(error)

    def set_row_count(self, n: int) -> None:
        if self._span is not None and n is not None and n >= 0:
            self._span.row_count = int(n)

    def __enter__(self) -> "span":
        self._span = _spans.start(*self._args)
        return self

    def __exit__(self, exc_type: Any, exc: Any, tb: Any) -> None:
        if self._span is not None:
            self._span.finish(error=exc)

    async def __aenter__(self) -> "span":
        return self.__enter__()

    async def __aexit__(self, exc_type: Any, exc: Any, tb: Any) -> None:
        self.__exit__(exc_type, exc, tb)


class background_span(span):
    """Record work a request starts and does not wait for as one span of kind
    ``background``, for work you schedule yourself (an executor,
    ``asyncio.create_task``)::

        ctx = contextvars.copy_context()
        def send():
            with visionary.background_span("send welcome email"):
                mailer.send(user)
        executor.submit(ctx.run, send)

    Run it in a copy of the request's context so the span, and the calls made
    inside it, keep the request's ``request_id`` and ``offset_ms`` even after
    the response was sent. An exception leaving the block marks the span as
    failed and propagates. Background spans never count in the request's
    ``db_*``/``http_*`` totals. Outside a request the span is stored when it
    takes at least ``VISIONARY_SPAN_THRESHOLD_MS``, with ``request_id`` NULL.
    """

    __slots__ = ()

    def __init__(self, name: str) -> None:
        super().__init__("background", name)


def background(name: str, fn: Callable[..., Any], /, *args: Any, **kwargs: Any) -> threading.Thread:
    """Run ``fn(*args, **kwargs)`` on a new (non-daemon) thread, inside a
    ``background`` span called ``name``, with a copy of the current context,
    so it stays linked to the request that started it. Returns the started
    thread. The response does not wait for it, and the process waits for it
    at exit. If ``fn`` raises, the span is marked failed and the exception
    goes to ``threading.excepthook`` as usual."""
    ctx = contextvars.copy_context()

    def run() -> None:
        with background_span(name):
            fn(*args, **kwargs)

    thread = threading.Thread(target=ctx.run, args=(run,), name="visionary-background: " + str(name)[:64],
                              daemon=False)
    thread.start()
    return thread


def capture_exception(exc: Optional[BaseException] = None) -> None:
    """Record a handled exception (``handled = 1``): ``exc``, or the one being
    handled when called inside ``except``. Inside a request it is linked to
    that request. Never raises."""
    if exc is None:
        exc = sys.exc_info()[1]
    if exc is None:
        return
    _context.mark_failed()
    agent = _spans.active() or _agent.get_agent()
    if agent is not None:
        agent.capture_error(exc, True)


def instrument_sqlalchemy(engine: Any) -> Any:
    """Record the statements of an SQLAlchemy ``Engine``/``AsyncEngine`` as spans."""
    from ._db import instrument_sqlalchemy as instrument

    return instrument(engine)
