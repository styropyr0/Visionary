# visionary-sdk (Python)

The Python collector for [Visionary](../../Readme.md). It runs inside your web
service and writes:

- one row per HTTP request into `api_logs` (method, route pattern, status,
  timing, user, redacted headers and bodies, error codes, and how many
  database and outbound HTTP calls it made),
- the database and HTTP calls of slow or failed requests into `spans`,
- unhandled and reported exceptions into `errors`,
- one `releases` row per deploy (when the release is known), and
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

Requires Python 3.9+. Without the `system` extra, Linux hosts read every host
and process column from `/proc` instead. On macOS and Windows most of them are
NULL; disk usage, thread count, GC and HTTP throughput are still recorded.

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
| `VISIONARY_LOG_RETENTION_DAYS` | `0` | off; above 0 the SDK also deletes older `api_logs`, `spans`, `errors` and `profiles` rows |
| `VISIONARY_LOG_REDACT_HEADERS`, `_REDACT_FIELDS`, `_REDACT_REQUEST_FIELDS` | empty | comma lists added to the defaults |
| `VISIONARY_LOG_ERROR_CODE_FIELD`, `_ERROR_KEY_FIELD` | `error.code`, `error.key` | dotted JSON paths |
| `VISIONARY_SYSTEM_METRICS` | `true` | sampler on/off |
| `VISIONARY_SYSTEM_INTERVAL` | `15s` | `15`, `500ms`, `1m` |
| `VISIONARY_SYSTEM_RETENTION_DAYS` | `0` | off; above 0 the SDK also deletes older `system_metrics` rows |
| `VISIONARY_SERVICE` | empty | service name on every row |
| `VISIONARY_RELEASE` | platform commit variable | release on every row; see [Telemetry](#telemetry-spans-errors-releases) |
| `VISIONARY_SPANS` | `true` | spans and call totals on/off |
| `VISIONARY_SPAN_THRESHOLD_MS` | `100` | store spans outside a request at least this slow |
| `VISIONARY_SPAN_MAX` | `100` | span rows per request; later calls still count in the totals |
| `VISIONARY_ERRORS` | `true` | error capture on/off |
| `VISIONARY_INSTRUMENT_HTTP` | `false` | Python only: call `instrument_http()` at start |
| `VISIONARY_PROFILE` | `true` | `false` ignores the dashboard and never takes CPU profiles; see [CPU profiles](#cpu-profiles) |

Booleans accept `true/false/1/0/yes/no`.

Old rows are deleted by the Visionary server, following Settings → Retention in
the dashboard. The two `*_RETENTION_DAYS` variables only matter when the
service writes to a database no Visionary server reads.

## Telemetry (spans, errors, releases)

The middleware records the request; the calls inside it are recorded only when
their client is hooked. Outbound HTTP is **not** hooked until you call
`visionary.instrument_http()` (or set `VISIONARY_INSTRUMENT_HTTP=true`); that
also covers boto3 and other SDKs built on `urllib3`, `requests` or `httpx`.
Wrap the rest (Redis, queues, CPU work such as hashing) in `visionary.span(...)`.
Anything left shows as **Untracked** in the request's breakdown; see
[What the middleware sees](../../Readme.md#what-the-middleware-sees-and-what-needs-code).

### Service and release

`VISIONARY_SERVICE` names the service, so several services can share one
database. The release (`version`) is `VISIONARY_RELEASE`, else the first of
`RAILWAY_GIT_COMMIT_SHA`, `RENDER_GIT_COMMIT`, `VERCEL_GIT_COMMIT_SHA`,
`HEROKU_SLUG_COMMIT`, `SOURCE_VERSION`, `GIT_COMMIT`, `GITHUB_SHA`,
`K_REVISION`; commit hashes are cut to 12 characters. Both go on every
`api_logs`, `system_metrics` and `errors` row (`spans`: service only). When
the release is known, one `releases` row (`sdk = python`, `runtime` such as
`CPython 3.13.1`) is written at start; the dashboards mark deploys with it.

### Spans

Every call the SDK can see is counted into `db_count`, `db_ms`, `http_count`
and `http_ms` on the request's `api_logs` row (NULL for a kind of call that is
not instrumented). Its first `VISIONARY_SPAN_MAX` calls are also stored as
`spans` rows, in start order. Spans outside a request are stored when they
take at least `VISIONARY_SPAN_THRESHOLD_MS` themselves, with `request_id` NULL.

The request is found through a contextvar, so spans from sync views, thread
pools that copy the context (FastAPI sync endpoints, `asgiref`,
`contextvars.copy_context().run`) and asyncio tasks all land on the right
request.

| Source | How |
|---|---|
| Django ORM / `connection.cursor()` | automatic: the middleware adds a wrapper to `execute_wrappers` of every database alias |
| SQLAlchemy (sync or async) | `visionary.instrument_sqlalchemy(engine)` |
| psycopg 3 (sync/async), psycopg2, PyMySQL, sqlite3 | `conn = visionary.instrument_connection(conn)` (or `instrument_cursor(cur)`) |
| `requests`, `httpx` (sync and async), `urllib3` | `visionary.instrument_http()` once at startup, or `VISIONARY_INSTRUMENT_HTTP=true` |
| anything else | `with visionary.span("cache", "GET session"):` (also `async with`) |

```python
engine = visionary.instrument_sqlalchemy(create_engine(DSN))

with visionary.span("queue", "publish order.created", target="rabbitmq") as s:
    channel.basic_publish(...)
    s.set_row_count(1)       # optional; s.set_error(...) marks it failed
```

Database spans are named after the first keyword and main table
(`SELECT users`, `INSERT bookings`, `BEGIN`), with the target
`system/database` (`postgresql/app`, `sqlite/app.db`). The statement is stored
sanitized: string and number literals become `?`, `IN (?, ?, ...)` becomes
`IN (?)`, whitespace is collapsed. Parameter values are never stored. HTTP
spans are named `METHOD host`; the detail is the URL without credentials,
query or fragment. An HTTP span fails on a transport error or a 5xx answer.
A call through `requests` makes one span, not a second one for urllib3.

The SDK's own writes are never recorded as spans.

### Background work

Work a handler starts and does not wait for (sending an email, resizing an
image) stays linked to its request. Start it with one of two helpers:

```python
# a new thread (not a daemon, so the process waits for it at exit)
visionary.background("send welcome email", send_welcome, user.id)

# work you schedule yourself: an executor, asyncio.create_task, ...
ctx = contextvars.copy_context()
def resize():
    with visionary.background_span("resize avatar"):   # also `async with`
        make_thumbnails(path)
executor.submit(ctx.run, resize)
```

Each records one span of kind `background`, named as you call it, covering
the whole job; it is marked `error` with the message if the job raises (the
exception still propagates). The database and HTTP calls inside are their own
spans as usual. All of them keep the request's `request_id` and an
`offset_ms` from the request's start, even when they run after the response
was sent.

Calls that end after the request was logged are not added to its
`db_*`/`http_*` totals, and `background` spans never are. They are stored
whatever their duration, up to `VISIONARY_SPAN_MAX` per request on top of the
spans stored with the request; beyond that, only those taking at least
`VISIONARY_SPAN_THRESHOLD_MS`. In the dashboard they appear in the request's
breakdown past the end of the request (`offset_ms + duration_ms` greater than
its `duration_ms`), and are left out of its untracked time, since the
response did not wait for them. Outside any request the helpers still record
the span, with `request_id` NULL, when it takes at least the threshold.

### Errors

The Django, ASGI and Flask integrations record every exception the
framework turns into a 500 (`handled = 0`) and leave the exception and the
framework's response unchanged. 4xx exceptions (`Http404`, `HTTPException`,
`abort(404)`) are not errors. To record one you handled:

```python
try:
    charge(card)
except PaymentError:
    visionary.capture_exception()      # or capture_exception(exc)
```

Inside a request the row carries its `request_id`, method, route, path and
user. `type` is the class name, qualified with its module unless it is a
builtin (`ValueError`, `psycopg.errors.UndefinedTable`); `stack` is
`traceback.format_exception` (the last 16 KB).

`fingerprint` groups the same error: the first 16 hex characters of
`sha1(type + "\n" + frames)`, where `frames` are the 5 innermost frames of
your own code (not the standard library, `site-packages` or this SDK), each
written `function@path` with the path relative to its `sys.path` entry
(`shop/views.py`), joined with `\n`. If no frame is your code, the 5
innermost frames are used; with no frames at all, the message with digit
runs replaced by `0`. At most 20 rows per fingerprint per minute are kept.

Retention: Settings → Retention on the Visionary server (and
`VISIONARY_LOG_RETENTION_DAYS` for `spans` and `errors` when it is set);
`releases` are kept.

## CPU profiles

CPU profiling is **off until someone turns it on in the dashboard**
(Settings → Profiling), and can be turned off there again; no redeploy either
way. The SDK reads the switch from the `visionary_settings` table at start and
every 30 seconds on a background thread, and logs one line when profiling
turns on or off. The dashboard can limit profiling to some services (matched
against `VISIONARY_SERVICE`) and set how often a profile is taken (default
every 300 s) and how long it samples (default 10 s). **Profile now** takes one
profile within about 30 seconds, even while profiling is off.

`VISIONARY_PROFILE=false` makes a service ignore the dashboard and never
profile: use it for code that must never be sampled.

While a profile runs, a daemon thread reads every thread's Python stack
(`sys._current_frames()`) about every 10 ms. It costs a few percent of one CPU
while it samples and nothing in between, never touches the request path, and
is skipped when no request arrived since the last profile. A scheduled profile
with fewer than 20 samples is dropped, and kept otherwise even when no request
finished during it (`routes` is then `{}`); a **Profile now** one is kept with
any samples. Each profile is one `profiles` row:

- `folded`: one `frame;frame;frame count` line per distinct stack, outermost
  first, heaviest first up to 90 000 bytes (the rest summed into `(other) n`).
  Frames read `function file:line`, the file showing whose code it is: your
  code relative to the working directory (`shop/views.py`), installed
  packages as `lib:django/db/models/query.py`, the standard library as
  `std:json/decoder.py`, and frames with no file as the function alone.
- `routes`: JSON of `"METHOD route"` to the milliseconds the requests that
  finished during the profile took, so the flame graph can be read per route.

Only threads that are using the CPU are counted; idle server threads, the
SDK's own threads and threads blocked on I/O or locks are left out. On Linux
this uses per-thread CPU clocks (a thread counts when its CPU time advanced
since the previous sample and it is not parked in a blocking call). **On
Windows and macOS there are no such clocks, so the filter is a heuristic**:
a thread whose innermost Python frame is calling a known blocking function
(`time.sleep`, lock/condition waits, `queue.get`, `select`/`poll`, socket
`accept`/`recv`, asyncio's idle `select`) is left out. It is approximate: a
thread waiting for the GIL, or blocked in a call it does not know, still
counts.

Profiles are pure Python stack samples: time inside C extensions shows on the
Python frame that called them (`hash_password app.py:12` for
`hashlib.pbkdf2_hmac`), and native frames are not seen. The SDK never creates
the `profiles` or `visionary_settings` tables (the Visionary server does);
until they exist profiling stays off. Profiles follow Settings → Retention, and
`VISIONARY_LOG_RETENTION_DAYS` when it is set.

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

With Django 5.1+ PostgreSQL pooling (`"OPTIONS": {"pool": ...}`) you don't need
the hook: when `db_stats` is unset, the Django middleware sums the stats of
Django's pools the same way. Without a pool the `db_*` columns stay NULL.

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
- **Database pool:** whatever your `db_stats` hook returns, or Django's
  PostgreSQL pools when it isn't set.
- **HTTP:** requests, 5xx responses, requests per second and in-flight
  requests seen by the integrations. These are counted even when request
  logging is off.
- **Node (node-exporter style):**
  - CPU: count, user / system / iowait / steal share.
  - Memory: available and page-cache memory, swap.
  - Disk: total size, inode use, read/write KB/s and IOPS summed over whole
    disks (partitions skipped), average I/O wait (await), and busy % of the
    busiest disk.
  - Network: packets/s, errors and drops.
  - TCP: connections established and retransmits.
  - Processes on the host, running and blocked.
  - Context switches/s.
  - This process: read/write KB/s and its open-file limit.

Sources that don't exist on the platform are NULL:

- **Linux only:** iowait, steal, disk busy, the TCP columns,
  `procs_running` / `procs_blocked`.
- **Not on Windows:** page cache, inodes and the open-file limit.
- **Windows resolution:** disk await comes from whole-second counters there,
  so it is coarse.

On Linux the host, process and node columns are read from `/proc` when
psutil isn't installed, so all of them are filled either way.

Older `api_logs` and `system_metrics` tables are upgraded in place at
startup: missing columns are added with `ALTER TABLE ... ADD COLUMN`, and
existing rows keep NULL in them.

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
- **Retention:** only with a retention variable above 0; runs 60 s after start,
  then daily, deleting in chunks of 5000.
- **Telemetry writes:** spans, errors and the release row share the request
  queue and batches (one transaction, or one D1 `batch` call, per flush).
- **Logging:** all messages go to the `visionary` logger.
- **Forking servers:** with servers like `gunicorn --preload`, the background
  threads are restarted in each worker after fork.
