package analytics

import (
	"context"
	"net/http"
	"time"

	sdk "github.com/styropyr0/visionary/sdks/go"
)

// Uptime checks (uptime_checks, uptime_results) and the public status page.
// Checks live in uptime_checks; the runner (uptime_runner.go) probes them and
// writes one uptime_results row per run, which the views, the alerts engine
// and the analyst read.

func (s *Server) uptimeSchema() []string {
	switch s.q.d {
	case sdk.Postgres:
		return []string{`CREATE TABLE IF NOT EXISTS uptime_checks (
    id               BIGSERIAL PRIMARY KEY,
    name             TEXT NOT NULL,
    url              TEXT NOT NULL,
    method           TEXT NOT NULL DEFAULT 'GET',
    expect_status    TEXT NOT NULL DEFAULT '200-299',
    body_contains    TEXT NOT NULL DEFAULT '',
    headers          TEXT NOT NULL DEFAULT '{}',
    interval_s       INTEGER NOT NULL DEFAULT 60,
    timeout_ms       INTEGER NOT NULL DEFAULT 10000,
    degraded_ms      INTEGER NOT NULL DEFAULT 0,
    follow_redirects INTEGER NOT NULL DEFAULT 1,
    enabled          INTEGER NOT NULL DEFAULT 1,
    on_status_page   INTEGER NOT NULL DEFAULT 0,
    status_show_url  INTEGER NOT NULL DEFAULT 0,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS uptime_results (
    id              BIGSERIAL PRIMARY KEY,
    created_at      TEXT NOT NULL,
    check_id        BIGINT NOT NULL,
    check_name      TEXT NOT NULL,
    url             TEXT NOT NULL,
    ok              INTEGER NOT NULL,
    status          INTEGER NOT NULL DEFAULT 0,
    duration_ms     DOUBLE PRECISION NOT NULL DEFAULT 0,
    error           TEXT,
    degraded        INTEGER NOT NULL DEFAULT 0,
    retried         INTEGER NOT NULL DEFAULT 0,
    dns_ms          DOUBLE PRECISION,
    connect_ms      DOUBLE PRECISION,
    tls_ms          DOUBLE PRECISION,
    ttfb_ms         DOUBLE PRECISION,
    cert_expires_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_uptime_results_check ON uptime_results (check_id, created_at);
CREATE INDEX IF NOT EXISTS idx_uptime_results_created ON uptime_results (created_at)`}
	case sdk.MySQL:
		return []string{`CREATE TABLE IF NOT EXISTS uptime_checks (
    id               BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    name             VARCHAR(255) NOT NULL,
    url              TEXT NOT NULL,
    method           VARCHAR(16) NOT NULL DEFAULT 'GET',
    expect_status    VARCHAR(255) NOT NULL DEFAULT '200-299',
    body_contains    TEXT NOT NULL,
    headers          TEXT NOT NULL,
    interval_s       INT NOT NULL DEFAULT 60,
    timeout_ms       INT NOT NULL DEFAULT 10000,
    degraded_ms      INT NOT NULL DEFAULT 0,
    follow_redirects INT NOT NULL DEFAULT 1,
    enabled          INT NOT NULL DEFAULT 1,
    on_status_page   INT NOT NULL DEFAULT 0,
    status_show_url  INT NOT NULL DEFAULT 0,
    created_at       VARCHAR(32) NOT NULL,
    updated_at       VARCHAR(32) NOT NULL
) CHARACTER SET utf8mb4;
CREATE TABLE IF NOT EXISTS uptime_results (
    id              BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    created_at      VARCHAR(32) NOT NULL,
    check_id        BIGINT NOT NULL,
    check_name      VARCHAR(255) NOT NULL,
    url             TEXT NOT NULL,
    ok              INT NOT NULL,
    status          INT NOT NULL DEFAULT 0,
    duration_ms     DOUBLE NOT NULL DEFAULT 0,
    error           TEXT,
    degraded        INT NOT NULL DEFAULT 0,
    retried         INT NOT NULL DEFAULT 0,
    dns_ms          DOUBLE,
    connect_ms      DOUBLE,
    tls_ms          DOUBLE,
    ttfb_ms         DOUBLE,
    cert_expires_at VARCHAR(32)
) CHARACTER SET utf8mb4;
CREATE INDEX idx_uptime_results_check ON uptime_results (check_id, created_at);
CREATE INDEX idx_uptime_results_created ON uptime_results (created_at)`}
	}
	return []string{`CREATE TABLE IF NOT EXISTS uptime_checks (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    name             TEXT    NOT NULL,
    url              TEXT    NOT NULL,
    method           TEXT    NOT NULL DEFAULT 'GET',
    expect_status    TEXT    NOT NULL DEFAULT '200-299',
    body_contains    TEXT    NOT NULL DEFAULT '',
    headers          TEXT    NOT NULL DEFAULT '{}',
    interval_s       INTEGER NOT NULL DEFAULT 60,
    timeout_ms       INTEGER NOT NULL DEFAULT 10000,
    degraded_ms      INTEGER NOT NULL DEFAULT 0,
    follow_redirects INTEGER NOT NULL DEFAULT 1,
    enabled          INTEGER NOT NULL DEFAULT 1,
    on_status_page   INTEGER NOT NULL DEFAULT 0,
    status_show_url  INTEGER NOT NULL DEFAULT 0,
    created_at       TEXT    NOT NULL,
    updated_at       TEXT    NOT NULL
);
CREATE TABLE IF NOT EXISTS uptime_results (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at      TEXT    NOT NULL,
    check_id        INTEGER NOT NULL,
    check_name      TEXT    NOT NULL,
    url             TEXT    NOT NULL,
    ok              INTEGER NOT NULL,
    status          INTEGER NOT NULL DEFAULT 0,
    duration_ms     REAL    NOT NULL DEFAULT 0,
    error           TEXT,
    degraded        INTEGER NOT NULL DEFAULT 0,
    retried         INTEGER NOT NULL DEFAULT 0,
    dns_ms          REAL,
    connect_ms      REAL,
    tls_ms          REAL,
    ttfb_ms         REAL,
    cert_expires_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_uptime_results_check ON uptime_results (check_id, created_at);
CREATE INDEX IF NOT EXISTS idx_uptime_results_created ON uptime_results (created_at)`}
}

func (s *Server) uptimeRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/uptime/checks", s.authed(s.handleUptimeList))
	mux.Handle("POST /api/uptime/checks", s.authed(s.handleUptimeCreate))
	mux.Handle("GET /api/uptime/checks/{id}", s.authed(s.handleUptimeGet))
	mux.Handle("PUT /api/uptime/checks/{id}", s.authed(s.handleUptimeUpdate))
	mux.Handle("DELETE /api/uptime/checks/{id}", s.authed(s.handleUptimeDelete))
	mux.Handle("POST /api/uptime/checks/{id}/enabled", s.authed(s.handleUptimeEnable))
	mux.Handle("POST /api/uptime/checks/{id}/run", s.authed(s.handleUptimeRun))
	mux.Handle("GET /api/uptime/checks/{id}/summary", s.authed(s.handleUptimeSummary))
	mux.Handle("GET /api/uptime/checks/{id}/incidents", s.authed(s.handleUptimeIncidents))
	mux.Handle("GET /api/uptime/checks/{id}/alert", s.authed(s.handleUptimeAlertGet))
	mux.Handle("PUT /api/uptime/checks/{id}/alert", s.authed(s.handleUptimeAlertSet))
	mux.Handle("GET /api/uptime/checks/{id}/{panel}", s.authed(s.handleUptimePanel))
	mux.HandleFunc("GET /status", s.handleStatusPage)
	mux.HandleFunc("GET /status.json", s.handleStatusJSON)
	mux.HandleFunc("GET /status.js", s.handleStatusScript)
}

func (s *Server) uptimeStart(ctx context.Context) {
	r := newUptimeRunner(s, s.uptimeSettings())
	uptimeRunners.Store(s, r)
	go r.run(ctx)
}

func (s *Server) uptimePrompt() string {
	return `uptime_results: one row per uptime check run (an HTTP probe the Visionary server makes every 30s–1h per check).
- created_at (ISO-8601 UTC text), check_id, check_name, url (credentials and secret query values removed).
- ok: 1 when the check passed (expected status, expected body text, no error), 0 when it failed. A failed attempt is retried once before a failure is recorded.
- status: HTTP status of the final response, 0 when there was none (DNS, connect, TLS or timeout errors). error: why it failed, NULL when ok.
- duration_ms: total time including reading the body. Phases of the last request: dns_ms, connect_ms, tls_ms (NULL for plain HTTP), ttfb_ms (request sent to first response byte).
- degraded: 1 when ok but slower than the check's latency threshold. retried: 1 when the first attempt failed. cert_expires_at: expiry of the TLS certificate (text, NULL for HTTP).
- Uptime % is 100.0 * SUM(CASE WHEN ok = 1 THEN 1 ELSE 0 END) / COUNT(*). An incident is a run of consecutive ok = 0 rows of one check_id ordered by created_at.`
}

// uptimeSettings are the runner's environment knobs.
type uptimeSettings struct {
	allowPrivate bool
	workers      int
}

func (s *Server) uptimeSettings() uptimeSettings {
	return uptimeSettings{allowPrivate: s.confBool("uptime_allow_private"), workers: 8}
}

func uptimeNow() string { return time.Now().UTC().Format(tsLayout) }
