# Changelog

## Unreleased

- Requests on the Settings → Data skip list (`requests.skip`) are handled
  like paths outside `VISIONARY_LOG_PATH_PREFIX`: no `api_logs` row or spans.

- Faster writes: SQL backends get each table's rows as multi-row INSERTs
  (500 rows a batch, `batch_size` up to 2000), with up to four batches in
  flight on PostgreSQL and MySQL. SQLite opens in WAL mode with a 5 s busy
  timeout. D1 is unchanged. `Visionary.writer_stats` reports rows queued,
  written and dropped.

- Custom metrics in `metrics`: `Visionary.counter(name, **labels).add(n)`,
  `Visionary.gauge(...).set(v)`, `Visionary.histogram(...).observe(v)` and
  `Visionary.describe_metric(name, unit:, buckets:)`. Added up in memory and
  written one row per series every interval by a writer thread of their own;
  at most 1000 label sets per name. `VISIONARY_METRICS`,
  `VISIONARY_METRICS_INTERVAL`, `VISIONARY_METRICS_MAX_SERIES`.
- Distributed tracing: requests continue an incoming W3C `traceparent` or
  start a trace, `Net::HTTP` sends `traceparent` (and `tracestate`) on, and
  `api_logs` and `spans` get `trace_id`, `span_id` and `parent_span_id`
  (added to existing tables at startup). Calls inside `Visionary.span` and
  `Visionary.background` nest under them; log lines carry the current span.
- Application logs in `app_logs`: `Visionary.logger(inner = nil)` (a Logger
  that forwards every call to `inner` unchanged and also captures) and
  `Visionary.capture_rails_logger` (broadcasts `Rails.logger`). Lines link to
  their request (`request_id`, `user_id`, `trace_id` from `traceparent`) and
  record where they were logged. What is kept follows Settings → Logs: level,
  overrides by route or user, drop rules and a per-minute cap; lower lines are
  held in the request and kept when it fails or is slow. Secrets are scrubbed
  from messages and attrs. Written by a writer thread of its own.
  `VISIONARY_LOGS`, `VISIONARY_LOG_LEVEL`.

## 0.1.0

First release of the Ruby SDK (`visionary-sdk`, `require "visionary"`).

- Rack middleware for any Rack app, and a Railtie for Rails: one `api_logs`
  row per request with route patterns (Rails, Sinatra, Grape), redacted
  headers and bodies, error codes, users (`Visionary.set_user`) and per-request
  database and HTTP call totals.
- Spans from `sql.active_record` and `Net::HTTP`, plus `Visionary.span`;
  errors from exceptions escaping the app or rendered as 5xx, plus
  `Visionary.capture_exception`; a `releases` row per process start.
- System metrics every 15 s, with the node-level columns from `/proc` on Linux.
- Storage: PostgreSQL (`pg`), MySQL (`mysql2`), SQLite (`sqlite3`), Cloudflare
  D1 over HTTPS, or the app's ActiveRecord pool. Older tables gain new columns
  at startup.
- Background threads restart in forked workers (Puma, Unicorn) through
  `Process._fork`.
