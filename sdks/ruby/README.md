# visionary-sdk (Ruby)

Add one gem to a Rails or Rack app and every request shows up in the
[Visionary](../../Readme.md) dashboard: route, status, time, user, redacted
bodies, the database and HTTP calls it made, and any exception. You also get
host and process metrics, and CPU profiles when you turn them on in the
dashboard.

- [Quick start](#quick-start)
- [Integration recipes](#integration-recipes)
- [Choosing the database](#choosing-the-database)
- [Configuration reference](#configuration-reference)
- [Troubleshooting](#troubleshooting)
- [How it works](#how-it-works)

## Quick start

You need Ruby 3.1 or later, and a database the Visionary server also reads
(PostgreSQL, MySQL, a SQLite file or Cloudflare D1).

**1. Install the gem.** Add it to your Gemfile with the driver for your
database:

```ruby
# Gemfile
gem "visionary-sdk", "~> 0.1", require: "visionary"
gem "pg"   # or "mysql2", or "sqlite3"; none for D1
```

```sh
bundle install
```

The gem has no runtime dependencies of its own. In a Rails app that already
uses ActiveRecord you can skip the driver and
[write through the app's pool](#write-through-the-activerecord-pool).

**2. Point it at the database.** Use the same database as the Visionary
server:

```sh
export VISIONARY_DATABASE_URL=postgres://user:pass@db.internal:5432/visionary
export VISIONARY_SERVICE=my-api   # any name; lets several services share one database
```

For D1, set `D1_ACCOUNT_ID`, `D1_DATABASE_ID` and `D1_API_TOKEN` instead. See
[Choosing the database](#choosing-the-database).

**3. Add the middleware.**

- **Rails:** nothing to do. The gem's Railtie adds the middleware first in the
  stack.
- **Any other Rack app** (Sinatra, Hanami, Roda, Grape):

  ```ruby
  # config.ru
  require "visionary"
  use Visionary::Middleware   # first, so it sees the final response
  run MyApp
  ```

**4. Start the app and send a request.** Collection starts on the first
request (or at boot if you call `Visionary.start`). The log shows:

```
visionary: logging requests (path_prefix="", bodies=true, retention_days=0)
visionary: recording system metrics every 15.0s as web-1
```

Tables are created on first use. At exit the SDK flushes what is queued (it
registers an `at_exit` hook that calls `Visionary.close`).

**5. Check the dashboard.** Open the Visionary dashboard. The request appears
on the API overview within a few seconds, with its route, status, time and
any error. Open it from Performance → Slowest requests to see its database and
HTTP calls on a timeline.

## Integration recipes

### Rails

**When:** any Rails app. Adding the gem is the whole integration. The Railtie:

- inserts `Visionary::Middleware` first in the middleware stack,
- records ActiveRecord queries as `db` spans,
- sees exceptions Rails turns into error pages,
- reads route patterns from Rails routing, and uses `Rails.logger`.

Optional extras go in an initializer:

```ruby
# config/initializers/visionary.rb
Visionary.configure do |c|
  c.connection = ActiveRecord::Base   # write through the app's pool instead of a URL
  c.lookup_user = ->(id) { u = User.find_by(id: id); u && [u.email, u.role] }
end
```

**What it records:**

- **Route:** the Rails route pattern without `(.:format)`, e.g.
  `/users/:id`. Rails 7.1+ reads `request.route_uri_pattern`; older versions
  recognize the request again.
- **Request id:** Visionary's id is handed to Rails, so `request.request_id`
  and the `X-Request-ID` response header match the `api_logs` row.
- **Errors:** exceptions that become a 5xx response are recorded with
  `handled = 0`. Ones Rails maps to a 4xx (`RecordNotFound`, routing errors)
  are not errors.

### Sinatra, Grape and other Rack apps

**When:** any app with a `config.ru`.

```ruby
# config.ru
require "visionary"
use Visionary::Middleware
run MyApp
```

Sinatra (`env["sinatra.route"]`) and Grape route patterns are read
automatically. For other routers, report the pattern so `/users/42` and
`/users/43` count as one route. Either call this in the handler:

```ruby
Visionary.set_route("/users/:id")
```

or give a resolver once:

```ruby
Visionary.configure { |c| c.route = ->(env) { env["my.router.pattern"] } }
```

**Records:** one `api_logs` row per request, and exceptions that escape the
app (recorded, then re-raised unchanged; the request is logged as 500).
Exceptions a framework turned into a 5xx page are found in
`env["sinatra.error"]`, `env["rack.exception"]` or
`env["action_dispatch.exception"]`.

### Attach the signed-in user

**When:** your app has logins and you want to see who made each request.

```ruby
# Rails: app/controllers/application_controller.rb
before_action do
  Visionary.set_user(current_user.id, handle: current_user.email, role: current_user.role) if current_user
end
```

Call it from any auth code (a `before_action`, a Warden callback, a Rack
middleware) once the user is known. Only `id` is required. To fill in handle
and role from the id, set `lookup_user` as in the Rails recipe. It runs on the
writer thread, once per distinct user per batch, only for users missing a
handle or role.

**Records:** `user_id`, `user_handle`, `user_role` on the request row and on
its errors.

### Database queries (ActiveRecord)

**When:** the app uses ActiveRecord (Rails or standalone). Nothing to add.
The hook is installed when the agent starts, if ActiveRecord is loaded.

**Records:** every `sql.active_record` notification, except `SCHEMA` and
cached queries, becomes a `db` span:

- name: the statement kind and table, e.g. `SELECT users`, `INSERT bookings`,
  `BEGIN`;
- target: the adapter and database, e.g. `postgresql/shop`, `mysql/shop`,
  `sqlite/app`;
- detail: the statement, sanitized (literals replaced by `?`, `IN (...)`
  collapsed). Parameter values are never stored.

Each request row gets `db_count` and `db_ms`. Both are NULL when no database
hook was installed and no `db` span was recorded. Queries made with `pg`,
`mysql2` or `sqlite3` directly, without ActiveRecord, are not hooked; wrap
them in a [custom span](#custom-spans-redis-queues-cpu-work) with
`kind: "db"`.

### Outbound HTTP (Net::HTTP, Faraday, HTTParty)

**When:** the app calls other APIs. Nothing to add. `Net::HTTP` is hooked when
the agent starts, so clients built on it are covered too: Faraday's default
adapter, HTTParty, and others.

**Records:** an `http` span per call, named `METHOD host` (e.g.
`POST api.stripe.com`), with the URL without its query string as detail. A
5xx answer or a transport error marks the span as an error. Each request row
gets `http_count` and `http_ms`.

Clients that don't use `Net::HTTP` (Faraday with another adapter, `httpx`,
`typhoeus`) are not seen. Wrap their calls in a custom span with
`kind: "http"`.

### Custom spans (Redis, queues, CPU work)

**When:** time inside a request shows as **Untracked** in its breakdown. That
is time spent in something the SDK has no hook for: Redis, a queue client, a
client without `Net::HTTP`, or your own CPU work.

```ruby
value = Visionary.span("GET session", kind: "cache", target: "redis/sessions") do
  redis.get(key)
end

Visionary.span("resize image", kind: "custom") { image.resize(800, 600) }
```

`span` returns the block's value. `kind` is `custom` (default), `cache`,
`queue`, `db`, `http`, `graphql` or `background`. Keep `name` low-cardinality
(the operation, not its arguments). `detail` is optional extra text; never
put secrets or parameter values in it.

**Records:** a span row with status `error` and the exception message if the
block raises (the exception is re-raised).

### Background work

**When:** a handler starts work it does not wait for, such as sending an email
after the response.

```ruby
Visionary.background("send welcome email") { UserMailer.welcome(user).deliver_now }
```

It runs the block in a new thread and returns the `Thread`.

**Records:**

- One span of kind `background` with the name you give, from start to finish.
  If the block raises, the span is marked `error` and the exception is
  re-raised in the thread as Ruby normally does.
- Calls made inside it keep the request's `request_id`, and their `offset_ms`
  counts from the request's start, even after the response was sent. Calls
  that end after the request was logged don't change its `db_*`/`http_*`
  totals. They are stored whatever their duration, up to
  `VISIONARY_SPAN_MAX` per request; after that only those taking at least
  `VISIONARY_SPAN_THRESHOLD_MS`. `background` spans never count in the
  totals.
- Outside a request the span has no `request_id` and is stored only when it
  takes at least `VISIONARY_SPAN_THRESHOLD_MS`.

On Ruby 3.2+ any thread or fiber started during a request inherits it, so a
plain `Thread.new` is attributed too. `background` also carries the request
over on 3.1, and adds the span around the work.

### Errors

**When:** always on. Exceptions that escape the app, or that the framework
turned into a 5xx page, are recorded with `handled = 0`. To record one you
rescued:

```ruby
begin
  charge!(order)
rescue PaymentError => e
  Visionary.capture_exception(e)   # handled = 1, linked to the current request
  render status: 402
end
```

**Records:** an `errors` row with the type, message and stack. Errors are
grouped by a fingerprint over the exception class and the top five frames of
your own code (not gems, the standard library or this SDK), as `method@path`
relative to the working directory, so the group stays the same across deploys
and line changes. At most 20 rows per fingerprint per minute are written.

### Error codes from JSON responses

**When:** your API answers errors with a JSON body like
`{"error": {"code": "card_declined", "key": "payment.declined"}}`.

Nothing to add for that shape. For another shape, set the dotted paths:

```sh
VISIONARY_LOG_ERROR_CODE_FIELD=errors.0.code
VISIONARY_LOG_ERROR_KEY_FIELD=errors.0.key
```

**Records:** `error_code` and `error_key` on requests with status 400 or
above.

### Write through the ActiveRecord pool

**When:** a Rails app that wants Visionary's tables in its own database,
without a second connection setup.

```ruby
# config/initializers/visionary.rb
Visionary.configure { |c| c.connection = ActiveRecord::Base }
```

Any abstract connection class works, for example one with its own
`connects_to` database. Each write checks a connection out of that pool on
the background thread and returns it right after; Visionary uses at most one
pool connection at a time. The adapter must be PostgreSQL, MySQL (or
Trilogy) or SQLite.

### Puma and Unicorn (forking servers)

**When:** Puma in cluster mode or Unicorn, especially with `preload_app!`.

Threads don't survive `fork`. Visionary hooks `Process._fork`, so when
workers are forked after the agent started, each worker restarts its writer,
sampler, profiler and retention threads with fresh queues and connections.
Without an explicit `Visionary.start`, each worker starts collection on its
first request and nothing runs in the master.

To be explicit, add Puma's hooks. They are safe to combine with the automatic
restart:

```ruby
# config/puma.rb
on_worker_boot { Visionary.after_fork }
on_worker_shutdown { Visionary.close }
```

### Start and stop by hand

**When:** you want collection to start at boot (so system metrics begin
before the first request), or you run a script or job runner.

```ruby
Visionary.start      # start now instead of on the first request
# ...
Visionary.close(10)  # flush, waiting at most 10 seconds; also runs at exit
```

`start` never raises. On any problem it logs a warning and collection is off.

### Database pool metrics

**When:** you want the `db_*` pool columns of `system_metrics` filled.

With ActiveRecord loaded, they come from its pool. For another pool, return
its numbers:

```ruby
Visionary.configure do |c|
  c.db_stats = -> { { total: pool.size, idle: pool.idle, in_use: pool.busy, max: pool.max } }
end
```

Optional cumulative keys: `acquires`, `empty_acquires`, `acquire_wait_ms`.

### CPU profiles

**When:** you want to see where CPU time goes. Nothing to add in code.

1. In the dashboard, open **Settings → Profiling** and turn it on. Pick the
   services (matched by `VISIONARY_SERVICE`), the interval (default every
   300 s) and the duration (default 10 s).
2. Within 30 s the app logs
   `visionary: CPU profiling on (10000 ms every 300s)`.
3. Profiles appear on the CPU profiles page. **Profile now** takes one within
   30 s.

Turn it off in the same place; no redeploy either way. To keep a service out
of profiling whatever the dashboard says, set `VISIONARY_PROFILE=false`.

**Records:** one `profiles` row per profile, with `folded` stacks and
`routes` (the milliseconds per `METHOD route` for requests that finished
during the profile). See [How profiling works](#how-profiling-works).

### Query plans (Explain)

The Ruby SDK has no explainer. It does not answer **Explain** on the Queries
page. See [Troubleshooting](#troubleshooting).

## Choosing the database

The app and the Visionary server must point at the same database. The first
match wins:

| Setting | Backend | Driver gem |
|---|---|---|
| `c.connection = ActiveRecord::Base` | the app's ActiveRecord pool (PostgreSQL, MySQL/Trilogy, SQLite) | the app's adapter |
| `VISIONARY_DATABASE_URL=postgres://user:pass@host:5432/db` (or `postgresql://`) | PostgreSQL | `pg` |
| `VISIONARY_DATABASE_URL=mysql://user:pass@host:3306/db` (or `mysql2://`) | MySQL | `mysql2` |
| `VISIONARY_DATABASE_URL=sqlite:///absolute/path.db` | SQLite, absolute path | `sqlite3` |
| `VISIONARY_DATABASE_URL=sqlite://relative.db` | SQLite, relative to the working directory | `sqlite3` |
| `D1_ACCOUNT_ID`, `D1_DATABASE_ID`, `D1_API_TOKEN` (optionally `VISIONARY_DATABASE_URL=d1://`) | Cloudflare D1 over HTTPS | none (`Net::HTTP`) |

Notes:

- On Windows, write an absolute SQLite path as
  `sqlite:///C:/data/visionary.db`.
- With a URL, Visionary opens one connection of its own and reopens it after
  errors.
- For D1, `D1_API_BASE` replaces `https://api.cloudflare.com/client/v4`, for
  a proxy.
- The tables (`api_logs`, `system_metrics`, `spans`, `errors`, `releases`)
  are created on first use, and tables from older versions gain the newer
  columns. `profiles` and `visionary_settings` are created by the Visionary
  server.
- SQLite and D1 suit modest traffic. For thousands of requests a second, use
  PostgreSQL or MySQL.

## Configuration reference

Every option comes from the environment. `Visionary.configure` sets the same
options in code, plus the hooks below. Set them before the first request
(for example in an initializer). Booleans accept `true/false/1/0/yes/no`.

| Variable | `Visionary.configure` | Default | What it does |
|---|---|---|---|
| `VISIONARY_DATABASE_URL` | `database_url` | none | Where rows go; see [Choosing the database](#choosing-the-database) |
| `D1_ACCOUNT_ID`, `D1_DATABASE_ID`, `D1_API_TOKEN` | `d1_account_id`, `d1_database_id`, `d1_api_token` | none | Cloudflare D1 credentials |
| `D1_API_BASE` | `d1_api_base` | Cloudflare's API | D1 API URL, for a proxy |
| `VISIONARY_INSTANCE` | `instance` | hostname | Labels this process's rows |
| `VISIONARY_SERVICE` | `service` | NULL | Service name; lets several services share a database |
| `VISIONARY_RELEASE` | `version` | the platform's commit variable | Release for the deploy marker; hashes are cut to 12 characters |
| `VISIONARY_LOG_REQUESTS` | `log_requests` | `true` | Write one `api_logs` row per request |
| `VISIONARY_LOG_PATH_PREFIX` | `path_prefix` | empty (everything) | Log only paths starting with this, e.g. `/api` |
| `VISIONARY_LOG_BODIES` | `capture_bodies` | `true` | Store redacted request and response bodies |
| `VISIONARY_LOG_MAX_BODY_BYTES` | `max_body_bytes` | `8192` | Capture cap per body |
| `VISIONARY_LOG_REDACT_HEADERS` | `redact_headers` | empty | Comma list of extra headers to redact |
| `VISIONARY_LOG_REDACT_FIELDS` | `redact_fields` | empty | Comma list of extra JSON/form keys to redact in both bodies |
| `VISIONARY_LOG_REDACT_REQUEST_FIELDS` | `redact_request_fields` | empty | Comma list of keys to redact in request bodies only |
| `VISIONARY_LOG_ERROR_CODE_FIELD` | `error_code_field` | `error.code` | Dotted path to the error code in JSON error bodies |
| `VISIONARY_LOG_ERROR_KEY_FIELD` | `error_key_field` | `error.key` | Dotted path to the error key |
| `VISIONARY_LOG_RETENTION_DAYS` | `log_retention_days` | `0` (off) | Above 0, the SDK also deletes older `api_logs`, `spans`, `errors` and `profiles` rows |
| `VISIONARY_SYSTEM_METRICS` | `system_metrics` | `true` | Write `system_metrics` samples |
| `VISIONARY_SYSTEM_INTERVAL` | `system_interval` (seconds) | `15s` | Sample interval: `15`, `15s`, `500ms`, `1m`, `1h` |
| `VISIONARY_SYSTEM_RETENTION_DAYS` | `system_retention_days` | `0` (off) | Above 0, the SDK also deletes older `system_metrics` rows |
| `VISIONARY_SPANS` | `spans` | `true` | Record database, HTTP and custom spans, and per-request totals |
| `VISIONARY_SPAN_THRESHOLD_MS` | `span_threshold_ms` | `100` | Spans outside a request (or past `SPAN_MAX`) are kept when at least this slow |
| `VISIONARY_SPAN_MAX` | `span_max` | `100` | Span rows stored per request |
| `VISIONARY_ERRORS` | `errors` | `true` | Record exceptions |
| `VISIONARY_PROFILE` | `profile` | `true` | Follow the dashboard's Profiling setting; `false` never profiles |

If `VISIONARY_RELEASE` is unset, the release is read from the first of
`RAILWAY_GIT_COMMIT_SHA`, `RENDER_GIT_COMMIT`, `VERCEL_GIT_COMMIT_SHA`,
`HEROKU_SLUG_COMMIT`, `SOURCE_VERSION`, `GIT_COMMIT`, `GITHUB_SHA`,
`K_REVISION`.

Old rows are normally deleted by the Visionary server, following Settings →
Retention in the dashboard. The two `*_RETENTION_DAYS` options only matter
when the service writes to a database no Visionary server reads.

Options only available in code:

| `Visionary.configure` | Default | What it does |
|---|---|---|
| `connection` | nil | An ActiveRecord connection class to write through |
| `logger` | `Rails.logger` in Rails, else stderr | Where the SDK logs |
| `lookup_user` | nil | `->(id) { [handle, role] }`, fills in missing user details |
| `route` | nil | `->(env) { "/users/:id" }`, route pattern for routers the SDK can't see |
| `db_stats` | the ActiveRecord pool | `-> { { total:, idle:, in_use:, max: } }`, pool metrics |
| `disk_path` | working directory | Filesystem whose usage is reported |
| `queue_size` | `5000` | Rows queued before new ones are dropped |
| `batch_size` | `50` | Rows per write (1 to 50) |
| `flush_interval` | `3.0` | Seconds between writes when the batch isn't full |

Always redacted: the headers `Authorization`, `Proxy-Authorization`,
`Cookie`, `Set-Cookie`, `X-Api-Key`, `X-Auth-Token`, and the body keys
`password`, `passwd`, `secret`, `client_secret`, `token`, `access_token`,
`refresh_token`, `id_token`, `api_key`, `apikey`, `otp`, `pin`,
`card_number`, `cvv`, `private_key`.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| Nothing shows up, and the log says `visionary: no database configured (set VISIONARY_DATABASE_URL or D1_*); collection is off` | The variables aren't set in the process that runs the app | Set `VISIONARY_DATABASE_URL` (or all three `D1_*`) where the app runs, then restart it |
| Nothing shows up, and there is no `visionary:` line at all | Collection starts on the first request; or the middleware isn't in the stack (not Rails, no `use Visionary::Middleware`) | Send a request, or call `Visionary.start` at boot. In a Rack app, add `use Visionary::Middleware` to `config.ru` |
| `visionary: unsupported database URL scheme ...` or a `LoadError` for `pg`, `mysql2` or `sqlite3` | Wrong scheme, or the driver gem is missing | Use one of the URLs in [Choosing the database](#choosing-the-database) and add the driver to the Gemfile |
| Rows are written but the dashboard is empty | The app and the server use different databases, or the dashboard filters another service | Point both at the same database. Check `VISIONARY_SERVICE` and the service filter |
| Some requests are missing | `VISIONARY_LOG_PATH_PREFIX` excludes them | Clear it or widen the prefix |
| Requests have no route, so `/users/42` and `/users/43` aren't grouped as `/users/:id` | The router isn't Rails, Sinatra or Grape | Call `Visionary.set_route` or set `c.route` |
| A request's breakdown is mostly **Untracked** | Its time goes to a client the SDK doesn't hook (Redis, non-`Net::HTTP` HTTP, raw `pg`) or to CPU work | Wrap those calls in [`Visionary.span`](#custom-spans-redis-queues-cpu-work) |
| **Explain** on the Queries page stays pending forever ("No SDK has picked this up") | The Ruby SDK has no explainer, so it never answers Explain requests | Plans come only from a service running an SDK with an explainer (Go or Node) for the same database. Its explainer target must match the span's target, e.g. `postgresql/<dbname>` |
| CPU profiles missing | Profiling is off in the dashboard, the service isn't in its service list, or `VISIONARY_PROFILE=false` | Turn it on in **Settings → Profiling** and check the service list. Wait for `visionary: CPU profiling on ...` in the log. A scheduled turn is skipped when no request arrived since the last one |
| `visionary: profiling settings unreadable (...); profiling stays off` | The Visionary server hasn't created `visionary_settings` yet in this database | Start the server against the same database |
| `visionary: batch write failed; ... entries dropped: d1: status ...: ... exceeded D1's free tier daily row read limit` | The D1 database hit Cloudflare's free-tier daily limit | Wait for the daily reset or move to the Workers Paid plan. To use less, log only `/api` (`VISIONARY_LOG_PATH_PREFIX`), raise `VISIONARY_SYSTEM_INTERVAL`, or switch to PostgreSQL or MySQL |
| `visionary: queue full; N entries dropped` | Writes can't keep up with traffic (common on SQLite and D1) | Use PostgreSQL or MySQL, or log fewer paths or no bodies |
| Rows lost on shutdown, or `visionary: log flush timed out; N pending` | The process was killed before the queue was flushed | Let the process exit normally (`at_exit` calls `Visionary.close`). In Puma, add `on_worker_shutdown { Visionary.close }`. In scripts, call `Visionary.close` before exiting |
| Nothing from Puma or Unicorn workers | The agent started in the master before `fork`, and the restart failed | Look for `visionary: restart after fork failed`. Add `on_worker_boot { Visionary.after_fork }` |
| A request body is empty in the log | Bodies are captured as the app reads them; the app never read it | Expected. The size is still stored, from `Content-Length` |

## How it works

All slow work runs on background threads, so requests are never blocked or
slowed. If the database is down, the SDK only writes log lines. Every
Visionary SDK follows the same contract,
[`sdks/SPEC.md`](../SPEC.md).

### What is written

- `api_logs`: one row per request: method, route pattern, status, timing,
  user, redacted headers and bodies, error codes, and `db_count`, `db_ms`,
  `http_count`, `http_ms`.
- `spans`: database, outbound HTTP, custom and background spans. Rows are
  stored for every request, at most `VISIONARY_SPAN_MAX` per request. Spans
  outside requests are stored when they take at least
  `VISIONARY_SPAN_THRESHOLD_MS`. Visionary's own writes never become spans.
- `errors`: exceptions, grouped by fingerprint.
- `releases`: one row per process start, when the release is known.
- `system_metrics`: a sample every `VISIONARY_SYSTEM_INTERVAL`.
- `profiles`: CPU profiles, only while they are on in the dashboard.

Request bodies are captured as the app reads them. Multipart, image, video,
audio, octet-stream, zip and PDF bodies are not captured.

The request is kept in fiber storage (Ruby 3.2+, inherited by child fibers
and threads) or a fiber-local variable on 3.1, so `set_user`, `set_route` and
spans work under Puma, Falcon and in Async tasks.

### System metrics

On Linux the host and node columns come from `/proc` (CPU split, load,
memory, swap, disk I/O, network, TCP, processes, context switches) and `df`
(disk usage). Process CPU comes from `Process.clock_gettime`, GC count and
time from `GC.stat`, heap from `GC.stat` slot and page counts, threads from
`/proc/self/status` (else `Thread.list`). Pool columns come from the
ActiveRecord pool, or from `c.db_stats`. On macOS and Windows most host
columns are NULL. In a container (or a cgroup with a CPU or memory limit) the
`container_*` columns come from the process's own cgroup, v2 or v1: CPU used,
limit and throttling, memory used (without inactive page cache) and limit,
OOM kills.

### How profiling works

The SDK reads the switch from the `visionary_settings` table at start and
every 30 s after that, on its own thread, and logs one line when profiling
turns on or off.

While a profile runs, a thread wakes about every 10 ms and takes the stack of
each app thread whose status is `run`. Threads that sleep, wait on a lock or
queue, or block on IO (a database or HTTP call) have the status `sleep`, so
they are left out, and so are Visionary's own threads.

- `folded`: one `frame;frame;... count` line per distinct stack, outermost
  call first. Each sample counts about 10 ms of CPU. Frames read
  `Class#method file:line`. Your code's paths are relative to the working
  directory. Gems read `lib:<gem>/<path>` (e.g. `lib:rack/rack/builder.rb`),
  the standard library `std:<path>` (e.g. `std:json/common.rb`). Each row is
  capped at 90 000 bytes; the lightest stacks are summed into one `(other)`
  line.
- `routes`: the milliseconds that the requests finishing during the profile
  took, per `METHOD route`.

A turn is skipped when no request arrived since the last one. A scheduled
profile with fewer than 20 samples (about 200 ms of CPU) is dropped, and kept
otherwise even when no request finished during it (`routes` is then `{}`). A
**Profile now** one is kept with any samples.

**Cost:** nothing while profiling is off, apart from the settings read every
30 s. While a profile runs, a few percent of one CPU.

**Accuracy.** Ruby has no built-in CPU sampler, so these are wall-clock
samples of runnable threads:

- Only the thread holding the GVL really runs. Several busy threads are each
  counted, so time spent waiting for the GVL counts as CPU.
- A thread computing in Ruby keeps the GVL for up to 100 ms while the sampler
  waits for it. So while a profile runs, the SDK lowers the app's threads to
  Ruby's lowest thread priority (`-3`, a 12.5 ms quantum) and gives each
  thread its own priority back afterwards. In CRuby, priority sets only this
  quantum, so no thread gains on another. A late sample counts for the 10 ms
  ticks it missed. Code that gives up the GVL within a few ms is still
  under-counted.
- A C extension that releases the GVL to do work (some database drivers,
  compression) keeps the status `run`. Its time shows on the Ruby frame that
  called it.
- C methods (`Array#each`, `Time#strftime`) show the file and line of the Ruby
  code that called them.

### API summary

```ruby
Visionary.set_user(id, handle: nil, role: nil)   # the signed-in user of the current request
Visionary.set_route("/users/:id")                # route pattern, for routers Visionary can't see
Visionary.span(name, kind: "custom", target: nil, detail: nil) { ... }   # a manual span
Visionary.background(name) { ... }               # a thread that stays with the request
Visionary.capture_exception(e)                   # a handled error (handled = 1)
Visionary.configure { |c| ... }                  # options and hooks, before the first request
Visionary.start                                  # start now instead of on the first request
Visionary.after_fork                             # restart threads in a forked worker
Visionary.close(10)                              # flush; also runs at exit
```
