"""CPU profiles, switched on and off from the dashboard (SPEC "CPU profiles").

The Visionary server keeps the switch in ``visionary_settings``. A daemon
thread reads it at start and every 30 seconds and, while profiling is on,
samples every thread's Python stack about every 10 ms for
``profile.duration_ms`` every ``profile.interval_s``, skipping a turn when no
request arrived since the last one. Each profile becomes one ``profiles`` row
of folded stacks plus the milliseconds each route's requests took while it
ran. ``VISIONARY_PROFILE=false`` ignores the dashboard entirely.

Only threads that are using the CPU are counted. Where the OS has per-thread
CPU clocks (Linux, and other Unixes with ``pthread_getcpuclockid``) a thread
counts when its CPU time advanced since the previous tick. Elsewhere
(Windows, macOS) the filter is a heuristic: a thread whose innermost Python
frame is calling a known blocking function (lock and condition waits,
``select``/``poll``, socket accept/recv, ``time.sleep``, ``queue.get``, ...) is
left out. That is approximate: a thread waiting for the GIL, or blocked in a
call the list doesn't know, still counts.
"""

from __future__ import annotations

import dis
import json
import logging
import os
import re
import sys
import sysconfig
import threading
import time
from calendar import timegm
from typing import Any, Callable, Dict, List, Optional, Sequence, Tuple

from . import _spans
from ._entry import format_ts

MAX_FOLDED = 90_000
KEEP_WHOLE = 72_000  # the heaviest stacks kept whole; the rest roll up (see fold)
MIN_SAMPLES = 20
SETTINGS_POLL_S = 30.0
NOW_WINDOW_S = 300.0
DEFAULT_INTERVAL_S = 300.0
DEFAULT_DURATION_MS = 10_000.0
SAMPLE_EVERY_S = 0.01
MAX_DEPTH = 256

# The SDK's own threads are never sampled.
_OWN_THREAD_PREFIX = "visionary-"


# --- request tally ---------------------------------------------------------


class RequestTally:
    """Requests started so far, and while a profile runs, the milliseconds
    each route's finished requests took ("GET /users/<id>")."""

    def __init__(self) -> None:
        self.started = 0
        self._busy: Optional[Dict[str, float]] = None
        self._lock = threading.Lock()

    def request_started(self) -> None:
        # Only ever compared for change; a lost increment under a race is harmless.
        self.started += 1

    def request_finished(self, method: str, route: str, seconds: float) -> None:
        if self._busy is None:
            return
        key = "%s %s" % (method or "GET", route or "/")
        with self._lock:
            if self._busy is not None:
                self._busy[key] = self._busy.get(key, 0.0) + seconds * 1000.0

    def begin(self) -> None:
        with self._lock:
            self._busy = {}

    def end(self) -> Dict[str, float]:
        with self._lock:
            busy, self._busy = self._busy, None
        return busy or {}


tally = RequestTally()


def request_started() -> None:
    """Integration API: a request reached the middleware."""
    tally.request_started()


def request_finished(method: str, route: str, seconds: float) -> None:
    """Integration API: a request finished after ``seconds``; ``route`` is the
    route pattern, or the path when there is none."""
    tally.request_finished(method, route, seconds)


# --- settings ----------------------------------------------------------------


class Settings:
    __slots__ = ("enabled", "services", "interval_s", "duration_ms", "now")

    def __init__(self, enabled: bool = False, services: Optional[List[str]] = None,
                 interval_s: float = DEFAULT_INTERVAL_S, duration_ms: float = DEFAULT_DURATION_MS,
                 now: str = "") -> None:
        self.enabled = enabled
        self.services = services or []
        self.interval_s = interval_s
        self.duration_ms = duration_ms
        self.now = now

    def includes(self, service: str) -> bool:
        """Every service when the list is empty; a service with no name only then."""
        if not self.services:
            return True
        return bool(service) and service in self.services


def parse_settings(rows: Sequence[Sequence[Any]]) -> Settings:
    """Settings from (name, value) rows; anything missing or unreadable takes
    its default."""
    s = Settings()
    for row in rows:
        if len(row) < 2:
            continue
        name, value = str(row[0]), "" if row[1] is None else str(row[1]).strip()
        if name == "profile.enabled":
            s.enabled = value.lower() in ("true", "1")
        elif name == "profile.services":
            try:
                lst = json.loads(value)
            except ValueError:
                continue
            if isinstance(lst, list):
                s.services = [str(x) for x in lst if isinstance(x, str) and x]
        elif name == "profile.interval_s":
            n = _positive(value)
            if n is not None:
                s.interval_s = max(n, 1.0)
        elif name == "profile.duration_ms":
            n = _positive(value)
            if n is not None:
                s.duration_ms = min(max(n, 100.0), 300_000.0)
        elif name == "profile.now":
            s.now = value
    return s


def _positive(v: str) -> Optional[float]:
    try:
        n = float(v)
    except ValueError:
        return None
    return n if n > 0 and n == n and n != float("inf") else None


_TS = re.compile(r"^(\d{4})-(\d{2})-(\d{2})[T ](\d{2}):(\d{2}):(\d{2})(\.\d+)?(Z|[+-]\d{2}:?\d{2})?$")


def parse_ts(v: str) -> Optional[float]:
    """Epoch seconds from a created_at-style timestamp (UTC unless an offset
    is given), or None."""
    m = _TS.match(v.strip())
    if not m:
        return None
    y, mo, d, h, mi, s = (int(m.group(i)) for i in range(1, 7))
    try:
        t = float(timegm((y, mo, d, h, mi, s, 0, 0, 0)))
    except (ValueError, OverflowError):
        return None
    if m.group(7):
        t += float("0" + m.group(7))
    tz = m.group(8)
    if tz and tz != "Z":
        sign = 1 if tz[0] == "+" else -1
        digits = tz[1:].replace(":", "")
        t -= sign * (int(digits[:2]) * 3600 + int(digits[2:]) * 60)
    return t


def fresh_now(value: str, started: float, now: float) -> bool:
    """Whether a profile.now value asks for a profile: newer than the SDK's
    start and less than 5 minutes old."""
    t = parse_ts(value) if value else None
    return t is not None and t > started and now - t < NOW_WINDOW_S


# --- frame naming and folding ----------------------------------------------


def _norm(p: str) -> str:
    p = p.replace("\\", "/").rstrip("/")
    return p.lower() if os.name == "nt" else p


class FrameNamer:
    """Names frames "function file:line", the file saying whose code it is:
    relative to the working directory for the app, ``lib:<package>/...`` for
    site-packages, ``std:...`` for the standard library."""

    def __init__(self, cwd: Optional[str] = None, stdlib: Optional[Sequence[str]] = None,
                 sdk_dir: Optional[str] = None) -> None:
        if stdlib is None:
            paths = sysconfig.get_paths()
            stdlib = [p for p in (paths.get("stdlib"), paths.get("platstdlib")) if p]
        if sdk_dir is None:
            sdk_dir = os.path.dirname(os.path.abspath(__file__))
        self._cwd = _norm(cwd if cwd is not None else os.getcwd())
        # Longest first, so a nested path wins.
        self._stdlib = sorted({_norm(p) for p in stdlib}, key=len, reverse=True)
        self._sdk = _norm(sdk_dir)
        self._files: Dict[str, str] = {}
        self._codes: Dict[Tuple[Any, int], str] = {}

    def file(self, filename: str) -> str:
        """The file part of a frame name, or "" when there is no source file."""
        label = self._files.get(filename)
        if label is None:
            label = self._label(filename)
            if len(self._files) > 50_000:
                self._files.clear()
            self._files[filename] = label
        return label

    def _label(self, filename: str) -> str:
        if not filename:
            return ""
        if filename.startswith("<"):
            m = re.match(r"^<frozen ([\w.]+)>$", filename)
            return "std:" + m.group(1).replace(".", "/") + ".py" if m else ""
        path = filename.replace("\\", "/")
        low = _norm(path)
        for marker in ("/site-packages/", "/dist-packages/"):
            i = low.rfind(marker)
            if i >= 0:
                return "lib:" + path[i + len(marker):]
        if low.startswith(self._sdk + "/"):
            return "lib:visionary/" + path[len(self._sdk) + 1:]
        for std in self._stdlib:
            if low.startswith(std + "/"):
                return "std:" + path[len(std) + 1:]
        if self._cwd and low.startswith(self._cwd + "/"):
            return path[len(self._cwd) + 1:]
        return path

    def name(self, code: Any, lineno: Optional[int]) -> str:
        key = (code, lineno or 0)
        out = self._codes.get(key)
        if out is None:
            fn = getattr(code, "co_qualname", None) or code.co_name or "(anonymous)"
            f = self.file(code.co_filename)
            out = (("%s %s:%d" % (fn, f, lineno)) if lineno else ("%s %s" % (fn, f))) if f else fn
            out = out.replace(";", ",").replace("\n", " ")
            if len(self._codes) > 100_000:
                self._codes.clear()
            self._codes[key] = out
        return out


def fold(lines: Dict[str, int]) -> Tuple[str, int]:
    """Folded text from "frame;frame;frame" -> count, heaviest first, within
    MAX_FOLDED bytes (sdks/SPEC.md, "CPU profiles"): whole lines up to
    KEEP_WHOLE, then the rest cut after the deepest frame they share with a
    kept stack and ending in "(other)", then one "(other) n" for whatever is
    left. Returns the text and the total sample count."""
    total = sum(lines.values())
    ordered = sorted(lines.items(), key=lambda kv: (-kv[1], kv[0]))
    out: List[str] = []
    size = 0
    # Whole lines first, leaving room for the rolled-up ones.
    i = 0
    for stack, n in ordered:
        line = "%s %d\n" % (stack, n)
        b = len(line.encode("utf-8"))
        if size + b > KEEP_WHOLE:
            break
        out.append(line)
        size += b
        i += 1
    if i == len(ordered):
        return "".join(out), total
    # The rest are cut after the deepest frame they share with a kept stack
    # and end in "(other)", so their time stays under the right caller.
    prefixes = set()
    for stack, _ in ordered[:i]:
        j = stack.find(";")
        while j >= 0:
            prefixes.add(stack[:j])
            j = stack.find(";", j + 1)
        prefixes.add(stack)
    rolled: Dict[str, int] = {}
    other = 0
    for stack, n in ordered[i:]:
        cut = ""
        for j in range(len(stack) + 1):
            if (j == len(stack) or stack[j] == ";") and stack[:j] in prefixes:
                cut = stack[:j]
        if not cut:
            other += n
            continue
        # The whole stack leads into a kept one: nothing to cut.
        key = stack if cut == stack else cut + ";(other)"
        rolled[key] = rolled.get(key, 0) + n
    for stack, n in sorted(rolled.items(), key=lambda kv: (-kv[1], kv[0])):
        line = "%s %d\n" % (stack, n)
        b = len(line.encode("utf-8"))
        if size + b > MAX_FOLDED - 32:  # room for the last "(other) n"
            other += n
            continue
        out.append(line)
        size += b
    if other:
        out.append("(other) %d\n" % other)
    return "".join(out), total


# --- which threads are using the CPU -----------------------------------------

# Functions a thread blocks in without using the CPU. Matched against what the
# innermost Python frame is calling right now (the C function is invisible).
BLOCKING_CALLS = frozenset({
    "sleep", "acquire", "wait", "wait_for", "join", "_wait_for_tstate_lock", "get",
    "select", "_select", "poll", "control", "epoll", "kqueue",
    "accept", "_accept", "sendall", "sendfile", "shutdown", "recv", "recv_into", "recvfrom", "recvfrom_into", "recvmsg", "recvmsg_into",
    "connect", "connect_ex", "getaddrinfo", "gethostbyname", "gethostbyname_ex", "gethostbyaddr",
    "waitpid", "wait3", "wait4", "waitid", "sigwait", "sigwaitinfo", "sigtimedwait", "pause",
    "GetQueuedCompletionStatus", "WaitForSingleObject", "WaitForMultipleObjects", "ConnectNamedPipe",
    "input",
})

_CALL_OPS = ("CALL", "CALL_FUNCTION", "CALL_METHOD", "CALL_KW", "CALL_FUNCTION_KW")
_SKIP_OPS = frozenset({"CACHE", "PRECALL", "KW_NAMES", "NOP", "EXTENDED_ARG"})
_NAME_OPS = frozenset({"LOAD_ATTR", "LOAD_METHOD", "LOAD_GLOBAL", "LOAD_NAME", "LOAD_DEREF",
                       "LOAD_CLASSDEREF", "LOAD_SUPER_ATTR", "LOAD_FAST", "LOAD_FAST_CHECK",
                       "LOAD_FAST_BORROW"})


class _Code:
    __slots__ = ("instrs", "index")

    def __init__(self, code: Any) -> None:
        self.instrs = list(dis.get_instructions(code))
        self.index = {ins.offset: i for i, ins in enumerate(self.instrs)}


_codes: Dict[Any, _Code] = {}
_callees: Dict[Tuple[Any, int], Optional[str]] = {}


def callee_name(code: Any, lasti: int) -> Optional[str]:
    """The name of what the instruction at lasti calls (``sleep`` for
    ``time.sleep(1)``), or None when it isn't a call or can't be told."""
    key = (code, lasti)
    if key in _callees:
        return _callees[key]
    try:
        name = _callee(code, lasti)
    except Exception:
        name = None
    if len(_callees) > 20_000:
        _callees.clear()
        _codes.clear()
    _callees[key] = name
    return name


def _callee(code: Any, lasti: int) -> Optional[str]:
    c = _codes.get(code)
    if c is None:
        c = _codes[code] = _Code(code)
    i = c.index.get(lasti)
    if i is None:
        return None
    ins = c.instrs[i]
    if ins.opname not in _CALL_OPS:
        return None
    # Walk back over the arguments by stack effect; the instruction before
    # them loaded the callable.
    need = (ins.arg or 0) + (1 if ins.opname in ("CALL_KW", "CALL_FUNCTION_KW") else 0)
    depth = 0
    j = i - 1
    while j >= 0 and depth < need:
        prev = c.instrs[j]
        j -= 1
        if prev.opname in _SKIP_OPS:
            continue
        try:
            depth += dis.stack_effect(prev.opcode, prev.arg, jump=False) if prev.opcode >= dis.HAVE_ARGUMENT \
                else dis.stack_effect(prev.opcode)
        except (ValueError, TypeError):
            return None
    # 3.13+ pushes the NULL after the callable.
    while j >= 0 and (c.instrs[j].opname in _SKIP_OPS or c.instrs[j].opname == "PUSH_NULL"):
        j -= 1
    if j < 0 or depth != need:
        return None
    loader = c.instrs[j]
    if loader.opname not in _NAME_OPS:
        return None
    v = loader.argval
    if isinstance(v, tuple):  # LOAD_SUPER_ATTR and friends
        v = v[-1] if v else None
    return v if isinstance(v, str) else None


def blocked(frame: Any) -> bool:
    """Whether the frame (a thread's innermost) is calling a known blocking
    function."""
    try:
        lasti = frame.f_lasti
        if lasti < 0:
            return False
        return callee_name(frame.f_code, lasti) in BLOCKING_CALLS
    except Exception:
        return False


def _linux_cpu_clock(native_id: int) -> Optional[float]:
    # MAKE_THREAD_CPUCLOCK(tid, CPUCLOCK_SCHED): a clock for any live thread
    # id; a thread that has exited gives EINVAL rather than undefined behaviour.
    return time.clock_gettime((~native_id << 3) | 6)


def default_cpu_clock() -> Optional[Callable[[int], Optional[float]]]:
    """Per-thread CPU seconds by native thread id, where the OS offers it."""
    if sys.platform.startswith("linux") and hasattr(time, "clock_gettime"):
        try:
            _linux_cpu_clock(threading.get_native_id())
            return _linux_cpu_clock
        except (OSError, AttributeError, OverflowError):
            pass
    return None


class _PthreadClock:
    """Other Unixes: pthread_getcpuclockid on the thread's ident, looked up
    only for threads threading.enumerate() just reported alive."""

    def __init__(self) -> None:
        self._ids: Dict[int, int] = {}

    def __call__(self, ident: int) -> Optional[float]:
        cid = self._ids.get(ident)
        if cid is None:
            cid = self._ids[ident] = time.pthread_getcpuclockid(ident)
        return time.clock_gettime(cid)


class CpuFilter:
    """Decides, per tick, which threads count as using the CPU.

    With a clock (``clock(thread_key) -> CPU seconds``) a thread counts when
    its CPU time advanced since the previous tick and it isn't parked in a
    blocking call right now. Without one only the blocking-call heuristic
    applies."""

    def __init__(self, clock: Optional[Callable[[int], Optional[float]]] = None) -> None:
        self.clock = clock
        self._last: Dict[int, float] = {}

    def using_cpu(self, key: Optional[int], frame: Any) -> bool:
        if self.clock is not None and key is not None:
            try:
                t = self.clock(key)
            except Exception:
                t = None
            if t is not None:
                prev = self._last.get(key)
                self._last[key] = t
                if prev is None or t <= prev:
                    return False
                return not blocked(frame)
        return not blocked(frame)

    def keep_only(self, keys: Any) -> None:
        for k in [k for k in self._last if k not in keys]:
            del self._last[k]


class Sampler:
    """Collects stacks of CPU-using threads, one tick at a time."""

    def __init__(self, namer: FrameNamer, cpu: Optional[CpuFilter] = None,
                 clock_key: str = "native_id") -> None:
        self._namer = namer
        if cpu is None:
            clock = default_cpu_clock()
            key = "native_id"
            if clock is None and hasattr(time, "pthread_getcpuclockid") and hasattr(time, "clock_gettime"):
                clock, key = _PthreadClock(), "ident"
            cpu = CpuFilter(clock)
            clock_key = key
        self._cpu = cpu
        self._clock_key = clock_key
        self._stacks: Dict[Tuple[Tuple[Any, int], ...], int] = {}
        self.ticks = 0

    def tick(self, frames: Optional[Dict[int, Any]] = None, threads: Optional[Sequence[Any]] = None) -> int:
        """Sample once; returns the stacks counted."""
        me = threading.get_ident()
        if threads is None:
            threads = threading.enumerate()
        own = {me}
        keys: Dict[int, Optional[int]] = {}
        for t in threads:
            if t.ident is None:
                continue
            if t.name.startswith(_OWN_THREAD_PREFIX):
                own.add(t.ident)
            keys[t.ident] = t.ident if self._clock_key == "ident" else getattr(t, "native_id", None)
        if frames is None:
            frames = sys._current_frames()
        self.ticks += 1
        counted = 0
        for ident, frame in frames.items():
            if ident in own or frame is None:
                continue
            if not self._cpu.using_cpu(keys.get(ident), frame):
                continue
            # Only the sampled frame keeps its line: callers are one frame
            # per function, whichever line they called from.
            stack = [(frame.f_code, frame.f_lineno or 0)]
            f = frame.f_back
            while f is not None and len(stack) < MAX_DEPTH:
                stack.append((f.f_code, 0))
                f = f.f_back
            stack.reverse()
            key = tuple(stack)
            self._stacks[key] = self._stacks.get(key, 0) + 1
            counted += 1
        if self.ticks % 100 == 0:
            self._cpu.keep_only({k for k in keys.values() if k is not None})
        return counted

    def result(self) -> Tuple[str, int]:
        lines: Dict[str, int] = {}
        name = self._namer.name
        for stack, n in self._stacks.items():
            line = ";".join(name(code, ln) for code, ln in stack)
            lines[line] = lines.get(line, 0) + n
        return fold(lines)


# --- the profiles row and the schedule -----------------------------------------


class Profile:
    """One profiles row, queued on the request writer."""

    __slots__ = ("created_at", "duration_ms", "samples", "routes", "folded")

    def __init__(self, created_at: float, duration_ms: float, samples: int, routes: str, folded: str) -> None:
        self.created_at = created_at
        self.duration_ms = duration_ms
        self.samples = samples
        self.routes = routes
        self.folded = folded


class Profiler:
    """Reads the dashboard's settings and takes profiles when they ask."""

    def __init__(self, backend: Any, push: Callable[[Profile], None], logger: logging.Logger,
                 service: str = "", retention_days: int = 0, poll_s: float = SETTINGS_POLL_S,
                 tick_s: float = 1.0) -> None:
        self._backend = backend
        self._push = push
        self._logger = logger
        self._service = service
        self._retention_days = retention_days
        self._poll_s = poll_s
        self._tick_s = tick_s
        self._namer = FrameNamer()
        self.started_at = time.time()
        self._init_state()

    def _init_state(self) -> None:
        self._stop = threading.Event()
        self._thread: Optional[threading.Thread] = None
        self.settings = Settings()
        self.on = False
        self.one_off = False
        self._last_now = ""
        self._last_started = tally.started
        self._next = 0.0
        self._read_failed = False
        self._pushed = False
        self._next_purge = 0.0

    def start(self) -> None:
        self._thread = threading.Thread(target=self._run, name="visionary-profiler", daemon=True)
        self._thread.start()

    def restart_after_fork(self) -> None:
        self._init_state()
        self.start()

    def close(self) -> None:
        self._stop.set()

    def _run(self) -> None:
        _spans.mark_internal()
        next_poll = 0.0
        while not self._stop.is_set():
            now = time.time()
            if now >= next_poll:
                self.poll(now)
                next_poll = time.time() + self._poll_s
            try:
                self.turn(time.time())
            except Exception:
                self._logger.exception("visionary: CPU profile failed")
            self._stop.wait(self._tick_s)

    def poll(self, now: float) -> None:
        """Read the settings; a failed read means off."""
        try:
            rows = self._backend.read_settings()
        except Exception as e:
            if not self._read_failed:
                self._read_failed = True
                self._logger.info("visionary: CPU profiling settings unavailable; profiling stays off until "
                                  "the Visionary server creates visionary_settings (%s)", e)
            rows = []
        self.apply(parse_settings(rows), now)

    def apply(self, s: Settings, now: float) -> None:
        """Take new settings: log a switch on or off, and arm a one-off when
        profile.now changed to a fresh value."""
        prev = self.settings
        self.settings = s
        on = s.enabled and s.includes(self._service)
        if on and not self.on:
            self._next = now + s.interval_s
            self._logger.info("visionary: CPU profiling on (%g ms every %g s)", s.duration_ms, s.interval_s)
        elif not on and self.on:
            self._logger.info("visionary: CPU profiling off")
        elif on and s.interval_s < prev.interval_s and now + s.interval_s < self._next:
            self._next = now + s.interval_s
        self.on = on
        if s.now and s.now != self._last_now:
            self._last_now = s.now
            if s.includes(self._service) and fresh_now(s.now, self.started_at, now):
                self.one_off = True

    def turn(self, now: float) -> None:
        """Take a profile when one is due."""
        if self.one_off:
            self.one_off = False
            self._last_started = tally.started
            self._logger.info("visionary: taking a CPU profile now (asked from the dashboard)")
            self.capture()
            self._next = time.time() + self.settings.interval_s
        elif self.on and now >= self._next:
            self._next = now + self.settings.interval_s
            n = tally.started
            if n != self._last_started:
                self._last_started = n
                self.capture()
        if self._pushed and self._retention_days > 0 and time.time() >= self._next_purge:
            self._next_purge = time.time() + 24 * 3600
            self._purge()

    def capture(self) -> Optional[Profile]:
        """Sample for the configured duration and queue the profile, unless
        it is too thin to be useful."""
        sampler = Sampler(self._namer)
        tally.begin()
        began = time.time()
        t0 = time.perf_counter()
        end = t0 + self.settings.duration_ms / 1000.0
        try:
            while not self._stop.is_set():
                tick = time.perf_counter()
                if tick >= end:
                    break
                sampler.tick()
                left = SAMPLE_EVERY_S - (time.perf_counter() - tick)
                # time.sleep, not Event.wait: it is high resolution on Windows
                # (3.11+), where a wait rounds up to the 15.6 ms timer tick.
                time.sleep(left if left > 0.001 else 0.001)
        finally:
            elapsed = (time.perf_counter() - t0) * 1000.0
            busy = tally.end()
        if self._stop.is_set() or not busy:
            return None
        folded, samples = sampler.result()
        if samples < MIN_SAMPLES:
            return None
        routes = {k: max(1, int(round(ms))) for k, ms in busy.items()}
        p = Profile(began, round(elapsed, 3), samples, json.dumps(routes, sort_keys=True), folded)
        self._push(p)
        if not self._pushed:
            self._pushed = True
            self._next_purge = time.time() + 3600  # first purge an hour from now
        return p

    def _purge(self) -> None:
        cutoff = format_ts(time.time() - self._retention_days * 86400)
        try:
            for _ in range(200):
                if self._stop.is_set() or self._backend.purge("profiles", cutoff) < 5000:
                    break
        except Exception as e:
            self._logger.warning("visionary: retention purge of profiles failed: %s", e)
