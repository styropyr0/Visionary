<p align="center"><img src="icon/visionary.svg" alt="Visionary" width="128"></p>

# Visionary

API and server analytics for your web service, with an AI analyst that answers *why*.

Visionary has two parts that meet in one SQL database: PostgreSQL, MySQL 8,
SQLite or Cloudflare D1.

```
your API ──(SDK middleware + recorder)──▶ database: api_logs, system_metrics ◀── Visionary server ──▶ browser
```

- **An SDK** in your service logs every HTTP request and samples the server
  every 15s, at the depth of a node-exporter dashboard: CPU by mode (user,
  system, iowait, steal), load per core, memory, cache and swap, disk space,
  inodes, throughput, IOPS, busy time and latency, network bytes, packets,
  errors, drops, TCP connections and retransmits, processes and context
  switches; the process CPU, memory, I/O and open files against the limit;
  runtime heap, GC and threads; database pool use and waits; and throughput.
  Writes are batched in the background and never block a request. Tables from
  older SDK versions gain new columns automatically at startup. SDKs:

  | Stack | Where |
  |---|---|
  | Go (`net/http`, chi, any router) | [`sdks/go`](sdks/go) |
  | Python: Django, FastAPI/Starlette (ASGI), Flask | [`sdks/python`](sdks/python) |
  | Java: Spring Boot 3 | [`sdks/java`](sdks/java) |

  Every SDK follows the same contract, [`sdks/SPEC.md`](sdks/SPEC.md), so
  services in different languages can share one database and one dashboard.
- **The server** (`cmd/visionary`) serves the dashboards: API overview;
  Performance (Apdex and SLO with error budget, a latency heatmap, percentiles,
  per-route p95 and share of server time, regressions against the previous
  period, saturation, slowest requests); System (CPU, memory, disk, network,
  process and database pool, Grafana-style); Explore (the analyst); Saved
  panels; and Request logs. It signs people in
  through your existing API, a shared team password, or not at all.

## The analyst

Ask "when did latency spike yesterday, and what caused it?". The analyst writes
read-only SQL, runs up to five queries to find the window, compares it with a
normal baseline across routes, errors, users and server metrics, then answers
in plain language with supporting charts. Each step is streamed to the browser
as it happens, and follow-up questions keep the context.

Query results go to the model provider so it can reason about them. Columns
holding IPs, user agents, headers and request/response bodies are masked first.

## Database

Point the SDKs and the server at the same database with one variable:

| Database | `VISIONARY_DATABASE_URL` |
|---|---|
| PostgreSQL | `postgres://user:pass@host:5432/db?sslmode=require` |
| MySQL 8 | `mysql://user:pass@host:3306/db` |
| SQLite | `sqlite:///var/lib/visionary/analytics.db` (one host only) |
| Cloudflare D1 | leave it unset (or `d1://`) and set `D1_ACCOUNT_ID`, `D1_DATABASE_ID`, `D1_API_TOKEN` |

Tables are created on first start: `api_logs`, `system_metrics` and
`analytics_panels`. The schemas for each database are in
[`sdks/go/schema/`](sdks/go/schema). Timestamps are stored as ISO-8601 UTC text in every
database, so all dialects read the same.

The tables can live in your application database. The server only ever reads
its own three tables (anything else is rejected before it reaches the
database), but a dedicated database or schema, or a database user granted only
those tables, is still the tidy choice.

<a id="collect-go"></a>
## Collect: add the SDK to a Go service

```go
import (
	_ "github.com/jackc/pgx/v5/stdlib" // for postgres:// (or go-sql-driver/mysql, modernc.org/sqlite)
	visionary "github.com/styropyr0/visionary/sdks/go"
)

agent, err := visionary.New(visionary.FromEnv())
if err != nil {
	log.Print(err) // collection is off; a nil agent is a no-op
}
agent.Start(ctx)
defer agent.Close(context.Background())

http.ListenAndServe(":8080", agent.Middleware(mux))
```

Tell it who made each request from your auth middleware, and it fills in the
user columns:

```go
visionary.SetUser(r.Context(), visionary.User{ID: "42", Handle: "ada", Role: "ADMIN"})
```

Optional hooks:

```go
cfg := visionary.FromEnv()
// Reuse a pool the app already has instead of VISIONARY_DATABASE_URL:
cfg.Database, cfg.Dialect = db, visionary.Postgres // or visionary.MySQL, visionary.SQLite
// Route patterns for routers other than Go 1.22's ServeMux (detected on its own), e.g. chi:
cfg.Requests.Route = func(r *http.Request) string { return chi.RouteContext(r.Context()).RoutePattern() }
// Fill in handle and role from a cache when only the ID is known:
cfg.Requests.LookupUser = func(ctx context.Context, id string) (visionary.User, bool) { ... }
// Database pool stats: visionary.SQLDB(db) for database/sql, or adapt pgxpool:
cfg.System.DB = func() visionary.DBStats { s := pool.Stat(); return visionary.DBStats{ /* ... */ } }
```

### SDK environment

| Variable | Default | |
|---|---|---|
| `VISIONARY_DATABASE_URL` | — | Where rows go (see [Database](#database)). Import the matching driver. |
| `D1_ACCOUNT_ID`, `D1_DATABASE_ID`, `D1_API_TOKEN` | — | For D1 instead. The token needs D1 edit access. |
| `VISIONARY_INSTANCE` | hostname | Labels this server in System. |
| `VISIONARY_LOG_REQUESTS` | `true` | |
| `VISIONARY_LOG_PATH_PREFIX` | all paths | e.g. `/api` |
| `VISIONARY_LOG_BODIES` | `true` | Store request/response bodies (redacted, capped). |
| `VISIONARY_LOG_MAX_BODY_BYTES` | `8192` | |
| `VISIONARY_LOG_RETENTION_DAYS` | `30` | `0` keeps rows forever. |
| `VISIONARY_LOG_REDACT_HEADERS` | | Added to Authorization, Cookie, Set-Cookie, X-Api-Key… |
| `VISIONARY_LOG_REDACT_FIELDS` | | JSON/form keys added to password, token, secret, otp, api_key… |
| `VISIONARY_LOG_REDACT_REQUEST_FIELDS` | | Keys redacted in requests only. |
| `VISIONARY_LOG_ERROR_CODE_FIELD`, `..._ERROR_KEY_FIELD` | `error.code`, `error.key` | Where your error responses keep their codes. |
| `VISIONARY_SYSTEM_METRICS` | `true` | |
| `VISIONARY_SYSTEM_INTERVAL` | `15s` | |
| `VISIONARY_SYSTEM_RETENTION_DAYS` | `30` | |

The Python and Java SDKs read the same variables.

## Run the server

```sh
docker run -d -p 8090:8090 --env-file .env ghcr.io/<owner>/visionary:latest
```

Or locally: `cd web && npm ci && npm run build && cd .. && go run ./cmd/visionary`.
Every setting is listed in [`.env.example`](.env.example). The server creates the
tables it needs at startup, so dashboards load before any service reports in.

### Sign-in modes

Set `VISIONARY_AUTH_MODE`:

| Mode | Use when | Needs |
|---|---|---|
| `otp` | Your API already signs users in with a one-time code (SMS or email). | Endpoints and field paths for your API. |
| `password` | Your API has a username/email + password login. | The same, without the code step. |
| `shared` | You just want a team password. | `VISIONARY_SHARED_PASSWORD` (12+ characters). |
| `none` | Local development. | Nothing; never expose it. |

With `otp` and `password`, Visionary calls your API's own endpoints, so nobody
needs a new account. Only users whose role is in `VISIONARY_ALLOWED_ROLES` get
in; anyone else's freshly issued session is logged out straight away. When a
profile endpoint is set, the role is re-checked every few minutes, so removing
someone's role or revoking their session takes effect mid-session.

Everything about the flow is an environment variable:

- endpoints: `VISIONARY_AUTH_START`, `_VERIFY`, `_PROFILE`, `_LOGOUT` as
  `"POST /path"`, with JSON bodies in `..._BODY` using `{{identifier}}`,
  `{{code}}`, `{{password}}`, `{{latitude}}`, `{{longitude}}`
- response paths: `VISIONARY_AUTH_FIELD_TOKEN`, `_ROLE`, `_NAME`, `_HANDLE`,
  `_USER_ID`, `_EXPIRES_AT`, `_SENT_TO`, `_ERROR` (dotted, e.g. `data.token`)
- the form: `VISIONARY_AUTH_IDENTIFIER_LABEL`, `_INPUT` (`tel`/`email`/`text`),
  `_NORMALIZE`, `_PATTERN`, `_HINT`, `VISIONARY_AUTH_CODE_LENGTH`
- extras: `VISIONARY_AUTH_HEADERS` (JSON, sent on every call, e.g. an API key),
  `VISIONARY_AUTH_TOKEN_HEADER`/`_PREFIX`, and `VISIONARY_AUTH_LOCATION`
  (`none`/`optional`/`required`) for APIs that expect the device's position.

The same settings can live in a JSON file (`VISIONARY_AUTH_CONFIG`, see
`examples/auth/`); env vars override the file.

## Deploy

`.github/workflows/image.yml` checks the build and pushes
`ghcr.io/<owner>/visionary` for `linux/amd64` and `linux/arm64`: `latest` from
the default branch, `sha-<short>`, and semver tags from `v*` git tags.

Run it behind HTTPS and set `VISIONARY_TRUST_PROXY=true`. The image is
distroless and runs as non-root. `GET /api/healthz` is the health check.

## Security

- **Read-only queries.** Every query that isn't built in (analyst, edited
  SQL, saved panels) must be a single `SELECT`/`WITH` over `api_logs`,
  `system_metrics` or `analytics_panels`. Other tables, schema-qualified names,
  comments, statement separators, backslashes, write/DDL keywords and server
  functions (`pg_*`, `sleep`, `load_file`…) are rejected. On PostgreSQL and
  MySQL the query then runs in a read-only transaction with a 30s timeout; on
  SQLite with `query_only` on. D1 has no read-only mode, so there the check is
  the guard.
- **Sessions** are AES-GCM encrypted, HttpOnly, SameSite=Strict cookies. The
  API token inside never reaches the page. Sign-in is rate-limited per client.
- **Logs are redacted** before they are stored. Defaults cover common
  credentials; add your own with the `VISIONARY_LOG_REDACT_*` variables.

## Cost

On D1, which bills by rows read, panels show their rows read under Inspect. A 24h
overview over moderate traffic reads tens of thousands of rows; 30-day ranges
read far more. Keep auto-refresh for short ranges.
