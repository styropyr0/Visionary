# visionary-sdk (Ruby)

The Ruby collector for [Visionary](../../Readme.md). It runs inside your Rack
or Rails service and writes:

- one row per HTTP request into `api_logs` (method, route pattern, status,
  timing, user, redacted headers and bodies, error codes, and how many
  database and outbound HTTP calls the request made),
- `spans` for database statements and outbound HTTP calls of slow or failed
  requests, `errors` for exceptions, and a `releases` row per deploy,
- a periodic host / process / runtime sample into `system_metrics`.

The Visionary server reads these tables for its dashboards. All slow work
happens on background threads, so requests are never blocked or slowed. If
the database is down, the SDK only writes log lines.

Every Visionary SDK behaves the same way. The contract is
[`sdks/SPEC.md`](../SPEC.md).

## Install

```ruby
# Gemfile
gem "visionary-sdk", require: "visionary"
```

Requires Ruby 3.1+. The gem has no runtime dependencies. To write through a
database URL, the app needs that database's driver: `pg`, `mysql2` or
`sqlite3`. Cloudflare D1 needs nothing (it is called over HTTPS with
`Net::HTTP`), and an ActiveRecord app can hand over its own pool instead.

## Quickstart

First configure a database (see [Choosing the database](#choosing-the-database)), for example:

```sh
export VISIONARY_DATABASE_URL=postgres://user:pass@localhost:5432/app
```

### Rails

Adding the gem is enough: the Railtie inserts `Visionary::Middleware` first in
the middleware stack, records ActiveRecord queries as spans, and sees the
exceptions Rails turns into error pages. Collection starts with the first
request.

```ruby
# config/initializers/visionary.rb (optional)
Visionary.configure do |c|
  c.connection = ActiveRecord::Base            # write through the app's pool instead of a URL
  c.lookup_user = ->(id) { u = User.find_by(id: id); u && [u.email, u.role] }
end

# app/controllers/application_controller.rb
before_action { Visionary.set_user(current_user.id, handle: current_user.email, role: current_user.role) if current_user }
```

- **Route:** the Rails route pattern, `request.route_uri_pattern` on Rails
  7.1+ (older versions recognize the request again), without `(.:format)`,
  e.g. `/users/:id`.
- **Request id:** Visionary's id is handed to Rails, so `request.request_id`
  and the `X-Request-ID` response header match the `api_logs` row.
- **Errors:** exceptions that become a 5xx response are recorded with
  `handled = 0`; ones Rails maps to a 4xx (`RecordNotFound`, routing errors)
  are not errors.

### Sinatra (and any Rack app: Hanami, Roda, Grape)

```ruby
# config.ru
require "visionary"
use Visionary::Middleware   # first, so it sees the final response
run MyApp
```

Sinatra and Grape route patterns are read automatically
(`env["sinatra.route"]`, Grape's route info). For other routers, call
`Visionary.set_route("/users/:id")` in the handler, or configure a resolver:

```ruby
Visionary.configure { |c| c.route = ->(env) { env["my.router.pattern"] } }
```

## API

```ruby
Visionary.set_user(id, handle: nil, role: nil)   # the signed-in user of the current request
Visionary.set_route("/users/:id")                # route pattern, for routers Visionary can't see
Visionary.span("charge card", kind: "http", target: "api.stripe.com") { ... }   # a manual span
Visionary.capture_exception(e)                   # a handled error (handled = 1)
Visionary.start                                  # start now instead of on the first request
Visionary.close(10)                              # flush; also runs at exit
```

`set_user`, `set_route` and spans attach to the request being handled. The
request is kept in fiber storage (Ruby 3.2+, inherited by child fibers and
threads) or a fiber-local variable on 3.1, so this works under Puma, Falcon
and in Async tasks. `lookup_user` runs on the writer thread, once per distinct
user per batch, for users missing a handle or role.

## Spans and errors

- **Database:** every `sql.active_record` notification except `SCHEMA` and
  cached queries becomes a `db` span named after the statement (`SELECT
  users`, `INSERT bookings`, `BEGIN`), with the statement sanitized (literals
  replaced by `?`, `IN (...)` collapsed). Parameter values are never stored.
- **Outbound HTTP:** `Net::HTTP` (and clients built on it, such as Faraday's
  default adapter and HTTParty) becomes `http` spans named `METHOD host`; a
  5xx answer or a transport error marks the span as an error.
- **Manual:** `Visionary.span` for anything else (`cache`, `queue`, `custom`).

Every logged request gets `db_count`, `db_ms`, `http_count`, `http_ms`
(`db_*` are NULL when ActiveRecord isn't loaded and no `db` span was
recorded). Span rows are stored only for requests that took at least
`VISIONARY_SPAN_THRESHOLD_MS` or answered 5xx, at most `VISIONARY_SPAN_MAX`
per request; spans outside requests are stored when they take at least the
threshold themselves. Visionary's own writes never become spans.

Exceptions that escape the app are recorded and re-raised unchanged; the
middleware logs the request as 500. Errors are grouped by a fingerprint over
the exception class and the top five frames of your own code (not gems, the
standard library or this SDK), as `method@path` relative to the working
directory, so it is stable across deploys and line changes.

## Forking servers (Puma, Unicorn)

Threads don't survive `fork`. Visionary hooks `Process._fork` (Ruby 3.1+), so
when Puma or Unicorn forks workers after the agent started (for example with
`preload_app!` and an initializer that calls `Visionary.start`), each worker
restarts its writer, sampler and retention threads with fresh queues and
connections. If you prefer to be explicit, Puma's hook does the same and is
safe to combine with it:

```ruby
# config/puma.rb
on_worker_boot { Visionary.after_fork }
on_worker_shutdown { Visionary.close }
```

Without an explicit start, each worker starts collection on its first
request, and nothing runs in the master.

## Choosing the database

In this order:

1. `Visionary.config.connection = ActiveRecord::Base` (or any abstract
   connection class, e.g. one with its own `connects_to` database). Each write
   checks a connection out of that pool on the background thread and returns
   it right after; Visionary uses at most one pool connection at a time.
2. `VISIONARY_DATABASE_URL`: `postgres://` / `postgresql://` (gem `pg`),
   `mysql://` (gem `mysql2`), `sqlite:///absolute/path.db` or
   `sqlite://relative.db` (gem `sqlite3`). Visionary opens one connection of
   its own and reopens it after errors.
3. Cloudflare D1: `D1_ACCOUNT_ID`, `D1_DATABASE_ID`, `D1_API_TOKEN` (and
   `D1_API_BASE` for a proxy), or `VISIONARY_DATABASE_URL=d1://` with those.

The tables (`api_logs`, `system_metrics`, `spans`, `errors`, `releases`) are
created on first use, and tables from older versions gain the newer columns.
With no database configured, Visionary logs one warning and does nothing.

## System metrics

Every `VISIONARY_SYSTEM_INTERVAL` a sample is written (four per batch). On
Linux the host and node columns come from `/proc` (CPU split, load, memory,
swap, disk I/O, network, TCP, processes, context switches) and `df` (disk
usage). Process CPU comes from `Process.clock_gettime`, GC count and time
from `GC.stat`, heap from `GC.stat` slot and page counts, threads from
`/proc/self/status` (else `Thread.list`). Pool columns come from the
ActiveRecord pool when it exists, or from `c.db_stats = -> { { total:, idle:,
in_use:, max: } }`. On macOS and Windows most host columns are NULL.
In a container (or a cgroup with a CPU or memory limit) the `container_*`
columns come from the process's own cgroup, v2 or v1: CPU used, limit and
throttling, memory used (without inactive page cache) and limit, OOM kills.

## Configuration

Everything comes from the environment (see the table in
[`sdks/SPEC.md`](../SPEC.md#environment)); `Visionary.configure` sets the same
options in code (`c.path_prefix = "/api"`, `c.capture_bodies = false`, ...)
plus the hooks above. Set it before the first request.

| Variable | Default |
|---|---|
| `VISIONARY_DATABASE_URL` | — |
| `VISIONARY_INSTANCE` | hostname |
| `VISIONARY_LOG_REQUESTS`, `VISIONARY_LOG_BODIES` | `true` |
| `VISIONARY_LOG_PATH_PREFIX` | everything |
| `VISIONARY_LOG_MAX_BODY_BYTES` | `8192` |
| `VISIONARY_LOG_RETENTION_DAYS`, `VISIONARY_SYSTEM_RETENTION_DAYS` | `30` (0 keeps forever) |
| `VISIONARY_LOG_REDACT_HEADERS`, `_REDACT_FIELDS`, `_REDACT_REQUEST_FIELDS` | added to the defaults |
| `VISIONARY_LOG_ERROR_CODE_FIELD`, `_ERROR_KEY_FIELD` | `error.code`, `error.key` |
| `VISIONARY_SYSTEM_METRICS` | `true` |
| `VISIONARY_SYSTEM_INTERVAL` | `15s` |
| `VISIONARY_SERVICE` | NULL |
| `VISIONARY_RELEASE` | the platform's commit variable, shortened to 12 characters |
| `VISIONARY_SPANS`, `VISIONARY_ERRORS` | `true` |
| `VISIONARY_SPAN_THRESHOLD_MS`, `VISIONARY_SPAN_MAX` | `100`, `100` |

Request bodies are captured as the app reads them, so a body the app never
reads is not stored (its size still is, from `Content-Length`).
