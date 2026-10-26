"""ASGI integration for FastAPI, Starlette and other ASGI apps.

    app = FastAPI()
    app.add_middleware(VisionaryASGIMiddleware)

or wrap any ASGI app: ``app = VisionaryASGIMiddleware(app)``. Without an
explicit agent it uses ``visionary.init()``.

Exceptions escaping the app are recorded in ``errors`` and re-raised, so the
server's own 500 handling is unchanged.
"""

from __future__ import annotations

import time
from typing import Any, Awaitable, Callable, Dict, List, MutableMapping, Optional, Tuple

from .. import _context, _profiler
from .._agent import Agent
from .._entry import (
    REQUEST_ID_HEADER, BodyCapture, Entry, capturable, client_ip, now, parse_content_length, request_id,
)
from ._common import finish, finish_unlogged, logger, report_unhandled, resolve_agent

Scope = MutableMapping[str, Any]
Message = MutableMapping[str, Any]
Receive = Callable[[], Awaitable[Message]]
Send = Callable[[Message], Awaitable[None]]

_RID = REQUEST_ID_HEADER.lower().encode("latin-1")


class VisionaryASGIMiddleware:
    def __init__(self, app: Callable[..., Awaitable[None]], agent: Optional[Agent] = None) -> None:
        self.app = app
        self._agent = agent

    @property
    def agent(self) -> Agent:
        if self._agent is None:
            self._agent = resolve_agent(None)
        return self._agent

    async def __call__(self, scope: Scope, receive: Receive, send: Send) -> None:
        if scope.get("type") != "http":
            await self.app(scope, receive, send)
            return
        agent = self.agent
        if not agent.enabled:
            await self.app(scope, receive, send)
            return
        agent.http.started()
        _profiler.request_started()
        status = [500]
        try:
            if agent.should_log(scope.get("path", "")):
                await self._logged(agent, scope, receive, send, status)
            else:
                async def send_status(message: Message) -> None:
                    if message["type"] == "http.response.start":
                        status[0] = message["status"]
                    await send(message)

                t0 = time.perf_counter()
                ann, token = _context.begin(None, _info(scope, None))
                try:
                    await self.app(scope, receive, send_status)
                except BaseException as e:
                    report_unhandled(agent, e)
                    raise
                finally:
                    _context.end(token)
                    finish_unlogged(scope.get("method", ""), ann.route or _route(scope) or scope.get("path", ""), t0)
        finally:
            agent.http.finished(status[0])

    async def _logged(self, agent: Agent, scope: Scope, receive: Receive, send: Send, status: List[int]) -> None:
        cfg = agent.config
        t0 = time.perf_counter()
        e = Entry()
        e.started_at = now()
        headers = _headers(scope)
        lookup = {k.lower(): v for k, v in headers}
        e.request_id = request_id(lookup.get("x-request-id"))
        e.request_content_type = lookup.get("content-type", "")
        req = BodyCapture(cfg.capture_bodies and capturable(e.request_content_type), cfg.max_body_bytes)
        resp = BodyCapture(False, cfg.max_body_bytes)
        state: Dict[str, Any] = {"declared": 0}
        rid = e.request_id.encode("latin-1", errors="replace")

        async def tee_receive() -> Message:
            message = await receive()
            if message.get("type") == "http.request":
                req.feed(message.get("body", b""))
            return message

        async def tee_send(message: Message) -> None:
            kind = message.get("type")
            if kind == "http.response.start":
                status[0] = message.get("status", 200)
                out = [(k, v) for k, v in message.get("headers", []) if k.lower() != _RID]
                out.append((_RID, rid))
                for k, v in out:
                    lk = k.lower()
                    if lk == b"content-type":
                        e.response_content_type = v.decode("latin-1")
                    elif lk == b"content-length":
                        state["declared"] = parse_content_length(v.decode("latin-1"))
                resp.enabled = cfg.capture_bodies and capturable(e.response_content_type)
                message = dict(message)
                message["headers"] = out
            elif kind == "http.response.body":
                resp.feed(message.get("body", b""))
            await send(message)

        trace = agent.new_trace(e.request_id, t0)
        ann, token = _context.begin(trace, _info(scope, e.request_id))
        try:
            await self.app(scope, tee_receive, tee_send)
        except BaseException as exc:
            report_unhandled(agent, exc)
            raise
        finally:
            _context.end(token)
            try:
                e.method = scope.get("method", "")
                e.path = scope.get("path", "")
                e.query = scope.get("query_string", b"").decode("latin-1")
                client = scope.get("client")
                e.ip = client_ip(lookup.get("x-forwarded-for"), client[0] if client else "")
                e.user_agent = lookup.get("user-agent", "")
                e.headers = headers
                e.route = ann.route or _route(scope)
                e.user_id, e.user_handle, e.user_role = ann.user()
                e.login_account = ann.login_account
                e.request_body, e.request_truncated = req.data, req.truncated
                e.request_size = max(parse_content_length(lookup.get("content-length")), req.size)
                e.response_body, e.response_truncated = resp.data, resp.truncated
                e.response_size = max(state["declared"], resp.size)
                finish(agent, e, t0, status[0], trace)
            except Exception:
                logger.exception("visionary: request capture failed")


def _info(scope: Scope, rid: Optional[str]) -> _context.RequestInfo:
    return _context.RequestInfo(rid, scope.get("method", ""), scope.get("path", ""), route=lambda: _route(scope))


def _headers(scope: Scope) -> List[Tuple[str, str]]:
    return [(k.decode("latin-1"), v.decode("latin-1")) for k, v in scope.get("headers", [])]


def _route(scope: Scope) -> str:
    # FastAPI/Starlette routers write the matched route into the scope we
    # passed down, so it is visible here after the call.
    route = scope.get("route")
    if route is None:
        return ""
    return str(getattr(route, "path_format", None) or getattr(route, "path", "") or "")
