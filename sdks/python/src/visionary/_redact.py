from __future__ import annotations

import json
import re
from decimal import Decimal
from typing import Any, Dict, Iterable, List, Tuple

from ._entry import canonical_header

REDACTED = "[REDACTED]"

DEFAULT_REDACT_HEADERS = ["Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie", "X-Api-Key", "X-Auth-Token"]
DEFAULT_REDACT_FIELDS = [
    "password", "passwd", "secret", "client_secret", "token", "access_token", "refresh_token", "id_token",
    "api_key", "apikey", "otp", "pin", "card_number", "cvv", "private_key",
]


def _key_pattern(names: Iterable[str]) -> str:
    return "|".join(re.escape(n.strip()) for n in names if n and n.strip())


def _json_secrets(keys: str) -> "re.Pattern[str]":
    # Regex on purpose: bodies cut at the capture cap are invalid JSON and
    # must still be scrubbed.
    return re.compile(r'("(?:' + keys + r')"\s*:\s*)("(?:[^"\\]|\\.)*"?|[^,}\]\s]+)', re.IGNORECASE)


class Redactor:
    def __init__(self, extra_headers: List[str], extra_fields: List[str], request_only: List[str]) -> None:
        self._headers = {h.strip().lower() for h in DEFAULT_REDACT_HEADERS + list(extra_headers) if h.strip()}
        both = _key_pattern(DEFAULT_REDACT_FIELDS + list(extra_fields))
        req_keys = both
        extra = _key_pattern(request_only)
        if extra:
            req_keys += "|" + extra
        self._resp_json = _json_secrets(both)
        self._req_json = _json_secrets(req_keys)
        self._form = re.compile(r"(^|&)(" + req_keys + r")=[^&]*", re.IGNORECASE)

    def headers(self, pairs: List[Tuple[str, str]]) -> str:
        if not pairs:
            return ""
        out: Dict[str, str] = {}
        for name, value in pairs:
            key = canonical_header(name)
            out[key] = out[key] + ", " + value if key in out else value
        for key in out:
            if key.lower() in self._headers:
                out[key] = REDACTED
        return json.dumps(out, ensure_ascii=False, separators=(",", ":"), sort_keys=True)

    def request_body(self, body: bytes, content_type: str) -> str:
        if not body:
            return ""
        text = body.decode("utf-8", errors="replace")
        if "application/x-www-form-urlencoded" in (content_type or "").lower():
            return self._form.sub(r"\1\2=" + REDACTED, text)
        return self._req_json.sub(r'\1"' + REDACTED + '"', text)

    def response_body(self, body: bytes) -> str:
        if not body:
            return ""
        return self._resp_json.sub(r'\1"' + REDACTED + '"', body.decode("utf-8", errors="replace"))


def error_fields(body: bytes, code_path: str, key_path: str) -> Tuple[str, str]:
    """Error code and key at dotted paths in a JSON error body. Bodies that
    don't parse (e.g. truncated) fall back to a match on the last segment."""
    if not body:
        return "", ""
    text = body.decode("utf-8", errors="replace")
    try:
        doc = json.loads(text)
    except ValueError:
        return _match_field(text, code_path), _match_field(text, key_path)
    return _lookup(doc, code_path), _lookup(doc, key_path)


def _lookup(doc: Any, path: str) -> str:
    if not path:
        return ""
    v = doc
    for seg in path.split("."):
        if not isinstance(v, dict):
            return ""
        v = v.get(seg)
    if isinstance(v, str):
        return v
    if isinstance(v, bool):
        return ""
    if isinstance(v, int):
        return str(v)
    if isinstance(v, float):
        # Plain decimal, never exponent notation (matches Go's 'f', -1).
        text = format(Decimal(repr(v)), "f")
        return text[:-2] if text.endswith(".0") else text
    return ""


def _match_field(text: str, path: str) -> str:
    if not path:
        return ""
    last = path.rsplit(".", 1)[-1]
    m = re.search('"' + re.escape(last) + r'"\s*:\s*"([^"]*)"', text)
    return m.group(1) if m else ""
