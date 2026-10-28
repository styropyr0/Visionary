# Changelog

## Unreleased

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
