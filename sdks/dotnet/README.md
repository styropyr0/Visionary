# Visionary for ASP.NET Core

A NuGet package, `Visionary.AspNetCore`, that collects what Visionary analyses,
straight from an ASP.NET Core application (minimal APIs, controllers, Razor
Pages, anything on endpoint routing):

- one row per HTTP request in `api_logs`, with the route pattern, user, redacted
  headers and bodies, error codes, and how many database and outbound HTTP calls
  the request made;
- periodic host, process, GC and throughput samples in `system_metrics`;
- database and `HttpClient` spans in `spans`, exceptions in `errors`, and a deploy
  marker in `releases`;
- CPU profiles in `profiles` when the dashboard asks for them, with the optional
  `Visionary.AspNetCore.Profiling` package ([CPU profiles](#cpu-profiles)),

written in batches on background services to Cloudflare D1, PostgreSQL,
MySQL/MariaDB or SQLite. The Visionary server reads those tables for its dashboards.

It follows the same contract as the other Visionary SDKs; see
[`sdks/SPEC.md`](../SPEC.md). The Go SDK in [`sdks/go`](../go) is the reference
implementation.

Requirements: .NET 8 or later. The package depends only on the ASP.NET Core shared
framework; database drivers are the application's choice.

## Install

From [nuget.org](https://www.nuget.org/packages/Visionary.AspNetCore), for .NET 8 or later:

```sh
dotnet add package Visionary.AspNetCore
```

To try an unreleased commit, pack it into a local feed instead:

```sh
cd sdks/dotnet/Visionary.AspNetCore
dotnet pack -c Release -o ../../../local-packages
dotnet nuget add source "$(pwd)/../../../local-packages" --name visionary-local
dotnet add package Visionary.AspNetCore --version 0.1.0   # in your application
```

## Quickstart: minimal API

```csharp
using Styropyr0.Visionary;

var builder = WebApplication.CreateBuilder(args);
builder.Services.AddVisionary();

var app = builder.Build();
app.UseVisionary();            // first, before exception handlers, auth and routing

app.MapGet("/users/{id}", (string id, HttpContext ctx) =>
{
    Visionary.SetUser(ctx, id, handle: "ann", role: "admin");
    return Results.Ok(new { id });
});

app.Run();
```

```sh
export VISIONARY_DATABASE_URL=postgres://user:pass@db.internal:5432/app
dotnet run
```

You will see one line like
`visionary: writing to postgres://user@db.internal:5432/app (postgres); requests on ...`.
The tables are created on first start (and older tables gain new columns).

## Quickstart: controllers

```csharp
using Styropyr0.Visionary;

var builder = WebApplication.CreateBuilder(args);
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
            return Ok(Load(id));
        }
        catch (TimeoutException e)
        {
            Visionary.CaptureException(e);    // a handled error, linked to this request
            return StatusCode(503);
        }
    }

    private static object Load(int id) => new { id };
}
```

This request is logged with `route = /api/Orders/{id}`.

With no database configured, the package logs one warning and does nothing else;
the application starts and serves exactly as before. A database that goes down
later never affects requests: writes fail on the background services, are
retried, and are logged.

## Choosing the database

The first match wins:

| Setting | Backend |
|---|---|
| `VisionaryOptions.DataSource` (a `DbDataSource`, e.g. `NpgsqlDataSource`) or `ConnectionFactory` | the application's own driver and pool; the dialect is detected from the provider or set with `Dialect` |
| `VISIONARY_DATABASE_URL=postgres://user:pass@host:5432/db` (or `postgresql://`) | PostgreSQL, through [Npgsql](https://www.nuget.org/packages/Npgsql) |
| `VISIONARY_DATABASE_URL=mysql://user:pass@host:3306/db` (or `mariadb://`) | MySQL 8+ / MariaDB, through [MySqlConnector](https://www.nuget.org/packages/MySqlConnector) (or MySql.Data) |
| `VISIONARY_DATABASE_URL=sqlite:///absolute/path.db`, `sqlite://relative.db` | SQLite, through [Microsoft.Data.Sqlite](https://www.nuget.org/packages/Microsoft.Data.Sqlite) |
| `VISIONARY_DATABASE_URL=d1://`, or unset with `D1_ACCOUNT_ID`, `D1_DATABASE_ID`, `D1_API_TOKEN` | Cloudflare D1 over its REST API (`HttpClient`, no driver); `D1_API_BASE` overrides the API URL |

The driver must be referenced by the application; the package finds it at runtime
(`DbProviderFactories` registrations first, then the driver's factory type). Query
parameters on `postgres://` and `mysql://` URLs are passed to the driver (for
example `?sslmode=require`); user and password may be percent-encoded. On Windows,
`sqlite:///C:/data/visionary.db` is an absolute path.

Sharing the application's pool:

```csharp
var dataSource = NpgsqlDataSource.Create(builder.Configuration.GetConnectionString("Default")!);
builder.Services.AddSingleton(dataSource);
builder.Services.AddVisionary(o => o.DataSource = dataSource);
```

## Attaching the user

Call `Visionary.SetUser(httpContext, id, handle, role)` once the user is known,
typically in auth middleware. Without it, an authenticated `HttpContext.User` is
used: the `NameIdentifier` (or `sub`) claim as `user_id`, the identity name as
`user_handle`, and the role claim as `user_role`.

To fill in handle and role from an id, set `LookupUser`. It runs on the background
writer, once per distinct user per batch, only for users missing a handle or role:

```csharp
builder.Services.AddVisionary(o => o.LookupUser = async (id, ct) =>
    cache.TryGetValue(id, out var u) ? new VisionaryUser(id, u.Name, u.Role) : null);
```

## Spans

The middleware records the request; the calls inside it are recorded only when
their client is hooked: EF Core, ADO.NET drivers with diagnostics and
`HttpClient` are. Wrap the rest (Redis, queues, CPU work) in
`Visionary.StartSpan`. Anything left shows as **Untracked** in the request's
breakdown; see [What the middleware sees](../../Readme.md#what-the-middleware-sees-and-what-needs-code).

Without any code, the package records:

- **`HttpClient`** calls (any handler, including `IHttpClientFactory` clients), from
  the runtime's `HttpHandlerDiagnosticListener`: `GET api.stripe.com`, with status
  `error` on transport failures and 5xx responses;
- **Npgsql** and **MySqlConnector** commands, from their `ActivitySource`s (ADO.NET,
  Dapper and EF Core alike);
- **EF Core** commands on any provider (SQLite, SQL Server, ...), from EF Core's
  `DiagnosticListener`. Commands a driver already reports are not counted twice.

Statements are stored sanitized (literals become `?`, `IN (...)` lists collapse),
never with parameter values. Every logged request gets `db_count`, `db_ms`,
`http_count` and `http_ms`; span rows are kept for every request, at most
`VISIONARY_SPAN_MAX` each. Calls outside a request are kept when they take at
least `VISIONARY_SPAN_THRESHOLD_MS`.

Plain ADO.NET on a driver without diagnostics (for example Dapper on SQLite) is
not visible; time such calls, or cache and queue calls, by hand:

```csharp
using (var span = Visionary.StartSpan("cache", "GET session", "redis/sessions"))
{
    value = await redis.StringGetAsync(key);
}
```

The package's own writes are never recorded as spans.

## Background work

Work a handler starts and does not wait for stays part of its request. Start it
with `Visionary.Background`:

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

It runs the work with `Task.Run` and records one span of kind `background`
named after it, with status `error` and the message if the work throws; the
returned `Task` completes or faults with the work. The work is not cancelled with
the request (`RequestAborted`); the token it gets is cancelled when the
application stops (`IHostApplicationLifetime.ApplicationStopping`). Don't touch
`HttpContext` in it: the request is over by then.

Calls made in the work (and in any `Task.Run` or un-awaited task a request
starts) carry the request's `request_id`, with `offset_ms` from the request's
start, even after the response went out; `offset_ms + duration_ms` past the
request's duration shows they ran after it. Calls that end after the request was
logged are left out of its `db_*`/`http_*` totals and are kept whatever their
duration, up to `VISIONARY_SPAN_MAX` of them per request, then only when they
take at least `VISIONARY_SPAN_THRESHOLD_MS`. `background` spans never count in the
totals. Outside a request the span has no `request_id` and is kept like other
lone spans, when it takes at least the threshold.

## Errors

Exceptions that escape the pipeline are recorded (`handled = 0`) and rethrown, so
the framework answers exactly as before; exceptions an `UseExceptionHandler`
placed after `UseVisionary` handled are recorded too. `Visionary.CaptureException(ex)`
records a handled one (`handled = 1`). Each row has the type, message, stack
(`Exception.ToString()`), and a fingerprint over the top five frames of your own
code (namespaces other than `System.*`, `Microsoft.*` and the SDK's), so the same
bug groups together across releases and machines.

## CPU profiles

Profiling is a separate package, so apps that don't use it don't carry its
dependencies (Microsoft's TraceEvent reader, about 3.5 MB of assemblies):

```sh
dotnet add package Visionary.AspNetCore.Profiling
```

```csharp
builder.Services.AddVisionary();
builder.Services.AddVisionaryProfiling();
```

Nothing is sampled until someone turns profiling on in the dashboard
(**Settings → Profiling**), and it can be turned off there again; no redeploy is
needed either way. The package reads the `profile.*` rows of
`visionary_settings` at start and every 30 seconds on a background service,
off the request path. While profiling is on, and only while requests are being
served, it samples the process for the chosen duration (10 s by default) every
chosen interval (5 minutes by default). **Profile now** in the dashboard takes
one profile at once, even while scheduled profiling is off. The dashboard's
service list limits profiling to the services named there (`VISIONARY_SERVICE`).
`VISIONARY_PROFILE=false` (or `o.Profile = false`) makes a service ignore the
dashboard and never profile, for code that must never be sampled.

**How it samples:** through EventPipe, the runtime's own tracing channel, on
the process's diagnostics port (the same thing `dotnet-trace` uses from
outside). The runtime's sample profiler walks every managed thread's stack
about once a millisecond; the session goes to a temporary `.nettrace` file,
which is read back and deleted when the profile ends. Only threads running
managed code count: threads that are waiting, sleeping or blocked in native
calls are left out, and so is the SDK's own background work (writer, system
sampler, the profiler itself). Your requests going through Visionary's
middleware do count.

**Cost:** nothing between profiles. While a profile runs, a few percent of one
CPU (measured at 3 to 7 % of one core with about 40 threads), plus a short
burst when the trace is read back (0.1 to 2 s of one core for a 10 s profile,
on a thread-pool thread). Requests are never blocked or failed by it.

**What is stored:** one row in `profiles` per profile, with `samples`, the
measured `duration_ms`, `routes` (each `"METHOD route"` and the milliseconds
its requests that finished during the profile took) and `folded`: one
`frame;frame;...;frame count` line per distinct stack, heaviest first, cut at
90 000 bytes with the rest summed into one `(other) n` line. A scheduled
profile with fewer than 20 samples is dropped, and kept otherwise even when no
request finished during it (`routes` is then `{}`); a **Profile now** one is
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
Profiles follow Settings → Retention, and `VISIONARY_LOG_RETENTION_DAYS` when it
is set. The Visionary server creates
`profiles` and `visionary_settings`; until it has, profiling stays off.

Sampling needs the diagnostics port: it is off when the app runs with
`DOTNET_EnableDiagnostics=0`, and on Linux it needs a writable temp directory
(`/tmp`). If a profile can't start, one warning is logged and the app carries on.

## Configuration

Every setting is read from the environment variable, or the `IConfiguration` key of
the same name (so `appsettings.json` works too), and can then be overridden in
`AddVisionary(o => ...)`.

| Variable | Default | `VisionaryOptions` |
|---|---|---|
| `VISIONARY_DATABASE_URL` | see above | `DatabaseUrl` |
| `D1_ACCOUNT_ID`, `D1_DATABASE_ID`, `D1_API_TOKEN`, `D1_API_BASE` | | `D1AccountId`, ... |
| `VISIONARY_INSTANCE` | hostname | `Instance` |
| `VISIONARY_LOG_REQUESTS` | `true` | `LogRequests` |
| `VISIONARY_LOG_PATH_PREFIX` | empty (everything) | `PathPrefix` |
| `VISIONARY_LOG_BODIES` | `true` | `LogBodies` |
| `VISIONARY_LOG_MAX_BODY_BYTES` | `8192` | `MaxBodyBytes` |
| `VISIONARY_LOG_RETENTION_DAYS` | `0` (off; above 0 the SDK also deletes older `api_logs`, `spans`, `errors` and `profiles` rows) | `LogRetentionDays` |
| `VISIONARY_LOG_REDACT_HEADERS`, `_REDACT_FIELDS`, `_REDACT_REQUEST_FIELDS` | empty (comma lists, added to the defaults) | `RedactHeaders`, ... |
| `VISIONARY_LOG_ERROR_CODE_FIELD`, `_ERROR_KEY_FIELD` | `error.code`, `error.key` | `ErrorCodeField`, `ErrorKeyField` |
| `VISIONARY_SYSTEM_METRICS` | `true` | `SystemMetrics` |
| `VISIONARY_SYSTEM_INTERVAL` | `15s` | `SystemInterval` |
| `VISIONARY_SYSTEM_RETENTION_DAYS` | `0` (off; above 0 the SDK also deletes older `system_metrics` rows) | `SystemRetentionDays` |
| `VISIONARY_SERVICE` | empty | `Service` |
| `VISIONARY_RELEASE` | the platform's commit variable (`GITHUB_SHA`, `RAILWAY_GIT_COMMIT_SHA`, ...) | `Release` |
| `VISIONARY_SPANS` | `true` | `Spans` |
| `VISIONARY_SPAN_THRESHOLD_MS` | `100` | `SpanThresholdMs` |
| `VISIONARY_SPAN_MAX` | `100` | `SpanMax` |
| `VISIONARY_ERRORS` | `true` | `Errors` |
| `VISIONARY_PROFILE` | `true` (profiles when the dashboard asks; needs `AddVisionaryProfiling()`) | `Profile` |

Old rows are deleted by the Visionary server, following Settings → Retention in the
dashboard. The two `*_RETENTION_DAYS` settings only matter when the service writes to
a database no Visionary server reads.

`DbStats` reports connection pool numbers for the `db_*` columns; `Route` names
requests endpoint routing doesn't see (or call `Visionary.SetRoute`).

## What is captured

- **Requests:** request id (`X-Request-ID` in, or a new one, echoed on the response),
  method, path, route pattern (`/users/{id}`; inline constraints dropped), query,
  status (500 when the pipeline threw), duration, client IP (first
  `X-Forwarded-For` hop), user agent, user, headers (with `Authorization`,
  `Cookie`, `Set-Cookie`, `X-Api-Key`, ... redacted), and bodies up to the cap with
  passwords, tokens, keys and card numbers redacted (JSON and form encoded, even when
  cut off). Bodies are copied as they stream past, so streaming responses still
  stream and `SendFileAsync` keeps its zero-copy path for binary content.
- **System metrics:** CPU (host and process; user, system, iowait, steal), load,
  memory, cache and swap, disk space and inodes, disk and network throughput,
  IOPS, latency, errors, TCP connections and retransmits, processes, context
  switches, process I/O, threads, open handles against the limit, GC heap,
  collections and pause time, thread pool size, and HTTP throughput. Linux reads
  `/proc`; Windows uses Win32 counters; whatever a platform lacks is NULL.

Everything is queued without blocking the request (5000 entries at most; beyond
that entries are dropped and counted) and written by one background service every
50 entries or 3 seconds, with two retries. Shutdown drains the queue.
