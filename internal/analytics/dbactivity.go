package analytics

import (
	"context"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/styropyr0/visionary/internal/agentapi"
	"github.com/styropyr0/visionary/internal/sqlredact"
	sdk "github.com/styropyr0/visionary/sdks/go"
)

// Database activity: the host agent reads the PostgreSQL and MySQL servers
// named in VISIONARY_AGENT_DATABASES and sends, with each report, every
// server's samples (db_stats), the sessions it saw running (db_sessions) and
// what its busiest statements did since the previous read (db_statements).
// db_servers holds one row per agent and server with its engine, version
// and what the agent could not read. The Databases view reads them through
// GET /api/databases/... (dbactivity_api.go).

const (
	dbMaxServers    = 20
	dbMaxSamples    = 1000
	dbMaxSessions   = 500
	dbMaxStatements = 400
)

var dbServerName = regexp.MustCompile(`^[A-Za-z0-9._:/@-]{1,128}$`)

var (
	dbStatTable    = &otlpTable{"db_stats", append([]string{"created_at", "host", "server", "engine"}, agentapi.DBColumns...)}
	dbSessionTable = &otlpTable{"db_sessions", []string{"created_at", "host", "server", "pid", "username", "db_name", "client", "app", "state",
		"query", "query_start", "elapsed_s", "tx_age_s", "wait_event", "blocked_by", "lock_type", "lock_mode", "lock_object", "seen_at"}}
	dbStatementTable = &otlpTable{"db_statements", []string{"created_at", "host", "server", "engine", "query_id", "db_name", "username", "name",
		"query", "calls", "total_ms", "max_ms", "row_count", "rows_examined", "errors", "interval_s"}}
)

// dbRealCols are the DBColumns stored as decimals; the rest are counts.
var dbRealCols = map[string]bool{"longest_query_s": true, "longest_tx_s": true, "longest_idle_tx_s": true}

func init() {
	for _, t := range []string{"DB_SERVERS", "DB_STATS", "DB_SESSIONS", "DB_STATEMENTS"} {
		allowedTables[t] = true
	}
}

func (q sqlDialect) dbActivityTables() string {
	id, key, ts, real, big := "INTEGER PRIMARY KEY AUTOINCREMENT", "TEXT", "TEXT", "REAL", "INTEGER"
	tail, index := "", "CREATE INDEX IF NOT EXISTS"
	switch q.d {
	case sdk.Postgres:
		id, real, big = "BIGSERIAL PRIMARY KEY", "DOUBLE PRECISION", "BIGINT"
	case sdk.MySQL:
		id, key, ts, real, big = "BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY", "VARCHAR(255)", "VARCHAR(32)", "DOUBLE", "BIGINT"
		tail, index = " CHARACTER SET utf8mb4", "CREATE INDEX"
	}
	ddl := `CREATE TABLE IF NOT EXISTS db_servers (
    host       {key} NOT NULL,
    server     {key} NOT NULL,
    engine     {key} NOT NULL,
    version    TEXT,
    notes      TEXT,
    first_seen {ts} NOT NULL,
    last_seen  {ts} NOT NULL,
    PRIMARY KEY (host, server)
){tail};
CREATE TABLE IF NOT EXISTS db_stats (
    id                {id},
    created_at        {ts} NOT NULL,
    host              {key} NOT NULL,
    server            {key} NOT NULL,
    engine            {key} NOT NULL,
    connections       INTEGER,
    max_connections   INTEGER,
    active            INTEGER,
    idle_in_tx        INTEGER,
    waiting           INTEGER,
    longest_query_s   {real},
    longest_tx_s      {real},
    longest_idle_tx_s {real},
    commits           {big},
    rollbacks         {big},
    deadlocks         {big}
){tail};
{index} idx_db_stats_created ON db_stats (created_at);
{index} idx_db_stats_server_created ON db_stats (server, created_at);
{index} idx_db_stats_host_created ON db_stats (host, created_at);
CREATE TABLE IF NOT EXISTS db_sessions (
    id          {id},
    created_at  {ts} NOT NULL,
    host        {key} NOT NULL,
    server      {key} NOT NULL,
    pid         {big} NOT NULL,
    username    TEXT,
    db_name     TEXT,
    client      TEXT,
    app         TEXT,
    state       TEXT,
    query       TEXT,
    query_start {ts},
    elapsed_s   {real},
    tx_age_s    {real},
    wait_event  TEXT,
    blocked_by  TEXT,
    lock_type   TEXT,
    lock_mode   TEXT,
    lock_object TEXT,
    seen_at     {ts} NOT NULL
){tail};
{index} idx_db_sessions_created ON db_sessions (created_at);
{index} idx_db_sessions_server_created ON db_sessions (server, created_at);
{index} idx_db_sessions_host_created ON db_sessions (host, created_at);
CREATE TABLE IF NOT EXISTS db_statements (
    id            {id},
    created_at    {ts} NOT NULL,
    host          {key} NOT NULL,
    server        {key} NOT NULL,
    engine        {key} NOT NULL,
    query_id      {key} NOT NULL,
    db_name       TEXT,
    username      TEXT,
    name          TEXT,
    query         TEXT,
    calls         {big} NOT NULL,
    total_ms      {real} NOT NULL,
    max_ms        {real},
    row_count     {big},
    rows_examined {big},
    errors        {big},
    interval_s    {real}
){tail};
{index} idx_db_statements_created ON db_statements (created_at);
{index} idx_db_statements_server_created ON db_statements (server, created_at);
{index} idx_db_statements_host_created ON db_statements (host, created_at)`
	return strings.NewReplacer("{id}", id, "{key}", key, "{ts}", ts, "{real}", real, "{big}", big, "{tail}", tail, "{index}", index).Replace(ddl)
}

// dbRows adds a report's database activity to rows. Statement text is
// redacted again here: the server never stores a literal value.
func dbRows(rep *agentapi.Report, at string, rows *otlpRows, n map[string]int) {
	host := rep.Host
	now := time.Now()
	for _, d := range capped(rep.Databases, dbMaxServers) {
		if !dbServerName.MatchString(d.Server) || (d.Engine != "postgres" && d.Engine != "mysql") {
			continue
		}
		for _, smp := range capped(d.Samples, dbMaxSamples) {
			ts, ok := reportTime(smp.At, now)
			if !ok || len(smp.Values) == 0 {
				continue
			}
			row := []any{ts, host, d.Server, d.Engine}
			for _, col := range agentapi.DBColumns {
				v, ok := smp.Values[col]
				switch {
				case !ok || math.IsNaN(v) || math.IsInf(v, 0) || v < 0:
					row = append(row, nil)
				case dbRealCols[col]:
					row = append(row, finite(v, 1))
				default:
					row = append(row, int64(math.Round(v)))
				}
			}
			rows.add(dbStatTable, row)
			n["db_samples"]++
		}
		for _, s := range capped(d.Sessions, dbMaxSessions) {
			seen, ok := reportTime(s.SeenAt, now)
			if !ok || s.PID <= 0 {
				continue
			}
			var start, tx any
			if ts, ok := reportTime(s.QueryStart, now); ok {
				start = ts
			}
			if s.TxAgeS != nil {
				tx = finite(math.Max(*s.TxAgeS, 0), 1)
			}
			rows.add(dbSessionTable, []any{at, host, d.Server, s.PID, otlpNullStr(clip(s.User, 128)), otlpNullStr(clip(s.Database, 128)),
				otlpNullStr(clip(s.Client, 128)), otlpNullStr(clip(s.App, 128)), otlpNullStr(clip(s.State, 64)),
				otlpNullStr(sqlredact.Redact(d.Engine, s.Query)), start, finite(math.Max(s.ElapsedS, 0), 1), tx,
				otlpNullStr(clip(s.WaitEvent, 128)), otlpNullStr(pidList(s.BlockedBy)), otlpNullStr(clip(s.LockType, 64)),
				otlpNullStr(clip(s.LockMode, 64)), otlpNullStr(clip(s.LockObject, 256)), seen})
			n["db_sessions"]++
		}
		for _, st := range capped(d.Statements, dbMaxStatements) {
			ts, ok := reportTime(st.At, now)
			id := clip(st.ID, 128)
			if !ok || id == "" || st.Calls <= 0 || st.TotalMs < 0 || math.IsNaN(st.TotalMs) || math.IsInf(st.TotalMs, 0) {
				continue
			}
			text := sqlredact.Redact(d.Engine, st.Query)
			name := any(nil)
			if text != "" {
				name = sdk.SQLName(text)
			}
			rows.add(dbStatementTable, []any{ts, host, d.Server, d.Engine, id, otlpNullStr(clip(st.Database, 128)), otlpNullStr(clip(st.User, 128)),
				name, otlpNullStr(text), st.Calls, finite(st.TotalMs, 3), positive(st.MaxMs, 3), max(st.Rows, 0),
				nonNegPtr(st.RowsExamined), nonNegPtr(st.Errors), positive(st.IntervalS, 1)})
			n["db_statements"]++
		}
	}
}

func nonNegPtr(v *int64) any {
	if v == nil || *v < 0 {
		return nil
	}
	return *v
}

// pidList is "12,40": the sessions blocking one.
func pidList(pids []int64) string {
	var b strings.Builder
	for _, p := range capped(pids, 50) {
		if p <= 0 {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatInt(p, 10))
	}
	return b.String()
}

// upsertDBServers records each reported server with its engine, version and
// notes, keeping first_seen.
func (s *Server) upsertDBServers(ctx context.Context, rep *agentapi.Report, at string) error {
	cols := []string{"host", "server", "engine", "version", "notes", "first_seen", "last_seen"}
	ph := "?"
	if s.cfg.DatabaseURL == "" || strings.HasPrefix(strings.ToLower(s.cfg.DatabaseURL), "d1:") {
		ph = "NULLIF(?, '')" // D1 sends every parameter as a string
	}
	values := strings.TrimSuffix(strings.Repeat(ph+", ", len(cols)), ", ")
	stmt := "INSERT INTO db_servers (" + strings.Join(cols, ", ") + ") VALUES (" + values + ")"
	if s.q.d == sdk.MySQL {
		stmt += " ON DUPLICATE KEY UPDATE engine = VALUES(engine), version = VALUES(version), notes = VALUES(notes), last_seen = VALUES(last_seen)"
	} else {
		stmt += " ON CONFLICT (host, server) DO UPDATE SET engine = excluded.engine, version = excluded.version, notes = excluded.notes, last_seen = excluded.last_seen"
	}
	for _, d := range capped(rep.Databases, dbMaxServers) {
		if !dbServerName.MatchString(d.Server) || (d.Engine != "postgres" && d.Engine != "mysql") {
			continue
		}
		var notes []string
		for _, n := range capped(d.Notes, 10) {
			if n = clip(n, 400); n != "" {
				notes = append(notes, n)
			}
		}
		if err := s.db.Exec(ctx, stmt, rep.Host, d.Server, d.Engine, otlpNullStr(clip(d.Version, 64)),
			otlpNullStr(strings.Join(notes, "\n")), at, at); err != nil {
			return err
		}
	}
	return nil
}

const dbActivityPrompt = `Database activity: PostgreSQL and MySQL servers the host agent reads (VISIONARY_AGENT_DATABASES). server is the server's label, host the agent's host.
- db_servers: one row per host and server: engine ('postgres' or 'mysql'), version, notes (what the agent could not read, newline-separated), first_seen, last_seen.
- db_stats: one row per server per sample (15 s): created_at, host, server, engine, connections, max_connections, active (running a statement), idle_in_tx (idle inside an open transaction), waiting (waiting on a lock), longest_query_s, longest_tx_s (oldest open transaction), longest_idle_tx_s, commits, rollbacks, deadlocks (counts since the previous sample).
- db_sessions: sessions seen running, one row per statement execution per report (about a minute): created_at (the report), host, server, pid, username, db_name, client, app, state ('active', 'idle in transaction', or MySQL's command), query (literals replaced with ?), query_start, elapsed_s (how long it had run when last seen; for idle in transaction how long idle), tx_age_s, wait_event, blocked_by (comma-separated pids holding the lock it waits for), lock_type, lock_mode, lock_object, seen_at (the last sample that saw it). Running now = the rows of a server's latest created_at whose seen_at is that report's latest seen_at. One execution seen in several reports has a row in each: group by server, pid, query_start for distinct executions.
- db_statements: what each normalized statement did between two reads of pg_stat_statements or performance_schema digests (about a minute): created_at, host, server, engine, query_id, db_name, username, name (e.g. 'SELECT users'), query (literals replaced with ?), calls, total_ms, max_ms (slowest call since the server's stats were reset, not per interval), row_count, rows_examined and errors (MySQL), interval_s. Values are deltas: SUM them over a range; mean time = SUM(total_ms) / SUM(calls).`
