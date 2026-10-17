"""Where rows go: D1 over REST, or a DB-API connection (SQLite, PostgreSQL, MySQL)."""

from __future__ import annotations

import threading
from importlib import resources
from typing import Any, Callable, Dict, List, Optional, Sequence, Tuple
from urllib.parse import unquote, urlsplit

from ._d1 import D1Client

REQUEST_COLUMNS = [
    "request_id", "created_at", "method", "path", "route", "query", "status", "duration_ms", "ip", "user_agent",
    "user_id", "user_handle", "user_role", "request_headers", "request_content_type", "request_body",
    "request_body_truncated", "request_size", "response_content_type", "response_body",
    "response_body_truncated", "response_size", "error_code", "error_key",
]

SYSTEM_COLUMNS = [
    "created_at", "instance", "interval_s",
    "host_cpu_percent", "load_1", "load_5", "load_15",
    "host_mem_used_mb", "host_mem_total_mb", "host_mem_percent",
    "disk_used_percent", "disk_free_gb", "net_rx_kbps", "net_tx_kbps",
    "proc_cpu_percent", "proc_rss_mb", "proc_threads", "proc_open_fds",
    "goroutines", "heap_alloc_mb", "heap_inuse_mb", "heap_objects",
    "gc_count", "gc_pause_ms", "gc_cpu_percent",
    "db_total_conns", "db_idle_conns", "db_acquired_conns", "db_max_conns",
    "db_acquires", "db_empty_acquires", "db_acquire_wait_ms",
    "http_requests", "http_errors", "http_rps", "http_in_flight",
    "uptime_s",
]

TABLES = {"requests": ("api_logs", REQUEST_COLUMNS), "system": ("system_metrics", SYSTEM_COLUMNS)}

# NOT NULL columns in api_logs; on D1 they are bound as-is, like the Go SDK.
_D1_REQUIRED = {
    "request_id", "created_at", "method", "path", "status", "duration_ms",
    "request_body_truncated", "request_size", "response_body_truncated", "response_size",
}

PURGE_CHUNK = 5000

# MySQL rejects over-long values in strict mode, which would fail the whole
# batch, so values are cut to the column size first. VARCHAR(n) counts
# characters; TEXT/MEDIUMTEXT count bytes.
_MYSQL_CHARS = {
    "request_id": 128, "created_at": 32, "method": 16, "route": 255, "ip": 64, "user_id": 128,
    "user_handle": 255, "user_role": 64, "request_content_type": 255, "response_content_type": 255,
    "error_code": 128, "error_key": 255, "instance": 128,
}
_MYSQL_BYTES = {
    "path": 65535, "query": 65535, "user_agent": 65535, "request_headers": 65535,
    "request_body": 16777215, "response_body": 16777215,
}


class StorageError(Exception):
    """Storage is misconfigured (unknown scheme, driver not installed)."""


def schema_statements(dialect: str, table: str) -> List[str]:
    folder = "sqlite" if dialect == "d1" else dialect
    name = "requests.sql" if table == "requests" else "system.sql"
    text = resources.files("visionary").joinpath("schema", folder, name).read_text(encoding="utf-8")
    return [s.strip() for s in text.split(";") if s.strip()]


def _purge_sql(dialect: str, table: str, ph: str) -> str:
    name = TABLES[table][0]
    if dialect == "mysql":
        # MySQL doesn't allow LIMIT inside an IN (subquery).
        return "DELETE FROM %s WHERE created_at < %s ORDER BY id LIMIT %d" % (name, ph, PURGE_CHUNK)
    return "DELETE FROM %s WHERE id IN (SELECT id FROM %s WHERE created_at < %s LIMIT %d)" % (
        name, name, ph, PURGE_CHUNK)


class Backend:
    dialect = ""

    def create_schema(self, table: str) -> None:
        raise NotImplementedError

    def insert(self, table: str, rows: List[Tuple[Any, ...]]) -> None:
        """Write rows (typed values, None for NULL) in one batch/transaction."""
        raise NotImplementedError

    def purge(self, table: str, cutoff: str) -> int:
        """Delete one chunk of rows older than cutoff; returns rows deleted."""
        raise NotImplementedError

    def close(self) -> None:
        pass

    def after_fork(self) -> None:
        pass


def _d1_param(v: Any) -> str:
    if v is None:
        return ""
    if isinstance(v, bool):
        return "1" if v else "0"
    if isinstance(v, float):
        text = repr(v)
        return format(v, ".6f") if "e" in text or "n" in text else text
    return str(v)


class D1Backend(Backend):
    dialect = "d1"

    def __init__(self, client: D1Client) -> None:
        self._client = client
        self._sql: Dict[str, str] = {}
        for table, (name, cols) in TABLES.items():
            ph = ["?" if table == "requests" and c in _D1_REQUIRED else "NULLIF(?, '')" for c in cols]
            self._sql[table] = "INSERT INTO %s (%s) VALUES (%s)" % (name, ", ".join(cols), ", ".join(ph))

    def create_schema(self, table: str) -> None:
        self._client.batch([(s, []) for s in schema_statements("d1", table)])

    def insert(self, table: str, rows: List[Tuple[Any, ...]]) -> None:
        if rows:
            sql = self._sql[table]
            self._client.batch([(sql, [_d1_param(v) for v in row]) for row in rows])

    def purge(self, table: str, cutoff: str) -> int:
        return self._client.changes(_purge_sql("d1", table, "?"), [cutoff])


class SQLBackend(Backend):
    """A DB-API connection obtained from ``connect``.

    PostgreSQL and MySQL share one connection behind a lock. SQLite gets one
    connection per worker thread, because sqlite3 connections refuse
    cross-thread use by default and are cheap to open.
    """

    def __init__(self, dialect: str, connect: Callable[[], Any]) -> None:
        if dialect not in ("sqlite", "postgres", "mysql"):
            raise StorageError("visionary: unknown dialect %r (want sqlite, postgres or mysql)" % dialect)
        self.dialect = dialect
        self._connect = connect
        self._ph = "?" if dialect == "sqlite" else "%s"
        self._sql = {
            table: "INSERT INTO %s (%s) VALUES (%s)" % (name, ", ".join(cols), ", ".join([self._ph] * len(cols)))
            for table, (name, cols) in TABLES.items()
        }
        self._per_thread = dialect == "sqlite"
        self._reset()

    def _reset(self) -> None:
        self._lock = threading.RLock()  # serializes use of the shared connection
        self._conns_lock = threading.Lock()  # guards _all only, never held during I/O
        self._local = threading.local()
        self._shared: Optional[Any] = None
        self._all: List[Any] = []

    def after_fork(self) -> None:
        # The parent's connections (sockets) must not be touched, or closed,
        # from the child; forget them and reconnect lazily.
        self._reset()

    def _get(self) -> Any:
        conn = getattr(self._local, "conn", None) if self._per_thread else self._shared
        if conn is not None and _is_closed(conn):
            self._discard(conn)
            conn = None
        if conn is None:
            conn = self._connect()
            if self._per_thread:
                self._local.conn = conn
            else:
                self._shared = conn
            with self._conns_lock:
                self._all.append(conn)
        return conn

    def _discard(self, conn: Any) -> None:
        if self._per_thread:
            self._local.conn = None
        elif self._shared is conn:
            self._shared = None
        with self._conns_lock:
            if conn in self._all:
                self._all.remove(conn)
        try:
            conn.close()
        except Exception:
            pass

    def _run(self, fn: Callable[[Any], Any]) -> Any:
        lock = _NullLock() if self._per_thread else self._lock
        with lock:
            conn = self._get()
            try:
                result = fn(conn)
                conn.commit()
                return result
            except Exception:
                try:
                    conn.rollback()
                except Exception:
                    self._discard(conn)
                raise

    def create_schema(self, table: str) -> None:
        for stmt in schema_statements(self.dialect, table):
            try:
                self._run(lambda c, s=stmt: _execute(c, s))
            except Exception as e:
                if self.dialect == "mysql" and _mysql_errno(e) == 1061:
                    continue  # index already exists; MySQL has no IF NOT EXISTS here
                raise

    def insert(self, table: str, rows: List[Tuple[Any, ...]]) -> None:
        if not rows:
            return
        cols = TABLES[table][1]
        rows = [self._fit(cols, row) for row in rows]
        sql = self._sql[table]

        def write(conn: Any) -> None:
            cur = conn.cursor()
            try:
                cur.executemany(sql, rows)
            finally:
                cur.close()

        self._run(write)

    def purge(self, table: str, cutoff: str) -> int:
        sql = _purge_sql(self.dialect, table, self._ph)

        def delete(conn: Any) -> int:
            cur = conn.cursor()
            try:
                cur.execute(sql, (cutoff,))
                return max(cur.rowcount or 0, 0)
            finally:
                cur.close()

        return self._run(delete)

    def _fit(self, cols: Sequence[str], row: Tuple[Any, ...]) -> Tuple[Any, ...]:
        if self.dialect == "postgres":
            # PostgreSQL text can't hold NUL bytes; one would fail the batch.
            return tuple(v.replace("\x00", "") if isinstance(v, str) and "\x00" in v else v for v in row)
        if self.dialect == "mysql":
            out = []
            for c, v in zip(cols, row):
                if isinstance(v, str):
                    if c in _MYSQL_CHARS:
                        v = v[:_MYSQL_CHARS[c]]
                    elif c in _MYSQL_BYTES:
                        v = _fit_bytes(v, _MYSQL_BYTES[c])
                out.append(v)
            return tuple(out)
        return row

    def close(self) -> None:
        with self._conns_lock:
            conns, self._all = self._all, []
        self._shared = None
        for conn in conns:
            try:
                conn.close()
            except Exception:
                pass


class _NullLock:
    def __enter__(self) -> None:
        return None

    def __exit__(self, *exc: Any) -> None:
        return None


def _execute(conn: Any, sql: str) -> None:
    cur = conn.cursor()
    try:
        cur.execute(sql)
    finally:
        cur.close()


def _is_closed(conn: Any) -> bool:
    closed = getattr(conn, "closed", False)  # psycopg
    if closed:
        return True
    return getattr(conn, "open", True) is False  # PyMySQL


def _mysql_errno(e: Exception) -> Optional[int]:
    args = getattr(e, "args", ())
    if args and isinstance(args[0], int):
        return args[0]
    return None


def _fit_bytes(s: str, limit: int) -> str:
    b = s.encode("utf-8")
    if len(b) <= limit:
        return s
    return b[:limit].decode("utf-8", errors="ignore")


def open_backend(cfg: Any) -> Optional[Backend]:
    """The backend the config asks for, or None when no database is configured."""
    if cfg.connection_factory is not None:
        return SQLBackend(_normalize_dialect(cfg.dialect or "sqlite"), cfg.connection_factory)

    url = (cfg.database_url or "").strip()
    if not url:
        if cfg.d1_account_id and cfg.d1_database_id and cfg.d1_api_token:
            return D1Backend(D1Client(cfg.d1_account_id, cfg.d1_database_id, cfg.d1_api_token, cfg.d1_api_base))
        return None

    scheme, sep, rest = url.partition("://")
    if not sep:
        raise StorageError("visionary: VISIONARY_DATABASE_URL needs a scheme (postgres://, mysql://, sqlite://, d1://)")
    dialect = _normalize_dialect(scheme.split("+", 1)[0])
    if dialect == "d1":
        return _d1_from_url(cfg, rest)
    if dialect == "sqlite":
        return SQLBackend("sqlite", _sqlite_connect(_sqlite_path(rest)))
    if dialect == "postgres":
        try:
            import psycopg
        except ImportError:
            raise StorageError("visionary: PostgreSQL needs psycopg: pip install 'visionary-sdk[postgres]'") from None
        pg_url = "postgresql://" + rest
        return SQLBackend("postgres", lambda: psycopg.connect(pg_url, connect_timeout=10))
    if dialect == "mysql":
        try:
            import pymysql
        except ImportError:
            raise StorageError("visionary: MySQL needs PyMySQL: pip install 'visionary-sdk[mysql]'") from None
        kwargs = _mysql_kwargs("mysql://" + rest)
        return SQLBackend("mysql", lambda: pymysql.connect(**kwargs))
    raise StorageError("visionary: unsupported database scheme %r" % scheme)


def _normalize_dialect(name: str) -> str:
    n = name.strip().lower()
    return {"postgresql": "postgres", "pg": "postgres", "mariadb": "mysql", "sqlite3": "sqlite"}.get(n, n)


def _d1_from_url(cfg: Any, rest: str) -> D1Backend:
    # d1:// uses the D1_* variables; d1://ACCOUNT/DATABASE overrides the ids.
    account, _, database = rest.strip("/").partition("/")
    account = account or cfg.d1_account_id
    database = database or cfg.d1_database_id
    if not (account and database and cfg.d1_api_token):
        raise StorageError("visionary: d1:// needs D1_ACCOUNT_ID, D1_DATABASE_ID and D1_API_TOKEN")
    return D1Backend(D1Client(account, database, cfg.d1_api_token, cfg.d1_api_base))


def _sqlite_path(rest: str) -> str:
    path = unquote(rest.split("?", 1)[0])
    # sqlite:///C:/data/x.db -> C:/data/x.db on Windows
    if len(path) > 2 and path[0] == "/" and path[2] == ":" and path[1].isalpha():
        path = path[1:]
    return path


def _sqlite_connect(path: str) -> Callable[[], Any]:
    import sqlite3

    return lambda: sqlite3.connect(path, timeout=10)


def _mysql_kwargs(url: str) -> Dict[str, Any]:
    u = urlsplit(url)
    return {
        "host": u.hostname or "localhost",
        "port": u.port or 3306,
        "user": unquote(u.username or ""),
        "password": unquote(u.password or ""),
        "database": unquote(u.path.lstrip("/")) or None,
        "charset": "utf8mb4",
        "autocommit": False,
        "connect_timeout": 10,
    }
