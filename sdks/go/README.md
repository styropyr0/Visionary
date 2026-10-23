# Visionary Go SDK

`github.com/styropyr0/visionary/sdks/go` (package `visionary`) records what the
Visionary dashboards show, straight from a Go service. It is its own module, so it brings
only `pgx` and `gopsutil`, not the server's dependencies:

```sh
go get github.com/styropyr0/visionary/sdks/go@latest
```

| Table | What |
|---|---|
| `api_logs` | one row per HTTP request, with how many database and outbound HTTP calls it made (`db_count`, `db_ms`, `http_count`, `http_ms`) |
| `spans` | the individual calls of slow (≥ 100 ms) or failed (5xx) requests, and slow calls outside requests |
| `errors` | panics that reach the middleware and errors passed to `CaptureError` |
| `releases` | one deploy marker per start when the release is known |
| `system_metrics` | host, process, Go runtime, pool and throughput samples |
| `profiles` | CPU profiles (folded stacks), only while turned on in the dashboard |

Rows go to Cloudflare D1, SQLite, PostgreSQL or MySQL in batches on
background goroutines. Nothing on a request path waits for the database, and
a database that is down never affects requests. The contract every SDK follows
is [`sdks/SPEC.md`](../SPEC.md).

## Start

```go
agent, err := visionary.New(visionary.FromEnv())
if err != nil {
	log.Printf("visionary off: %v", err) // a nil *Agent is a no-op
}
agent.Start(ctx)
defer agent.Close(context.Background()) // flushes what is queued

http.ListenAndServe(":8080", agent.Middleware(mux))
```

`FromEnv` reads the `VISIONARY_*` variables (see `FromEnv`'s doc comment or
the SPEC's Environment table). Pass `Config.Database` + `Config.Dialect` to
write through a `*sql.DB` the app already has; otherwise set
`VISIONARY_DATABASE_URL` (and import its driver) or the `D1_*` variables.

Old rows are deleted by the Visionary server, following Settings → Retention
in the dashboard. `VISIONARY_LOG_RETENTION_DAYS` and
`VISIONARY_SYSTEM_RETENTION_DAYS` default to 0 (off) and only matter when the
service writes to a database no Visionary server reads.

Name the service and release so several services can share a database and
deploys show up on the charts:

```sh
VISIONARY_SERVICE=trips-api
VISIONARY_RELEASE=v1.4.2   # or let it come from GITHUB_SHA, RAILWAY_GIT_COMMIT_SHA, ...
```

### Middleware order

Mount `agent.Middleware` **inside** the router's panic recovery, so panics
pass through it:

```go
r := chi.NewRouter()
r.Use(middleware.Recoverer) // answers 500 as before
r.Use(agent.Middleware)     // records the panic, logs status 500, re-panics
```

The middleware never changes what the client gets. A panic is recorded
(`handled = 0`, stack without SDK frames), then re-panicked, so the recovery
middleware (or net/http's own) handles it exactly as before.
`http.ErrAbortHandler` is passed on without being recorded.

Attach the signed-in user from auth middleware:

```go
visionary.SetUser(r.Context(), visionary.User{ID: strconv.FormatInt(id, 10)})
```

## Spans: database and outbound calls

Each logged request carries a collector in its context. The hooks below
report into it, from any goroutine the handler starts, as long as they get
the request's context (or one derived from it).

### pgx v5 / pgxpool

```go
cfg, err := pgxpool.ParseConfig(url)
cfg.ConnConfig.Tracer = visionary.PgxTracer(cfg.ConnConfig.Tracer) // keeps an existing tracer
pool, err := pgxpool.NewWithConfig(ctx, cfg)
```

Queries, `SendBatch` (one span per batch, `BATCH INSERT bookings`), `CopyFrom`
(`COPY trips`) and `Prepare` calls that reach the server (including pgx's
automatic statement cache, once per connection) become spans. Use one tracer
per pool. Tracers passed in are called after Visionary's, including their
batch/copy/prepare/connect hooks.

### database/sql

```go
db, err := visionary.OpenDB("sqlite", "file:app.db", "sqlite/app") // like sql.Open
// or, with a connector:
db := sql.OpenDB(visionary.WrapConnector(connector, "postgresql/shop"))
// or register a wrapped driver:
sql.Register("sqlite-traced", visionary.WrapDriver(&sqlite.Driver{}, "sqlite/app"))
```

The last argument labels the spans' `target` (system/database). Statements,
`BEGIN`, `COMMIT` and `ROLLBACK` are recorded; a query's span lasts until its
rows are closed and counts the rows read. The SDK's own writes are never
traced, so you can also pass the wrapped pool as `Config.Database`.

### Outbound HTTP

```go
client := &http.Client{Timeout: 10 * time.Second, Transport: visionary.Transport(nil)} // nil: http.DefaultTransport
req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
resp, err := client.Do(req)
```

Spans are named `METHOD host` and end when the response headers arrive.
Transport errors and 5xx responses are marked `error`. Query strings are never
stored.

`http.DefaultClient` is instrumented at `Start` (`VISIONARY_INSTRUMENT_HTTP=false`
turns that off), so `http.Get`, `http.Post` and `http.DefaultClient.Do` are
recorded too. A call joins the request's breakdown only when it carries the
handler's context: `http.Post(url, ...)` has none, so prefer
`http.NewRequestWithContext(ctx, ...)` with `http.DefaultClient.Do`. Without a
context the call is stored on its own when it takes the span threshold.

### Anything else

```go
end := visionary.StartSpan(ctx, visionary.KindCache, "GET session")
val, err := rdb.Get(ctx, key).Result()
end(err)
```

Kinds: `KindDB`, `KindHTTP`, `KindCache`, `KindQueue`, `KindCustom`. Wrap
CPU-heavy steps too (hashing, image resizing, building a report): the
request's breakdown shows time outside every span as untracked, and a custom
span names it.

### Covering everything a request does

`Middleware` records the request; it cannot see the calls inside it. A call
appears in the request's breakdown only when its client reports to the SDK
*and* it gets the request's context. Whatever is left shows up as
**Untracked** in the breakdown, placed where it happened, which tells you
which of these is missing:

**Vendor SDKs build their own HTTP clients.** `visionary.Transport` on your
clients does not reach them; hand them a traced one:

```go
// AWS SDK v2: S3, Cloudflare R2, SQS, SES…
traced := &http.Client{Transport: visionary.Transport(awshttp.NewBuildableClient().GetTransport())}
cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithHTTPClient(traced))

// openai-go, and Google APIs (Firebase, Maps): both take an *http.Client
client := openai.NewClient(option.WithHTTPClient(&http.Client{Transport: visionary.Transport(nil)}))
```

**Pass the context all the way down.** `http.Post(url, …)` and
`client.Get(url)` carry none, so the call cannot join its request:

```go
req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
resp, err := client.Do(req)
```

The same goes for `db.Query` against `db.QueryContext`, and for anything a
service method calls: take `ctx` and pass it on.

**Clients without a hook** (Redis, gRPC, a queue): wrap the call.

```go
end := visionary.StartSpan(ctx, visionary.KindCache, "GET session")
val, err := rdb.Get(ctx, key).Result()
end(err)
```

With go-redis, one hook covers every command and pipeline. Name spans by
command only, so keys never reach the database, and don't count a miss as an
error:

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

Calls through a Redis client show as **Cache** in the request's breakdown
only when they get the request's `ctx`, like every other call.

**Your own CPU work** (bcrypt, image resizing, rendering a report) takes no
call, so it stays untracked unless you name it:

```go
end := visionary.StartSpan(ctx, visionary.KindCustom, "hash otp")
hash, err := bcrypt.GenerateFromPassword(code, bcrypt.DefaultCost)
end(err)
```

**Work that outlives the response**: start it with `visionary.Go` and the
request's breakdown shows it as one `background` span, with the calls it made
and when it finished after the response. It gets the request's values but not
its cancellation, so the response being sent doesn't cut it short:

```go
visionary.Go(r.Context(), "send welcome email", func(ctx context.Context) error {
	return mailer.Send(ctx, user.Email)
})
```

Calls made after the response stay linked to their request either way (up to
`VISIONARY_SPAN_MAX` of them, then those over the span threshold), and are
not counted in its totals.

### GraphQL

Record each operation, for the Queries page (depth, field count, the queries
its resolvers ran). With gqlgen, from an `AroundOperations` extension:

```go
oc := graphql.GetOperationContext(ctx)
end := visionary.GraphQLOperation(ctx, string(oc.Operation.Operation), oc.OperationName, oc.RawQuery)
defer end(nil)
```

### Query plans

Give the Agent an explainer and the dashboard can ask for a statement's plan;
it runs `EXPLAIN` (never `EXPLAIN ANALYZE`) on your database and sends it back:

```go
cfg.Explain = []*visionary.Explainer{visionary.PgxExplainer(pool)}
// database/sql: visionary.SQLExplainer(db, visionary.Postgres, "postgresql/shop")
```

`VISIONARY_EXPLAIN=false` turns it off without a code change.

### What is stored

- Every logged request gets `db_count`/`db_ms` and `http_count`/`http_ms`
  (NULL for a kind no hook is installed for).
- Span rows are kept for every request, at most `VISIONARY_SPAN_MAX` (100)
  per request, in start order. Calls beyond the cap still count in the totals.
- Calls outside requests (workers, startup) are stored when they alone take
  `VISIONARY_SPAN_THRESHOLD_MS` (100), with no `request_id`.
- Statements are sanitized: literals become `?`, `IN (...)` lists become
  `IN (?)`. Parameter values are never stored. Names are `SELECT users`,
  `INSERT bookings`, `BEGIN`.
- `VISIONARY_SPANS=false` turns all of it off.

## Errors

```go
if err := h.store.SaveTrip(ctx, trip); err != nil {
	visionary.CaptureError(ctx, err) // handled = 1, linked to the request
	return err
}
```

The `type` is the innermost error of the `Unwrap` chain (`*pgconn.PgError`).
Errors with the same type and the same top five application frames
(`function@file`, no line numbers) share a `fingerprint`. Each fingerprint is
stored at most 20 times a minute per process. Errors captured during a request
are written when it ends, with its final route and user.
`VISIONARY_ERRORS=false` turns error capture off.

## CPU profiles

Profiling is **off until you turn it on in the dashboard** (Settings →
Profiling), and you can turn it off there again at any time, with no redeploy
either way. The Agent reads the switch from `visionary_settings` at start and
every 30 seconds on its own goroutine, and logs one line when profiling turns
on or off for this service. Settings → Profiling also picks which services take
part (by `VISIONARY_SERVICE`), how often a profile is taken (default every 300
s) and how long each one samples (default 10 s), and has a "Profile now" button
for a one-off.

While it is on, the Agent runs `runtime/pprof`'s CPU profiler (about one
sample every 10 ms, counting only goroutines running on a CPU) for the chosen
duration, then decodes the profile itself; no extra dependency. It skips a turn
when no request arrived since the last one, and keeps a profile when it has at
least 20 samples, whether or not a request finished during it (a "Profile now"
profile is kept with any samples). If the process
already runs its own CPU profile (`pprof.StartCPUProfile`, `net/http/pprof`),
the turn is skipped quietly.

Each profile is one `profiles` row:

- `folded`: one `frame;frame;frame count` line per distinct stack, outermost
  caller first, heaviest first, capped at 90 000 bytes (the rest summed into an
  `(other) n` line). A frame reads `pkg.Func file:line`: your code with its path
  relative to the module root (`handlers/trips.go`), modules as
  `lib:github.com/jackc/pgx/v5/conn.go`, the standard library and runtime as
  `std:net/http/server.go`. The SDK's own background goroutines are left out.
- `routes`: JSON of `"METHOD route"` to the milliseconds the requests that
  finished during the profile took, so the dashboard can tell which endpoints
  the CPU went to.
- `samples`, `duration_ms`, and the usual `service`, `instance`, `version`.

Cost: nothing between profiles beyond an atomic counter per request; while a
profile runs, the usual `pprof` overhead (a few percent of one CPU) and a map
update per finished request. Rows follow Settings → Retention (and
`VISIONARY_LOG_RETENTION_DAYS` when it is set).
The SDK creates neither table: the Visionary server does, so before it has
started nothing is profiled.

`VISIONARY_PROFILE=false` (or `Config.ProfileOff`) makes the service ignore
the dashboard and never profile, for code that must never be sampled.

## A complete service (Voygram-style)

A chi router, pgxpool, auth middleware that sets the user, and outbound
clients for third-party APIs:

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
	transport := visionary.Transport(http.DefaultTransport)
	places := &http.Client{Timeout: 10 * time.Second, Transport: transport}
	openai := &http.Client{Timeout: 60 * time.Second, Transport: transport}

	// Visionary: env config plus app-specific options.
	vc := visionary.FromEnv() // VISIONARY_SERVICE=voygram-api, release from the platform
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
		_ = openai
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
`db_count = 40`. When it is slow, its 40 `SELECT trip_stops` spans appear
on the request's timeline, and the dashboards flag the N+1.

## Notes

- Windows' monotonic clock ticks about every 0.5 ms, so very fast queries can
  show `duration_ms = 0` there. Linux and macOS measure in nanoseconds.
- Calls a handler starts in goroutines that finish after the response are
  stored on their own (if slow enough), still carrying the `request_id`.
- `Close` closes a database the SDK opened from `VISIONARY_DATABASE_URL`;
  a `Config.Database` pool belongs to the application and stays open.
