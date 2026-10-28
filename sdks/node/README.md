# visionary-sdk (Node.js)

The Node.js SDK for [Visionary](../../Readme.md). Add one middleware and every
request your service handles shows up in the dashboard, with its route,
status, time, user, errors, database queries and outbound calls, plus CPU,
memory and GC samples of the process.

The SDK writes straight into a database (PostgreSQL, MySQL, SQLite or
Cloudflare D1) that the Visionary server reads. Writes are batched off the
request path, so requests are never blocked or slowed.

## Quick start

About 10 minutes. You need a running Visionary server (see
[Get started](../../Readme.md#get-started)) and its database URL.

**1. Install.** Node 18.18 or later. Add the driver for your database too:

```sh
npm install visionary-sdk
npm install pg              # PostgreSQL; or mysql2 for MySQL, better-sqlite3 for SQLite; nothing for D1
```

**2. Point it at the database.** Use the same database as the Visionary
server:

```sh
VISIONARY_DATABASE_URL=postgres://user:pass@host:5432/visionary
VISIONARY_SERVICE=my-api          # any name; tells services apart
```

**3. Add the middleware.** Call `init()` once at startup and mount the
middleware before your routes (Express shown; other frameworks are below):

```js
import express from "express";
import visionary from "visionary-sdk";

visionary.init();
const app = express();
app.use(visionary.express());            // first, so it sees every request
app.use(express.json());
// ... your routes ...
app.use(visionary.expressErrorHandler()); // after the routes
const server = app.listen(3000);
```

**4. Flush on shutdown.** Queued rows are flushed when the event loop
empties. On SIGTERM, stop the server, then:

```js
process.on("SIGTERM", () => server.close(() => visionary.close())); // waits at most 10 s
```

**5. Check it works.** Start the service. It logs:

```text
visionary: logging requests (path_prefix="", bodies=true, retention_days=0)
visionary: recording system metrics every 15s (instance my-host)
```

Send a request. It shows up on **API overview** and **Requests** within
a few seconds, and the process shows up on **System**. If you see
`visionary: no database configured ...; collection is off` instead, step 2
did not reach the process.

Optional next steps, each a line or two: [the signed-in user](#the-signed-in-user),
[database queries](#database-queries), [Redis and other calls](#redis-queues-and-your-own-code),
[query plans](#query-plans).

## Frameworks

Every integration names the request by its route pattern (`/users/:id`), so
`/users/42` and `/users/43` count as one route. Mount it first. Errors with a
status below 500 (http-errors, Fastify validation, Nest `HttpException`) are
the app answering, not failures, and are not recorded as errors.

### Express

Shown in the quick start. The route is `req.baseUrl + req.route.path`
(`/api/users/:id`). `expressErrorHandler()` records the error and calls
`next(err)`, so your own error handler still answers.

### Fastify

```js
import Fastify from "fastify";
import visionary from "visionary-sdk";

visionary.init();
const app = Fastify();
await app.register(visionary.fastify()); // applies to the whole app, like fastify-plugin
```

The route is `request.routeOptions.url` (`routerPath` before Fastify 4.10).
Errors that reach `onError` are recorded; Fastify answers them as before.

### Koa

```js
import Koa from "koa";
import visionary from "visionary-sdk";

visionary.init();
const app = new Koa();
app.use(visionary.koa()); // first
```

The route is `ctx._matchedRoute` from `@koa/router` or `koa-router`. Errors
that propagate through the middleware are recorded and rethrown.

### NestJS

Nest runs on Express or Fastify, so requests are logged by that adapter's
integration. `VisionaryModule` calls `init()` and adds a global interceptor
that records controller exceptions and rethrows them unchanged.

```ts
// app.module.ts
import { VisionaryModule } from "visionary-sdk";

@Module({ imports: [VisionaryModule.forRoot()] }) // forRoot(options) passes options to init()
export class AppModule {}
```

```ts
// main.ts, Express adapter
import visionary from "visionary-sdk";
const app = await NestFactory.create(AppModule);
app.use(visionary.express());

// main.ts, Fastify adapter
const app = await NestFactory.create<NestFastifyApplication>(AppModule, new FastifyAdapter());
await app.register(visionary.fastify());
```

The interceptor is also exported as `VisionaryInterceptor`, for
`app.useGlobalInterceptors(...)`.

### Plain `node:http`

There is no router, so name the route yourself:

```js
import http from "node:http";
import visionary from "visionary-sdk";

visionary.init();
http.createServer(visionary.wrap((req, res) => {
  visionary.setRoute("/users/:id");
  res.end("ok");
})).listen(3000);
```

Exceptions thrown by the handler are recorded and rethrown.

### Other frameworks

Any framework built on `node:http` can use `wrap` around its request
listener. `visionary.setRoute(pattern)` sets the route from inside the
request when the integration cannot see the router. The integrations
`express()`, `fastify()`, `koa()` and `wrap(handler)` each take an optional
`Agent` as their last argument, for apps that build their own (`new
visionary.Agent(options).start()`) instead of using `init()`.

## The signed-in user

Attach the user from your auth middleware. Only `id` is required:

```js
visionary.setUser({ id: user.id, handle: user.email, role: user.role });
```

It uses `AsyncLocalStorage`, so it works from any code running for the
request. When `setUser` was not called, the integrations use the framework's
user if there is one (`req.user` from Passport or @fastify/jwt, Koa's
`ctx.state.user`): `id` (or `_id`, `sub`), `username` or `email` as the
handle, and `role`.

To fill in handles and roles for ids logged without them:

```js
visionary.init({ lookupUser: async (id) => ({ handle, role }) }); // keep it cheap (a cache)
```

## Login attempts

In the login handler, record the account the request tried, whether or not
it exists or the password was right. The security detectors use it to spot
one IP trying many accounts, many IPs trying one account, and a signed-in
user trying someone else's:

```js
app.post("/login", async (req, res) => {
  visionary.loginAttempt(req.body.email);
  const user = await authenticate(req.body.email, req.body.password);
  if (!user) return res.status(401).json({ error: "invalid credentials" });
  res.json({ token: issueToken(user) });
});
```

The value is trimmed and clipped to 255 characters; outside a logged request
it does nothing.

It runs on the writer, once per distinct user per batch.

## Database queries

The SDK does not patch drivers on its own. Add one line where the client or
pool is created; each request's breakdown then lists its queries on a
timeline, and **Queries** and **Performance** get slowest statements and N+1
detection.

**pg (node-postgres)**

```js
import pg from "pg";
visionary.instrumentPg(pg);          // every Client and Pool; or instrumentPg(pool)
```

**mysql2**

```js
import mysql from "mysql2/promise";
const pool = visionary.instrumentMysql2(mysql.createPool(url)); // pool or connection, callback or promise API
```

**better-sqlite3 or node:sqlite**

```js
import Database from "better-sqlite3";
const db = visionary.instrumentSqlite(new Database("app.db")); // also a node:sqlite DatabaseSync
```

**Prisma**

```js
const prisma = new PrismaClient().$extends(visionary.prismaExtension()); // prismaExtension({ target }) to rename
```

What is recorded: each statement as a span named `SELECT users`,
`INSERT bookings`, with the statement sanitized (literals become `?`,
`IN (...)` lists collapse; parameter values are never stored), and its
duration and row count. The span's target is `postgresql/<dbname>`,
`mysql/<dbname>`, `sqlite/<file>` or `prisma`. Until a database is
instrumented, a request's database totals stay empty ("cannot see"), not
zero.

## Outbound HTTP

Nothing to add. `fetch` and `undici` (through their diagnostics channels) and
`http`/`https` (axios, got, node-fetch and most API clients, including the
AWS, Stripe and OpenAI SDKs) are recorded automatically. Each call is a span
named `POST api.stripe.com`, ending when the response headers arrive. Turn it
off with `init({ instrumentHttp: false })`.

## Redis, queues and your own code

There is no Redis or queue hook. Wrap the call in a span, sync or async:

```js
const user = await visionary.span("cache", "GET user", () => redis.get(key));
await visionary.span("queue", "publish order.created", () => channel.publish(msg), { target: "rabbitmq" });
const hash = visionary.span("custom", "hash password", () => bcrypt.hashSync(pw, 10));
```

Kinds are `db`, `http`, `cache`, `queue`, `custom`, `graphql` and
`background`. Options: `target` and `detail`. A thrown error marks the span
failed and is rethrown. Time no span covers shows as **Untracked** in the
request's breakdown; see
[What the middleware sees](../../Readme.md#what-the-middleware-sees-and-what-needs-code).

## Work after the response

Work a request starts and doesn't wait for stays linked to it. Name it with
`background` and it shows as one span in the request's breakdown, with when
it finished; its calls appear marked as after the response and are left out
of the request's totals:

```js
visionary.background("send welcome email", () => mailer.send(user.email));
```

## GraphQL

Each operation becomes a span named after it ("query GetUser") with its
document, so **Queries** can show its depth, field count and the queries its
resolvers ran.

```js
new ApolloServer({ typeDefs, resolvers, plugins: [visionary.apolloPlugin()] });

// any other server
const result = await visionary.graphqlOperation("query", operationName, source, () => execute(args));
```

## Errors

Exceptions that reach an integration are recorded (`handled = 0`) and then
continue exactly as before, so the framework's normal 500 is unchanged. To
record one you handled:

```js
try { ... } catch (err) {
  visionary.captureException(err); // handled = 1, linked to the current request
}
```

Errors are grouped into issues on **Errors**. The fingerprint is the first 16
hex characters of SHA-1 over the type and the top five application frames
(`function@file`, files relative to the working directory, `node_modules`
and Node internals skipped). At most 20 rows per fingerprint per minute are
written.

Uncaught exceptions are recorded with `uncaughtExceptionMonitor`, which does
not change whether the process crashes. Unhandled rejections are recorded the
same way when they crash the process; if your app already listens for
`unhandledRejection` at `init()`, the SDK listens too. A crashing process
cannot wait for a network write, so that last row is only guaranteed on
SQLite. Turn this off with `init({ captureUncaught: false })`.

## Logs

Your application's own log lines can go to **Logs** too, linked to the
request they were logged in (its request id, the trace id from a
`traceparent` header, and the signed-in user). Route them to the SDK once,
after `init()`; what your service prints does not change:

```js
visionary.captureConsole();                                     // console.log/info/warn/error/debug/trace

const log = pino(visionary.pinoStream());                       // pino: still prints to stdout

const logger = winston.createLogger({                           // winston: an extra transport
  level: "debug",
  transports: [new winston.transports.Console({ level: "info" }), visionary.winstonTransport()],
});
```

By default info and above are kept. Debug lines are held in their request
and kept only when it fails (a 5xx or a recorded exception) or takes at
least a second, so every failure comes with the lines that led to it; the
rest are dropped. So it pays to give the SDK your debug lines while printing
only info, as the winston example does; with pino, use a multistream:

```js
const log = pino({ level: "debug" }, pino.multistream([
  { level: "info", stream: process.stdout },
  { level: "debug", stream: visionary.pinoStream(null) },  // null: capture only, print nothing
]));
```

`console.log` lines are info unless they start with a level (`ERROR ...`,
`[warn] ...`, `level=debug`); `console.debug`, `warn` and `error` keep theirs.

**Settings → Logs** in the dashboard changes this live, checked every 30
seconds: the level, the buffer for failed and slow requests, a louder or
quieter level for one route or user for a while, rules that drop noisy
lines, and a cap of lines a minute (6000 by default). Passwords, tokens,
keys and JWTs are scrubbed from messages and fields before anything is
stored. `VISIONARY_LOGS=false` turns capture off; `VISIONARY_LOG_LEVEL` is the
level until the dashboard's setting has been read.

## Query plans

On **Queries**, **Explain** asks for a statement's plan. Visionary never
connects to your database: the server queues the request, and your service
runs `EXPLAIN` (never `EXPLAIN ANALYZE`, so nothing is executed) and sends
the plan back within about 10 seconds. Give the SDK the pool to plan on:

```js
const pool = new pg.Pool({ connectionString: process.env.DATABASE_URL });
visionary.instrumentPg(pool);
visionary.init({ explain: [visionary.explainWith(pool)] }); // pg Pool or Client, or a mysql2 pool
```

The explainer only answers statements whose span target matches its own
(`postgresql/<dbname>` or `mysql/<dbname>`, worked out the same way the query
hooks name it). If they differ, pass it: `explainWith(pool, { target:
"postgresql/shop" })`. PostgreSQL 16+ plans stored statements as they are;
MySQL and older PostgreSQL plan them with NULLs, and the plan says so.
`VISIONARY_EXPLAIN=false` turns it off without a code change.

## CPU profiles

Off until someone turns them on in the dashboard (**Settings → Profiling**),
for every service or only the ones chosen there. No redeploy either way;
the SDK checks every 30 seconds and logs `visionary: CPU profiles on, every
300s while serving requests` or `visionary: CPU profiles off`.

While on, the SDK samples the CPU with Node's built-in inspector (every 5
minutes for 10 seconds by default, both set in the dashboard), only while
requests are being served, and records how long each route's requests took
meanwhile. **Profile now** on **CPU profiles** takes one at once. Sampling
costs a few percent of CPU while it runs and nothing in between.

`VISIONARY_PROFILE=false` (or `init({ profile: false })`) makes a service
ignore the dashboard and never profile.

## System metrics

On by default. Every 15 seconds a sample is taken from `os`, `process`, `v8`,
`fs.statfs` and `perf_hooks` (GC count and pause time). On Linux the
node-level columns (CPU breakdown with iowait/steal, memory available and
cached, swap, disk and network I/O, TCP, processes, context switches,
process I/O, threads, file descriptors) come from `/proc`. Columns a
platform cannot report are empty; on Windows and macOS that is most of the
I/O columns.

The `goroutines` column holds Node's active handles, requests and timers.
`heap_alloc_mb` and `heap_inuse_mb` are V8's used and total heap.

To fill the database pool columns:

```js
visionary.init({ dbStats: visionary.pgPoolStats(pool) }); // or mysqlPoolStats(pool), or your own function
```

## Service and release

`VISIONARY_SERVICE` names the service, so several can share one database.
The release is `VISIONARY_RELEASE`, else the platform's commit variable
(`RAILWAY_GIT_COMMIT_SHA`, `RENDER_GIT_COMMIT`, `VERCEL_GIT_COMMIT_SHA`,
`HEROKU_SLUG_COMMIT`, `SOURCE_VERSION`, `GIT_COMMIT`, `GITHUB_SHA`,
`K_REVISION`), commit hashes shortened to 12 characters. When it is known, a
`releases` row is written at start and the dashboards mark the deploy.

## Choosing the database

The app and the Visionary server must use the same database. The SDK takes
the first of these that is set:

1. **A pool your app already has**: `init({ db })` with a `pg.Pool`, a
   `mysql2` pool (callback or promise API), a `better-sqlite3` Database or a
   `node:sqlite` DatabaseSync. The dialect is detected; pass `dialect:
   "postgres" | "mysql" | "sqlite"` to be explicit. `close()` does not end it.
2. **`VISIONARY_DATABASE_URL`** (or `init({ databaseUrl })`).
3. **`D1_ACCOUNT_ID`, `D1_DATABASE_ID` and `D1_API_TOKEN`**.

| Database | `VISIONARY_DATABASE_URL` | Driver to install |
|---|---|---|
| PostgreSQL | `postgres://user:pass@host:5432/db` (or `postgresql://`) | `pg` |
| MySQL 8 / MariaDB | `mysql://user:pass@host:3306/db` | `mysql2` |
| SQLite | `sqlite:///var/lib/app/visionary.db`, `sqlite:///C:/data/visionary.db` on Windows, `sqlite://relative.db` | `better-sqlite3`, or none on Node 22.5+ (`node:sqlite`) |
| Cloudflare D1 | leave it unset, or `d1://`, and set `D1_ACCOUNT_ID`, `D1_DATABASE_ID`, `D1_API_TOKEN` | none (REST API over `fetch`) |

`d1://ACCOUNT/DATABASE` overrides the two ids; the token still comes from
`D1_API_TOKEN`. The D1 token needs D1 edit access. SQLite works only when the
app and the server run on the same host.

Tables are created at startup from the schema in `schema/`, in the
background, so a slow database never delays boot; older tables gain the
columns added since.

## Configuration

Everything is optional. Options passed to `init()` win over the environment.

| Variable | `init()` option | Default | What it does |
|---|---|---|---|
| `VISIONARY_DATABASE_URL` | `databaseUrl` | | Where rows go; see above. |
| `D1_ACCOUNT_ID`, `D1_DATABASE_ID`, `D1_API_TOKEN`, `D1_API_BASE` | `d1: { accountId, databaseId, apiToken, apiBase }` | | Cloudflare D1. `D1_API_BASE` overrides the API URL. |
| | `db`, `dialect` | | A pool or connection your app already has. |
| `VISIONARY_SERVICE` | `service` | empty | Service name on every row. |
| `VISIONARY_RELEASE` | `version` | platform commit | Release on every row and the deploy marker. |
| `VISIONARY_INSTANCE` | `instance` | hostname | Labels this process on **System**. |
| `VISIONARY_LOG_REQUESTS` | `logRequests` | `true` | Request logging on or off. |
| `VISIONARY_LOG_PATH_PREFIX` | `pathPrefix` | empty (all) | Only log paths starting with this, e.g. `/api`. |
| `VISIONARY_LOG_BODIES` | `captureBodies` | `true` | Store request and response bodies (redacted). |
| `VISIONARY_LOG_MAX_BODY_BYTES` | `maxBodyBytes` | `8192` | Body bytes kept per body. |
| `VISIONARY_LOG_REDACT_HEADERS` | `redactHeaders` | | Comma list of headers added to the redacted defaults. |
| `VISIONARY_LOG_REDACT_FIELDS` | `redactFields` | | JSON/form keys added to the redacted defaults. |
| `VISIONARY_LOG_REDACT_REQUEST_FIELDS` | `redactRequestFields` | | Keys redacted in request bodies only. |
| `VISIONARY_LOG_ERROR_CODE_FIELD` | `errorCodeField` | `error.code` | Where error responses keep their code (dotted JSON path). |
| `VISIONARY_LOG_ERROR_KEY_FIELD` | `errorKeyField` | `error.key` | The same, for the error key. |
| `VISIONARY_LOG_RETENTION_DAYS` | `retentionDays` | `0` (off) | Only for a database no Visionary server reads: the SDK then deletes its own api_logs, spans, errors, profiles and app_logs older than this. |
| `VISIONARY_SYSTEM_METRICS` | `systemMetrics` | `true` | System samples on or off. |
| `VISIONARY_SYSTEM_INTERVAL` | `systemInterval` (seconds) | `15s` | Time between samples: `15`, `500ms`, `1m`. |
| `VISIONARY_SYSTEM_RETENTION_DAYS` | `systemRetentionDays` | `0` (off) | The same as the log retention, for system_metrics. |
| `VISIONARY_SPANS` | `spans` | `true` | Database and outbound call spans and totals. |
| `VISIONARY_SPAN_THRESHOLD_MS` | `spanThresholdMs` | `100` | Keep calls made outside a request (jobs, startup) at least this slow. |
| `VISIONARY_SPAN_MAX` | `spanMax` | `100` | Spans kept per request; later calls still count in the totals. |
| `VISIONARY_ERRORS` | `errors` | `true` | Error capture on or off. |
| `VISIONARY_EXPLAIN` | (`explain` takes the explainers) | `true` | `false` ignores the explainers passed to `init`. |
| `VISIONARY_PROFILE` | `profile` | `true` (follow the dashboard) | `false` never profiles. |
| `VISIONARY_LOGS` | `logs` | `true` | Application log capture, where you route logs to the SDK ([Logs](#logs)). |
| `VISIONARY_LOG_LEVEL` | `logLevel` | `info` | Lowest log level kept until **Settings → Logs** is read. |
| | `instrumentHttp` | `true` | Record outbound `fetch`/`undici`/`http`/`https` calls. |
| | `captureUncaught` | `true` | Record uncaught exceptions and crashing rejections. |
| | `explain` | `[]` | Explainers for query plans (`explainWith(pool)`). |
| | `dbStats` | | Pool stats for the `db_*` columns (`pgPoolStats(pool)`). |
| | `diskPath` | working directory | Filesystem reported in the disk columns. |
| | `lookupUser` | | `(id) => ({ handle, role })`, see [the signed-in user](#the-signed-in-user). |
| | `logger` | console | Anything with `info`, `warn` and `error`. |

Booleans accept `true/false/1/0/yes/no`. Old rows are normally deleted by the
Visionary server, following **Settings → Data** in the dashboard.

Redaction: sensitive headers (`Authorization`, `Cookie`, `Set-Cookie`,
`X-Api-Key`, ...) and JSON and form fields (`password`, `token`, `api_key`,
`card_number`, ...) become `[REDACTED]` before anything is stored, also in
truncated bodies.

## API

| Call | What it does |
|---|---|
| `init(options?)` | Builds, starts and returns the process-wide agent. Never throws; with no database it does nothing. |
| `close(timeoutMs = 10000)` | Flushes queued rows and stops. |
| `getAgent()` | The agent from `init()`, if any. |
| `express()`, `expressErrorHandler()`, `fastify()`, `koa()`, `wrap(handler)` | Framework integrations. |
| `VisionaryModule.forRoot(options?)`, `VisionaryInterceptor` | NestJS. |
| `setUser({ id, handle?, role? })`, `setRoute(pattern)`, `loginAttempt(account)` | Annotate the current request. |
| `captureException(err)` | Record a handled error. |
| `captureConsole()`, `pinoStream(next?)`, `winstonTransport()` | Send application logs to **Logs**. `captureConsole` returns a function that undoes it. |
| `span(kind, name, fn, { target?, detail? })`, `background(name, fn)` | Custom spans. |
| `instrumentPg`, `instrumentMysql2`, `instrumentSqlite`, `prismaExtension()` | Database hooks. |
| `apolloPlugin()`, `graphqlOperation(type, name, source, fn)` | GraphQL. |
| `explainWith(db, { target? })` | Query plan explainer. |
| `pgPoolStats(pool)`, `mysqlPoolStats(pool)` | Pool stats for system metrics. |

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| Nothing shows up; the log says `no database configured ... collection is off` | `VISIONARY_DATABASE_URL` (or the D1 variables) is not set in the process. | Set it where the service runs, or pass `databaseUrl`/`db` to `init()`. |
| Nothing shows up; no `visionary:` lines at all | `init()` and the middleware are not reached, or the middleware is mounted after the routes. | Call `init()` at startup and mount the middleware first. |
| Nothing shows up, but the SDK logs fine | The app and the server use different databases. | Point both at the same database (same host, database name and, for D1, the same database id). |
| `... needs the pg package: npm install pg` (or mysql2, better-sqlite3) | The driver for the URL scheme is missing. | Install it, or use Node 22.5+ for SQLite. |
| `create ... tables failed (will retry)` | The database user cannot create tables, or the database is down. | Grant `CREATE`, or check the connection. It retries on the next write. |
| Requests show but have no database queries | The driver is not instrumented. | Add the line from [Database queries](#database-queries). |
| Some requests show large **Untracked** time | Redis, queue clients or CPU work are not wrapped. | Wrap them in `visionary.span(...)`. |
| **Explain** on **Queries** stays pending forever | No running service was given an explainer for that database, or its target does not match the span's target. | Pass `explain: [visionary.explainWith(pool)]` to `init()`. If the span says `postgresql/shop` and the explainer guesses differently, pass `{ target: "postgresql/shop" }`. Check `VISIONARY_EXPLAIN` is not `false`. |
| No CPU profiles | Profiling is off in the dashboard, the service is not in the chosen list, `VISIONARY_PROFILE=false`, or no requests are being served. | Turn it on in **Settings → Profiling** and check the service name matches `VISIONARY_SERVICE`. Look for `visionary: CPU profiles on` in the log. |
| `batch write failed ... exceeded D1's free tier daily row read limit` | Cloudflare D1's free plan allows a fixed number of row reads and writes a day; the SDK's writes and the dashboard's reads count. | Move to the Workers Paid plan, or to PostgreSQL or MySQL. Until then, write less: `VISIONARY_LOG_PATH_PREFIX=/api`, `VISIONARY_LOG_BODIES=false`, a longer `VISIONARY_SYSTEM_INTERVAL`. |
| `queue full; N rows dropped` | Writes cannot keep up (common on SQLite or D1 under heavy traffic). | Use PostgreSQL or MySQL for thousands of requests a second. |
| The last requests before a deploy are missing | The process exited before the queue was flushed. | Call `await visionary.close()` after the server stops, on SIGTERM. A `flush timed out; N rows pending` line means 10 s was not enough; pass a longer timeout. |
| `/users/42` and `/users/43` show as separate routes | No route pattern (plain `http`, or a router the integration cannot see). | Call `visionary.setRoute("/users/:id")` in the handler. |

## Building

```sh
npm install
npm run build      # tsup: dist/index.js (ESM), dist/index.cjs, types
npm run typecheck
npm test           # node:test, against a bundle of the tests in test-dist/
```

Every Visionary SDK follows the same contract: [`sdks/SPEC.md`](../SPEC.md).
