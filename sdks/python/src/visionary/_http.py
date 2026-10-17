"""Outbound HTTP spans: ``requests``, ``httpx`` (sync and async) and ``urllib3``.

``instrument_http()`` patches the libraries that are installed, once. A
request made through ``requests`` also passes through urllib3; the context
flag below keeps that to one span.
"""

from __future__ import annotations

import functools
import threading
from contextvars import ContextVar
from typing import Any, Optional, Tuple
from urllib.parse import urlsplit, urlunsplit

from . import _spans

_inside: ContextVar[bool] = ContextVar("visionary_http_inside", default=False)
_lock = threading.Lock()
_patched: set = set()
_DEFAULT_PORTS = {"http": 80, "https": 443}


def instrument_http() -> list:
    """Patch requests, httpx and urllib3 (whichever are installed) so their
    calls become ``http`` spans. Safe to call more than once; returns the
    libraries patched so far."""
    with _lock:
        for name, patch in (("requests", _patch_requests), ("httpx", _patch_httpx), ("urllib3", _patch_urllib3)):
            if name in _patched:
                continue
            try:
                patch()
            except ImportError:
                continue
            _patched.add(name)
        if _patched:
            _spans.http_visible = True
        return sorted(_patched)


def describe(method: str, url: str) -> Tuple[str, str, str]:
    """(name, target, detail) for a call: ``GET api.example.com``, the host,
    and the method with the URL minus credentials, query and fragment."""
    method = (method or "GET").upper()
    try:
        u = urlsplit(url)
        host = u.hostname or ""
        port = u.port
    except ValueError:
        return method, "", method
    if port and port != _DEFAULT_PORTS.get(u.scheme):
        host = "%s:%d" % (host, port)
    clean = urlunsplit((u.scheme, host, u.path, "", ""))
    return "%s %s" % (method, host), host, "%s %s" % (method, clean)


def _begin(method: str, url: str) -> Tuple[Optional[_spans.Span], Any]:
    if _inside.get():
        return None, None
    name, target, detail = describe(method, url)
    span = _spans.start("http", name, target, detail)
    if span is None:
        return None, None
    return span, _inside.set(True)


def _end(span: Optional[_spans.Span], token: Any, status: Optional[int], error: Optional[BaseException]) -> None:
    if span is None:
        return
    _inside.reset(token)
    if error is not None:
        span.finish(error=error)
    elif status is not None and status >= 500:
        span.finish(error="HTTP %d" % status)
    else:
        span.finish()


def _patch_requests() -> None:
    import requests.sessions

    original = requests.sessions.Session.send
    if getattr(original, "__visionary__", False):
        return

    @functools.wraps(original)
    def send(self: Any, request: Any, **kwargs: Any) -> Any:
        span, token = _begin(getattr(request, "method", ""), str(getattr(request, "url", "")))
        try:
            resp = original(self, request, **kwargs)
        except BaseException as e:
            _end(span, token, None, e)
            raise
        _end(span, token, getattr(resp, "status_code", None), None)
        return resp

    send.__visionary__ = True  # type: ignore[attr-defined]
    requests.sessions.Session.send = send  # type: ignore[assignment]


def _patch_httpx() -> None:
    import httpx

    sync_send = httpx.Client.send
    if not getattr(sync_send, "__visionary__", False):
        @functools.wraps(sync_send)
        def send(self: Any, request: Any, *args: Any, **kwargs: Any) -> Any:
            span, token = _begin(request.method, str(request.url))
            try:
                resp = sync_send(self, request, *args, **kwargs)
            except BaseException as e:
                _end(span, token, None, e)
                raise
            _end(span, token, resp.status_code, None)
            return resp

        send.__visionary__ = True  # type: ignore[attr-defined]
        httpx.Client.send = send  # type: ignore[assignment]

    async_send = httpx.AsyncClient.send
    if not getattr(async_send, "__visionary__", False):
        @functools.wraps(async_send)
        async def asend(self: Any, request: Any, *args: Any, **kwargs: Any) -> Any:
            span, token = _begin(request.method, str(request.url))
            try:
                resp = await async_send(self, request, *args, **kwargs)
            except BaseException as e:
                _end(span, token, None, e)
                raise
            _end(span, token, resp.status_code, None)
            return resp

        asend.__visionary__ = True  # type: ignore[attr-defined]
        httpx.AsyncClient.send = asend  # type: ignore[assignment]


def _patch_urllib3() -> None:
    import urllib3.connectionpool

    pool = urllib3.connectionpool.HTTPConnectionPool
    original = pool.urlopen
    if getattr(original, "__visionary__", False):
        return

    @functools.wraps(original)
    def urlopen(self: Any, method: str, url: str, *args: Any, **kwargs: Any) -> Any:
        if _inside.get():
            return original(self, method, url, *args, **kwargs)
        full = url if "://" in url else "%s://%s:%s%s" % (self.scheme, self.host, self.port, url)
        span, token = _begin(method, full)
        try:
            resp = original(self, method, url, *args, **kwargs)
        except BaseException as e:
            _end(span, token, None, e)
            raise
        _end(span, token, getattr(resp, "status", None), None)
        return resp

    urlopen.__visionary__ = True  # type: ignore[attr-defined]
    pool.urlopen = urlopen  # type: ignore[assignment]
