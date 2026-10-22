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
  In a container it also shows the service's own CPU and memory against the
  container's limits, throttling and OOM kills. With the
  [host agent](#host-agent) it adds the machine's details, every drive, the
  top processes and failed systemd services.
- **Containers:** every Docker or Podman container on the machines the host
  agent watches: state, health, restarts, CPU and memory against each
  container's limits, network and disk, and OOM kills, crashes and failed
  health checks. A service on a container platform (Railway, Render,
  Kubernetes, Cloud Run, ECS, Azure Container Apps) is listed on its own, no
  agent needed: its SDK reports the container's CPU and memory against its
  limits, throttling and OOM kills.
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

## Integrate in three steps

Each step works on its own; stop wherever the detail is enough. Times are for
a typical API in one service.

**1. Requests, errors and the server (about 15 minutes).** Add your
language's SDK, wrap the router in its middleware and set
`VISIONARY_DATABASE_URL` (or the [D1 variables](#sdk-settings)). Then
[run the server](#run-the-server) and point its sign-in at your API.

- You get every request with route, status, duration, user and redacted
  bodies; panics and handled errors grouped into issues; CPU, memory, GC and
  the database pool; Performance, Apdex and SLOs; uptime checks; alerts;
  security findings.
- Worth the extra line: attach the signed-in user (`SetUser`), and with a
  router that has route patterns (chi, Express, Django…) report the pattern,
  so `/users/42` and `/users/43` count as one route.

**2. Database and outbound calls (another 15–30 minutes).** One line where
the database pool is created, and the HTTP hook. Each request's breakdown
then shows its queries and outbound calls on a timeline, and Performance gets
slowest queries, outbound calls by host and N+1 detection.

- The HTTP hook differs by language: Node patches `http` and `fetch` itself;
  Python needs `visionary.instrument_http()`; Go needs `visionary.Transport`
  on every `http.Client` you build (or uses `http.DefaultClient`, which is
  instrumented for you). Search your code for each place a client is made.

**3. No blind spots (as long as it takes).** The middleware cannot see what
it is not handed, so some time inside a request stays **Untracked**: calls
made by vendor SDKs that build their own HTTP client (AWS S3 or R2, OpenAI,
Stripe, Firebase), calls made without the request's context, Redis and queue
clients, and your own CPU work. Each is a small code change, listed in
[What the middleware sees](#what-the-middleware-sees-and-what-needs-code),
with copy-paste examples in each SDK's README.

**Check your coverage.** Open Performance → Slowest requests and open a slow
request:

- *Untracked* above a few percent: the hatched row sits where the time went
  ("1.2 s after `SELECT attributed`"); the code that runs next makes a call
  the SDK cannot see. Hook its client or wrap it in a custom span.
- *Database* or *Outbound HTTP* missing from Calls: that hook is not
  installed (the totals are empty, not zero).
- An outbound call you know happens is missing: it was made without the
  request's context, or through a client without the hook.

**Things that trip people up**

- **Your own rate limiting.** A load test from one machine mostly measures
  your rate limiter: requests rejected with 429 crowd out the rest. Test from
  several addresses, or raise the limit while testing.
- **Traffic spikes on SQLite or D1.** The SDK writes in the background and
  never slows a request; when writes cannot keep up it drops rows and logs
  `visionary: queue full; rows dropped`. For thousands of requests a second,
  use PostgreSQL or MySQL.
- **`.env` comments.** Lines must start with `#`. A line starting with `;`
  makes the whole file unreadable, and the server starts as if no settings
  were given ("choose a sign-in mode").
- **Calls in goroutines or background jobs** after the response is written
  are stored on their own, not in the request.

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
| Any Linux VM, no app needed | The [host agent](#host-agent), [`cmd/visionary-agent`](cmd/visionary-agent) |

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

### What the middleware sees, and what needs code

The middleware alone records every request: route, status, duration, user,
bodies and errors. It cannot see *inside* the request. Each call the request
makes is recorded only when the client making it is hooked:

| Inside a request | Recorded by |
|---|---|
| Database queries | The SDK's hook for your driver (pgx tracer, wrapped `database/sql`, ORM integration). One line where the pool is created. |
| Outbound HTTP through your own clients | The SDK's HTTP hook. Node patches `http`/`fetch` itself; in Go, every `http.Client` needs `visionary.Transport` (or use `http.DefaultClient`). |
| Calls made by a vendor SDK (AWS S3 or R2, OpenAI, Stripe, Firebase…) | Nothing, until you hand that SDK a traced HTTP client. They build their own clients, so the hook above never sees them. |
| Cache, queue and RPC clients (Redis, SQS, gRPC) | A custom span around the call, or the client's own hook calling one. |
| Your own code: hashing, image work, JSON of a big response, loops | A custom span, if you want it named. |

Two more rules: a call joins its request only when it carries the request's
context (Go: pass `r.Context()` down, use `http.NewRequestWithContext`; a bare
`http.Post` has none), and work started in a goroutine or thread that outlives
the response is stored on its own, not in the request.

**Untracked time.** A request's breakdown lays its calls on a timeline. Any
stretch that no recorded call covers is shown as *Untracked*: the request was
busy, but not in anything the SDK could see. Overlapping calls count once, so
untracked = duration − the time covered by at least one call. A large untracked
share means one of the rows above is missing, and the hatched row's position
says where: "1.2 s after `SELECT attributed`" points at the code that runs next.
Hook that client or wrap that code, and the row gets a name.

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
| `VISIONARY_SPAN_THRESHOLD_MS` | `100` | Keep calls made outside any request (jobs, startup) at least this slow; every request keeps its calls. |
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

## Host agent

The agent is not the Visionary server: it is a helper you install on a machine
to report it to your server ([`cmd/visionary-agent`](cmd/visionary-agent)).
The SDKs see the host an app runs on. The agent watches the whole machine,
with or without an app on it (a database box, a worker, a bastion, the VM your
containers run on). It posts to the server over HTTPS with a **server** ingest
key (Settings → API keys, or `VISIONARY_INGEST_KEYS`) and never needs database
credentials. What it reports shows up in three places:

- **System:** every 15 s it samples CPU, memory, swap, disks, network and load
  (the same `system_metrics` columns the SDKs write, with `sdk = 'agent'` and
  the machine's name as the instance, so the charts and alert rules such as
  "Instance stopped reporting" cover it too). Once a minute it adds the
  machine's details (OS, kernel, architecture, CPUs, memory, boot time,
  address), every real mount with space and inodes used, the top 15 processes
  by CPU and by memory (command lines clipped, obvious secrets such as
  `--password=...` and tokens masked), and failed systemd services plus those
  in `VISIONARY_AGENT_UNITS`.
- **Containers:** with Docker or Podman on the machine, every container: state,
  health, restarts, last exit code, limits and published ports once a minute,
  and every 15 s each running container's CPU (against its limit), throttling,
  memory (against its limit, without page cache, as `docker stats` counts it),
  network, disk and process count. Between reports it records restarts, OOM
  kills, exits and health checks that start or stop failing. The agent finds
  `/var/run/docker.sock` or Podman's socket on its own (or `DOCKER_HOST`).
- **Security** (Linux): failed and successful SSH logins and sudo from
  `/var/log/auth.log`, `/var/log/secure` or journald; listening ports opened or
  closed; pending package updates every 6 hours (apt, dnf, yum, apk); reboots.

While the server is unreachable it keeps up to an hour of reports in memory and
retries with backoff.

It runs on the machine itself as a systemd service, as root, so it can read
other users' processes, the auth log and the container engine's socket:

```sh
curl -fsSL https://raw.githubusercontent.com/styropyr0/Visionary/main/deploy/agent/install.sh \
  | sudo VISIONARY_URL=https://visionary.example.com VISIONARY_KEY=<server key> sh
```

[`install.sh`](deploy/agent/install.sh) downloads the release binary for the
machine's architecture and installs `/etc/visionary-agent.env` and
[`visionary-agent.service`](deploy/agent/visionary-agent.service). Settings
passed to it (any variable below) go into the env file;
`VISIONARY_AGENT_BINARY=./visionary-agent` installs one you built with
`go build ./cmd/visionary-agent`.

| Variable (flag) | Default | |
|---|---|---|
| `VISIONARY_URL` (`-url`) | | The Visionary server. |
| `VISIONARY_KEY` (`-key`) | | A server ingest key. |
| `VISIONARY_HOST` (`-host`) | the host name | Name shown in Visionary (`system_metrics.instance`). |
| `VISIONARY_SERVICE` (`-service`) | | Service the host belongs to. |
| `VISIONARY_AGENT_INTERVAL` (`-interval`) | `15s` | Sample interval. |
| `VISIONARY_AGENT_REPORT` (`-report`) | `1m` | Report interval. |
| `VISIONARY_AGENT_UNITS` (`-units`) | | systemd services to watch, comma-separated. |
| `VISIONARY_AGENT_SECURITY` (`-security`) | `true` | SSH, sudo, port and update findings. |
| `VISIONARY_AGENT_AUTH_LOG` (`-auth-log`) | auto | A log file, `journald` or `off`. |
| `VISIONARY_AGENT_DOCKER` (`-docker`) | auto | The Docker or Podman socket, or `off`. |
| `VISIONARY_DISK_PATHS` (`-disk-paths`) | all mounts | Mount points to report; the first fills the disk columns. |

On the server, `VISIONARY_HOST_RETENTION_DAYS` (14) keeps process, mount, unit
and container snapshots, `VISIONARY_SECURITY_RETENTION_DAYS` (180) security
events, and `VISIONARY_SYSTEM_RETENTION_DAYS` (30) the agents' metric rows and
the container metrics and events; machines silent for 30 days leave the
inventory. The ingest endpoint is
`POST /api/agent/v1/report` (gzip JSON, [`internal/agentapi`](internal/agentapi)),
rate limited like OTLP (`VISIONARY_AGENT_RATE`, 50 per second).

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

Roles are re-checked every few minutes (`VISIONARY_ROLE_CHECK_INTERVAL`, 5m)
through the profile endpoint, so someone who loses a role is signed out. The
session cookie itself is the proof of sign-in, so Visionary does not depend on
your API for each request: while the API is down, slow, rate limiting or
failing, signed-in users keep working until their session expires, and the
check is retried at the same interval instead of on every page. Only an answer
from the API (the token revoked, the role removed) ends a session early.
Signing *in* still needs the API.

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

## Security monitoring

The **Security** page (under Monitor) works from data Visionary already has:
the request log, the host agent's events and Visionary's own audit log. It
shows open findings, failed logins, scanner IPs, the blocked/forbidden rate,
SSH failures and hosts with pending updates; a findings table with filters
and a detail drawer (the IP's or account's requests around the finding, a
link to Request logs, and **Investigate** for the analyst); charts of failed
logins, auth errors, top IPs, probed paths and user agents; host security;
and recent sensitive actions in Visionary (admins). **Report** opens a
printable report of the range (`#/security/report?from=&to=`) with a
**Print / Save as PDF** button.

Findings live in `security_events`. The host agent writes rows with source
`host`; the server's detectors read `api_logs` every minute and write rows
with source `api`:

| Kind | Fires when (per 5-minute window) | Critical when |
|---|---|---|
| `auth_bruteforce` | an IP has 10+ failed logins | 5× the threshold, or a login from that IP then succeeded |
| `credential_stuffing` | an IP fails against 5+ different accounts | 5× the threshold, or a login succeeded |
| `account_targeted` | 5+ IPs fail to sign in as one account | 5× the threshold |
| `scanner` | an IP asks for 3+ probe paths (`/.env`, `/.git/`, `/wp-admin`, `/actuator`, `../`…) or sends a scanner user agent (sqlmap, nikto, nuclei, masscan, zgrab, gobuster…) | a probe path was answered with 2xx |
| `rate_abuse` | an IP or user makes 120+ requests/min and 5× its own median of the last hour | 5× the floor |
| `forbidden_spike` | a user (or a role, across users) gets 20+ 403s and 5× their usual | 5× the threshold |

Everything else is a warning. A failed login is a 401 or 403 on a login
route, or a 4xx whose `error_key` says the credentials or code were wrong.
Login routes are those whose route or path contains login, signin, sign-in,
auth, token, otp, password or session, or match your own globs in
`VISIONARY_SECURITY_LOGIN_ROUTES`. Accounts come from `user_handle`/`user_id`,
so `account_targeted` needs your API to set the user on failed logins; request
bodies are never read and findings never store bodies, query strings or
secrets (token-like path segments are masked).

**One row per finding, not per minute.** A finding is written once per kind
and IP (or account) per `VISIONARY_SECURITY_DEDUPE` (10m). While the activity
goes on, later runs add the new requests to its `count` and can raise its
severity; after the period a new row starts with only the new requests, so
`SUM(count)` is the number of occurrences. `rate_abuse` and `forbidden_spike`
learn each key's usual rate in memory, so they start ten minutes after the
server does.

Visionary's audit log is read in place: sign-ins and failed sign-ins
(`auth.sign_in_failed`, with the identifier masked), API keys, notification
channels, and `team.role_mapping`, written at startup when the sign-in mode or
role mapping changed since the last start.

**Alerts:** pick **Security findings** (filter by kind, minimum severity,
source and host; fires per host, IP or account) or **Failed logins** as the
metric, or start from a preset: brute force, credential stuffing, scanner, any
critical finding, failed logins spike, SSH brute force, new open port, pending
security updates. The **weekly digest** gets a security section: new findings
by severity, top offending IPs, failed logins against the week before, hosts
with pending updates and notable audit actions.

| Variable | Default | |
|---|---|---|
| `VISIONARY_SECURITY` | `true` | `false` turns the API detectors off; host events and the audit log still show. |
| `VISIONARY_SECURITY_LOGIN_ROUTES` | login-like words | Comma list of route or path globs, e.g. `/auth/*,/api/v1/sessions`. |
| `VISIONARY_SECURITY_INTERVAL`, `_WINDOW`, `_DEDUPE` | `1m`, `5m`, `10m` | How often detectors run, the window they read, one row per key per. |
| `VISIONARY_SECURITY_BRUTEFORCE` | `10` | Failed logins from one IP per window. |
| `VISIONARY_SECURITY_STUFFING_ACCOUNTS` | `5` | Accounts one IP failed against. |
| `VISIONARY_SECURITY_TARGETED_IPS` | `5` | IPs failing against one account. |
| `VISIONARY_SECURITY_SCANNER_HITS` | `3` | Probe-path requests from one IP. |
| `VISIONARY_SECURITY_RATE_MIN`, `_RATE_FACTOR` | `120`, `5` | Requests per minute floor, and times the key's usual rate. |
| `VISIONARY_SECURITY_FORBIDDEN` | `20` | 403s for one user or role per window. |
| `VISIONARY_SECURITY_IGNORE_IPS` | — | IPs or CIDRs never flagged (health checks, office, internal services). |
| `VISIONARY_SECURITY_RETENTION_DAYS` | `180` | Findings kept, all sources; `0` keeps them forever. |

Each run reads the window twice through the `created_at` index (once grouped
per IP, once per account). On D1, which bills by rows read, that is about
`2 × window ÷ interval` reads of every request; a longer interval or shorter
window lowers it.

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
