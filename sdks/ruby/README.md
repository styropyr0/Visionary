# visionary-sdk (Ruby)

The Ruby collector for [Visionary](../../Readme.md). It runs inside your Rack
or Rails service and writes:

- one row per HTTP request into `api_logs` (method, route pattern, status,
  timing, user, redacted headers and bodies, error codes, and how many
  database and outbound HTTP calls the request made),
- `spans` for database statements and outbound HTTP calls of slow or failed
  requests, `errors` for exceptions, and a `releases` row per deploy,
- a periodic host / process / runtime sample into `system_metrics`,
- CPU profiles into `profiles`, only while they are turned on in the
  dashboard.

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
Visionary.background("send welcome email") { ... } # a thread that stays with the request
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

The middleware records the request; the calls inside it are recorded only when
their client is hooked (see below). Wrap the rest (a Redis or queue client
without a hook, CPU work such as image processing) in `Visionary.span`.
Anything left shows as **Untracked** in the request's breakdown; see
[What the middleware sees](../../Readme.md#what-the-middleware-sees-and-what-needs-code).

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
recorded). Span rows are stored for every request, at most
`VISIONARY_SPAN_MAX` per request; spans outside requests are stored when they
take at least `VISIONARY_SPAN_THRESHOLD_MS` themselves. Visionary's own writes never become spans.

Exceptions that escape the app are recorded and re-raised unchanged; the
middleware logs the request as 500. Errors are grouped by a fingerprint over
the exception class and the top five frames of your own code (not gems, the
standard library or this SDK), as `method@path` relative to the working
directory, so it is stable across deploys and line changes.

## Background work

Work a handler starts and doesn't wait for still belongs to its request. Start
it with `Visionary.background`, which runs the block in a new thread and
returns the `Thread`:

```ruby
Visionary.background("send welcome email") { UserMailer.welcome(user).deliver_now }
```

- The block is one span of kind `background` with the name you give, from
  start to finish. If it raises, the span is marked `error` with the message
  and the exception is re-raised in the thread as Ruby normally does (printed
  when `Thread.report_on_exception` is on, raised again by `join`/`value`).
- Calls made inside it keep the request's `request_id`, and their `offset_ms`
  counts from the request's start, even after the response was sent. Calls
  that end after the request was logged don't change its `db_*`/`http_*`
  totals. They are stored whatever their duration, up to
  `VISIONARY_SPAN_MAX` of them per request; after that only those taking at
  least `VISIONARY_SPAN_THRESHOLD_MS`. `background` spans never count in the
  totals.
- Outside a request the `background` span has no `request_id` and is stored
  only when it takes at least `VISIONARY_SPAN_THRESHOLD_MS`.

On Ruby 3.2+ any thread or fiber started during a request inherits it, so
calls from a plain `Thread.new` are attributed the same way; `background`
also carries the request over on 3.1 and adds the span around the work.

## CPU profiles

Profiling is **off until you turn it on in the dashboard** (Settings →
Profiling), and you can turn it off there again at any time. Neither needs a
redeploy. The SDK reads the switch from the `visionary_settings` table when it
starts and every 30 s after that, on its own thread. It logs one line when
profiling turns on or off. It follows the dashboard's service list
(`VISIONARY_SERVICE`), interval (default every 300 s) and duration (default
10 s), and "Profile now" takes one profile within 30 s. To keep a service out
of profiling whatever the dashboard says, set `VISIONARY_PROFILE=false`.

While a profile runs, a thread wakes about every 10 ms and takes the stack of
each app thread whose status is `run`. Threads that sleep, wait on a lock or
queue, or block on IO (a database or HTTP call) have the status `sleep`, so
they are left out, and so are Visionary's own threads. One row is written per
profile:

- `folded`: one `frame;frame;... count` line per distinct stack, outermost
  call first. Each sample counts about 10 ms of CPU. Frames read
  `Class#method file:line`. Your code's paths are relative to the working
  directory. Gems are shown as `lib:<gem>/<path>`, e.g.
  `lib:rack/rack/builder.rb`, and the standard library as `std:<path>`, e.g.
  `std:json/common.rb` or `std:kernel.rb` for Ruby's built-in Ruby code.
  Each row is capped at 90 000 bytes, and the lightest stacks are summed into
  one `(other)` line.
- `routes`: the milliseconds that the requests finishing during the profile
  took, per `METHOD route`.

A turn is skipped when no request arrived since the last one. A scheduled
profile is dropped when it has fewer than 20 samples (about 200 ms of CPU), and
kept otherwise even when no request finished during it (`routes` is then
`{}`). A **Profile now** one is kept with any samples.

**Cost:** nothing while profiling is off apart from the settings read every
30 s. While a profile runs, the sampler takes some tens of stack walks per
second, which costs a few percent of one CPU.

**How accurate it is.** Ruby has no built-in CPU sampler, so these are
wall-clock samples of threads that are runnable:

- Only the thread holding the GVL really runs. Several busy threads are each
  counted, so time spent waiting for the GVL counts as CPU.
- A thread computing in Ruby keeps the GVL for up to 100 ms while the sampler
  waits for it. A request that computes for 40 ms and then writes its answer
  would rarely be caught computing. So while a profile runs, the SDK lowers
  the app's threads to Ruby's lowest thread priority (`-3`, a 12.5 ms
  quantum) and gives each thread its own priority back afterwards. In CRuby,
  priority sets only this quantum, so no thread gains on another. A late
  sample counts for the 10 ms ticks it missed. Code that gives up the GVL
  within a few ms (short bursts between IO calls) is still under-counted.
- A C extension that releases the GVL to do work (some database drivers,
  compression) keeps the status `run`. Its time shows on the Ruby frame that
  called it.
- C methods (`Array#each`, `Time#strftime`) show the file and line of the Ruby
  code that called them.

## Forking servers (Puma, Unicorn)

Threads don't survive `fork`. Visionary hooks `Process._fork` (Ruby 3.1+), so
when Puma or Unicorn forks workers after the agent started (for example with
`preload_app!` and an initializer that calls `Visionary.start`), each worker
restarts its writer, sampler, profiler and retention threads with fresh queues and
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
| `VISIONARY_LOG_RETENTION_DAYS`, `VISIONARY_SYSTEM_RETENTION_DAYS` | `0` (off; above 0 the SDK also deletes older rows) |
| `VISIONARY_LOG_REDACT_HEADERS`, `_REDACT_FIELDS`, `_REDACT_REQUEST_FIELDS` | added to the defaults |
| `VISIONARY_LOG_ERROR_CODE_FIELD`, `_ERROR_KEY_FIELD` | `error.code`, `error.key` |
| `VISIONARY_SYSTEM_METRICS` | `true` |
| `VISIONARY_SYSTEM_INTERVAL` | `15s` |
| `VISIONARY_SERVICE` | NULL |
| `VISIONARY_RELEASE` | the platform's commit variable, shortened to 12 characters |
| `VISIONARY_SPANS`, `VISIONARY_ERRORS` | `true` |
| `VISIONARY_SPAN_THRESHOLD_MS`, `VISIONARY_SPAN_MAX` | `100`, `100` |
| `VISIONARY_PROFILE` | `true`: follow the dashboard's Profiling setting; `false` never profiles |

Old rows are deleted by the Visionary server, following Settings → Retention in
the dashboard. The two `*_RETENTION_DAYS` variables only matter when the
service writes to a database no Visionary server reads.

Request bodies are captured as the app reads them, so a body the app never
reads is not stored (its size still is, from `Content-Length`).
