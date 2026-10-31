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
[![License: VNCL v1.0](https://img.shields.io/badge/license-VNCL%20v1.0-orange)](LICENSE)

Self-hosted monitoring for your APIs, servers and frontends: requests, traces,
logs, errors, metrics, uptime, alerts and an AI analyst that answers *why*.

Visionary is one Go server with a React dashboard. It reads one SQL database
that you choose: PostgreSQL, MySQL 8, SQLite or Cloudflare D1. There is no
separate collector, queue or time-series store to run.

**Where the step-by-step docs are.** This README gets you running and explains
the ideas. For how to use each page, open **How to use** at the bottom of the
dashboard's sidebar, or the **Help** button on any page: it says what the page
shows, how to read it, and the code or settings that fill it, in your language.

## How it works

```
your services (server SDK) ──────── write rows ─────────▶ ┌──────────┐
                                                          │ database │
browser and mobile apps ─┐                                └──────────┘
OpenTelemetry, Prometheus├── post with a key ──▶ Visionary ◀─┘ reads and writes
log shippers, syslog     │                       server ──▶ dashboard, Slack, email…
host agent, CI webhooks ─┘
```

- **Server SDKs** (Go, Node, Python, Ruby, Java/Spring, .NET) write straight
  into the database, batched in the background. They need the database URL.
- **Everything else** posts to the Visionary server over HTTP with a key: the
  browser and mobile SDKs, OpenTelemetry (OTLP over HTTP, or gRPC if you turn
  it on), Prometheus remote_write, log shippers (Fluent Bit, Vector, Filebeat,
  Logstash, Promtail/Alloy), syslog, the [host agent](#host-agent) and CI
  webhooks. They never need database credentials.
- **The server** draws the dashboard, runs alerts, uptime checks and security
  detectors, and keeps its settings in the same database.

## Get started

Five steps, about 15 minutes. After step 5 you have a working setup.

### 1. Choose a database

| Database | `VISIONARY_DATABASE_URL` | Good for |
|---|---|---|
| PostgreSQL | `postgres://user:pass@host:5432/visionary?sslmode=require` | Most setups; high traffic. |
| MySQL 8 | `mysql://user:pass@host:3306/visionary` | The same, if you run MySQL. |
| SQLite | `sqlite:///var/lib/visionary/analytics.db` (Windows: `sqlite:///C:/data/analytics.db`) | One machine, where the server and your services share the file. |
| Cloudflare D1 | leave it unset and set `D1_ACCOUNT_ID`, `D1_DATABASE_ID`, `D1_API_TOKEN` | No database to run. Modest traffic; billed by rows read. |

An empty database is fine: tables are created on first start and gain new
columns on upgrade. They can also live in your application's database; see
[Database and scale](#database-and-scale).

### 2. Run the server

The server needs one thing: the database. Set `VISIONARY_DATABASE_URL` (or the
three `D1_*` variables). It listens on `:8090`.

**Docker Compose** (the quickest way: Visionary and a PostgreSQL database for
it, built from this repository):

```sh
git clone https://github.com/styropyr0/Visionary.git && cd Visionary
POSTGRES_PASSWORD=something-long docker compose up -d --build
```

Open http://localhost:8090. Your services' SDKs write to the same database:
`postgres://visionary:<password>@<this machine>:5432/visionary?sslmode=disable`.
For browser uptime checks, start it with `VISIONARY_TARGET=browser` to build
the image that includes Chromium. [`compose.yaml`](compose.yaml) explains each
line; [`deploy/replicas`](deploy/replicas) runs two replicas behind a proxy.

**Docker** (`linux/amd64` and `linux/arm64`), with a database you already have:

```sh
docker run -d -p 8090:8090 \
  -e VISIONARY_DATABASE_URL="postgres://user:pass@host:5432/visionary" \
  ghcr.io/styropyr0/visionary:latest
```

Or copy [`.env.example`](.env.example) to `.env`, fill it in, and use
`--env-file .env`.

**From source** (Go 1.26 and Node 22). The server embeds the dashboard, so build
the dashboard first:

```sh
cd web && npm ci && npm run build && cd ..
go run ./cmd/visionary                       # reads .env from the current directory
go build -o visionary ./cmd/visionary        # or: one binary with everything in it
```

**On a platform** (Railway, Render, Fly.io, Kubernetes, Cloud Run): deploy the
image or the [`Dockerfile`](Dockerfile), set the database variable and expose
port 8090. The health check is `GET /api/healthz`. See [Deploy](#deploy).

The log says where it listens and which database it opened.

### 3. Sign in and create keys

Open http://localhost:8090. The first time, you come straight in as admin,
with a banner saying sign-in is not set up. Before the URL is public:

1. **Settings → Sign-in:** choose **Accounts** and add yourself with the
   **Admin** role. From then on everyone signs in.
2. **Settings → Server → Sessions:** behind HTTPS on a proxy or platform, turn
   on *Behind a trusted proxy*.
3. **Settings → API keys:** create a **server** key if you'll use
   OpenTelemetry, log shippers or the host agent, and a **client** key for
   browser and mobile apps. Each key is shown once.

Everything else can wait. Settings apply within about 10 seconds, without a
restart (a few are marked *After restart*).

### 4. Connect your first service

A server SDK needs two variables in the service's environment:

```sh
VISIONARY_DATABASE_URL=postgres://user:pass@host:5432/visionary   # the same database as the server
VISIONARY_SERVICE=my-api                                          # any name you like
```

You can leave out `VISIONARY_DATABASE_URL` if your code passes its own
connection to the SDK. For D1, set the three `D1_*` variables instead. Then
add the middleware. In Go:

```go
import (
	_ "github.com/jackc/pgx/v5/stdlib" // the driver for your URL (or go-sql-driver/mysql, modernc.org/sqlite)
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

Other apps:

| Your app | Install | Connects with | Guide |
|---|---|---|---|
| Go (`net/http`, chi, any router) | `go get github.com/styropyr0/visionary/sdks/go` | database URL | [`sdks/go`](sdks/go/README.md) |
| Node (Express, Fastify, Koa, NestJS) | `npm install visionary-sdk` | database URL | [`sdks/node`](sdks/node/README.md) |
| Python (Django, FastAPI, Flask) | `pip install "visionary-sdk[postgres]"` | database URL | [`sdks/python`](sdks/python/README.md) |
| Ruby (Rails, Sinatra, any Rack app) | `gem "visionary-sdk", require: "visionary"` | database URL | [`sdks/ruby`](sdks/ruby/README.md) |
| Java (Spring Boot 3) | `io.github.styropyr0:visionary-spring-boot-starter` | database URL | [`sdks/java`](sdks/java/README.md) |
| .NET (ASP.NET Core) | `dotnet add package Visionary.AspNetCore` | database URL | [`sdks/dotnet`](sdks/dotnet/README.md) |
| Browser | `npm install visionary-browser` | server URL + client key | [`sdks/browser`](sdks/browser/README.md) |
| iOS (Swift) | Swift Package `https://github.com/styropyr0/Visionary.git` | server URL + client key | [`sdks/ios`](sdks/ios/README.md) |
| Android (Kotlin) | `io.github.styropyr0:visionary-android` | server URL + client key | [`sdks/android`](sdks/android/README.md) |
| Anything with OpenTelemetry | your OTLP exporter | server URL + server key | [`docs/otlp.md`](docs/otlp.md) |
| A Linux machine and its containers | the host agent | server URL + server key | [`cmd/visionary-agent`](cmd/visionary-agent/README.md) |

In short: Node `visionary.init()` then `app.use(visionary.express())`; Django
puts `visionary.integrations.django.VisionaryMiddleware` first in
`MIDDLEWARE`; Rails and Spring Boot pick the SDK up on their own; ASP.NET Core
`builder.Services.AddVisionary()` and `app.UseVisionary()`.

**Browser and mobile apps** use no environment variables. Pass the server URL
and the client key to `init()`, and add the site's origin under Settings →
Server → Ingest → Allowed web origins:

```js
import Visionary from "visionary-browser";
Visionary.init({ endpoint: "https://visionary.example.com", key: "pk_…", app_version: "1.0.0" });
```

**OpenTelemetry** needs no Visionary SDK. Use the standard variables:

```sh
OTEL_EXPORTER_OTLP_ENDPOINT=https://visionary.example.com
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
OTEL_EXPORTER_OTLP_HEADERS="authorization=Bearer <server key>"
OTEL_SERVICE_NAME=my-api
```

For OTLP/gRPC, turn on *Accept OpenTelemetry over gRPC* in Settings → Server →
Ingest, restart, publish port 4317 and use `OTEL_EXPORTER_OTLP_PROTOCOL=grpc`.
It is plaintext, so put a proxy that speaks HTTP/2 in front for traffic that
leaves your network. Details in [`docs/otlp.md`](docs/otlp.md).

### 5. Check that data arrives

Restart the service and send it one request.

- It shows on **API overview** and **Requests** within a few seconds, and the
  service appears in the service picker.
- The service's log has no `visionary:` warning. Java and .NET print one line
  saying where they write.
- Browser and mobile events show on **Frontend**; host agent machines on
  **System** within a minute.

Nothing there? See [Troubleshooting](#troubleshooting).

## Core ideas

**Service.** A service is one app that sends data, such as `shop-api` or a
worker. It is only a name stamped on every row that app writes. There is
nothing to register: a service appears as soon as data arrives with its name.
Server SDKs take it from `VISIONARY_SERVICE`, OpenTelemetry from
`OTEL_SERVICE_NAME`, browser and mobile SDKs from the app name you give them.
Service filters, the service map and workspaces all go by this name.

**Instance and version.** An instance is one running copy of a service (a pod,
a container, a machine; the hostname unless you set `VISIONARY_INSTANCE`).
Three replicas are one service with three instances. The version is the
release that is deployed, from `VISIONARY_RELEASE` or the platform's commit
variable. It draws deploy markers and before/after comparisons.

**Requests and their calls.** A request is one call your service answered,
recorded by the SDK's middleware (or an OpenTelemetry server span). Each
database query, outbound HTTP call or custom step inside it is a span.
Requests, API overview, Performance, Queries and Dependencies are built from
these rows.

**Traces.** A trace is the same requests and spans, followed across services.
A service passes the trace id on in the `traceparent` header, and the service
it calls records its work under the same id. Traces joins the pieces into one
timeline and draws the service map. It is not a separate pipeline.

**Logs.** Logs come from the SDKs, OpenTelemetry, log shippers, syslog, the
host agent or plain HTTP. SDK lines link to the request that wrote them. A log
line never becomes a request or a query: an nginx access line is searchable in
Logs, but does not show in Requests, Performance or Queries. Those pages need
the SDK or OpenTelemetry inside the app. Logs from the agent or a shipper are
the catch-all for everything else.

**Environments.** There is no environment field. We recommend one database and
one Visionary server per environment, so staging can't slow production, page
anyone or show production data to testers. Keep alert rules, dashboards,
checks and keys in sync with Settings → Configuration (export and import) or
the Terraform provider in [`terraform/`](terraform). If you'd rather share one
install, put the environment in the service name (`shop-api-staging`) and
never let two environments share one name.

## What you get

| Page | What it shows |
|---|---|
| API overview | Traffic, errors and latency per route; Apdex and SLOs with error budgets. |
| Performance | Where each route's time goes, slowest requests call by call, regressions, N+1 detection, deploy comparisons. |
| Queries | Every SQL statement with rate, latency and the routes that run it; GraphQL operations; [query plans](#query-plans) on demand. |
| Dependencies | Every third-party API you call (Stripe, S3, OpenAI…) with rate, failures and latency. |
| CPU profiles | Flame graphs from every server SDK, off until turned on in Settings → Profiling. |
| System | Host CPU, memory, disk, network, process, runtime and pool metrics; container limits, throttling and OOM kills. |
| Containers | Docker and Podman containers on agent machines, plus services on container platforms. |
| Metrics | Your counters, gauges and histograms, OTLP metrics and Prometheus remote_write; PromQL; a Prometheus API for Grafana at `/api/prom`. |
| Errors | Exceptions and panics grouped into issues, with stacks, versions and the request. |
| Frontend | Web Vitals, page loads, JS and mobile crashes, sessions, and session replay (opt-in, sampled, inputs masked). |
| Uptime | HTTP checks with timing phases and certificate expiry, multi-step API checks with variables and assertions, [browser checks](#browser-checks), TCP, DNS, ping and TLS certificate checks, heartbeats for cron jobs, Playwright tests reported from your CI, runs from CI by tag ([checks in CI](#checks-in-ci)), and a public status page at `/status`. |
| CI | GitHub Actions runs from a signed webhook, other CI and JUnit results: durations, failing and flaky tests, deploys. |
| Security | Built-in detectors, your own detection rules with MITRE ATT&CK tags, and signals to triage; see [Security monitoring](#security-monitoring). |
| Explore | Ask a question in plain words; the AI analyst answers with charts. |
| Logs | Search with a query language, live tail, patterns, alerts; see [Logs](#logs). |
| Requests | Every request with its waterfall of queries and outbound calls. |
| Traces | Trace search, the trace view across services, and the service map. |
| Dashboards, Saved panels | Any panel on a grid, with variables, share links and a TV mode. |
| Alerts | Thresholds, seasonal anomalies and SLO burn rates over 60+ metrics, to Slack, Discord, Teams, Google Chat, webhooks, email, PagerDuty, Opsgenie, Telegram or SMS. |
| Incidents | Declare from an alert or by hand; timeline, status updates and a drafted postmortem. |
| Settings | Sign-in (accounts, team password, your API's login, SSO with OIDC or SAML, SCIM), workspaces, API keys, retention, configuration export/import, replicas. |
| How to use | Step-by-step guide to every page. |

Every page with nothing connected yet shows how to connect it.

## Turn on more

All optional. Your SDK's README and the Help drawer show the exact code.

- **Queries and outbound calls** on each request: add the database hook where
  you create the pool and the HTTP hook on your clients. Ruby, Java and .NET do
  this on their own. See [What the middleware sees](#what-the-middleware-sees).
- **Who made the request:** `SetUser` with the signed-in user.
- **Redis, queues or your own slow code:** wrap it in a custom span.
- **Work that outlives the response:** start it with the SDK's background
  helper (`visionary.Go` in Go).
- **Logs:** route your logger to the SDK (one line), or point OpenTelemetry or
  a shipper at the server; see [Logs](#logs).
- **Sign-in attempts** for the security detectors: call the SDK's login helper
  in your login handler (`visionary.LoginAttempt(ctx, email)` in Go).
- **AI analyst:** add an OpenAI-compatible API key in Settings → Server → AI
  analyst.
- **CPU profiles:** no code; Settings → Profiling.
- **Alerts:** add a channel and a rule under Alerts. Email needs SMTP in
  Settings → Server → Email.
- **Uptime and status page:** add a check under Uptime (HTTP, API steps,
  browser, TCP, DNS, ping, certificate, or a heartbeat your cron job calls at
  `/api/heartbeat/<token>`); turn the page on in Settings → Status page and
  mark checks public.
- **Deploy markers:** set `VISIONARY_RELEASE`, or let the SDK read the
  platform's commit (Railway, Render, Vercel, Heroku, GitHub Actions and more).

## Running in production

**Cheap and safe to keep on:** requests, spans and traces, errors, metrics and
info-level logs. SDK writes are batched in the background and never block a
request; a database that is down never breaks one. If the queue fills, rows
are dropped and counted (`visionary: queue full` in the log) rather than
slowing requests. There is no trace sampling yet: every request is kept.

**Turn on while investigating, then off:**

- Debug logs for one service, route or user for 15 minutes, from a line in
  Logs or Settings → Logs. Debug lines are already kept for failed and slow
  requests.
- CPU profiling (Settings → Profiling), for chosen services.
- Session replay, with a low `sampleRate`.
- Request and response bodies. They are stored by default, redacted and capped
  at 8 KB; set `VISIONARY_LOG_BODIES=false` on a service to stop.

**Retention** is set per kind of data in Settings → Data. By default requests,
spans, errors and system metrics are kept 30 days, logs 7 days (warn and above 30),
uptime and CI 90 days. On PostgreSQL with millions of rows a day, turn on time
partitioning there too.

## Logs

Nothing is captured until you send it. Info and above is kept; debug lines are
held during a request and kept only when it fails or is slow. Settings → Logs
changes the level, adds temporary overrides, drop rules and a per-minute cap,
and every SDK picks it up within 30 seconds.

**From an SDK** (each line linked to its request):

| SDK | Turn it on |
|---|---|
| Go | `slog.New(visionary.LogHandler(yourHandler))`, or `agent.CaptureStdLog()` for `log` |
| Node | `visionary.captureConsole()`, `pino({}, visionary.pinoStream())` or `visionary.winstonTransport()` |
| Python | `visionary.capture_logging()` after logging is configured |
| Ruby | `Visionary.capture_rails_logger`, or `Visionary.logger(Logger.new($stdout))` |
| Java | `VISIONARY_LOG_CAPTURE=true`, or `VisionaryLogbackAppender` in `logback-spring.xml` |
| .NET | `builder.Logging.AddVisionary()` |
| Browser, Android, iOS | `captureConsole: true` or `Visionary.log(...)`, linked to the session |

**From anything else**, with a server key in `Authorization` (Bearer, ApiKey,
or Basic with the key as password), `x-visionary-key` or `?key=`:

| Sender | Where |
|---|---|
| OpenTelemetry | `/v1/logs` |
| Fluent Bit, Vector, Filebeat, Logstash (Elasticsearch output) | `https://visionary.example.com/api/ingest/logs` as the Elasticsearch URL |
| Promtail, Grafana Alloy, Fluent Bit or Vector (Loki output) | `/loki/api/v1/push` |
| curl, scripts, functions | `POST /api/ingest/logs`: a JSON array, NDJSON or plain text |
| Routers, firewalls, anything syslog | the syslog receiver, on the address in Settings → Logs |
| A machine's journal, files and containers | the host agent with `VISIONARY_AGENT_LOGS` |

JSON, logfmt and nginx/Apache access lines become fields; parse rules in
Settings → Logs handle other text. Search with words, `field:value`,
`level:error+`, `status:5xx`, `-word`, `OR`, `(…)` and `/regex/`. Secrets are
scrubbed before storage. With a bucket in Settings → Server → Log archive (S3,
R2, MinIO, B2), each finished hour is also written there and can be loaded
back.

## Host agent

The agent ([`cmd/visionary-agent`](cmd/visionary-agent/README.md)) is a small
helper for a whole Linux machine, with or without an app on it. It posts to the
server with a server key and never needs database credentials. It installs as
a systemd service running as root:

```sh
curl -fsSL https://raw.githubusercontent.com/styropyr0/Visionary/main/deploy/agent/install.sh \
  | sudo VISIONARY_URL=https://visionary.example.com VISIONARY_KEY=<server key> sh
```

It reports to three pages:

- **System:** CPU, memory, swap, disks, network and load every 15 s; machine
  details, mounts, top processes and failed systemd services every minute.
- **Containers:** every Docker or Podman container, with CPU and memory
  against limits, restarts, OOM kills and failing health checks.
- **Security:** SSH logins and sudo, open ports, pending updates, reboots,
  `authorized_keys` and system file changes, suspicious processes, risky
  containers.

It also ships logs from journald, containers and files when you set
`VISIONARY_AGENT_LOGS`. While the server is unreachable it keeps up to an hour
of reports and retries. Variables are listed under
[Environment reference](#host-agent-1).

**Uptime locations.** Started with `VISIONARY_AGENT_LOCATION` and a location
key, the agent runs uptime checks from its machine instead of reporting it,
so a check can run from several places at once (Uptime → Locations): down in
Frankfurt but fine in Mumbai shows as such. A check is down when any, a
majority or all of its locations fail; a location that stops reporting is
marked stale and left out, and the *Uptime location offline* alert tells
admins once. It runs anywhere, in Docker too (`deploy/location/compose.yaml`).
See [the agent's README](cmd/visionary-agent/README.md#uptime-locations).

## Browser checks

A browser check plays scripted steps in a headless Chrome, Chromium or Edge on
the server: go to a URL, click, type (values can be `{{secret.NAME}}`, masked in
results), wait, and check text, the URL or an element. Each run records step
times, LCP, CLS, TTFB, console and network errors, and a screenshot on
failure. Runs share the schedule, alerts and status page of HTTP checks.

The default image has no browser, so browser checks stay paused in it. Build
the variant with Chromium (about 400 MB larger):

```sh
docker build --target browser -t visionary:browser .
```

Outside Docker, the server finds the browser from Settings → Server → Other,
then `VISIONARY_CHROME`, then the usual install locations and `PATH`.

## Checks in CI

Tag checks (`smoke`, `checkout`) and run a tag from your pipeline after a
deploy with the [`run-checks` action](.github/actions/run-checks/action.yml)
or the `visionary-checks` CLI (attached to each release); add
`--url-override https://pr-12.example.com` to aim the same checks at a preview.
It prints each result, can write JUnit XML and fails the job when a check
fails; the run shows on the CI page next to its commit. Playwright suites stay
in your repository: [`visionary-playwright-reporter`](sdks/playwright-reporter)
reports each test as a check from wherever it runs, and the server never runs
your code. Both use an API key with the `ci` scope.

## What the middleware sees

The middleware alone records every request: route, status, duration, user,
bodies and errors. A call made inside a request is recorded only when its
client is hooked:

| Inside a request | Recorded by |
|---|---|
| Database queries | The SDK's hook for your driver or ORM; one line where the pool is created. |
| Outbound HTTP | The SDK's HTTP hook. Node patches `http`/`fetch` itself; in Go, wrap clients with `visionary.Transport`. |
| Vendor SDKs (AWS, OpenAI, Stripe…) | Nothing, until you give that SDK a traced HTTP client. |
| Redis, queues, gRPC | A custom span around the call. |
| Your own code | A custom span, if you want it named. |

A call joins its request only when it carries the request's context (in Go,
pass `r.Context()` and use `http.NewRequestWithContext`). Time no call covers
shows as *Untracked* on the request's timeline, placed where it happened, so
you can see which client to hook next.

### Query plans

Open a statement on Queries and ask for its plan. Visionary never connects to
your database: an SDK that was given an explainer runs `EXPLAIN` (never
`EXPLAIN ANALYZE`) and sends the plan back. Only Go and Node have explainers:

```go
cfg.Explain = []*visionary.Explainer{visionary.PgxExplainer(pool)}
```

```js
visionary.init({ explain: [visionary.explainWith(pool)] }); // pg or mysql2
```

The explainer's target must match the statement's, such as `postgresql/shop`.
A plan that stays pending means no running service has a matching explainer.

## Database and scale

Tables are created on first start; the schemas are in
[`sdks/go/schema/`](sdks/go/schema). They can share your application's
database: the analyst and saved panels can only read Visionary's own tables.
A dedicated database or schema is still the tidy choice. Long ranges read
per-minute and per-hour rollups, so a 30-day chart reads a small fraction of
the raw rows.

Measure your own database with `cmd/visionary-bench`:

```sh
go run ./cmd/visionary-bench -db "$VISIONARY_DATABASE_URL" -rate 2000 -duration 60s
go run ./cmd/visionary-bench -server http://localhost:8090 -read-only   # sign-in off, or -token
```

On one laptop (Core Ultra 5 125H, NVMe, default PostgreSQL 18.4; about 6 rows
per request):

| Backend | Requests/s offered | Rows/s written | Dropped |
|---|---|---|---|
| PostgreSQL | 4,000 | 24,000 | 0 |
| PostgreSQL | 8,000 | 38,400 | 98,400 |
| SQLite | 1,000 | 5,700 | 0 |
| SQLite | 2,000 | 7,600 | 23,500 |

Rules of thumb:

- **SQLite:** up to about 1,000 requests a second from one host.
- **PostgreSQL:** about 4,000 requests a second on a machine like that, with
  the dashboard still fast. A dedicated server and `synchronous_commit = off`
  go further. MySQL gets the same writes but was not measured.
- **D1:** modest traffic; for hundreds of requests a second, use PostgreSQL.
- **Time partitioning (PostgreSQL 13+):** from a few million rows a day, turn
  on Settings → Data → Time partitioning. The busiest tables get one partition
  per day, so retention drops whole days. Tables with rows convert online when
  an admin presses **Convert** (it needs room for a second copy).

## Sign-in, keys and settings

**Sign-in** (Settings → Sign-in) can be:

- **Accounts** kept by Visionary, each with a role.
- **A team password**, with an optional read-only one.
- **Your API's own login**, by password or one-time code, with your roles
  mapped to Visionary's (`ADMIN=admin,*=viewer`).
- **Single sign-on** with OpenID Connect or SAML, next to the above, with SCIM
  provisioning.

Roles are **viewer** (read everything, use Explore), **editor** (also edit
dashboards, alerts and checks) and **admin** (also sign-in, server settings,
keys, data and the audit log). Two-step sign-in and an IP allow list are in
Settings → Server → Sessions.

**Keys** (Settings → API keys, shown once, stored as a hash):

| Scope | For |
|---|---|
| read, editor, admin | Visionary's HTTP API and Grafana, with that role |
| server | OpenTelemetry, Prometheus remote_write, log shippers, the host agent, CI |
| client | Browser and mobile apps; public, so pair it with allowed web origins |
| scim | Your identity provider |
| location | One uptime location's runner; made in Uptime → Locations |

**Settings** live in the database, so every replica shares them. Tabs:
Preferences, Members & roles, Workspaces, Sign-in, Server, API keys,
Thresholds, Data, Logs, Profiling, Pages, Status page, Configuration, Audit
log, About. The Help drawer on Settings explains each.

**Locked out?** Restart the server with `VISIONARY_ENABLE_AUTH=false`, fix
Settings → Sign-in, then remove the variable and restart.

## Security monitoring

The Security page works from data Visionary already has: the request log, the
host agent's events and its own audit log. Built-in detectors catch brute
force, credential stuffing, account takeover signs (new location or device,
impossible travel), enumeration, injection attempts, scanners, rate abuse,
SSH brute force and changes to Visionary itself. Add your own detection rules
(threshold, new value, sequence, anomaly) over requests, logs and events.

- Call the SDK's login helper in your login handler so failed sign-ins carry
  an account; without it, the account detectors can't fire.
- Location and network databases (GeoLite2 or DB-IP `.mmdb`) and blocklist
  feeds are optional, in Settings → Server → Threat intelligence.
- **Block IP** and **End sessions** call your own webhook (Settings → Server →
  Response), signed with HMAC-SHA256 when a secret is set.
- Thresholds are in Settings → Server → Security detectors. On D1, a longer
  scan interval or shorter look-back reads fewer rows.
- `GET /api/security/audit/export` streams a hash-chained, signed audit log.

The Help drawer on Security lists every detector and when it fires.

## Deploy

The image `ghcr.io/styropyr0/visionary` is built for `linux/amd64` and
`linux/arm64`: `latest` from the default branch, `sha-<short>`, and semver tags
from `v*` git tags. It is distroless, runs as non-root and listens on 8090
(4317 too, if OTLP/gRPC is on). Run it behind HTTPS and turn on *Behind a
trusted proxy* in Settings → Server → Sessions.

### Several replicas

Run two or more servers on one PostgreSQL, MySQL or D1 database behind a load
balancer (SQLite: same machine only). Jobs that must run once (alerts, uptime
probes, rollups, retention, detectors) are held by one replica through a lease
in the database; another takes over within 40 seconds if it stops.

- `GET /api/healthz` is liveness; `GET /api/readyz` is readiness.
- Settings changes reach the other replicas within about 10 seconds.
- Rate limits are per replica, so the effective limit is the setting times
  the number of replicas.
- Settings → Server → Replicas lists replicas and the jobs each holds. Set
  `VISIONARY_REPLICA_ID` only when two could share a host name and port.

Examples: [`deploy/replicas/kubernetes.yaml`](deploy/replicas/kubernetes.yaml)
and [`deploy/replicas/compose.yaml`](deploy/replicas/compose.yaml) (two
containers behind Caddy).

## Security

- **Read-only queries.** Anything not built in (the analyst, edited SQL, saved
  panels) must be a single `SELECT`/`WITH` over Visionary's own tables, and
  runs read-only with a 30 s timeout (SQLite: `query_only`; D1 has no
  read-only mode, so the check is the guard).
- **Sessions** are AES-GCM encrypted, HttpOnly, SameSite=Strict cookies.
  Sign-in is rate-limited, and every change is checked against the role and
  written to the audit log.
- **Secrets live in the database** (`visionary_settings`), and the dashboard
  never sends one back. Anyone with the database credentials can read them,
  so treat those credentials (which your SDKs also hold) as admin access.
- **Data is redacted** before storage: auth headers, cookies and password-like
  fields in bodies and logs; SQL values become `?`. Add your own with
  `VISIONARY_LOG_REDACT_*`.
- **Ingest endpoints** need a key, cap body sizes and rate-limit per IP and key.
- **Uptime checks** can't reach private, loopback or metadata addresses, even
  through DNS or redirects, unless you allow it.

## Cost

On D1, which bills by rows read, panels show their rows read under Inspect.
Rollups keep long ranges cheap; a short range reads raw rows, so keep
auto-refresh for short ranges.

## Troubleshooting

- **`no database configured … collection is off`** in the service's log: the
  service has no `VISIONARY_DATABASE_URL` (or `D1_*`).
- **A connection error:** the URL is wrong or unreachable from the service. Go
  also needs the driver imported.
- **No error and no data:** the service and server use different databases.
  Check host, database name, schema and, for SQLite, the exact file path. Also
  try the 15m range with all services.
- **Browser or mobile events missing:** `401` is a wrong key, `429` the rate
  limit; check the site's origin is allowed.
- **`visionary: queue full`:** the database can't keep up. Use PostgreSQL or
  MySQL for thousands of requests a second.
- **A query plan stays pending:** no running service has an explainer for that
  database; see [Query plans](#query-plans).
- **D1 free-tier row limit hit:** keep Chart summaries on (Settings → Server →
  Other), use short ranges, scan detectors less often, check alert rules less
  often and keep less data. Or move to D1 paid, PostgreSQL or MySQL.
- **`.env` ignored:** comment lines must start with `#`; a line starting with
  `;` stops the whole file loading. It is read from the directory the server
  starts in.
- **Load test returns 429s:** you're hitting your own rate limiter; test from
  several addresses or raise the limit.

## Environment reference

Only the first rows of each table are needed. Everything else has a sensible
default, and most behaviour is set in the dashboard instead.

### Server

| Variable | Default | When you'd set it |
|---|---|---|
| `VISIONARY_DATABASE_URL` | — | Always, unless you use D1. |
| `D1_ACCOUNT_ID`, `D1_DATABASE_ID`, `D1_API_TOKEN` | — | For D1 instead of a URL. The token needs D1 edit access. |
| `D1_API_BASE` | `https://api.cloudflare.com/client/v4` | A proxy in front of the D1 API. |
| `VISIONARY_ADDR` | `:8090` | The platform picks the port. |
| `VISIONARY_ENABLE_AUTH` | `true` | `false` to get back in when sign-in is broken. |
| `VISIONARY_REPLICA_ID` | host name and port | Two replicas could share a host name and port. |
| `VISIONARY_PARTITIONING` | from Settings → Data | `daily` to partition on PostgreSQL before anything is saved (setup as code). |
| `VISIONARY_CHROME` | found automatically | The browser for browser checks is somewhere unusual. |
| `VISIONARY_VERSION`, `VISIONARY_COMMIT` | from the build | Shown in About; change nothing else. |

### Server SDKs

| Variable | Default | When you'd set it |
|---|---|---|
| `VISIONARY_DATABASE_URL` | — | Always, unless your code passes its own connection or you use D1. |
| `D1_ACCOUNT_ID`, `D1_DATABASE_ID`, `D1_API_TOKEN` | — | For D1. |
| `VISIONARY_SERVICE` | none | Always; it names the service. |
| `VISIONARY_RELEASE` | the platform's commit | Your platform isn't detected, or you want a version name. |
| `VISIONARY_INSTANCE` | hostname | Hostnames are not meaningful. |
| `VISIONARY_LOG_REQUESTS` | `true` | `false` to stop recording requests. |
| `VISIONARY_LOG_PATH_PREFIX` | all paths | Record only e.g. `/api`. |
| `VISIONARY_LOG_BODIES` | `true` | `false` to not store request and response bodies. |
| `VISIONARY_LOG_MAX_BODY_BYTES` | `8192` | Keep more or less of each body. |
| `VISIONARY_LOG_REDACT_HEADERS` | | More headers to redact (added to Authorization, Cookie, Set-Cookie, X-Api-Key…). |
| `VISIONARY_LOG_REDACT_FIELDS` | | More body keys to redact (added to password, token, secret, otp, api_key…). |
| `VISIONARY_LOG_REDACT_REQUEST_FIELDS` | | Keys redacted in requests only. |
| `VISIONARY_LOG_ERROR_CODE_FIELD`, `VISIONARY_LOG_ERROR_KEY_FIELD` | `error.code`, `error.key` | Your error responses keep codes elsewhere. |
| `VISIONARY_SPANS` | `true` | `false` to stop recording queries and outbound calls. |
| `VISIONARY_SPAN_THRESHOLD_MS` | `100` | Calls outside any request (jobs) faster than this are dropped. |
| `VISIONARY_SPAN_MAX` | `100` | Calls kept per request. |
| `VISIONARY_ERRORS` | `true` | `false` to stop capturing exceptions. |
| `VISIONARY_LOGS` | `true` | `false` to ignore the log capture set up in code. |
| `VISIONARY_LOG_LEVEL` | `info` | Level used until Settings → Logs is read. |
| `VISIONARY_LOG_CAPTURE` | `false` | Java: `true` to capture Logback logs. |
| `VISIONARY_METRICS` | `true` | `false` to stop custom metrics. |
| `VISIONARY_METRICS_INTERVAL` | `15s` | How often metrics are written. |
| `VISIONARY_METRICS_MAX_SERIES` | `1000` | Label sets allowed per metric name. |
| `VISIONARY_SYSTEM_METRICS` | `true` | `false` to stop host and process metrics. |
| `VISIONARY_SYSTEM_INTERVAL` | `15s` | How often the host is sampled. |
| `VISIONARY_EXPLAIN` | `true` | `false` to refuse query plan requests (needs an explainer in code anyway). |
| `VISIONARY_PROFILE` | follow Settings → Profiling | `false` so this service never profiles. |
| `VISIONARY_LOG_RETENTION_DAYS`, `VISIONARY_SYSTEM_RETENTION_DAYS` | `0` (off) | Only when no Visionary server reads this database; the SDK then deletes its own old rows. |

Browser and mobile SDKs use no variables: the server URL and client key go in
`init()`. OpenTelemetry uses the standard `OTEL_*` variables.

### Host agent

Each variable has a matching flag (`-url`, `-key`, …). The install script
writes them to `/etc/visionary-agent.env`.

| Variable | Default | When you'd set it |
|---|---|---|
| `VISIONARY_URL` | — | Always: the Visionary server. |
| `VISIONARY_KEY` | — | Always: a server key. |
| `VISIONARY_HOST` | host name | Show the machine under another name. |
| `VISIONARY_SERVICE` | none | Tie the machine to a service. |
| `VISIONARY_AGENT_LOGS` | none | Ship logs: `journald`, `docker`, file globs like `/var/log/nginx/*.log`. |
| `VISIONARY_AGENT_STATE` | `/var/lib/visionary-agent` | Where log positions are kept. |
| `VISIONARY_AGENT_INTERVAL`, `VISIONARY_AGENT_REPORT` | `15s`, `1m` | Sample and report less or more often. |
| `VISIONARY_AGENT_UNITS` | | systemd services to watch, comma-separated. |
| `VISIONARY_DISK_PATHS` | all mounts | Report only some mounts. |
| `VISIONARY_AGENT_DOCKER` | auto | A container socket path, or `off`. |
| `VISIONARY_AGENT_AUTH_LOG` | auto | A log file, `journald` or `off`. |
| `VISIONARY_AGENT_SECURITY` | `true` | `false` turns off all security findings. |
| `VISIONARY_AGENT_SSH_KEYS`, `VISIONARY_AGENT_FILE_INTEGRITY`, `VISIONARY_AGENT_PROCESS_CHECKS`, `VISIONARY_AGENT_CONTAINER_RISKS` | `true` | Turn off one kind of security check. |
| `VISIONARY_AGENT_LOCATION` | none | Run as an uptime location (with that location's key) instead of reporting the host. |
| `VISIONARY_AGENT_ALLOW_PRIVATE` | the server's setting | Location mode: `true` or `false`, may checks reach private addresses. |
