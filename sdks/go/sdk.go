// Package visionary collects what Visionary analyses, straight from a Go service:
// one row per HTTP request (api_logs) and periodic host, process, runtime,
// database-pool and throughput samples (system_metrics), written to Cloudflare
// D1 in batches on background goroutines.
//
//	agent, err := sdk.New(sdk.FromEnv())
//	if err != nil { log.Fatal(err) }
//	agent.Start(ctx)
//	defer agent.Close(context.Background())
//	http.ListenAndServe(":8080", agent.Middleware(mux))
//
// Inside handlers or auth middleware, attach the signed-in user with
// sdk.SetUser(r.Context(), sdk.User{ID: "42"}). Everything is configurable
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

	"github.com/styropyr0/visionary/internal/d1"
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
func FromEnv() Config {
	return Config{
		DatabaseURL: env("VISIONARY_DATABASE_URL", ""),
		D1: D1Config{
			AccountID:  env("D1_ACCOUNT_ID", ""),
			DatabaseID: env("D1_DATABASE_ID", ""),
			APIToken:   env("D1_API_TOKEN", ""),
			APIBase:    env("D1_API_BASE", ""),
		},
		Instance: env("VISIONARY_INSTANCE", ""),
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

// Agent runs request logging and system metrics. A nil *Agent is a valid
// no-op, so callers need no checks when Visionary is not configured.
type Agent struct {
	cfg      Config
	db       store
	requests *requestWriter
	system   *recorder
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
		return sqlStore{db: db, d: d}, nil
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
		cfg.Instance, _ = os.Hostname()
	}
	a := &Agent{cfg: cfg, db: db}
	if cfg.Requests.Enabled {
		a.requests = newRequestWriter(cfg.Requests, db, cfg.Logger)
	}
	if cfg.System.Enabled && cfg.System.Interval > 0 {
		a.system = newRecorder(cfg.System, cfg.Instance, db, cfg.Logger)
	}
	return a, nil
}

// Start creates the tables if needed and launches the background workers.
// They stop when ctx ends; call Close to flush what is queued.
func (a *Agent) Start(ctx context.Context) {
	if a == nil {
		return
	}
	schemaCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	d := a.db.dialect()
	if a.requests != nil {
		if err := a.db.execScript(schemaCtx, d.RequestsSchema()); err != nil {
			a.cfg.Logger.Error("visionary: create api_logs failed", "err", err)
		}
		a.requests.start(ctx)
	}
	if a.system != nil {
		if err := a.db.execScript(schemaCtx, d.SystemSchema()); err != nil {
			a.cfg.Logger.Error("visionary: create system_metrics failed", "err", err)
		}
		a.system.start(ctx)
	}
}

// Close flushes queued requests and buffered samples, waiting until ctx ends
// at most.
func (a *Agent) Close(ctx context.Context) {
	if a == nil {
		return
	}
	if a.requests != nil {
		a.requests.close(ctx)
	}
	if a.system != nil {
		a.system.close(ctx)
	}
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
