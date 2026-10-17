# Changelog

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
