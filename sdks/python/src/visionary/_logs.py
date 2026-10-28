"""Application logs (SPEC "Application logs"): lines the service logs through
the ``logging`` module (see ``LogHandler``), linked to the request whose
context they were logged in, written to ``app_logs``.

What is kept follows the dashboard (Settings -> Logs), read from
``visionary_settings`` every 30 seconds like the profiling switch:

- ``logs.level``: lines below it are not stored...
- ...except inside a request that failed (5xx, an exception, a captured
  error) or was slow (``logs.slow_ms``): there the lines down to
  ``logs.buffer_level`` that were held back are written too, so every
  failure comes with its debug lines (``logs.buffer``).
- ``logs.overrides`` lower or raise the level for a service, a route or a
  user until a time, without a redeploy.
- ``logs.drop`` drops matching lines; ``logs.rate`` caps lines per minute.

Lines below the level are held in the request until it ends and then written
or dropped; nothing on a request path waits for the database. Scrubbing,
attribute encoding and file naming happen on the writer thread.
"""

from __future__ import annotations

import json
import logging
import math
import re
import socket
import threading
import time
from typing import Any, Dict, Iterable, List, Optional, Sequence, Tuple

from . import _context, _spans
from ._config import parse_bool
from ._entry import format_ts
from ._profiler import FrameNamer, parse_ts
from ._redact import DEFAULT_REDACT_FIELDS, REDACTED
from ._writer import RequestWriter, Schema

# Severities as stored in app_logs.severity, with their level names.
TRACE, DEBUG, INFO, WARN, ERROR, FATAL = 1, 2, 3, 4, 5, 6
LEVEL_NAMES = ("", "trace", "debug", "info", "warn", "error", "fatal")

MAX_MESSAGE = 8 << 10
MAX_ATTRS = 16 << 10
MAX_ATTR_VALUE = 1024  # a string attribute's bytes once attrs are over MAX_ATTRS
MAX_HELD = 500  # lines one request holds back; past it the oldest are dropped
SETTINGS_POLL_S = 30.0
QUEUE_SIZE = 10000

_HOST = socket.gethostname()

_LEVELS = {
    TRACE: ("trace", "trc", "finest", "finer", "verbose", "vrb"),
    DEBUG: ("debug", "dbg", "fine", "config"),
    INFO: ("info", "inf", "information", "informational", "notice", "log"),
    WARN: ("warn", "warning", "wrn"),
    ERROR: ("error", "err", "eror", "severe"),
    FATAL: ("fatal", "critical", "crit", "alert", "emerg", "emergency", "panic", "ftl"),
}
_BY_NAME = {name: sev for sev, names in _LEVELS.items() for name in names}


def level_name(sev: int) -> str:
    """A severity's level name; out-of-range severities are info."""
    return LEVEL_NAMES[sev] if TRACE <= sev <= FATAL else "info"


def parse_level(s: Any) -> int:
    """A level name the way sources spell them (trace, debug, info, notice,
    warn, warning, error, err, critical, fatal, panic...) as a severity;
    0 when unknown."""
    if not isinstance(s, str):
        return 0
    return _BY_NAME.get(s.strip().lower(), 0)


def python_severity(levelno: int) -> int:
    """A ``logging`` level number as a severity: below DEBUG is trace, then
    one per standard level, CRITICAL and above fatal."""
    if levelno < logging.DEBUG:
        return TRACE
    if levelno < logging.INFO:
        return DEBUG
    if levelno < logging.WARNING:
        return INFO
    if levelno < logging.ERROR:
        return WARN
    if levelno < logging.CRITICAL:
        return ERROR
    return FATAL


# --- levels in plain text ------------------------------------------------------

_LEVEL_FIELD = re.compile(r"""(?i)\b(?:level|lvl|severity)=["']?([a-z]+)""")
# A date, a time or both, as loggers print them.
_TIMESTAMP = re.compile(r"^[\[(]?[0-9][0-9/:.,\-TZ+]*[\])]?$")


def text_level(s: str) -> int:
    """The level a plain-text line states at its start, the way loggers print
    it: "ERROR ...", "[warn] ...", "error: ...", "2026-10-28 09:30:12 WARN
    ...", or a level=/lvl=/severity= field in the first 200 bytes. A bare
    lower-case word ("error rate is low") is not a level. 0 when the line
    states none. ``logging`` records carry their own level; this is for
    plain text, kept in step with the other SDKs."""
    head = _cut(s, 200)
    m = _LEVEL_FIELD.search(head)
    if m:
        n = parse_level(m.group(1))
        if n:
            return n
    for i, f in enumerate(head.split()):
        if i > 3:
            break
        if _TIMESTAMP.match(f):
            continue
        word = f.strip("[]<>():|")
        n = parse_level(word)
        if not n:
            return 0
        marked = f.startswith(("[", "<", "(")) or f.endswith((":", "]", "|"))
        return n if marked or word == word.upper() else 0
    return 0


def strip_level_word(s: str, sev: int) -> str:
    """Remove a leading upper-case level word that ``text_level`` read as sev
    ("INFO started" -> "started", "WARNING: disk" -> "disk"): the level has
    its own column."""
    word, sep, rest = s.partition(" ")
    if not sep or not rest.strip() or word != word.upper():
        return s
    w = word[:-1] if word.endswith(":") else word
    w = w.lower()
    return rest if w == level_name(sev) or w == "warning" else s


# --- secrets and encoding ------------------------------------------------------

# Secrets in free text, replaced before a line is stored. Every SDK uses these
# same patterns, in this order (SPEC "Application logs", Secrets).
_SECRETS = [
    (re.compile(r"-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(-----END [A-Z ]*PRIVATE KEY-----|$)"),
     "[REDACTED:private-key]"),
    (re.compile(r"\beyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}"), "[REDACTED:jwt]"),
    (re.compile(r"(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}"), r"\1 [REDACTED]"),
    (re.compile(r"\b(AKIA|ASIA)[0-9A-Z]{16}\b"), "[REDACTED:aws-key]"),
    (re.compile(r"""(?i)\b(password|passwd|pwd|secret|token|api[_-]?key|access[_-]?token|refresh[_-]?token"""
                r"""|client[_-]?secret)\b(["']?\s*[=:]\s*)("[^"]*"|'[^']*'|[^\s,;&"']+)"""), r"\1\2[REDACTED]"),
]


def scrub(s: str) -> str:
    """Replace credentials in s."""
    for pattern, repl in _SECRETS:
        s = pattern.sub(repl, s)
    return s


def secret_keys(extra: Iterable[str] = ()) -> frozenset:
    """Attribute names whose values are never stored: the request redaction
    defaults plus extra (VISIONARY_LOG_REDACT_FIELDS), lower case."""
    return frozenset(k.strip().lower() for k in list(DEFAULT_REDACT_FIELDS) + list(extra) if k and k.strip())


def _cut(s: str, n: int) -> str:
    """s cut to at most n UTF-8 bytes, on a character boundary."""
    if len(s) * 4 <= n:
        return s
    b = s.encode("utf-8", errors="replace")
    if len(b) <= n:
        return s
    return b[:n].decode("utf-8", errors="ignore")


def _size(s: str) -> int:
    return len(s.encode("utf-8", errors="replace"))


def _dumps(obj: Dict[str, Any]) -> str:
    return json.dumps(obj, ensure_ascii=False, sort_keys=True, separators=(",", ":"))


def encode_attrs(attrs: Dict[str, Any], secret: Iterable[str] = ()) -> str:
    """attrs as one flat JSON object, values under a secret key (its last
    dotted part) redacted and strings scrubbed, at most MAX_ATTRS bytes: long
    string values are cut first, then keys dropped in sorted order."""
    secret = secret if isinstance(secret, (set, frozenset)) else set(secret)
    out: Dict[str, Any] = {}
    for k, v in attrs.items():
        if k.rsplit(".", 1)[-1].lower() in secret:
            out[k] = REDACTED
        else:
            out[k] = scrub(v) if isinstance(v, str) else v
    text = _dumps(out)
    if _size(text) <= MAX_ATTRS:
        return text
    for k, v in out.items():
        if isinstance(v, str) and _size(v) > MAX_ATTR_VALUE:
            out[k] = _cut(v, MAX_ATTR_VALUE) + "…"
    text = _dumps(out)
    if _size(text) <= MAX_ATTRS:
        return text
    kept: Dict[str, Any] = {"_truncated": True}
    for k in sorted(out):
        kept[k] = out[k]
        if _size(_dumps(kept)) > MAX_ATTRS:
            del kept[k]
            break
    return _dumps(kept)


def attr_value(v: Any) -> Any:
    """A value as stored in attrs: strings, numbers and booleans as they
    are, times in RFC 3339, anything else as its string form."""
    if isinstance(v, (str, bool, int)):
        return v
    if isinstance(v, float):
        return v if math.isfinite(v) else str(v)
    iso = getattr(v, "isoformat", None)
    if iso is not None and hasattr(v, "year"):
        try:
            if getattr(v, "tzinfo", None) is not None:
                from datetime import timezone

                v = v.astimezone(timezone.utc)
                return v.isoformat().replace("+00:00", "Z")
            return iso()
        except Exception:
            pass
    try:
        return str(v)
    except Exception:
        return repr(type(v))


def flatten(out: Dict[str, Any], prefix: str, value: Any, depth: int = 0) -> None:
    """Nested dicts become dotted keys (``user.id``)."""
    if isinstance(value, dict) and depth < 8:
        for k, v in value.items():
            flatten(out, prefix + str(k) + ".", v, depth + 1)
        return
    if prefix:
        out[prefix[:-1]] = attr_value(value)


# --- settings ------------------------------------------------------------------


class Override:
    """Changes the level for part of the traffic until a time."""

    __slots__ = ("service", "route", "user", "sev", "until")

    def __init__(self, service: str, route: str, user: str, sev: int, until: Optional[float]) -> None:
        self.service, self.route, self.user, self.sev, self.until = service, route, user, sev, until

    def active(self, service: str, now: float) -> bool:
        if self.until is not None and now > self.until:
            return False
        return not self.service or self.service == service

    def matches(self, route: str, user: str) -> bool:
        """An override with neither route nor user applies to everything in its service."""
        if self.route and not route_matches(self.route, route):
            return False
        return not self.user or self.user == user


class Drop:
    """Drops the lines it matches; empty fields match anything, and a level
    drops only lines at or below it."""

    __slots__ = ("service", "logger", "contains", "sev")

    def __init__(self, service: str, logger: str, contains: str, sev: int) -> None:
        self.service, self.logger, self.contains, self.sev = service, logger, contains, sev


def route_matches(pattern: str, route: str) -> bool:
    """A route pattern, or a prefix ending in ``*``."""
    if pattern.endswith("*"):
        return route.startswith(pattern[:-1])
    return pattern == route


class LogSettings:
    """What Settings -> Logs asks for (visionary_settings ``logs.*``)."""

    __slots__ = ("enabled", "level", "buffer", "buffer_level", "slow_s", "rate", "overrides", "drops")

    def __init__(self, level: int = INFO) -> None:
        self.enabled = True
        self.level = level or INFO
        self.buffer = True
        self.buffer_level = DEBUG
        self.slow_s = 1.0
        self.rate = 6000
        self.overrides: List[Override] = []
        self.drops: List[Drop] = []

    def floor(self, service: str, route: str, user: str, now: float) -> int:
        """The lowest level kept for a line of service on route by user (both
        may be empty): the base level, then service-wide overrides, then the
        most specific matching override (a user beats a route). Later
        overrides win among equals."""
        level, rank = self.level, 0
        for o in self.overrides:
            if not o.active(service, now) or not o.matches(route, user):
                continue
            r = 3 if o.user else 2 if o.route else 1
            if r >= rank:
                level, rank = o.sev, r
        return level

    def hold_below(self, service: str, now: float) -> int:
        """The level under which a line logged in a request is held until the
        request ends: everything a route or user override could keep."""
        level = self.level
        for o in self.overrides:
            if o.active(service, now) and not o.route and not o.user:
                level = o.sev
        for o in self.overrides:
            if o.active(service, now) and (o.route or o.user):
                level = max(level, o.sev)
        return level

    def dropped(self, service: str, logger: str, message: str, sev: int) -> bool:
        for d in self.drops:
            if d.service and d.service != service:
                continue
            if d.logger and d.logger != logger:
                continue
            if d.sev and sev > d.sev:
                continue
            if d.contains and d.contains not in message:
                continue
            return True
        return False


def _json_list(v: str) -> List[Dict[str, Any]]:
    try:
        lst = json.loads(v)
    except ValueError:
        return []
    return [x for x in lst if isinstance(x, dict)] if isinstance(lst, list) else []


def _field(d: Dict[str, Any], key: str) -> str:
    v = d.get(key)
    return v if isinstance(v, str) else ""


def parse_log_settings(rows: Sequence[Sequence[Any]], base: int = INFO) -> LogSettings:
    """Settings from (name, value) rows of logs.*; anything missing or
    unreadable keeps its default (base is the level before any is read)."""
    s = LogSettings(base)
    for row in rows:
        if len(row) < 2:
            continue
        name, v = str(row[0]), "" if row[1] is None else str(row[1]).strip()
        if name == "logs.enabled":
            s.enabled = parse_bool(v, s.enabled)
        elif name == "logs.level":
            s.level = parse_level(v) or s.level
        elif name == "logs.buffer":
            s.buffer = parse_bool(v, s.buffer)
        elif name == "logs.buffer_level":
            s.buffer_level = parse_level(v) or s.buffer_level
        elif name == "logs.slow_ms":
            n = _number(v)
            if n is not None and n > 0:
                s.slow_s = n / 1000.0
        elif name == "logs.rate":
            try:
                n = int(v)
            except ValueError:
                continue
            if n >= 0:
                s.rate = n
        elif name == "logs.overrides":
            for d in _json_list(v):
                sev = parse_level(d.get("level"))
                if not sev:
                    continue
                until = None
                if _field(d, "until"):
                    until = parse_ts(_field(d, "until"))
                    if until is None:
                        continue
                s.overrides.append(Override(_field(d, "service"), _field(d, "route"), _field(d, "user"), sev, until))
        elif name == "logs.drop":
            for d in _json_list(v):
                drop = Drop(_field(d, "service"), _field(d, "logger"), _field(d, "contains"),
                            parse_level(d.get("level")))
                if drop.service or drop.logger or drop.contains or drop.sev:
                    s.drops.append(drop)
    return s


def _number(v: str) -> Optional[float]:
    try:
        n = float(v)
    except ValueError:
        return None
    return n if math.isfinite(n) else None


# --- lines and the pipeline ----------------------------------------------------


class LogLine:
    """One captured line. Scrubbing, encoding and file naming happen on the
    writer thread."""

    __slots__ = ("created_at", "sev", "logger", "message", "attrs", "request_id", "trace_id", "span_id",
                 "user_id", "session_id", "file", "line", "func")

    def __init__(self, created_at: float, sev: int, message: str, logger: str = "",
                 attrs: Optional[Dict[str, Any]] = None) -> None:
        self.created_at = created_at
        self.sev = sev
        self.logger = logger
        self.message = message
        self.attrs = attrs or None
        self.request_id = ""
        self.trace_id = ""
        self.span_id = ""
        self.user_id = ""
        self.session_id = ""
        self.file = ""  # the raw path; named like CPU profile frames when written
        self.line = 0
        self.func = ""

    def take(self, attrs: Dict[str, Any]) -> None:
        """Move the attributes that have columns of their own (request_id,
        trace_id, span_id, user_id, session_id, logger) out of attrs."""
        for k in ("request_id", "trace_id", "span_id", "user_id", "session_id", "logger"):
            v = attrs.get(k)
            if isinstance(v, bool) or not isinstance(v, (str, int)):
                continue
            setattr(self, k, str(v))
            del attrs[k]
        self.attrs = attrs or None


class Held:
    """A request's lines below its level, kept until it ends."""

    __slots__ = ("lines", "dropped")

    def __init__(self) -> None:
        self.lines: List[LogLine] = []
        self.dropped = 0


class LogPipeline:
    """Decides which lines are kept and queues them on the log writer."""

    def __init__(self, backend: Any, writer: "LogWriter", logger: logging.Logger, service: str = "",
                 base: int = INFO, poll_s: float = SETTINGS_POLL_S) -> None:
        self._backend = backend
        self.writer = writer
        self._logger = logger
        self._service = service
        self._base = base or INFO
        self._poll_s = poll_s
        self.settings = LogSettings(self._base)
        self._init_state()

    def _init_state(self) -> None:
        self._stop = threading.Event()
        self._thread: Optional[threading.Thread] = None
        self._read_failed = False
        # The per-minute cap: lines counted in the current minute, and those
        # dropped by it, reported once the minute ends.
        self._rate_lock = threading.Lock()
        self._rate_minute = 0
        self._rate_count = 0
        self._rate_drop = 0

    def start(self) -> None:
        self._thread = threading.Thread(target=self._run, name="visionary-log-settings", daemon=True)
        self._thread.start()

    def restart_after_fork(self) -> None:
        self._init_state()
        self.start()

    def close(self) -> None:
        self._stop.set()

    def _run(self) -> None:
        _spans.mark_internal()
        while not self._stop.is_set():
            self.poll()
            self._stop.wait(self._poll_s)

    def poll(self) -> None:
        """Read Settings -> Logs; a failed read keeps the previous values."""
        try:
            rows = self._backend.read_settings("logs")
        except Exception as e:
            # No server yet, or the table is missing: keep what we had.
            if not self._read_failed:
                self._read_failed = True
                self._logger.debug("visionary: log settings unavailable; using defaults (%s)", e)
            return
        self._read_failed = False
        self.settings = parse_log_settings(rows, self._base)

    def capture(self, line: LogLine, ann: Any = None) -> None:
        """Take one line logged in the request ``ann`` (the current one when
        None). Never blocks."""
        s = self.settings
        # The SDK's own messages are left out: a write failure logged here
        # would otherwise be queued behind the write that failed.
        if not s.enabled or line.message.startswith("visionary: "):
            return
        if s.dropped(self._service, line.logger, line.message, line.sev):
            return
        if ann is None:
            ann = _context.current()
        if ann is None:
            if line.sev >= s.floor(self._service, "", "", line.created_at):
                self.emit(line)
            return
        with ann._lock:
            if not line.request_id and ann.request is not None:
                line.request_id = ann.request.request_id or ""
            if not line.trace_id:
                line.trace_id = ann.trace_id
            if not line.user_id:
                line.user_id = ann.user_id
            if ann.done:
                # Work the request left running: its route and user are known now.
                route, user = ann.route, ann.user_id
            elif line.sev >= s.hold_below(self._service, line.created_at):
                route = user = None
            else:
                if ann.logs is None:
                    ann.logs = Held()
                held = ann.logs
                if len(held.lines) >= MAX_HELD:
                    del held.lines[0]
                    held.dropped += 1
                held.lines.append(line)
                return
        if route is None or line.sev >= s.floor(self._service, route, user, line.created_at):
            self.emit(line)

    def finish(self, ann: Any, route: str, failed: bool, seconds: float, user: str = "") -> None:
        """Decide a request's held lines once it has ended: kept when they
        reach the level for its route and user, or, when the request failed
        or was slow, the buffer's level. Lines logged later are decided at once."""
        with ann._lock:
            held, ann.logs = ann.logs, None
            ann.done = True
            ann.route = route
            if user and not ann.user_id:
                ann.user_id = user
            user = ann.user_id
        if held is None or not held.lines:
            return
        s = self.settings
        floor = s.floor(self._service, route, user, time.time())
        keep_all = s.buffer and (failed or (s.slow_s > 0 and seconds >= s.slow_s))
        for line in held.lines:
            if not line.user_id:
                line.user_id = user
            if line.sev >= floor or (keep_all and line.sev >= s.buffer_level):
                self.emit(line)

    def emit(self, line: LogLine) -> None:
        """Queue a line within the per-minute cap. Errors and fatals pass the
        cap up to ten times over, so a flood of info lines never hides them."""
        rate = self.settings.rate
        if rate > 0:
            minute = int(line.created_at // 60)
            report = 0
            with self._rate_lock:
                if minute != self._rate_minute:
                    report = self._rate_drop
                    self._rate_minute, self._rate_count, self._rate_drop = minute, 0, 0
                self._rate_count += 1
                n = self._rate_count
                over = n > rate and (line.sev < ERROR or n > 10 * rate)
                if over:
                    self._rate_drop += 1
            if report:
                self.writer.log(LogLine(time.time(), WARN, "visionary: %d log lines dropped over the cap of %d a "
                                        "minute (Settings → Logs)" % (report, rate), "visionary"))
            if over:
                return
        self.writer.log(line)


# The pipeline LogHandler captures into when it isn't given an agent: the
# last one started. Set by Agent.start().
_active: Optional[LogPipeline] = None


def set_active(p: LogPipeline) -> None:
    global _active
    _active = p


def clear_active(p: LogPipeline) -> None:
    global _active
    if _active is p:
        _active = None


def active() -> Optional[LogPipeline]:
    return _active


# --- the writer ----------------------------------------------------------------


class LogWriter(RequestWriter):
    """A background thread of its own for app_logs, so a flood of lines never
    crowds out request rows: the same batching and retries, with a queue of
    10000 lines."""

    _THREAD = "visionary-logs"
    _WHAT = "log"

    def __init__(self, cfg: Any, backend: Any, logger: logging.Logger, instance: str,
                 queue_size: int = QUEUE_SIZE) -> None:
        super().__init__(cfg, backend, logger, instance)
        self._queue_size = queue_size if queue_size > 0 else QUEUE_SIZE
        self._schemas = [Schema(backend, "logs", logger)]
        self._secrets = secret_keys(cfg.redact_fields)
        self._namer: Optional[FrameNamer] = None
        self._init_state()

    def _rows(self, batch: List[Any]) -> List[Tuple[str, List[Tuple[Any, ...]]]]:
        return [("logs", [self.row(line) for line in batch if isinstance(line, LogLine)])]

    def row(self, line: LogLine) -> Tuple[Any, ...]:
        """The app_logs row in LOG_COLUMNS order."""
        msg = _cut(scrub(line.message), MAX_MESSAGE)
        attrs = encode_attrs(line.attrs, self._secrets) if line.attrs else ""
        code_file = ""
        if line.file:
            if self._namer is None:
                self._namer = FrameNamer()
            code_file = self._namer.file(line.file)
        return (
            format_ts(line.created_at),
            self._service,
            self._instance or None,
            self._version,
            "python",
            _HOST or None,
            None,
            level_name(line.sev),
            line.sev,
            line.logger or None,
            msg,
            attrs or None,
            line.request_id or None,
            line.trace_id or None,
            line.span_id or None,
            line.user_id or None,
            line.session_id or None,
            code_file or None,
            line.line or None,
            line.func or None,
            _size(msg) + _size(attrs),
        )
