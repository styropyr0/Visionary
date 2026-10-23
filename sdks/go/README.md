# Visionary Go SDK

Add one middleware to a Go service and Visionary shows every request, its
database and outbound calls, errors, host and runtime metrics, and CPU
profiles. The SDK writes rows to your database in the background; the
Visionary server reads the same database and draws the dashboards.

Module `github.com/styropyr0/visionary/sdks/go`, package `visionary`. It is
its own module and brings only `pgx` and `gopsutil`. Needs Go 1.26 or later.
The contract every SDK follows is [`sdks/SPEC.md`](../SPEC.md).

## Quick start

Five steps, about 10 minutes. You need the Visionary server running against a
database (see the [main README](../../Readme.md#get-started)).

**1. Install.**

```sh
go get github.com/styropyr0/visionary/sdks/go@latest   # current release: v0.7.1
```

Import the driver for your database too (skip this for D1):

```go
import _ "github.com/jackc/pgx/v5/stdlib" // postgres://
// import _ "github.com/go-sql-driver/mysql" // mysql://
// import _ "modernc.org/sqlite"             // sqlite://
```

**2. Point it at the database the server uses.**

```sh
VISIONARY_DATABASE_URL=postgres://user:pass@host:5432/visionary
VISIONARY_SERVICE=my-api   # any name; tells services apart on the dashboard
```

**3. Start the agent and wrap your router.**

```go
agent, err := visionary.New(visionary.FromEnv())
if err != nil {
	log.Printf("visionary off: %v", err) // a nil *Agent is a no-op, the app runs as before
}
agent.Start(ctx)
defer agent.Close(context.Background()) // flushes rows still queued

http.ListenAndServe(":8080", agent.Middleware(mux))
```

**4. Run it and check the log.** On start you see:

```
INFO visionary: logging requests path_prefix="" bodies=true retention_days=0
INFO visionary: recording system metrics interval=15s instance=my-host
```

**5. Send a request.** It shows up on the API overview within a few seconds,
with route, status, time and any error. The System page fills within 15
seconds. If nothing shows up, see [Troubleshooting](#troubleshooting).

That is the whole integration. The recipes below add database queries,
outbound calls, users and more.

## Databases

The service and the Visionary server must point at the same database.

| Database | Setting | Driver to import |
|---|---|---|
| PostgreSQL | `VISIONARY_DATABASE_URL=postgres://user:pass@host:5432/db?sslmode=require` (or `postgresql://`) | `github.com/jackc/pgx/v5/stdlib` |
| MySQL 8 | `VISIONARY_DATABASE_URL=mysql://user:pass@host:3306/db` | `github.com/go-sql-driver/mysql` |
| SQLite | `VISIONARY_DATABASE_URL=sqlite:///var/lib/visionary/app.db` | `modernc.org/sqlite` |
| Cloudflare D1 | `D1_ACCOUNT_ID`, `D1_DATABASE_ID`, `D1_API_TOKEN` (leave the URL unset, or set it to `d1://`) | none, it uses the REST API |

- **SQLite on Windows:** `sqlite:///C:/data/visionary.db`.
- **SQLite shared by two processes** (your app and the server on one host):
  add `?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)` to the URL in
  both, so one waits for the other instead of failing with "database is
  locked".
- **MySQL** connections are set to UTC (`time_zone='+00:00'`) unless the URL
  sets `time_zone`.
- **D1:** the API token needs D1 edit access. `D1_API_BASE` overrides the
  Cloudflare API URL (for a proxy).
- **Use a pool you already have:** set `Config.Database` and `Config.Dialect`
  instead of a URL. The app keeps owning the pool; `Close` leaves it open.

  ```go
  cfg := visionary.FromEnv()
  cfg.Database, cfg.Dialect = db, visionary.Postgres // or visionary.MySQL, visionary.SQLite
  ```

The SDK creates its tables (`api_logs`, `spans`, `errors`, `releases`,
`system_metrics`) on `Start` and adds new columns when you upgrade. Old rows
are deleted by the Visionary server, following Settings → Data (Retention).

## Recipes

Each recipe is optional. Use the ones that match your stack.

### net/http ServeMux

```go
mux := http.NewServeMux()
mux.HandleFunc("GET /users/{id}", getUser)
http.ListenAndServe(":8080", agent.Middleware(mux))
```

With Go 1.22+ patterns the route (`/users/{id}`) is picked up by itself.

### chi

Mount `agent.Middleware` inside the panic recovery, so panics pass through it,
and report chi's route pattern:

```go
cfg := visionary.FromEnv()
cfg.Requests.Route = func(r *http.Request) string {
	if rc := chi.RouteContext(r.Context()); rc != nil {
		return rc.RoutePattern()
	}
	return ""
}
agent, err := visionary.New(cfg)

r := chi.NewRouter()
r.Use(middleware.Recoverer) // answers 500 as before
r.Use(agent.Middleware)     // records the panic, logs status 500, re-panics
```

The middleware never changes what the client gets. A panic is recorded
(`handled = 0`), then re-panicked, so the recovery middleware handles it as
before. `http.ErrAbortHandler` is passed on without being recorded. Mounted
outside the recovery, the middleware sees only the 500, not the panic.

### gin

Wrap the engine, and report gin's route from a gin middleware:

```go
r := gin.New()
r.Use(gin.CustomRecovery(func(c *gin.Context, p any) {
	visionary.CaptureError(c.Request.Context(), fmt.Errorf("panic: %v", p))
	c.AbortWithStatus(http.StatusInternalServerError)
}))
r.Use(func(c *gin.Context) {
	visionary.SetRoute(c.Request.Context(), c.FullPath()) // "/users/:id"
	c.Next()
})
http.ListenAndServe(":8080", agent.Middleware(r))
```

gin's recovery sits inside the Visionary middleware, so a panic reaches it as
a 500; the `CaptureError` call above stores the panic as an error.

### echo

```go
e := echo.New()
e.Use(middleware.Recover())                  // outermost
e.Use(echo.WrapMiddleware(agent.Middleware)) // inside it, so panics are recorded
e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		visionary.SetRoute(c.Request().Context(), c.Path()) // "/users/:id"
		return next(c)
	}
})
```

### Other routers

Any router that takes `func(http.Handler) http.Handler` middleware works. For
the route pattern, set `cfg.Requests.Route`, or call
`visionary.SetRoute(r.Context(), pattern)` from inside the request. Order:
`SetRoute`, then `Route`, then the ServeMux pattern; without any, the path is
stored as is and `/users/42` and `/users/43` count as two routes.

### Signed-in user

From auth middleware, once the user is known:

```go
visionary.SetUser(r.Context(), visionary.User{ID: strconv.FormatInt(id, 10), Handle: name, Role: role})
```

Only `ID` is required. To fill in handle and role from an ID, give a lookup.
It runs on the background writer, once per user per batch, so keep it cheap
(a cache):

```go
cfg.Requests.LookupUser = func(ctx context.Context, id string) (visionary.User, bool) {
	u, ok := userCache.Get(id)
	return visionary.User{ID: id, Handle: u.Name, Role: u.Role}, ok
}
```

### pgx v5 / pgxpool

Set the tracer before creating the pool:

```go
pc, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
pc.ConnConfig.Tracer = visionary.PgxTracer(pc.ConnConfig.Tracer) // keeps an existing tracer
pool, err := pgxpool.NewWithConfig(ctx, pc)
```

Records queries, `SendBatch` (one span per batch, `BATCH INSERT bookings`),
`CopyFrom` (`COPY trips`) and `Prepare` calls that reach the server. Spans
carry the target `postgresql/<database>`. Use one tracer per pool. Tracers
passed in are called after Visionary's.

### database/sql

```go
db, err := visionary.OpenDB("pgx", dsn, "postgresql/shop") // like sql.Open
// or, with a connector:
db := sql.OpenDB(visionary.WrapConnector(connector, "postgresql/shop"))
// or register a wrapped driver:
sql.Register("sqlite-traced", visionary.WrapDriver(&sqlite.Driver{}, "sqlite/app"))
```

The last argument is the spans' `target` (system/database). Records
statements, `BEGIN`, `COMMIT` and `ROLLBACK`; a query's span lasts until its
rows are closed and counts the rows read. The SDK's own writes are never
traced, so you can also pass the wrapped pool as `Config.Database`.

### Connection pool metrics

Report pool stats on the System page:

```go
cfg.System.DB = visionary.SQLDB(db) // database/sql

cfg.System.DB = func() visionary.DBStats { // pgxpool
	s := pool.Stat()
	return visionary.DBStats{
		Total: int64(s.TotalConns()), Idle: int64(s.IdleConns()), InUse: int64(s.AcquiredConns()),
		Max: int64(s.MaxConns()), Acquires: s.AcquireCount(), EmptyAcquires: s.EmptyAcquireCount(),
		AcquireWait: s.AcquireDuration(),
	}
}
```

### Outbound HTTP

```go
client := &http.Client{Timeout: 10 * time.Second, Transport: visionary.Transport(nil)} // nil: http.DefaultTransport
req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
resp, err := client.Do(req)
```

Spans are named `METHOD host` and end when the response headers arrive.
Transport errors and 5xx responses are marked `error`. Query strings are never
stored.

`http.DefaultClient` is instrumented at `Start`, so `http.Get`, `http.Post`
and `http.DefaultClient.Do` are recorded too (`VISIONARY_INSTRUMENT_HTTP=false`
turns that off). A call joins its request only when it carries the handler's
context: `http.Post(url, ...)` has none, so prefer
`http.NewRequestWithContext(ctx, ...)`.

**Vendor SDKs** build their own HTTP clients. Hand them a traced one:

```go
// AWS SDK v2: S3, Cloudflare R2, SQS, SES...
traced := &http.Client{Transport: visionary.Transport(awshttp.NewBuildableClient().GetTransport())}
cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithHTTPClient(traced))

// openai-go, and Google APIs (Firebase, Maps): both take an *http.Client
client := openai.NewClient(option.WithHTTPClient(&http.Client{Transport: visionary.Transport(nil)}))
```

### go-redis

One hook covers every command and pipeline. Spans are named by command only,
so keys never reach the database, and a miss is not an error:

```go
type visionaryHook struct{}

func (visionaryHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (visionaryHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		end := visionary.StartSpan(ctx, visionary.KindCache, strings.ToUpper(cmd.Name()))
		err := next(ctx, cmd)
		if errors.Is(err, redis.Nil) {
			end(nil) // a cache miss is an answer, not a failure
		} else {
			end(err)
		}
		return err
	}
}

func (visionaryHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		end := visionary.StartSpan(ctx, visionary.KindCache, "PIPELINE")
		err := next(ctx, cmds)
		end(err)
		return err
	}
}

rdb.AddHook(visionaryHook{}) // after any startup PING, so it isn't recorded
```

Calls show as **Cache** in the request's breakdown when they get the request's
`ctx`.

### Custom spans

For any client without a hook (gRPC, a queue) and for your own CPU work
(hashing, image resizing, building a report):

```go
end := visionary.StartSpan(ctx, visionary.KindCustom, "hash otp")
hash, err := bcrypt.GenerateFromPassword(code, bcrypt.DefaultCost)
end(err)
```

Kinds: `KindDB`, `KindHTTP`, `KindCache`, `KindQueue`, `KindCustom`. Time no
span covers shows as **Untracked** in the request's breakdown; a custom span
names it.

### Work that outlives the response

Start it with `visionary.Go`. The request's breakdown shows it as one
`background` span, with the calls it made and when it finished. It keeps the
request's values but not its cancellation, so sending the response does not
cut it short:

```go
visionary.Go(r.Context(), "send welcome email", func(ctx context.Context) error {
	return mailer.Send(ctx, user.Email)
})
```

A panic in the function is recorded as the span's error and re-panics.

### GraphQL

Records each operation for the Queries page (depth, field count, the queries
its resolvers ran). With gqlgen, from an `AroundOperations` extension:

```go
oc := graphql.GetOperationContext(ctx)
end := visionary.GraphQLOperation(ctx, string(oc.Operation.Operation), oc.OperationName, oc.RawQuery)
defer end(nil)
```

Literal values are removed from the stored document.

### Errors

Panics that reach the middleware are recorded by themselves. Record errors you
handle:

```go
if err := h.store.SaveTrip(ctx, trip); err != nil {
	visionary.CaptureError(ctx, err) // handled = 1, linked to the request
	return err
}
```

The `type` is the innermost error of the `Unwrap` chain (`*pgconn.PgError`).
Errors with the same type and the same top five application frames
(`function@file`, no line numbers) share a fingerprint and group into one
issue. Each fingerprint is stored at most 20 times a minute per process.
Errors captured during a request are written when it ends, with its final
route and user. Outside a request, `CaptureError` still records the error,
with no request.

### Query plans

Lets the dashboard show a statement's plan (Queries page → Explain). The SDK
runs `EXPLAIN` (never `EXPLAIN ANALYZE`, so nothing is executed) on your
database and writes the plan back:

```go
cfg.Explain = []*visionary.Explainer{visionary.PgxExplainer(pool)}                         // target postgresql/<database>
cfg.Explain = []*visionary.Explainer{visionary.SQLExplainer(db, visionary.Postgres, "postgresql/shop")} // database/sql
```

The explainer's target must match the target of the spans: `PgxExplainer`
uses `postgresql/<database>` like `PgxTracer`; for `SQLExplainer` pass the
same target you gave `OpenDB` or `WrapDriver`. The agent checks for requests
every 10 seconds. `VISIONARY_EXPLAIN=false` turns it off.

### CPU profiles

No code. Profiling is off until you turn it on in the dashboard, under
Settings → Profiling, and you can turn it off there at any time, with no
redeploy. The same page picks which services take part (by
`VISIONARY_SERVICE`), how often a profile is taken (default every 300 s) and
how long each samples (default 10 s), and has a **Profile now** button. The
agent reads the switch at start and every 30 seconds, and logs
`visionary: CPU profiling on` or `off`.

While on, it runs `runtime/pprof`'s CPU profiler and decodes the profile
itself. It skips a turn when no request arrived since the last one, and keeps
a profile with at least 20 samples (a Profile now one with any). If the
process already runs its own CPU profile (`pprof.StartCPUProfile`,
`net/http/pprof`), the turn is skipped. Cost: nothing between profiles;
while one runs, the usual `pprof` overhead (a few percent of one CPU).

`VISIONARY_PROFILE=false` makes a service ignore the dashboard and never
profile.

### System metrics

On by default, every 15 seconds: host CPU, memory, disk, network and load;
this process's CPU, memory, threads and file descriptors; the container's
cgroup limits and throttling; Go heap, GC and goroutines; request throughput;
and pool stats when `cfg.System.DB` is set. `cfg.System.DiskPath` picks the
filesystem whose usage is reported (default: the working directory's).
`VISIONARY_SYSTEM_METRICS=false` turns it off.

### Bodies and redaction

Request and response bodies are stored up to 8 KB each
(`VISIONARY_LOG_MAX_BODY_BYTES`), with values of sensitive fields replaced by
`[REDACTED]` before anything is stored. Defaults, matched case-insensitively:

- headers `Authorization`, `Proxy-Authorization`, `Cookie`, `Set-Cookie`,
  `X-Api-Key`, `X-Auth-Token`;
- JSON and form fields `password`, `passwd`, `secret`, `client_secret`,
  `token`, `access_token`, `refresh_token`, `id_token`, `api_key`, `apikey`,
  `otp`, `pin`, `card_number`, `cvv`, `private_key`.

Add your own:

```sh
VISIONARY_LOG_REDACT_HEADERS=X-Session
VISIONARY_LOG_REDACT_FIELDS=ssn,dob
VISIONARY_LOG_REDACT_REQUEST_FIELDS=code,fcm_token   # request bodies only
```

Multipart, image, video, audio, `application/octet-stream`, `application/zip`
and `application/pdf` bodies are never stored, only their size.
`VISIONARY_LOG_BODIES=false` keeps sizes only.

### Release markers

```sh
VISIONARY_RELEASE=v1.4.2
```

Without it, the release comes from `RAILWAY_GIT_COMMIT_SHA`,
`RENDER_GIT_COMMIT`, `VERCEL_GIT_COMMIT_SHA`, `HEROKU_SLUG_COMMIT`,
`SOURCE_VERSION`, `GIT_COMMIT`, `GITHUB_SHA` or `K_REVISION` (hashes cut to
12 characters). When known, `Start` writes one `releases` row and the charts
show the deploy.

## A complete service

A chi router, pgxpool, auth middleware that sets the user, and outbound
clients:

```go
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Database: trace the pool before creating it.
	pc, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	pc.ConnConfig.Tracer = visionary.PgxTracer(pc.ConnConfig.Tracer)
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	// Outbound clients: one traced transport shared by all of them.
	places := &http.Client{Timeout: 10 * time.Second, Transport: visionary.Transport(nil)}

	// Visionary: env config plus app-specific options.
	vc := visionary.FromEnv() // VISIONARY_DATABASE_URL, VISIONARY_SERVICE=trips-api
	vc.Requests.PathPrefix = "/api"
	vc.Requests.RedactRequestFields = append(vc.Requests.RedactRequestFields, "code", "fcm_token")
	vc.Requests.Route = func(r *http.Request) string {
		if rc := chi.RouteContext(r.Context()); rc != nil {
			return rc.RoutePattern()
		}
		return ""
	}
	vc.System.DB = func() visionary.DBStats {
		s := pool.Stat()
		return visionary.DBStats{
			Total: int64(s.TotalConns()), Idle: int64(s.IdleConns()), InUse: int64(s.AcquiredConns()),
			Max: int64(s.MaxConns()), Acquires: s.AcquireCount(), EmptyAcquires: s.EmptyAcquireCount(),
			AcquireWait: s.AcquireDuration(),
		}
	}
	vc.Explain = []*visionary.Explainer{visionary.PgxExplainer(pool)}
	agent, err := visionary.New(vc)
	if err != nil {
		log.Printf("visionary off: %v", err)
	}
	agent.Start(ctx)
	defer func() {
		flush, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		agent.Close(flush)
	}()

	r := chi.NewRouter()
	r.Use(middleware.Recoverer) // outermost: still answers panics with 500
	r.Use(agent.Middleware)     // inside it, so panics are recorded
	r.Use(auth)                 // calls visionary.SetUser
	r.Get("/api/trips/{id}", func(w http.ResponseWriter, r *http.Request) {
		trip, err := loadTrip(r.Context(), pool, chi.URLParam(r, "id")) // db spans
		if err != nil {
			visionary.CaptureError(r.Context(), err)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, placesURL(trip), nil)
		if resp, err := places.Do(req); err == nil { // http span
			defer resp.Body.Close()
		}
		json.NewEncoder(w).Encode(trip)
	})

	srv := &http.Server{Addr: ":8080", Handler: r}
	go func() { <-ctx.Done(); srv.Shutdown(context.Background()) }()
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
```

A request to `/api/trips/{id}` that makes 40 queries in a loop shows up with
`db_count = 40`, its 40 `SELECT trip_stops` spans on the request's timeline,
and the dashboards flag the N+1.

## What is stored

| Table | What |
|---|---|
| `api_logs` | one row per request: route, status, duration, user, IP, headers, redacted bodies, and its call totals (`db_count`, `db_ms`, `http_count`, `http_ms`) |
| `spans` | the database, HTTP, cache and custom calls of each request, and slow calls outside requests |
| `errors` | panics that reach the middleware and errors passed to `CaptureError` |
| `releases` | one deploy marker per start, when the release is known |
| `system_metrics` | host, process, Go runtime, pool and throughput samples |
| `profiles` | CPU profiles (folded stacks per route), only while turned on in the dashboard |

- Each request keeps at most `VISIONARY_SPAN_MAX` (100) spans, in start
  order. Calls beyond the cap still count in the totals. A call total is
  NULL when no hook for that kind is installed.
- Calls outside requests (workers, startup) are stored when they alone take
  `VISIONARY_SPAN_THRESHOLD_MS` (100 ms), with no `request_id`.
- Calls made after the response stay linked to their request (up to
  `VISIONARY_SPAN_MAX`, then only those over the threshold) and are not
  counted in its totals.
- Statements are sanitized: literals become `?`, `IN (...)` lists become
  `IN (?)`. Parameter values are never stored. Names read `SELECT users`,
  `INSERT bookings`, `BEGIN`.
- An incoming `X-Request-ID` (up to 128 characters) is kept as the request
  id, else a random one is made; it is echoed on the response.
- Rows are queued in memory (5000) and written in batches of 50 or every 3
  seconds on background goroutines. Nothing on a request path waits for the
  database, and a database that is down never affects requests.

## Configuration

`FromEnv` reads these variables. Booleans take `true`/`false`/`1`/`0`.

| Variable | Default | Meaning |
|---|---|---|
| `VISIONARY_DATABASE_URL` | unset | `postgres://`, `mysql://`, `sqlite:///path` or `d1://`. See [Databases](#databases). |
| `D1_ACCOUNT_ID`, `D1_DATABASE_ID`, `D1_API_TOKEN` | unset | Cloudflare D1, used when no URL (or `d1://`) is set. |
| `D1_API_BASE` | Cloudflare's API | Another D1 API URL (a proxy). |
| `VISIONARY_SERVICE` | none | Names the service when several share a database. |
| `VISIONARY_RELEASE` | the platform's commit variable | The version on deploy markers. See [Release markers](#release-markers). |
| `VISIONARY_INSTANCE` | hostname | Labels this server on the System page. |
| `VISIONARY_LOG_REQUESTS` | `true` | Log requests to `api_logs`. |
| `VISIONARY_LOG_PATH_PREFIX` | all paths | Log only paths starting with this, e.g. `/api`. |
| `VISIONARY_LOG_BODIES` | `true` | Store request and response bodies (redacted, capped). |
| `VISIONARY_LOG_MAX_BODY_BYTES` | `8192` | Bytes kept per body. |
| `VISIONARY_LOG_REDACT_HEADERS` | none | Comma list of headers to redact, added to the defaults. |
| `VISIONARY_LOG_REDACT_FIELDS` | none | Comma list of JSON/form fields to redact, added to the defaults. |
| `VISIONARY_LOG_REDACT_REQUEST_FIELDS` | none | Fields redacted in request bodies only. |
| `VISIONARY_LOG_ERROR_CODE_FIELD` | `error.code` | Dotted path in JSON error responses (status ≥ 400), stored as `error_code`. |
| `VISIONARY_LOG_ERROR_KEY_FIELD` | `error.key` | The same, stored as `error_key`. |
| `VISIONARY_LOG_RETENTION_DAYS` | `0` (off) | Set only when no Visionary server reads this database: the SDK then deletes `api_logs`, `spans`, `errors` and `profiles` rows older than this, daily. |
| `VISIONARY_SPANS` | `true` | Record database and outbound calls. |
| `VISIONARY_SPAN_THRESHOLD_MS` | `100` | Keep calls outside requests at least this slow. |
| `VISIONARY_SPAN_MAX` | `100` | Spans kept per request. |
| `VISIONARY_INSTRUMENT_HTTP` | `true` | Record calls through `http.DefaultClient`. |
| `VISIONARY_EXPLAIN` | `true` | Answer query plan requests; needs `Config.Explain`. |
| `VISIONARY_ERRORS` | `true` | Record panics and `CaptureError` calls. |
| `VISIONARY_PROFILE` | follow the dashboard | `false`: never take CPU profiles, whatever Settings → Profiling says. |
| `VISIONARY_SYSTEM_METRICS` | `true` | Record `system_metrics`. |
| `VISIONARY_SYSTEM_INTERVAL` | `15s` | Sample interval; a plain number is seconds. |
| `VISIONARY_SYSTEM_RETENTION_DAYS` | `0` (off) | Like `VISIONARY_LOG_RETENTION_DAYS`, for `system_metrics`. |

Options set only in code, on the `Config` that `FromEnv` returns:

| Field | Default | Meaning |
|---|---|---|
| `Database`, `Dialect` | unset | Write through a `*sql.DB` the app already has; takes precedence over the URL and D1. |
| `Logger` | `slog.Default()` | Where the SDK logs. |
| `Explain` | none | Explainers for [query plans](#query-plans). |
| `Requests.Route` | ServeMux pattern | Returns the route pattern for a request; see [chi](#chi). |
| `Requests.LookupUser` | none | Fills in a user's handle and role; see [Signed-in user](#signed-in-user). |
| `Requests.QueueSize` | `5000` | Rows queued in memory before new ones are dropped. |
| `Requests.BatchSize` | `50` | Rows per write (at most 50). |
| `Requests.FlushInterval` | `3s` | Longest a row waits before it is written. |
| `System.DB` | none | Pool stats; see [Connection pool metrics](#connection-pool-metrics). |
| `System.DiskPath` | working directory | Filesystem whose usage is reported. |

Everything else (sign-in, alerts, retention, profiling, thresholds) is set in
the Visionary dashboard under Settings, not in the service.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| Nothing shows up, and the log says `visionary off: visionary: no database configured` | No database set. | Set `VISIONARY_DATABASE_URL` or the three `D1_*` variables on the service. |
| `visionary: open postgres (import the "pgx" driver)` | The driver for the URL is not imported. | Add the blank import from [Databases](#databases). |
| Nothing shows up, no errors | The service and the server use different databases (another file, another D1 database, another schema). | Use the exact same URL or D1 IDs in both. For SQLite, use an absolute path. |
| Nothing shows up, no `visionary: logging requests` line | `agent.Start` is not called, or `agent.Middleware` does not wrap the handler that serves the request. | Call `Start` after `New`, and wrap the router (or mount the middleware on it). |
| Some requests are missing | `VISIONARY_LOG_PATH_PREFIX` excludes them. | Unset it, or set the prefix your routes really start with. |
| Every request has its own route (`/users/42`, `/users/43`) | The router's pattern is not reported. | Set `cfg.Requests.Route` or call `visionary.SetRoute`; see [Recipes](#recipes). |
| Panics show as 500 requests but not as errors | The middleware is mounted outside the router's recovery. | Mount it inside, as in [chi](#chi). |
| Database or HTTP totals are empty, or time shows as Untracked | No hook for that client, or the call does not get the request's `ctx`. | Add the [pgx](#pgx-v5--pgxpool), [database/sql](#databasesql) or [HTTP](#outbound-http) hook; use `QueryContext` and `NewRequestWithContext(ctx, ...)`. |
| Explain on the Queries page stays pending forever | No running service has an explainer for that database, or its target differs from the span's target. | Add `cfg.Explain` in a service that can reach the database; the target must match the span's, e.g. `postgresql/<dbname>`. Check `VISIONARY_EXPLAIN` is not `false`. |
| No CPU profiles | Profiling is off in the dashboard, the service is not in its list, or no request arrived since the last turn. | Turn it on in Settings → Profiling and include the service. Check `VISIONARY_PROFILE` is not `false`. |
| `visionary: CPU profiling settings unavailable` | The Visionary server has not created its tables in this database yet. | Start the server against the same database. |
| D1 writes fail with `exceeded D1's free tier daily row read limit` | Cloudflare's free plan limit is used up for today. | Wait for the reset at midnight UTC, move to a paid Workers plan, or use PostgreSQL or MySQL. |
| `visionary: queue full; rows dropped` | Writes cannot keep up, usually SQLite or D1 under heavy traffic. | Use PostgreSQL or MySQL, or raise `Requests.QueueSize`. |
| `database is locked` with SQLite | Two processes write the same file. | Add `?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)` to both URLs. |
| The last requests before a shutdown are missing, or `visionary: log flush timed out` | The process exits before queued rows are written. | Call `agent.Close` with a timeout (10 s is plenty) after the HTTP server has shut down. |
| `duration_ms = 0` for fast queries on Windows | Windows' clock ticks about every 0.5 ms. | Nothing to fix; Linux and macOS measure in nanoseconds. |

## Notes

- A nil `*Agent` (when `New` returned an error) is a valid no-op: `Start`,
  `Close` and `Middleware` do nothing, so the app needs no checks.
- `Close` closes a database the SDK opened from `VISIONARY_DATABASE_URL`; a
  `Config.Database` pool belongs to the application and stays open.
