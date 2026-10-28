"""The ``logging`` side of application logs: ``LogHandler`` and
``capture_logging()`` (SPEC "Application logs", Hooks)."""

from __future__ import annotations

import logging
import sys
import threading
from typing import Any, Dict, Optional, Union

from . import _logs, _spans

# Read from a real record so every Python version's fields are covered.
_STANDARD = frozenset(logging.LogRecord("", 0, "", 0, "", (), None).__dict__) | {"message", "asctime"}

_formatter = logging.Formatter()
_busy = threading.local()


class LogHandler(logging.Handler):
    """Sends every record it gets to Visionary's ``app_logs``, linked to the
    request it was logged in. It prints nothing; add it next to your own
    handlers (``capture_logging()`` does that on the root logger)::

        logging.getLogger().addHandler(visionary.LogHandler())

    Records go to ``agent``, else the agent started last; before any starts
    they are ignored. Which ones are stored follows Settings -> Logs, so give
    the handler (and the loggers it hangs off) a level low enough for what
    you want kept: debug lines are stored for failed and slow requests.
    """

    def __init__(self, level: Union[int, str] = logging.NOTSET, agent: Any = None) -> None:
        super().__init__(level)
        self._agent = agent
        # Set by capture_logging, to keep what Python's last resort would have printed.
        self._last_resort = False
        self._keep: Optional["_KeepOutput"] = None

    def handle(self, record: logging.LogRecord) -> bool:
        if self._last_resort:
            _last_resort(self, record)
        # No handler lock: capture is thread-safe, and request threads mustn't queue behind each other.
        rv = self.filter(record)
        if rv:
            if isinstance(rv, logging.LogRecord):  # 3.12+: a filter may return a replacement
                record = rv
            self.emit(record)
        return bool(rv)

    def emit(self, record: logging.LogRecord) -> None:
        # Skip the SDK's own lines (its threads too, e.g. a DB driver) and re-entrant calls.
        name = record.name
        if name == "visionary" or name.startswith("visionary.") or _spans.is_internal():
            return
        if getattr(_busy, "on", False):
            return
        if self._agent is not None:
            pipe = self._agent.logs
        else:
            pipe = _logs.active()
        if pipe is None or not pipe.settings.enabled:
            return
        _busy.on = True
        try:
            pipe.capture(line_from_record(record))
        except Exception:
            # never break the app over a log line
            pass
        finally:
            _busy.on = False


def line_from_record(record: logging.LogRecord) -> _logs.LogLine:
    """A LogLine from a record: its message with the exception text, its
    extra fields as attrs, and where it was logged."""
    try:
        msg = record.getMessage()
    except Exception:
        msg = str(record.msg)
    if record.exc_info and record.exc_info[0] is not None:
        text = record.exc_text or _formatter.formatException(record.exc_info)
        if text:
            msg = msg + "\n" + text if msg else text
    elif record.exc_text:
        msg = msg + "\n" + record.exc_text if msg else record.exc_text
    if record.stack_info:
        msg = msg + "\n" + _formatter.formatStack(record.stack_info)

    attrs: Dict[str, Any] = {}
    for k, v in record.__dict__.items():
        if k not in _STANDARD and not k.startswith("_"):
            _logs.flatten(attrs, k + ".", v)
    line = _logs.LogLine(record.created, _logs.python_severity(record.levelno), msg, record.name)
    line.take(attrs)
    line.file = record.pathname or ""
    line.line = record.lineno or 0
    line.func = record.funcName or ""
    _otel(line)
    return line


def _otel(line: _logs.LogLine) -> None:
    """The active OpenTelemetry span, when the app uses OpenTelemetry (only
    looked up once it has been imported)."""
    trace = sys.modules.get("opentelemetry.trace")
    if trace is None or (line.trace_id and line.span_id):
        return
    try:
        ctx = trace.get_current_span().get_span_context()
        if not ctx.is_valid:
            return
        tid = format(ctx.trace_id, "032x")
        if not line.trace_id:
            line.trace_id = tid
        if not line.span_id and line.trace_id == tid:
            line.span_id = format(ctx.span_id, "016x")
    except Exception:
        pass


class _KeepOutput(logging.Filter):
    """Keeps a handler printing what it printed before capture_logging()
    lowered the root logger's level: a record below the old level passes only
    when its logger, or a parent below the root, has a level of its own (it
    was created then too)."""

    def __init__(self, level: int) -> None:
        super().__init__()
        self.level = level

    def filter(self, record: logging.LogRecord) -> bool:
        if record.levelno >= self.level:
            return True
        lg = logging.Logger.manager.loggerDict.get(record.name)
        root = logging.getLogger()
        while isinstance(lg, logging.Logger) and lg is not root:
            if lg.level:
                return True
            lg = lg.parent
        return False


def _level(level: Union[int, str, None]) -> int:
    if level is None:
        return logging.DEBUG
    if isinstance(level, int):
        return level
    sev = _logs.parse_level(level)
    if not sev:
        raise ValueError("visionary: unknown log level %r" % (level,))
    return {_logs.TRACE: 5, _logs.DEBUG: logging.DEBUG, _logs.INFO: logging.INFO, _logs.WARN: logging.WARNING,
            _logs.ERROR: logging.ERROR, _logs.FATAL: logging.CRITICAL}[sev]


def capture_logging(level: Union[int, str, None] = None, agent: Any = None) -> LogHandler:
    """Send everything logged through ``logging`` to Visionary too, without
    changing what the app prints. Call it once logging is configured
    (``basicConfig``, ``dictConfig``, Django's ``LOGGING``): those replace
    the root logger's handlers, and ``basicConfig`` does nothing once the
    root logger has one.

    Adds a ``LogHandler`` at ``level`` (default DEBUG, so a failed request's
    debug lines can be kept; 5 or "trace" for lower) to the root logger. When
    the root logger's level is above it, the root logger is lowered to it and
    every handler that exists now gets a filter that keeps its output as it
    was: it still prints only what the old root level let through (plus what
    loggers with a level of their own send it, as before). Handlers added
    later see the lower level unless they have a level of their own. An app
    with no handlers at all keeps Python's "last resort" output (warnings
    and above on stderr). Calling it again returns the same handler.
    """
    root = logging.getLogger()
    for h in root.handlers:
        if isinstance(h, LogHandler):
            return h
    want = _level(level)
    handler = LogHandler(want, agent)
    handler._last_resort = True
    before = root.level
    if before and want < before:
        keep = _KeepOutput(before)
        handler._keep = keep
        for h in _all_handlers():
            if not any(isinstance(f, _KeepOutput) for f in h.filters):
                h.addFilter(keep)
        root.setLevel(want or 1)
    root.addHandler(handler)
    return handler


def _all_handlers() -> list:
    out = list(logging.getLogger().handlers)
    for lg in list(logging.Logger.manager.loggerDict.values()):
        if isinstance(lg, logging.Logger):
            out.extend(h for h in lg.handlers if h not in out)
    return out


def _found_other(record: logging.LogRecord, me: logging.Handler) -> bool:
    """Whether a handler other than ``me`` sees the record, the way
    Logger.callHandlers looks for one."""
    lg: Optional[Any] = logging.Logger.manager.loggerDict.get(record.name)
    if not isinstance(lg, logging.Logger):
        lg = logging.getLogger()
    while lg is not None:
        if any(h is not me for h in lg.handlers):
            return True
        if not lg.propagate:
            return False
        lg = lg.parent
    return False


def _last_resort(me: LogHandler, record: logging.LogRecord) -> None:
    """Python prints records nobody handles through logging.lastResort
    (warnings and above, on stderr). With LogHandler on the root logger every
    record is handled, so it passes those on itself to keep that output."""
    resort = logging.lastResort
    if resort is None or record.levelno < resort.level or _found_other(record, me):
        return
    if me._keep is None or me._keep.filter(record):
        resort.handle(record)
