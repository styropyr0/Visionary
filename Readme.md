<p align="center"><img src="icon/visionary.svg" alt="Visionary" width="128"></p>

# Visionary

API, server and frontend monitoring for your services, with alerts, uptime
checks, error tracking and an AI analyst that answers *why*.

Everything meets in one SQL database you choose: PostgreSQL, MySQL 8, SQLite
or Cloudflare D1.

```
your services ──(SDKs or OpenTelemetry)──┐
browser and mobile apps ──(ingest API)───┼──▶ database ◀── Visionary server ──▶ browser, Slack, email
                                         ┘
```

**What you get**

- **API overview and Performance:** traffic, errors and latency per route;
  Apdex and SLOs with error budgets; a latency heatmap; each route's share of
  server time; regressions against the previous period; database and outbound
  time per request; slowest queries; N+1 detection; deploy markers on every
  chart and a side-by-side comparison of two versions.
- **System**, at the depth of a node-exporter dashboard: CPU by mode, load per
  core, memory, swap, disk space and I/O (throughput, IOPS, busy, latency),
  network (bytes, packets, errors, TCP), processes, the process itself
  (CPU, memory, I/O, open files), runtime heap and GC, and the database pool.
- **Errors:** exceptions and panics grouped into issues, with stacks, the
  versions they appear in, new and regressed issues, and links to the request.
- **Request logs** with a waterfall of every query and outbound call.
- **Frontend:** Web Vitals, page loads, client-side API latency next to
  server-side latency, JS and mobile crashes, sessions, devices and countries.
- **Uptime:** HTTP checks with timing phases, incidents, certificate expiry,
  and an optional public status page.
- **Alerts:** thresholds, seasonal anomalies, SLO burn rates and silent
  instances, to Slack, Discord, webhooks or email; each alert says what most
  likely caused it. A weekly digest is optional.
- **Explore:** ask a question, get an answer with charts. **Dashboards**
  arrange any panel on a grid, with variables, share links and a TV mode.
- **Team:** viewer, editor and admin roles from your own auth, API keys, and an
  audit log.

## Collect

| Source | Where |
|---|---|
| Go (`net/http`, chi, any router; pgx, `database/sql`) | [`sdks/go`](sdks/go) |
| Python: Django, FastAPI/Starlette, Flask; SQLAlchemy, psycopg, httpx, requests | [`sdks/python`](sdks/python) |
| Java: Spring Boot 3 (JDBC, RestTemplate/RestClient/WebClient) | [`sdks/java`](sdks/java) |
| Node: Express, Fastify, Koa, NestJS; pg, mysql2, SQLite, Prisma | [`sdks/node`](sdks/node) |
| Ruby: Rails, Sinatra, any Rack app | [`sdks/ruby`](sdks/ruby) |
| .NET: ASP.NET Core (EF Core, HttpClient) | [`sdks/dotnet`](sdks/dotnet) |
| Anything with OpenTelemetry | [OTLP/HTTP](docs/otlp.md) at `/v1/traces`, `/v1/metrics`, `/v1/logs` |
| Browsers | [`sdks/browser`](sdks/browser) |
| Android (Kotlin) and iOS (Swift) | [`sdks/android`](sdks/android), [`sdks/ios`](sdks/ios) |

Server SDKs write straight to the database, batched in the background; they
never block a request, and a database that is down never breaks one. They all
follow one contract, [`sdks/SPEC.md`](sdks/SPEC.md), so services in different
languages share the same tables and dashboards. Browser and mobile SDKs post to
Visionary's ingest API instead.

### Go example

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

Who made the request, database and outbound calls, and handled errors:

```go
visionary.SetUser(r.Context(), visionary.User{ID: "42", Handle: "ada", Role: "ADMIN"})

poolCfg.ConnConfig.Tracer = visionary.PgxTracer(poolCfg.ConnConfig.Tracer) // pgx
client := &http.Client{Transport: visionary.Transport(nil)}              // outbound HTTP
visionary.CaptureError(r.Context(), err)                                   // handled errors
```

See [`sdks/go/README.md`](sdks/go/README.md) for `database/sql`, chi route
patterns, user lookup and pool stats. Each SDK's README has its own quickstart.

### SDK settings

Every server SDK reads the same variables.

| Variable | Default | |
|---|---|---|
| `VISIONARY_DATABASE_URL` | — | Where rows go (see [Database](#database)). |
| `D1_ACCOUNT_ID`, `D1_DATABASE_ID`, `D1_API_TOKEN` | — | For D1 instead. The token needs D1 edit access. |
| `VISIONARY_SERVICE` | — | Names the service when several share a database. |
| `VISIONARY_RELEASE` | the platform's commit SHA | The version shown on deploy markers. Railway, Render, Vercel, Heroku and GitHub Actions are detected. |
| `VISIONARY_INSTANCE` | hostname | Labels this server in System. |
| `VISIONARY_LOG_REQUESTS` | `true` | |
| `VISIONARY_LOG_PATH_PREFIX` | all paths | e.g. `/api` |
| `VISIONARY_LOG_BODIES` | `true` | Store request/response bodies (redacted, capped). |
| `VISIONARY_LOG_MAX_BODY_BYTES` | `8192` | |
| `VISIONARY_LOG_RETENTION_DAYS` | `30` | Requests, spans and errors. `0` keeps them forever. |
| `VISIONARY_LOG_REDACT_HEADERS` | | Added to Authorization, Cookie, Set-Cookie, X-Api-Key… |
| `VISIONARY_LOG_REDACT_FIELDS` | | JSON/form keys added to password, token, secret, otp, api_key… |
| `VISIONARY_LOG_REDACT_REQUEST_FIELDS` | | Keys redacted in requests only. |
| `VISIONARY_LOG_ERROR_CODE_FIELD`, `..._ERROR_KEY_FIELD` | `error.code`, `error.key` | Where your error responses keep their codes. |
| `VISIONARY_SPANS` | `true` | Database and outbound call timing. |
| `VISIONARY_SPAN_THRESHOLD_MS` | `100` | Keep individual calls for requests at least this slow (or failed); totals are always kept. |
| `VISIONARY_SPAN_MAX` | `100` | Calls kept per request. |
| `VISIONARY_ERRORS` | `true` | Exception and panic capture. |
| `VISIONARY_SYSTEM_METRICS` | `true` | |
| `VISIONARY_SYSTEM_INTERVAL` | `15s` | |
| `VISIONARY_SYSTEM_RETENTION_DAYS` | `30` | |

## Database

Point the SDKs and the server at the same database with one variable:

| Database | `VISIONARY_DATABASE_URL` |
|---|---|
| PostgreSQL | `postgres://user:pass@host:5432/db?sslmode=require` |
| MySQL 8 | `mysql://user:pass@host:3306/db` |
| SQLite | `sqlite:///var/lib/visionary/analytics.db` (one host only) |
| Cloudflare D1 | leave it unset (or `d1://`) and set `D1_ACCOUNT_ID`, `D1_DATABASE_ID`, `D1_API_TOKEN` |

Tables are created on first start and gain new columns automatically when you
upgrade. The schemas are in [`sdks/go/schema/`](sdks/go/schema). Timestamps are
ISO-8601 UTC text in every database, so all dialects read the same.

The tables can live in your application database. The analyst and saved
panels can only read Visionary's own tables (anything else is rejected before
it reaches the database), but a dedicated database or schema, or a user
granted only those tables, is still the tidy choice.

Long ranges read pre-aggregated rollups (per minute and per hour), so a 30-day
dashboard reads a small fraction of the raw rows: about 10× fewer for
requests and 60× fewer for system metrics. They build themselves in the
background, including history.

## The analyst

Ask "when did latency spike yesterday, and what caused it?". The analyst writes
read-only SQL, runs up to five queries to find the window, compares it with a
normal baseline across routes, errors, users, queries, deploys and server
metrics, then answers in plain language with supporting charts. Each step is
streamed to the browser, and follow-up questions keep the context. Firing
alerts get a short explanation from the same data.

Query results go to the model provider so it can reason about them. Columns
holding IPs, user agents, headers and request/response bodies are masked first.

## Run the server

```sh
docker run -d -p 8090:8090 --env-file .env ghcr.io/<owner>/visionary:latest
```

Or locally: `cd web && npm ci && npm run build && cd .. && go run ./cmd/visionary`.
Every setting is in [`.env.example`](.env.example).

### Sign-in and roles

Set `VISIONARY_AUTH_MODE`:

| Mode | Use when | Needs |
|---|---|---|
| `otp` | Your API already signs users in with a one-time code (SMS or email). | Endpoints and field paths for your API. |
| `password` | Your API has a username/email + password login. | The same, without the code step. |
| `shared` | You just want a team password. | `VISIONARY_SHARED_PASSWORD` (12+ characters); optionally `VISIONARY_VIEWER_PASSWORD` for read-only access. |
| `none` | Local development. | Nothing; never expose it. |

With `otp` and `password`, Visionary calls your API's own endpoints, so nobody
needs a new account. `VISIONARY_ROLE_MAP` maps your API's roles to Visionary's
(`ADMIN=admin,ENGINEER=editor,SUPPORT=viewer`); `VISIONARY_ALLOWED_ROLES` on its
own makes those roles admins. Anyone else's session is logged out straight
away, and roles are re-checked every few minutes through the profile endpoint.

| Role | Can |
|---|---|
| viewer | Read every dashboard, use Explore, run read-only queries. |
| editor | Also edit saved panels, dashboards, alert rules and uptime checks. |
| admin | Also manage API keys, notification channels and role mapping, and read the audit log. |

Everything about the sign-in flow is an environment variable:

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

### Keys

Admins create keys in Settings (only a hash is stored; the key is shown once):

- **read**: use Visionary's HTTP API as a viewer, with `Authorization: Bearer vis_…`.
- **server**: OpenTelemetry ingestion. `VISIONARY_INGEST_KEYS` also works.
- **client**: browser and mobile apps; public by nature, so combine it with
  `VISIONARY_CLIENT_ORIGINS`. `VISIONARY_CLIENT_KEYS` also works.

### Server settings

The essentials (database, session secret, sign-in, OpenAI key) are in
`.env.example`. Everything else has a sensible default:

| Variable | Default | |
|---|---|---|
| `VISIONARY_APDEX_MS`, `VISIONARY_SLO_TARGET` | `500`, `99` | Apdex threshold and the default SLO. |
| `VISIONARY_ROLLUPS` | `true` | Pre-aggregation for long ranges. |
| `VISIONARY_ROLLUP_MINUTE_DAYS`, `_HOUR_DAYS` | `14`, `400` | Rollup retention. |
| `VISIONARY_SMTP_HOST`, `_PORT`, `_USER`, `_PASSWORD`, `_FROM` | — | Email alerts (STARTTLS). |
| `VISIONARY_DIGEST_TZ` | server zone | Time zone of weekly digests. |
| `VISIONARY_UPTIME_RETENTION_DAYS` | `90` | |
| `VISIONARY_UPTIME_ALLOW_PRIVATE` | `false` | Let checks reach private and loopback addresses. |
| `VISIONARY_STATUS_PAGE`, `VISIONARY_STATUS_TITLE` | off, the app name | Public status page at `/status` (and `/status.json`). |
| `VISIONARY_CLIENT_ORIGINS` | any | Browser origins allowed to send (wildcards like `https://*.example.com`). |
| `VISIONARY_CLIENT_RATE_PER_IP`, `_PER_KEY` | `120`, `6000` per minute | Client ingest limits. |
| `VISIONARY_CLIENT_RETENTION_DAYS` | `30` | |
| `VISIONARY_AUDIT_RETENTION_DAYS` | `365` | |

## Deploy

`.github/workflows/image.yml` checks the build and pushes
`ghcr.io/<owner>/visionary` for `linux/amd64` and `linux/arm64`: `latest` from
the default branch, `sha-<short>`, and semver tags from `v*` git tags.

Run it behind HTTPS and set `VISIONARY_TRUST_PROXY=true`. The image is
distroless and runs as non-root. `GET /api/healthz` is the health check.

Run one Visionary server per database for now: alert rules, uptime checks and
rollups run inside the server, and a second replica would repeat them.

## Security

- **Read-only queries.** Every query that isn't built in (analyst, edited SQL,
  saved panels, dashboard variables) must be a single `SELECT`/`WITH` over
  Visionary's own tables. Other tables, schema-qualified names, comments,
  statement separators, backslashes, write/DDL keywords and server functions
  (`pg_*`, `sleep`, `load_file`…) are rejected. On PostgreSQL and MySQL the
  query then runs in a read-only transaction with a 30s timeout; on SQLite with
  `query_only` on. D1 has no read-only mode, so there the check is the guard.
- **Sessions** are AES-GCM encrypted, HttpOnly, SameSite=Strict cookies. The
  API token inside never reaches the page. Sign-in is rate-limited per client.
  Every change is checked against the user's role and written to the audit log.
- **Logs are redacted** before they are stored; SQL is stored with its values
  replaced by `?`. Add your own redactions with `VISIONARY_LOG_REDACT_*`.
- **Ingest endpoints** need a key, cap body sizes (compressed and
  decompressed) and rate-limit per IP and per key.
- **Uptime checks** cannot reach private, loopback or cloud metadata addresses,
  including through DNS or redirects, unless you allow it.
- **Secrets** (webhook URLs, SMTP passwords, check headers) are masked in the
  API and never logged.

## Cost

On D1, which bills by rows read, panels show their rows read under Inspect.
Rollups keep long ranges cheap; a short range reads raw rows, so keep
auto-refresh for short ranges.
