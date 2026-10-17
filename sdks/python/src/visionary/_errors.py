"""Error rows: exception type, message, stack and a stable fingerprint."""

from __future__ import annotations

import hashlib
import os
import re
import sys
import sysconfig
import threading
import time
import traceback
from typing import Dict, List, Optional, Tuple

from . import _context

MESSAGE_MAX = 1000
STACK_MAX = 16 * 1024
FINGERPRINT_FRAMES = 5
RATE_LIMIT = 20  # rows per fingerprint per minute per process

_DIGITS = re.compile(r"\d+")
_SDK_DIR = os.path.normcase(os.path.dirname(os.path.abspath(__file__))) + os.sep


def _library_dirs() -> Tuple[str, ...]:
    dirs = set()
    for key in ("stdlib", "platstdlib"):
        try:
            p = sysconfig.get_paths().get(key)
        except Exception:
            p = None
        if p:
            dirs.add(os.path.normcase(os.path.abspath(p)) + os.sep)
    return tuple(dirs)


_LIB_DIRS = _library_dirs()


class ErrorEvent:
    """One errors row, built on the thread that raised (the fingerprint is
    needed there for rate limiting); the writer adds service and instance."""

    __slots__ = ("created_at", "request_id", "method", "route", "path", "user_id",
                 "type", "message", "stack", "fingerprint", "handled")

    def __init__(self) -> None:
        self.created_at = 0.0
        self.request_id: Optional[str] = None
        self.method: Optional[str] = None
        self.route: Optional[str] = None
        self.path: Optional[str] = None
        self.user_id: Optional[str] = None
        self.type = ""
        self.message = ""
        self.stack = ""
        self.fingerprint = ""
        self.handled = 0


def build(exc: BaseException, handled: bool, info: Optional[_context.RequestInfo] = None) -> ErrorEvent:
    ev = ErrorEvent()
    ev.created_at = time.time()
    ev.handled = 1 if handled else 0
    ev.type = type_name(type(exc))
    ev.message = str(exc)[:MESSAGE_MAX]
    ev.stack = _cap_stack("".join(traceback.format_exception(type(exc), exc, exc.__traceback__)))
    ev.fingerprint = fingerprint(ev.type, str(exc), traceback.extract_tb(exc.__traceback__))

    ann = _context.current()
    if info is None and ann is not None:
        info = ann.request
    if info is not None:
        ev.request_id = info.request_id or None
        ev.method = info.method or None
        ev.path = info.path or None
        ev.route = info.route(ann) or None
        ev.user_id = info.user_id(ann) or None
    return ev


def type_name(cls: type) -> str:
    """``ValueError`` for builtins, else ``module.QualName``."""
    module = getattr(cls, "__module__", "") or ""
    name = getattr(cls, "__qualname__", None) or cls.__name__
    return name if module in ("builtins", "") else module + "." + name


def fingerprint(type_: str, message: str, frames: List[traceback.FrameSummary]) -> str:
    """SHA-1 of the type and the innermost 5 application frames
    (``function@file``, no line numbers), first 16 hex characters."""
    innermost = list(reversed(frames))
    app = [f for f in innermost if in_app(f.filename)]
    chosen = (app or innermost)[:FINGERPRINT_FRAMES]
    if chosen:
        body = "\n".join("%s@%s" % (f.name, display_path(f.filename)) for f in chosen)
    else:
        body = _DIGITS.sub("0", message)
    return hashlib.sha1((type_ + "\n" + body).encode("utf-8")).hexdigest()[:16]


def in_app(filename: str) -> bool:
    """Not the standard library, an installed package or this SDK."""
    if not filename or filename.startswith("<"):
        return False
    path = os.path.normcase(os.path.abspath(filename))
    parts = path.split(os.sep)
    if "site-packages" in parts or "dist-packages" in parts:
        return False
    if path.startswith(_SDK_DIR):
        return False
    return not path.startswith(_LIB_DIRS)


def display_path(filename: str) -> str:
    """The file relative to the import root it was loaded from (e.g.
    ``shop/views.py``), so the fingerprint doesn't change between machines
    or deploy directories."""
    path = os.path.abspath(filename)
    best = ""
    for entry in sys.path:
        root = os.path.abspath(entry or os.getcwd())
        if len(root) > len(best) and os.path.normcase(path).startswith(os.path.normcase(root) + os.sep):
            best = root
    rel = path[len(best) + 1:] if best else os.path.basename(path)
    return rel.replace(os.sep, "/")


def _cap_stack(stack: str) -> str:
    # The end of a Python traceback (the raise site) matters most.
    data = stack.encode("utf-8")
    if len(data) <= STACK_MAX:
        return stack
    return "...\n" + data[-(STACK_MAX - 4):].decode("utf-8", errors="ignore")


class RateLimiter:
    """At most ``RATE_LIMIT`` rows per fingerprint per minute."""

    def __init__(self, limit: int = RATE_LIMIT, window: float = 60.0) -> None:
        self._limit = limit
        self._window = window
        self._lock = threading.Lock()
        self._counts: Dict[str, List[float]] = {}

    def allow(self, key: str) -> bool:
        now = time.monotonic()
        with self._lock:
            entry = self._counts.get(key)
            if entry is None or now - entry[0] >= self._window:
                if len(self._counts) > 10000:
                    self._counts = {k: v for k, v in self._counts.items() if now - v[0] < self._window}
                self._counts[key] = [now, 1.0]
                return True
            if entry[1] >= self._limit:
                return False
            entry[1] += 1
            return True


def mark_seen(exc: BaseException) -> bool:
    """True the first time an unhandled exception is reported, so a framework
    signal and the middleware don't both record it."""
    if getattr(exc, "__visionary_seen__", False):
        return False
    try:
        exc.__visionary_seen__ = True  # type: ignore[attr-defined]
    except Exception:
        pass
    return True


def current_exception() -> Optional[BaseException]:
    return sys.exc_info()[1]


def runtime() -> str:
    """``CPython 3.13.1``."""
    import platform

    return "%s %s" % (platform.python_implementation(), platform.python_version())

