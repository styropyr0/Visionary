from __future__ import annotations

import gc
import logging
import os
import shutil
import threading
import time
import tracemalloc
from typing import Any, Dict, List, Optional, Tuple

from ._entry import format_ts
from ._storage import SYSTEM_COLUMNS, Backend
from ._writer import Schema

try:
    import psutil
except ImportError:  # host/process columns stay NULL without the "system" extra
    psutil = None  # type: ignore[assignment]

# One write per minute at the default 15 s interval.
SAMPLES_PER_WRITE = 4

_MB = float(1 << 20)


class HttpCounters:
    """Throughput seen by the integrations, read by the sampler."""

    __slots__ = ("_lock", "requests", "errors", "in_flight")

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self.requests = 0
        self.errors = 0
        self.in_flight = 0

    def started(self) -> None:
        with self._lock:
            self.in_flight += 1

    def finished(self, status: int) -> None:
        with self._lock:
            self.in_flight -= 1
            self.requests += 1
            if status >= 500:
                self.errors += 1

    def snapshot(self) -> Tuple[int, int, int]:
        with self._lock:
            return self.requests, self.errors, self.in_flight


class _GCTimer:
    """Accumulates collector pause time through gc.callbacks."""

    def __init__(self) -> None:
        self.total = 0.0
        self._start = 0.0

    def __call__(self, phase: str, info: Dict[str, Any]) -> None:
        if phase == "start":
            self._start = time.perf_counter()
        elif self._start:
            self.total += time.perf_counter() - self._start
            self._start = 0.0

    def install(self) -> None:
        if self not in gc.callbacks:
            gc.callbacks.append(self)

    def remove(self) -> None:
        try:
            gc.callbacks.remove(self)
        except ValueError:
            pass


def _gc_collections() -> int:
    return sum(s.get("collections", 0) for s in gc.get_stats())


class Recorder:
    """Samples host, process and runtime metrics on a background thread."""

    def __init__(self, cfg: Any, instance: str, backend: Backend, http: HttpCounters, logger: logging.Logger):
        self._interval = cfg.system_interval
        self._db_stats = cfg.db_stats
        self._disk_path = cfg.disk_path or os.getcwd()
        self._instance = instance
        self._backend = backend
        self._http = http
        self._logger = logger
        self._schema = Schema(backend, "system", logger)
        self._gc = _GCTimer()
        self._started = time.monotonic()
        self._init_state()

    def _init_state(self) -> None:
        self._buf: List[Tuple[Any, ...]] = []
        self._stop = threading.Event()
        self._thread: Optional[threading.Thread] = None
        self._proc = None
        if psutil is not None:
            try:
                self._proc = psutil.Process()
            except Exception:
                self._proc = None

    def start(self) -> None:
        self._gc.install()
        self._prime()
        self._thread = threading.Thread(target=self._run, name="visionary-system", daemon=True)
        self._thread.start()

    def restart_after_fork(self) -> None:
        self._init_state()
        self.start()

    def close(self, timeout: float) -> bool:
        """Stop and flush; False if the thread was still busy at the timeout."""
        self._stop.set()
        self._gc.remove()
        if self._thread is not None:
            self._thread.join(timeout)
            return not self._thread.is_alive()
        return True

    def _prime(self) -> None:
        """Baseline readings so the first sample has real deltas."""
        self._last = time.monotonic()
        self._requests, self._errors, _ = self._http.snapshot()
        self._net: Optional[Tuple[int, int]] = None
        self._gc_count = _gc_collections()
        self._gc_pause = self._gc.total
        self._db_prev: Dict[str, Any] = {}
        if psutil is not None:
            _quiet(lambda: psutil.cpu_percent(None))
            if self._proc is not None:
                _quiet(lambda: self._proc.cpu_percent(None))
            io = _quiet(psutil.net_io_counters)
            if io is not None:
                self._net = (io.bytes_recv, io.bytes_sent)
        if self._db_stats is not None:
            self._db_prev = _quiet(self._db_stats) or {}

    def _run(self) -> None:
        self._schema.ensure()
        next_at = time.monotonic() + self._interval
        while not self._stop.wait(max(0.0, next_at - time.monotonic())):
            next_at += self._interval
            now = time.monotonic()
            if next_at < now:  # fell behind (suspended host); don't burst
                next_at = now + self._interval
            try:
                self._buf.append(self.sample())
            except Exception:
                self._logger.exception("visionary: system sample failed")
            if len(self._buf) >= SAMPLES_PER_WRITE:
                self._flush()
        self._flush()

    def _flush(self) -> None:
        if not self._buf:
            return
        self._schema.ensure()
        try:
            self._backend.insert("system", list(self._buf))
        except Exception as e:
            self._logger.warning("visionary: system metrics write failed (%d rows kept): %s", len(self._buf), e)
            keep = max(1, int(3600 / self._interval))
            if len(self._buf) > keep:
                del self._buf[:-keep]
            return
        self._buf.clear()

    def sample(self) -> Tuple[Any, ...]:
        """Read every source once. A source that fails or doesn't exist on this
        OS is NULL, never an error."""
        now = time.monotonic()
        secs = now - self._last
        self._last = now
        if secs <= 0:
            secs = self._interval
        v: Dict[str, Any] = {
            "created_at": format_ts(time.time()),
            "instance": self._instance,
            "interval_s": round(secs, 2),
        }

        load = _quiet(os.getloadavg) if hasattr(os, "getloadavg") else None
        if load is not None:
            v["load_1"], v["load_5"], v["load_15"] = (round(x, 2) for x in load)

        self._disk(v)
        if psutil is not None:
            self._host(v, secs)
            self._process(v)

        v["goroutines"] = threading.active_count()
        if tracemalloc.is_tracing():
            v["heap_alloc_mb"] = round(tracemalloc.get_traced_memory()[0] / _MB, 1)

        count = _gc_collections()
        pause = self._gc.total
        pause_ms = (pause - self._gc_pause) * 1000.0
        v["gc_count"] = count - self._gc_count
        v["gc_pause_ms"] = round(pause_ms, 2)
        v["gc_cpu_percent"] = round(pause_ms / (secs * 1000.0) * 100.0, 2)
        self._gc_count, self._gc_pause = count, pause

        if self._db_stats is not None:
            self._db(v)

        reqs, errs, in_flight = self._http.snapshot()
        d_reqs, d_errs = reqs - self._requests, errs - self._errors
        self._requests, self._errors = reqs, errs
        v["http_requests"] = d_reqs
        v["http_errors"] = d_errs
        v["http_rps"] = round(d_reqs / secs, 2)
        v["http_in_flight"] = in_flight
        v["uptime_s"] = int(now - self._started)
        return tuple(v.get(c) for c in SYSTEM_COLUMNS)

    def _disk(self, v: Dict[str, Any]) -> None:
        if psutil is not None:
            du = _quiet(lambda: psutil.disk_usage(self._disk_path))
            if du is not None:
                v["disk_used_percent"] = round(du.percent, 1)
                v["disk_free_gb"] = round(du.free / (1 << 30), 2)
                return
        du = _quiet(lambda: shutil.disk_usage(self._disk_path))
        if du is not None and du.total:
            v["disk_used_percent"] = round(du.used / du.total * 100.0, 1)
            v["disk_free_gb"] = round(du.free / (1 << 30), 2)

    def _host(self, v: Dict[str, Any], secs: float) -> None:
        cpu = _quiet(lambda: psutil.cpu_percent(None))
        if cpu is not None:
            v["host_cpu_percent"] = round(cpu, 1)
        vm = _quiet(psutil.virtual_memory)
        if vm is not None:
            v["host_mem_used_mb"] = round(vm.used / _MB, 1)
            v["host_mem_total_mb"] = round(vm.total / _MB, 1)
            v["host_mem_percent"] = round(vm.percent, 1)
        io = _quiet(psutil.net_io_counters)
        if io is not None:
            rx, tx = io.bytes_recv, io.bytes_sent
            if self._net is not None and rx >= self._net[0] and tx >= self._net[1]:
                v["net_rx_kbps"] = round((rx - self._net[0]) / 1024.0 / secs, 1)
                v["net_tx_kbps"] = round((tx - self._net[1]) / 1024.0 / secs, 1)
            self._net = (rx, tx)

    def _process(self, v: Dict[str, Any]) -> None:
        p = self._proc
        if p is None:
            return
        cpu = _quiet(lambda: p.cpu_percent(None))
        if cpu is not None:
            v["proc_cpu_percent"] = round(cpu, 1)
        mem = _quiet(p.memory_info)
        if mem is not None:
            v["proc_rss_mb"] = round(mem.rss / _MB, 1)
        v["proc_threads"] = _quiet(p.num_threads)
        if hasattr(p, "num_fds"):
            v["proc_open_fds"] = _quiet(p.num_fds)
        elif hasattr(p, "num_handles"):
            v["proc_open_fds"] = _quiet(p.num_handles)

    def _db(self, v: Dict[str, Any]) -> None:
        st = _quiet(self._db_stats)
        if not isinstance(st, dict):
            return
        for key, col in (("total", "db_total_conns"), ("idle", "db_idle_conns"),
                         ("in_use", "db_acquired_conns"), ("max", "db_max_conns")):
            if st.get(key) is not None:
                v[col] = int(st[key])
        prev = self._db_prev
        for key, col in (("acquires", "db_acquires"), ("empty_acquires", "db_empty_acquires")):
            if st.get(key) is not None:
                v[col] = int(st[key]) - int(prev.get(key) or 0)
        if st.get("acquire_wait_ms") is not None:
            v["db_acquire_wait_ms"] = round(float(st["acquire_wait_ms"]) - float(prev.get("acquire_wait_ms") or 0), 1)
        self._db_prev = st


def _quiet(fn: Any) -> Any:
    try:
        return fn()
    except Exception:
        return None
