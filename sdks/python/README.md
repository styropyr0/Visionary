# visionary-sdk (Python)

The Python collector for [Visionary](../../Readme.md). It runs inside your web
service and writes:

- one row per HTTP request into `api_logs` (method, route pattern, status,
  timing, user, redacted headers and bodies, error codes), and
- a periodic host / process / runtime sample into `system_metrics`.

The Visionary server reads these tables for its dashboards. All slow work
happens on background threads, so requests are never blocked or slowed. If
the database is down, the SDK only writes log lines.

Every Visionary SDK behaves the same way. The contract is
[`sdks/SPEC.md`](../SPEC.md).

## Install

```sh
pip install visionary-sdk                 # core: stdlib only (D1, SQLite)
pip install "visionary-sdk[system]"       # + psutil: host CPU/memory/network, process CPU/RSS/threads/fds
pip install "visionary-sdk[postgres]"     # + psycopg 3
pip install "visionary-sdk[mysql]"        # + PyMySQL
```

Requires Python 3.9+. Without the `system` extra, the host and process
columns are NULL. Disk usage, thread count, GC and HTTP throughput are still
recorded.

## Quickstart

First configure a database (see [Choosing the database](#choosing-the-database)). For example:

```sh
export VISIONARY_DATABASE_URL=postgres://user:pass@localhost:5432/app
```

### Django

```python
# settings.py
MIDDLEWARE = [
    "visionary.integrations.django.VisionaryMiddleware",  # first, so it sees the final response
    # ...
]
```

The middleware calls `visionary.init()` the first time it loads, so nothing
else is needed. It works under WSGI and ASGI, with sync and async views.

- **Route:** `request.resolver_match.route` with a leading `/`, e.g. `/users/<int:id>/`.
- **User:** if you don't call `set_user`, an authenticated `request.user` is
  used (`pk` as the id, `get_username()` as the handle).

### FastAPI / Starlette (any ASGI app)

```python
from fastapi import FastAPI
from visionary.integrations.asgi import VisionaryASGIMiddleware

app = FastAPI()
app.add_middleware(VisionaryASGIMiddleware)
# or, for any ASGI app: app = VisionaryASGIMiddleware(app)
```

The route is the matched route's path (`/users/{user_id}`). Streaming
responses keep streaming. Only the first `VISIONARY_LOG_MAX_BODY_BYTES` of a
body are copied.

### Flask

```python
from flask import Flask
from visionary.integrations.flask import Visionary

app = Flask(__name__)
Visionary(app)            # or: ext = Visionary(); ext.init_app(app)
```

The route is `request.url_rule.rule` (`/users/<int:user_id>`).

### Anything else

```python
import visionary

agent = visionary.init()   # reads the environment, starts, flushes at exit
```

`init()` never raises. If no database is configured, it logs one warning and
returns a no-op agent, and the integrations pass requests straight through.

For explicit control (tests, several agents, custom config):

```python
cfg = visionary.from_env()          # or visionary.Config(...)
cfg.path_prefix = "/api"
agent = visionary.Agent(cfg).start()
app.add_middleware(VisionaryASGIMiddleware, agent=agent)
...
agent.close(timeout=10)             # drains the queue and flushes samples
```

## Choosing the database

The SDK checks these in order:

1. **Your own connection.** Use this when the app already has a driver:

   ```python
   import psycopg, visionary

   cfg = visionary.from_env()
   cfg.connection_factory = lambda: psycopg.connect(DSN)
   cfg.dialect = "postgres"            # "postgres", "mysql" or "sqlite"
   visionary.init(cfg)
   ```

   The factory must return a new DB-API connection. Visionary keeps that
   connection for its own use:
   - PostgreSQL and MySQL: one shared connection.
   - SQLite: one connection per worker thread.

   After an error it reconnects through the factory, and it closes the
   connection at shutdown. Each batch runs as one transaction. If you hand it
   an autocommit connection, batches are not atomic.

2. **`VISIONARY_DATABASE_URL`**, chosen by scheme:

   | URL | Backend |
   |---|---|
   | `postgres://user:pass@host:5432/db` (or `postgresql://`) | PostgreSQL via psycopg 3 |
   | `mysql://user:pass@host:3306/db` | MySQL 8+ / MariaDB via PyMySQL |
   | `sqlite:///absolute/path.db`, `sqlite:///C:/data/x.db`, `sqlite://relative.db` | SQLite via `sqlite3` |
   | `d1://` | Cloudflare D1, credentials from `D1_*` |

3. **Cloudflare D1**, when `D1_ACCOUNT_ID`, `D1_DATABASE_ID` and
   `D1_API_TOKEN` are set. `D1_API_BASE` overrides the API URL.

Tables and indexes are created at startup from the schema files shipped in
the package (`visionary/schema/<dialect>/`). This happens on the background
threads, so a slow database never delays boot. If the database is down at
startup, creation is retried before each write.

## Configuration

Every setting has an environment variable, and `visionary.Config` has the
same settings as attributes. The full list and semantics are in
[SPEC.md, "Environment"](../SPEC.md#environment).

| Variable | Default | Notes |
|---|---|---|
| `VISIONARY_DATABASE_URL` | | see above |
| `D1_ACCOUNT_ID`, `D1_DATABASE_ID`, `D1_API_TOKEN`, `D1_API_BASE` | | D1 only |
| `VISIONARY_INSTANCE` | hostname | labels `system_metrics` rows |
| `VISIONARY_LOG_REQUESTS` | `true` | request logging on/off |
| `VISIONARY_LOG_PATH_PREFIX` | empty | only log paths starting with this |
| `VISIONARY_LOG_BODIES` | `true` | capture request/response bodies |
| `VISIONARY_LOG_MAX_BODY_BYTES` | `8192` | per body |
| `VISIONARY_LOG_RETENTION_DAYS` | `30` | `0` keeps forever |
| `VISIONARY_LOG_REDACT_HEADERS`, `_REDACT_FIELDS`, `_REDACT_REQUEST_FIELDS` | empty | comma lists added to the defaults |
| `VISIONARY_LOG_ERROR_CODE_FIELD`, `_ERROR_KEY_FIELD` | `error.code`, `error.key` | dotted JSON paths |
| `VISIONARY_SYSTEM_METRICS` | `true` | sampler on/off |
| `VISIONARY_SYSTEM_INTERVAL` | `15s` | `15`, `500ms`, `1m` |
| `VISIONARY_SYSTEM_RETENTION_DAYS` | `30` | |

Booleans accept `true/false/1/0/yes/no`.

## Users

```python
import visionary

visionary.set_user(user.id, handle=user.email, role="admin")
```

Call `set_user` from auth middleware or a view once the user is known. Only
`id` is required. It is contextvar-based, so it works from sync views, thread
pools (FastAPI sync endpoints) and asyncio tasks. Outside a request handled
by an integration, it does nothing.

`visionary.set_route("/custom/{pattern}")` works the same way for the route,
for routers the integration can't see.

## Hooks

```python
cfg = visionary.from_env()

def lookup_user(user_id):
    """Fill in handle/role for ids logged without them. Runs on the writer
    thread, once per distinct user per batch; keep it cheap (cache)."""
    u = USER_CACHE.get(user_id)
    return (u.email, u.role) if u else None

def db_stats():
    """Your pool's stats for the db_* columns. Counters are cumulative;
    Visionary stores per-sample deltas."""
    s = pool.get_stats()                     # e.g. psycopg_pool
    return {
        "total": s["pool_size"], "idle": s["pool_available"],
        "in_use": s["pool_size"] - s["pool_available"], "max": s["pool_max"],
        "acquires": s.get("requests_num", 0), "empty_acquires": s.get("requests_queued", 0),
        "acquire_wait_ms": s.get("requests_wait_ms", 0),
    }

cfg.lookup_user = lookup_user
cfg.db_stats = db_stats
visionary.init(cfg)
```

If `db_stats()` leaves out a key, that column is stored as NULL.

## What is captured

Each request (`api_logs`) records:

- **Request id:** the incoming `X-Request-ID` if it is at most 128 characters,
  otherwise a random 32-hex id. It is echoed on the response.
- **Timing:** start time (`2026-10-17T17:34:00.123Z`) and duration in ms.
- **Request line:** method, path, route pattern and query string.
- **Status:** the final status (500 when the handler raised).
- **Client:** IP (first `X-Forwarded-For` hop, otherwise the peer address) and user agent.
- **User:** id, handle and role.
- **Headers:** all request headers as JSON.
- **Bodies:** content types, bodies up to the cap with `*_truncated` flags,
  and full body sizes.
- **Errors:** for `status >= 400`, the error code and key from the JSON response body.

Bodies are not captured for `multipart/*`, `image/*`, `video/*`, `audio/*`,
`application/octet-stream`, `application/zip` and `application/pdf`.

Streaming responses are counted, and their first bytes are copied without
buffering. File responses (`FileResponse`, Flask `send_file`) keep their
sendfile path, so only their `Content-Length` is recorded.

Each sample (`system_metrics`) records:

- **Host:** CPU, load average (Unix only), memory, disk usage of the working
  directory, and network rates.
- **Process:** CPU, RSS, threads and open files.
- **Runtime:** live Python threads (in the `goroutines` column), the
  `tracemalloc` heap when tracing is on, and GC collections and pause time
  (via `gc.callbacks`).
- **Database pool:** whatever your `db_stats` hook returns.
- **HTTP:** requests, 5xx responses, requests per second and in-flight
  requests seen by the integrations. These are counted even when request
  logging is off.

Sources that don't exist on the platform are NULL.

## Redaction

Redaction runs on the writer thread before anything is stored, and all
matching is case-insensitive.

- **Headers:** `Authorization`, `Proxy-Authorization`, `Cookie`, `Set-Cookie`,
  `X-Api-Key` and `X-Auth-Token` (plus `VISIONARY_LOG_REDACT_HEADERS`) become
  `[REDACTED]`.
- **JSON keys:** `password`, `passwd`, `secret`, `client_secret`, `token`,
  `access_token`, `refresh_token`, `id_token`, `api_key`, `apikey`, `otp`,
  `pin`, `card_number`, `cvv` and `private_key` (plus
  `VISIONARY_LOG_REDACT_FIELDS`) are redacted in request and response bodies.
  `VISIONARY_LOG_REDACT_REQUEST_FIELDS` adds keys redacted in request bodies
  only.
- **Truncated bodies:** matching uses a regex, so bodies cut at the cap are
  still scrubbed.
- **Form bodies:** `application/x-www-form-urlencoded` request bodies are
  scrubbed the same way (`password=[REDACTED]`).

## Operational notes

- **Request queue:** holds 5000 entries. When it is full, new entries are
  dropped and the count is logged.
- **Request writes:** batches of up to 50 are written every 3 s. A failed
  batch is retried up to 3 times (1 s, then 2 s backoff), then dropped with an
  error log.
- **System writes:** samples are written 4 at a time. On failure they are kept
  (at most an hour's worth) and retried with the next write.
- **Retention:** runs 60 s after start, then daily, deleting in chunks of 5000.
- **Logging:** all messages go to the `visionary` logger.
- **Forking servers:** with servers like `gunicorn --preload`, the background
  threads are restarted in each worker after fork.
