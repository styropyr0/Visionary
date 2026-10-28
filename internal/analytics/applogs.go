package analytics

import (
	"context"
	"net/http"
	"strings"

	sdk "github.com/styropyr0/visionary/sdks/go"
)

// app_logs.severity runs 1 (trace) to 6 (fatal); pattern is filled later by the pattern job.

func init() {
	for _, t := range []string{"APP_LOGS", "LOG_PATTERNS"} {
		allowedTables[t] = true
	}
}

func (s *Server) applogsRoutes(mux *http.ServeMux) {
	s.applogsIngestRoutes(mux)
	s.applogsAPIRoutes(mux)
	s.applogsSettingsRoutes(mux)
	s.applogsAnalysisRoutes(mux)
	s.applogsAIRoutes(mux)
	s.applogsSessionRoutes(mux)
	s.applogsArchiveRoutes(mux)
}

func (s *Server) applogsSchema() []string {
	return []string{s.q.d.LogsSchema(), s.q.logTables()}
}

func (q sqlDialect) logTables() string {
	id, key, ts := "INTEGER PRIMARY KEY AUTOINCREMENT", "TEXT", "TEXT"
	tail, index := "", "CREATE INDEX IF NOT EXISTS"
	switch q.d {
	case sdk.Postgres:
		id = "BIGSERIAL PRIMARY KEY"
	case sdk.MySQL:
		id, key, ts = "BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY", "VARCHAR(255)", "VARCHAR(32)"
		tail, index = " CHARACTER SET utf8mb4", "CREATE INDEX"
	}
	ddl := `CREATE TABLE IF NOT EXISTS log_patterns (
    id            {key} NOT NULL PRIMARY KEY,
    service       {key},
    template      TEXT NOT NULL,
    sample        TEXT,
    level         {key},
    severity      INTEGER NOT NULL DEFAULT 3,
    first_seen    {ts} NOT NULL,
    last_seen     {ts} NOT NULL,
    first_version {key},
    code_file     TEXT,
    code_line     INTEGER,
    lines         BIGINT NOT NULL DEFAULT 0,
    bytes         BIGINT NOT NULL DEFAULT 0,
    viewed_at     {ts}
){tail};
{index} idx_log_patterns_last_seen ON log_patterns (last_seen);
{index} idx_log_patterns_first_seen ON log_patterns (first_seen);
CREATE TABLE IF NOT EXISTS log_views (
    id         {id},
    name       {key} NOT NULL,
    query      TEXT NOT NULL,
    columns    TEXT,
    created_by {key},
    created_at {ts} NOT NULL
){tail};
CREATE TABLE IF NOT EXISTS log_archives (
    id         {id},
    hour       {ts} NOT NULL,
    object     TEXT NOT NULL,
    lines      BIGINT NOT NULL DEFAULT 0,
    bytes      BIGINT NOT NULL DEFAULT 0,
    created_at {ts} NOT NULL
){tail};
{index} idx_log_archives_hour ON log_archives (hour);
{index} idx_client_events_session ON client_events (session_id, created_at)`
	if q.d == sdk.MySQL {
		// MySQL can't index TEXT without a length
		ddl = strings.Replace(ddl, "id            {key} NOT NULL PRIMARY KEY", "id            VARCHAR(16) NOT NULL PRIMARY KEY", 1)
	}
	return strings.NewReplacer("{id}", id, "{key}", key, "{ts}", ts, "{tail}", tail, "{index}", index).Replace(ddl)
}

// applogsStart also adds a trigram index on PostgreSQL when it can, so text search doesn't scan the range.
func (s *Server) applogsStart(ctx context.Context) {
	s.syslogStart(ctx)
	s.applogsPatternsStart(ctx)
	s.applogsArchiveStart(ctx)
	if s.q.d != sdk.Postgres {
		return
	}
	go func() {
		if err := s.db.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS pg_trgm`); err != nil {
			s.logger.Info("logs: pg_trgm unavailable; text search scans the time range", "err", err)
			return
		}
		if err := s.db.Exec(ctx, `CREATE INDEX IF NOT EXISTS idx_app_logs_message_trgm ON app_logs USING gin (message gin_trgm_ops)`); err != nil {
			s.logger.Warn("logs: trigram index", "err", err)
		}
	}()
}

func (s *Server) applogsPrompt() string {
	return `Table app_logs: application log lines (what services print), from the SDKs' logging hooks, OpenTelemetry, the ingest API, syslog and the host agent (journald, files, container output).
- id, created_at TEXT (same format), service, instance, version TEXT, source TEXT ('go', 'node', 'python', 'ruby', 'java', 'dotnet', 'otlp', 'http', 'bulk', 'loki', 'syslog', 'journald', 'file', 'docker', 'browser', 'android', 'ios'), host, container TEXT
- level TEXT ('trace', 'debug', 'info', 'warn', 'error', 'fatal') and severity INTEGER (1 trace .. 6 fatal; use severity >= 5 for errors and worse), logger TEXT, message TEXT, attrs TEXT (a flat JSON object of the line's fields; read one with the dialect's JSON function)
- request_id TEXT (join to api_logs.request_id and spans.request_id), trace_id, span_id, user_id, session_id TEXT
- code_file TEXT, code_line INTEGER, code_func TEXT: where the line was logged, when the SDK knows
- pattern TEXT: the id of its message template in log_patterns; size INTEGER bytes stored
- Debug lines are kept only for failed or slow requests unless someone lowered the level, so a request's debug lines usually mean it failed or was slow.
- Lines at ERROR inside requests that answered 2xx or 3xx are failures the API did not report: join app_logs to api_logs on request_id where severity >= 5 and status < 400.

Table log_patterns: one row per message template per service, with numbers, ids, quoted strings and the like replaced by <*>.
- id TEXT (= app_logs.pattern), service, template TEXT, sample TEXT (one real line), level TEXT, severity INTEGER, first_seen, last_seen TEXT, first_version TEXT (the release it first appeared in), code_file TEXT, code_line INTEGER, lines and bytes (counted since first seen)
- New patterns of a release: first_version = that version. Cite log lines by id when explaining a problem.`
}
