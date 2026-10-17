from __future__ import annotations

import logging
import queue
import threading
import time
from typing import Any, Dict, List, Optional, Tuple

from ._entry import Entry, format_ts
from ._redact import Redactor, error_fields
from ._storage import PURGE_CHUNK, Backend

_STOP = object()


class Schema:
    """Creates a table on first use and retries on later writes until it works,
    so a database that is down at startup doesn't leave the table missing."""

    def __init__(self, backend: Backend, table: str, logger: logging.Logger) -> None:
        self._backend = backend
        self._table = table
        self._logger = logger
        self.ready = False
        self._reported = False

    def ensure(self) -> None:
        if self.ready:
            return
        try:
            self._backend.create_schema(self._table)
            self.ready = True
        except Exception as e:
            # Once until it works; the failed writes that follow are logged anyway.
            if not self._reported:
                self._reported = True
                self._logger.error("visionary: create %s schema failed (will retry): %s", self._table, e)


class RequestWriter:
    """One background thread that batches api_logs rows."""

    def __init__(self, cfg: Any, backend: Backend, logger: logging.Logger) -> None:
        self._cfg = cfg
        self._backend = backend
        self._logger = logger
        self._redact = Redactor(cfg.redact_headers, cfg.redact_fields, cfg.redact_request_fields)
        self._batch_size = cfg.batch_size if 0 < cfg.batch_size <= 50 else 50
        self._flush_interval = cfg.flush_interval if cfg.flush_interval > 0 else 3.0
        self._queue_size = cfg.queue_size if cfg.queue_size > 0 else 5000
        self._code_field = cfg.error_code_field or "error.code"
        self._key_field = cfg.error_key_field or "error.key"
        self._schema = Schema(backend, "requests", logger)
        self._init_state()

    def _init_state(self) -> None:
        self._queue: "queue.Queue[Any]" = queue.Queue(maxsize=self._queue_size)
        self._dropped = 0
        self._dropped_lock = threading.Lock()
        self._stop = threading.Event()
        self._thread: Optional[threading.Thread] = None

    def start(self) -> None:
        self._thread = threading.Thread(target=self._run, name="visionary-requests", daemon=True)
        self._thread.start()

    def restart_after_fork(self) -> None:
        self._init_state()
        self.start()

    def log(self, e: Entry) -> None:
        """Enqueue without blocking; a full queue drops and counts the entry."""
        try:
            self._queue.put_nowait(e)
        except queue.Full:
            with self._dropped_lock:
                self._dropped += 1

    def close(self, timeout: float) -> bool:
        """Stop and flush; False if the thread was still busy at the timeout."""
        self._stop.set()
        try:
            self._queue.put_nowait(_STOP)
        except queue.Full:
            pass  # the loop checks the event between entries
        if self._thread is not None:
            self._thread.join(timeout)
            if self._thread.is_alive():
                self._logger.warning("visionary: request log flush timed out; %d pending", self._queue.qsize())
                return False
        return True

    def _run(self) -> None:
        self._schema.ensure()
        batch: List[Entry] = []
        next_flush = time.monotonic() + self._flush_interval
        while True:
            if self._stop.is_set():
                self._drain(batch)
                return
            try:
                item = self._queue.get(timeout=max(0.0, next_flush - time.monotonic()))
            except queue.Empty:
                item = None
            if item is not None and item is not _STOP:
                batch.append(item)
                if len(batch) >= self._batch_size:
                    self._write(batch)
                    batch = []
            if time.monotonic() >= next_flush:
                if batch:
                    self._write(batch)
                    batch = []
                self._report_drops()
                next_flush = time.monotonic() + self._flush_interval

    def _drain(self, batch: List[Entry]) -> None:
        while True:
            try:
                item = self._queue.get_nowait()
            except queue.Empty:
                break
            if item is _STOP:
                continue
            batch.append(item)
            if len(batch) >= self._batch_size:
                self._write(batch)
                batch = []
        if batch:
            self._write(batch)
        self._report_drops()

    def _report_drops(self) -> None:
        with self._dropped_lock:
            n, self._dropped = self._dropped, 0
        if n:
            self._logger.warning("visionary: request queue full; %d entries dropped", n)

    def _write(self, batch: List[Entry]) -> None:
        try:
            users = self._lookup_users(batch)
            rows = [self._row(e, users) for e in batch]
        except Exception:
            self._logger.exception("visionary: building request rows failed; %d entries dropped", len(batch))
            return
        err: Optional[Exception] = None
        for attempt in (1, 2, 3):
            self._schema.ensure()
            try:
                self._backend.insert("requests", rows)
                return
            except Exception as e:
                err = e
            if attempt < 3:
                time.sleep(attempt)
        self._logger.error("visionary: request batch write failed; %d entries dropped: %s", len(batch), err)

    def _lookup_users(self, batch: List[Entry]) -> Dict[str, Tuple[str, str]]:
        out: Dict[str, Tuple[str, str]] = {}
        lookup = self._cfg.lookup_user
        if lookup is None:
            return out
        for e in batch:
            uid = e.user_id
            if not uid or (e.user_handle and e.user_role) or uid in out:
                continue
            try:
                found = lookup(uid)
            except Exception as ex:
                self._logger.warning("visionary: lookup_user(%r) failed: %s", uid, ex)
                found = None
            if found:
                handle, role = found
                out[uid] = (handle or "", role or "")
            else:
                out[uid] = ("", "")
        return out

    def _row(self, e: Entry, users: Dict[str, Tuple[str, str]]) -> Tuple[Any, ...]:
        handle, role = e.user_handle, e.user_role
        if e.user_id in users:
            found_handle, found_role = users[e.user_id]
            handle = handle or found_handle
            role = role or found_role
        code = key = ""
        if e.status >= 400:
            code, key = error_fields(e.response_body, self._code_field, self._key_field)
        req_body = resp_body = ""
        if self._cfg.capture_bodies:
            req_body = self._redact.request_body(e.request_body, e.request_content_type)
            resp_body = self._redact.response_body(e.response_body)
        return (
            e.request_id,
            format_ts(e.started_at),
            e.method,
            e.path,
            e.route or None,
            e.query or None,
            int(e.status),
            round(e.duration * 1000.0, 3),
            e.ip or None,
            e.user_agent or None,
            e.user_id or None,
            handle or None,
            role or None,
            self._redact.headers(e.headers) or None,
            e.request_content_type or None,
            req_body or None,
            1 if e.request_truncated else 0,
            int(e.request_size),
            e.response_content_type or None,
            resp_body or None,
            1 if e.response_truncated else 0,
            int(e.response_size),
            code or None,
            key or None,
        )


class Retention:
    """Purges expired rows 60 s after start, then every 24 h."""

    def __init__(self, backend: Backend, logger: logging.Logger, tables: Dict[str, int]) -> None:
        self._backend = backend
        self._logger = logger
        self._tables = {t: d for t, d in tables.items() if d > 0}
        self._init_state()

    def _init_state(self) -> None:
        self._stop = threading.Event()
        self._thread: Optional[threading.Thread] = None

    @property
    def needed(self) -> bool:
        return bool(self._tables)

    def start(self) -> None:
        self._thread = threading.Thread(target=self._run, name="visionary-retention", daemon=True)
        self._thread.start()

    def restart_after_fork(self) -> None:
        self._init_state()
        self.start()

    def close(self) -> None:
        self._stop.set()

    def _run(self) -> None:
        wait = 60.0
        while not self._stop.wait(wait):
            for table, days in self._tables.items():
                self.purge(table, days)
            wait = 24 * 3600.0

    def purge(self, table: str, days: int) -> int:
        cutoff = format_ts(time.time() - days * 86400)
        total = 0
        for _ in range(200):
            if self._stop.is_set():
                break
            try:
                n = self._backend.purge(table, cutoff)
            except Exception as e:
                self._logger.error("visionary: retention purge of %s failed after %d rows: %s", table, total, e)
                return total
            total += n
            if n < PURGE_CHUNK:
                break
        if total:
            self._logger.info("visionary: retention purge of %s deleted %d rows older than %s", table, total, cutoff)
        return total
