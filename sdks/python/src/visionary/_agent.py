from __future__ import annotations

import atexit
import logging
import os
import socket
import threading
import time
import weakref
from typing import Callable, Optional

from . import _context, _errors, _logs, _spans
from ._config import Config, from_env
from ._context import RequestInfo
from ._entry import Entry
from ._profiler import Profiler
from ._storage import Backend, open_backend
from ._system import HttpCounters, Recorder
from ._writer import Release, RequestWriter, Retention

logger = logging.getLogger("visionary")


class Agent:
    """Runs request logging, telemetry (spans, errors, releases) and system
    metrics for one process.

    With no database configured the agent is a no-op: it logs one warning
    and every method does nothing. It never raises into the host app once
    constructed.
    """

    def __init__(self, config: Optional[Config] = None, *, backend: Optional[Backend] = None) -> None:
        cfg = config if config is not None else from_env()
        self.config = cfg
        self.http = HttpCounters()
        self._backend: Optional[Backend] = None
        self._requests: Optional[RequestWriter] = None
        self._system: Optional[Recorder] = None
        self._retention: Optional[Retention] = None
        self._profiler: Optional[Profiler] = None
        # Own writer, so a flood of log lines never crowds out request rows.
        self._logs: Optional[_logs.LogPipeline] = None
        self._error_limit = _errors.RateLimiter()
        self.spans_on = False
        self.errors_on = False
        self._started = False
        self._closed = False
        self._lock = threading.Lock()

        try:
            self._backend = backend if backend is not None else open_backend(cfg)
        except Exception as e:
            logger.warning("%s; collection is off", e)
            return
        if self._backend is None:
            logger.warning("visionary: no database configured (set VISIONARY_DATABASE_URL or D1_*); collection is off")
            return

        instance = cfg.instance or socket.gethostname()
        self.instance = instance
        if cfg.log_requests or cfg.spans or cfg.errors or cfg.version:
            # One writer for api_logs and the telemetry tables.
            self._requests = RequestWriter(cfg, self._backend, logger, instance, _errors.runtime())
            self.spans_on = cfg.spans
            self.errors_on = cfg.errors
        if self._requests is not None and cfg.profile:
            self._profiler = Profiler(self._backend, self._requests.log, logger, cfg.service,
                                      cfg.log_retention_days)
        if cfg.logs:
            writer = _logs.LogWriter(cfg, self._backend, logger, instance, cfg.log_queue_size)
            self._logs = _logs.LogPipeline(self._backend, writer, logger, cfg.service,
                                           _logs.parse_level(cfg.log_level) or _logs.INFO)
        if cfg.system_metrics and cfg.system_interval > 0:
            self._system = Recorder(cfg, instance, self._backend, self.http, logger)
        retention = {}
        if self._requests is not None and cfg.log_requests:
            retention["requests"] = cfg.log_retention_days
        if self.spans_on:
            retention["spans"] = cfg.log_retention_days
        if self.errors_on:
            retention["errors"] = cfg.log_retention_days
        if self._logs is not None:
            retention["logs"] = cfg.log_retention_days
        if self._system is not None:
            retention["system"] = cfg.system_retention_days
        self._retention = Retention(self._backend, logger, retention)

    @property
    def enabled(self) -> bool:
        """True when anything is collected (a database and at least one feature)."""
        return self._requests is not None or self._system is not None or self._logs is not None

    @property
    def logs(self) -> Optional[_logs.LogPipeline]:
        """The log pipeline LogHandler captures into, or None when logs are off."""
        return self._logs

    @property
    def logging_requests(self) -> bool:
        return self._requests is not None and self.config.log_requests

    def should_log(self, path: str) -> bool:
        """Whether a request to path gets an api_logs row (integration API)."""
        return self.logging_requests and path.startswith(self.config.path_prefix)

    def record(self, entry: Entry) -> None:
        """Queue a captured request (integration API). Never blocks."""
        if self.logging_requests and not self._closed:
            self._requests.log(entry)

    def new_trace(self, request_id: str, t0: float) -> Optional[_spans.Trace]:
        """The span collector for a logged request (integration API); t0 is
        its time.perf_counter() start."""
        if not self.spans_on:
            return None
        return _spans.Trace(request_id, t0, self.config.span_max)

    def finish_trace(self, entry: Entry, trace: Optional[_spans.Trace]) -> None:
        """Copy call totals and the spans onto the entry (integration API,
        after status and duration are set)."""
        if trace is None:
            return
        entry.spans = trace.close()
        if _spans.db_visible:
            entry.db_count, entry.db_ms = trace.db_count, trace.db_ms
        if _spans.http_visible:
            entry.http_count, entry.http_ms = trace.http_count, trace.http_ms

    def finish_logs(self, ann: Optional[_context.Annotations], route: str, status: int, seconds: float,
                    user: str = "") -> None:
        """Decide the log lines a request held back, once it has ended
        (integration API). route is its route pattern, "" when none."""
        if self._logs is None or ann is None:
            return
        try:
            if not user and not ann.user_id and ann.logs is not None and ann.request is not None:
                user = ann.request.user_id(ann)
            self._logs.finish(ann, route, status >= 500 or ann.failed, seconds, user)
        except Exception:
            logger.exception("visionary: deciding a request's log lines failed")

    def record_span(self, span: _spans.Span) -> None:
        """Queue a span recorded outside a request, or one that ended after
        its request was logged. Never blocks."""
        if self.spans_on and not self._closed:
            self._requests.log(span)

    def capture_error(self, exc: BaseException, handled: bool, info: Optional[RequestInfo] = None) -> None:
        """Queue an errors row for exc, rate limited per fingerprint. Never
        raises."""
        if not self.errors_on or self._closed or exc is None:
            return
        try:
            ev = _errors.build(exc, handled, info)
            if self._error_limit.allow(ev.fingerprint):
                self._requests.log(ev)
        except Exception:
            logger.exception("visionary: capturing an error failed")

    def default_db_stats(self, fn: Callable[[], Optional[dict]]) -> None:
        """Fill the db_* columns from fn unless the app set config.db_stats
        (integration API). Works before or after start()."""
        if self._system is not None and self.config.db_stats is None:
            self._system.set_default_db_stats(fn)

    def start(self) -> "Agent":
        """Launch the background threads. Tables are created on them, so a slow
        or unreachable database never delays startup. Safe to call twice."""
        with self._lock:
            if self._started or self._closed or not self.enabled:
                return self
            self._started = True
        if self._requests is not None:
            self._requests.start()
            if self.config.version:
                self._requests.log(Release(time.time(), _sdk_version(), _errors.runtime()))
            if self.logging_requests:
                logger.info("visionary: logging requests (path_prefix=%r, bodies=%s, retention_days=%d)",
                            self.config.path_prefix, self.config.capture_bodies, self.config.log_retention_days)
            if self.spans_on or self.errors_on:
                _spans.set_active(self)
            if self.spans_on and self.config.instrument_http:
                from ._http import instrument_http

                instrument_http()
        if self._profiler is not None:
            self._profiler.start()
        if self._logs is not None:
            self._logs.writer.start()
            self._logs.start()
            _logs.set_active(self._logs)
        if self._system is not None:
            self._system.start()
            logger.info("visionary: recording system metrics every %ss", self.config.system_interval)
        if self._retention is not None and self._retention.needed:
            self._retention.start()
        _track_for_fork(self)
        return self

    def close(self, timeout: float = 10) -> None:
        """Flush queued requests and buffered samples, waiting at most timeout
        seconds in total."""
        with self._lock:
            if self._closed:
                return
            self._closed = True
            started = self._started
        _spans.clear_active(self)
        if self._logs is not None:
            _logs.clear_active(self._logs)
        idle = True
        if started:
            deadline = _Deadline(timeout)
            if self._profiler is not None:
                self._profiler.close()
            if self._retention is not None:
                self._retention.close()
            if self._requests is not None:
                idle = self._requests.close(deadline.left()) and idle
            if self._logs is not None:
                self._logs.close()
                idle = self._logs.writer.close(deadline.left()) and idle
            if self._system is not None:
                idle = self._system.close(deadline.left()) and idle
        # A worker still busy (e.g. stuck connecting) keeps its connection;
        # it is a daemon thread and goes away with the process.
        if idle and self._backend is not None:
            try:
                self._backend.close()
            except Exception:
                pass

    def _after_fork(self) -> None:
        # Threads don't survive fork (e.g. gunicorn --preload): restart them
        # in the child with fresh queues and connections.
        if not self._started or self._closed:
            return
        self._lock = threading.Lock()
        self.http = HttpCounters()
        if self._backend is not None:
            self._backend.after_fork()
        if self._system is not None:
            self._system._http = self.http
            self._system.restart_after_fork()
        if self._requests is not None:
            self._requests.restart_after_fork()
        if self._retention is not None and self._retention.needed:
            self._retention.restart_after_fork()
        if self._profiler is not None:
            self._profiler.restart_after_fork()
        if self._logs is not None:
            self._logs.writer.restart_after_fork()
            self._logs.restart_after_fork()

    def __enter__(self) -> "Agent":
        return self.start()

    def __exit__(self, *exc: object) -> None:
        self.close()


def _sdk_version() -> str:
    from . import __version__

    return __version__


class _Deadline:
    def __init__(self, seconds: float) -> None:
        self._end = time.monotonic() + max(0.0, seconds)

    def left(self) -> float:
        return max(0.0, self._end - time.monotonic())


_fork_agents: "weakref.WeakSet[Agent]" = weakref.WeakSet()
_fork_hooked = False


def _track_for_fork(agent: Agent) -> None:
    global _fork_hooked
    _fork_agents.add(agent)
    if not _fork_hooked and hasattr(os, "register_at_fork"):
        os.register_at_fork(after_in_child=lambda: [a._after_fork() for a in list(_fork_agents)])
        _fork_hooked = True


_default: Optional[Agent] = None
_default_lock = threading.Lock()


def init(config: Optional[Config] = None) -> Agent:
    """Build the process-wide agent (from the environment unless config is
    given), start it and flush it at exit. Returns the existing agent when
    called again. Never raises: on any problem it returns a no-op agent."""
    global _default
    with _default_lock:
        if _default is not None:
            return _default
        try:
            agent = Agent(config)
            agent.start()
        except Exception:
            logger.exception("visionary: init failed; collection is off")
            agent = Agent(Config(log_requests=False, system_metrics=False, spans=False, errors=False, logs=False),
                          backend=_NoBackend())
        atexit.register(agent.close)
        _default = agent
        return agent


def get_agent() -> Optional[Agent]:
    """The agent set up by init(), if any."""
    return _default


class _NoBackend(Backend):
    """Placeholder for the disabled agent built after a failed init."""
