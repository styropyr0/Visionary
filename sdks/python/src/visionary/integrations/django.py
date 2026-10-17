"""Django integration.

Add the middleware first in ``MIDDLEWARE`` so it sees the final response::

    MIDDLEWARE = ["visionary.integrations.django.VisionaryMiddleware", ...]

It uses the agent from ``visionary.init()``, calling it on first load if your
code hasn't. Works under WSGI and ASGI.
"""

from __future__ import annotations

import time
from typing import Any, Optional

from django.http import FileResponse

from .. import _context
from .._agent import Agent
from .._entry import (
    REQUEST_ID_HEADER, BodyCapture, Entry, capturable, client_ip, now, parse_content_length, request_id,
)
from ._common import CountingIterator, finish, logger, resolve_agent

try:
    from asgiref.sync import iscoroutinefunction, markcoroutinefunction
except ImportError:  # asgiref < 3.6 (Django < 4.1): sync only
    from asyncio import iscoroutinefunction

    markcoroutinefunction = None  # type: ignore[assignment]


class _State:
    __slots__ = ("entry", "t0", "ann", "token")

    def __init__(self, entry: Entry, t0: float, ann: Any, token: Any) -> None:
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

    def __call__(self, request: Any) -> Any:
        if self._async:
            return self._acall(request)
        if not self.agent.enabled:
            return self.get_response(request)
        state = self._begin(request)
        try:
            response = self.get_response(request)
        except BaseException:
            self._end(request, None, state)
            raise
        return self._end(request, response, state)

    async def _acall(self, request: Any) -> Any:
        if not self.agent.enabled:
            return await self.get_response(request)
        state = self._begin(request)
        try:
            response = await self.get_response(request)
        except BaseException:
            self._end(request, None, state)
            raise
        return self._end(request, response, state)

    def _begin(self, request: Any) -> Optional[_State]:
        self.agent.http.started()
        try:
            if not self.agent.should_log(request.path):
                return None
            e = Entry()
            e.started_at = now()
            e.request_id = request_id(request.headers.get(REQUEST_ID_HEADER))
            ann, token = _context.begin()
            return _State(e, time.perf_counter(), ann, token)
        except Exception:
            logger.exception("visionary: request capture failed")
            return None

    def _end(self, request: Any, response: Any, state: Optional[_State]) -> Any:
        status = getattr(response, "status_code", 500) if response is not None else 500
        if state is None:
            self.agent.http.finished(status)
            return response
        _context.end(state.token)
        try:
            streamed = self._fill(request, response, state)
        except Exception:
            logger.exception("visionary: request capture failed")
            streamed = False
        if not streamed:
            self.agent.http.finished(status)
            finish(self.agent, state.entry, state.t0, status)
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
            finish(self.agent, e, state.t0, status)

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
