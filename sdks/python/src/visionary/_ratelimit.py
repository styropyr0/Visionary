"""Rate limits from Security -> Rate limits (SPEC "Rate limits"): rules
polled from ``visionary_settings``, enforced by the integrations before the
view runs (request phase) and by ``check_rate_limit()`` (user phase), with
what was limited written to ``rate_limit_events``. Counters are per process.

A port of the Go reference, ``sdks/go/ratelimit``.
"""

from __future__ import annotations

import hashlib
import ipaddress
import json
import logging
import math
import re
import threading
import time
from typing import Any, Callable, Dict, List, Optional, Tuple
from urllib.parse import parse_qs

from . import _context, _spans
from ._entry import format_ts
from ._redact import DEFAULT_REDACT_HEADERS

SETTING_VERSION = "ratelimit.version"
SETTING_CONFIG = "ratelimit.config"
POLL_S = 15.0

MAX_WINDOW = 86400
MAX_COUNTERS = 10000
MAX_AGGREGATES = 1000

DEFAULT_COUNTRY_HEADERS = ["CF-IPCountry", "CloudFront-Viewer-Country", "X-Vercel-IP-Country", "Fastly-Geo-Country",
                           "X-AppEngine-Country", "X-Country-Code"]

LIMITED, BLOCKED, WOULD_LIMIT, WOULD_BLOCK = "limited", "blocked", "would_limit", "would_block"

_ID = re.compile(r"^[A-Za-z0-9_-]{1,32}$")


class _Bad(Exception):
    """A rule field of the wrong type: the rule is dropped."""


# Parsing mirrors Go's encoding/json into typed fields: null is the zero
# value, a wrong type drops the rule.

def _str(v: Any) -> str:
    if v is None:
        return ""
    if not isinstance(v, str):
        raise _Bad
    return v


def _num(v: Any) -> float:
    if v is None:
        return 0.0
    if isinstance(v, bool) or not isinstance(v, (int, float)):
        raise _Bad
    return float(v)


def _int(v: Any) -> int:
    if v is None:
        return 0
    if isinstance(v, bool) or not isinstance(v, int):
        raise _Bad
    return v


def _bool(v: Any) -> Optional[bool]:
    if v is None:
        return None
    if not isinstance(v, bool):
        raise _Bad
    return v


def _strs(v: Any) -> List[str]:
    if v is None:
        return []
    if not isinstance(v, list):
        raise _Bad
    return [_str(x) for x in v]


def _obj(v: Any) -> Dict[str, Any]:
    if v is None:
        return {}
    if not isinstance(v, dict):
        raise _Bad
    return v


def _rule(raw: Any) -> Optional[Dict[str, Any]]:
    if raw is None:
        raw = {}
    if not isinstance(raw, dict):
        return None
    try:
        return {
            "id": _str(raw.get("id")), "name": _str(raw.get("name")), "enabled": _bool(raw.get("enabled")),
            "mode": _str(raw.get("mode")), "action": _str(raw.get("action")),
            "services": _strs(raw.get("services")), "methods": _strs(raw.get("methods")),
            "paths": _strs(raw.get("paths")), "ips": _strs(raw.get("ips")), "countries": _strs(raw.get("countries")),
            "match": {k: _strs(v) for k, v in _obj(raw.get("match")).items()},
            "key": _str(raw.get("key")), "limit": _num(raw.get("limit")), "window_s": _num(raw.get("window_s")),
            "algorithm": _str(raw.get("algorithm")), "burst": _num(raw.get("burst")),
            "limit_by": _str(raw.get("limit_by")),
            "limits": {k: _num(v) for k, v in _obj(raw.get("limits")).items()},
            "status": _int(raw.get("status")), "body": _str(raw.get("body")),
            "content_type": _str(raw.get("content_type")),
        }
    except _Bad:
        return None


def parse(raw: str) -> Dict[str, Any]:
    """The stored document, read leniently: a rule that does not decode is
    dropped, the rest kept. Raises ValueError only for invalid JSON."""
    doc = json.loads(raw)
    cfg: Dict[str, Any] = {"enabled": None, "ip_header": "", "country_headers": [], "rules": []}
    if not isinstance(doc, dict):
        return cfg
    for name, conv in (("enabled", _bool), ("ip_header", _str), ("country_headers", _strs)):
        try:
            cfg[name] = conv(doc.get(name))
        except _Bad:
            pass
    rules = doc.get("rules")
    if isinstance(rules, list):
        cfg["rules"] = [r for r in (_rule(x) for x in rules) if r is not None]
    return cfg


# Properties: (kind, name).
_IP, _COUNTRY, _METHOD, _PATH, _HEADER, _QUERY, _GLOBAL, _USER, _ROLE, _ATTR = range(10)
_USER_PHASE = (_USER, _ROLE, _ATTR)
_SIMPLE = {"ip": _IP, "country": _COUNTRY, "method": _METHOD, "path": _PATH, "global": _GLOBAL, "user": _USER,
           "role": _ROLE}


def _prop(s: str) -> Optional[Tuple[int, str]]:
    for prefix, kind in (("header:", _HEADER), ("query:", _QUERY), ("attr:", _ATTR)):
        if s.startswith(prefix) and len(s) > len(prefix):
            name = s[len(prefix):]
            return kind, name.lower() if kind == _HEADER else name
    if s in _SIMPLE:
        return _SIMPLE[s], ""
    return None


def _prefix(s: str) -> Optional[Any]:
    s = s.strip()
    try:
        if "/" in s:
            net = ipaddress.ip_network(s, strict=False)
            if net.version == 6 and net.prefixlen >= 96 and net.network_address.ipv4_mapped is not None:
                net = ipaddress.ip_network("%s/%d" % (net.network_address.ipv4_mapped, net.prefixlen - 96),
                                           strict=False)
            return net
        return ipaddress.ip_network(str(_addr(s)))
    except ValueError:
        return None


def _addr(s: str) -> Any:
    s = s.strip()
    if "%" in s:
        s = s.split("%", 1)[0]
    a = ipaddress.ip_address(s)
    if a.version == 6 and a.ipv4_mapped is not None:
        return a.ipv4_mapped
    return a


class _Counter:
    __slots__ = ("win", "curr", "prev", "tokens", "last")

    def __init__(self, tokens: float, last: float) -> None:
        self.win: Optional[int] = None
        self.curr = self.prev = 0.0
        self.tokens, self.last = tokens, last


class _Counters:
    def __init__(self) -> None:
        self.lock = threading.Lock()
        self.m: Dict[str, _Counter] = {}


_SLIDING, _FIXED, _BUCKET = range(3)
_ALGS = {"": _SLIDING, "sliding": _SLIDING, "fixed": _FIXED, "token_bucket": _BUCKET}


class _Rule:
    def __init__(self, src: Dict[str, Any]) -> None:
        self.id = src["id"]
        self.name = src["name"] or self.id
        self.action = src["action"]
        self.status, self.body, self.content_type = src["status"], src["body"], src["content_type"]
        self.monitor = src["mode"] == "monitor"
        self.methods = [m.strip().upper() for m in src["methods"]]
        self.paths = src["paths"]
        self.has_ips = bool(src["ips"])
        self.prefixes = [p for p in (_prefix(s) for s in src["ips"]) if p is not None]
        self.countries = [c.strip().upper() for c in src["countries"]]
        self.match: List[Tuple[Tuple[int, str], List[str]]] = []
        self.user_phase = False
        self.key: Tuple[int, str] = (_IP, "")
        self.limit = self.window = self.burst = 0.0
        self.alg = _SLIDING
        self.limit_by: Optional[Tuple[int, str]] = None
        self.limits: Dict[str, float] = {}
        self.counters = _Counters()

    def shape(self) -> tuple:
        return self.key, self.window, self.alg, self.burst

    def deny(self, retry: int) -> "Denial":
        status = self.status or (403 if self.action == "block" else 429)
        if self.body:
            body = self.body.replace("{retry_after}", str(retry))
            ctype = "application/json" if self.body.lstrip(" \t\r\n")[:1] in ("{", "[") else "text/plain; charset=utf-8"
        elif self.action == "block":
            body = '{"error":{"code":"blocked","key":"%s","message":"Forbidden"}}' % self.id
            ctype = "application/json"
        else:
            body = '{"error":{"code":"rate_limited","key":"%s","message":"Too many requests"}}' % self.id
            ctype = "application/json"
        return Denial(status, self.id, retry, body.encode("utf-8"), self.content_type or ctype)

    def capacity(self, limit: float) -> float:
        return self.burst if self.burst > 0 else limit

    def hit(self, key: str, limit: float, t: float) -> Tuple[int, bool]:
        """Take one request on key's counter: (seconds to wait, over)."""
        W = self.window
        if limit == 0:
            return int(math.ceil(W)), True
        cs = self.counters
        with cs.lock:
            c = cs.m.get(key)
            if c is None:
                if len(cs.m) >= MAX_COUNTERS:
                    self.sweep_locked(t)
                    if len(cs.m) >= MAX_COUNTERS:
                        cs.m = {}
                c = _Counter(self.capacity(limit), t)
                cs.m[key] = c
            if self.alg == _BUCKET:
                C, R = self.capacity(limit), limit / W
                if t > c.last:
                    c.tokens = min(C, c.tokens + (t - c.last) * R)
                    c.last = t
                if c.tokens < 1:
                    return max(1, int(math.ceil((1 - c.tokens) / R))), True
                c.tokens -= 1
                return 0, False
            w = int(math.floor(t / W))
            if c.win is None:
                c.prev = c.curr = 0.0
            elif w == c.win + 1:
                c.prev, c.curr = c.curr, 0.0
            elif w > c.win + 1:
                c.prev = c.curr = 0.0
            c.win = w if c.win is None else max(c.win, w)
            if self.alg == _FIXED:
                over = c.curr >= limit
            else:
                e = t - w * W
                over = c.prev * (W - e) / W + c.curr + 1 > limit
            if over:
                return max(1, int(math.ceil((w + 1) * W - t))), True
            c.curr += 1
            return 0, False

    def sweep_locked(self, t: float) -> None:
        w = int(math.floor(t / self.window)) if self.window else 0
        m = self.counters.m
        for k in list(m):
            c = m[k]
            if self.alg == _BUCKET:
                # the limit may differ per key; the static one is close enough to call it idle
                if self.limit <= 0 or c.tokens + (t - c.last) * (self.limit / self.window) >= self.capacity(self.limit):
                    del m[k]
            elif c.win is not None and c.win < w - 1:
                del m[k]


def _compile_rule(src: Dict[str, Any]) -> Optional[_Rule]:
    if not _ID.match(src["id"]) or src["mode"] not in ("", "enforce", "monitor") \
            or src["action"] not in ("limit", "block", "allow"):
        return None
    r = _Rule(src)
    for name, values in src["match"].items():
        p = _prop(name)
        if p is None or p[0] == _GLOBAL:
            return None
        r.match.append((p, values))
        r.user_phase = r.user_phase or p[0] in _USER_PHASE
    if r.action != "limit":
        return r
    k = _prop(src["key"] or "ip")
    if k is None:
        return None
    r.key = k
    r.user_phase = r.user_phase or k[0] in _USER_PHASE
    if src["window_s"] < 1 or src["window_s"] > MAX_WINDOW or src["algorithm"] not in _ALGS:
        return None
    r.window, r.limit, r.burst, r.alg = src["window_s"], src["limit"], src["burst"], _ALGS[src["algorithm"]]
    if src["limit_by"]:
        p = _prop(src["limit_by"])
        if p is None or p[0] == _GLOBAL:
            return None
        r.limit_by, r.limits = p, src["limits"]
        r.user_phase = r.user_phase or p[0] in _USER_PHASE
    return r


class _RuleSet:
    def __init__(self, cfg: Dict[str, Any], service: str) -> None:
        self.ip_header = cfg["ip_header"].strip()
        self.country_headers = [h.strip() for h in cfg["country_headers"] if h.strip()] or DEFAULT_COUNTRY_HEADERS
        self.request: List[_Rule] = []
        self.user: List[_Rule] = []
        self.by_id: Dict[str, _Rule] = {}
        if cfg["enabled"] is False:
            return
        for src in cfg["rules"]:
            r = _compile_rule(src)
            if r is None or src["enabled"] is False or r.id in self.by_id or not _serves(src["services"], service):
                continue
            self.by_id[r.id] = r
            (self.user if r.user_phase else self.request).append(r)


def _serves(lst: List[str], service: str) -> bool:
    if not lst:
        return True
    return bool(service) and service in lst


_EMPTY = {"enabled": None, "ip_header": "", "country_headers": [], "rules": []}


class Request:
    """What the limiter sees of a request. ``header`` and ``query`` return a
    header's (name case-insensitive) or query parameter's first value, "" when
    absent."""

    __slots__ = ("method", "path", "remote_ip", "header", "query")

    def __init__(self, method: str, path: str, remote_ip: str, header: Optional[Callable[[str], str]] = None,
                 query: Optional[Callable[[str], str]] = None) -> None:
        self.method, self.path, self.remote_ip = method, path, remote_ip
        self.header, self.query = header, query


class User:
    __slots__ = ("id", "role")

    def __init__(self, id: str = "", role: str = "") -> None:
        self.id, self.role = id, role


class Denial:
    """The answer for a limited or blocked request."""

    __slots__ = ("status", "rule_id", "retry_after", "body", "content_type")

    def __init__(self, status: int, rule_id: str, retry_after: int, body: bytes, content_type: str) -> None:
        self.status, self.rule_id, self.retry_after = status, rule_id, retry_after
        self.body, self.content_type = body, content_type

    @property
    def headers(self) -> Dict[str, str]:
        h = {"Content-Type": self.content_type}
        if self.retry_after > 0:
            h["Retry-After"] = str(self.retry_after)
        return h


class State:
    """Carries one request between the two phases."""

    __slots__ = ("set", "ip", "country", "allowed", "user_done", "denial")

    def __init__(self) -> None:
        self.set: Optional[_RuleSet] = None
        self.ip = self.country = ""
        self.allowed = self.user_done = False
        self.denial: Optional[Denial] = None


class Event:
    __slots__ = ("created_at", "rule_id", "rule_name", "outcome", "key_value", "ip", "country", "method", "path",
                 "user_id", "hits")

    def __init__(self, **kw: Any) -> None:
        for k in self.__slots__:
            setattr(self, k, kw.get(k, ""))
        self.hits = 0


class Limiter:
    """Evaluates the rules in force and keeps the counters. Thread-safe."""

    def __init__(self, service: str = "") -> None:
        self._service = service
        self._set: Optional[_RuleSet] = None
        self._shapes: Dict[str, tuple] = {}
        self._load_lock = threading.Lock()
        self._redact = {h.lower() for h in DEFAULT_REDACT_HEADERS}
        self._agg_lock = threading.Lock()
        self._agg: Dict[tuple, Event] = {}
        # Sees every recorded outcome (tests).
        self._trace: Optional[Callable[[str, str], None]] = None

    def set_redact_headers(self, names: List[str]) -> None:
        self._redact = {n.strip().lower() for n in names}

    def load(self, cfg: Dict[str, Any]) -> None:
        """Put a parsed config in force, carrying over the counters of rules
        whose key, window, algorithm and burst did not change."""
        with self._load_lock:
            nxt = _RuleSet(cfg, self._service)
            old = self._set
            shapes = {}
            for rid, r in nxt.by_id.items():
                shapes[rid] = r.shape()
                if old is not None and rid in old.by_id and self._shapes.get(rid) == shapes[rid]:
                    r.counters = old.by_id[rid].counters
            self._shapes = shapes
            self._set = nxt

    def clear(self) -> None:
        self.load(_EMPTY)

    def active(self) -> bool:
        s = self._set
        return s is not None and bool(s.by_id)

    def check_request(self, req: Request, now: float) -> State:
        st = State()
        s = self._set
        if s is None or not s.by_id:
            return st
        try:
            st.set = s
            st.ip = _client_ip(req, s.ip_header)
            st.country = _country(req, s.country_headers)
            self._run(s.request, st, req, None, None, now)
            return st
        except Exception:
            return State()  # fail open

    def check_user(self, st: Optional[State], req: Request, user: User, attrs: Optional[Dict[str, str]],
                   now: float) -> Optional[Denial]:
        """The user phase, once per request; later calls repeat the outcome."""
        if st is None or st.set is None:
            return None
        if st.user_done or st.allowed or st.denial is not None:
            return st.denial
        st.user_done = True
        if not st.set.user:
            return None
        try:
            self._run(st.set.user, st, req, user, attrs or {}, now)
        except Exception:
            return None
        return st.denial

    def _run(self, rules: List[_Rule], st: State, req: Request, user: Optional[User],
             attrs: Optional[Dict[str, str]], now: float) -> None:
        for r in rules:
            if not self._matches(r, st, req, user, attrs):
                continue
            if r.action == "allow":
                if not r.monitor:
                    st.allowed = True
                    return
            elif r.action == "block":
                if r.monitor:
                    self._record(r, WOULD_BLOCK, "", st, req, user, now)
                    continue
                self._record(r, BLOCKED, "", st, req, user, now)
                st.denial = r.deny(0)
                return
            elif r.action == "limit":
                key = _value(r.key, st, req, user, attrs)
                if not key:
                    continue
                limit = r.limit
                if r.limit_by is not None:
                    v = _value(r.limit_by, st, req, user, attrs)
                    if v in r.limits:
                        limit = r.limits[v]
                if limit < 0:
                    continue
                retry, over = r.hit(key, limit, now)
                if not over:
                    continue
                if r.monitor:
                    self._record(r, WOULD_LIMIT, key, st, req, user, now)
                    continue
                self._record(r, LIMITED, key, st, req, user, now)
                st.denial = r.deny(retry)
                return

    def _matches(self, r: _Rule, st: State, req: Request, user: Optional[User],
                 attrs: Optional[Dict[str, str]]) -> bool:
        if r.methods and req.method.upper() not in r.methods:
            return False
        if r.paths and not any(glob(g, req.path) for g in r.paths):
            return False
        if r.has_ips and not _in_prefixes(r.prefixes, st.ip):
            return False
        if r.countries and (not st.country or st.country not in r.countries):
            return False
        for p, values in r.match:
            v = _value(p, st, req, user, attrs)
            if not values:
                if not v:
                    return False
            elif v not in values:
                return False
        return True

    def sweep(self, now: float) -> None:
        """Drop idle counters of every rule (at each poll)."""
        s = self._set
        if s is None:
            return
        for r in list(s.by_id.values()):
            with r.counters.lock:
                r.sweep_locked(now)

    def _record(self, r: _Rule, outcome: str, key: str, st: State, req: Request, user: Optional[User],
                now: float) -> None:
        if self._trace is not None:
            self._trace(r.id, outcome)
        if key and r.key[0] == _HEADER and r.key[1] in self._redact:
            key = "sha256:" + hashlib.sha256(key.encode("utf-8")).hexdigest()[:12]
        key = key[:200]
        ip = st.ip
        with self._agg_lock:
            agg_id = (r.id, outcome, key, ip)
            e = self._agg.get(agg_id)
            if e is None and len(self._agg) >= MAX_AGGREGATES:
                key, ip = "(other)", ""
                agg_id = (r.id, outcome, key, ip)
                e = self._agg.get(agg_id)
            if e is None:
                e = Event(created_at=now, rule_id=r.id, rule_name=r.name, outcome=outcome, key_value=key, ip=ip,
                          country=st.country, method=req.method.upper(), path=req.path[:300],
                          user_id=user.id if user is not None else "")
                self._agg[agg_id] = e
            e.hits += 1

    def drain(self) -> List[Event]:
        """The outcomes recorded since the last call, oldest first."""
        with self._agg_lock:
            out, self._agg = list(self._agg.values()), {}
        return out


def _value(p: Tuple[int, str], st: State, req: Request, user: Optional[User], attrs: Optional[Dict[str, str]]) -> str:
    kind, name = p
    if kind == _IP:
        return st.ip
    if kind == _COUNTRY:
        return st.country
    if kind == _METHOD:
        return req.method.upper()
    if kind == _PATH:
        return req.path
    if kind == _HEADER:
        return (req.header(name) or "") if req.header is not None else ""
    if kind == _QUERY:
        return (req.query(name) or "") if req.query is not None else ""
    if kind == _GLOBAL:
        return "*"
    if kind == _USER:
        return user.id if user is not None else ""
    if kind == _ROLE:
        return user.role if user is not None else ""
    if kind == _ATTR:
        return (attrs or {}).get(name, "")
    return ""


def _client_ip(req: Request, header: str) -> str:
    if header and req.header is not None:
        v = req.header(header.lower()) or ""
        first = v.split(",", 1)[0].strip()
        if first:
            return first
    return req.remote_ip


def _country(req: Request, headers: List[str]) -> str:
    if req.header is None:
        return ""
    for h in headers:
        v = (req.header(h.lower()) or "").strip()
        if v:
            return v.upper()
    return ""


def _in_prefixes(lst: List[Any], ip: str) -> bool:
    try:
        a = _addr(ip)
    except ValueError:
        return False
    return any(a.version == p.version and a in p for p in lst)


def glob(pattern: str, s: str) -> bool:
    """Whether the whole of s matches pattern, where * is any run of characters."""
    p = i = 0
    star, mark = -1, 0
    while i < len(s):
        if p < len(pattern) and pattern[p] == "*":
            star, mark = p, i
            p += 1
        elif p < len(pattern) and pattern[p] == s[i]:
            p += 1
            i += 1
        elif star >= 0:
            p = star + 1
            mark += 1
            i = mark
        else:
            return False
    while p < len(pattern) and pattern[p] == "*":
        p += 1
    return p == len(pattern)


# --- the SDK side: polling, writing events, the per-request hooks ---

class RateLimited(Exception):
    """Raised by ``check_rate_limit()`` when the request is over a limit or
    blocked. The integrations answer with ``status``, ``headers`` and
    ``body``."""

    def __init__(self, denial: Denial) -> None:
        super().__init__("rate limited by rule %s" % denial.rule_id)
        self.denial = denial
        self.status = denial.status
        self.headers = denial.headers
        self.body = denial.body
        self.rule_id = denial.rule_id
        self.retry_after = denial.retry_after


class RequestLimit:
    """One request's rate-limit state, kept on its Annotations."""

    __slots__ = ("limiter", "state", "req", "denial", "raised", "lock")

    def __init__(self, limiter: Limiter, state: State, req: Request) -> None:
        self.limiter, self.state, self.req = limiter, state, req
        self.denial: Optional[Denial] = None
        self.raised = False
        self.lock = threading.Lock()


class RateLimits:
    """Polls the rules every 15 s on a daemon thread and writes what was
    limited to rate_limit_events."""

    def __init__(self, backend: Any, logger: logging.Logger, service: str, instance: str,
                 redact_headers: List[str], poll_s: float = POLL_S) -> None:
        self._backend = backend
        self._logger = logger
        self._service = service
        self._instance = instance
        self._redact = list(DEFAULT_REDACT_HEADERS) + list(redact_headers)
        self._poll_s = poll_s
        self._init_state()

    def _init_state(self) -> None:
        self.limiter = Limiter(self._service)
        self.limiter.set_redact_headers(self._redact)
        self._version = ""
        self._bad_version = ""
        self._write_failed = False
        self._stop = threading.Event()
        self._thread: Optional[threading.Thread] = None

    def start(self) -> None:
        self._thread = threading.Thread(target=self._run, name="visionary-ratelimit", daemon=True)
        self._thread.start()

    def restart_after_fork(self) -> None:
        # Fresh locks and counters; the first poll reloads the rules.
        self._init_state()
        self.start()

    def close(self, timeout: float) -> None:
        """Stop polling and write what is still held."""
        self._stop.set()
        t = self._thread
        if t is not None and t.is_alive():
            t.join(timeout)
        else:
            self.flush()

    def _run(self) -> None:
        _spans.mark_internal()
        while True:
            self.poll()
            if self._stop.wait(self._poll_s):
                break
        self.flush()

    def poll(self) -> None:
        try:
            self._load()
        except Exception:
            self._logger.exception("visionary: reading rate limit rules failed")
        self.limiter.sweep(time.time())
        self.flush()

    def _load(self) -> None:
        try:
            version = self._backend.read_setting(SETTING_VERSION)
        except Exception:
            return  # keep the rules in force
        if not version:
            if self._version:
                self.limiter.clear()
            self._version = ""
            return
        if version == self._version:
            return
        try:
            raw = self._backend.read_setting(SETTING_CONFIG) or ""
        except Exception:
            return
        cfg = _EMPTY
        if raw:
            try:
                cfg = parse(raw)
            except ValueError as e:
                if self._bad_version != version:
                    self._bad_version = version
                    self._logger.warning("visionary: rate limit rules are not valid JSON; keeping the previous "
                                         "ones (%s)", e)
                return
        self.limiter.load(cfg)
        self._version = version

    def rows(self, events: List[Event]) -> List[Tuple[Any, ...]]:
        def n(v: str) -> Optional[str]:
            return v or None

        return [(format_ts(e.created_at), n(self._service), self._instance, e.rule_id, e.rule_name, e.outcome,
                 n(e.key_value), n(e.ip), n(e.country), n(e.method), n(e.path), n(e.user_id), e.hits)
                for e in events]

    def flush(self) -> None:
        events = self.limiter.drain()
        if not events:
            return
        try:
            self._backend.insert("ratelimit", self.rows(events))
        except Exception as e:
            if not self._write_failed:
                self._write_failed = True
                self._logger.info("visionary: could not record rate-limited requests (the Visionary server "
                                  "creates rate_limit_events): %s", e)

    def begin(self, ann: Any, method: str, path: str, remote_ip: str, header: Callable[[str], str],
              query_string: str = "", query: Optional[Callable[[str], str]] = None) -> Optional[Denial]:
        """The request phase, before the view runs; returns the answer when it
        denies. Never raises."""
        lim = self.limiter
        if ann is None or not lim.active():
            return None
        try:
            if query is None:
                query = _query_lookup(query_string)
            req = Request(method or "", path or "", remote_ip or "", header, query)
            st = lim.check_request(req, time.time())
            ann.rate_limit = RequestLimit(lim, st, req)
            return st.denial
        except Exception:
            return None


def _query_lookup(qs: str) -> Callable[[str], str]:
    parsed: List[Dict[str, List[str]]] = []

    def get(name: str) -> str:
        if not parsed:
            parsed.append(parse_qs(qs, keep_blank_values=True))
        v = parsed[0].get(name)
        return v[0] if v else ""

    return get


def check_rate_limit(attrs: Optional[Dict[str, Any]] = None) -> None:
    """Check the rate-limit rules that need the user (by user, role or an
    attribute such as the plan) once ``set_user`` has run, and raise
    ``RateLimited`` when one denies the request::

        visionary.set_user(user.id, role=user.role)
        visionary.check_rate_limit({"plan": user.plan})

    The Django, Flask and ASGI integrations turn the exception into the
    answer. Only the first call per request counts (later ones repeat its
    outcome); outside a request handled by an integration it does nothing.
    """
    ann = _context.current()
    rl: Optional[RequestLimit] = getattr(ann, "rate_limit", None) if ann is not None else None
    if rl is None:
        return
    with rl.lock:
        d = rl.denial
        if d is None:
            try:
                uid, _, role = ann.user()
                clean = {str(k): "" if v is None else str(v) for k, v in (attrs or {}).items()}
                d = rl.limiter.check_user(rl.state, rl.req, User(uid, role), clean, time.time())
            except Exception:
                d = None
            rl.denial = d
        if d is None:
            return
        rl.raised = True
    raise RateLimited(d)
