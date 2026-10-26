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

# api_logs columns added after the first release, in insert order after the
# ones above (Go: AddedRequestColumns).
ADDED_REQUEST_COLUMNS = [
    ("service", "text"), ("version", "text"),
    ("db_count", "int"), ("db_ms", "real"), ("http_count", "int"), ("http_ms", "real"),
    ("login_account", "text"),
]
REQUEST_COLUMNS = REQUEST_COLUMNS + [name for name, _ in ADDED_REQUEST_COLUMNS]

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

# Columns added after the first release, in insert order after the ones
# above (Go: AddedSystemColumns). Older tables gain them at startup.
ADDED_SYSTEM_COLUMNS = [
    ("cpu_count", "int"), ("cpu_user_percent", "real"), ("cpu_system_percent", "real"),
    ("cpu_iowait_percent", "real"), ("cpu_steal_percent", "real"),
    ("host_mem_available_mb", "real"), ("host_mem_cached_mb", "real"),
    ("swap_used_mb", "real"), ("swap_total_mb", "real"),
    ("disk_total_gb", "real"), ("disk_inodes_percent", "real"),
    ("disk_read_kbps", "real"), ("disk_write_kbps", "real"), ("disk_read_iops", "real"),
    ("disk_write_iops", "real"), ("disk_busy_percent", "real"), ("disk_await_ms", "real"),
    ("net_rx_pps", "real"), ("net_tx_pps", "real"), ("net_errors", "int"), ("net_drops", "int"),
    ("tcp_established", "int"), ("tcp_retrans", "int"),
    ("host_procs", "int"), ("procs_running", "int"), ("procs_blocked", "int"),
    ("ctx_switches_ps", "real"), ("proc_read_kbps", "real"), ("proc_write_kbps", "real"),
    ("proc_max_fds", "int"), ("service", "text"), ("version", "text"),
    ("sdk", "text"), ("runtime", "text"),
    ("container_cpu_cores", "real"), ("container_cpu_limit", "real"),
    ("container_cpu_throttled_percent", "real"), ("container_mem_used_mb", "real"),
    ("container_mem_limit_mb", "real"), ("container_oom_kills", "int"),
]
SYSTEM_COLUMNS = SYSTEM_COLUMNS + [name for name, _ in ADDED_SYSTEM_COLUMNS]

SPAN_COLUMNS = [
    "created_at", "request_id", "service", "instance", "kind", "name", "target", "detail",
    "offset_ms", "duration_ms", "status", "error", "row_count",
]
ERROR_COLUMNS = [
    "created_at", "service", "instance", "version", "request_id", "method", "route", "path", "user_id",
    "type", "message", "stack", "fingerprint", "handled", "runtime",
]
RELEASE_COLUMNS = ["created_at", "service", "instance", "version", "sdk", "sdk_version", "runtime"]
# CPU profiles; the Visionary server creates the table, never the SDK.
PROFILE_COLUMNS = ["created_at", "service", "instance", "version", "sdk", "duration_ms", "samples", "routes", "folded"]
SETTINGS_SQL = "SELECT name, value FROM visionary_settings WHERE name LIKE 'profile.%'"

TABLES = {
    "requests": ("api_logs", REQUEST_COLUMNS),
    "system": ("system_metrics", SYSTEM_COLUMNS),
    "spans": ("spans", SPAN_COLUMNS),
    "errors": ("errors", ERROR_COLUMNS),
    "releases": ("releases", RELEASE_COLUMNS),
    "profiles": ("profiles", PROFILE_COLUMNS),
}

# Schema file per group, and the columns older tables of that group may lack.
_SCHEMA_FILES = {"requests": "requests.sql", "system": "system.sql", "telemetry": "telemetry.sql"}
_MIGRATIONS = {"requests": ("api_logs", ADDED_REQUEST_COLUMNS), "system": ("system_metrics", ADDED_SYSTEM_COLUMNS)}

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
    "service": 255, "version": 255, "kind": 255, "name": 255, "target": 255, "type": 255,
    "fingerprint": 255, "runtime": 255, "sdk": 255, "sdk_version": 255, "login_account": 255,
}
_MYSQL_BYTES = {
    "path": 65535, "query": 65535, "user_agent": 65535, "request_headers": 65535,
    "request_body": 16777215, "response_body": 16777215,
    "detail": 65535, "error": 65535, "message": 65535, "stack": 16777215,
}


class StorageError(Exception):
    """Storage is misconfigured (unknown scheme, driver not installed)."""


def schema_statements(dialect: str, table: str) -> List[str]:
    folder = "sqlite" if dialect == "d1" else dialect
    name = _SCHEMA_FILES[table]
    text = resources.files("visionary").joinpath("schema", folder, name).read_text(encoding="utf-8")
    return [s.strip() for s in text.split(";") if s.strip()]


def _columns_query(dialect: str, table: str) -> str:
    if dialect == "postgres":
        return ("SELECT column_name FROM information_schema.columns "
                "WHERE table_schema = current_schema() AND table_name = '%s'" % table)
    if dialect == "mysql":
        return ("SELECT column_name FROM information_schema.columns "
                "WHERE table_schema = DATABASE() AND table_name = '%s'" % table)
    return "SELECT name FROM pragma_table_info('%s')" % table


def _sql_type(dialect: str, kind: str) -> str:
    if kind == "text":
        return "VARCHAR(255)" if dialect == "mysql" else "TEXT"
    if kind == "int":
        return "INTEGER" if dialect in ("sqlite", "d1") else "BIGINT"
    return {"postgres": "DOUBLE PRECISION", "mysql": "DOUBLE"}.get(dialect, "REAL")


def missing_column_statements(dialect: str, table: str, added: Sequence[Tuple[str, str]],
                              existing: Sequence[str]) -> List[str]:
    have = {str(c).lower() for c in existing}
    return ["ALTER TABLE %s ADD COLUMN %s %s" % (table, name, _sql_type(dialect, kind))
            for name, kind in added if name not in have]


def is_duplicate_column(e: Exception) -> bool:
    """An ALTER that lost a race with another instance."""
    msg = str(e).lower()
    return "duplicate column" in msg or "already exists" in msg or "1060" in msg


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
        """Create a schema group ("requests", "system" or "telemetry"): its
        tables and indexes, then the columns an older table is missing."""
        raise NotImplementedError

    def _column_names(self, query: str) -> List[str]:
        raise NotImplementedError

    def _alter(self, sql: str) -> None:
        raise NotImplementedError

    def _migrate(self, group: str) -> None:
        if group not in _MIGRATIONS:
            return
        table, added = _MIGRATIONS[group]
        existing = self._column_names(_columns_query(self.dialect, table))
        for stmt in missing_column_statements(self.dialect, table, added, existing):
            try:
                self._alter(stmt)
            except Exception as e:
                if not is_duplicate_column(e):
                    raise

    def insert(self, table: str, rows: List[Tuple[Any, ...]]) -> None:
        """Write rows (typed values, None for NULL) in one batch/transaction."""
        self.insert_many([(table, rows)])

    def insert_many(self, groups: List[Tuple[str, List[Tuple[Any, ...]]]]) -> None:
        """Write rows to several tables in one batch/transaction."""
        raise NotImplementedError

    def purge(self, table: str, cutoff: str) -> int:
        """Delete one chunk of rows older than cutoff; returns rows deleted."""
        raise NotImplementedError

    def read_settings(self) -> List[Tuple[str, str]]:
        """The dashboard's profile.* settings as (name, value) rows. Raises
        when visionary_settings doesn't exist yet."""
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
        self._migrate(table)

    def _column_names(self, query: str) -> List[str]:
        return [str(row[0]) for row in self._client.rows(query) if row]

    def _alter(self, sql: str) -> None:
        self._client.query(sql)

    def insert_many(self, groups: List[Tuple[str, List[Tuple[Any, ...]]]]) -> None:
        stmts = [(self._sql[table], [_d1_param(v) for v in row]) for table, rows in groups for row in rows]
        if stmts:
            self._client.batch(stmts)

    def purge(self, table: str, cutoff: str) -> int:
        return self._client.changes(_purge_sql("d1", table, "?"), [cutoff])

    def read_settings(self) -> List[Tuple[str, str]]:
        return [(str(r[0]), "" if r[1] is None else str(r[1])) for r in self._client.rows(SETTINGS_SQL) if len(r) >= 2]


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
        self._migrate(table)

    def _column_names(self, query: str) -> List[str]:
        def read(conn: Any) -> List[str]:
            cur = conn.cursor()
            try:
                cur.execute(query)
                return [str(r[0]) for r in cur.fetchall()]
            finally:
                cur.close()

        return self._run(read)

    def _alter(self, sql: str) -> None:
        self._run(lambda c: _execute(c, sql))

    def insert_many(self, groups: List[Tuple[str, List[Tuple[Any, ...]]]]) -> None:
        work = [(self._sql[table], [self._fit(TABLES[table][1], row) for row in rows])
                for table, rows in groups if rows]
        if not work:
            return

        def write(conn: Any) -> None:
            cur = conn.cursor()
            try:
                for sql, rows in work:
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

    def read_settings(self) -> List[Tuple[str, str]]:
        def read(conn: Any) -> List[Tuple[str, str]]:
            cur = conn.cursor()
            try:
                cur.execute(SETTINGS_SQL)  # no parameters: "%" stays literal for every driver
                return [(str(r[0]), "" if r[1] is None else str(r[1])) for r in cur.fetchall()]
            finally:
                cur.close()

        return self._run(read)

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
