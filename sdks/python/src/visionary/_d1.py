from __future__ import annotations

import json
import urllib.error
import urllib.request
from typing import Any, List, Optional, Sequence, Tuple

DEFAULT_API_BASE = "https://api.cloudflare.com/client/v4"
MAX_BATCH = 50
_RESPONSE_LIMIT = 1 << 20


class D1Error(Exception):
    pass


class D1Client:
    """Minimal Cloudflare D1 REST client: ``/query`` with string params."""

    def __init__(self, account_id: str, database_id: str, api_token: str, api_base: str = "", timeout: float = 20.0):
        base = (api_base or DEFAULT_API_BASE).strip().rstrip("/")
        self._url = "%s/accounts/%s/d1/database/%s/query" % (base, account_id.strip(), database_id.strip())
        self._token = api_token.strip()
        self._timeout = timeout

    def query(self, sql: str, params: Sequence[str] = ()) -> Any:
        body = {"sql": sql}
        if params:
            body["params"] = list(params)
        return self._post(body)

    def batch(self, statements: List[Tuple[str, Sequence[str]]]) -> None:
        for i in range(0, len(statements), MAX_BATCH):
            chunk = statements[i:i + MAX_BATCH]
            self._post({"batch": [{"sql": sql, "params": list(params)} for sql, params in chunk]})

    def changes(self, sql: str, params: Sequence[str] = ()) -> int:
        result = self.query(sql, params)
        try:
            return int(result[0]["meta"]["changes"])
        except (TypeError, KeyError, IndexError, ValueError):
            return 0

    def _post(self, body: dict) -> Any:
        req = urllib.request.Request(
            self._url,
            data=json.dumps(body).encode("utf-8"),
            method="POST",
            headers={"Authorization": "Bearer " + self._token, "Content-Type": "application/json"},
        )
        status: Optional[int] = None
        try:
            with urllib.request.urlopen(req, timeout=self._timeout) as resp:
                status = resp.status
                raw = resp.read(_RESPONSE_LIMIT + 1)
        except urllib.error.HTTPError as e:
            status = e.code
            raw = e.read(_RESPONSE_LIMIT + 1)
        except (urllib.error.URLError, OSError) as e:
            raise D1Error("d1: request: %s" % e) from e
        if len(raw) > _RESPONSE_LIMIT:
            raise D1Error("d1: response larger than %d bytes" % _RESPONSE_LIMIT)
        try:
            env = json.loads(raw)
        except ValueError:
            raise D1Error("d1: status %s: unreadable response" % status) from None
        if not isinstance(env, dict) or not env.get("success") or (status or 0) >= 300:
            errors = env.get("errors") if isinstance(env, dict) else None
            msgs = [str(e.get("message", "")) for e in errors or [] if isinstance(e, dict)]
            raise D1Error("d1: status %s: %s" % (status, "; ".join(msgs) or "unknown D1 error"))
        return env.get("result")
