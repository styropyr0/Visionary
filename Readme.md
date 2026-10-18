<p align="center"><img src="icon/visionary.svg" alt="Visionary" width="128"></p>

# Visionary

[![Image](https://github.com/styropyr0/Visionary/actions/workflows/image.yml/badge.svg)](https://github.com/styropyr0/Visionary/actions/workflows/image.yml)
[![Android SDK](https://github.com/styropyr0/Visionary/actions/workflows/android.yml/badge.svg)](https://github.com/styropyr0/Visionary/actions/workflows/android.yml)
[![JitPack](https://jitpack.io/v/styropyr0/Visionary.svg)](https://jitpack.io/#styropyr0/Visionary)
[![Latest tag](https://img.shields.io/github/v/tag/styropyr0/Visionary?sort=semver&label=release)](https://github.com/styropyr0/Visionary/tags)
[![Go Reference](https://pkg.go.dev/badge/github.com/styropyr0/visionary/sdks/go.svg)](https://pkg.go.dev/github.com/styropyr0/visionary/sdks/go)
[![Go version](https://img.shields.io/github/go-mod/go-version/styropyr0/Visionary)](go.mod)
[![Go Report Card](https://goreportcard.com/badge/github.com/styropyr0/visionary)](https://goreportcard.com/report/github.com/styropyr0/visionary)
[![Image on GHCR](https://img.shields.io/badge/image-ghcr.io%2Fstyropyr0%2Fvisionary-2496ED?logo=docker&logoColor=white)](https://github.com/styropyr0/Visionary/pkgs/container/visionary)
[![Last commit](https://img.shields.io/github/last-commit/styropyr0/Visionary)](https://github.com/styropyr0/Visionary/commits/main)
[![Stars](https://img.shields.io/github/stars/styropyr0/Visionary?style=flat)](https://github.com/styropyr0/Visionary/stargazers)

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

Set `VISIONARY_AUTH_MODE` to one of four modes:

| Mode | Use when | Steps at sign-in |
|---|---|---|
| `shared` | You just want a team password. | One: the password. |
| `password` | Your API already has an email/username + password login. | One: identifier and password, sent to your login endpoint. |
| `otp` | Your API signs users in with a one-time code (SMS or email). | Two: request a code, then enter it. |
| `none` | Local development only. | None. Never expose it. |

With `password` and `otp`, Visionary calls your API's own endpoints, so nobody
needs a new account and your API decides who someone is. Visionary decides who
may come in, from the role your API returns:

| Role | Can |
|---|---|
| viewer | Read every dashboard, use Explore, run read-only queries. |
| editor | Also edit saved panels, dashboards, alert rules and uptime checks. |
| admin | Also manage API keys, notification channels and role mapping, and read the audit log. |

- `VISIONARY_ALLOWED_ROLES=ADMIN,OPS`: users with those roles come in as admins.
- `VISIONARY_ROLE_MAP=ADMIN=admin,ENGINEER=editor,SUPPORT=viewer`: map each of
  your roles to a Visionary role. Anyone whose role isn't listed is turned away.
- `*` matches every user of your API: `VISIONARY_ALLOWED_ROLES=*` lets everyone
  in as admin, and `VISIONARY_ROLE_MAP=ADMIN=admin,*=viewer` makes your admins
  admins and everyone else a viewer. With only `*`, `VISIONARY_AUTH_FIELD_ROLE`
  is optional.

Roles are re-checked every few minutes through the profile endpoint, so
someone who loses a role is signed out.

Copy the preset for your mode into `.env` and change the values for your API.
Each line says what it does. Field paths are dotted paths into your API's JSON
responses: `data.token` reads `{"data": {"token": "..."}}`, and `user.roles.0`
reads the first item of an array.

#### `shared`: one team password

```sh
VISIONARY_AUTH_MODE=shared
# The password everyone types, 12+ characters. Signs in as admin.
VISIONARY_SHARED_PASSWORD=change-me-to-something-long
# Optional: a second password that signs in as a read-only viewer.
# VISIONARY_VIEWER_PASSWORD=another-long-password
```

#### `password`: your API's email + password login (one step)

```sh
VISIONARY_AUTH_MODE=password
# Shown on the sign-in screen: "Use your Acme account".
VISIONARY_AUTH_PROVIDER=Acme
# Your API's base URL; endpoint paths below are relative to it.
VISIONARY_AUTH_BASE_URL=https://api.example.com

# The sign-in form's first field.
VISIONARY_AUTH_IDENTIFIER_LABEL=Email
# HTML input type: email, tel or text.
VISIONARY_AUTH_IDENTIFIER_INPUT=email
# Applied before sending: lowercase, digits or trim.
VISIONARY_AUTH_IDENTIFIER_NORMALIZE=lowercase

# Your login endpoint, called once with what the user typed.
# {{identifier}} and {{password}} are replaced with the form values.
VISIONARY_AUTH_LOGIN=POST /auth/login
VISIONARY_AUTH_LOGIN_BODY={"email":"{{identifier}}","password":"{{password}}"}

# Where the login response keeps the token (required).
VISIONARY_AUTH_FIELD_TOKEN=token
# Where it keeps the user's role: a string or an array of strings.
VISIONARY_AUTH_FIELD_ROLE=role
# Optional user details, read from the profile response (or the login
# response when there is no profile endpoint).
VISIONARY_AUTH_FIELD_NAME=name
VISIONARY_AUTH_FIELD_HANDLE=email
# VISIONARY_AUTH_FIELD_USER_ID=id
# Token expiry, if the response has one.
# VISIONARY_AUTH_FIELD_EXPIRES_AT=expires_at
# Where error responses keep a message to show the user.
# VISIONARY_AUTH_FIELD_ERROR=message

# Optional: returns the signed-in user, called with the token. Lets Visionary
# re-check roles while someone is signed in.
VISIONARY_AUTH_PROFILE=GET /me
# Optional: called with the token when someone signs out.
VISIONARY_AUTH_LOGOUT=POST /auth/logout

# Who may sign in (see roles above). * lets every user in.
VISIONARY_ALLOWED_ROLES=admin
# VISIONARY_ROLE_MAP=admin=admin,engineer=editor,*=viewer
```

#### `otp`: your API's one-time code login (two steps)

```sh
VISIONARY_AUTH_MODE=otp
VISIONARY_AUTH_PROVIDER=Acme
VISIONARY_AUTH_BASE_URL=https://api.example.com

# The sign-in form's first field, checked against the pattern before any call.
VISIONARY_AUTH_IDENTIFIER_LABEL=Phone number
VISIONARY_AUTH_IDENTIFIER_INPUT=tel
VISIONARY_AUTH_IDENTIFIER_NORMALIZE=digits
VISIONARY_AUTH_IDENTIFIER_PATTERN=^[0-9]{8,15}$
VISIONARY_AUTH_IDENTIFIER_HINT=Enter your phone number as digits.
# Digits in the code (4-10, default 6), and seconds before "resend" (default 30).
VISIONARY_AUTH_CODE_LENGTH=6
# VISIONARY_AUTH_RESEND_SECONDS=30

# Step 1: your endpoint that sends the code.
VISIONARY_AUTH_START=POST /auth/otp/request
VISIONARY_AUTH_START_BODY={"phone":"{{identifier}}"}
# Optional: where its response says the code went ("+91 ****1234").
# VISIONARY_AUTH_FIELD_SENT_TO=masked_phone

# Step 2: your endpoint that checks the code and returns a token.
VISIONARY_AUTH_VERIFY=POST /auth/otp/verify
VISIONARY_AUTH_VERIFY_BODY={"phone":"{{identifier}}","code":"{{code}}"}

# Response fields and optional endpoints, as in password mode.
VISIONARY_AUTH_FIELD_TOKEN=data.token
VISIONARY_AUTH_FIELD_ROLE=data.role
VISIONARY_AUTH_FIELD_NAME=data.name
VISIONARY_AUTH_PROFILE=GET /me
VISIONARY_AUTH_LOGOUT=POST /auth/logout

VISIONARY_ROLE_MAP=ADMIN=admin,ENGINEER=editor,SUPPORT=viewer
```

#### `none`: no sign-in

```sh
# Everyone is an admin. Local development only; never expose it.
VISIONARY_AUTH_MODE=none
```

#### Settings for either API mode

```sh
# Headers sent on every call to your API, e.g. an API key it requires.
# VISIONARY_AUTH_HEADERS={"X-Api-Key":"..."}
# Headers for one endpoint only: VISIONARY_AUTH_LOGIN_HEADERS, VISIONARY_AUTH_PROFILE_HEADERS, ...
# How later calls send the token (default: Authorization: Bearer <token>).
# VISIONARY_AUTH_TOKEN_HEADER=Authorization
# VISIONARY_AUTH_TOKEN_PREFIX="Bearer "
# Timeout per call to your API (default 15s).
# VISIONARY_AUTH_TIMEOUT=15s
# If your API wants the device's position at sign-in: none, optional or required.
# The browser asks for it, and bodies can use "latitude":"{{latitude}}","longitude":"{{longitude}}".
# VISIONARY_AUTH_LOCATION=none
# Turn off an optional endpoint a config file set: VISIONARY_AUTH_PROFILE=off
```

The same settings can live in a JSON file (`VISIONARY_AUTH_CONFIG`, see
`examples/auth/`); env vars override the file. Older password configs that set
the login endpoint as `VISIONARY_AUTH_VERIFY` keep working.

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
