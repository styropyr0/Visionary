package main

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/styropyr0/visionary/internal/agentapi"
)

// pgEngine reads PostgreSQL 10 or later. A user with the pg_monitor role
// sees every session; pg_stat_statements must be loaded and created in the
// database the URL names for statement stats.
type pgEngine struct {
	notes   *noteSet
	version int // server_version_num
}

func (e *pgEngine) init(ctx context.Context, db *sql.DB) (string, error) {
	var v string
	if err := db.QueryRowContext(ctx, `SELECT current_setting('server_version'), current_setting('server_version_num')::int`).Scan(&v, &e.version); err != nil {
		return "", err
	}
	if e.version < 100000 {
		return "", errors.New("PostgreSQL " + v + " is too old: version 10 or later is needed")
	}
	if i := strings.IndexByte(v, ' '); i > 0 {
		v = v[:i] // "16.4 (Debian 16.4-1.pgdg120+1)"
	}
	return v, nil
}

const pgCounts = `SELECT count(*),
  count(*) FILTER (WHERE state = 'active'),
  count(*) FILTER (WHERE state LIKE 'idle in transaction%'),
  count(*) FILTER (WHERE wait_event_type = 'Lock'),
  COALESCE(EXTRACT(EPOCH FROM max(now() - query_start) FILTER (WHERE state = 'active')), 0)::float8,
  COALESCE(EXTRACT(EPOCH FROM max(now() - xact_start)), 0)::float8,
  COALESCE(EXTRACT(EPOCH FROM max(now() - state_change) FILTER (WHERE state LIKE 'idle in transaction%')), 0)::float8,
  count(*) FILTER (WHERE state IS NULL),
  current_setting('max_connections')::int
FROM pg_stat_activity
WHERE backend_type = 'client backend' AND pid <> pg_backend_pid()`

const pgCounters = `SELECT COALESCE(sum(xact_commit), 0)::float8, COALESCE(sum(xact_rollback), 0)::float8, COALESCE(sum(deadlocks), 0)::float8 FROM pg_stat_database`

// pgSessions are the sessions doing something: running, idle inside a
// transaction, or holding a lock another waits for. For a waiting session
// the lock it waits on comes from pg_locks; pg_blocking_pids, which is not
// free, runs only for those.
const pgSessions = `SELECT a.pid, COALESCE(a.usename, ''), COALESCE(a.datname, ''), COALESCE(host(a.client_addr), ''),
  COALESCE(a.application_name, ''), COALESCE(a.state, ''), COALESCE(a.query, ''), a.query_start,
  COALESCE(EXTRACT(EPOCH FROM now() - CASE WHEN a.state = 'active' THEN a.query_start ELSE a.state_change END), 0)::float8,
  EXTRACT(EPOCH FROM now() - a.xact_start)::float8,
  COALESCE(a.wait_event_type || ': ' || a.wait_event, ''),
  CASE WHEN a.wait_event_type = 'Lock' THEN array_to_string(pg_blocking_pids(a.pid), ',') ELSE '' END,
  COALESCE(l.locktype, ''), COALESCE(l.mode, ''), COALESCE(l.relation::regclass::text, '')
FROM pg_stat_activity a
LEFT JOIN LATERAL (SELECT locktype, mode, relation FROM pg_locks WHERE pid = a.pid AND NOT granted LIMIT 1) l ON a.wait_event_type = 'Lock'
WHERE a.backend_type = 'client backend' AND a.pid <> pg_backend_pid()
  AND (a.state IS DISTINCT FROM 'idle' OR a.pid IN (
    SELECT unnest(pg_blocking_pids(w.pid)) FROM pg_stat_activity w WHERE w.wait_event_type = 'Lock'))
  AND a.state IS NOT NULL
ORDER BY COALESCE(a.xact_start, a.query_start) NULLS LAST
LIMIT 200`

func (e *pgEngine) sample(ctx context.Context, db *sql.DB, now time.Time) (dbReading, error) {
	r := dbReading{values: map[string]float64{}, counters: map[string]float64{}}
	var conns, active, idleTx, waiting, hidden, maxConns int64
	var longQ, longTx, longIdle float64
	if err := db.QueryRowContext(ctx, pgCounts).Scan(&conns, &active, &idleTx, &waiting, &longQ, &longTx, &longIdle, &hidden, &maxConns); err != nil {
		return r, err
	}
	if hidden > 0 {
		e.notes.set("privilege", "Other users' sessions are hidden: GRANT pg_monitor TO the agent's user.")
	} else {
		e.notes.clear("privilege")
	}
	r.values = map[string]float64{"connections": float64(conns), "max_connections": float64(maxConns), "active": float64(active),
		"idle_in_tx": float64(idleTx), "waiting": float64(waiting), "longest_query_s": round(longQ, 1), "longest_tx_s": round(longTx, 1),
		"longest_idle_tx_s": round(longIdle, 1)}
	var commits, rollbacks, deadlocks float64
	if err := db.QueryRowContext(ctx, pgCounters).Scan(&commits, &rollbacks, &deadlocks); err == nil {
		r.counters = map[string]float64{"commits": commits, "rollbacks": rollbacks, "deadlocks": deadlocks}
	}
	rows, err := db.QueryContext(ctx, pgSessions)
	if err != nil {
		return r, err
	}
	defer rows.Close()
	for rows.Next() {
		var s agentapi.DBSession
		var start sql.NullTime
		var tx sql.NullFloat64
		var blocked string
		if err := rows.Scan(&s.PID, &s.User, &s.Database, &s.Client, &s.App, &s.State, &s.Query, &start, &s.ElapsedS, &tx,
			&s.WaitEvent, &blocked, &s.LockType, &s.LockMode, &s.LockObject); err != nil {
			return r, err
		}
		if start.Valid {
			s.QueryStart = start.Time.UTC().Format(time.RFC3339Nano)
		}
		if tx.Valid {
			v := round(tx.Float64, 1)
			s.TxAgeS = &v
		}
		s.ElapsedS = round(s.ElapsedS, 1)
		s.BlockedBy = parsePIDs(blocked)
		r.sessions = append(r.sessions, s)
	}
	return r, rows.Err()
}

func parsePIDs(s string) []int64 {
	var out []int64
	for _, f := range strings.Split(s, ",") {
		if n, err := strconv.ParseInt(strings.TrimSpace(f), 10, 64); err == nil && n > 0 {
			out = append(out, n)
		}
	}
	return out
}

func (e *pgEngine) statements(ctx context.Context, db *sql.DB) ([]stmtRow, error) {
	total, maxT := "total_exec_time", "max_exec_time"
	if e.version < 130000 {
		total, maxT = "total_time", "max_time"
	}
	// showtext false: the counters alone, without reading the text file.
	rows, err := db.QueryContext(ctx, `SELECT s.queryid::text, COALESCE(d.datname, ''), COALESCE(r.rolname, ''),
  s.calls::float8, s.`+total+`::float8, s.`+maxT+`::float8, s.rows::float8
FROM pg_stat_statements(false) s
LEFT JOIN pg_database d ON d.oid = s.dbid
LEFT JOIN pg_roles r ON r.oid = s.userid
WHERE s.queryid IS NOT NULL`)
	if err != nil {
		msg := err.Error()
		switch {
		case strings.Contains(msg, "does not exist"):
			e.notes.set("statements", "pg_stat_statements is not installed in this database: add it to shared_preload_libraries, restart, and run CREATE EXTENSION pg_stat_statements.")
		case strings.Contains(msg, "shared_preload_libraries"):
			e.notes.set("statements", "pg_stat_statements is created but not loaded: add it to shared_preload_libraries and restart PostgreSQL.")
		default:
			e.notes.set("statements", "Cannot read pg_stat_statements: "+clipRunes(msg, 200))
		}
		return nil, err
	}
	defer rows.Close()
	e.notes.clear("statements")
	var out []stmtRow
	for rows.Next() {
		var r stmtRow
		if err := rows.Scan(&r.id, &r.db, &r.user, &r.c.calls, &r.c.totalMs, &r.c.maxMs, &r.c.rows); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (e *pgEngine) texts(ctx context.Context, db *sql.DB, ids []string) (map[string]string, error) {
	args := make([]any, 0, len(ids))
	marks := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, err := strconv.ParseInt(id, 10, 64); err != nil {
			continue
		}
		args = append(args, id)
		marks = append(marks, "$"+strconv.Itoa(len(args)))
	}
	if len(args) == 0 {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT ON (queryid) queryid::text, COALESCE(query, '') FROM pg_stat_statements
WHERE queryid::text IN (`+strings.Join(marks, ", ")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, q string
		if err := rows.Scan(&id, &q); err != nil {
			return nil, err
		}
		out[id] = q
	}
	return out, rows.Err()
}
