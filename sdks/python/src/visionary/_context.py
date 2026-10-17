from __future__ import annotations

import threading
from contextvars import ContextVar, Token
from typing import Any, Callable, Optional


class Annotations:
    """What app code says about the current request.

    Integrations put one instance in a contextvar at request start. Because
    copied contexts (thread pools, asyncio tasks) share the same object,
    set_user works from wherever the request's code runs.
    """

    __slots__ = ("_lock", "user_id", "user_handle", "user_role", "route", "trace", "request")

    def __init__(self, trace: Any = None, request: Optional["RequestInfo"] = None) -> None:
        self._lock = threading.Lock()
        self.user_id = ""
        self.user_handle = ""
        self.user_role = ""
        self.route = ""
        self.trace = trace  # _spans.Trace, or None when spans are off
        self.request = request

    def set_user(self, id: Any, handle: Optional[str], role: Optional[str]) -> None:
        with self._lock:
            self.user_id = "" if id is None else str(id)
            self.user_handle = handle or ""
            self.user_role = role or ""

    def user(self) -> tuple:
        with self._lock:
            return self.user_id, self.user_handle, self.user_role


class RequestInfo:
    """What an error row says about the request it happened in. The route is
    resolved lazily, since routers only match it during the call."""

    __slots__ = ("request_id", "method", "path", "_route", "_user")

    def __init__(self, request_id: Optional[str], method: str, path: str,
                 route: Optional[Callable[[], str]] = None, user: Optional[Callable[[], str]] = None) -> None:
        self.request_id = request_id
        self.method = method
        self.path = path
        self._route = route
        self._user = user

    def route(self, ann: Optional[Annotations] = None) -> str:
        if ann is not None and ann.route:
            return ann.route
        try:
            return self._route() if self._route is not None else ""
        except Exception:
            return ""

    def user_id(self, ann: Optional[Annotations] = None) -> str:
        if ann is not None and ann.user_id:
            return ann.user_id
        try:
            return self._user() if self._user is not None else ""
        except Exception:
            return ""


_current: ContextVar[Optional[Annotations]] = ContextVar("visionary_request", default=None)


def begin(trace: Any = None, request: Optional[RequestInfo] = None) -> tuple:
    ann = Annotations(trace, request)
    return ann, _current.set(ann)


def end(token: Token) -> None:
    try:
        _current.reset(token)
    except ValueError:
        # Reset from a different context than the one that set it (some
        # frameworks run hooks in copies); clearing is equivalent here.
        _current.set(None)


def current() -> Optional[Annotations]:
    return _current.get()


def set_user(id: Any, handle: Optional[str] = None, role: Optional[str] = None) -> None:
    """Attach the signed-in user to the request being logged.

    Call it from auth middleware or a view once the user is known. Only
    ``id`` is required. Outside a request handled by a Visionary integration
    this does nothing.
    """
    ann = _current.get()
    if ann is not None:
        ann.set_user(id, handle, role)


def set_route(pattern: str) -> None:
    """Record the route pattern (e.g. ``/users/{id}``) for routers the
    integration can't see. No-op outside a request."""
    ann = _current.get()
    if ann is not None:
        ann.route = pattern or ""
