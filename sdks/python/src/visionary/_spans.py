"""Spans: database and outbound calls made while handling a request.

Instrumentation calls ``start()`` before a call and ``Span.finish()`` after
it. Inside a request the span lands on the request's ``Trace`` (found through
the contextvar in ``_context``); the totals always count and up to
``span_max`` spans are kept for the writer. Outside a request a span is
queued on its own when it took at least the threshold.

A span that ends after its request was logged (work the handler started and
did not wait for) keeps the request's id and offset but is not counted in its
totals. The first ``span_max`` such late spans of a request are queued
whatever their duration, later ones only when they took at least the
threshold. ``background`` spans (the work itself) are never counted.
"""

from __future__ import annotations

import re
import threading
import time
from typing import Any, List, Optional

from . import _context

KINDS = ("db", "http", "cache", "queue", "custom", "graphql", "background")
DETAIL_MAX = 8000
ERROR_MAX = 500

# The agent spans and errors go to: the last one started. Set by Agent.start().
_active: Any = None

# Which kinds of call the SDK can see; totals of the others are NULL.
db_visible = False
http_visible = False

_internal = threading.local()


def mark_internal() -> None:
    """Called on the SDK's own threads: their writes are never spans."""
    _internal.on = True


def is_internal() -> bool:
    return getattr(_internal, "on", False)


def set_active(agent: Any) -> None:
    global _active
    _active = agent


def clear_active(agent: Any) -> None:
    global _active
    if _active is agent:
        _active = None


def active() -> Any:
    return _active


class Trace:
    """The spans and call totals of one logged request."""

    __slots__ = ("request_id", "t0", "limit", "lock", "spans", "claimed", "closed", "late",
                 "db_count", "db_ms", "http_count", "http_ms")

    def __init__(self, request_id: str, t0: float, limit: int) -> None:
        self.request_id = request_id
        self.t0 = t0  # time.perf_counter() at request start
        self.limit = limit
        self.lock = threading.Lock()
        self.spans: List["Span"] = []
        self.claimed = 0
        self.closed = False
        self.late = 0  # spans that ended after the request was logged
        self.db_count = 0
        self.db_ms = 0.0
        self.http_count = 0
        self.http_ms = 0.0

    def claim(self) -> bool:
        """Whether a span starting now is among the first ``limit``. Decided
        at start so the kept spans are the earliest, even when nested."""
        with self.lock:
            self.claimed += 1
            return self.claimed <= self.limit

    def add(self, span: "Span") -> bool:
        """Count the span; False when the request already finished."""
        with self.lock:
            if self.closed:
                return False
            if span.kind == "db":
                self.db_count += 1
                self.db_ms += span.duration_ms
            elif span.kind == "http":
                self.http_count += 1
                self.http_ms += span.duration_ms
            if span.keep:
                self.spans.append(span)
            return True

    def claim_late(self) -> bool:
        """Whether a span ending after the request was logged is among the
        first ``limit`` such spans, which are stored whatever their duration."""
        with self.lock:
            self.late += 1
            return self.late <= self.limit

    def close(self) -> List["Span"]:
        with self.lock:
            self.closed = True
            spans = sorted(self.spans, key=lambda s: s.offset_ms or 0.0)
            self.spans = []
            return spans


class Span:
    """One call. ``finish`` records it; calling it again does nothing."""

    __slots__ = ("kind", "name", "target", "detail", "started_at", "_t0", "offset_ms", "duration_ms",
                 "status", "error", "row_count", "keep", "_trace", "_agent", "_done")

    def __init__(self, agent: Any, trace: Optional[Trace], kind: str, name: str,
                 target: Optional[str], detail: Optional[str]) -> None:
        self.kind = kind if kind in KINDS else "custom"
        self.name = (name or self.kind)[:255]
        self.target = target or None
        self.detail = detail[:DETAIL_MAX] if detail else None
        self.started_at = time.time()
        self._t0 = time.perf_counter()
        self.offset_ms: Optional[float] = None
        self.duration_ms = 0.0
        self.status = "ok"
        self.error: Optional[str] = None
        self.row_count: Optional[int] = None
        self._agent = agent
        self._trace = trace
        self.keep = False
        if trace is not None:
            self.offset_ms = round((self._t0 - trace.t0) * 1000.0, 3)
            self.keep = trace.claim()
        self._done = False

    @property
    def request_id(self) -> Optional[str]:
        return self._trace.request_id if self._trace is not None else None

    def set_error(self, error: Any) -> None:
        """Mark the call failed; error is an exception or a message."""
        self.status = "error"
        self.error = error_message(error)

    def finish(self, error: Any = None, row_count: Optional[int] = None) -> None:
        if self._done:
            return
        self._done = True
        self.duration_ms = round((time.perf_counter() - self._t0) * 1000.0, 3)
        if error is not None:
            self.set_error(error)
        if row_count is not None and row_count >= 0:
            self.row_count = int(row_count)
        try:
            trace = self._trace
            if trace is not None:
                if trace.add(self):
                    return
                # Ended after its request was logged: keep the request's id
                # and offset, stored like a lone span past the late cap.
                if trace.claim_late():
                    self._agent.record_span(self)
                    return
            if self.duration_ms >= self._agent.config.span_threshold_ms:
                self._agent.record_span(self)
        except Exception:
            pass  # instrumentation must never break the call it wraps


def start(kind: str, name: str, target: Optional[str] = None, detail: Optional[str] = None) -> Optional[Span]:
    """A started span, or None when spans are off or this is SDK work."""
    agent = _active
    if agent is None or not agent.spans_on or is_internal():
        return None
    ann = _context.current()
    trace = ann.trace if ann is not None else None
    return Span(agent, trace, kind, name, target, detail)


def error_message(error: Any) -> str:
    if isinstance(error, BaseException):
        text = str(error) or type(error).__name__
    else:
        text = str(error)
    return text[:ERROR_MAX]


# --- SQL ---------------------------------------------------------------------

_STRING = re.compile(r"'(?:[^']|'')*'")
_NUMBER = re.compile(r"(?<![\w$.:%])\d+(?:\.\d+)?(?:[eE][+-]?\d+)?\b")
_PLACEHOLDER = r"(?:\?|%s|%\(\w+\)s|\$\d+|:\w+)"
_IN_LIST = re.compile(r"\bIN\s*\(\s*(" + _PLACEHOLDER + r")(?:\s*,\s*" + _PLACEHOLDER + r")*\s*\)", re.IGNORECASE)
_SPACE = re.compile(r"\s+")


def sanitize_sql(sql: str) -> str:
    """Literals become ``?``, ``IN (?, ?, ...)`` becomes ``IN (?)``, and
    whitespace is collapsed (SPEC "Sanitizing statements")."""
    s = _STRING.sub("?", sql)
    s = _NUMBER.sub("?", s)
    s = _IN_LIST.sub(lambda m: "IN (" + m.group(1) + ")", s)
    return _SPACE.sub(" ", s).strip()[:DETAIL_MAX]


_FIRST_WORD = re.compile(r"\s*\(?\s*([A-Za-z]+)")
_IDENT = r"((?:[`\"\[]?[\w$]+[`\"\]]?\.)*[`\"\[]?[\w$]+[`\"\]]?)"
_TABLE_AFTER = {
    "SELECT": re.compile(r"\bFROM\s+" + _IDENT, re.IGNORECASE),
    "DELETE": re.compile(r"\bFROM\s+" + _IDENT, re.IGNORECASE),
    "INSERT": re.compile(r"\bINTO\s+" + _IDENT, re.IGNORECASE),
    "REPLACE": re.compile(r"\bINTO\s+" + _IDENT, re.IGNORECASE),
    "UPDATE": re.compile(r"^\s*UPDATE\s+(?:ONLY\s+)?" + _IDENT, re.IGNORECASE),
}


def sql_name(sql: str) -> str:
    """``SELECT users``, ``INSERT bookings``, ``BEGIN``: the first keyword and
    the main table, for grouping."""
    m = _FIRST_WORD.match(sql)
    if not m:
        return "query"
    word = m.group(1).upper()
    if word == "WITH":
        # A CTE: name it after the statement that follows the CTE list.
        tail = re.search(r"\)\s*(SELECT|INSERT|UPDATE|DELETE)\b", sql, re.IGNORECASE)
        word = tail.group(1).upper() if tail else "SELECT"
    pattern = _TABLE_AFTER.get(word)
    if pattern is not None:
        t = pattern.search(sql)
        if t:
            table = t.group(1).split(".")[-1].strip('`"[]')
            if table:
                return word + " " + table
    return word
