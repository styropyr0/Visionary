# visionary-sdk (Python)

The Python SDK for [Visionary](../../Readme.md). Add one middleware and every
request your Django, Flask or FastAPI service handles shows up in the
dashboard, with its route, status, time, user, errors, database queries and
outbound calls, plus CPU, memory and GC samples of the process.

The SDK writes straight into a database (PostgreSQL, MySQL, SQLite or
Cloudflare D1) that the Visionary server reads. All slow work runs on
background threads, so requests are never blocked or slowed.

## Quick start

About 10 minutes. You need a running Visionary server (see
[Get started](../../Readme.md#get-started)) and its database URL.

**1. Install.** Python 3.9 or later. Pick the extra for your database:

```sh
pip install "visionary-sdk[postgres]"   # PostgreSQL (psycopg 3)
pip install "visionary-sdk[mysql]"      # MySQL (PyMySQL)
pip install visionary-sdk               # SQLite or Cloudflare D1: no extra needed
```

Add `system` (psutil) for full host metrics on macOS and Windows, or `all`
for everything: `pip install "visionary-sdk[postgres,system]"`.

**2. Point it at the database.** Use the same database as the Visionary
server:

```sh
VISIONARY_DATABASE_URL=postgres://user:pass@host:5432/visionary
VISIONARY_SERVICE=my-api          # any name; tells services apart
```

**3. Add the middleware.** Django shown; Flask and FastAPI are below.

```python
# settings.py
MIDDLEWARE = [
    "visionary.integrations.django.VisionaryMiddleware",  # first, so it sees the final response
    # ...
]
```

The middleware calls `visionary.init()` the first time it loads. Nothing
else is needed.

**4. Shutdown.** `init()` registers an `atexit` handler that flushes queued
rows (waiting at most 10 s), so a normal exit or SIGTERM handled by your
server loses nothing. Forking servers (`gunicorn --preload`) are handled:
the background threads restart in each worker.

**5. Check it works.** The SDK logs to the `visionary` logger. With INFO
logging on (`logging.basicConfig(level=logging.INFO)`), it prints at start:

```text
visionary: logging requests (path_prefix='', bodies=True, retention_days=0)
visionary: recording system metrics every 15.0s
```

Send a request. It shows up on **API overview** and **Request logs** within
a few seconds, and the process shows up on **System**. If the log says
`visionary: no database configured (set VISIONARY_DATABASE_URL or D1_*);
collection is off`, step 2 did not reach the process.

Optional next steps, each a line or two: [the signed-in user](#the-signed-in-user),
[outbound HTTP](#outbound-http), [database queries](#database-queries)
outside Django, [Redis and other calls](#redis-queues-and-your-own-code).

## Frameworks

Every integration names the request by its route pattern, so `/users/42` and
`/users/43` count as one route. Exceptions the framework turns into a 500 are
recorded as errors; 4xx exceptions (`Http404`, `HTTPException`, `abort(404)`)
are not.

### Django

Shown in the quick start. Works under WSGI and ASGI, with sync and async
views.

- Route: `request.resolver_match.route` with a leading `/`
  (`/users/<int:id>/`).
- User: when you don't call `set_user`, an authenticated `request.user` is
  used (`pk` as the id, `get_username()` as the handle).
- Database queries: recorded automatically for every database alias (the
  middleware adds a wrapper to `execute_wrappers`).
- Pool stats: with Django 5.1+ PostgreSQL pooling (`"OPTIONS": {"pool": ...}`)
  the `db_*` columns on **System** are filled automatically.

### Flask

```python
from flask import Flask
from visionary.integrations.flask import Visionary

app = Flask(__name__)
Visionary(app)            # or: ext = Visionary(); ext.init_app(app)
```

The route is `request.url_rule.rule` (`/users/<int:user_id>`). `send_file`
keeps its sendfile path; only its `Content-Length` is recorded.

### FastAPI, Starlette and any ASGI app

```python
from fastapi import FastAPI
from visionary.integrations.asgi import VisionaryASGIMiddleware

app = FastAPI()
app.add_middleware(VisionaryASGIMiddleware)
# any other ASGI app: app = VisionaryASGIMiddleware(app)
```

The route is the matched route's path (`/users/{user_id}`). Streaming
responses keep streaming; only the first `VISIONARY_LOG_MAX_BODY_BYTES` of a
body are copied. Spans from sync endpoints (run in a thread pool) and asyncio
tasks land on the right request.

### Other frameworks and scripts

There is no generic WSGI middleware. For a worker, script or another
framework, start the agent yourself; spans, errors and system metrics are
still recorded:

```python
import visionary

agent = visionary.init()   # reads the environment, starts, flushes at exit
```

`init()` never raises. With no database configured it logs one warning and
returns an agent that does nothing, and the integrations pass requests
straight through.

For explicit control (tests, several agents, config in code):

```python
cfg = visionary.from_env()          # or visionary.Config(...)
cfg.path_prefix = "/api"
agent = visionary.Agent(cfg).start()
app.add_middleware(VisionaryASGIMiddleware, agent=agent)   # Flask: Visionary(app, agent=agent)
...
agent.close(timeout=10)             # drains the queue and flushes samples
```

`Agent` is also a context manager (`with visionary.Agent(cfg) as agent:`).

## The signed-in user

Call `set_user` from auth middleware or a view once the user is known. Only
`id` is required:

```python
visionary.set_user(user.id, handle=user.email, role="admin")
```

It is contextvar-based, so it works from sync views, thread pools and
asyncio tasks. Outside a request handled by an integration, it does nothing.
`visionary.set_route("/custom/{pattern}")` sets the route the same way, for
routers the integration cannot see.

## Login attempts

In the login view, record the account the request tried, whether or not it
exists or the password was right. The security detectors use it to spot one
IP trying many accounts, many IPs trying one account, and a signed-in user
trying someone else's:

```python
@app.post("/login")
async def login(form: LoginForm):
    visionary.login_attempt(form.email)
    user = await authenticate(form.email, form.password)
    if user is None:
        raise HTTPException(401, "invalid credentials")
    return {"token": issue_token(user)}
```

The value is trimmed and clipped to 255 characters; outside a request handled
by an integration it does nothing.

To fill in handles and roles for ids logged without them:

```python
def lookup_user(user_id):
    u = USER_CACHE.get(user_id)          # runs on the writer thread; keep it cheap
    return (u.email, u.role) if u else None

cfg = visionary.from_env()
cfg.lookup_user = lookup_user
visionary.init(cfg)
```

## Outbound HTTP

Off until you turn it on. Call this once at startup (or set
`VISIONARY_INSTRUMENT_HTTP=true`):

```python
visionary.instrument_http()   # patches requests, httpx (sync and async) and urllib3
```

That also covers boto3 and other SDKs built on `urllib3`, `requests` or
`httpx`. Each call is a span named `METHOD host`; the detail is the URL
without credentials, query or fragment. It fails on a transport error or a
5xx answer. A call through `requests` makes one span, not a second one for
urllib3.

## Database queries

Django's ORM is automatic. For anything else, add one line where the engine
or connection is created:

**SQLAlchemy** (sync or async engine)

```python
engine = visionary.instrument_sqlalchemy(create_engine(DSN))
```

**psycopg 3 (sync or async), psycopg2, PyMySQL, sqlite3**

```python
conn = visionary.instrument_connection(psycopg.connect(DSN))   # or instrument_cursor(cur)
```

Both take an optional `target` to rename the database.

What is recorded: each statement as a span named after the first keyword and
main table (`SELECT users`, `INSERT bookings`, `BEGIN`), with the target
`system/database` (`postgresql/app`, `sqlite/app.db`). The statement is
sanitized: literals become `?`, `IN (?, ?, ...)` becomes `IN (?)`, whitespace
is collapsed. Parameter values are never stored. Until a database is
instrumented, a request's database totals stay empty ("cannot see"), not
zero.

## Redis, queues and your own code

There is no Redis or queue hook. Wrap the call in a span (also `async with`):

```python
with visionary.span("cache", "GET session"):
    session = redis.get(key)

with visionary.span("queue", "publish order.created", target="rabbitmq") as s:
    channel.basic_publish(...)
    s.set_row_count(1)       # optional; s.set_error(...) marks it failed
```

Kinds are `db`, `http`, `cache`, `queue`, `custom`, `graphql` and
`background`; anything else becomes `custom`. An exception marks the span
failed and propagates unchanged. Time no span covers shows as **Untracked**
in the request's breakdown; see
[What the middleware sees](../../Readme.md#what-the-middleware-sees-and-what-needs-code).

## Work after the response

Work a handler starts and does not wait for (an email, an image resize)
stays linked to its request. Start it with one of two helpers:

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

Each records one `background` span covering the whole job, marked `error`
if the job raises (the exception still propagates). Its database and HTTP
calls are their own spans as usual. In the request's breakdown they appear
after the end of the request and are left out of its totals and untracked
time. Outside a request the helpers still record the span when it takes at
least `VISIONARY_SPAN_THRESHOLD_MS`.

## Errors

The integrations record every exception the framework turns into a 500
(`handled = 0`) and leave the exception and the response unchanged. To
record one you handled:

```python
try:
    charge(card)
except PaymentError:
    visionary.capture_exception()      # or capture_exception(exc)
```

Inside a request the row carries its request id, method, route, path and
user. Errors are grouped into issues on **Errors** by a fingerprint: the
first 16 hex characters of `sha1(type + "\n" + frames)`, over the 5 innermost
frames of your own code (not the standard library, `site-packages` or this
SDK), each `function@path` relative to its `sys.path` entry. If no frame is
yours, the 5 innermost frames are used; with no frames, the message with
digit runs replaced by `0`. At most 20 rows per fingerprint per minute are
kept.

## Query plans

Not supported in the Python SDK yet. **Explain** on **Queries** is answered
only by a service that registered an explainer for that database (the Go and
Node SDKs can). For a database only Python services use, the request stays
pending.

## CPU profiles

Off until someone turns them on in the dashboard (**Settings → Profiling**).
No redeploy either way: the SDK reads the switch at start and every 30
seconds, and logs `visionary: CPU profiling on (10000 ms every 300 s)` or
`visionary: CPU profiling off`. The dashboard can limit profiling to some
services (matched against `VISIONARY_SERVICE`) and set how often a profile is
taken (default every 300 s) and how long it samples (default 10 s).
**Profile now** on **CPU profiles** takes one within about 30 seconds, even
while profiling is off.

`VISIONARY_PROFILE=false` makes a service ignore the dashboard and never
profile.

How it works: a daemon thread reads every thread's Python stack about every
10 ms while a profile runs. It costs a few percent of one CPU while sampling
and nothing in between, and is skipped when no request arrived since the
last profile. Only threads using the CPU are counted. On Linux that uses
per-thread CPU clocks. On Windows and macOS it is a heuristic: a thread whose
innermost frame is in a known blocking call (`time.sleep`, lock waits,
`queue.get`, `select`, socket `accept`/`recv`, asyncio's idle `select`) is
left out. Time inside C extensions shows on the Python frame that called them.

The SDK never creates the `profiles` or `visionary_settings` tables (the
Visionary server does). Until they exist profiling stays off and the log says
`CPU profiling settings unavailable`.

## System metrics

On by default, every 15 seconds: host CPU, load, memory, disk and network;
process CPU, RSS, threads and open files; Python threads (in the `goroutines`
column), GC collections and pause time, the `tracemalloc` heap when tracing
is on; HTTP throughput seen by the integrations; and node-exporter style
columns (CPU breakdown, swap, disk I/O, TCP, processes, context switches).

On Linux everything is read from `/proc`, with or without psutil. On macOS
and Windows install the `system` extra, or most host and process columns
stay empty. iowait, steal, disk busy, TCP and process counts are Linux only.

To fill the database pool columns, give it your pool's stats (counters are
cumulative; missing keys are stored empty):

```python
def db_stats():
    s = pool.get_stats()                     # e.g. psycopg_pool
    return {
        "total": s["pool_size"], "idle": s["pool_available"],
        "in_use": s["pool_size"] - s["pool_available"], "max": s["pool_max"],
        "acquires": s.get("requests_num", 0), "empty_acquires": s.get("requests_queued", 0),
        "acquire_wait_ms": s.get("requests_wait_ms", 0),
    }

cfg = visionary.from_env()
cfg.db_stats = db_stats
visionary.init(cfg)
```

## Service and release

`VISIONARY_SERVICE` names the service, so several can share one database.
The release is `VISIONARY_RELEASE`, else the first of
`RAILWAY_GIT_COMMIT_SHA`, `RENDER_GIT_COMMIT`, `VERCEL_GIT_COMMIT_SHA`,
`HEROKU_SLUG_COMMIT`, `SOURCE_VERSION`, `GIT_COMMIT`, `GITHUB_SHA`,
`K_REVISION`; commit hashes are cut to 12 characters. When it is known, a
`releases` row is written at start and the dashboards mark the deploy.

## Choosing the database

The app and the Visionary server must use the same database. The SDK takes
the first of these that is set:

1. **Your own connection factory**, when the app already has a driver:

   ```python
   cfg = visionary.from_env()
   cfg.connection_factory = lambda: psycopg.connect(DSN)   # returns a new DB-API connection
   cfg.dialect = "postgres"                               # "postgres", "mysql" or "sqlite"
   visionary.init(cfg)
   ```

   The SDK keeps that connection for itself (one shared connection for
   PostgreSQL and MySQL, one per worker thread for SQLite), reconnects
   through the factory after an error and closes it at shutdown. Each batch
   is one transaction; an autocommit connection makes batches non-atomic.

2. **`VISIONARY_DATABASE_URL`**.
3. **`D1_ACCOUNT_ID`, `D1_DATABASE_ID` and `D1_API_TOKEN`**.

| Database | `VISIONARY_DATABASE_URL` | Install |
|---|---|---|
| PostgreSQL | `postgres://user:pass@host:5432/db` (or `postgresql://`) | `visionary-sdk[postgres]` (psycopg 3) |
| MySQL 8 / MariaDB | `mysql://user:pass@host:3306/db` | `visionary-sdk[mysql]` (PyMySQL) |
| SQLite | `sqlite:///var/lib/app/visionary.db`, `sqlite:///C:/data/visionary.db` on Windows, `sqlite://relative.db` | nothing (stdlib `sqlite3`) |
| Cloudflare D1 | leave it unset, or `d1://`, and set `D1_ACCOUNT_ID`, `D1_DATABASE_ID`, `D1_API_TOKEN` | nothing |

The D1 token needs D1 edit access; `D1_API_BASE` overrides the API URL.
SQLite works only when the app and the server run on the same host.

Tables are created at startup from the schema files in the package
(`visionary/schema/<dialect>/`), on the background threads, so a slow
database never delays boot. If the database is down, creation is retried
before each write. Older tables gain missing columns in place.

## Configuration

Every variable has a matching `visionary.Config` attribute. Set it on the
result of `visionary.from_env()` and pass that to `init()`.

| Variable | `Config` attribute | Default | What it does |
|---|---|---|---|
| `VISIONARY_DATABASE_URL` | `database_url` | | Where rows go; see above. |
| `D1_ACCOUNT_ID`, `D1_DATABASE_ID`, `D1_API_TOKEN`, `D1_API_BASE` | `d1_account_id`, `d1_database_id`, `d1_api_token`, `d1_api_base` | | Cloudflare D1. |
| | `connection_factory`, `dialect` | | Your own connection; see above. |
| `VISIONARY_SERVICE` | `service` | empty | Service name on every row. |
| `VISIONARY_RELEASE` | `version` | platform commit | Release on every row and the deploy marker. |
| `VISIONARY_INSTANCE` | `instance` | hostname | Labels this process on **System**. |
| `VISIONARY_LOG_REQUESTS` | `log_requests` | `true` | Request logging on or off. |
| `VISIONARY_LOG_PATH_PREFIX` | `path_prefix` | empty (all) | Only log paths starting with this, e.g. `/api`. |
| `VISIONARY_LOG_BODIES` | `capture_bodies` | `true` | Store request and response bodies (redacted). |
| `VISIONARY_LOG_MAX_BODY_BYTES` | `max_body_bytes` | `8192` | Body bytes kept per body. |
| `VISIONARY_LOG_REDACT_HEADERS` | `redact_headers` | | Comma list of headers added to the redacted defaults. |
| `VISIONARY_LOG_REDACT_FIELDS` | `redact_fields` | | JSON/form keys added to the redacted defaults. |
| `VISIONARY_LOG_REDACT_REQUEST_FIELDS` | `redact_request_fields` | | Keys redacted in request bodies only. |
| `VISIONARY_LOG_ERROR_CODE_FIELD` | `error_code_field` | `error.code` | Where error responses keep their code (dotted JSON path). |
| `VISIONARY_LOG_ERROR_KEY_FIELD` | `error_key_field` | `error.key` | The same, for the error key. |
| `VISIONARY_LOG_RETENTION_DAYS` | `log_retention_days` | `0` (off) | Only for a database no Visionary server reads: the SDK then deletes its own api_logs, spans, errors and profiles older than this. |
| `VISIONARY_SYSTEM_METRICS` | `system_metrics` | `true` | System samples on or off. |
| `VISIONARY_SYSTEM_INTERVAL` | `system_interval` (seconds) | `15s` | Time between samples: `15`, `500ms`, `1m`. |
| `VISIONARY_SYSTEM_RETENTION_DAYS` | `system_retention_days` | `0` (off) | The same as the log retention, for system_metrics. |
| `VISIONARY_SPANS` | `spans` | `true` | Database and outbound call spans and totals. |
| `VISIONARY_SPAN_THRESHOLD_MS` | `span_threshold_ms` | `100` | Keep calls made outside a request at least this slow. |
| `VISIONARY_SPAN_MAX` | `span_max` | `100` | Spans kept per request; later calls still count in the totals. |
| `VISIONARY_ERRORS` | `errors` | `true` | Error capture on or off. |
| `VISIONARY_INSTRUMENT_HTTP` | `instrument_http` | `false` | Call `instrument_http()` at start. |
| `VISIONARY_PROFILE` | `profile` | `true` (follow the dashboard) | `false` never profiles. |
| | `queue_size` | `5000` | Request queue entries; when full, new entries are dropped and logged. |
| | `batch_size` | `50` | Rows per write. |
| | `flush_interval` | `3.0` | Seconds between writes. |
| | `db_stats` | | Pool stats for the `db_*` columns. |
| | `disk_path` | working directory | Filesystem reported in the disk columns. |
| | `lookup_user` | | `(id) -> (handle, role)`, see [the signed-in user](#the-signed-in-user). |

Booleans accept `true/false/1/0/yes/no`. Old rows are normally deleted by the
Visionary server, following **Settings → Data** in the dashboard.

## What is captured

Each request (`api_logs`): the request id (incoming `X-Request-ID` up to 128
characters, else a random one, echoed on the response), start time and
duration, method, path, route and query string, final status (500 when the
handler raised), client IP (first `X-Forwarded-For` hop) and user agent, the
user, request headers, bodies up to the cap with truncation flags and full
sizes, the error code and key from JSON error bodies, and the request's
database and HTTP call counts and time.

Bodies are not captured for `multipart/*`, `image/*`, `video/*`, `audio/*`,
`application/octet-stream`, `application/zip` and `application/pdf`.

Redaction runs before anything is stored, case-insensitive:

- Headers `Authorization`, `Proxy-Authorization`, `Cookie`, `Set-Cookie`,
  `X-Api-Key`, `X-Auth-Token`, plus `VISIONARY_LOG_REDACT_HEADERS`.
- JSON and form keys `password`, `passwd`, `secret`, `client_secret`,
  `token`, `access_token`, `refresh_token`, `id_token`, `api_key`, `apikey`,
  `otp`, `pin`, `card_number`, `cvv`, `private_key`, plus
  `VISIONARY_LOG_REDACT_FIELDS`, also in bodies cut at the cap.

Writes: requests, spans, errors and the release row are batched (up to 50
every 3 s, one transaction per flush). A failed batch is retried up to 3
times, then dropped with an error log. System samples are written 4 at a
time and kept (up to an hour) while the database is unreachable.

## API

| Call | What it does |
|---|---|
| `init(config=None)` | Builds, starts and returns the process-wide agent; flushes at exit. Never raises. |
| `get_agent()` | The agent from `init()`, if any. |
| `from_env()`, `Config(...)`, `Agent(config).start()`, `agent.close(timeout=10)` | Explicit setup. |
| `VisionaryMiddleware` (Django), `Visionary` (Flask), `VisionaryASGIMiddleware` | Framework integrations, in `visionary.integrations.*`. |
| `set_user(id, handle=None, role=None)`, `set_route(pattern)`, `login_attempt(account)` | Annotate the current request. |
| `capture_exception(exc=None)` | Record a handled error. |
| `span(kind, name, target=None, detail=None)` | Custom span (`with` / `async with`). |
| `background(name, fn, *args, **kwargs)`, `background_span(name)` | Work after the response. |
| `instrument_http()` | Hook requests, httpx and urllib3. |
| `instrument_sqlalchemy(engine)`, `instrument_connection(conn, target=None)`, `instrument_cursor(cur, target=None)` | Database hooks. |

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| Nothing shows up; the log says `no database configured ... collection is off` | `VISIONARY_DATABASE_URL` (or the D1 variables) is not set in the process. | Set it where the service runs (check the gunicorn/uvicorn environment, not only your shell). |
| Nothing shows up; no `visionary:` lines at all | The middleware is not installed, or INFO logs are hidden. | Check the middleware is listed first. Turn on INFO for the `visionary` logger to see its start lines. |
| Nothing shows up, but the SDK logs fine | The app and the server use different databases. | Point both at the same database (same host, database name and, for D1, the same database id). |
| `PostgreSQL needs psycopg: pip install 'visionary-sdk[postgres]'` (or MySQL / PyMySQL) | The driver for the URL scheme is missing. | Install the extra. |
| `create ... schema failed (will retry)` | The database user cannot create tables, or the database is down. | Grant `CREATE`, or check the connection. It retries before each write. |
| Requests show but no outbound calls | HTTP is not hooked by default. | Call `visionary.instrument_http()` at startup or set `VISIONARY_INSTRUMENT_HTTP=true`. |
| Requests show but no database queries (Flask, FastAPI) | The driver is not instrumented (only Django's ORM is automatic). | Add `instrument_sqlalchemy(engine)` or `instrument_connection(conn)`. |
| Some requests show large **Untracked** time | Redis, queue clients or CPU work are not wrapped. | Wrap them in `visionary.span(...)`. |
| **Explain** on **Queries** stays pending forever | The Python SDK has no explainer, so nothing claims the request. | Plans are answered only by a Go or Node service registered with an explainer for that database, whose target matches the span target (`postgresql/<dbname>`). |
| No CPU profiles | Profiling is off in the dashboard, the service is not in the chosen list, `VISIONARY_PROFILE=false`, or no requests arrived since the last profile. | Turn it on in **Settings → Profiling** and check the service name matches `VISIONARY_SERVICE`. Look for `visionary: CPU profiling on` in the log. |
| `request batch write failed ... exceeded D1's free tier daily row read limit` | Cloudflare D1's free plan allows a fixed number of row reads and writes a day; the SDK's writes and the dashboard's reads count. | Move to the Workers Paid plan, or to PostgreSQL or MySQL. Until then, write less: `VISIONARY_LOG_PATH_PREFIX=/api`, `VISIONARY_LOG_BODIES=false`, a longer `VISIONARY_SYSTEM_INTERVAL`. |
| `request queue full; N entries dropped` | Writes cannot keep up (common on SQLite or D1 under heavy traffic). | Use PostgreSQL or MySQL, or raise `queue_size` / `batch_size`. |
| The last requests before a deploy are missing | The process was killed before the `atexit` flush ran (SIGKILL, or a SIGTERM nobody handled). | Let the server shut down gracefully, or call `visionary.get_agent().close()` in your shutdown hook. A `request log flush timed out; N pending` line means 10 s was not enough. |
| `/users/42` and `/users/43` show as separate routes | No route pattern for this request. | Call `visionary.set_route("/users/{id}")` in the handler. |

Every Visionary SDK follows the same contract: [`sdks/SPEC.md`](../SPEC.md).
