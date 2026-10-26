# Visionary for ASP.NET Core

Add one NuGet package and two lines to an ASP.NET Core app, and every request
shows up in the [Visionary](../../Readme.md) dashboard: route, status, time,
user, redacted bodies, the database and `HttpClient` calls it made, and any
exception. You also get host, process and GC metrics, and CPU profiles with an
optional second package.

- [Quick start](#quick-start)
- [Integration recipes](#integration-recipes)
- [Choosing the database](#choosing-the-database)
- [Configuration reference](#configuration-reference)
- [Troubleshooting](#troubleshooting)
- [How it works](#how-it-works)

## Quick start

You need .NET 8 or later, and a database the Visionary server also reads
(PostgreSQL, MySQL/MariaDB, a SQLite file or Cloudflare D1).

**1. Install the package** from
[nuget.org](https://www.nuget.org/packages/Visionary.AspNetCore), plus the
driver for your database:

```sh
dotnet add package Visionary.AspNetCore --version 0.1.0
dotnet add package Npgsql            # PostgreSQL; or MySqlConnector, or Microsoft.Data.Sqlite; none for D1
```

The package depends only on the ASP.NET Core shared framework. It finds the
driver at runtime.

**2. Point it at the database.** Use the same database as the Visionary
server:

```sh
export VISIONARY_DATABASE_URL=postgres://user:pass@db.internal:5432/visionary
export VISIONARY_SERVICE=my-api   # any name; lets several services share one database
```

For D1, set `D1_ACCOUNT_ID`, `D1_DATABASE_ID` and `D1_API_TOKEN` instead. Every
setting can also go in `appsettings.json` under the same key. See
[Choosing the database](#choosing-the-database).

**3. Register the services and add the middleware.**

```csharp
using Styropyr0.Visionary;

var builder = WebApplication.CreateBuilder(args);
builder.Services.AddVisionary();

var app = builder.Build();
app.UseVisionary();            // first, before exception handlers, auth and routing

app.MapGet("/users/{id}", (string id) => Results.Ok(new { id }));

app.Run();
```

**4. Start the app.** The log shows one line like:

```
visionary: writing to postgres://user@db.internal:5432/visionary (postgres); requests on (path prefix "", bodies True), spans True, errors True, service my-api, version -
```

The tables are created on first start, and older tables gain new columns. On
shutdown (Ctrl+C, SIGTERM, `app.StopAsync()`) the queue is written out before
the app exits.

**5. Send a request and check the dashboard.** The request appears on the API
overview within a few seconds, with its route (`/users/{id}`), status, time and
any error. Open it from Performance → Slowest requests to see its database and
HTTP calls on a timeline.

## Integration recipes

### Minimal APIs

**When:** apps built with `MapGet`, `MapPost` and route groups.

```csharp
builder.Services.AddVisionary();
var app = builder.Build();
app.UseVisionary();

app.MapGet("/users/{id}", (string id, HttpContext ctx) =>
{
    Visionary.SetUser(ctx, id, handle: "ann", role: "admin");
    return Results.Ok(new { id });
});
```

**Records:** one `api_logs` row per request, with the endpoint's route pattern
(`/users/{id}`; inline constraints such as `{id:int}` are dropped).

### Controllers

**When:** MVC or Web API controllers.

```csharp
builder.Services.AddControllers();
builder.Services.AddVisionary(o => o.PathPrefix = "/api");

var app = builder.Build();
app.UseVisionary();
app.UseAuthentication();
app.UseAuthorization();
app.MapControllers();
app.Run();

[ApiController]
[Route("api/[controller]")]
public class OrdersController : ControllerBase
{
    [HttpGet("{id:int}")]
    public IActionResult Get(int id)
    {
        try
        {
            return Ok(new { id });
        }
        catch (TimeoutException e)
        {
            Visionary.CaptureException(e);    // a handled error, linked to this request
            return StatusCode(503);
        }
    }
}
```

**Records:** this request is logged with `route = /api/Orders/{id}`. Razor
Pages and anything else on endpoint routing work the same way.

### Middleware order and DI registration

- `AddVisionary()` registers the agent and three hosted services: span
  listeners, the writer, and the system sampler. They start and stop with the
  host.
- `UseVisionary()` must come after `AddVisionary()` (otherwise it throws
  `call builder.Services.AddVisionary() before app.UseVisionary()`). Put it
  first in the pipeline, before `UseExceptionHandler`, authentication and
  routing, so the log has the status the client got. Calling it twice adds it
  once.
- With no database configured, `UseVisionary()` adds nothing and the app
  serves exactly as before.

### Routes the endpoint routing can't see

**When:** terminal middleware, `app.Run(...)` handlers, or anything without an
endpoint. Report the pattern so `/files/1` and `/files/2` count as one route:

```csharp
Visionary.SetRoute(ctx, "/files/{id}");
```

or name them in one place:

```csharp
builder.Services.AddVisionary(o => o.Route = ctx =>
    ctx.Request.Path.StartsWithSegments("/files") ? "/files/{id}" : null);
```

`Route` is called after the handler, only when endpoint routing has no
pattern.

### Attach the signed-in user

**When:** your app has logins and you want to see who made each request.

Without code, an authenticated `HttpContext.User` is used: the
`NameIdentifier` claim (or `sub`, else the identity name) as `user_id`, the
identity name as `user_handle`, and the role claim as `user_role`. To set it
yourself, call this once the user is known, typically in auth middleware:

```csharp
Visionary.SetUser(httpContext, id, handle: "ann", role: "admin");
```

To fill in handle and role from an id, set `LookupUser`. It runs on the
background writer, once per distinct user per batch, only for users missing a
handle or role. Keep it cheap (a cache) and return null when unknown:

```csharp
builder.Services.AddVisionary(o => o.LookupUser = async (id, ct) =>
    cache.TryGetValue(id, out var u) ? new VisionaryUser(id, u.Name, u.Role) : null);
```

### Record login attempts

**When:** you want the security detectors to spot one IP trying many
accounts, many IPs trying one account, or a signed-in user trying someone
else's.

```csharp
app.MapPost("/login", async (LoginForm form, HttpContext ctx, AuthService auth) =>
{
    Visionary.LoginAttempt(ctx, form.Email);
    var user = await auth.AuthenticateAsync(form.Email, form.Password);
    return user is null ? Results.Unauthorized() : Results.Ok(auth.IssueToken(user));
});
```

Call it in the login handler with the username or email typed, whether or
not it exists or the password was right. The value is trimmed and clipped to
255 characters.

**Records:** `login_account` on the request row.

### EF Core, Npgsql, MySqlConnector, Dapper

**When:** the app talks to a database. Nothing to add. The span listeners
start with the host and record:

- **Npgsql** and **MySqlConnector** commands, from their `ActivitySource`s.
  This covers plain ADO.NET, Dapper and EF Core on those drivers.
- **EF Core** commands on any provider (SQLite, SQL Server, ...), from EF
  Core's `DiagnosticListener`. Commands a driver already reports are not
  counted twice.

**Records:** a `db` span per command:

- name: the statement kind and table, e.g. `SELECT users`;
- target: the system and database, e.g. `postgresql/shop`, `sqlite/app.db`;
- detail: the statement, sanitized (literals become `?`, `IN (...)` lists
  collapse). Parameter values are never stored.

Each request row gets `db_count` and `db_ms`.

Plain ADO.NET on a driver without diagnostics (for example Dapper on
Microsoft.Data.Sqlite, or SqlClient without EF Core) is not visible. Wrap those
calls in a [custom span](#custom-spans-redis-queues-cpu-work) with kind `db`.

### HttpClient and IHttpClientFactory

**When:** the app calls other APIs. Nothing to add: no handler to register.
Every `HttpClient` (any handler, including `IHttpClientFactory` clients and
typed clients) is seen through the runtime's `HttpHandlerDiagnosticListener`.

**Records:** an `http` span per call, named `METHOD host` (e.g.
`GET api.stripe.com`), with status `error` on transport failures and 5xx
responses. Each request row gets `http_count` and `http_ms`.

### Custom spans (Redis, queues, CPU work)

**When:** time inside a request shows as **Untracked** in its breakdown. That
is time in something the SDK has no hook for: Redis, a queue client, a
database driver without diagnostics, or your own CPU work.

```csharp
using (var span = Visionary.StartSpan("cache", "GET session", "redis/sessions"))
{
    try
    {
        value = await redis.StringGetAsync(key);
    }
    catch (Exception e)
    {
        span.SetError(e);
        throw;
    }
}
```

`StartSpan(kind, name, target, detail)`: `kind` is `db`, `http`, `cache`,
`queue`, `custom`, `graphql` or `background`. Keep `name` low-cardinality.
`detail` is optional; never put parameter values or secrets in it. Set
`span.RowCount` when you know it. The span is recorded when disposed.

### Background work

**When:** a handler starts work it does not wait for, such as sending an email
after the response.

```csharp
app.MapPost("/signup", (IDbContextFactory<AppDb> dbs) =>
{
    _ = Visionary.Background("send welcome email", async ct =>
    {
        await using var db = await dbs.CreateDbContextAsync(ct);
        // ... queries, HttpClient calls
    });
    return Results.Ok();
});
```

It runs the work with `Task.Run` and returns its `Task`, which completes or
faults with the work.

- The work is not cancelled with the request (`RequestAborted`). The token it
  gets is cancelled when the application stops
  (`IHostApplicationLifetime.ApplicationStopping`).
- Don't touch `HttpContext` in it: the request is over by then.

**Records:** one span of kind `background` named after it, with status `error`
and the message if the work throws. Calls made in the work (and in any
`Task.Run` or un-awaited task a request starts) carry the request's
`request_id`, with `offset_ms` from the request's start, even after the
response went out. Calls that end after the request was logged are left out of
its `db_*`/`http_*` totals and are kept whatever their duration, up to
`VISIONARY_SPAN_MAX` per request, then only when they take at least
`VISIONARY_SPAN_THRESHOLD_MS`. `background` spans never count in the totals.
Outside a request the span has no `request_id` and is kept when it takes at
least the threshold.

### Errors

**When:** always on. Exceptions that escape the pipeline are recorded
(`handled = 0`) and rethrown, so the framework answers exactly as before.
Exceptions handled by a `UseExceptionHandler` placed after `UseVisionary` are
recorded too. To record one you caught:

```csharp
Visionary.CaptureException(e);          // linked to the current request, if any
Visionary.CaptureException(ctx, e);     // linked to this request
```

**Records:** an `errors` row with the type, message, stack
(`Exception.ToString()`) and a fingerprint over the top five frames of your
own code (namespaces other than `System.*`, `Microsoft.*` and the SDK's), so
the same bug groups together across releases and machines. At most 20 rows per
fingerprint per minute are written.

### Error codes from JSON responses

**When:** your API answers errors with a body like
`{"error": {"code": "card_declined", "key": "payment.declined"}}`. Nothing to
add for that shape. For another shape, set the dotted paths:

```csharp
builder.Services.AddVisionary(o =>
{
    o.ErrorCodeField = "errors.0.code";
    o.ErrorKeyField = "errors.0.key";
});
```

### Share the application's connection pool

**When:** the app already has a `DbDataSource` and you want Visionary to use
the same driver, pool and credentials.

```csharp
var dataSource = NpgsqlDataSource.Create(builder.Configuration.GetConnectionString("Default")!);
builder.Services.AddSingleton(dataSource);
builder.Services.AddVisionary(o => o.DataSource = dataSource);
```

Or give a factory of new, unopened connections:

```csharp
builder.Services.AddVisionary(o =>
{
    o.ConnectionFactory = () => new MySqlConnection(connectionString);
    o.Dialect = VisionaryDialect.MySql;   // optional; detected from the provider when null
});
```

The dialect is detected from the provider type. If detection fails, startup
logs `set VisionaryOptions.Dialect for this data source` and collection is
off; set `Dialect` to `Postgres`, `MySql` or `Sqlite`.

### Database pool metrics

**When:** you want the `db_*` pool columns of `system_metrics` filled.

```csharp
builder.Services.AddVisionary(o => o.DbStats = () => new VisionaryDbStats
{
    Total = pool.Total, Idle = pool.Idle, InUse = pool.Busy, Max = pool.Max,
});
```

`Acquires`, `EmptyAcquires` and `AcquireWaitMs` are optional and cumulative;
the sampler stores per-sample deltas.

### CPU profiles

**When:** you want to see where CPU time goes. Profiling is a separate package,
so apps that don't use it don't carry its dependencies (Microsoft's TraceEvent
reader, about 3.5 MB of assemblies).

1. Install it at the same version as the main package:

   ```sh
   dotnet add package Visionary.AspNetCore.Profiling --version 0.1.0
   ```

2. Register it after `AddVisionary()`:

   ```csharp
   builder.Services.AddVisionary();
   builder.Services.AddVisionaryProfiling();
   ```

3. In the dashboard, open **Settings → Profiling** and turn it on. Pick the
   services (matched by `VISIONARY_SERVICE`), the interval (default every 5
   minutes) and the duration (default 10 s).
4. Within 30 s the app logs
   `visionary: CPU profiles on, 10000 ms every 300 s while serving requests`.
   Profiles appear on the CPU profiles page. **Profile now** takes one at
   once, even while scheduled profiling is off.

Turn it off in the same place; no redeploy either way. To keep a service out of
profiling whatever the dashboard says, set `VISIONARY_PROFILE=false` (or
`o.Profile = false`). See [How profiling works](#how-profiling-works).

### Query plans (Explain)

The .NET SDK has no explainer. It does not answer **Explain** on the Queries
page. See [Troubleshooting](#troubleshooting).

## Choosing the database

The app and the Visionary server must point at the same database. The first
match wins:

| Setting | Backend | Driver package |
|---|---|---|
| `VisionaryOptions.DataSource` or `ConnectionFactory` | the application's own driver and pool | the app's |
| `VISIONARY_DATABASE_URL=postgres://user:pass@host:5432/db` (or `postgresql://`) | PostgreSQL | [Npgsql](https://www.nuget.org/packages/Npgsql) |
| `VISIONARY_DATABASE_URL=mysql://user:pass@host:3306/db` (or `mariadb://`) | MySQL 8+ / MariaDB | [MySqlConnector](https://www.nuget.org/packages/MySqlConnector) (or MySql.Data) |
| `VISIONARY_DATABASE_URL=sqlite:///absolute/path.db` (or `sqlite3://`) | SQLite, absolute path | [Microsoft.Data.Sqlite](https://www.nuget.org/packages/Microsoft.Data.Sqlite) (or System.Data.SQLite) |
| `VISIONARY_DATABASE_URL=sqlite://relative.db` | SQLite, relative to the working directory | as above |
| `D1_ACCOUNT_ID`, `D1_DATABASE_ID`, `D1_API_TOKEN` (optionally `VISIONARY_DATABASE_URL=d1://`) | Cloudflare D1 over its REST API | none (`HttpClient`) |

Notes:

- On Windows, write an absolute SQLite path as
  `sqlite:///C:/data/visionary.db`.
- The driver must be referenced by the application. The package finds it at
  runtime: `DbProviderFactories` registrations first, then the driver's
  factory type.
- Query parameters on `postgres://` and `mysql://` URLs are passed to the
  driver, e.g. `?sslmode=require`. User and password may be percent-encoded.
  The password is left out of the startup log line.
- `D1_API_BASE` replaces `https://api.cloudflare.com/client/v4`, for a proxy.
  `d1://` without the three `D1_*` values fails with
  `d1:// needs D1_ACCOUNT_ID, D1_DATABASE_ID and D1_API_TOKEN`.
- The package creates `api_logs`, `system_metrics`, `spans`, `errors` and
  `releases`. `profiles` and `visionary_settings` are created by the Visionary
  server.
- SQLite and D1 suit modest traffic. For thousands of requests a second, use
  PostgreSQL or MySQL.

## Configuration reference

Every setting is read from the `IConfiguration` key of the same name
(`appsettings.json`, user secrets, command line), else the environment
variable. It can then be overridden in `AddVisionary(o => ...)`. Booleans
accept `true/false/1/0/yes/no/on/off`.

```json
{
  "VISIONARY_DATABASE_URL": "postgres://user:pass@db.internal:5432/visionary",
  "VISIONARY_SERVICE": "my-api"
}
```

| Variable | `VisionaryOptions` | Default | What it does |
|---|---|---|---|
| `VISIONARY_DATABASE_URL` | `DatabaseUrl` | none | Where rows go; see [Choosing the database](#choosing-the-database) |
| `D1_ACCOUNT_ID`, `D1_DATABASE_ID`, `D1_API_TOKEN` | `D1AccountId`, `D1DatabaseId`, `D1ApiToken` | none | Cloudflare D1 credentials |
| `D1_API_BASE` | `D1ApiBase` | Cloudflare's API | D1 API URL, for a proxy |
| `VISIONARY_INSTANCE` | `Instance` | hostname | Labels this server's rows |
| `VISIONARY_SERVICE` | `Service` | empty | Service name; lets several services share a database |
| `VISIONARY_RELEASE` | `Release` | the platform's commit variable | Release for the deploy marker; hashes are cut to 12 characters |
| `VISIONARY_LOG_REQUESTS` | `LogRequests` | `true` | Write one `api_logs` row per request |
| `VISIONARY_LOG_PATH_PREFIX` | `PathPrefix` | empty (everything) | Log only paths starting with this, e.g. `/api` |
| `VISIONARY_LOG_BODIES` | `LogBodies` | `true` | Store redacted request and response bodies |
| `VISIONARY_LOG_MAX_BODY_BYTES` | `MaxBodyBytes` | `8192` | Capture cap per body |
| `VISIONARY_LOG_REDACT_HEADERS` | `RedactHeaders` | empty | Comma list of extra headers to redact |
| `VISIONARY_LOG_REDACT_FIELDS` | `RedactFields` | empty | Comma list of extra JSON/form keys to redact in both bodies |
| `VISIONARY_LOG_REDACT_REQUEST_FIELDS` | `RedactRequestFields` | empty | Comma list of keys to redact in request bodies only |
| `VISIONARY_LOG_ERROR_CODE_FIELD` | `ErrorCodeField` | `error.code` | Dotted path to the error code in JSON error bodies |
| `VISIONARY_LOG_ERROR_KEY_FIELD` | `ErrorKeyField` | `error.key` | Dotted path to the error key |
| `VISIONARY_LOG_RETENTION_DAYS` | `LogRetentionDays` | `0` (off) | Above 0, the SDK also deletes older `api_logs`, `spans`, `errors` and `profiles` rows |
| `VISIONARY_SYSTEM_METRICS` | `SystemMetrics` | `true` | Write `system_metrics` samples |
| `VISIONARY_SYSTEM_INTERVAL` | `SystemInterval` | `15s` | Sample interval: a plain number is seconds; also `500ms`, `1m`, `1h30m` |
| `VISIONARY_SYSTEM_RETENTION_DAYS` | `SystemRetentionDays` | `0` (off) | Above 0, the SDK also deletes older `system_metrics` rows |
| `VISIONARY_SPANS` | `Spans` | `true` | Record database, HTTP and custom spans, and per-request totals |
| `VISIONARY_SPAN_THRESHOLD_MS` | `SpanThresholdMs` | `100` | Spans outside a request (or past `SPAN_MAX`) are kept when at least this slow |
| `VISIONARY_SPAN_MAX` | `SpanMax` | `100` | Span rows stored per request |
| `VISIONARY_ERRORS` | `Errors` | `true` | Record exceptions |
| `VISIONARY_PROFILE` | `Profile` | `true` | Profile when the dashboard asks (needs `AddVisionaryProfiling()`); `false` never profiles |

If `VISIONARY_RELEASE` is unset, the release is read from the first of
`RAILWAY_GIT_COMMIT_SHA`, `RENDER_GIT_COMMIT`, `VERCEL_GIT_COMMIT_SHA`,
`HEROKU_SLUG_COMMIT`, `SOURCE_VERSION`, `GIT_COMMIT`, `GITHUB_SHA`,
`K_REVISION`.

Old rows are normally deleted by the Visionary server, following Settings →
Retention in the dashboard. The two `*_RETENTION_DAYS` settings only matter
when the service writes to a database no Visionary server reads.

Options only available in code:

| `VisionaryOptions` | Default | What it does |
|---|---|---|
| `DataSource` | null | A `DbDataSource` to write through |
| `ConnectionFactory` | null | `() => DbConnection`, an alternative to `DataSource` |
| `Dialect` | detected | `Postgres`, `MySql` or `Sqlite`, for `DataSource` or `ConnectionFactory` |
| `Route` | null | `ctx => "/files/{id}"`, route pattern when endpoint routing has none |
| `LookupUser` | null | Fills in handle and role for a user id |
| `DbStats` | null | `() => VisionaryDbStats`, pool metrics |
| `DiskPath` | working directory's filesystem | Filesystem whose usage is reported |

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| Nothing shows up, and the log says `visionary: no database configured (...); collection is off` | No URL, D1 settings or `DataSource` reached the app | Set `VISIONARY_DATABASE_URL` (or all three `D1_*`) in the environment or `appsettings.json` of the running app, then restart |
| Startup logs `visionary: ...; collection is off` with a driver or scheme message | The driver package isn't referenced, or the URL scheme is wrong | Add the driver package from [Choosing the database](#choosing-the-database) and check the URL |
| `call builder.Services.AddVisionary() before app.UseVisionary()` | `UseVisionary()` without `AddVisionary()` | Add `builder.Services.AddVisionary()` before `builder.Build()` |
| Rows are written but the dashboard is empty | The app and the server use different databases, or the dashboard filters another service | Point both at the same database. Check `VISIONARY_SERVICE` and the service filter |
| Some requests are missing | `VISIONARY_LOG_PATH_PREFIX` excludes them, or `UseVisionary()` comes after middleware that answers them (static files, short-circuits) | Clear or widen the prefix. Move `UseVisionary()` first |
| The logged status differs from the one the client got | `UseVisionary()` comes after `UseExceptionHandler` or other middleware that rewrites the response | Put `UseVisionary()` first |
| Requests have no route | Terminal middleware or a handler without an endpoint | Call `Visionary.SetRoute` or set `o.Route` |
| A request's breakdown is mostly **Untracked** | Its time goes to a client without a hook (Redis, a driver without diagnostics) or CPU work | Wrap those calls in [`Visionary.StartSpan`](#custom-spans-redis-queues-cpu-work) |
| **Explain** on the Queries page stays pending forever ("No SDK has picked this up") | The .NET SDK has no explainer, so it never answers Explain requests | Plans come only from a service running an SDK with an explainer (Go or Node) for the same database. Its explainer target must match the span's target, e.g. `postgresql/<dbname>` |
| CPU profiles missing | `AddVisionaryProfiling()` not called, profiling off in the dashboard, the service isn't in its service list, or `VISIONARY_PROFILE=false` | Install and register the profiling package. Turn profiling on in **Settings → Profiling** and check the service list. Wait for `visionary: CPU profiles on ...`. Scheduled profiles run only while requests are being served |
| `visionary: CPU profiles off until visionary_settings can be read` | The Visionary server hasn't created `visionary_settings` yet in this database | Start the server against the same database |
| `visionary: CPU profile failed` or `CPU profiler: ...` | The diagnostics port is off (`DOTNET_EnableDiagnostics=0`), or on Linux `/tmp` isn't writable | Remove `DOTNET_EnableDiagnostics=0` and give the app a writable temp directory |
| `visionary: batch write failed; N entries dropped: d1: status ...: ... exceeded D1's free tier daily row read limit` | The D1 database hit Cloudflare's free-tier daily limit | Wait for the daily reset or move to the Workers Paid plan. To use less, log only `/api` (`VISIONARY_LOG_PATH_PREFIX`), raise `VISIONARY_SYSTEM_INTERVAL`, or switch to PostgreSQL or MySQL |
| `visionary: write queue full; N entries dropped` | Writes can't keep up with traffic (common on SQLite and D1) | Use PostgreSQL or MySQL, or log fewer paths or no bodies |
| Rows lost on shutdown | The process was killed before the hosted services stopped (`kill -9`, a container stop grace period that is too short) | Stop the app with SIGTERM or Ctrl+C and let `app.Run()` return. The writer drains the queue for up to 10 s; keep the host's shutdown timeout and the container's grace period above that |
| `visionary: create tables failed (retrying on the next write)` | The database user can't create tables, or the database is unreachable | Grant `CREATE` and `ALTER`, or create the tables by running the Visionary server first |

## How it works

Everything is queued without blocking the request (5000 entries at most;
beyond that entries are dropped and counted) and written by one background
service every 50 entries or 3 seconds. A failed batch is retried twice (after
1 s, then 2 s). Shutdown drains the queue. A database that goes down never
affects requests: writes fail on the background services and are logged.
Every Visionary SDK follows the same contract, [`sdks/SPEC.md`](../SPEC.md);
the Go SDK in [`sdks/go`](../go) is the reference implementation.

### What is written

- `api_logs`: request id (`X-Request-ID` in, or a new one, echoed on the
  response), method, path, route pattern, query, status (500 when the pipeline
  threw), duration, client IP (first `X-Forwarded-For` hop), user agent, user,
  headers (`Authorization`, `Cookie`, `Set-Cookie`, `X-Api-Key`, ... redacted),
  and bodies up to the cap with passwords, tokens, keys and card numbers
  redacted (JSON and form encoded, even when cut off). Bodies are copied as
  they stream past, so streaming responses still stream and `SendFileAsync`
  keeps its zero-copy path for binary content. Plus `db_count`, `db_ms`,
  `http_count`, `http_ms`.
- `spans`: rows are kept for every request, at most `VISIONARY_SPAN_MAX` each.
  Calls outside a request are kept when they take at least
  `VISIONARY_SPAN_THRESHOLD_MS`. The package's own writes are never recorded
  as spans.
- `errors`, and one `releases` row per start when the release is known.
- `system_metrics`: CPU (host and process; user, system, iowait, steal), load,
  memory, cache and swap, disk space and inodes, disk and network throughput,
  IOPS, latency, errors, TCP connections and retransmits, processes, context
  switches, process I/O, threads, open handles against the limit, GC heap,
  collections and pause time, thread pool size, and HTTP throughput. Linux
  reads `/proc`; Windows uses Win32 counters; whatever a platform lacks is
  NULL.
- `profiles`: with the profiling package, only while profiling is on.

### How profiling works

The package reads the `profile.*` rows of `visionary_settings` at start and
every 30 seconds on a background service, off the request path. While
profiling is on, and only while requests are being served, it samples the
process for the chosen duration every chosen interval.

**Sampling:** through EventPipe, the runtime's own tracing channel, on the
process's diagnostics port (the same thing `dotnet-trace` uses). The runtime's
sample profiler walks every managed thread's stack about once a millisecond.
The session goes to a temporary `.nettrace` file, which is read back and
deleted when the profile ends. Only threads running managed code count:
threads that are waiting, sleeping or blocked in native calls are left out,
and so is the SDK's own background work. Your requests going through
Visionary's middleware do count.

**Cost:** nothing between profiles. While a profile runs, a few percent of one
CPU (measured at 3 to 7 % of one core with about 40 threads), plus a short
burst when the trace is read back (0.1 to 2 s of one core for a 10 s profile,
on a thread-pool thread). Requests are never blocked or failed by it.

**What is stored:** one row per profile, with `samples`, the measured
`duration_ms`, `routes` (each `"METHOD route"` and the milliseconds its
requests that finished during the profile took) and `folded`: one
`frame;frame;...;frame count` line per distinct stack, heaviest first, cut at
90 000 bytes with the rest summed into one `(other) n` line. A scheduled
profile with fewer than 20 samples is dropped, and kept otherwise even when no
request finished during it (`routes` is then `{}`). A **Profile now** one is
kept with any samples. Frames read:

| Code | Frame |
|---|---|
| Your code, with its PDB next to it | `MyApp.Orders.Total Services/Orders.cs:42` (only the sampled frame has a line; callers read `MyApp.Orders.Total Services/Orders.cs`) |
| Your code, no PDB | `MyApp.Orders.Total MyApp/Orders` |
| NuGet packages (from the app's `deps.json`) | `Npgsql.NpgsqlCommand.ExecuteReader lib:Npgsql/NpgsqlCommand` |
| The runtime and shared frameworks | `System.Text.Json.JsonSerializer.Serialize std:System.Text.Json/JsonSerializer` |
| Code generated at run time, native frames | `dynamicClass.lambda_method1`, `(native)` |

Parameter lists and generic arguments are left out, and an async method's
state machine reads as the method (`Orders+<TotalAsync>d__3.MoveNext` is
`Orders.TotalAsync`). Methods the JIT inlined show as part of their caller.
Profiles follow Settings → Retention, and `VISIONARY_LOG_RETENTION_DAYS` when
it is set.

### API summary

```csharp
builder.Services.AddVisionary(o => { ... });     // register; options override VISIONARY_* settings
builder.Services.AddVisionaryProfiling();        // optional, Visionary.AspNetCore.Profiling
app.UseVisionary();                              // the request middleware, first in the pipeline

Visionary.SetUser(ctx, id, handle, role);        // the signed-in user of this request
Visionary.SetRoute(ctx, "/files/{id}");          // route pattern, for requests without an endpoint
Visionary.LoginAttempt(ctx, form.Email);         // the account a sign-in request tried
Visionary.CaptureException(e);                   // a handled error (handled = 1)
Visionary.CaptureException(ctx, e);              // the same, for a specific request
using var span = Visionary.StartSpan(kind, name, target, detail);   // a manual span
_ = Visionary.Background(name, async ct => { ... });               // work that outlives the request
```

To try an unreleased commit, pack it into a local feed:

```sh
cd sdks/dotnet/Visionary.AspNetCore
dotnet pack -c Release -o ../../../local-packages
dotnet nuget add source "$(pwd)/../../../local-packages" --name visionary-local
dotnet add package Visionary.AspNetCore --version 0.1.0   # in your application
```
