# Visionary for ASP.NET Core

A NuGet package, `Visionary.AspNetCore`, that collects what Visionary analyses,
straight from an ASP.NET Core application (minimal APIs, controllers, Razor
Pages, anything on endpoint routing):

- one row per HTTP request in `api_logs`, with the route pattern, user, redacted
  headers and bodies, error codes, and how many database and outbound HTTP calls
  the request made;
- periodic host, process, GC and throughput samples in `system_metrics`;
- database and `HttpClient` spans in `spans`, exceptions in `errors`, and a deploy
  marker in `releases`,

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

## Errors

Exceptions that escape the pipeline are recorded (`handled = 0`) and rethrown, so
the framework answers exactly as before; exceptions an `UseExceptionHandler`
placed after `UseVisionary` handled are recorded too. `Visionary.CaptureException(ex)`
records a handled one (`handled = 1`). Each row has the type, message, stack
(`Exception.ToString()`), and a fingerprint over the top five frames of your own
code (namespaces other than `System.*`, `Microsoft.*` and the SDK's), so the same
bug groups together across releases and machines.

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
| `VISIONARY_LOG_RETENTION_DAYS` | `30` (0 keeps forever) | `LogRetentionDays` |
| `VISIONARY_LOG_REDACT_HEADERS`, `_REDACT_FIELDS`, `_REDACT_REQUEST_FIELDS` | empty (comma lists, added to the defaults) | `RedactHeaders`, ... |
| `VISIONARY_LOG_ERROR_CODE_FIELD`, `_ERROR_KEY_FIELD` | `error.code`, `error.key` | `ErrorCodeField`, `ErrorKeyField` |
| `VISIONARY_SYSTEM_METRICS` | `true` | `SystemMetrics` |
| `VISIONARY_SYSTEM_INTERVAL` | `15s` | `SystemInterval` |
| `VISIONARY_SYSTEM_RETENTION_DAYS` | `30` | `SystemRetentionDays` |
| `VISIONARY_SERVICE` | empty | `Service` |
| `VISIONARY_RELEASE` | the platform's commit variable (`GITHUB_SHA`, `RAILWAY_GIT_COMMIT_SHA`, ...) | `Release` |
| `VISIONARY_SPANS` | `true` | `Spans` |
| `VISIONARY_SPAN_THRESHOLD_MS` | `100` | `SpanThresholdMs` |
| `VISIONARY_SPAN_MAX` | `100` | `SpanMax` |
| `VISIONARY_ERRORS` | `true` | `Errors` |

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
