from __future__ import annotations

import os
import re
from dataclasses import dataclass, field
from typing import Any, Callable, Dict, List, Optional, Tuple

LookupUser = Callable[[str], Optional[Tuple[Optional[str], Optional[str]]]]
DBStatsFunc = Callable[[], Dict[str, Any]]


@dataclass
class Config:
    """Everything the agent needs. ``from_env()`` fills it from ``VISIONARY_*``.

    Storage is chosen in this order: ``connection_factory`` + ``dialect``,
    then ``database_url``, then the ``d1_*`` credentials.
    """

    database_url: str = ""
    d1_account_id: str = ""
    d1_database_id: str = ""
    d1_api_token: str = ""
    d1_api_base: str = ""
    # A zero-argument callable returning a DB-API connection the SDK may keep
    # for itself (it reconnects through it after errors and closes it at
    # shutdown). ``dialect`` is "sqlite", "postgres" or "mysql".
    connection_factory: Optional[Callable[[], Any]] = None
    dialect: str = ""

    instance: str = ""

    log_requests: bool = True
    path_prefix: str = ""
    capture_bodies: bool = True
    max_body_bytes: int = 8192
    queue_size: int = 5000
    batch_size: int = 50
    flush_interval: float = 3.0
    log_retention_days: int = 30
    redact_headers: List[str] = field(default_factory=list)
    redact_fields: List[str] = field(default_factory=list)
    redact_request_fields: List[str] = field(default_factory=list)
    error_code_field: str = "error.code"
    error_key_field: str = "error.key"
    # Runs on the writer thread, once per distinct user per batch, for users
    # missing a handle or role. Return (handle, role) or None.
    lookup_user: Optional[LookupUser] = None

    system_metrics: bool = True
    system_interval: float = 15.0
    system_retention_days: int = 30
    # Returns pool stats: total, idle, in_use, max, acquires, empty_acquires,
    # acquire_wait_ms (the last three cumulative).
    db_stats: Optional[DBStatsFunc] = None
    # Filesystem reported in disk_* columns (default: the working directory).
    disk_path: str = ""

    # Telemetry (SPEC "Telemetry"). service and version label every row;
    # version defaults to the platform's commit variable (see from_env).
    service: str = ""
    version: str = ""
    spans: bool = True
    span_threshold_ms: float = 100.0
    span_max: int = 100
    errors: bool = True
    # Patch requests, httpx and urllib3 at start (same as instrument_http()).
    instrument_http: bool = False


def from_env(environ: Optional[Dict[str, str]] = None) -> Config:
    """Build a Config from environment variables (see SPEC.md "Environment")."""
    env = os.environ if environ is None else environ

    def s(key: str, default: str = "") -> str:
        v = env.get(key, "").strip()
        return v or default

    return Config(
        database_url=s("VISIONARY_DATABASE_URL"),
        d1_account_id=s("D1_ACCOUNT_ID"),
        d1_database_id=s("D1_DATABASE_ID"),
        d1_api_token=s("D1_API_TOKEN"),
        d1_api_base=s("D1_API_BASE"),
        instance=s("VISIONARY_INSTANCE"),
        log_requests=parse_bool(s("VISIONARY_LOG_REQUESTS"), True),
        path_prefix=s("VISIONARY_LOG_PATH_PREFIX"),
        capture_bodies=parse_bool(s("VISIONARY_LOG_BODIES"), True),
        max_body_bytes=parse_int(s("VISIONARY_LOG_MAX_BODY_BYTES"), 8192),
        log_retention_days=parse_int(s("VISIONARY_LOG_RETENTION_DAYS"), 30),
        redact_headers=parse_list(s("VISIONARY_LOG_REDACT_HEADERS")),
        redact_fields=parse_list(s("VISIONARY_LOG_REDACT_FIELDS")),
        redact_request_fields=parse_list(s("VISIONARY_LOG_REDACT_REQUEST_FIELDS")),
        error_code_field=s("VISIONARY_LOG_ERROR_CODE_FIELD", "error.code"),
        error_key_field=s("VISIONARY_LOG_ERROR_KEY_FIELD", "error.key"),
        system_metrics=parse_bool(s("VISIONARY_SYSTEM_METRICS"), True),
        system_interval=parse_duration(s("VISIONARY_SYSTEM_INTERVAL"), 15.0),
        system_retention_days=parse_int(s("VISIONARY_SYSTEM_RETENTION_DAYS"), 30),
        service=s("VISIONARY_SERVICE"),
        version=release_from_env(env),
        spans=parse_bool(s("VISIONARY_SPANS"), True),
        span_threshold_ms=float(parse_int(s("VISIONARY_SPAN_THRESHOLD_MS"), 100)),
        span_max=parse_int(s("VISIONARY_SPAN_MAX"), 100),
        errors=parse_bool(s("VISIONARY_ERRORS"), True),
        instrument_http=parse_bool(s("VISIONARY_INSTRUMENT_HTTP"), False),
    )


# Commit variables set by common platforms, in the order SPEC.md lists them.
_COMMIT_VARS = (
    "RAILWAY_GIT_COMMIT_SHA", "RENDER_GIT_COMMIT", "VERCEL_GIT_COMMIT_SHA", "HEROKU_SLUG_COMMIT",
    "SOURCE_VERSION", "GIT_COMMIT", "GITHUB_SHA", "K_REVISION",
)
_COMMIT_HASH = re.compile(r"^[0-9a-fA-F]{13,64}$")


def release_from_env(env: Dict[str, str]) -> str:
    """VISIONARY_RELEASE, else the first platform commit variable set; commit
    hashes are shortened to 12 characters."""
    for key in ("VISIONARY_RELEASE",) + _COMMIT_VARS:
        v = env.get(key, "").strip()
        if v:
            return v[:12] if _COMMIT_HASH.match(v) else v
    return ""


_TRUE = {"true", "1", "yes"}
_FALSE = {"false", "0", "no"}


def parse_bool(value: str, default: bool) -> bool:
    v = value.strip().lower()
    if v in _TRUE:
        return True
    if v in _FALSE:
        return False
    return default


def parse_int(value: str, default: int) -> int:
    try:
        n = int(value.strip())
    except ValueError:
        return default
    return n if n >= 0 else default


def parse_list(value: str) -> List[str]:
    return [p.strip() for p in value.split(",") if p.strip()]


_DURATION_PART = re.compile(r"(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)")
_UNITS = {"ns": 1e-9, "us": 1e-6, "µs": 1e-6, "ms": 1e-3, "s": 1.0, "m": 60.0, "h": 3600.0}


def parse_duration(value: str, default: float) -> float:
    """Seconds from "15", "15s", "500ms", "1m30s" (Go duration syntax)."""
    v = value.strip()
    if not v:
        return default
    try:
        return float(int(v))
    except ValueError:
        pass
    pos, total = 0, 0.0
    for m in _DURATION_PART.finditer(v):
        if m.start() != pos:
            return default
        total += float(m.group(1)) * _UNITS[m.group(2)]
        pos = m.end()
    if pos != len(v) or pos == 0:
        return default
    return total
