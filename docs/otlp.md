# OpenTelemetry (OTLP/HTTP)

Visionary accepts OpenTelemetry traces, metrics and logs over OTLP/HTTP, so any
service with an OpenTelemetry SDK can report to it without a Visionary SDK. The
data lands in the same tables the SDKs write (`api_logs`, `spans`, `errors`,
`releases`, `system_metrics`), so the dashboards and the analyst work
unchanged.

## Sending data

Point the exporter at the Visionary server and pass a server key (Settings →
API keys, or one listed in Settings → Server → Ingest):

```sh
OTEL_EXPORTER_OTLP_ENDPOINT=https://visionary.example.com
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf        # or http/json
OTEL_EXPORTER_OTLP_HEADERS="authorization=Bearer <key>"   # or x-visionary-key=<key>
OTEL_SERVICE_NAME=checkout
OTEL_RESOURCE_ATTRIBUTES="service.version=1.4.2,service.instance.id=checkout-1"
```

| Endpoint | Body |
|---|---|
| `POST /v1/traces` | `ExportTraceServiceRequest` |
| `POST /v1/metrics` | `ExportMetricsServiceRequest` |
| `POST /v1/logs` | `ExportLogsServiceRequest` |

- `Content-Type: application/x-protobuf` or `application/json` (the OTLP JSON
  encoding, with hex trace and span ids).
- `Content-Encoding: gzip` is supported. Bodies over 8 MB (on the wire or
  after decompression) get `413`.
- Answers: `200` with an `Export*ServiceResponse` (with `partial_success` when
  items without valid ids or timestamps were rejected), `400` for a body that
  does not decode, `401` for a missing or wrong key, `415` for another content
  type or encoding, `429` (with `Retry-After`) beyond 8 concurrent exports or
  about 50 exports per second, `503` when the database cannot be written.
  Error bodies are a `google.rpc.Status` in the request's encoding. OTLP
  exporters retry `429` and `503` and drop the rest, as the specification
  says.
- OTLP/gRPC is not served; use the HTTP exporters.

Settings → Server → Ingest sets, for OTLP data, whether spans and errors are
kept, the span threshold outside requests and the spans kept per request (the
server-side `VISIONARY_SPANS`, `VISIONARY_SPAN_THRESHOLD_MS`,
`VISIONARY_SPAN_MAX` and `VISIONARY_ERRORS`, see `sdks/SPEC.md`), and the
export rate limit (per second, default 50). They apply after a restart.

## Mapping

### Resource

| Visionary | Resource attribute |
|---|---|
| `service` | `service.name` |
| `version` | `service.version` |
| `instance` | `service.instance.id`, else `host.name`, else `host.id`, else the service name |
| `runtime` (errors, releases) | `process.runtime.name` + `process.runtime.version` |
| `sdk_version` (releases) | `telemetry.sdk.language` + `telemetry.sdk.version` |

### Requests (`api_logs`)

A span becomes a request when it is `SERVER` kind, or a local root span (no
parent) of `INTERNAL`/unspecified kind with `http.request.method` or
`http.method`. Every other span belongs to its nearest request ancestor.

| Column | From |
|---|---|
| `request_id` | `<trace id>-<span id>` (hex) |
| `created_at`, `duration_ms` | span start; end - start |
| `method` | `http.request.method` / `http.method`; `RPC` for `rpc.system` spans; else `SERVER` |
| `path` | `url.path`, else `http.target` without the query, else the path of `url.full` / `http.url`; for RPC `/<rpc.service>/<rpc.method>`; else the span name |
| `route` | `http.route`; for RPC `/<rpc.service>/<rpc.method>` |
| `query` | `url.query`, else the query part of `http.target` |
| `status` | `http.response.status_code` / `http.status_code`; `rpc.grpc.status_code` mapped as gRPC-gateway does (0 → 200, 5 → 404, 14 → 503, ...); else 500 when the span status is ERROR, 200 otherwise |
| `ip` | `client.address`, `http.client_ip`, `network.peer.address`, `net.sock.peer.addr`, `net.peer.ip` |
| `user_agent` | `user_agent.original` / `http.user_agent` |
| `user_id`, `user_handle`, `user_role` | `enduser.id` / `user.id`; `user.name` / `enduser.name`; `enduser.role` / `user.roles` |
| `request_headers` | `http.request.header.*` attributes as a JSON object; `Authorization`, `Cookie`, `Set-Cookie`, `X-Api-Key`, `X-Auth-Token`, `X-Visionary-Key` redacted |
| `request_content_type`, `response_content_type` | `http.request.header.content-type`, `http.response.header.content-type` |
| `request_size`, `response_size` | `http.request.body.size` / `http.request_content_length`; `http.response.body.size` / `http.response_content_length`; else 0 |
| `service`, `version` | resource |
| `db_count`, `db_ms`, `http_count`, `http_ms` | the request's `db` and `http` spans (below), counted and summed; 0 when there were none, NULL with `VISIONARY_SPANS=false` |

Bodies, `error_code` and `error_key` are not part of OpenTelemetry and stay
NULL.

### Calls (`spans`)

| Span | `kind` | `name` | `target` | `detail` |
|---|---|---|---|---|
| has `db.system` / `db.system.name` | `db` (`cache` for redis, memcached) | `db.operation(.name)` + `db.collection.name` / `db.sql.table`, else first keyword + main table of the statement, e.g. `SELECT users` | `<system>/<db.namespace or db.name>` | `db.query.text` / `db.statement`, sanitized as in the SDK contract |
| `CLIENT` with an HTTP method | `http` | `METHOD host` | `server.address` / `net.peer.name` / the URL's host | method and URL without query |
| has `messaging.system` | `queue` | operation + destination | messaging system | |
| anything else | `custom` | span name | | |

`status` is `error` when the span status is ERROR (and for `http` on a 5xx);
`error` is the status message or the exception message; `row_count` comes from
`db.response.returned_rows` / `db.rows_affected`. Span rows link to their
request's `request_id` with `offset_ms` from the request start, and follow the
contract's rules per request: stored for every request, at most
`VISIONARY_SPAN_MAX` in start order, while the totals count every call. Spans
outside any request are stored when they themselves take at least
`VISIONARY_SPAN_THRESHOLD_MS`, with `request_id` NULL.

Exporters send spans as they end, so children usually arrive before their
request span and sometimes in an earlier export. Spans whose request has not
arrived wait in memory for up to 30 s; spans that arrive after their request
was written (up to 2 minutes later) still become span rows (the request's
totals are not updated then). After 30 s a span with a missing parent is
treated as background work.

### Exceptions (`errors`)

Every span event named `exception` becomes an `errors` row: `type` from
`exception.type`, `message` from `exception.message`, `stack` from
`exception.stacktrace`, linked to the span's request (`request_id`, `method`,
`route`, `path`, `user_id`). `handled` is 0 when `exception.escaped` is true or
the exception ended a request span with ERROR status, else 1. `fingerprint`
follows the SDK contract; frames are read from Python, Java/Kotlin, .NET,
Node.js, Go and Ruby stack traces (innermost first, application frames
preferred over standard library and installed packages). At most 20 rows per
fingerprint per minute are kept.

### Logs

Only log records with severity ERROR or above that carry `exception.*`
attributes are stored, as `errors` rows with `handled = 1` (the record body is
the message when `exception.message` is missing). They link to the request of
the span that was active (a log record that arrives before its span waits for
it up to 30 s). An exception recorded both as a span event and as a log record
on the same span is stored once. All other log records are accepted and
dropped: Visionary is not a log store.

### Releases

The first time a `service.version` is seen (per service), a `releases` row is
written with `sdk = 'otlp'`, at the earliest timestamp of that export, unless
the database already has that version.

### Metrics (`system_metrics`)

Datapoints of one instance (and service) are gathered into 15 s windows (by
datapoint time); each window becomes one row 20 s after its first datapoint
arrived. Within a window the last value of each series counts; cumulative
counters become per-second rates (or increases, for counts) against the
previous datapoint of the series. Columns nothing maps to stay NULL, and so do
columns whose metric was not sent. `interval_s` is the time since the
instance's previous row.

| Column | Metric |
|---|---|
| `host_cpu_percent`, `cpu_user_percent`, `cpu_system_percent`, `cpu_iowait_percent`, `cpu_steal_percent`, `cpu_count` | `system.cpu.utilization` by state and CPU (or the rate of `system.cpu.time`); `system.cpu.logical.count` |
| `load_1/5/15` | `system.cpu.load_average.1m/5m/15m` |
| `host_mem_used_mb`, `host_mem_total_mb`, `host_mem_percent`, `host_mem_available_mb`, `host_mem_cached_mb` | `system.memory.usage` by state (total from `system.memory.limit` or the sum of used, free, cached and buffers); `system.memory.utilization` (used) |
| `swap_used_mb`, `swap_total_mb` | `system.paging.usage` (or `system.swap.usage`) |
| `disk_used_percent`, `disk_free_gb`, `disk_total_gb` | `system.filesystem.usage` of `/`, else the largest filesystem |
| `disk_read/write_kbps`, `disk_read/write_iops` | rates of `system.disk.io`, `system.disk.operations`, whole disks only (the contract's partition rule) |
| `net_rx/tx_kbps`, `net_rx/tx_pps`, `net_errors`, `net_drops` | `system.network.io`, `system.network.packets`, `system.network.errors`, `system.network.dropped` (or `dropped_packets`) |
| `host_procs`, `procs_running`, `procs_blocked` | `system.processes.count` (or `system.process.count`) by status |
| `proc_cpu_percent` | rate of `process.cpu.time` (cores in use × 100), else `process.cpu.utilization` × CPUs × 100 |
| `proc_rss_mb`, `proc_threads`, `proc_open_fds`, `uptime_s` | `process.memory.usage`, `process.thread.count`, `process.open_file_descriptor.count`, `process.uptime` |
| `goroutines` | `go.goroutine.count`, else `jvm.thread.count`, else the process threads |
| `heap_alloc_mb`, `heap_inuse_mb` | `jvm.memory.used` / `jvm.memory.committed` (heap) |
| `gc_count` | increase of the runtime's GC count |

The older `process.runtime.<runtime>.*` names (`cpu_time`, `memory`,
`thread_count`, `gc_count`, `cpu.utilization`, Go's `goroutines` and
`mem.heap_alloc`) are read when the current name is not sent. HTTP server
metrics such as `http.server.request.duration` are ignored: requests come from
spans.

## Storage

Each export is written as multi-row INSERTs: on Cloudflare D1 as one `batch`
call (string params with `NULLIF(?, '')`, at most 100 params per statement and
50 statements per call, more calls for very large exports), on SQLite,
PostgreSQL and MySQL in one transaction with typed params. A failed write
answers `503`, so the exporter retries the whole export; nothing of it was
stored. Spans waiting for their request and metric windows are flushed by a
background loop every 2 s; a failure there is logged and the rows dropped.

The in-memory state (waiting spans, recent requests, counter values, metric
windows) is bounded and per server process. Running several Visionary
replicas behind a load balancer works, but a trace split across replicas
loses the parent-child links that span exports.
