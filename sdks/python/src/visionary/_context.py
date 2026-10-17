from __future__ import annotations

import threading
from contextvars import ContextVar, Token
from typing import Any, Optional


class Annotations:
    """What app code says about the current request.

    Integrations put one instance in a contextvar at request start. Because
    copied contexts (thread pools, asyncio tasks) share the same object,
    set_user works from wherever the request's code runs.
    """

    __slots__ = ("_lock", "user_id", "user_handle", "user_role", "route")

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self.user_id = ""
        self.user_handle = ""
        self.user_role = ""
        self.route = ""

    def set_user(self, id: Any, handle: Optional[str], role: Optional[str]) -> None:
        with self._lock:
            self.user_id = "" if id is None else str(id)
            self.user_handle = handle or ""
            self.user_role = role or ""

    def user(self) -> tuple:
        with self._lock:
            return self.user_id, self.user_handle, self.user_role


_current: ContextVar[Optional[Annotations]] = ContextVar("visionary_request", default=None)


def begin() -> tuple:
    ann = Annotations()
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
