from __future__ import annotations

import secrets
import time
from datetime import datetime, timezone
from typing import Any, List, Optional, Tuple

REQUEST_ID_HEADER = "X-Request-ID"

_UNCAPTURABLE = (
    "multipart/",
    "image/",
    "video/",
    "audio/",
    "application/octet-stream",
    "application/zip",
    "application/pdf",
)


class Entry:
    """The raw capture of one request. Integrations fill it on the request
    path; redaction, JSON encoding and user lookup happen on the writer."""

    __slots__ = (
        "request_id", "started_at", "method", "path", "route", "query", "status",
        "duration", "ip", "user_agent", "user_id", "user_handle", "user_role", "login_account",
        "headers", "request_content_type", "request_body", "request_truncated",
        "request_size", "response_content_type", "response_body",
        "response_truncated", "response_size",
        "db_count", "db_ms", "http_count", "http_ms", "spans",
    )

    def __init__(self) -> None:
        self.request_id = ""
        self.started_at = 0.0  # epoch seconds
        self.method = ""
        self.path = ""
        self.route = ""
        self.query = ""
        self.status = 200
        self.duration = 0.0  # seconds
        self.ip = ""
        self.user_agent = ""
        self.user_id = ""
        self.user_handle = ""
        self.user_role = ""
        self.login_account = ""
        self.headers: List[Tuple[str, str]] = []
        self.request_content_type = ""
        self.request_body = b""
        self.request_truncated = False
        self.request_size = 0
        self.response_content_type = ""
        self.response_body = b""
        self.response_truncated = False
        self.response_size = 0
        # Call totals (None when the SDK can't see that kind of call) and the
        # spans kept for this request, filled when it finishes.
        self.db_count: Optional[int] = None
        self.db_ms: Optional[float] = None
        self.http_count: Optional[int] = None
        self.http_ms: Optional[float] = None
        self.spans: List[Any] = []


def clip_login(account: str) -> str:
    """Trims a login identifier to what the column holds (255 characters)."""
    return account.strip()[:255]


class BodyCapture:
    """Counts every byte that passes and keeps the first ``limit`` of them."""

    __slots__ = ("enabled", "limit", "buf", "size", "truncated")

    def __init__(self, enabled: bool, limit: int) -> None:
        self.enabled = enabled
        self.limit = limit
        self.buf = bytearray()
        self.size = 0
        self.truncated = False

    def feed(self, chunk: bytes) -> None:
        if not chunk:
            return
        self.size += len(chunk)
        if not self.enabled:
            return
        room = self.limit - len(self.buf)
        if room <= 0:
            self.truncated = True
        elif len(chunk) > room:
            self.buf += chunk[:room]
            self.truncated = True
        else:
            self.buf += chunk

    @property
    def data(self) -> bytes:
        return bytes(self.buf)


def capturable(content_type: Optional[str]) -> bool:
    """Binary and multipart payloads are useless as text and costly to store."""
    ct = (content_type or "").strip().lower()
    return not ct.startswith(_UNCAPTURABLE)


def request_id(incoming: Optional[str]) -> str:
    if incoming and len(incoming) <= 128:
        return incoming
    return secrets.token_hex(16)


def client_ip(forwarded_for: Optional[str], peer: Optional[str]) -> str:
    # The first X-Forwarded-For hop, as most deployments sit behind a proxy.
    # Informational only, never used for decisions.
    if forwarded_for:
        return forwarded_for.split(",", 1)[0].strip()
    return peer or ""


def format_ts(epoch: float) -> str:
    """``YYYY-MM-DDTHH:MM:SS.mmmZ`` in UTC, so string order is time order."""
    dt = datetime.fromtimestamp(epoch, tz=timezone.utc)
    return dt.strftime("%Y-%m-%dT%H:%M:%S.") + "%03dZ" % (dt.microsecond // 1000)


def now() -> float:
    return time.time()


def canonical_header(name: str) -> str:
    """Go's http.CanonicalHeaderKey, so every SDK stores the same keys."""
    return "-".join(p[:1].upper() + p[1:].lower() for p in name.split("-"))


def parse_content_length(value: Optional[str]) -> int:
    try:
        n = int(value or "")
    except ValueError:
        return 0
    return max(n, 0)
