"""Database spans from DB-API connections and SQLAlchemy engines.

Django is instrumented by its integration (``execute_wrapper``). For other
code, wrap a connection (psycopg 3, psycopg2, PyMySQL, sqlite3)::

    conn = visionary.instrument_connection(psycopg.connect(DSN))

or attach to a SQLAlchemy engine (sync or async)::

    visionary.instrument_sqlalchemy(engine)
"""

from __future__ import annotations

import inspect
import os
from typing import Any, Optional

from . import _spans


def start_query(sql: Any, target: Optional[str]) -> Optional[_spans.Span]:
    """A started ``db`` span for one statement."""
    if _spans.active() is None:
        return None
    text = sql.decode("utf-8", "replace") if isinstance(sql, (bytes, bytearray)) else str(sql)
    return _spans.start("db", _spans.sql_name(text), target, _spans.sanitize_sql(text))


def row_count(cursor: Any) -> Optional[int]:
    try:
        n = cursor.rowcount
    except Exception:
        return None
    return n if isinstance(n, int) and n >= 0 else None


def target_of(system: str, database: Any) -> str:
    """``postgresql/app``; file databases by their file name."""
    db = str(database or "")
    if system == "sqlite" and db and db != ":memory:":
        db = os.path.basename(db)
    return "%s/%s" % (system, db) if db else system


def connection_target(conn: Any) -> str:
    """Best effort ``system/database`` for a DB-API connection."""
    module = type(conn).__module__.split(".", 1)[0]
    try:
        if module in ("psycopg", "psycopg2"):
            info = getattr(conn, "info", None)
            return target_of("postgresql", getattr(info, "dbname", "") if info is not None else "")
        if module == "pymysql":
            db = getattr(conn, "db", b"")
            return target_of("mysql", db.decode() if isinstance(db, bytes) else db)
        if module == "sqlite3":
            return "sqlite"
    except Exception:
        pass
    return module or "db"


# --- DB-API proxies ------------------------------------------------------------


class _Proxy:
    __slots__ = ("_inner", "_target")

    def __init__(self, inner: Any, target: str) -> None:
        object.__setattr__(self, "_inner", inner)
        object.__setattr__(self, "_target", target)

    def __getattr__(self, name: str) -> Any:
        return getattr(self._inner, name)

    def __setattr__(self, name: str, value: Any) -> None:
        setattr(self._inner, name, value)

    def __iter__(self) -> Any:
        return iter(self._inner)

    def __next__(self) -> Any:
        return next(self._inner)

    def __repr__(self) -> str:
        return "<visionary %r>" % (self._inner,)


class CursorProxy(_Proxy):
    """A cursor whose ``execute``/``executemany`` record spans."""

    __slots__ = ()

    def execute(self, sql: Any, *args: Any, **kwargs: Any) -> Any:
        return self._run(self._inner.execute, sql, args, kwargs)

    def executemany(self, sql: Any, *args: Any, **kwargs: Any) -> Any:
        return self._run(self._inner.executemany, sql, args, kwargs)

    def _run(self, fn: Any, sql: Any, args: Any, kwargs: Any) -> Any:
        span = start_query(sql, self._target)
        try:
            result = fn(sql, *args, **kwargs)
        except BaseException as e:
            if span is not None:
                span.finish(error=e)
            raise
        if span is not None:
            span.finish(row_count=row_count(self._inner))
        # Some drivers return the cursor itself (sqlite3, psycopg); keep the proxy.
        return self if result is self._inner else result

    def __enter__(self) -> "CursorProxy":
        self._inner.__enter__()
        return self

    def __exit__(self, *exc: Any) -> Any:
        return self._inner.__exit__(*exc)


class AsyncCursorProxy(_Proxy):
    """psycopg 3 ``AsyncCursor`` counterpart of CursorProxy."""

    __slots__ = ()

    async def execute(self, sql: Any, *args: Any, **kwargs: Any) -> Any:
        return await self._run(self._inner.execute, sql, args, kwargs)

    async def executemany(self, sql: Any, *args: Any, **kwargs: Any) -> Any:
        return await self._run(self._inner.executemany, sql, args, kwargs)

    async def _run(self, fn: Any, sql: Any, args: Any, kwargs: Any) -> Any:
        span = start_query(sql, self._target)
        try:
            result = await fn(sql, *args, **kwargs)
        except BaseException as e:
            if span is not None:
                span.finish(error=e)
            raise
        if span is not None:
            span.finish(row_count=row_count(self._inner))
        return self if result is self._inner else result

    def __aiter__(self) -> Any:
        return self._inner.__aiter__()

    async def __aenter__(self) -> "AsyncCursorProxy":
        await self._inner.__aenter__()
        return self

    async def __aexit__(self, *exc: Any) -> Any:
        return await self._inner.__aexit__(*exc)


class ConnectionProxy(_Proxy):
    """A connection whose cursors (and psycopg's ``conn.execute``) record spans."""

    __slots__ = ()

    def cursor(self, *args: Any, **kwargs: Any) -> Any:
        return instrument_cursor(self._inner.cursor(*args, **kwargs), self._target)

    def execute(self, sql: Any, *args: Any, **kwargs: Any) -> Any:
        # sqlite3 and psycopg 3 shortcut: a new cursor per call.
        cur = self.cursor()
        return cur.execute(sql, *args, **kwargs)

    def __enter__(self) -> "ConnectionProxy":
        self._inner.__enter__()
        return self

    def __exit__(self, *exc: Any) -> Any:
        return self._inner.__exit__(*exc)


class AsyncConnectionProxy(_Proxy):
    __slots__ = ()

    def cursor(self, *args: Any, **kwargs: Any) -> Any:
        return instrument_cursor(self._inner.cursor(*args, **kwargs), self._target)

    async def execute(self, sql: Any, *args: Any, **kwargs: Any) -> Any:
        cur = self.cursor()
        return await cur.execute(sql, *args, **kwargs)

    async def __aenter__(self) -> "AsyncConnectionProxy":
        await self._inner.__aenter__()
        return self

    async def __aexit__(self, *exc: Any) -> Any:
        return await self._inner.__aexit__(*exc)


def instrument_connection(conn: Any, target: Optional[str] = None) -> Any:
    """Wrap a DB-API connection (psycopg 3 sync or async, psycopg2, PyMySQL,
    sqlite3) so every statement run through its cursors becomes a ``db``
    span. ``target`` overrides the ``system/database`` label."""
    _spans.db_visible = True
    if isinstance(conn, (ConnectionProxy, AsyncConnectionProxy)):
        return conn
    t = target or connection_target(conn)
    if inspect.iscoroutinefunction(getattr(conn, "commit", None)):
        return AsyncConnectionProxy(conn, t)
    return ConnectionProxy(conn, t)


def instrument_cursor(cursor: Any, target: Optional[str] = None) -> Any:
    """Wrap one DB-API cursor; see instrument_connection."""
    _spans.db_visible = True
    if isinstance(cursor, (CursorProxy, AsyncCursorProxy)):
        return cursor
    t = target or connection_target(getattr(cursor, "connection", cursor))
    if inspect.iscoroutinefunction(getattr(cursor, "execute", None)):
        return AsyncCursorProxy(cursor, t)
    return CursorProxy(cursor, t)


# --- SQLAlchemy ------------------------------------------------------------------

_SA_KEY = "_visionary_span"


def instrument_sqlalchemy(engine: Any) -> Any:
    """Record every statement an SQLAlchemy ``Engine`` or ``AsyncEngine``
    runs as a ``db`` span. Returns the engine."""
    from sqlalchemy import event

    sync = getattr(engine, "sync_engine", engine)
    if getattr(sync, "_visionary", False):
        return engine
    sync._visionary = True
    _spans.db_visible = True
    url = sync.url
    system = {"postgres": "postgresql"}.get(sync.dialect.name, sync.dialect.name)
    target = target_of(system, url.database)

    @event.listens_for(sync, "before_cursor_execute")
    def _before(conn: Any, cursor: Any, statement: str, parameters: Any, context: Any, executemany: bool) -> None:
        span = start_query(statement, target)
        if span is not None and context is not None:
            setattr(context, _SA_KEY, span)

    @event.listens_for(sync, "after_cursor_execute")
    def _after(conn: Any, cursor: Any, statement: str, parameters: Any, context: Any, executemany: bool) -> None:
        span = getattr(context, _SA_KEY, None) if context is not None else None
        if span is not None:
            span.finish(row_count=row_count(cursor))

    @event.listens_for(sync, "handle_error")
    def _error(ctx: Any) -> None:
        context = ctx.execution_context
        span = getattr(context, _SA_KEY, None) if context is not None else None
        if span is not None:
            span.finish(error=ctx.original_exception)

    return engine
