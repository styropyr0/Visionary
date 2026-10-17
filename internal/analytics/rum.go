package analytics

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/styropyr0/visionary/sdks/go"
)

// Browser and mobile ingestion (client_events), sdks/SPEC.md "Client ingest API".
//
// The SDKs post batches to POST /api/ingest/client; the handler validates
// them, a background writer inserts them, and the Frontend view reads them
// through GET /api/rum/{panel}.

// rumState is the feature's runtime state. The Server struct is shared by
// every feature, so the state hangs off it through rumStates instead.
type rumState struct {
	origins   []string // allowed browser origins; empty allows any
	limits    *rumLimiter
	writer    *rumWriter
	retention int // days; 0 keeps rows forever
}

var rumStates sync.Map // *Server -> *rumState

func (s *Server) rum() *rumState {
	if st, ok := rumStates.Load(s); ok {
		return st.(*rumState)
	}
	st := &rumState{
		origins:   parseOrigins(os.Getenv("VISIONARY_CLIENT_ORIGINS")),
		limits:    newRUMLimiter(rumEnvInt("VISIONARY_CLIENT_RATE_PER_IP", 120), rumEnvInt("VISIONARY_CLIENT_RATE_PER_KEY", 6000)),
		writer:    newRUMWriter(s),
		retention: rumEnvInt("VISIONARY_CLIENT_RETENTION_DAYS", 30),
	}
	actual, _ := rumStates.LoadOrStore(s, st)
	return actual.(*rumState)
}

func (s *Server) rumSchema() []string {
	switch s.q.d {
	case sdk.Postgres:
		return []string{`CREATE TABLE IF NOT EXISTS client_events (
    id          BIGSERIAL PRIMARY KEY,
    created_at  TEXT NOT NULL,
    received_at TEXT NOT NULL,
    app         TEXT NOT NULL,
    app_version TEXT,
    version     TEXT,
    session_id  TEXT,
    user_id     TEXT,
    device      TEXT,
    os          TEXT,
    country     TEXT,
    kind        TEXT NOT NULL,
    name        TEXT,
    url         TEXT,
    method      TEXT,
    status      INTEGER,
    duration_ms DOUBLE PRECISION,
    value       DOUBLE PRECISION,
    message     TEXT,
    stack       TEXT,
    fingerprint TEXT,
    attrs       TEXT
);
CREATE INDEX IF NOT EXISTS idx_client_events_created ON client_events (created_at);
CREATE INDEX IF NOT EXISTS idx_client_events_kind ON client_events (kind, created_at);
CREATE INDEX IF NOT EXISTS idx_client_events_app ON client_events (app, created_at);
CREATE INDEX IF NOT EXISTS idx_client_events_fingerprint ON client_events (fingerprint, created_at)`}
	case sdk.MySQL:
		return []string{`CREATE TABLE IF NOT EXISTS client_events (
    id          BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    created_at  VARCHAR(32) NOT NULL,
    received_at VARCHAR(32) NOT NULL,
    app         VARCHAR(32) NOT NULL,
    app_version VARCHAR(64),
    version     VARCHAR(64),
    session_id  VARCHAR(128),
    user_id     VARCHAR(128),
    device      VARCHAR(128),
    os          VARCHAR(64),
    country     VARCHAR(2),
    kind        VARCHAR(16) NOT NULL,
    name        VARCHAR(256),
    url         VARCHAR(2048),
    method      VARCHAR(16),
    status      INT,
    duration_ms DOUBLE,
    value       DOUBLE,
    message     TEXT,
    stack       MEDIUMTEXT,
    fingerprint VARCHAR(16),
    attrs       TEXT
) CHARACTER SET utf8mb4;
CREATE INDEX idx_client_events_created ON client_events (created_at);
CREATE INDEX idx_client_events_kind ON client_events (kind, created_at);
CREATE INDEX idx_client_events_app ON client_events (app, created_at);
CREATE INDEX idx_client_events_fingerprint ON client_events (fingerprint, created_at)`}
	}
	return []string{`CREATE TABLE IF NOT EXISTS client_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at  TEXT NOT NULL,
    received_at TEXT NOT NULL,
    app         TEXT NOT NULL,
    app_version TEXT,
    version     TEXT,
    session_id  TEXT,
    user_id     TEXT,
    device      TEXT,
    os          TEXT,
    country     TEXT,
    kind        TEXT NOT NULL,
    name        TEXT,
    url         TEXT,
    method      TEXT,
    status      INTEGER,
    duration_ms REAL,
    value       REAL,
    message     TEXT,
    stack       TEXT,
    fingerprint TEXT,
    attrs       TEXT
);
CREATE INDEX IF NOT EXISTS idx_client_events_created ON client_events (created_at);
CREATE INDEX IF NOT EXISTS idx_client_events_kind ON client_events (kind, created_at);
CREATE INDEX IF NOT EXISTS idx_client_events_app ON client_events (app, created_at);
CREATE INDEX IF NOT EXISTS idx_client_events_fingerprint ON client_events (fingerprint, created_at)`}
}

func (s *Server) rumRoutes(mux *http.ServeMux) {
	// Public: the SDKs authenticate with a client key, not a session.
	mux.HandleFunc("POST /api/ingest/client", s.handleClientIngest)
	mux.HandleFunc("OPTIONS /api/ingest/client", s.handleClientPreflight)
	mux.Handle("GET /api/rum/errors/{fingerprint}", s.authed(s.handleRUMError))
	mux.Handle("GET /api/rum/{panel}", s.authed(s.handleRUM))
}

func (s *Server) rumStart(ctx context.Context) {
	st := s.rum()
	go st.writer.run(ctx)
	go st.limits.sweep(ctx)
	if st.retention > 0 {
		go s.rumRetention(ctx, st.retention)
	}
}

func (s *Server) rumPrompt() string {
	return `Table client_events: one row per event sent by the browser and mobile SDKs (real user monitoring). Columns:
- created_at (event time, same text format as api_logs), received_at (when the server got it)
- app ('web', 'android', 'ios'), app_version (the app's version name), version (build or commit), session_id (one browser tab or app launch), user_id (when the app set one), device (e.g. 'Chrome 141 on macOS', 'Pixel 8'), os, country (ISO 3166 alpha-2 from the CDN, NULL when unknown)
- kind, and what the other columns mean for it:
  - 'page_load' (web): name = page path with ids as :id, duration_ms = navigation start to load, url = page URL without query string and with ids as :id (so it groups by page). Single-page app route changes are page_load rows too, with attrs soft = 1.
  - 'web_vital' (web): name = 'LCP', 'INP', 'CLS', 'FCP' or 'TTFB'; value in ms (CLS is unitless); url = the page, written like page_load.url. Google's thresholds (good up to / poor above): LCP 2500 / 4000 ms, INP 200 / 500 ms, CLS 0.1 / 0.25, FCP 1800 / 3000 ms, TTFB 800 / 1800 ms. Report the 75th percentile, not the average.
  - 'screen' (mobile): name = screen or activity, duration_ms = time to first draw.
  - 'api': a call the client made; name = 'METHOD /path' with ids as :id, url (no query string), method, status (0 = network failure), duration_ms as the user experienced it, network included. Compared with api_logs.duration_ms for the same route, the difference is network and client time.
  - 'error': name = error type, message, stack, url, fingerprint (the same error has the same fingerprint: GROUP BY fingerprint).
  - 'custom': name, value, duration_ms, attrs.
- attrs: JSON object text with extra flat values.
Count sessions with COUNT(DISTINCT session_id) and users with COUNT(DISTINCT user_id). Always filter on kind.`
}

// rumRetention deletes old client events 60 s after start and then daily.
func (s *Server) rumRetention(ctx context.Context, days int) {
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		s.deleteOldClientEvents(ctx, days)
		timer.Reset(24 * time.Hour)
	}
}

func (s *Server) deleteOldClientEvents(ctx context.Context, days int) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("client_events retention panicked", "panic", r)
		}
	}()
	cutoff := time.Now().UTC().AddDate(0, 0, -days).Format(tsLayout)
	stmt := `DELETE FROM client_events WHERE id IN (SELECT id FROM client_events WHERE created_at < ? LIMIT 5000)`
	if s.q.d == sdk.MySQL {
		stmt = `DELETE FROM client_events WHERE created_at < ? ORDER BY id LIMIT 5000`
	}
	deleted := int64(0)
	for round := 0; round < 200; round++ {
		// The shared database layer does not report affected rows, so count
		// first (stopping at 5001) to know whether another round is needed.
		res, err := s.db.Query(ctx, `SELECT COUNT(*) FROM (SELECT id FROM client_events WHERE created_at < ? LIMIT 5001) AS old`, cutoff)
		if err == nil {
			var n int64
			if len(res.Rows) > 0 && len(res.Rows[0]) > 0 {
				n = toInt(res.Rows[0][0])
			}
			if n == 0 {
				break
			}
			if err = s.db.Exec(ctx, stmt, cutoff); err == nil {
				deleted += min(n, 5000)
				if n <= 5000 {
					break
				}
				continue
			}
		}
		if ctx.Err() == nil {
			s.logger.Warn("client_events retention", "err", err)
		}
		return
	}
	if deleted > 0 {
		s.logger.Info("client_events retention", "deleted", deleted, "older_than_days", days)
	}
}

func parseOrigins(raw string) []string {
	var out []string
	for _, o := range strings.Split(raw, ",") {
		if o = strings.TrimRight(strings.TrimSpace(o), "/"); o != "" {
			out = append(out, strings.ToLower(o))
		}
	}
	return out
}

// rumEnvInt reads a non-negative integer; 0 is a valid value (off).
func rumEnvInt(key string, fallback int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key))); err == nil && v >= 0 {
		return v
	}
	return fallback
}
