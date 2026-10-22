# visionary-sdk (Node.js)

The Node.js collector for [Visionary](../../Readme.md). It runs inside your
service and writes:

- one row per HTTP request into `api_logs` (method, route pattern, status,
  timing, user, redacted headers and bodies, error codes, and how many
  database and outbound HTTP calls the request made),
- a periodic host / process / runtime sample into `system_metrics`,
- `spans` for database statements and outbound HTTP calls of slow or failed
  requests, `errors` for exceptions, and a `releases` row per deploy.

All writes are batched off the request path, so requests are never blocked or
slowed. If the database is down, the SDK only writes log lines. With no
database configured it logs one warning and does nothing.

Every Visionary SDK behaves the same way. The contract is
[`sdks/SPEC.md`](../SPEC.md); the Go SDK is the reference.

## Install

```sh
npm install visionary-sdk
```

The npm name is `visionary-sdk`. It needs Node 18.18+, ships ESM and CommonJS
builds with TypeScript types, and has **no runtime dependencies**. Database
drivers are optional peer dependencies: the SDK uses the one your app already
has.

| Database | Driver |
|---|---|
| Cloudflare D1 | none (REST API over `fetch`) |
| SQLite | `better-sqlite3`, else `node:sqlite` (Node 22.5+) |
| PostgreSQL | `pg` |
| MySQL 8 / MariaDB | `mysql2` |

## Quickstart

Configure a database, for example:

```sh
export VISIONARY_DATABASE_URL=postgres://user:pass@localhost:5432/app
```

Then call `visionary.init()` once at startup and add the integration for your
framework. Mount it first, so it sees every request and the final status.

### Express

```js
import express from "express";
import visionary from "visionary-sdk";

visionary.init();
const app = express();
app.use(visionary.express());
app.use(express.json());
// ... routes ...
app.use(visionary.expressErrorHandler()); // after the routes: records errors, then next(err)
```

The route is `req.baseUrl + req.route.path` (`/api/users/:id`).

### Fastify

```js
import Fastify from "fastify";
import visionary from "visionary-sdk";

visionary.init();
const app = Fastify();
await app.register(visionary.fastify()); // applies to the whole app, like fastify-plugin
```

The route is `request.routeOptions.url` (`routerPath` on older versions).
Errors that reach Fastify's `onError` are recorded; Fastify answers them as
before.

### Koa

```js
import Koa from "koa";
import visionary from "visionary-sdk";

visionary.init();
const app = new Koa();
app.use(visionary.koa()); // first
```

The route is `ctx._matchedRoute` from `@koa/router` / `koa-router`. Errors that
propagate through the middleware are recorded and rethrown.

### NestJS

Nest runs on Express or Fastify, so request logging comes from the adapter's
integration; `VisionaryModule` adds a global interceptor that records
controller exceptions (HttpExceptions below 500 are skipped) and rethrows them
unchanged.

```ts
// app.module.ts
import { VisionaryModule } from "visionary-sdk";

@Module({ imports: [VisionaryModule.forRoot()] })
export class AppModule {}

// main.ts (Express adapter)
import visionary from "visionary-sdk";
const app = await NestFactory.create(AppModule);
app.use(visionary.express());

// main.ts (Fastify adapter)
const app = await NestFactory.create<NestFastifyApplication>(AppModule, new FastifyAdapter());
await app.register(visionary.fastify());
```

`VisionaryModule.forRoot(options)` calls `init(options)`. The interceptor is
also exported as `VisionaryInterceptor` for `app.useGlobalInterceptors(...)`.

### Plain `node:http`

```js
import http from "node:http";
import visionary from "visionary-sdk";

visionary.init();
http.createServer(visionary.wrap((req, res) => {
  visionary.setRoute("/users/:id"); // no router, so name the route yourself
  res.end("ok");
})).listen(3000);
```

### Shutdown

Queued rows are flushed when the event loop empties. On SIGTERM, stop the
server first, then:

```js
await visionary.close(); // flushes, waiting at most 10 s
```

## The user

Attach the signed-in user from your auth middleware; only the id is required:

```js
visionary.setUser({ id: user.id, handle: user.email, role: user.role });
```

It uses `AsyncLocalStorage`, so it works from any code running for the
request (middleware, handlers, async functions). When `setUser` was not
called, the integrations use the framework's user if there is one
(`req.user` from Passport or @fastify/jwt, Koa's `ctx.state.user`): `id`
(or `_id`, `sub`), `username`/`email` as the handle and `role`.

`init({ lookupUser: async (id) => ({ handle, role }) })` fills in what is
missing on the writer, once per distinct user per batch.

## Spans

The middleware records the request; the calls inside it are recorded only when
their client is hooked. `http`, `https` and `fetch` are patched for you, so
most vendor SDKs (AWS, Stripe, OpenAI) are covered. Wrap what isn't a call:
Redis or queue clients without a hook, and CPU work such as hashing or image
processing, in `visionary.span(...)`. Anything left shows as **Untracked** in
the request's breakdown; see [What the middleware sees](../../Readme.md#what-the-middleware-sees-and-what-needs-code).

For every logged request the SDK counts database and outbound HTTP calls and
their total time into `db_count`, `db_ms`, `http_count`, `http_ms`, and up to
`VISIONARY_SPAN_MAX` (100) `spans` rows, in start order. Calls outside a
request are stored when they take at least `VISIONARY_SPAN_THRESHOLD_MS` (100).

Outbound HTTP is recorded automatically: `fetch` and `undici` through their
diagnostics channels, and `http`/`https` (axios, got, node-fetch, most API
clients). A span ends when the response headers arrive.

Databases need one line, as the SDK does not patch drivers on its own:

```js
import pg from "pg";
visionary.instrumentPg(pg);                // every Client and Pool; or instrumentPg(pool)

import mysql from "mysql2/promise";
const pool = visionary.instrumentMysql2(mysql.createPool(url)); // pool or connection, either API

import Database from "better-sqlite3";
const db = visionary.instrumentSqlite(new Database("app.db")); // also node:sqlite DatabaseSync

const prisma = new PrismaClient().$extends(visionary.prismaExtension());
```

Statements are stored sanitized (literals become `?`, `IN (...)` lists
collapse); parameter values are never stored. Spans are named
`SELECT users`, `INSERT bookings`, `POST api.stripe.com`. Until a database is
instrumented, `db_count` and `db_ms` stay NULL ("cannot see").

Anything else (cache, queue, your own steps):

```js
const user = await visionary.span("cache", "GET user", () => redis.get(key));
```

## Errors

Exceptions that reach the integrations are recorded with `handled = 0` and
then continue exactly as before, so the framework's normal 500 is unchanged.
Errors with a status below 500 (http-errors, Fastify validation, Nest
`HttpException`) are the app answering, not failures, and are skipped.

```js
try { ... } catch (err) {
  visionary.captureException(err); // handled = 1, linked to the current request
}
```

Uncaught exceptions are recorded with `uncaughtExceptionMonitor`, which does
not change whether the process crashes. Unhandled rejections are recorded the
same way when they crash the process; if your app already listens for
`unhandledRejection` at `init()`, the SDK listens too. A process that is about
to crash cannot wait for a network write, so that last row is only guaranteed
on SQLite (written synchronously).

The fingerprint is the first 16 hex characters of SHA-1 over the type and the
top five application frames (`function@file`, files relative to the working
directory, `node_modules` and Node internals skipped). At most 20 rows per
fingerprint per minute are written.

## Service and release

`VISIONARY_SERVICE` names the service; the release comes from
`VISIONARY_RELEASE` or the platform's commit variable (Railway, Render,
Vercel, Heroku, `GIT_COMMIT`, `GITHUB_SHA`, Cloud Run's `K_REVISION`), commit
hashes shortened to 12 characters. When the release is known, a `releases`
row (`sdk = node`, `runtime = Node.js 22.11.0`) is written at start.

## Choosing the database

In this order:

1. `init({ db })`: a `pg.Pool`, a `mysql2` pool (callback or promise API), a
   `better-sqlite3` Database or a `node:sqlite` DatabaseSync your app already
   has (preferred: your app controls the driver and pooling). The dialect is
   detected; pass `dialect: "postgres" | "mysql" | "sqlite"` to be explicit.
   `close()` does not end it.
2. `VISIONARY_DATABASE_URL`: `postgres://`, `postgresql://`, `mysql://`,
   `sqlite:///absolute/path.db` (`sqlite:///C:/data/app.db` on Windows),
   `sqlite://relative.db`, or `d1://` (uses the `D1_*` variables).
3. `D1_ACCOUNT_ID`, `D1_DATABASE_ID`, `D1_API_TOKEN` (and optionally
   `D1_API_BASE`): Cloudflare D1 over its REST API.

The tables are created at startup with the schema in `schema/` (a copy of
`sdks/go/schema`), and older tables gain the columns added since.

## System metrics

Every `VISIONARY_SYSTEM_INTERVAL` (15 s) a sample is taken from `os`,
`process`, `v8`, `fs.statfs` and `perf_hooks` (GC count and pause time from a
`gc` PerformanceObserver); on Linux the node-level columns (CPU breakdown
with iowait/steal, memory available/cached, swap, disk and network I/O, TCP,
processes, context switches, process I/O, threads, open and max file
descriptors) come from `/proc`. Columns a platform cannot report are NULL; on
Windows and macOS that is most of the I/O columns.

The `goroutines` column holds Node's concurrency units: active handles,
requests and timers (`process.getActiveResourcesInfo()`). `heap_alloc_mb` and
`heap_inuse_mb` are V8's used and total heap; `heap_objects` is NULL.

The `db_*` columns come from an optional callback:

```js
visionary.init({ dbStats: visionary.pgPoolStats(pool) }); // or mysqlPoolStats(pool), or your own
```

## Configuration

`init(options)` takes everything below as options too (camelCase, e.g.
`pathPrefix`, `spanThresholdMs`); options win over the environment.

| Variable | Default |
|---|---|
| `VISIONARY_DATABASE_URL` | see above |
| `D1_ACCOUNT_ID`, `D1_DATABASE_ID`, `D1_API_TOKEN`, `D1_API_BASE` | for D1 |
| `VISIONARY_INSTANCE` | hostname |
| `VISIONARY_SERVICE`, `VISIONARY_RELEASE` | empty / from the platform |
| `VISIONARY_LOG_REQUESTS` | `true` |
| `VISIONARY_LOG_PATH_PREFIX` | empty (log everything) |
| `VISIONARY_LOG_BODIES` | `true` |
| `VISIONARY_LOG_MAX_BODY_BYTES` | `8192` |
| `VISIONARY_LOG_RETENTION_DAYS` | `30` (also spans and errors; 0 keeps forever) |
| `VISIONARY_LOG_REDACT_HEADERS`, `_REDACT_FIELDS`, `_REDACT_REQUEST_FIELDS` | empty (comma lists, added to the defaults) |
| `VISIONARY_LOG_ERROR_CODE_FIELD`, `_ERROR_KEY_FIELD` | `error.code`, `error.key` |
| `VISIONARY_SYSTEM_METRICS` | `true` |
| `VISIONARY_SYSTEM_INTERVAL` | `15s` |
| `VISIONARY_SYSTEM_RETENTION_DAYS` | `30` |
| `VISIONARY_SPANS` | `true` |
| `VISIONARY_SPAN_THRESHOLD_MS` | `100` |
| `VISIONARY_SPAN_MAX` | `100` |
| `VISIONARY_ERRORS` | `true` |

Options only: `instrumentHttp: false` (no outbound HTTP spans),
`captureUncaught: false`, `logger` (anything with `info`/`warn`/`error`),
`diskPath`, `dbStats`, `lookupUser`.

Sensitive headers (`Authorization`, `Cookie`, `Set-Cookie`, `X-Api-Key`, ...)
and JSON/form fields (`password`, `token`, `api_key`, `card_number`, ...) are
replaced with `[REDACTED]` before anything is stored, also in truncated bodies.

## Building

```sh
npm install
npm run build      # tsup: dist/index.js (ESM), dist/index.cjs, types
npm run typecheck
```
