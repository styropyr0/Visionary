"""W3C trace context (https://www.w3.org/TR/trace-context/).

A request continues the trace in its ``traceparent`` header or starts one;
the request and every call it makes get their own span id; outbound requests
carry the trace on, so the service they reach joins it.
"""

from __future__ import annotations

import random
import sys
from contextvars import ContextVar, Token
from typing import Any, Optional, Tuple

TRACEPARENT = "traceparent"
TRACESTATE = "tracestate"


class SpanContext:
    """The span that calls made in a context belong to."""

    __slots__ = ("trace_id", "span_id", "flags", "state")

    def __init__(self, trace_id: str, span_id: str, flags: str = "01", state: str = "") -> None:
        self.trace_id = trace_id
        self.span_id = span_id
        # flags is the trace-flags byte in hex ("01" sampled); state the
        # caller's tracestate, passed on unchanged.
        self.flags = flags or "01"
        self.state = state

    def child(self, span_id: str) -> "SpanContext":
        return SpanContext(self.trace_id, span_id, self.flags, self.state)

    def traceparent(self) -> str:
        return "00-%s-%s-%s" % (self.trace_id, self.span_id, self.flags)


_current: ContextVar[Optional[SpanContext]] = ContextVar("visionary_span", default=None)


def current_span() -> Optional[SpanContext]:
    """The innermost span: a custom or background span, else the request's
    server span. None outside any trace."""
    return _current.get()


def use(sc: Optional[SpanContext]) -> Token:
    return _current.set(sc)


def reset(token: Token) -> None:
    try:
        _current.reset(token)
    except ValueError:
        _current.set(None)


_HEX = frozenset("0123456789abcdef")


def _hex(s: str, n: int) -> bool:
    return len(s) == n and set(s) <= _HEX


def parse_traceparent(header: Optional[str]) -> Optional[SpanContext]:
    """The caller's span from a traceparent header, or None for anything
    malformed, version ff and all-zero ids."""
    if not header:
        return None
    parts = header.strip().split("-")
    if len(parts) < 4 or parts[0] == "ff":
        return None
    # Version 00 has exactly four fields; later versions may append more.
    if parts[0] == "00" and len(parts) != 4:
        return None
    version, trace_id, parent, flags = parts[:4]
    if not (_hex(version, 2) and _hex(trace_id, 32) and _hex(parent, 16) and _hex(flags, 2)):
        return None
    if not trace_id.strip("0") or not parent.strip("0"):
        return None
    return SpanContext(trace_id, parent, flags)


# random's generator is seeded from the OS; the ids are not secrets.
def new_trace_id() -> str:
    n = 0
    while n == 0:
        n = random.getrandbits(128)
    return "%032x" % n


def new_span_id() -> str:
    n = 0
    while n == 0:
        n = random.getrandbits(64)
    return "%016x" % n


def request_span(traceparent: Optional[str], tracestate: Optional[str] = None) -> Tuple[SpanContext, str]:
    """The server span of an incoming request and its parent span id ("" when
    it starts a trace). An active OpenTelemetry span wins over the header,
    since OpenTelemetry already read it."""
    parent = _otel_span() or parse_traceparent(traceparent)
    if parent is not None:
        if tracestate and not parent.state:
            parent.state = tracestate
        return parent.child(new_span_id()), parent.span_id
    return SpanContext(new_trace_id(), new_span_id()), ""


def _otel_span() -> Optional[SpanContext]:
    trace = sys.modules.get("opentelemetry.trace")
    if trace is None:
        return None
    try:
        ctx = trace.get_current_span().get_span_context()
        if not ctx.is_valid:
            return None
        state = ""
        if ctx.trace_state:
            state = ",".join("%s=%s" % kv for kv in ctx.trace_state.items())
        return SpanContext(format(ctx.trace_id, "032x"), format(ctx.span_id, "016x"),
                           "%02x" % int(ctx.trace_flags), state)
    except Exception:
        return None


def has_header(headers: Any, name: str) -> bool:
    """Case-insensitive lookup in a mapping or a list of pairs."""
    if headers is None:
        return False
    try:
        items = headers.items() if hasattr(headers, "items") else headers
        for k, _ in items:
            key = k.decode("latin-1") if isinstance(k, bytes) else str(k)
            if key.lower() == name:
                return True
    except Exception:
        return True  # unreadable: leave the request alone
    return False
