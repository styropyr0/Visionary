"""Counters, gauges and histograms, written as one ``metrics`` row per series every interval"""

from __future__ import annotations

import hashlib
import logging
import math
import threading
import time
from bisect import bisect_left
from decimal import Decimal
from typing import Any, Dict, List, Mapping, Optional, Sequence, Tuple

from . import _spans
from ._entry import format_ts
from ._writer import RequestWriter, Schema

MAX_NAME = 200
MAX_LABELS = 20
MAX_LABEL_KEY = 100
MAX_LABEL_VALUE = 200
DEFAULT_MAX_SERIES = 1000
QUEUE_SIZE = 20000

# OpenTelemetry's default bounds, in milliseconds
DEFAULT_BUCKETS = (0.0, 5.0, 10.0, 25.0, 50.0, 75.0, 100.0, 250.0, 500.0, 750.0, 1000.0, 2500.0, 5000.0, 7500.0,
                   10000.0)

COUNTER, GAUGE, HISTOGRAM = "counter", "gauge", "histogram"


def _finite(v: Any) -> Optional[float]:
    try:
        f = float(v)
    except (TypeError, ValueError):
        return None
    return f if math.isfinite(f) else None


class _Series:
    __slots__ = ("name", "kind", "unit", "labels", "id", "bounds", "lock", "touched", "total", "value",
                 "counts", "count", "sum", "min", "max")

    def __init__(self, name: str, kind: str, unit: str, labels: str, sid: str, bounds: Sequence[float]) -> None:
        self.name, self.kind, self.unit, self.labels, self.id = name, kind, unit, labels, sid
        self.bounds = tuple(bounds)
        self.lock = threading.Lock()
        self.touched = False
        self.total = 0.0
        self.value = 0.0
        self.counts = [0] * (len(self.bounds) + 1) if kind == HISTOGRAM else []
        self.count = 0
        self.sum = 0.0
        self.min = 0.0
        self.max = 0.0


class Counter:
    __slots__ = ("_s",)

    def __init__(self, s: Optional[_Series]) -> None:
        self._s = s

    def add(self, n: float = 1) -> None:
        """Add ``n``, ignored unless positive and finite"""
        v = _finite(n)
        s = self._s
        if s is None or v is None or v <= 0:
            return
        with s.lock:
            s.total += v
            s.touched = True

    def inc(self) -> None:
        self.add(1)


class Gauge:
    __slots__ = ("_s",)

    def __init__(self, s: Optional[_Series]) -> None:
        self._s = s

    def set(self, value: float) -> None:
        v = _finite(value)
        s = self._s
        if s is None or v is None:
            return
        with s.lock:
            s.value, s.touched = v, True

    def add(self, delta: float) -> None:
        """Move the gauge by ``delta``, from 0 when it was never set"""
        v = _finite(delta)
        s = self._s
        if s is None or v is None:
            return
        with s.lock:
            s.value += v
            s.touched = True


class Histogram:
    __slots__ = ("_s",)

    def __init__(self, s: Optional[_Series]) -> None:
        self._s = s

    def observe(self, value: float) -> None:
        v = _finite(value)
        s = self._s
        if s is None or v is None:
            return
        i = bisect_left(s.bounds, v)
        with s.lock:
            s.counts[i] += 1
            if s.count == 0 or v < s.min:
                s.min = v
            if s.count == 0 or v > s.max:
                s.max = v
            s.count += 1
            s.sum += v


class Registry:
    def __init__(self) -> None:
        self.lock = threading.Lock()
        self.series: Dict[Tuple[Any, ...], _Series] = {}
        self.per_name: Dict[str, int] = {}
        self.dropped: Dict[str, int] = {}
        self.opts: Dict[str, Tuple[str, Tuple[float, ...]]] = {}
        self.max_series = DEFAULT_MAX_SERIES

    def get(self, name: Any, kind: str, labels: Optional[Mapping[Any, Any]]) -> Optional[_Series]:
        name = clean_name(name)
        if not name:
            return None
        pairs = {} if not labels else {str(k): "" if v is None else str(v) for k, v in labels.items()}
        key = (name, kind) + tuple(sorted(pairs.items()))
        with self.lock:
            s = self.series.get(key)
            if s is not None:
                return s
            if self.per_name.get(name, 0) >= self.max_series:
                self.dropped[name] = self.dropped.get(name, 0) + 1
                return None
            text, sid = series_id(name, pairs)
            unit, bounds = self.opts.get(name, ("", ()))
            s = _Series(name, kind, unit, text, sid, (bounds or DEFAULT_BUCKETS) if kind == HISTOGRAM else ())
            self.series[key] = s
            self.per_name[name] = self.per_name.get(name, 0) + 1
            return s

    def collect(self, at: float) -> Tuple[List["MetricRow"], Dict[str, int]]:
        with self.lock:
            all_series = list(self.series.values())
            dropped, self.dropped = self.dropped, {}
        out = []
        for s in all_series:
            row = None
            with s.lock:
                if s.kind == COUNTER:
                    if s.total > 0:
                        row = MetricRow(at, s, s.total)
                        s.total = 0.0
                elif s.kind == GAUGE:
                    if s.touched:
                        row = MetricRow(at, s, s.value)
                elif s.count > 0:
                    row = MetricRow(at, s, s.sum / s.count)
                    row.histogram = (s.count, s.sum, s.min, s.max, encode_buckets(s.bounds, s.counts))
                    s.count, s.sum, s.min, s.max = 0, 0.0, 0.0, 0.0
                    s.counts = [0] * len(s.counts)
            if row is not None:
                out.append(row)
        out.sort(key=lambda r: r.series)
        return out, dropped


class MetricRow:
    __slots__ = ("at", "name", "kind", "unit", "labels", "series", "value", "histogram")

    def __init__(self, at: float, s: _Series, value: float) -> None:
        self.at = at
        self.name, self.kind, self.unit, self.labels, self.series = s.name, s.kind, s.unit, s.labels, s.id
        self.value = value
        self.histogram: Optional[Tuple[int, float, float, float, str]] = None


registry = Registry()


def counter(name: str, labels: Optional[Mapping[str, Any]] = None, /, **kw: Any) -> Counter:
    """The counter ``name``, labels as keyword arguments or a dict"""
    return Counter(registry.get(name, COUNTER, _labels(labels, kw)))


def gauge(name: str, labels: Optional[Mapping[str, Any]] = None, /, **kw: Any) -> Gauge:
    """The gauge ``name``, labels as keyword arguments or a dict"""
    return Gauge(registry.get(name, GAUGE, _labels(labels, kw)))


def histogram(name: str, labels: Optional[Mapping[str, Any]] = None, /, **kw: Any) -> Histogram:
    """The histogram ``name``, labels as keyword arguments or a dict"""
    return Histogram(registry.get(name, HISTOGRAM, _labels(labels, kw)))


def describe_metric(name: str, unit: Optional[str] = None, buckets: Optional[Sequence[float]] = None) -> None:
    """Set a metric's unit and histogram buckets, before its first use"""
    name = clean_name(name)
    if not name:
        return
    bounds: Tuple[float, ...] = ()
    if buckets:
        bounds = tuple(sorted({v for v in (_finite(b) for b in buckets) if v is not None}))
    with registry.lock:
        registry.opts[name] = (unit or "", bounds)


def _labels(labels: Optional[Mapping[str, Any]], kw: Dict[str, Any]) -> Optional[Mapping[str, Any]]:
    if labels and kw:
        merged = dict(labels)
        merged.update(kw)
        return merged
    return labels or kw


def _cut(s: str, n: int) -> str:
    b = s.encode("utf-8", errors="replace")
    if len(b) <= n:
        return s
    return b[:n].decode("utf-8", errors="ignore")


def clean_name(name: Any) -> str:
    return _cut(str(name or "").strip(), MAX_NAME)


def series_id(name: str, labels: Mapping[str, str]) -> Tuple[str, str]:
    """Labels as JSON with sorted keys, and the first 16 hex digits of SHA-256 over name, newline, JSON"""
    clean: Dict[str, str] = {}
    for k, v in labels.items():
        k = _cut(k.strip(), MAX_LABEL_KEY)
        if k:
            clean[k] = _cut(v, MAX_LABEL_VALUE)
    keys = sorted(clean, key=lambda k: k.encode("utf-8", errors="replace"))[:MAX_LABELS]
    text = "{" + ",".join(_json_str(k) + ":" + _json_str(clean[k]) for k in keys) + "}"
    digest = hashlib.sha256((name + "\n" + text).encode("utf-8", errors="replace")).hexdigest()
    return text, digest[:16]


def _json_str(s: str) -> str:
    """Escape only what JSON requires, so every SDK writes the same bytes"""
    out = ['"']
    for c in s:
        if c == '"' or c == "\\":
            out.append("\\" + c)
        elif c < " ":
            out.append("\\u%04x" % ord(c))
        else:
            out.append(c)
    out.append('"')
    return "".join(out)


def _num(v: float) -> str:
    """A number as Go's encoding/json writes it"""
    if v == int(v) and abs(v) < 1e21:
        return str(int(v))
    a = abs(v)
    if a < 1e-6 or a >= 1e21:
        mant, exp = repr(v).split("e")
        return mant + "e" + exp[0] + exp[1:].lstrip("0")
    return format(Decimal(repr(v)), "f")


def encode_buckets(bounds: Sequence[float], counts: Sequence[int]) -> str:
    return '{"bounds":[%s],"counts":[%s]}' % (",".join(_num(b) for b in bounds), ",".join(str(c) for c in counts))


class MetricsWriter(RequestWriter):
    """The table is created on the first write, so services without custom metrics never get it"""

    _THREAD = "visionary-metrics"
    _WHAT = "metric"

    def __init__(self, cfg: Any, backend: Any, logger: logging.Logger, instance: str) -> None:
        super().__init__(cfg, backend, logger, instance)
        self._queue_size = QUEUE_SIZE
        self._schemas = []
        self._metrics_schema = Schema(backend, "metrics", logger)
        self._init_state()

    def _rows(self, batch: List[Any]) -> List[Tuple[str, List[Tuple[Any, ...]]]]:
        self._schemas = [self._metrics_schema]
        return [("metrics", [self.row(r) for r in batch if isinstance(r, MetricRow)])]

    def row(self, r: MetricRow) -> Tuple[Any, ...]:
        count = total = lo = hi = buckets = None
        if r.histogram is not None:
            count, total, lo, hi, buckets = r.histogram
        return (format_ts(r.at), self._service, self._instance or None, r.name, r.kind, r.unit or None, r.labels,
                r.series, r.value, count, total, lo, hi, buckets)


class MetricsRecorder:
    def __init__(self, cfg: Any, backend: Any, logger: logging.Logger, instance: str) -> None:
        self.writer = MetricsWriter(cfg, backend, logger, instance)
        self._logger = logger
        self._interval = cfg.metrics_interval if cfg.metrics_interval > 0 else 15.0
        if cfg.metrics_max_series > 0:
            registry.max_series = cfg.metrics_max_series
        self._init_state()

    def _init_state(self) -> None:
        self._stop = threading.Event()
        self._thread: Optional[threading.Thread] = None

    def start(self) -> None:
        self.writer.start()
        self._thread = threading.Thread(target=self._run, name="visionary-metrics-interval", daemon=True)
        self._thread.start()

    def restart_after_fork(self) -> None:
        self.writer.restart_after_fork()
        self._init_state()
        self._thread = threading.Thread(target=self._run, name="visionary-metrics-interval", daemon=True)
        self._thread.start()

    def _run(self) -> None:
        _spans.mark_internal()
        while not self._stop.wait(self._interval):
            self.flush()

    def flush(self) -> None:
        rows, dropped = registry.collect(time.time())
        for name, n in dropped.items():
            self._logger.warning("visionary: metric series dropped over the limit (metric=%s dropped=%d limit=%d)",
                                 name, n, registry.max_series)
        for r in rows:
            self.writer.log(r)

    def close(self, timeout: float) -> bool:
        self._stop.set()
        if self._thread is not None:
            self._thread.join(min(timeout, 1.0))
        self.flush()
        return self.writer.close(timeout)
