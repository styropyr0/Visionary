"""Django integration.

Add the middleware first in ``MIDDLEWARE`` so it sees the final response::

    MIDDLEWARE = ["visionary.integrations.django.VisionaryMiddleware", ...]

It uses the agent from ``visionary.init()``, calling it on first load if your
code hasn't. Works under WSGI and ASGI.

It also records every SQL statement of every database alias as a span (via
``execute_wrappers``, the list behind ``connection.execute_wrapper``) and
every exception Django turns into a 500 (``got_request_exception``).

With Django 5.1+ PostgreSQL pooling (``OPTIONS["pool"]``) and no
``db_stats`` hook, the ``db_*`` system columns come from Django's pools.
"""

from __future__ import annotations

import sys
import time
from typing import Any, Optional

from django.core.signals import got_request_exception
from django.db.backends.signals import connection_created
from django.http import FileResponse

from .. import _context, _db, _profiler, _spans
from .._agent import Agent
from .._entry import (
    REQUEST_ID_HEADER, BodyCapture, Entry, capturable, client_ip, now, parse_content_length, request_id,
)
from ._common import CountingIterator, finish, finish_unlogged, logger, report_unhandled, resolve_agent

try:
    from asgiref.sync import iscoroutinefunction, markcoroutinefunction
except ImportError:  # asgiref < 3.6 (Django < 4.1): sync only
    from asyncio import iscoroutinefunction

    markcoroutinefunction = None  # type: ignore[assignment]


class _State:
    __slots__ = ("entry", "t0", "ann", "token")

    def __init__(self, entry: Optional[Entry], t0: float, ann: Any, token: Any) -> None:
        self.entry, self.t0, self.ann, self.token = entry, t0, ann, token


class VisionaryMiddleware:
    sync_capable = True
    async_capable = markcoroutinefunction is not None

    def __init__(self, get_response: Any, agent: Optional[Agent] = None) -> None:
        self.get_response = get_response
        self.agent = resolve_agent(agent)
        self._async = markcoroutinefunction is not None and iscoroutinefunction(get_response)
        if self._async:
            markcoroutinefunction(self)
        if _uses_pool():
            self.agent.default_db_stats(_pool_stats)
        if self.agent.spans_on:
            install_db_spans()
        if self.agent.errors_on:
            got_request_exception.connect(self._on_exception, weak=False,
                                          dispatch_uid="visionary-%d" % id(self.agent))

    def __call__(self, request: Any) -> Any:
        if self._async:
            return self._acall(request)
        if not self.agent.enabled:
            return self.get_response(request)
        state = self._begin(request)
        try:
            response = self.get_response(request)
        except BaseException as e:
            report_unhandled(self.agent, e)
            self._end(request, None, state)
            raise
        return self._end(request, response, state)

    async def _acall(self, request: Any) -> Any:
        if not self.agent.enabled:
            return await self.get_response(request)
        state = self._begin(request)
        try:
            response = await self.get_response(request)
        except BaseException as e:
            report_unhandled(self.agent, e)
            self._end(request, None, state)
            raise
        return self._end(request, response, state)

    def _on_exception(self, sender: Any = None, request: Any = None, **kwargs: Any) -> None:
        # Sent from inside Django's except block, before it builds the 500.
        exc = sys.exc_info()[1]
        if exc is None:
            return
        info = None
        if _context.current() is None and request is not None:
            info = _request_info(request, None)
        report_unhandled(self.agent, exc, info)

    def _begin(self, request: Any) -> Optional[_State]:
        self.agent.http.started()
        _profiler.request_started()
        try:
            t0 = time.perf_counter()
            e = None
            if self.agent.should_log(request.path):
                e = Entry()
                e.started_at = now()
                e.request_id = request_id(request.headers.get(REQUEST_ID_HEADER))
            rid = e.request_id if e is not None else None
            trace = self.agent.new_trace(rid, t0) if e is not None else None
            ann, token = _context.begin(trace, _request_info(request, rid))
            return _State(e, t0, ann, token)
        except Exception:
            logger.exception("visionary: request capture failed")
            return None

    def _end(self, request: Any, response: Any, state: Optional[_State]) -> Any:
        status = getattr(response, "status_code", 500) if response is not None else 500
        if state is not None:
            _context.end(state.token)
        if state is None or state.entry is None:
            self.agent.http.finished(status)
            if state is not None:
                try:
                    finish_unlogged(request.method, state.ann.route or _route(request) or request.path, state.t0)
                except Exception:
                    pass
            return response
        try:
            streamed = self._fill(request, response, state)
        except Exception:
            logger.exception("visionary: request capture failed")
            streamed = False
        if not streamed:
            self.agent.http.finished(status)
            finish(self.agent, state.entry, state.t0, status, state.ann.trace)
        return response

    def _fill(self, request: Any, response: Any, state: _State) -> bool:
        """Fill the entry; returns True when a streaming body will finish it."""
        e, cfg = state.entry, self.agent.config
        meta = request.META
        e.method = request.method or ""
        e.path = request.path
        e.query = meta.get("QUERY_STRING", "")
        e.ip = client_ip(meta.get("HTTP_X_FORWARDED_FOR"), meta.get("REMOTE_ADDR"))
        e.user_agent = meta.get("HTTP_USER_AGENT", "")
        e.headers = list(request.headers.items())
        e.request_content_type = meta.get("CONTENT_TYPE", "")

        e.route = state.ann.route or _route(request)
        e.user_id, e.user_handle, e.user_role = state.ann.user()
        e.login_account = state.ann.login_account
        if not e.user_id:
            e.user_id, e.user_handle = _django_user(request)

        req = BodyCapture(cfg.capture_bodies and capturable(e.request_content_type), cfg.max_body_bytes)
        if req.enabled:
            req.feed(_request_body(request))
        e.request_body, e.request_truncated = req.data, req.truncated
        e.request_size = max(parse_content_length(meta.get("CONTENT_LENGTH")), req.size)

        if response is None:
            return False
        response[REQUEST_ID_HEADER] = e.request_id
        e.response_content_type = response.get("Content-Type", "")
        declared = parse_content_length(response.get("Content-Length"))
        resp = BodyCapture(cfg.capture_bodies and capturable(e.response_content_type), cfg.max_body_bytes)

        if not getattr(response, "streaming", False):
            resp.feed(response.content)
            e.response_body, e.response_truncated = resp.data, resp.truncated
            e.response_size = max(declared, resp.size)
            return False
        if isinstance(response, FileResponse):
            # Re-wrapping would disable wsgi.file_wrapper (sendfile).
            e.response_size = declared
            return False

        status = response.status_code

        def done() -> None:
            e.response_body, e.response_truncated = resp.data, resp.truncated
            e.response_size = max(declared, resp.size)
            self.agent.http.finished(status)
            finish(self.agent, e, state.t0, status, state.ann.trace)

        if getattr(response, "is_async", False):
            response.streaming_content = _acount(response.streaming_content, resp, done)
        else:
            response.streaming_content = CountingIterator(response.streaming_content, resp, done)
        return True


async def _acount(inner: Any, capture: BodyCapture, done: Any) -> Any:
    try:
        async for chunk in inner:
            capture.feed(chunk.encode("utf-8") if isinstance(chunk, str) else bytes(chunk))
            yield chunk
    finally:
        done()


def _request_info(request: Any, rid: Optional[str]) -> _context.RequestInfo:
    return _context.RequestInfo(rid, request.method or "", request.path,
                                route=lambda: _route(request), user=lambda: _django_user(request)[0])


def install_db_spans() -> None:
    """Add the span wrapper to every Django database connection, current and
    future, in every thread. Called by the middleware; idempotent."""
    _spans.db_visible = True
    connection_created.connect(_on_connection_created, weak=False, dispatch_uid="visionary-db-spans")
    from django.db import connections

    try:
        existing = connections.all(initialized_only=True)
    except TypeError:  # Django < 4.1
        existing = []
    for conn in existing:
        _attach(conn)


def _on_connection_created(sender: Any = None, connection: Any = None, **kwargs: Any) -> None:
    if connection is not None:
        _attach(connection)


def _attach(conn: Any) -> None:
    wrappers = getattr(conn, "execute_wrappers", None)
    if wrappers is not None and _execute_wrapper not in wrappers:
        wrappers.insert(0, _execute_wrapper)


def _execute_wrapper(execute: Any, sql: Any, params: Any, many: bool, context: Any) -> Any:
    conn = context.get("connection")
    span = _db.start_query(sql, _db_target(conn)) if conn is not None else None
    if span is None:
        return execute(sql, params, many, context)
    try:
        result = execute(sql, params, many, context)
    except BaseException as e:
        span.finish(error=e)
        raise
    span.finish(row_count=_db.row_count(context.get("cursor")))
    return result


def _db_target(conn: Any) -> str:
    try:
        return _db.target_of(conn.vendor, conn.settings_dict.get("NAME"))
    except Exception:
        return "db"


def _uses_pool() -> bool:
    """Whether a PostgreSQL alias turns on Django 5.1+ pooling."""
    try:
        from django.conf import settings

        return any("postgresql" in (db.get("ENGINE") or "") and (db.get("OPTIONS") or {}).get("pool")
                   for db in settings.DATABASES.values())
    except Exception:
        return False


def _pool_stats() -> Optional[dict]:
    """db_* stats summed over Django's psycopg pools; None before any exists.
    Reads the pools Django already made: ``connection.pool`` would create one."""
    try:
        from django.db.backends.postgresql.base import DatabaseWrapper
    except Exception:
        return None
    pools = list(getattr(DatabaseWrapper, "_connection_pools", {}).values())
    if not pools:
        return None
    s: dict = {}
    for pool in pools:
        for k, n in pool.get_stats().items():
            s[k] = s.get(k, 0) + n
    return {
        "total": s.get("pool_size", 0), "idle": s.get("pool_available", 0),
        "in_use": s.get("pool_size", 0) - s.get("pool_available", 0), "max": s.get("pool_max", 0),
        "acquires": s.get("requests_num", 0), "empty_acquires": s.get("requests_queued", 0),
        "acquire_wait_ms": s.get("requests_wait_ms", 0),
    }


def _route(request: Any) -> str:
    match = getattr(request, "resolver_match", None)
    route = getattr(match, "route", None) if match is not None else None
    if not route:
        return ""
    route = str(route).lstrip("^")
    return route if route.startswith("/") else "/" + route


def _django_user(request: Any) -> tuple:
    try:
        user = getattr(request, "user", None)
        if user is not None and getattr(user, "is_authenticated", False):
            return str(user.pk), user.get_username()
    except Exception:
        pass
    return "", ""


def _request_body(request: Any) -> bytes:
    # request.body raises if the view consumed the stream via request.read();
    # then there is nothing left to log.
    try:
        return request.body or b""
    except Exception:
        return b""
