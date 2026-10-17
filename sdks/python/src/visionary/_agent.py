from __future__ import annotations

import atexit
import logging
import os
import socket
import threading
import time
import weakref
from typing import Optional

from ._config import Config, from_env
from ._entry import Entry
from ._storage import Backend, open_backend
from ._system import HttpCounters, Recorder
from ._writer import RequestWriter, Retention

logger = logging.getLogger("visionary")


class Agent:
    """Runs request logging and system metrics for one process.

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
        if cfg.log_requests:
            self._requests = RequestWriter(cfg, self._backend, logger)
        if cfg.system_metrics and cfg.system_interval > 0:
            self._system = Recorder(cfg, instance, self._backend, self.http, logger)
        retention = {}
        if self._requests is not None:
            retention["requests"] = cfg.log_retention_days
        if self._system is not None:
            retention["system"] = cfg.system_retention_days
        self._retention = Retention(self._backend, logger, retention)

    @property
    def enabled(self) -> bool:
        """True when anything is collected (a database and at least one feature)."""
        return self._requests is not None or self._system is not None

    @property
    def logging_requests(self) -> bool:
        return self._requests is not None

    def should_log(self, path: str) -> bool:
        """Whether a request to path gets an api_logs row (integration API)."""
        return self._requests is not None and path.startswith(self.config.path_prefix)

    def record(self, entry: Entry) -> None:
        """Queue a captured request (integration API). Never blocks."""
        if self._requests is not None and not self._closed:
            self._requests.log(entry)

    def start(self) -> "Agent":
        """Launch the background threads. Tables are created on them, so a slow
        or unreachable database never delays startup. Safe to call twice."""
        with self._lock:
            if self._started or self._closed or not self.enabled:
                return self
            self._started = True
        if self._requests is not None:
            self._requests.start()
            logger.info("visionary: logging requests (path_prefix=%r, bodies=%s, retention_days=%d)",
                        self.config.path_prefix, self.config.capture_bodies, self.config.log_retention_days)
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
        idle = True
        if started:
            deadline = _Deadline(timeout)
            if self._retention is not None:
                self._retention.close()
            if self._requests is not None:
                idle = self._requests.close(deadline.left()) and idle
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

    def __enter__(self) -> "Agent":
        return self.start()

    def __exit__(self, *exc: object) -> None:
        self.close()


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
            agent = Agent(Config(log_requests=False, system_metrics=False), backend=_NoBackend())
        atexit.register(agent.close)
        _default = agent
        return agent


def get_agent() -> Optional[Agent]:
    """The agent set up by init(), if any."""
    return _default


class _NoBackend(Backend):
    """Placeholder for the disabled agent built after a failed init."""
