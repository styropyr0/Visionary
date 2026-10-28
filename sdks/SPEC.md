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
| `login_account` | From `login_attempt` / `loginAttempt` (see below), trimmed and clipped to 255; empty means NULL. |
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

A second call records the account a sign-in request tried (the username or
email typed, whether or not it exists or the password was right), for the
security detectors:

- Go: `visionary.LoginAttempt(ctx, account)`
- Python: `visionary.login_attempt(account)`
- Java: `Visionary.loginAttempt(request, account)`

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
- Retention is the Visionary server's job: it deletes old rows of every
  table by Settings → Retention. An SDK deletes rows itself only when
  `VISIONARY_LOG_RETENTION_DAYS` is set above 0 (default 0, off), for a
  service writing to a database no Visionary server reads. Then, 60 s after
  start and every 24 h, it deletes rows older than that in chunks:
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
	service, version, db_count, db_ms, http_count, http_ms, login_account
) VALUES (31 placeholders)
```

The last seven columns were added later (`AddedRequestColumns` in
`sdks/go/migrate.go`); add them to old tables at startup exactly as described for
`system_metrics` below. `service` and `version` are the service name and release
(see Telemetry); `db_count`/`db_ms` and `http_count`/`http_ms` are the number and
total duration of database and outbound HTTP calls the request made (0 when it
made none, NULL when the SDK cannot see that kind of call); `login_account` is
the account a sign-in tried.

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
measurements `REAL` / `DOUBLE PRECISION` / `DOUBLE`, text `TEXT` / `TEXT` /
`VARCHAR(255)`. Ignore "duplicate column"
errors (another instance got there first). `AddedSystemColumns` in
`sdks/go/migrate.go` is the canonical list and insert order.
Counts are integers, measurements floats, `created_at`, `instance`, `service`,
`version`, `sdk` and `runtime` text.

| Column | Meaning | Python | Java |
|---|---|---|---|
| `created_at` | sample time, same format as above | | |
| `instance` | `VISIONARY_INSTANCE` or hostname | | |
| `interval_s` | seconds since the previous sample (2 decimals) | | |
| `host_cpu_percent` | whole-machine CPU % since the previous sample | `psutil.cpu_percent(None)` | `/proc/stat` on Linux, else `OperatingSystemMXBean.getCpuLoad()*100` |
| `load_1/5/15` | load average | `os.getloadavg()` | `getSystemLoadAverage()` (load_1 only) |
| `host_mem_used_mb/total_mb/percent` | host memory | `psutil.virtual_memory()` | `/proc/meminfo` on Linux, else `getTotalMemorySize()/getFreeMemorySize()` |
| `disk_used_percent`, `disk_free_gb` | the working directory's filesystem | `psutil.disk_usage` | `File.getTotalSpace()/getUsableSpace()` |
| `net_rx_kbps`, `net_tx_kbps` | host network KB/s since previous sample | `psutil.net_io_counters()` | `/proc/net/dev` on Linux, else NULL |
| `proc_cpu_percent` | this process, % of one core | `Process.cpu_percent(None)` | `getProcessCpuLoad()*100*cores` |
| `proc_rss_mb` | resident memory | `memory_info().rss` | NULL unless available |
| `proc_threads` | OS threads | `num_threads()` | `ThreadMXBean.getThreadCount()` |
| `proc_open_fds` | open files/handles | `num_fds()`/`num_handles()` | `UnixOperatingSystemMXBean.getOpenFileDescriptorCount()` |
| `goroutines` | concurrency units: goroutines in Go, live threads elsewhere | `threading.active_count()` | `ThreadMXBean.getThreadCount()` |
| `heap_alloc_mb`, `heap_inuse_mb` | runtime heap used / committed | `tracemalloc` traced size in both while tracing, else NULL | `MemoryMXBean` heap used / committed |
| `heap_objects` | live heap objects | `len(gc.get_objects())` is too slow: NULL | NULL |
| `gc_count` | collections since previous sample | `gc.get_stats()` collections delta | sum of `GarbageCollectorMXBean.getCollectionCount()` deltas |
| `gc_pause_ms` | GC time since previous sample | measured with `gc.callbacks` | `getCollectionTime()` delta |
| `gc_cpu_percent` | GC time as % of the interval | derived | derived |
| `db_*` | pool stats from an optional user callback; `db_acquires`, `db_empty_acquires`, `db_acquire_wait_ms` are per-sample deltas of cumulative counters; a counter the pool doesn't have is NULL (Go `SQLDB`: `db_acquires`) | callback, else Django 5.1+ psycopg pools (`OPTIONS["pool"]`) when present | HikariCP `HikariPoolMXBean` when present |
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
| `service`, `version` | service name and release (see Telemetry) | | |
| `sdk` | the SDK that wrote the row: `go`, `python`, `java`, `node`, `ruby` or `dotnet` (as `releases.sdk`) | `python` | `java` |
| `runtime` | language runtime and version, as `releases.runtime` | `_errors.runtime()`, e.g. `CPython 3.13.1` | `Origin.javaRuntime()`, e.g. `OpenJDK 21.0.4` |
| `container_cpu_cores` | CPU used by the process's own cgroup, in cores, since the previous sample (3 decimals) | see Container below | same |
| `container_cpu_limit` | the cgroup's CPU limit in cores; NULL when unlimited (2 decimals) | | |
| `container_cpu_throttled_percent` | throttled CFS periods / periods since the previous sample × 100; 0 when there were no periods and a CPU limit is set, else NULL | | |
| `container_mem_used_mb` | cgroup memory in use minus its inactive page cache, as `docker stats` counts it | | |
| `container_mem_limit_mb` | the cgroup's memory limit; NULL when unlimited | | |
| `container_oom_kills` | OOM kills in the cgroup since the previous sample (integer) | | |

Container (the `container_*` columns, **Linux only**, else NULL): read the
process's own cgroup, so a service in a container reports its container's
use against its real limits (the `host_*` columns describe the machine under
it). Fill them only when the process runs in a container (`/.dockerenv` or
`/run/.containerenv` exists, a container platform's variable is set
(`KUBERNETES_SERVICE_HOST`, `RAILWAY_SERVICE_ID` for Railway,
`RENDER_SERVICE_ID` for Render, `DYNO` for Heroku, `K_SERVICE` for Cloud Run,
`ECS_CONTAINER_METADATA_URI_V4` for ECS/Fargate, `CONTAINER_APP_NAME` for
Azure Container Apps), or
`/proc/self/cgroup` mentions `docker`, `kubepods`, `containerd`, `libpod` or
`lxc`) or when its cgroup sets a CPU or memory limit; otherwise leave all six
NULL. Take a baseline at start like the other deltas. `sdks/go/cgroup.go` is
the reference.

- Find the cgroup in `/proc/self/cgroup`. A `0::<path>` line is cgroup v2:
  the directory is `/sys/fs/cgroup/<path>`, or `/sys/fs/cgroup` itself when
  that does not exist (inside a cgroup namespace the path is `/`). Otherwise
  (v1) the `memory`, `cpu` and `cpuacct` lines name each controller's path
  under `/sys/fs/cgroup/<controllers>/`, with the same fallback to the mount.
- v2: CPU limit from `cpu.max` (`max 100000` is unlimited, else quota /
  period); CPU time `usage_usec` (× 1000 for ns), `nr_periods` and
  `nr_throttled` from `cpu.stat`; memory `memory.current`, limit `memory.max`
  (`max` is unlimited), page cache `inactive_file` from `memory.stat`; OOM
  kills `oom_kill` from `memory.events`.
- v1: CPU limit `cpu.cfs_quota_us` / `cpu.cfs_period_us` (quota -1 is
  unlimited); CPU time `cpuacct.usage` (ns); `nr_periods`, `nr_throttled` from
  `cpu.stat`; memory `memory.usage_in_bytes`, limit `memory.limit_in_bytes`
  (2^62 or more is unlimited), page cache `total_inactive_file` from
  `memory.stat`; OOM kills `oom_kill` from `memory.oom_control` (newer kernels
  only, else NULL).
- A counter that went down (the cgroup was reset) gives NULL for that sample.

Partition rule: skip disk devices matching
`^((sd|vd|xvd|hd)[a-z]+\d+|nvme\d+n\d+p\d+|mmcblk\d+p\d+|loop\d+|ram\d+|zram\d+|dm-\d+|sr\d+|fd\d+|md\d+)$`,
so a partition's I/O is not counted again on top of its disk.

Retention: the server's (Settings → Retention); with
`VISIONARY_SYSTEM_RETENTION_DAYS` above 0 (default 0, off) the SDK also
deletes on the same schedule as requests,
`DELETE FROM system_metrics WHERE id IN (SELECT id FROM system_metrics WHERE created_at < ? LIMIT 5000)`.

## Telemetry (`spans`, `errors`, `releases`)

Created at startup from `sdks/go/schema/<dialect>/telemetry.sql` (copy it like
the other schemas). Written through the same writer as requests: batched, never
blocking a request, dropped (and counted) when the queue is full. Retention:
the server's (Settings → Retention); with `VISIONARY_LOG_RETENTION_DAYS` set,
`spans` and `errors` follow it with the same chunked delete; `releases` are
kept.

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
- **Span rows** are stored for every logged request, at most
  `VISIONARY_SPAN_MAX` (default 100) per request, in start order; calls beyond
  the cap still count in the totals. Spans outside any request are stored when
  they themselves take at least `VISIONARY_SPAN_THRESHOLD_MS` (default 100),
  with `request_id` NULL.
- `VISIONARY_SPANS=false` turns span capture off (totals and rows).

Columns:

| Column | Value |
|---|---|
| `created_at` | span start, same format as everywhere |
| `request_id` | the parent request's `request_id`, NULL outside a request |
| `service`, `instance` | as above |
| `kind` | `db`, `http`, `cache`, `queue`, `custom`, `graphql` or `background` |
| `name` | short and low-cardinality, used for grouping: `db`: first keyword + main table, e.g. `SELECT users`, `INSERT bookings`, `UPDATE trips`, `BEGIN`; `http`: `METHOD host`, e.g. `POST api.stripe.com`; `graphql`: operation type and name, e.g. `query GetUser`; others: the operation |
| `target` | `db`: system and database, e.g. `postgresql/voygram`; `http`: host |
| `detail` | `db`: the statement, sanitized (below); `http`: method and URL without query string; `graphql`: the operation document with literal values removed; at most 10000 characters, so a statement can still be planned with EXPLAIN |
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

#### Work a request starts in the background

Work a handler starts and does not wait for (a goroutine, a thread, a task,
a job queued in-process) keeps belonging to its request:

- Calls made in it carry the request's `request_id` and `offset_ms`, from the
  request's start, even when they start or end after the response. Those that
  end after the request was logged are not counted in its totals, and are
  stored whatever their duration, up to `VISIONARY_SPAN_MAX` of them per
  request (in addition to the spans stored with the request); further ones are
  stored only when they take at least `VISIONARY_SPAN_THRESHOLD_MS`. An
  `offset_ms + duration_ms` past the request's `duration_ms` is how the
  dashboard tells they ran after the response.
- Each SDK offers a helper that starts the work and records it as one span of
  kind `background`, named by the caller ("send welcome email"), from when
  the work started to when it finished, failed with its error. The calls made
  inside it are its own spans as usual. The helper passes the request's
  context on but not its cancellation, so the work is not cut short when the
  response is sent:
  - Go: `visionary.Go(ctx, name, func(ctx context.Context) error)`
  - Node: `visionary.background(name, async () => ...)` (returns the promise)
  - Python: `visionary.background(name, fn, *args)` (starts a thread) and
    `with visionary.background_span(name):` for executors, Celery eager tasks
  - Ruby: `Visionary.background(name) { ... }` (starts a thread)
  - Java: `Visionary.background(name, runnable)` and
    `Visionary.wrap(name, runnable)` for an executor
  - .NET: `Visionary.Background(name, async ct => ...)` (a task)

`background` spans are not counted in `db_*`/`http_*` totals, and the
dashboard leaves them out of a request's untracked time, since the request
does not wait for them.

GraphQL operations are spans of kind `graphql`, recorded around execution:
`detail` is the document with string and number literals replaced by `?`.
The dashboard measures depth and field count from it, so SDKs need no GraphQL
library.

### Query plans (`explain_requests`, optional)

The Visionary server creates `explain_requests (id, created_at, target,
statement, status, claimed_by, plan, error, finished_at)` and inserts a row
with `status = 'pending'` when someone asks for a statement's plan. An SDK
given an explainer for a database (matched by `target`, e.g.
`postgresql/shop`) polls about every 10 seconds:

1. `SELECT id, target, statement FROM explain_requests WHERE status = 'pending' ORDER BY id LIMIT 5`
   (a missing table means the server has not started yet: skip quietly).
2. Claim one: `UPDATE ... SET status = 'running', claimed_by = <instance> WHERE id = ? AND status = 'pending'`;
   go on only when exactly one row changed.
3. Run `EXPLAIN` on the application's database (never `EXPLAIN ANALYZE`), off
   the request path and unrecorded as a span. Accept only one statement
   starting with `SELECT`, `WITH`, `INSERT`, `UPDATE`, `DELETE`, `VALUES` or
   `TABLE`. PostgreSQL: `EXPLAIN (GENERIC_PLAN)` with each `?` numbered after
   the highest `$n`; then with the removed values as `NULL`; then plain
   `EXPLAIN` with every value `NULL`. MySQL: `EXPLAIN` with `NULL`s. SQLite:
   `EXPLAIN QUERY PLAN`. Typed literals stored as `INTERVAL ?`, `DATE ?`, ...
   get a constant of their type. A plan made with `NULL`s starts with a `--`
   note saying so.
4. Write back `status = 'done'` with `plan` (text, one line per plan line), or
   `'failed'` with `error` (500 characters), and `finished_at`.

`VISIONARY_EXPLAIN=false` turns it off.

### CPU profiles (`profiles`, every server SDK)

Profiling is **off until someone turns it on in the dashboard** (Settings →
Profiling), and can be turned off there again at any time; no redeploy is
needed either way. The Visionary server keeps the switch in
`visionary_settings (name, value, updated_at)` (it creates the table; values
are text):

| `name` | `value` | Missing means |
|---|---|---|
| `profile.enabled` | `true` or `false` | `false` |
| `profile.services` | JSON array of service names | every service |
| `profile.interval_s` | seconds between profiles | `300` |
| `profile.duration_ms` | how long each profile samples | `10000` |
| `profile.now` | a timestamp (`created_at` format), set by "Profile now" | no request |

The SDK reads `SELECT name, value FROM visionary_settings WHERE name LIKE
'profile.%'` at start and then every 30 seconds, off the request path (a
missing table or a failed read means "off", logged once at most). It takes
part when its `VISIONARY_SERVICE` is in `profile.services` (or the list is
empty or missing; a service with no name matches only an empty list).
`VISIONARY_PROFILE=false` in a service makes it ignore the dashboard and
never profile: the switch for code that must never be sampled.

When enabled it samples the CPU for `duration_ms` every `interval_s`,
skipping a turn when no request arrived since the last one, and dropping a
profile that has fewer than 20 samples. A profile is kept whether or not a
request finished during it (`routes` is then `{}`): quiet services and
background work are profiled too. A "Profile now" profile is kept with any
samples at all.
When `profile.now` changes to a value newer than the SDK's start and less
than 5 minutes old, the SDK takes one profile at once, whether or not
profiling is enabled (still honouring `profile.services` and
`VISIONARY_PROFILE=false`), and then goes back to the schedule. Only one
profile runs at a time per process; if the runtime's profiler is already in
use (Go's `pprof`), skip the turn quietly.

Sample about every 10 ms (Node: 1 ms) and count only threads that are using
the CPU: idle, sleeping and blocked threads are left out, and so are the
SDK's own threads. Sampling must cost little (a few percent of one CPU while
it runs, nothing in between) and must never fail a request.

One row per profile. Columns: `created_at` (when sampling started),
`service`, `instance`, `version`, `sdk` (`go`, `node`, `python`, `ruby`,
`java`, `dotnet`), `duration_ms` (measured), `samples` (counted samples),
`routes` (JSON: `"METHOD route"` to the milliseconds its requests that
finished during the profile took, each at least 1) and `folded`:

- one `frame;frame;frame count` line per distinct stack, outermost caller
  first, the sampled function last, heaviest lines first (ties in text
  order), at most 90 000 bytes (UTF-8) in all. When they do not fit, keep
  the time where it was spent rather than lose it:
  1. keep whole lines, heaviest first, while the text stays within 72 000
     bytes;
  2. cut each remaining stack after the deepest frame at which some kept
     stack has the same frames so far (same first frame, same second, ...),
     and end it with an `(other)` frame: `a;b;c;d;e 3` becomes `a;b;(other) 3`
     when a kept line starts `a;b;`; sum the counts of stacks cut the same way;
  3. add those lines, heaviest first, while the text stays within 90 000
     bytes;
  4. sum everything left, and stacks that share no first frame with a kept
     one, into a last `(other) n` line;
- each frame reads `function file`, and the last (sampled) frame
  `function file:line` (`function` alone when there is no source location),
  with `;` in names replaced by `,`. Callers carry no line: the same function
  called from two lines is one frame, which keeps stacks few and the flame
  graph readable. (Node writes the line where each function starts on every
  frame, which is the same thing.);
- `file` says whose code it is, which the flame graph colours by:
  - the application's own code: the path relative to the working directory
    (or the project root);
  - third-party code: `lib:<package>/<file>`, e.g.
    `lib:github.com/jackc/pgx/v5/conn.go`, `lib:django/db/models/query.py`,
    `lib:rails/actionpack/metal.rb`, `lib:spring-webmvc/DispatcherServlet.java`,
    `lib:Npgsql/NpgsqlCommand.cs` (Node writes `npm:<package>/<file>`);
  - the language's standard library and runtime: `std:<path>`, e.g.
    `std:net/http/server.go`, `std:json/decoder.py`, `std:java/util/HashMap.java`
    (Node writes `node:...`);
  - native code, garbage collection and frames without a file: no file.

The SDK creates no table: the server creates `profiles` and
`visionary_settings`. Before the server has started, reads fail (profiling
stays off) and nothing is written. Profiles follow Settings → Retention, and
`VISIONARY_LOG_RETENTION_DAYS` when it is set.

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

## Application logs (`app_logs`)

Lines the application logs through its usual logger, linked to the request
whose context they were logged in. Created at startup from
`sdks/go/schema/<dialect>/logs.sql` (copy it like the other schemas).
`VISIONARY_LOGS=false` turns capture off. The Go SDK is the reference
(`sdks/go/logs.go`, `logs_slog.go`).

### Hooks

Capture starts only where the application routes its logs to the SDK; it
never changes what the application prints:

| SDK | Hook |
|---|---|
| Go | `visionary.LogHandler(next slog.Handler)` (forwards to `next` unchanged), `agent.CaptureStdLog()` (tees the `log` package) |
| Node | `visionary.captureConsole()` (wraps `console.*`, still prints), `visionary.pinoStream()` / `visionary.winstonTransport()` |
| Python | `visionary.LogHandler` (a `logging.Handler`), `visionary.capture_logging(level=None)` adds it to the root logger |
| Ruby | `Visionary.logger(inner = nil)` (a `Logger` that also forwards), `Visionary.capture_rails_logger` (broadcasts `Rails.logger`) |
| Java | `io.github.styropyr0.visionary.VisionaryLogbackAppender`, declared in `logback-spring.xml`, or added to the root logger by the Spring Boot starter when `VISIONARY_LOG_CAPTURE=true` |
| .NET | `builder.Logging.AddVisionary()` (an `ILoggerProvider`); `services.AddVisionary()` alone captures nothing |

No SDK captures logs until the application opts in like this, so upgrading
an SDK never starts writing lines nobody asked for.

### Columns

Insert, columns in this order (same placeholder rules as requests):

```sql
INSERT INTO app_logs (
	created_at, service, instance, version, source, host, container, level, severity, logger,
	message, attrs, request_id, trace_id, span_id, user_id, session_id, code_file, code_line,
	code_func, size
) VALUES (21 placeholders)
```

| Column | Value |
|---|---|
| `created_at` | when the line was logged, same format as everywhere |
| `service`, `instance`, `version` | as on every row |
| `source` | the SDK: `go`, `node`, `python`, `ruby`, `java`, `dotnet` |
| `host` | the machine's hostname; `container` NULL (the agent fills it for container output) |
| `level` | `trace`, `debug`, `info`, `warn`, `error` or `fatal` |
| `severity` | 1 trace, 2 debug, 3 info, 4 warn, 5 error, 6 fatal (integer) |
| `logger` | logger/category name (Python `record.name`, Java logger name, .NET category), or an attribute named `logger`; NULL when none |
| `message` | the formatted message, secrets scrubbed (below), at most 8 KB (UTF-8, cut on a character boundary) |
| `attrs` | the line's structured fields as one flat JSON object: nested groups become dotted keys (`user.id`); values are strings, numbers or booleans (anything else as its string form; times RFC 3339); secrets redacted; at most 16 KB: when larger, cut each string value to 1024 bytes plus `…`, and if still larger keep keys in sorted order while they fit, with `"_truncated": true`. NULL when there are none |
| `request_id` | the request's `request_id` when logged inside a logged request (or an attribute `request_id`) |
| `trace_id`, `span_id` | the W3C trace id from the request's `traceparent` header (`00-<32 hex>-<16 hex>-<2 hex>`, all-zero ids are none), or the active OpenTelemetry span, or attributes `trace_id`/`span_id` |
| `user_id` | the request's user (`set_user`), or attribute `user_id` |
| `session_id` | attribute `session_id` |
| `code_file`, `code_line`, `code_func` | where the line was logged, when the logger records it cheaply (Go slog PC, Python `pathname`/`lineno`/`funcName`, Logback caller data when enabled, Ruby `caller_locations(…, 1)`); `code_file` is named like CPU profile frames (application files relative to the project, `lib:`/`std:` otherwise). NULL otherwise |
| `size` | bytes of `message` plus `attrs` as stored |

The attributes `request_id`, `trace_id`, `span_id`, `user_id`,
`session_id` and `logger` move to their columns and are not repeated in
`attrs`. `pattern` is left NULL: the Visionary server fills it.

Level mapping: names as `trace trc finest finer verbose` → trace,
`debug dbg fine config` → debug, `info inf information informational notice log` → info,
`warn warning wrn` → warn, `error err severe` → error,
`fatal critical crit alert emerg emergency panic` → fatal. Numeric levels:
Go slog `< -4` trace, `< 0` debug, `< 4` info, `< 8` warn, `< 12` error,
else fatal; Python `< 10` trace, `< 20` debug, `< 30` info, `< 40` warn,
`< 50` error, else fatal.

Plain text with no level of its own (`console.log`, the Go `log` package,
`puts`) is info unless it states one at its start: a `level=`/`lvl=`/`severity=`
field in the first 200 bytes, or one of the first four whitespace-separated
words (after words that are only a date or time, `^[\[(]?[0-9][0-9/:.,\-TZ+]*[\])]?$`)
is a level name that is upper case, or bracketed (`[warn]`, `<error>`), or
followed by `:` or `|`. A bare lower-case word is not a level
(`error rate is low` is info). When the line starts with the upper-case
level word followed by text (`INFO started`, `WARNING: disk`), that word is
removed from the message.

### Secrets

Before writing, values of attributes whose last dotted key is one of the
redacted field names (the request redaction defaults plus
`VISIONARY_LOG_REDACT_FIELDS`, case-insensitive) become `"[REDACTED]"`, and
these patterns are replaced in the message and in every string attribute,
in this order:

| Pattern | Replacement |
|---|---|
| `-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(-----END [A-Z ]*PRIVATE KEY-----\|$)` | `[REDACTED:private-key]` |
| `\beyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}` | `[REDACTED:jwt]` |
| `(?i)\b(bearer\|basic)\s+[A-Za-z0-9._~+/=-]{8,}` | `$1 [REDACTED]` |
| `\b(AKIA\|ASIA)[0-9A-Z]{16}\b` | `[REDACTED:aws-key]` |
| `(?i)\b(password\|passwd\|pwd\|secret\|token\|api[_-]?key\|access[_-]?token\|refresh[_-]?token\|client[_-]?secret)\b(["']?\s*[=:]\s*)("[^"]*"\|'[^']*'\|[^\s,;&"']+)` | `$1$2[REDACTED]` |

The SDK's own messages (starting `visionary: `) are never captured, so a
failed write is not queued behind itself.

### What is kept (Settings → Logs)

The dashboard keeps these in `visionary_settings`; the SDK reads
`SELECT name, value FROM visionary_settings WHERE name LIKE 'logs.%'` at
start and every 30 seconds, off the request path, keeping its previous
values (at first the defaults) when the read fails:

| `name` | `value` | Missing means |
|---|---|---|
| `logs.enabled` | `true`/`false` | `true` |
| `logs.level` | the lowest level kept | `VISIONARY_LOG_LEVEL`, else `info` |
| `logs.buffer` | keep a failed or slow request's lower lines | `true` |
| `logs.buffer_level` | the lowest level the buffer keeps | `debug` |
| `logs.slow_ms` | a request at least this slow counts as slow | `1000` |
| `logs.rate` | lines per minute per process, 0 for no cap | `6000` |
| `logs.overrides` | JSON `[{"service","route","user","level","until"}]` | none |
| `logs.drop` | JSON `[{"service","logger","contains","level"}]` | none |

- **Overrides** change the level for part of the traffic, until `until`
  (a timestamp; empty is no end; expired ones are ignored). Empty fields
  match anything; `service` must equal `VISIONARY_SERVICE`; `route` is a
  route pattern, or a prefix ending in `*`; `user` is a `user_id`. For a
  line, the level is `logs.level`, replaced by the last matching override
  with neither route nor user, then by the last matching one with a route,
  then by the last matching one with a user (most specific wins).
- **Drop rules** drop matching lines: every non-empty field must match
  (`logger` equal, `contains` a substring of the message, `level` drops
  lines at or below it).
- **Inside a request**, a line is written at once when its level is at least
  the highest level any service-wide override or route/user override could
  give this request (`logs.level` when there are none). Otherwise it is held
  in the request (at most 500 lines; past that the oldest are dropped). When
  the request ends, each held line is written when it reaches the level for
  the request's final route and user, or, when `logs.buffer` is on and the
  request failed (status ≥ 500, a panic/exception, or an error captured in
  it) or took at least `logs.slow_ms`, when it reaches `logs.buffer_level`.
  The rest are dropped. Lines logged by work the request left running, after
  it ended, are decided at once with its route and user.
- **Outside requests**, a line is kept when it reaches the level (overrides
  without route and user apply).
- **Rate:** count lines per wall-clock minute per process. Past `logs.rate`,
  lines below error are dropped; error and fatal are dropped only past ten
  times the rate. When a minute had drops, write one `warn` line with
  `logger` = `visionary`: `visionary: N log lines dropped over the cap of R a
  minute (Settings → Logs)`.

### Writer

A separate background worker from the requests writer, so a flood of lines
never crowds out request rows: the same batching (50 rows or 3 s, retries),
with a queue of 10000 lines, dropped and counted when full. With
`VISIONARY_LOG_RETENTION_DAYS` set it also purges `app_logs` the same way.

## Client ingest API (browser and mobile)

Browser and mobile SDKs do not write to the database; they post to the
Visionary server, which writes `client_events`.

`POST /api/ingest/client`, `Content-Type: application/json` (or
`text/plain`, for `navigator.sendBeacon`). The key is a public client key
(a client key created in Settings → API keys, or listed under Settings →
Server → Ingest → Client keys), sent as
`Authorization: Bearer <key>` or, where headers cannot be set, as `?key=<key>`.
Browsers are also checked against Settings → Server → Ingest → Allowed web
origins (comma list; empty allows any origin). The server answers `204`, `400` for a bad payload,
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
| `VISIONARY_LOG_RETENTION_DAYS` | `0` (off: the server applies Settings → Retention) |
| `VISIONARY_LOG_REDACT_HEADERS`, `_REDACT_FIELDS`, `_REDACT_REQUEST_FIELDS` | empty (comma lists) |
| `VISIONARY_LOG_ERROR_CODE_FIELD`, `_ERROR_KEY_FIELD` | `error.code`, `error.key` |
| `VISIONARY_SYSTEM_METRICS` | `true` |
| `VISIONARY_SYSTEM_INTERVAL` | `15s` (plain number = seconds; also `500ms`, `1m`) |
| `VISIONARY_SYSTEM_RETENTION_DAYS` | `0` (off: the server applies Settings → Retention) |
| `VISIONARY_SERVICE` | empty (NULL) |
| `VISIONARY_RELEASE` | from the platform's commit variable (see Telemetry), else NULL |
| `VISIONARY_SPANS` | `true` |
| `VISIONARY_SPAN_THRESHOLD_MS` | `100` |
| `VISIONARY_SPAN_MAX` | `100` |
| `VISIONARY_ERRORS` | `true` |
| `VISIONARY_EXPLAIN` | `true` (only with an explainer given in code) |
| `VISIONARY_PROFILE` | follow the dashboard; `false` never profiles |
| `VISIONARY_LOGS` | `true` (capture where the app routes its logs; see Application logs) |
| `VISIONARY_LOG_LEVEL` | `info` (until Settings → Logs is read) |

Booleans accept `true/false/1/0/yes/no`. No database configured means
"collection off": log one warning and make every API a no-op, never crash the
host application. A database that is down must not affect requests either:
writes fail on the background worker and are logged.
