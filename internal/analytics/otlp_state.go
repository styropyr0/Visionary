package analytics

import (
	"context"
	"database/sql"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/styropyr0/visionary/internal/d1"
	sdk "github.com/styropyr0/visionary/sdks/go"
)

// otlpState is the ingestion's memory: spans waiting for their request span,
// requests written recently (for children that arrive late), the last value
// of each counter, and the releases and error rates seen so far. All of it is
// bounded.
type otlpState struct {
	s        *Server
	d1       *d1.Client
	sql      *sql.DB // a small pool of our own, for transactions
	inflight chan struct{}
	limiter  *rate.Limiter

	spansOn, errorsOn bool
	thresholdMs       float64
	spanMax           int

	mu       sync.Mutex
	pending  map[string]*otlpPendingTrace // by trace id
	nPending int
	recent   map[string]*otlpRecent // by span id, every span of a written request
	series   map[string]otlpPoint   // last cumulative value per series, for rates
	// metricRows are the open system_metrics windows by instance and start.
	metricRows map[string]*otlpMetricRow
	lastRow    map[string]time.Time // last system_metrics row per instance
	releases   map[string]bool
	fpMinute   int64
	fpCount    map[string]int
	// excSeen holds the exceptions already stored per span, so one recorded
	// both as a span event and as a log record is stored once.
	excSeen map[string]time.Time
	// pendingLogs are exception log records waiting for their span, by span
	// id, so they link to the request.
	pendingLogs  map[string][]*otlpPendingLog
	nPendingLogs int

	startOnce sync.Once
}

const (
	// otlpHold is how long spans wait for the request span they belong to;
	// SDKs export every 5 s by default, and parents end after their children.
	otlpHold          = 30 * time.Second
	otlpRecentTTL     = 2 * time.Minute
	otlpMaxPending    = 50000
	otlpMaxRecent     = 200000
	otlpMaxSeries     = 50000
	otlpErrorsPerMin  = 20
	otlpMaxMetricRows = 2000
)

var otlpStates sync.Map // *Server -> *otlpState

// otlp returns the server's ingestion state and makes sure the background
// flush runs.
func (s *Server) otlp() *otlpState {
	v, ok := otlpStates.Load(s)
	if !ok {
		v, _ = otlpStates.LoadOrStore(s, s.newOTLPState())
	}
	o := v.(*otlpState)
	o.start(context.Background())
	return o
}

// otlpStart runs the background flush until ctx ends. Ingestion also starts
// it on the first export, so it works before this hook is wired.
func (s *Server) otlpStart(ctx context.Context) { s.otlp().start(ctx) }

func (s *Server) newOTLPState() *otlpState {
	o := &otlpState{
		s:           s,
		inflight:    make(chan struct{}, otlpMaxInFlight),
		limiter:     rate.NewLimiter(rate.Limit(otlpEnvInt("VISIONARY_OTLP_RATE", 50)), 100),
		spansOn:     otlpEnvBool("VISIONARY_SPANS", true),
		errorsOn:    otlpEnvBool("VISIONARY_ERRORS", true),
		thresholdMs: float64(otlpEnvInt("VISIONARY_SPAN_THRESHOLD_MS", 100)),
		spanMax:     otlpEnvInt("VISIONARY_SPAN_MAX", 100),
		pending:     map[string]*otlpPendingTrace{},
		recent:      map[string]*otlpRecent{},
		series:      map[string]otlpPoint{},
		metricRows:  map[string]*otlpMetricRow{},
		lastRow:     map[string]time.Time{},
		releases:    map[string]bool{},
		fpCount:     map[string]int{},
		excSeen:     map[string]time.Time{},
		pendingLogs: map[string][]*otlpPendingLog{},
	}
	// db.DB has neither batches nor transactions, which ingestion needs: an
	// export is one D1 batch call or one SQL transaction.
	cfg := s.cfg
	if cfg.DatabaseURL == "" || strings.HasPrefix(strings.ToLower(cfg.DatabaseURL), "d1:") {
		o.d1 = d1.New(cfg.D1APIBase, cfg.D1AccountID, cfg.D1DatabaseID, cfg.D1APIToken)
		return o
	}
	dsn := cfg.DatabaseURL
	if strings.HasPrefix(strings.ToLower(dsn), "sqlite") && !strings.Contains(dsn, "busy_timeout") {
		// Wait for other writers (the SDKs, the server) instead of failing.
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		dsn += sep + "_pragma=busy_timeout(5000)"
	}
	conn, d, err := sdk.OpenURL(dsn)
	if err != nil {
		s.logger.Warn("otlp: own connection pool unavailable; writing without transactions", "err", err)
		return o
	}
	conn.SetMaxOpenConns(4)
	if d == sdk.SQLite {
		conn.SetMaxOpenConns(1) // SQLite has one writer anyway
	}
	o.sql = conn
	return o
}

func (o *otlpState) start(ctx context.Context) {
	o.startOnce.Do(func() {
		go func() {
			t := time.NewTicker(2 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					o.flushDue(ctx)
				}
			}
		}()
	})
}

// flushDue writes what waited long enough: spans whose request span never
// came, and metric windows nothing else closed.
func (o *otlpState) flushDue(ctx context.Context) {
	defer func() {
		if p := recover(); p != nil {
			o.s.logger.Error("otlp flush panicked", "panic", p)
		}
	}()
	now := time.Now()
	rows := o.expireTraces(now)
	metrics := o.dueMetrics(now)
	for _, t := range metrics.order {
		for _, row := range metrics.tables[t] {
			rows.add(t, row)
		}
	}
	if rows.empty() {
		return
	}
	wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := o.store(wctx, rows); err != nil {
		o.s.logger.Warn("otlp flush", "err", err)
	}
}

// store writes rows, adding a releases row for each service version that
// is new to the database.
func (o *otlpState) store(ctx context.Context, rows *otlpRows) error {
	keys := make([]string, 0, len(rows.releases))
	for key := range rows.releases {
		keys = append(keys, key)
	}
	for _, row := range rows.releases {
		known, err := o.releaseStored(ctx, row[1], row[3].(string))
		if err != nil {
			o.mu.Lock()
			for _, k := range keys {
				delete(o.releases, k)
			}
			o.mu.Unlock()
			return err
		}
		if !known {
			rows.add(otlpReleasesTable, row)
		}
	}
	if err := o.write(ctx, rows); err != nil {
		o.mu.Lock()
		for _, k := range keys {
			delete(o.releases, k) // try again with the next export
		}
		o.mu.Unlock()
		return err
	}
	return nil
}

// noteRelease queues a releases row for res's version unless it is known.
// The caller holds o.mu.
func (o *otlpState) noteRelease(rows *otlpRows, res *otlpResource, ns uint64) {
	if res.version == "" || ns == 0 {
		return
	}
	key := res.service + "\n" + res.version
	at := otlpTime(ns)
	if old, ok := rows.releases[key]; ok {
		if at < old[0].(string) {
			old[0] = at // the earliest time marks the deploy
		}
		return
	}
	if o.releases[key] {
		return
	}
	if rows.releases == nil {
		rows.releases = map[string][]any{}
	}
	if len(o.releases) > 10000 {
		o.releases = map[string]bool{}
	}
	// Claimed now, so concurrent exports do not add it twice; store gives
	// it back if the write fails.
	o.releases[key] = true
	rows.releases[key] = []any{at, otlpNullStr(res.service), res.instance, res.version, "otlp", otlpNullStr(res.sdkVersion), otlpNullStr(res.runtime)}
}

func (o *otlpState) releaseStored(ctx context.Context, service any, version string) (bool, error) {
	q, args := "SELECT 1 FROM releases WHERE version = ? AND service IS NULL LIMIT 1", []any{version}
	if service != nil {
		q, args = "SELECT 1 FROM releases WHERE version = ? AND service = ? LIMIT 1", []any{version, service}
	}
	res, err := o.s.db.Query(ctx, q, args...)
	if err != nil {
		return false, err
	}
	return len(res.Rows) > 0, nil
}

func otlpEnvInt(key string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key))); err == nil && v >= 0 {
		return v
	}
	return def
}

func otlpEnvBool(key string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "true", "1", "yes":
		return true
	case "false", "0", "no":
		return false
	}
	return def
}
