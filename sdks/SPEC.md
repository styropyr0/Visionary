# Collector SDK contract

Every Visionary server SDK (Go in `sdks/go`, Python in `sdks/python`, Java/Spring
Boot in `sdks/java`, Node in `sdks/node`, Ruby in `sdks/ruby`, .NET in
`sdks/dotnet`) writes the same rows to the same tables, reads the same
environment variables, and behaves the same way. The dashboards and the
analyst depend only on this contract. The Go SDK is the reference
implementation; when in doubt, match it.

## Databases

The tables can live in any of these. All SDKs support all four:

| Backend | How the SDK connects | Placeholders | Schema |
|---|---|---|---|
| Cloudflare D1 | REST API (below) | `?`, string params | `sdks/go/schema/sqlite/` |
| SQLite | the app's/driver's connection to a file | `?` | `sdks/go/schema/sqlite/` |
| PostgreSQL | the app's connection or pool | `$1, $2, ...` (Go `database/sql` + pgx), `%s` (psycopg), `?` (JDBC) | `sdks/go/schema/postgres/` |
| MySQL 8+ / MariaDB | the app's connection or pool | `?` (Go, JDBC), `%s` (PyMySQL) | `sdks/go/schema/mysql/` |

`VISIONARY_DATABASE_URL` picks the backend from its scheme: `postgres://` or
`postgresql://`, `mysql://`, `sqlite:///absolute/path.db` (or
`sqlite://relative.db`), `d1://` (or unset with the `D1_*` variables present).
SDKs may also accept a connection/pool the application already has, which is
preferred: the app controls drivers and pooling.

`created_at` is stored as **text** in every backend, in the fixed format
`YYYY-MM-DDTHH:MM:SS.mmmZ`, so string comparison is time order and every
dialect filters the same way. `user_id` is text in PostgreSQL and MySQL
(UUIDs work); SQLite/D1 keep it as given.

## Tables

Create them at startup with the exact SQL in `sdks/go/schema/<dialect>/requests.sql`,
`system.sql` and `telemetry.sql`, statement by statement (split on `;`). Ship copies inside each
SDK package; do not hand-edit the schema. MySQL has no `CREATE INDEX IF NOT
EXISTS`: ignore error 1061 (duplicate key name) when creating indexes.

## Writing rows

Build each row as **typed values** (string, integer, float, or null). Then:

- **SQL backends:** run the plain insert with the backend's placeholders and
  the typed values (null for missing).
- **D1:** the REST API takes string params, so send every value as a string
  (`""` for null) and wrap each placeholder in `NULLIF(?, '')`.

Batches: in SQL backends, run a batch's inserts in one transaction (or one
multi-row statement); in D1, one `batch` call.

## Talking to D1

Cloudflare REST API, `Authorization: Bearer <D1_API_TOKEN>`:

- Base: `D1_API_BASE` or `https://api.cloudflare.com/client/v4`, then
  `/accounts/{D1_ACCOUNT_ID}/d1/database/{D1_DATABASE_ID}`.
- Writes and schema: `POST {base}/query` with
  `{"sql": "...", "params": ["..."]}` for one statement, or
  `{"batch": [{"sql": "...", "params": [...]}, ...]}` for many in one call.
- Success is `{"success": true, ...}`; failures have `success: false` and an
  `errors` array of `{code, message}`. Treat HTTP >= 300 or `success: false`
  as an error.
- **Params are always strings.** Column affinity converts numbers. Use `NULLIF(?, '')`
  in SQL and send `""` for NULL.
- At most 100 bound params per statement; at most 50 statements per batch.
- Retention deletes read `result[0].meta.changes` to know how many rows went.

## Request log (`api_logs`)

### Capture (in the framework middleware, on the request thread)

Keep this cheap; everything slow happens on the writer thread.

| Column | Value |
|---|---|
| `request_id` | Incoming `X-Request-ID` if present and at most 128 chars, else a random 32-hex id. Set `X-Request-ID` on the response. |
| `created_at` | Request start, UTC, `YYYY-MM-DDTHH:MM:SS.mmmZ` (always 3 fraction digits, literal `Z`). |
| `method`, `path` | As received; `path` without the query string. |
| `route` | The matched route pattern, e.g. `/users/{id}` (Django `request.resolver_match.route`, Starlette/FastAPI `scope["route"].path`, Flask `request.url_rule.rule`, Spring `HandlerMapping.BEST_MATCHING_PATTERN_ATTRIBUTE`). Empty if none. |
| `query` | Raw query string without `?`. |
| `status` | Final status the client got (500 when the handler raised and the framework turned it into a 500). |
| `duration_ms` | Wall time in ms, 3 decimals. |
| `ip` | First `X-Forwarded-For` hop if present, else the peer address without port. |
| `user_agent` | `User-Agent` header. |
| `user_id`, `user_handle`, `user_role` | From `set_user` / `setUser` (see below), else framework auth if the integration knows it, else empty. |
| `request_headers` | JSON object of all request headers, multiple values joined with `", "`, sensitive ones replaced with `[REDACTED]`. |
| `request_content_type`, `response_content_type` | Header values. |
| `request_body`, `response_body` | Only when bodies are on and the content type is capturable (below). At most `max_body_bytes` bytes each, decoded as UTF-8 with replacement, then redacted. |
| `request_body_truncated`, `response_body_truncated` | `1` if the body was cut at the cap, else `0`. |
| `request_size`, `response_size` | Full body sizes in bytes (Content-Length or bytes actually read/written, whichever is larger). |
| `error_code`, `error_key` | Only when `status >= 400`: values at the dotted paths `VISIONARY_LOG_ERROR_CODE_FIELD` (default `error.code`) and `VISIONARY_LOG_ERROR_KEY_FIELD` (default `error.key`) in the JSON response body. Numbers are written as plain decimals. If the body isn't valid JSON (e.g. truncated), fall back to a regex on the path's last segment: `"<segment>"\s*:\s*"([^"]*)"`. |

**Capturable content types:** empty, or anything except `multipart/*`, `image/*`,
`video/*`, `audio/*`, `application/octet-stream`, `application/zip`,
`application/pdf` (case-insensitive prefix match).

Only paths starting with `VISIONARY_LOG_PATH_PREFIX` are logged (empty = all).
Never break or slow the response. Streaming responses must still stream; only
copy up to the cap.

### User

Each SDK exposes a way for app code (usually auth middleware) to attach the
user to the current request:

- Go: `visionary.SetUser(ctx, visionary.User{ID, Handle, Role})`
- Python: `visionary.set_user(id, handle=None, role=None)` (contextvar-based,
  works in sync and async code)
- Java: `Visionary.setUser(request, id, handle, role)` (request attribute)

Integrations may also read the framework's own user (Django `request.user`,
Spring Security principal name) when nothing was set explicitly. An optional
`lookup_user(id) -> (handle, role)` hook runs on the writer thread, once per
distinct user per batch, to fill in what's missing.

### Redaction

Defaults (case-insensitive), extended by env lists:

- Headers: `Authorization, Proxy-Authorization, Cookie, Set-Cookie, X-Api-Key, X-Auth-Token`
  + `VISIONARY_LOG_REDACT_HEADERS`.
- JSON/form keys, request **and** response: `password, passwd, secret, client_secret,
  token, access_token, refresh_token, id_token, api_key, apikey, otp, pin,
  card_number, cvv, private_key` + `VISIONARY_LOG_REDACT_FIELDS`.
- Request-only keys: `VISIONARY_LOG_REDACT_REQUEST_FIELDS`.

The JSON redaction is a regex so truncated bodies are still scrubbed:
`(?i)("(?:KEYS)"\s*:\s*)("(?:[^"\\]|\\.)*"?|[^,}\]\s]+)` → `$1"[REDACTED]"`, where
KEYS is the regex-escaped keys joined with `|`. For
`application/x-www-form-urlencoded` request bodies:
`(?i)(^|&)(KEYS)=[^&]*` → `$1$2=[REDACTED]`.

### Writer

A single background worker (thread/goroutine):

- Bounded queue of 5000 entries. When full, drop the entry and count it; log a
  warning with the dropped count on the next flush. Never block the request.
- Flush when 50 entries are queued or every 3 s, whichever is first, as one
  `batch` call (one INSERT per entry, the statement below).
- On failure retry the batch up to 3 attempts total, sleeping 1 s then 2 s;
  then log an error and drop the batch.
- `close()`/shutdown drains the queue and flushes, bounded by a timeout.
- Retention: 60 s after start and then every 24 h, delete rows older than
  `VISIONARY_LOG_RETENTION_DAYS` (0 disables) in chunks:
  `DELETE FROM api_logs WHERE id IN (SELECT id FROM api_logs WHERE created_at < ? LIMIT 5000)`,
  repeating while 5000 rows were deleted (at most 200 rounds). MySQL rejects
  `LIMIT` inside `IN (subquery)`, so there use
  `DELETE FROM api_logs WHERE created_at < ? ORDER BY id LIMIT 5000`.

Insert, columns in this order (SQL backends: plain placeholders, null for
missing; D1: every placeholder as `NULLIF(?, '')`, `""` for missing):

```sql
INSERT INTO api_logs (
	request_id, created_at, method, path, route, query, status, duration_ms, ip, user_agent,
	user_id, user_handle, user_role, request_headers, request_content_type, request_body,
	request_body_truncated, request_size, response_content_type, response_body,
	response_body_truncated, response_size, error_code, error_key,
	service, version, db_count, db_ms, http_count, http_ms
) VALUES (30 placeholders)
```

The last six columns were added later (`AddedRequestColumns` in
`sdks/go/migrate.go`); add them to old tables at startup exactly as described for
`system_metrics` below. `service` and `version` are the service name and release
(see Telemetry); `db_count`/`db_ms` and `http_count`/`http_ms` are the number and
total duration of database and outbound HTTP calls the request made (0 when it
made none, NULL when the SDK cannot see that kind of call).

Types: `status`, `*_truncated` (0/1), `*_size` are integers; `duration_ms` a
float; everything else text.

## System metrics (`system_metrics`)

A background sampler every `VISIONARY_SYSTEM_INTERVAL` (default 15 s). It
buffers rows and writes 4 at a time in one `batch` call. Flush on shutdown.
If a write fails, keep the rows (at most one hour's worth) and try again with
the next batch. Take baseline readings at start so the first row has real
deltas. A source that fails or doesn't exist on this runtime/OS is NULL
(`""`), never an error.

Insert: all columns below in this order (same placeholder rules as requests).

The columns from `cpu_count` on were added later. At startup, after creating the
table, read its columns (SQLite/D1: `SELECT name FROM pragma_table_info('system_metrics')`;
PostgreSQL/MySQL: `information_schema.columns` for the current schema or database)
and run `ALTER TABLE system_metrics ADD COLUMN <name> <type>` for each one missing.
Types for SQLite / PostgreSQL / MySQL: counts `INTEGER` / `BIGINT` / `BIGINT`,
measurements `REAL` / `DOUBLE PRECISION` / `DOUBLE`. Ignore "duplicate column"
errors (another instance got there first). `AddedSystemColumns` in
`sdks/go/migrate.go` is the canonical list and insert order.
Counts are integers, measurements floats, `created_at`/`instance` text.

| Column | Meaning | Python | Java |
|---|---|---|---|
| `created_at` | sample time, same format as above | | |
| `instance` | `VISIONARY_INSTANCE` or hostname | | |
| `interval_s` | seconds since the previous sample (2 decimals) | | |
| `host_cpu_percent` | whole-machine CPU % since the previous sample | `psutil.cpu_percent(None)` | `OperatingSystemMXBean.getCpuLoad()*100` |
| `load_1/5/15` | load average | `os.getloadavg()` | `getSystemLoadAverage()` (load_1 only) |
| `host_mem_used_mb/total_mb/percent` | host memory | `psutil.virtual_memory()` | `getTotalMemorySize()/getFreeMemorySize()` |
| `disk_used_percent`, `disk_free_gb` | the working directory's filesystem | `psutil.disk_usage` | `File.getTotalSpace()/getUsableSpace()` |
| `net_rx_kbps`, `net_tx_kbps` | host network KB/s since previous sample | `psutil.net_io_counters()` | `/proc/net/dev` on Linux, else NULL |
| `proc_cpu_percent` | this process, % of one core | `Process.cpu_percent(None)` | `getProcessCpuLoad()*100*cores` |
| `proc_rss_mb` | resident memory | `memory_info().rss` | NULL unless available |
| `proc_threads` | OS threads | `num_threads()` | `ThreadMXBean.getThreadCount()` |
| `proc_open_fds` | open files/handles | `num_fds()`/`num_handles()` | `UnixOperatingSystemMXBean.getOpenFileDescriptorCount()` |
| `goroutines` | concurrency units: goroutines in Go, live threads elsewhere | `threading.active_count()` | `ThreadMXBean.getThreadCount()` |
| `heap_alloc_mb`, `heap_inuse_mb` | runtime heap used / committed | `tracemalloc` if on, else NULL | `MemoryMXBean` heap used / committed |
| `heap_objects` | live heap objects | `len(gc.get_objects())` is too slow: NULL | NULL |
| `gc_count` | collections since previous sample | `gc.get_stats()` collections delta | sum of `GarbageCollectorMXBean.getCollectionCount()` deltas |
| `gc_pause_ms` | GC time since previous sample | measured with `gc.callbacks` | `getCollectionTime()` delta |
| `gc_cpu_percent` | GC time as % of the interval | derived | derived |
| `db_*` | pool stats from an optional user callback; `db_acquires`, `db_empty_acquires`, `db_acquire_wait_ms` are per-sample deltas of cumulative counters | callback | HikariCP `HikariPoolMXBean` when present |
| `http_requests`, `http_errors` | requests / 5xx finished since previous sample (counted by the middleware even when request logging is off) | | |
| `http_rps` | `http_requests / interval_s` (2 decimals) | | |
| `http_in_flight` | requests in progress now | | |
| `uptime_s` | seconds since the SDK started | | |
| `cpu_count` | logical CPUs | `psutil.cpu_count()` / `os.cpu_count()` | `Runtime.availableProcessors()` |
| `cpu_user_percent`, `cpu_system_percent` | share of all CPU time since the previous sample: user (+nice), system (+irq, softirq) | `psutil.cpu_times()` deltas over the sum of all fields | `/proc/stat` deltas on Linux, else NULL |
| `cpu_iowait_percent`, `cpu_steal_percent` | same, for iowait and steal; **Linux only**, else NULL | `cpu_times()` iowait/steal | `/proc/stat` |
| `host_mem_available_mb`, `host_mem_cached_mb` | memory available to new work; page cache (NULL when 0 or unknown) | `virtual_memory().available` / `.cached` | `/proc/meminfo` MemAvailable/Cached on Linux; available = `getFreeMemorySize()` elsewhere |
| `swap_used_mb`, `swap_total_mb` | swap | `psutil.swap_memory()` | `getTotalSwapSpaceSize() - getFreeSwapSpaceSize()` |
| `disk_total_gb`, `disk_inodes_percent` | size of the working directory's filesystem; inodes used % (NULL when unknown) | `disk_usage`; `os.statvfs` f_files/f_ffree | `File.getTotalSpace()`; inodes NULL |
| `disk_read_kbps`, `disk_write_kbps`, `disk_read_iops`, `disk_write_iops` | host disk I/O per second, summed over whole disks only (see the partition rule below) | `psutil.disk_io_counters(perdisk=True)` | `/proc/diskstats` on Linux, else NULL |
| `disk_await_ms` | (read time + write time) delta / (reads + writes) delta; NULL when there was no I/O | same | same |
| `disk_busy_percent` | the busiest whole disk's I/O-time delta / interval × 100, capped at 100; **Linux only** | `busy_time` per disk | `/proc/diskstats` field 13 |
| `net_rx_pps`, `net_tx_pps` | packets per second | `net_io_counters()` | `/proc/net/dev` on Linux, else NULL |
| `net_errors`, `net_drops` | errors in+out and drops in+out since the previous sample | same | same |
| `tcp_established` | TCP connections in ESTABLISHED now; **Linux only** | `/proc/net/snmp` Tcp CurrEstab | same |
| `tcp_retrans` | TCP segments retransmitted since the previous sample; **Linux only** | `/proc/net/snmp` Tcp RetransSegs | same |
| `host_procs`, `procs_running`, `procs_blocked` | processes on the host; running, and blocked on I/O (`/proc/stat` procs_running/procs_blocked on Linux, else NULL) | `len(psutil.pids())`; `/proc/stat` | `/proc` count and `/proc/stat` on Linux, else NULL |
| `ctx_switches_ps` | host context switches per second | `psutil.cpu_stats().ctx_switches` delta | `/proc/stat` ctxt delta on Linux, else NULL |
| `proc_read_kbps`, `proc_write_kbps` | this process's I/O per second | `Process.io_counters()` read_bytes/write_bytes | `/proc/self/io` on Linux, else NULL |
| `proc_max_fds` | soft limit on open files (NULL on Windows or when unlimited) | `resource.getrlimit(RLIMIT_NOFILE)[0]` | `UnixOperatingSystemMXBean.getMaxFileDescriptorCount()` |

Partition rule: skip disk devices matching
`^((sd|vd|xvd|hd)[a-z]+\d+|nvme\d+n\d+p\d+|mmcblk\d+p\d+|loop\d+|ram\d+|zram\d+|dm-\d+|sr\d+|fd\d+|md\d+)$`,
so a partition's I/O is not counted again on top of its disk.

Retention: same schedule as requests, `VISIONARY_SYSTEM_RETENTION_DAYS`,
`DELETE FROM system_metrics WHERE id IN (SELECT id FROM system_metrics WHERE created_at < ? LIMIT 5000)`.

## Telemetry (`spans`, `errors`, `releases`)

Created at startup from `sdks/go/schema/<dialect>/telemetry.sql` (copy it like
the other schemas). Written through the same writer as requests: batched, never
blocking a request, dropped (and counted) when the queue is full. Retention:
`spans` and `errors` follow `VISIONARY_LOG_RETENTION_DAYS` with the same chunked
delete; `releases` are kept.

### Service and version

- `service`: `VISIONARY_SERVICE`, else NULL. Lets several services share a database.
- `version` (the release): `VISIONARY_RELEASE`, else the first non-empty of
  `RAILWAY_GIT_COMMIT_SHA`, `RENDER_GIT_COMMIT`, `VERCEL_GIT_COMMIT_SHA`,
  `HEROKU_SLUG_COMMIT`, `SOURCE_VERSION`, `GIT_COMMIT`, `GITHUB_SHA`, `K_REVISION`;
  commit hashes are shortened to 12 characters. NULL when none is set.
- Both go on every `api_logs`, `system_metrics`, `errors` and `spans` row (spans:
  `service` only).

### Releases (deploy markers)

At start, when `version` is known, write one row:
`releases(created_at, service, instance, version, sdk, sdk_version, runtime)`.
`sdk` is `go`, `python`, `java`, `node`, `ruby`, `dotnet` or `otlp`;
`runtime` is the language runtime and version, e.g. `go1.26.2`, `CPython 3.13.1`,
`OpenJDK 21.0.4`. The dashboards draw a marker at the earliest `created_at` of
each version.

### Spans (database and outbound calls)

A span is one call the service made while handling a request (or in background
work): a database statement, an outbound HTTP request, a cache or queue call.

- **Always**, for every logged request: count the calls and sum their durations
  into `db_count`, `db_ms`, `http_count`, `http_ms` on the `api_logs` row (kinds
  `cache`/`queue`/`custom` only become span rows).
- **Span rows** are stored when the request took at least
  `VISIONARY_SPAN_THRESHOLD_MS` (default 100) or answered ≥ 500, at most
  `VISIONARY_SPAN_MAX` (default 100) per request, in start order; calls beyond
  the cap still count in the totals. Spans outside any request are stored when
  they themselves take at least the threshold, with `request_id` NULL.
- `VISIONARY_SPANS=false` turns span capture off (totals and rows).

Columns:

| Column | Value |
|---|---|
| `created_at` | span start, same format as everywhere |
| `request_id` | the parent request's `request_id`, NULL outside a request |
| `service`, `instance` | as above |
| `kind` | `db`, `http`, `cache`, `queue` or `custom` |
| `name` | short and low-cardinality, used for grouping: `db`: first keyword + main table, e.g. `SELECT users`, `INSERT bookings`, `UPDATE trips`, `BEGIN`; `http`: `METHOD host`, e.g. `POST api.stripe.com`; others: the operation |
| `target` | `db`: system and database, e.g. `postgresql/voygram`; `http`: host |
| `detail` | `db`: the statement, sanitized (below); `http`: method and URL without query string; at most 2000 characters |
| `offset_ms` | start relative to the request start (NULL outside a request) |
| `duration_ms` | 3 decimals |
| `status` | `ok` or `error` (`http`: `error` on a transport error or a 5xx response) |
| `error` | short error message, at most 500 characters |
| `row_count` | rows returned or affected when the driver reports it |

Sanitizing statements: replace string literals (`'...'`, with doubled quotes
inside) and numeric literals with `?`, collapse `IN (?, ?, ...)` to `IN (?)`,
collapse runs of whitespace to one space, trim. Parameter values are never
stored. Hook points per stack: Go: a pgx `QueryTracer`, a `database/sql`
driver wrapper, an `http.RoundTripper`; Python: Django `execute_wrapper`,
SQLAlchemy events, psycopg/PyMySQL cursors, `requests`/`httpx`/`urllib3`;
Java: a JDBC `DataSource` proxy, `RestTemplate`/`RestClient`/`WebClient`
interceptors; Node: `pg`, `mysql2`, Prisma, `fetch`/`undici`/`http`;
Ruby: `ActiveSupport::Notifications` (`sql.active_record`), `Net::HTTP`;
.NET: `DiagnosticListener`/`ActivitySource` for `HttpClient`, ADO.NET and EF Core.

The dashboards flag N+1 patterns (one `name` repeated 10 or more times in a
request) from these rows; SDKs do not need to detect them.

### Errors

- Record every unhandled exception (panic) that reaches the middleware, then
  let it continue exactly as before (re-raise, re-panic): the framework's own
  handling must not change. `handled = 0`.
- Expose a manual API to record a handled error (`handled = 1`): Go
  `visionary.CaptureError(ctx, err)`, Python `visionary.capture_exception(exc=None)`,
  Java `Visionary.captureException(Throwable)`, Node
  `visionary.captureException(err)`, Ruby `Visionary.capture_exception(e)`,
  .NET `Visionary.CaptureException(ex)`. Inside a request it links to it.
- Columns: `created_at`, `service`, `instance`, `version`, `request_id`,
  `method`, `route`, `path`, `user_id` (when inside a request), `type` (the
  exception class or Go error type, e.g. `ValueError`, `*pgconn.PgError`),
  `message` (at most 1000 characters), `stack` (formatted the language's usual
  way, at most 16 KB), `fingerprint`, `handled` (0/1), `runtime`.
- `fingerprint`: the first 16 hex characters of SHA-1 over
  `type + "\n" + frames`, where frames are the top 5 (innermost first: where the error was raised,
  then its callers) frames from the
  application's own code (skip the standard library, the SDK and installed
  packages when they can be told apart; when the stack has frames but none are
  the application's, or they cannot be told apart, the top 5 frames of any kind) written as
  `function@file` without line numbers, joined with `\n`. With no frames, use
  `type + "\n" + message` with every digit run replaced by `0`.
- At most 20 rows per fingerprint per minute per process; drop the rest.
- `VISIONARY_ERRORS=false` turns error capture off.

## Client ingest API (browser and mobile)

Browser and mobile SDKs do not write to the database; they post to the
Visionary server, which writes `client_events`.

`POST /api/ingest/client`, `Content-Type: application/json` (or
`text/plain`, for `navigator.sendBeacon`). The key is a public client key
(`VISIONARY_CLIENT_KEYS` on the server, or a key created in Settings), sent as
`Authorization: Bearer <key>` or, where headers cannot be set, as `?key=<key>`.
Browsers are also checked against `VISIONARY_CLIENT_ORIGINS` (comma list; empty
allows any origin). The server answers `204`, `400` for a bad payload,
`401` for a bad key, `413` over 64 KB, and `429` when a client sends too much;
SDKs drop the batch on 4xx and retry once later on 5xx.

```json
{
  "app": "web | android | ios",
  "app_version": "2.4.1",
  "version": "a1b2c3d4e5f6",
  "session_id": "random per app launch or browser tab",
  "user_id": "optional",
  "device": "Pixel 8 | iPhone15,2 | Chrome 141 on macOS",
  "os": "Android 16 | iOS 26.1 | macOS",
  "events": [
    {
      "ts": "2026-10-17T09:30:12.345Z",
      "kind": "page_load | web_vital | screen | api | error | custom",
      "name": "LCP | /checkout | CheckoutActivity | GET /api/trips | TypeError",
      "url": "https://app.example.com/checkout",
      "method": "GET",
      "status": 200,
      "duration_ms": 412.5,
      "value": 2350,
      "message": "error message",
      "stack": "stack trace",
      "attrs": { "any": "flat string or number values" }
    }
  ]
}
```

Bodies may be gzip-compressed (`Content-Encoding: gzip`); the 64 KB limit applies
after decompression.

Ids in `api` names: replace a path segment with `:id` when it is all digits, a
UUID, 16 or more hex characters, or 20 or more characters containing a digit
(e.g. `/api/trips/8412/photos/3f2a9c...` becomes `/api/trips/:id/photos/:id`).

At most 100 events per request. Kinds:

| Kind | name | Fields |
|---|---|---|
| `page_load` | path, e.g. `/checkout` | `duration_ms` (navigation start to load), `attrs`: `ttfb`, `fcp`, `dom_interactive` |
| `web_vital` | `LCP`, `INP`, `CLS`, `FCP` or `TTFB` | `value` (ms, CLS unitless), `url`, `attrs.rating`: `good`/`needs-improvement`/`poor` |
| `screen` | screen or activity name | `duration_ms`: time to first draw (mobile) |
| `api` | `METHOD /path` with ids replaced by `:id` | `url` (no query string), `method`, `status` (0 on network failure), `duration_ms` |
| `error` | error type | `message`, `stack`, `url` |
| `custom` | any | `value`, `duration_ms`, `attrs` |

The server stores one `client_events` row per event and computes `fingerprint`
for errors.

## Environment

| Variable | Default |
|---|---|
| `VISIONARY_DATABASE_URL` | see Databases; may be omitted when the app passes its own connection, or when `D1_*` is set |
| `D1_ACCOUNT_ID`, `D1_DATABASE_ID`, `D1_API_TOKEN`, `D1_API_BASE` | for D1 only |
| `VISIONARY_INSTANCE` | hostname |
| `VISIONARY_LOG_REQUESTS` | `true` |
| `VISIONARY_LOG_PATH_PREFIX` | empty (log everything) |
| `VISIONARY_LOG_BODIES` | `true` |
| `VISIONARY_LOG_MAX_BODY_BYTES` | `8192` |
| `VISIONARY_LOG_RETENTION_DAYS` | `30` |
| `VISIONARY_LOG_REDACT_HEADERS`, `_REDACT_FIELDS`, `_REDACT_REQUEST_FIELDS` | empty (comma lists) |
| `VISIONARY_LOG_ERROR_CODE_FIELD`, `_ERROR_KEY_FIELD` | `error.code`, `error.key` |
| `VISIONARY_SYSTEM_METRICS` | `true` |
| `VISIONARY_SYSTEM_INTERVAL` | `15s` (plain number = seconds; also `500ms`, `1m`) |
| `VISIONARY_SYSTEM_RETENTION_DAYS` | `30` |
| `VISIONARY_SERVICE` | empty (NULL) |
| `VISIONARY_RELEASE` | from the platform's commit variable (see Telemetry), else NULL |
| `VISIONARY_SPANS` | `true` |
| `VISIONARY_SPAN_THRESHOLD_MS` | `100` |
| `VISIONARY_SPAN_MAX` | `100` |
| `VISIONARY_ERRORS` | `true` |

Booleans accept `true/false/1/0/yes/no`. No database configured means
"collection off": log one warning and make every API a no-op, never crash the
host application. A database that is down must not affect requests either:
writes fail on the background worker and are logged.
