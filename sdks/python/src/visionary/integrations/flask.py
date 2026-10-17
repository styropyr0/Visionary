"""Flask integration.

    app = Flask(__name__)
    Visionary(app)              # or: ext = Visionary(); ext.init_app(app)

Without an explicit agent it uses ``visionary.init()``. Exceptions Flask turns
into a 500 are recorded in ``errors``; Flask's handling is unchanged.
"""

from __future__ import annotations

import time
from typing import Any, Optional

from flask import Flask, request

from .. import _context
from .._agent import Agent
from .._entry import (
    REQUEST_ID_HEADER, BodyCapture, Entry, capturable, client_ip, now, parse_content_length, request_id,
)
from ._common import CountingIterator, finish, logger, report_unhandled, resolve_agent

_KEY = "visionary.state"


def _info(req: Any, rid: Optional[str]) -> _context.RequestInfo:
    def route() -> str:
        rule = req.url_rule
        return rule.rule if rule is not None else ""

    return _context.RequestInfo(rid, req.method, req.path, route=route)


class _State:
    __slots__ = ("entry", "t0", "ann", "token", "body", "done")

    def __init__(self) -> None:
        self.entry: Optional[Entry] = None
        self.t0 = 0.0
        self.ann: Any = None
        self.token: Any = None
        self.body: Optional[BodyCapture] = None
        self.done = False


class _TeeInput:
    """wsgi.input wrapper that copies what the app reads, up to the cap."""

    def __init__(self, inner: Any, capture: BodyCapture) -> None:
        self._inner = inner
        self._capture = capture

    def read(self, *args: Any) -> bytes:
        data = self._inner.read(*args)
        self._capture.feed(data)
        return data

    def readinto(self, b: Any) -> int:
        inner = getattr(self._inner, "readinto", None)
        if inner is not None:
            n = inner(b) or 0
            if n:
                self._capture.feed(bytes(memoryview(b)[:n]))
            return n
        data = self.read(len(b))
        b[:len(data)] = data
        return len(data)

    def readline(self, *args: Any) -> bytes:
        data = self._inner.readline(*args)
        self._capture.feed(data)
        return data

    def readlines(self, *args: Any) -> list:
        lines = self._inner.readlines(*args)
        for line in lines:
            self._capture.feed(line)
        return lines

    def __iter__(self) -> Any:
        for line in self._inner:
            self._capture.feed(line)
            yield line

    def __getattr__(self, name: str) -> Any:
        return getattr(self._inner, name)


class Visionary:
    def __init__(self, app: Optional[Flask] = None, agent: Optional[Agent] = None) -> None:
        self._agent = agent
        if app is not None:
            self.init_app(app)

    @property
    def agent(self) -> Agent:
        if self._agent is None:
            self._agent = resolve_agent(None)
        return self._agent

    def init_app(self, app: Flask) -> None:
        agent = self.agent
        app.extensions["visionary"] = self
        if not agent.enabled:
            return
        # First before_request (so auth hooks can call set_user) and last
        # after_request (so the final response is seen); Flask runs
        # after_request functions in reverse order.
        app.before_request_funcs.setdefault(None, []).insert(0, self._before)
        app.after_request_funcs.setdefault(None, []).insert(0, self._after)
        app.teardown_request_funcs.setdefault(None, []).insert(0, self._teardown)

    def _before(self) -> None:
        agent = self.agent
        st = _State()
        environ = request.environ
        environ[_KEY] = st
        agent.http.started()
        try:
            req = request._get_current_object()
            if not agent.should_log(request.path):
                st.ann, st.token = _context.begin(None, _info(req, None))
                return
            e = Entry()
            e.started_at = now()
            e.request_id = request_id(request.headers.get(REQUEST_ID_HEADER))
            st.entry, st.t0 = e, time.perf_counter()
            trace = agent.new_trace(e.request_id, st.t0)
            st.ann, st.token = _context.begin(trace, _info(req, e.request_id))
            cfg = agent.config
            ct = environ.get("CONTENT_TYPE", "")
            if cfg.capture_bodies and capturable(ct):
                st.body = BodyCapture(True, cfg.max_body_bytes)
                # Only before Werkzeug has opened the stream; it reads
                # environ["wsgi.input"] lazily on first access.
                if "stream" not in request._get_current_object().__dict__ and "wsgi.input" in environ:
                    environ["wsgi.input"] = _TeeInput(environ["wsgi.input"], st.body)
        except Exception:
            logger.exception("visionary: request capture failed")
        return None

    def _after(self, response: Any) -> Any:
        st = request.environ.get(_KEY)
        if st is None or st.done:
            return response
        st.done = True
        status = response.status_code
        if st.entry is None:
            self.agent.http.finished(status)
            return response
        try:
            if self._fill(st, response):
                return response
        except Exception:
            logger.exception("visionary: request capture failed")
        self.agent.http.finished(status)
        finish(self.agent, st.entry, st.t0, status, st.ann.trace)
        return response

    def _teardown(self, exc: Optional[BaseException]) -> None:
        st = request.environ.get(_KEY)
        if st is None:
            return
        if exc is not None:
            # Unhandled: Flask answered (or will propagate) a 500.
            report_unhandled(self.agent, exc)
        if st.token is not None:
            _context.end(st.token)
            st.token = None
        if st.done:
            return
        # after_request didn't run: the exception propagated (e.g. testing
        # or PROPAGATE_EXCEPTIONS), so the client gets a 500.
        st.done = True
        self.agent.http.finished(500)
        if st.entry is not None:
            try:
                self._fill(st, None)
            except Exception:
                logger.exception("visionary: request capture failed")
            finish(self.agent, st.entry, st.t0, 500, st.ann.trace if st.ann is not None else None)

    def _fill(self, st: _State, response: Any) -> bool:
        """Fill the entry; returns True when a streaming body will finish it."""
        e, cfg = st.entry, self.agent.config
        environ = request.environ
        e.method = request.method
        e.path = request.path
        e.query = environ.get("QUERY_STRING", "")
        e.ip = client_ip(environ.get("HTTP_X_FORWARDED_FOR"), environ.get("REMOTE_ADDR"))
        e.user_agent = environ.get("HTTP_USER_AGENT", "")
        e.headers = list(request.headers.items())
        e.request_content_type = environ.get("CONTENT_TYPE", "")
        rule = request.url_rule
        e.route = st.ann.route or (rule.rule if rule is not None else "")
        e.user_id, e.user_handle, e.user_role = st.ann.user()

        body = st.body
        if body is not None and body.size == 0:
            # The stream was opened before our hook, or parsed into a cache.
            cached = getattr(request._get_current_object(), "_cached_data", None)
            if cached:
                body.feed(cached)
        if body is not None:
            e.request_body, e.request_truncated = body.data, body.truncated
        e.request_size = max(parse_content_length(environ.get("CONTENT_LENGTH")), body.size if body else 0)

        if response is None:
            return False
        response.headers[REQUEST_ID_HEADER] = e.request_id
        e.response_content_type = response.headers.get("Content-Type", "")
        declared = parse_content_length(response.headers.get("Content-Length"))
        resp = BodyCapture(cfg.capture_bodies and capturable(e.response_content_type), cfg.max_body_bytes)

        if not response.is_streamed:
            resp.feed(response.get_data())
            e.response_body, e.response_truncated = resp.data, resp.truncated
            e.response_size = max(declared, resp.size)
            return False
        if response.direct_passthrough:
            # send_file: keep the file wrapper (sendfile) intact.
            e.response_size = declared
            return False

        status = response.status_code

        def done() -> None:
            e.response_body, e.response_truncated = resp.data, resp.truncated
            e.response_size = max(declared, resp.size)
            self.agent.http.finished(status)
            finish(self.agent, e, st.t0, status, st.ann.trace)

        response.response = CountingIterator(response.response, resp, done)
        return True
