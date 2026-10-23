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
  time per request; where each route's time goes (database, outbound, its own
  code); N+1 detection; deploy markers on every chart and a side-by-side
  comparison of two versions.
- **Queries:** every statement with its rate, latency spread, total time and
  the routes that run it; queries and nesting depth per route; what a deploy
  changed, statement by statement; GraphQL operations with their depth, field
  count and the queries their resolvers ran; and query plans on demand
  (EXPLAIN, run by your SDK, with full scans and missing indexes pointed out
  and an explanation in plain words).
- **Dependencies:** every third-party API you call (Stripe, S3, OpenAI...)
  treated like a route of your own: rate, failures, latency and their change.
- **CPU profiles** (every server SDK): flame graphs sampled while requests
  are served, filtered to a service or to the routes that were busy. Off until
  turned on in Settings → Profiling, for all services or chosen ones, and off
  again without a redeploy; **Profile now** takes one on demand.
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
- **Team:** viewer, editor and admin roles, from a login per person kept by
  Visionary, a team password, or your own API's sign-in; API keys and an
  audit log. Admins choose which pages the sidebar shows (Settings → Pages)
  and whether services take CPU profiles (Settings → Profiling).
- **Set up in the dashboard:** besides the database connection, everything
  (sign-in, the AI analyst, email, ingest keys, limits, detectors) is in
  Settings and applies without a redeploy. A page with nothing connected yet
  shows how to connect it.
- **Help on every page:** the Help button explains what the page shows, how
  to read it and the code or settings that fill it, per language.

## Integrate in three steps

Each step works on its own; stop wherever the detail is enough. Times are for
a typical API in one service.

**1. Requests, errors and the server (about 15 minutes).** Add your
language's SDK, wrap the router in its middleware and set
`VISIONARY_DATABASE_URL` (or the [D1 variables](#sdk-settings)). Then
[run the server](#run-the-server) with the same database and set up sign-in
in Settings → Sign-in.

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
  were given ("set VISIONARY_DATABASE_URL ...").
- **Work a request leaves running** (a goroutine, thread or task that sends
  an email after the response) stays linked to the request as long as it
  carries the request's context. Start it with the SDK's background helper
  (`visionary.Go`, `visionary.background`, ...) and the request's breakdown
  shows it by name, with its calls and when it finished after the response.

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
the response is shown under "Left running after the response", named when it
was started with the SDK's background helper.

**Untracked time.** A request's breakdown lays its calls on a timeline. Any
stretch that no recorded call covers is shown as *Untracked*: the request was
busy, but not in anything the SDK could see. Overlapping calls count once, so
untracked = duration − the time covered by at least one call. A large untracked
share means one of the rows above is missing, and the hatched row's position
says where: "1.2 s after `SELECT attributed`" points at the code that runs next.
Hook that client or wrap that code, and the row gets a name.

### Query plans

On the Queries page, open a statement and ask for its plan. Visionary never
connects to your database: it queues the request, and an SDK that was given an
explainer for that database runs `EXPLAIN` (never `EXPLAIN ANALYZE`, so the
statement is planned, not run) and sends the plan back. The dashboard points
out full scans of large tables and sorts that an index would avoid, and the
analyst can explain the plan in plain words.

```go
cfg.Explain = []*visionary.Explainer{visionary.PgxExplainer(pool)}                              // Go, pgx
cfg.Explain = []*visionary.Explainer{visionary.SQLExplainer(db, visionary.MySQL, "mysql/shop")} // database/sql
```

```js
visionary.init({ explain: [visionary.explainWith(pool)] }); // Node, pg or mysql2
```

Statements are stored with their values replaced by `?`. PostgreSQL 16 and
later plan them as they are (`GENERIC_PLAN`); MySQL and older PostgreSQL plan
them with NULLs, and the plan says so. A statement whose values the database
cannot guess the types of (`GREATEST(?, 0.5)`) cannot be planned, and the
dashboard says why.

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
| `VISIONARY_LOG_RETENTION_DAYS` | `0` (off) | The server applies Settings → Data. Set it only when no Visionary server reads this database: the SDK then deletes its own requests, spans, errors and profiles older than this. |
| `VISIONARY_LOG_REDACT_HEADERS` | | Added to Authorization, Cookie, Set-Cookie, X-Api-Key… |
| `VISIONARY_LOG_REDACT_FIELDS` | | JSON/form keys added to password, token, secret, otp, api_key… |
| `VISIONARY_LOG_REDACT_REQUEST_FIELDS` | | Keys redacted in requests only. |
| `VISIONARY_LOG_ERROR_CODE_FIELD`, `..._ERROR_KEY_FIELD` | `error.code`, `error.key` | Where your error responses keep their codes. |
| `VISIONARY_SPANS` | `true` | Database and outbound call timing. |
| `VISIONARY_SPAN_THRESHOLD_MS` | `100` | Keep calls made outside any request (jobs, startup) at least this slow; every request keeps its calls. |
| `VISIONARY_SPAN_MAX` | `100` | Calls kept per request. |
| `VISIONARY_ERRORS` | `true` | Exception and panic capture. |
| `VISIONARY_EXPLAIN` | `true` | Answer query plan requests; needs an explainer in code (Go, Node), see [Query plans](#query-plans). |
| `VISIONARY_PROFILE` | follow the dashboard | CPU profiles are turned on and off in Settings → Profiling; `false` makes a service never profile. |
| `VISIONARY_SYSTEM_METRICS` | `true` | |
| `VISIONARY_SYSTEM_INTERVAL` | `15s` | |
| `VISIONARY_SYSTEM_RETENTION_DAYS` | `0` (off) | The same, for system metrics. |

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
key (Settings → API keys, or Settings → Server → Ingest) and never needs database
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

How long snapshots, security events and metrics are kept is set in Settings →
Data; machines silent for 30 days leave the inventory. The ingest endpoint is
`POST /api/agent/v1/report` (gzip JSON, [`internal/agentapi`](internal/agentapi)),
rate limited like OTLP (Settings → Server → Ingest, 50 per second).

## Run the server

```sh
docker run -d -p 8090:8090 --env-file .env ghcr.io/<owner>/visionary:latest
```

Or locally: `cd web && npm ci && npm run build && cd .. && go run ./cmd/visionary`.

The environment only says where the server listens and which database it
uses. Everything else (sign-in, the analyst, email, ingest keys, limits,
detectors) is set in the dashboard under **Settings** and kept in the
database, so every instance shares it and a change needs no redeploy:

| Variable | Default | |
|---|---|---|
| `VISIONARY_DATABASE_URL` | | The database; see [Database](#database). For D1, `D1_ACCOUNT_ID`, `D1_DATABASE_ID` and `D1_API_TOKEN` instead. |
| `VISIONARY_ADDR` | `:8090` | Where the server listens. |
| `VISIONARY_ENABLE_AUTH` | `true` | `false` turns sign-in off: everyone who reaches the server is an admin. For getting back in when sign-in is broken; see below. |

[`.env.example`](.env.example) has these and nothing else.

**First start.** With nothing saved yet, sign-in isn't set up, and the
dashboard opens straight in as an admin, with a banner on every page saying
so. Open **Settings → Sign-in**, choose how people sign in and save; from then
on everyone signs in. Set it up before the URL is public: until you do,
anyone who finds it is an admin.

The session secret that seals cookies is created on first start and stored in
the database too, so sessions survive restarts and work across instances.

### Sign-in and roles

Choose one of four ways in under **Settings → Sign-in** (admins only):

| Way in | Use when | Steps at sign-in |
|---|---|---|
| Accounts | Each person should have their own login, kept by Visionary. | One: username and password. |
| Team password | You just want one shared password. | One: the password. |
| Your API: username and password | Your API already has an email/username + password login. | One: identifier and password, sent to your login endpoint. |
| Your API: one-time code | Your API signs users in with a one-time code (SMS or email). | Two: request a code, then enter it. |

**Accounts.** Admins add people under Settings → Sign-in → Accounts: a
username (an email address works), an optional name, a role (viewer, editor
or admin) and a password of 12+ characters to hand over privately. Admins can
change someone's role, disable or delete them, or set a new password; it
applies within a minute, and a new password signs that person out everywhere.
People change their own password under Settings → Members & roles. Passwords
are stored as bcrypt hashes in `visionary_users`; a wrong username and a wrong
password get the same answer. Switching to accounts needs at least one admin
account, and the last admin account can't be demoted, disabled or deleted.

**Team password.** One password of 12+ characters signs in as admin.
Optionally, a second read-only password signs in as a viewer.

**Your API.** Visionary calls your API's own endpoints, so nobody needs a new
account and your API decides who someone is. You fill in:

- **API base URL** and a **provider name** for the sign-in screen ("Use your
  Acme account").
- **Identifier:** the first field's label, input type (email, phone, text) and
  how it is normalized (lowercase, digits only).
- **Endpoints**, each a method and a path relative to the base URL, with a
  JSON body where `{{identifier}}`, `{{password}}` and `{{code}}` are replaced
  with what the user typed:
  - password mode: **Log in**, e.g. `POST /auth/login` with
    `{"email": "{{identifier}}", "password": "{{password}}"}`;
  - code mode: **Send the code** (`POST /auth/otp/request`,
    `{"phone": "{{identifier}}"}`) and **Verify the code**
    (`POST /auth/otp/verify`, `{"phone": "{{identifier}}", "code": "{{code}}"}`);
  - optional **Profile** (`GET /me`), which returns the signed-in user and lets
    Visionary re-check roles while someone is signed in, and **Log out**.
- **Where things are in the responses**, as dotted paths: `data.token` reads
  `{"data": {"token": "..."}}` and `user.roles.0` reads the first item of an
  array. The token is required; the role, name, handle, user ID, expiry,
  where the code was sent and the error message are optional.
- **Token header** (default `Authorization: Bearer <token>`), headers sent on
  every call (e.g. an API key your API wants), code length and resend delay,
  and whether to ask for the device's location (`{{latitude}}`,
  `{{longitude}}` in bodies).

Saving checks the whole setup first and refuses one that can't work. Then
Visionary decides who may come in, from the role your API returns:

| Role | Can |
|---|---|
| viewer | Read every dashboard, use Explore, run read-only queries. |
| editor | Also edit saved panels, dashboards, alert rules and uptime checks. |
| admin | Also manage sign-in, server settings, API keys, notification channels and data, and read the audit log. |

The **role mapping** maps each of your roles to a Visionary role:
`ADMIN=admin,ENGINEER=editor,SUPPORT=viewer`. Anyone whose role isn't listed
is turned away. `*` matches every user of your API: `ADMIN=admin,*=viewer`
makes your admins admins and everyone else a viewer, and with only `*` the
role field is optional.

Roles are re-checked every few minutes (Settings → Server → Sessions, 5m)
through the profile endpoint, so someone who loses a role is signed out. The
session cookie itself is the proof of sign-in, so Visionary does not depend on
your API for each request: while the API is down, slow, rate limiting or
failing, signed-in users keep working until their session expires, and the
check is retried at the same interval instead of on every page. Only an answer
from the API (the token revoked, the role removed) ends a session early.
Signing *in* still needs the API.

A change applies at once on the server you saved it on, within 30 seconds on
the others, and is written to the audit log. People whose way in changed sign
in again.

**Locked out?** If sign-in stops working (your API moved, the role mapping
leaves nobody in, a saved setup no longer loads, every admin forgot their
password), set
`VISIONARY_ENABLE_AUTH=false` on the server and restart it. Sign-in is then
off, the dashboard opens as an admin with a warning banner, and the saved
setup is kept. Fix it in Settings → Sign-in, remove the variable and restart.
A saved setup that can't be used never opens the dashboard on its own: the
sign-in screen says what is wrong and nobody gets in until it is fixed or the
variable is set.

### Upgrading from environment variables

Older versions took sign-in and the rest from the environment
(`VISIONARY_AUTH_*`, `VISIONARY_SHARED_PASSWORD`, `VISIONARY_VIEWER_PASSWORD`,
`VISIONARY_ALLOWED_ROLES`, `VISIONARY_ROLE_MAP`, `VISIONARY_AUTH_CONFIG`,
`VISIONARY_SESSION_SECRET`, `OPENAI_API_KEY` and every variable listed under
[Settings → Server](#settings--server)). On the first start of this version,
each one still set is copied into Settings once, and the log says so for each;
from then on it is ignored and can be removed. Nothing to do by hand: sign-in
works as before, and sessions sealed with `VISIONARY_SESSION_SECRET` stay
valid. `VISIONARY_AUTH_MODE=none` is not copied: use
`VISIONARY_ENABLE_AUTH=false` instead.

### Keys

Admins create keys in Settings → API keys (only a hash is stored; the key is
shown once):

- **read**: use Visionary's HTTP API as a viewer, with `Authorization: Bearer vis_…`.
- **server**: SDKs over HTTP, OpenTelemetry and the host agent.
- **client**: browser and mobile apps; public by nature, so combine it with
  the allowed web origins in Settings → Server → Ingest.

Settings → Server → Ingest also takes comma-separated server and client keys,
for keys you already hand out and want to keep.

### Settings in the dashboard

Everything you'd change while running Visionary is in **Settings**, takes
effect within 30 seconds on every server sharing the database (a few, marked
*After restart*, when the server next starts), and is recorded in the audit
log.

| Tab | Who | What |
|---|---|---|
| **Preferences** | everyone, for themselves | Start page, the time range and auto-refresh a visit starts with, theme (or follow the device), time zone (this device or UTC) and 12/24-hour clock, the language Help shows code in, routes left out of Slowest requests. Kept on the server, so they follow you to any browser. |
| **Sign-in** | admins | How people sign in and the role mapping; see [Sign-in and roles](#sign-in-and-roles). |
| **Server** | admins | The analyst, sessions, email, alerts, ingest and the security detectors; see below. |
| **Thresholds** | admins | Apdex threshold T (default 500 ms), the SLO target (99%), and routes with their own T, such as exports and uploads, so they don't drag Apdex and the SLO down. |
| **Data** | admins | Retention per kind of data, with how many rows each holds and the oldest; and deleting data by kind, time range and service. |
| **Profiling** | admins | CPU profiles on or off, for all services or chosen ones, and how often. |
| **Pages** | admins | Which pages the sidebar shows. |
| **Status page** | admins | The public page at `/status` (and `/status.json`): on or off, and its title. |

Default retention: request logs, spans, errors, system and container metrics,
CPU profiles, frontend events and query plans 30 days; host snapshots 14;
uptime results 90; security findings 180; alert history and the audit log
365 (at least 30); deploys forever; per-minute chart summaries 14 days and
hourly ones 400. The server deletes older rows every 6 hours.

#### Settings → Server

Each setting kept its old variable's name in the API
(`GET /api/settings/config` lists both), so existing docs and deployments map
one to one. Secrets (the OpenAI key, the SMTP password, ingest keys) are never sent
back to the browser: the page only shows whether one is set.

| Group | Settings (old variable) | Defaults |
|---|---|---|
| AI analyst | OpenAI API key (`OPENAI_API_KEY`), API base URL (`OPENAI_API_BASE`), model (`VISIONARY_MODEL`), reasoning effort (`VISIONARY_REASONING`), product name (`VISIONARY_APP_NAME`), notes about your data (`VISIONARY_DATA_NOTES`), rows per query (`VISIONARY_MAX_ROWS`), longest time range (`VISIONARY_MAX_RANGE_DAYS`) | off until a key is set; `https://api.openai.com/v1`; `gpt-4.1`; 5000 rows; 90 days |
| Sessions | session length (`VISIONARY_SESSION_TTL`), re-check roles every (`VISIONARY_ROLE_CHECK_INTERVAL`), behind a trusted proxy (`VISIONARY_TRUST_PROXY`), secure cookie (`VISIONARY_SECURE_COOKIE`) | 12h; 5m; off; auto |
| Email | SMTP host, port, user, password, from address (`VISIONARY_SMTP_*`) | port 587 (STARTTLS); from defaults to the user |
| Alerts | check rules every (`VISIONARY_ALERTS_INTERVAL`), digest time zone (`VISIONARY_DIGEST_TZ`) | 1m; the server's zone |
| Ingest | server and client keys (`VISIONARY_INGEST_KEYS`, `VISIONARY_CLIENT_KEYS`), allowed web origins (`VISIONARY_CLIENT_ORIGINS`, wildcards like `https://*.example.com`), frontend events per IP and per key (`VISIONARY_CLIENT_RATE_PER_IP`, `_PER_KEY`), OpenTelemetry and host agent rates (`VISIONARY_OTLP_RATE`, `VISIONARY_AGENT_RATE`), OpenTelemetry spans and errors kept (`VISIONARY_SPANS`, `VISIONARY_ERRORS`, `VISIONARY_SPAN_THRESHOLD_MS`, `VISIONARY_SPAN_MAX`) | any origin; 120 and 6000 per minute; 50 per second; on; 100 ms; 100 |
| Security detectors | see [Security monitoring](#security-monitoring) | |
| Other | chart summaries (`VISIONARY_ROLLUPS`), uptime checks may reach private addresses (`VISIONARY_UPTIME_ALLOW_PRIVATE`), check for new versions (`VISIONARY_UPDATE_CHECK`) | on; off; on |

**Upgrading:** Thresholds, Data and Status page used to be environment
variables too (`VISIONARY_APDEX_MS`, `VISIONARY_SLO_TARGET`,
`VISIONARY_STATUS_PAGE`, `VISIONARY_STATUS_TITLE` and the
`VISIONARY_*_RETENTION_DAYS` and `VISIONARY_ROLLUP_*_DAYS` variables). Like
everything above, a value still set is copied into Settings once on the first
start, and ignored from then on.

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
Settings → Server → Security detectors → Login routes. Accounts come from `user_handle`/`user_id`,
so `account_targeted` needs your API to set the user on failed logins; request
bodies are never read and findings never store bodies, query strings or
secrets (token-like path segments are masked).

**One row per finding, not per minute.** A finding is written once per kind
and IP (or account) per *Repeat a finding after* (10m). While the activity
goes on, later runs add the new requests to its `count` and can raise its
severity; after the period a new row starts with only the new requests, so
`SUM(count)` is the number of occurrences. `rate_abuse` and `forbidden_spike`
learn each key's usual rate in memory, so they start ten minutes after the
server does.

Visionary's audit log is read in place: sign-ins and failed sign-ins
(`auth.sign_in_failed`, with the identifier masked), API keys, notification
channels, accounts (`team.account_created`, `_updated`, `_deleted`,
`auth.password_changed`), sign-in and server settings changes (`settings.sign_in`,
`settings.server`, which record the names of the settings changed, never their
values), and `team.role_mapping`, written whenever the sign-in mode or role
mapping in force changes.

**Alerts:** pick **Security findings** (filter by kind, minimum severity,
source and host; fires per host, IP or account) or **Failed logins** as the
metric, or start from a preset: brute force, credential stuffing, scanner, any
critical finding, failed logins spike, SSH brute force, new open port, pending
security updates. The **weekly digest** gets a security section: new findings
by severity, top offending IPs, failed logins against the week before, hosts
with pending updates and notable audit actions.

Tune them in **Settings → Server → Security detectors** (they apply after a
restart; the old variable is in brackets):

| Setting | Default | |
|---|---|---|
| Detectors on (`VISIONARY_SECURITY`) | on | Off turns the API detectors off; host events and the audit log still show. |
| Login routes (`VISIONARY_SECURITY_LOGIN_ROUTES`) | login-like words | Comma list of route or path globs, e.g. `/auth/*,/api/v1/sessions`. |
| Scan every, Look back, Repeat a finding after (`_INTERVAL`, `_WINDOW`, `_DEDUPE`) | `1m`, `5m`, `10m` | How often detectors run, the window they read, one row per key per. |
| Failed logins from one IP (`_BRUTEFORCE`) | `10` | Per window. |
| Accounts tried from one IP (`_STUFFING_ACCOUNTS`) | `5` | Accounts one IP failed against. |
| IPs failing on one account (`_TARGETED_IPS`) | `5` | |
| Scanner paths from one IP (`_SCANNER_HITS`) | `3` | Probe-path requests. |
| Requests per minute, Times the usual rate (`_RATE_MIN`, `_RATE_FACTOR`) | `120`, `5` | Requests per minute floor, and times the key's usual rate. |
| 403s from one IP (`_FORBIDDEN`) | `20` | 403s for one user or role per window. |
| Ignore IPs (`_IGNORE_IPS`) | — | IPs or CIDRs never flagged (health checks, office, internal services). |

Each run reads the window twice through the `created_at` index (once grouped
per IP, once per account). On D1, which bills by rows read, that is about
`2 × window ÷ interval` reads of every request; a longer interval or shorter
window lowers it.

## Deploy

`.github/workflows/image.yml` checks the build and pushes
`ghcr.io/<owner>/visionary` for `linux/amd64` and `linux/arm64`: `latest` from
the default branch, `sha-<short>`, and semver tags from `v*` git tags.

Run it behind HTTPS and turn on *Behind a trusted proxy* in Settings →
Server → Sessions. The image is distroless and runs as non-root.
`GET /api/healthz` is the health check.

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
- **Settings live in the database** (`visionary_settings`), including the
  session secret, the team passwords, the OpenAI key, the SMTP password and
  ingest keys. Account passwords are bcrypt hashes in `visionary_users`. The dashboard never sends a secret back, and queries from
  Explore, the analyst and saved panels can't read that table. Anyone with the
  database credentials can, so treat those credentials (which your services'
  SDKs also hold) as admin access to Visionary.
- **Logs are redacted** before they are stored; SQL is stored with its values
  replaced by `?`. Add your own redactions with `VISIONARY_LOG_REDACT_*`.
- **Ingest endpoints** need a key, cap body sizes (compressed and
  decompressed) and rate-limit per IP and per key.
- **Uptime checks** cannot reach private, loopback or cloud metadata addresses,
  including through DNS or redirects, unless you allow it.
- **Secrets** (webhook URLs, SMTP passwords, check headers, API keys in
  Settings → Server) are masked in the API and never logged.

## Cost

On D1, which bills by rows read, panels show their rows read under Inspect.
Rollups keep long ranges cheap; a short range reads raw rows, so keep
auto-refresh for short ranges.
