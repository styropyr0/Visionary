// Package visionary collects what Visionary analyses, straight from a Go
// service: one row per HTTP request (api_logs), the database and outbound HTTP
// calls each request makes (spans), panics and captured errors (errors), a
// deploy marker per release (releases), and periodic host, process, runtime,
// database-pool and throughput samples (system_metrics). Rows are written in
// batches on background goroutines to Cloudflare D1, SQLite, PostgreSQL or
// MySQL; nothing on a request path waits for the database.
//
//	agent, err := visionary.New(visionary.FromEnv())
//	if err != nil { log.Print(err) } // a nil *Agent is a no-op
//	agent.Start(ctx)
//	defer agent.Close(context.Background())
//	http.ListenAndServe(":8080", agent.Middleware(mux))
//
// Inside handlers or auth middleware, attach the signed-in user with
// visionary.SetUser(r.Context(), visionary.User{ID: "42"}).
//
// Calls become spans through hooks on the clients the application already
// uses, all keyed off the request context:
//
//	pgxCfg.ConnConfig.Tracer = visionary.PgxTracer()        // pgx v5 / pgxpool
//	db, _ := visionary.OpenDB("sqlite", dsn, "sqlite/app")  // database/sql
//	client := &http.Client{Transport: visionary.Transport(nil)}
//	end := visionary.StartSpan(ctx, visionary.KindCache, "GET session"); end(err)
//
// Panics reaching Middleware are recorded and re-panicked; handled errors are
// recorded with visionary.CaptureError(ctx, err). Everything is configurable
// through VISIONARY_* environment variables; see FromEnv.
package visionary

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/styropyr0/visionary/sdks/go/d1"
)

// User is who made a request. Only ID is required; Handle and Role make the
// dashboards friendlier and let the analyst group by role.
type User struct {
	ID     string
	Handle string
	Role   string
}

// DBStats is a database pool snapshot. Counters (Acquires, EmptyAcquires,
// AcquireWait) are cumulative; the recorder turns them into per-sample deltas.
type DBStats struct {
	Total, Idle, InUse, Max int64
	Acquires, EmptyAcquires int64
	AcquireWait             time.Duration
	// noAcquires marks Acquires as unknown (database/sql has no such
	// counter), so db_acquires is stored as NULL rather than 0.
	noAcquires bool
}

// Config chooses where rows go, in this order: Database (a connection the
// app already has), DatabaseURL, then D1.
type Config struct {
	// Database is an open database/sql pool to write to; Dialect says which
	// SQL flavour it speaks. The app owns the pool and its driver.
	Database *sql.DB
	Dialect  Dialect
	// DatabaseURL (postgres://, mysql://, sqlite:///path, or d1://) is opened
	// by the SDK; see OpenURL for the drivers it needs.
	DatabaseURL string
	D1          D1Config
	// Instance labels this server's rows (defaults to the hostname).
	Instance string
	Logger   *slog.Logger

	// Service names this service, for databases several services share
	// (VISIONARY_SERVICE). Release is the deployed version or commit
	// (VISIONARY_RELEASE, else the platform's commit variable); when set,
	// Start writes a releases row so dashboards can mark the deploy.
	Service string
	Release string

	// Spans turns on per-request call capture: db_*/http_* totals and span
	// rows on every logged request, at most SpanMax per request. Calls
	// outside a request are kept when they alone take SpanThreshold.
	Spans         bool
	SpanThreshold time.Duration
	SpanMax       int
	// InstrumentHTTP makes http.DefaultClient (http.Get, http.Post, ...)
	// record its calls; other clients need Transport.
	InstrumentHTTP bool
	// Errors records panics that reach Middleware and CaptureError calls.
	Errors bool

	Requests RequestsConfig
	System   SystemConfig
}

type D1Config struct {
	AccountID, DatabaseID, APIToken string
	// APIBase overrides the Cloudflare API URL (for a proxy or a stand-in).
	APIBase string
}

type RequestsConfig struct {
	Enabled bool
	// PathPrefix limits logging to matching paths, e.g. "/api". Empty logs all.
	PathPrefix string
	// CaptureBodies stores request and response bodies (redacted, capped at
	// MaxBodyBytes). Off, only sizes are kept.
	CaptureBodies bool
	MaxBodyBytes  int
	QueueSize     int
	BatchSize     int
	FlushInterval time.Duration
	RetentionDays int

	// RedactHeaders and RedactFields are matched case-insensitively; values
	// are replaced with [REDACTED] before anything is stored.
	RedactHeaders []string
	RedactFields  []string
	// RedactRequestFields are redacted in request bodies only, for names that
	// carry secrets on the way in but harmless data on the way out.
	RedactRequestFields []string
	// ErrorCodeField and ErrorKeyField are dotted paths into JSON error
	// responses, stored as error_code and error_key.
	ErrorCodeField string
	ErrorKeyField  string

	// Route returns the route pattern for a request (e.g. "/users/{id}"),
	// so requests group well. Called after the handler. Defaults to the
	// Go 1.22+ ServeMux pattern when available, otherwise the path.
	Route func(r *http.Request) string
	// LookupUser fills in Handle and Role for a user ID, on the background
	// writer. Keep it cheap (a cache); it is called once per user per batch.
	LookupUser func(ctx context.Context, id string) (User, bool)
}

type SystemConfig struct {
	Enabled       bool
	Interval      time.Duration
	RetentionDays int
	// DB reports database pool stats; optional. See PgxPool-style adapters in
	// the README, or SQLDB for database/sql.
	DB func() DBStats
	// DiskPath is the filesystem whose usage is reported (default: the
	// working directory's volume).
	DiskPath string
}

// FromEnv builds a Config from VISIONARY_* variables. Request logging and
// system metrics are on by default; set them to false to opt out.
//
//	VISIONARY_DATABASE_URL               (postgres://, mysql://, sqlite:///path, d1://)
//	D1_ACCOUNT_ID, D1_DATABASE_ID, D1_API_TOKEN, D1_API_BASE
//	VISIONARY_INSTANCE                   (default: hostname)
//	VISIONARY_LOG_REQUESTS=true|false    (default true)
//	VISIONARY_LOG_PATH_PREFIX            (e.g. /api; default: everything)
//	VISIONARY_LOG_BODIES=true|false      (default true)
//	VISIONARY_LOG_MAX_BODY_BYTES         (default 8192)
//	VISIONARY_LOG_RETENTION_DAYS         (default 30, 0 keeps forever)
//	VISIONARY_LOG_REDACT_HEADERS         (comma list, added to the defaults)
//	VISIONARY_LOG_REDACT_FIELDS          (comma list, added to the defaults)
//	VISIONARY_LOG_REDACT_REQUEST_FIELDS  (comma list, request bodies only)
//	VISIONARY_LOG_ERROR_CODE_FIELD       (default error.code)
//	VISIONARY_LOG_ERROR_KEY_FIELD        (default error.key)
//	VISIONARY_SYSTEM_METRICS=true|false  (default true)
//	VISIONARY_SYSTEM_INTERVAL            (default 15s)
//	VISIONARY_SYSTEM_RETENTION_DAYS      (default 30)
//	VISIONARY_SERVICE                    (default: none)
//	VISIONARY_RELEASE                    (default: RAILWAY_GIT_COMMIT_SHA, RENDER_GIT_COMMIT,
//	                                      VERCEL_GIT_COMMIT_SHA, HEROKU_SLUG_COMMIT, SOURCE_VERSION,
//	                                      GIT_COMMIT, GITHUB_SHA or K_REVISION; hashes cut to 12)
//	VISIONARY_SPANS=true|false           (default true)
//	VISIONARY_SPAN_THRESHOLD_MS          (default 100)
//	VISIONARY_SPAN_MAX                   (default 100)
//	VISIONARY_INSTRUMENT_HTTP=true|false (default true)
//	VISIONARY_ERRORS=true|false          (default true)
func FromEnv() Config {
	return Config{
		DatabaseURL: env("VISIONARY_DATABASE_URL", ""),
		D1: D1Config{
			AccountID:  env("D1_ACCOUNT_ID", ""),
			DatabaseID: env("D1_DATABASE_ID", ""),
			APIToken:   env("D1_API_TOKEN", ""),
			APIBase:    env("D1_API_BASE", ""),
		},
		Instance:       env("VISIONARY_INSTANCE", ""),
		Service:        env("VISIONARY_SERVICE", ""),
		Release:        releaseFromEnv(),
		Spans:          envBool("VISIONARY_SPANS", true),
		SpanThreshold:  time.Duration(envInt("VISIONARY_SPAN_THRESHOLD_MS", 100)) * time.Millisecond,
		SpanMax:        envInt("VISIONARY_SPAN_MAX", 100),
		InstrumentHTTP: envBool("VISIONARY_INSTRUMENT_HTTP", true),
		Errors:         envBool("VISIONARY_ERRORS", true),
		Requests: RequestsConfig{
			Enabled:             envBool("VISIONARY_LOG_REQUESTS", true),
			PathPrefix:          env("VISIONARY_LOG_PATH_PREFIX", ""),
			CaptureBodies:       envBool("VISIONARY_LOG_BODIES", true),
			MaxBodyBytes:        envInt("VISIONARY_LOG_MAX_BODY_BYTES", 8<<10),
			RetentionDays:       envInt("VISIONARY_LOG_RETENTION_DAYS", 30),
			RedactHeaders:       envList("VISIONARY_LOG_REDACT_HEADERS"),
			RedactFields:        envList("VISIONARY_LOG_REDACT_FIELDS"),
			RedactRequestFields: envList("VISIONARY_LOG_REDACT_REQUEST_FIELDS"),
			ErrorCodeField:      env("VISIONARY_LOG_ERROR_CODE_FIELD", "error.code"),
			ErrorKeyField:       env("VISIONARY_LOG_ERROR_KEY_FIELD", "error.key"),
		},
		System: SystemConfig{
			Enabled:       envBool("VISIONARY_SYSTEM_METRICS", true),
			Interval:      envDuration("VISIONARY_SYSTEM_INTERVAL", 15*time.Second),
			RetentionDays: envInt("VISIONARY_SYSTEM_RETENTION_DAYS", 30),
		},
	}
}

// Agent runs request logging, span and error capture, and system metrics. A
// nil *Agent is a valid no-op, so callers need no checks when Visionary is not
// configured.
type Agent struct {
	cfg     Config
	db      store
	ident   identity
	writer  *writer
	system  *recorder
	limiter errLimiter
}

// ErrNotConfigured is returned when no database is configured. Callers can
// treat it as "collection off" and continue with a nil Agent.
var ErrNotConfigured = errors.New("visionary: no database configured (set VISIONARY_DATABASE_URL, pass Config.Database, or set the D1_* variables)")

func openStore(cfg Config) (store, error) {
	switch {
	case cfg.Database != nil:
		if cfg.Dialect == "" {
			return nil, errUnknownDialect
		}
		return sqlStore{db: cfg.Database, d: cfg.Dialect}, nil
	case cfg.DatabaseURL != "" && !strings.HasPrefix(strings.ToLower(cfg.DatabaseURL), "d1:"):
		db, d, err := OpenURL(cfg.DatabaseURL)
		if err != nil {
			return nil, err
		}
		return sqlStore{db: db, d: d, owned: true}, nil
	}
	c := d1.New(cfg.D1.APIBase, cfg.D1.AccountID, cfg.D1.DatabaseID, cfg.D1.APIToken)
	if c == nil {
		return nil, ErrNotConfigured
	}
	return d1Store{c: c}, nil
}

func New(cfg Config) (*Agent, error) {
	db, err := openStore(cfg)
	if err != nil {
		return nil, err
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Instance == "" {
		cfg.Instance = hostname()
	}
	if cfg.SpanMax <= 0 {
		cfg.SpanMax = 100
	}
	cfg.Release = shortRelease(cfg.Release)
	a := &Agent{cfg: cfg, db: db, ident: identity{service: cfg.Service, instance: cfg.Instance, version: cfg.Release}}
	if cfg.Requests.Enabled || cfg.Spans || cfg.Errors {
		a.writer = newWriter(cfg.Requests, cfg.Requests.Enabled, a.ident, db, cfg.Logger)
	}
	if cfg.System.Enabled && cfg.System.Interval > 0 {
		a.system = newRecorder(cfg.System, a.ident, db, cfg.Logger)
	}
	return a, nil
}

// Start creates the tables if needed, writes the releases row and launches
// the background workers. They stop when ctx ends; call Close to flush what
// is queued. The started Agent also records spans and errors that happen
// outside any request.
func (a *Agent) Start(ctx context.Context) {
	if a == nil {
		return
	}
	schemaCtx, cancel := context.WithTimeout(quiet(ctx), 20*time.Second)
	defer cancel()
	d := a.db.dialect()
	if a.cfg.Requests.Enabled {
		if err := a.db.execScript(schemaCtx, d.RequestsSchema()); err != nil {
			a.cfg.Logger.Error("visionary: create api_logs failed", "err", err)
		}
		if err := migrateRequests(schemaCtx, a.db); err != nil {
			a.cfg.Logger.Error("visionary: add api_logs columns failed", "err", err)
		}
	}
	if a.writer != nil || a.ident.version != "" {
		if err := a.db.execScript(schemaCtx, d.TelemetrySchema()); err != nil {
			a.cfg.Logger.Error("visionary: create telemetry tables failed", "err", err)
		}
		a.writeRelease(schemaCtx)
	}
	if a.writer != nil {
		a.writer.start(ctx)
		active.Store(a)
		if a.cfg.Spans && a.cfg.InstrumentHTTP {
			instrumentDefaultClient()
		}
	}
	if a.system != nil {
		if err := a.db.execScript(schemaCtx, d.SystemSchema()); err != nil {
			a.cfg.Logger.Error("visionary: create system_metrics failed", "err", err)
		}
		if err := migrateSystem(schemaCtx, a.db); err != nil {
			a.cfg.Logger.Error("visionary: add system_metrics columns failed", "err", err)
		}
		a.system.start(ctx)
	}
}

// Close flushes queued rows and buffered samples, waiting until ctx ends at
// most.
func (a *Agent) Close(ctx context.Context) {
	if a == nil {
		return
	}
	active.CompareAndSwap(a, nil)
	if a.writer != nil {
		a.writer.close(ctx)
	}
	if a.system != nil {
		a.system.close(ctx)
	}
	_ = a.db.close()
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func envBool(k string, def bool) bool {
	if v, err := strconv.ParseBool(env(k, "")); err == nil {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(env(k, "")); err == nil && v >= 0 {
		return v
	}
	return def
}

func envDuration(k string, def time.Duration) time.Duration {
	v := env(k, "")
	if v == "" {
		return def
	}
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(n) * time.Second
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	return def
}

func envList(k string) []string {
	var out []string
	for _, s := range strings.Split(env(k, ""), ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
