package main

import (
	"context"
	"database/sql"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/styropyr0/visionary/internal/agentapi"
)

// mysqlEngine reads MySQL 5.7 or later and MariaDB 10.3 or later. PROCESS
// lets it see every connection and InnoDB's transactions and lock waits;
// SELECT on performance_schema gives statement digests and, on MySQL 8,
// lock waits.
type mysqlEngine struct {
	notes   *noteSet
	mariadb bool
	// noTrx: INNODB_TRX was not readable; locks: where lock waits are read,
	// "ps" (performance_schema, MySQL 8), "is" (information_schema) or "none".
	noTrx     bool
	locks     string
	noMetrics bool
}

func (e *mysqlEngine) init(ctx context.Context, db *sql.DB) (string, error) {
	var v string
	if err := db.QueryRowContext(ctx, `SELECT VERSION()`).Scan(&v); err != nil {
		return "", err
	}
	e.mariadb = strings.Contains(strings.ToLower(v), "mariadb")
	return v, nil
}

// mysqlProc is one PROCESSLIST row with its InnoDB transaction, if any.
type mysqlProc struct {
	id                                   int64
	user, host, db, command, state, info string
	time                                 int64
	trxAge                               sql.NullInt64
	trxState                             string
}

const mysqlProcs = `SELECT p.ID, COALESCE(p.USER, ''), COALESCE(p.HOST, ''), COALESCE(p.DB, ''), COALESCE(p.COMMAND, ''),
  COALESCE(p.STATE, ''), COALESCE(p.INFO, ''), COALESCE(p.TIME, 0), TIMESTAMPDIFF(SECOND, t.trx_started, NOW()), COALESCE(t.trx_state, '')
FROM information_schema.PROCESSLIST p
LEFT JOIN information_schema.INNODB_TRX t ON t.trx_mysql_thread_id = p.ID
WHERE p.ID <> CONNECTION_ID()`

const mysqlProcsNoTrx = `SELECT ID, COALESCE(USER, ''), COALESCE(HOST, ''), COALESCE(DB, ''), COALESCE(COMMAND, ''),
  COALESCE(STATE, ''), COALESCE(INFO, ''), COALESCE(TIME, 0), NULL, ''
FROM information_schema.PROCESSLIST WHERE ID <> CONNECTION_ID()`

func (e *mysqlEngine) sample(ctx context.Context, db *sql.DB, now time.Time) (dbReading, error) {
	r := dbReading{values: map[string]float64{}, counters: map[string]float64{}}
	status, err := mysqlStatus(ctx, db, "Threads_connected", "Handler_commit", "Handler_rollback", "Innodb_deadlocks")
	if err != nil {
		return r, err
	}
	var maxConns float64
	if err := db.QueryRowContext(ctx, `SELECT @@max_connections`).Scan(&maxConns); err != nil {
		return r, err
	}
	procs, err := e.processes(ctx, db)
	if err != nil {
		return r, err
	}
	r.values, r.sessions = mysqlSummary(procs, now)
	r.values["max_connections"] = maxConns
	if v, ok := status["Threads_connected"]; ok {
		r.values["connections"] = max(v-1, 0) // less the agent's own
		if seen := float64(len(procs)); seen < v-1 && v-1-seen > 2 {
			e.notes.set("privilege", "Only the agent's own connections are visible: GRANT PROCESS ON *.* TO the agent's user.")
		} else {
			e.notes.clear("privilege")
		}
	}
	r.counters["commits"], r.counters["rollbacks"] = status["Handler_commit"], status["Handler_rollback"]
	if v, ok := status["Innodb_deadlocks"]; ok { // MariaDB
		r.counters["deadlocks"] = v
	} else if !e.noMetrics {
		var n float64
		if err := db.QueryRowContext(ctx, `SELECT COUNT FROM information_schema.INNODB_METRICS WHERE NAME = 'lock_deadlocks' AND STATUS = 'enabled'`).Scan(&n); err == nil {
			r.counters["deadlocks"] = n
		} else {
			e.noMetrics = true
		}
	}
	if r.values["waiting"] > 0 {
		waits := e.lockWaits(ctx, db, mysqlMetadataWaits(r.sessions))
		applyLockWaits(r.sessions, waits)
	}
	return r, nil
}

func (e *mysqlEngine) processes(ctx context.Context, db *sql.DB) ([]mysqlProc, error) {
	q := mysqlProcs
	if e.noTrx {
		q = mysqlProcsNoTrx
	}
	rows, err := db.QueryContext(ctx, q)
	if err != nil && !e.noTrx {
		e.noTrx = true
		e.notes.set("trx", "InnoDB transactions are not readable, so idle-in-transaction sessions and lock waits are missing: GRANT PROCESS ON *.* TO the agent's user.")
		rows, err = db.QueryContext(ctx, mysqlProcsNoTrx)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []mysqlProc
	for rows.Next() {
		var p mysqlProc
		if err := rows.Scan(&p.id, &p.user, &p.host, &p.db, &p.command, &p.state, &p.info, &p.time, &p.trxAge, &p.trxState); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// mysqlSystemThread is a server thread rather than a client's connection.
func mysqlSystemThread(p mysqlProc) bool {
	switch p.user {
	case "event_scheduler", "system user", "unauthenticated user":
		return true
	}
	switch p.command {
	case "Daemon", "Binlog Dump", "Binlog Dump GTID", "Killed":
		return true
	}
	return false
}

// mysqlSummary counts the connections' states and lists the sessions doing
// something: running a statement or idle inside an open transaction.
func mysqlSummary(procs []mysqlProc, now time.Time) (map[string]float64, []agentapi.DBSession) {
	var active, idleTx, waiting, longQ, longTx, longIdle float64
	var out []agentapi.DBSession
	for _, p := range procs {
		if mysqlSystemThread(p) {
			continue
		}
		inTx := p.trxAge.Valid
		sleeping := p.command == "Sleep"
		if sleeping && !inTx {
			continue
		}
		s := agentapi.DBSession{PID: p.id, User: p.user, Database: p.db, Client: stripPort(p.host), Query: p.info,
			ElapsedS: float64(max(p.time, 0)), WaitEvent: p.state}
		if inTx {
			age := float64(max(p.trxAge.Int64, 0))
			s.TxAgeS = &age
			longTx = max(longTx, age)
		}
		if sleeping {
			s.State = "idle in transaction"
			idleTx++
			longIdle = max(longIdle, s.ElapsedS)
		} else {
			s.State = p.command
			active++
			longQ = max(longQ, s.ElapsedS)
			s.QueryStart = now.Add(-time.Duration(s.ElapsedS) * time.Second).Format(time.RFC3339Nano)
		}
		if p.trxState == "LOCK WAIT" || (strings.HasPrefix(p.state, "Waiting for") && strings.Contains(p.state, "lock")) {
			waiting++
		}
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ElapsedS > out[j].ElapsedS })
	if len(out) > dbMaxSessions {
		out = out[:dbMaxSessions]
	}
	return map[string]float64{"active": active, "idle_in_tx": idleTx, "waiting": waiting,
		"longest_query_s": longQ, "longest_tx_s": longTx, "longest_idle_tx_s": longIdle}, out
}

// stripPort drops the client port of PROCESSLIST's "10.0.0.5:53422".
func stripPort(h string) string {
	i := strings.LastIndexByte(h, ':')
	if i <= 0 {
		return h
	}
	if _, err := strconv.Atoi(h[i+1:]); err != nil {
		return h
	}
	return strings.Trim(h[:i], "[]")
}

// lockWait is one waiting session and the one blocking it.
type lockWait struct {
	waiting, blocking         int64
	object, lockType, lockMod string
}

func mysqlMetadataWaits(ss []agentapi.DBSession) bool {
	for _, s := range ss {
		if strings.Contains(s.WaitEvent, "metadata lock") {
			return true
		}
	}
	return false
}

const mysqlInnoWaitsPS = `SELECT rt.PROCESSLIST_ID, bt.PROCESSLIST_ID, COALESCE(CONCAT_WS('.', l.OBJECT_SCHEMA, l.OBJECT_NAME), ''),
  COALESCE(l.LOCK_TYPE, ''), COALESCE(l.LOCK_MODE, '')
FROM performance_schema.data_lock_waits w
JOIN performance_schema.threads rt ON rt.THREAD_ID = w.REQUESTING_THREAD_ID
JOIN performance_schema.threads bt ON bt.THREAD_ID = w.BLOCKING_THREAD_ID
LEFT JOIN performance_schema.data_locks l ON l.ENGINE = w.ENGINE AND l.ENGINE_LOCK_ID = w.REQUESTING_ENGINE_LOCK_ID
WHERE rt.PROCESSLIST_ID IS NOT NULL AND bt.PROCESSLIST_ID IS NOT NULL
LIMIT 500`

const mysqlInnoWaitsIS = `SELECT r.trx_mysql_thread_id, b.trx_mysql_thread_id, COALESCE(l.lock_table, ''), COALESCE(l.lock_type, ''), COALESCE(l.lock_mode, '')
FROM information_schema.INNODB_LOCK_WAITS w
JOIN information_schema.INNODB_TRX b ON b.trx_id = w.blocking_trx_id
JOIN information_schema.INNODB_TRX r ON r.trx_id = w.requesting_trx_id
LEFT JOIN information_schema.INNODB_LOCKS l ON l.lock_id = w.requested_lock_id
LIMIT 500`

// A pending table metadata lock is waiting on every other session holding
// one on the same table, as sys.schema_table_lock_waits reads it.
const mysqlMetaWaits = `SELECT wt.PROCESSLIST_ID, bt.PROCESSLIST_ID, CONCAT_WS('.', w.OBJECT_SCHEMA, w.OBJECT_NAME), 'metadata', w.LOCK_TYPE
FROM performance_schema.metadata_locks w
JOIN performance_schema.metadata_locks b ON b.OBJECT_TYPE = w.OBJECT_TYPE AND b.OBJECT_SCHEMA <=> w.OBJECT_SCHEMA
  AND b.OBJECT_NAME <=> w.OBJECT_NAME AND b.LOCK_STATUS = 'GRANTED' AND b.OWNER_THREAD_ID <> w.OWNER_THREAD_ID
JOIN performance_schema.threads wt ON wt.THREAD_ID = w.OWNER_THREAD_ID
JOIN performance_schema.threads bt ON bt.THREAD_ID = b.OWNER_THREAD_ID
WHERE w.LOCK_STATUS = 'PENDING' AND w.OBJECT_TYPE = 'TABLE' AND wt.PROCESSLIST_ID IS NOT NULL AND bt.PROCESSLIST_ID IS NOT NULL
LIMIT 500`

// lockWaits reads who waits on whom, only when some session waits.
func (e *mysqlEngine) lockWaits(ctx context.Context, db *sql.DB, metadata bool) []lockWait {
	var out []lockWait
	read := func(q string) error {
		rows, err := db.QueryContext(ctx, q)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var w lockWait
			if err := rows.Scan(&w.waiting, &w.blocking, &w.object, &w.lockType, &w.lockMod); err != nil {
				return err
			}
			out = append(out, w)
		}
		return rows.Err()
	}
	switch e.locks {
	case "":
		if read(mysqlInnoWaitsPS) == nil {
			e.locks = "ps"
		} else if out = nil; read(mysqlInnoWaitsIS) == nil {
			e.locks = "is"
		} else {
			out, e.locks = nil, "none"
			e.notes.set("locks", "Lock waits are not readable: on MySQL 8 GRANT SELECT ON performance_schema.* and PROCESS; on MariaDB GRANT PROCESS.")
		}
	case "ps":
		_ = read(mysqlInnoWaitsPS)
	case "is":
		_ = read(mysqlInnoWaitsIS)
	}
	if metadata && !e.mariadb {
		_ = read(mysqlMetaWaits)
	}
	return out
}

// applyLockWaits sets each waiting session's blockers and the lock it wants.
func applyLockWaits(ss []agentapi.DBSession, waits []lockWait) {
	byPID := map[int64]int{}
	for i, s := range ss {
		byPID[s.PID] = i
	}
	for _, w := range waits {
		i, ok := byPID[w.waiting]
		if !ok {
			continue
		}
		s := &ss[i]
		dup := false
		for _, b := range s.BlockedBy {
			dup = dup || b == w.blocking
		}
		if !dup && w.blocking != w.waiting {
			s.BlockedBy = append(s.BlockedBy, w.blocking)
		}
		if s.LockType == "" {
			s.LockType, s.LockMode, s.LockObject = strings.ToLower(w.lockType), w.lockMod, strings.ReplaceAll(w.object, "`", "")
		}
	}
}

// mysqlStatus reads global status counters by name.
func mysqlStatus(ctx context.Context, db *sql.DB, names ...string) (map[string]float64, error) {
	q := `SHOW GLOBAL STATUS WHERE Variable_name IN ('` + strings.Join(names, "', '") + `')`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			out[k] = f
		}
	}
	return out, rows.Err()
}

func (e *mysqlEngine) statements(ctx context.Context, db *sql.DB) ([]stmtRow, error) {
	rows, err := db.QueryContext(ctx, `SELECT COALESCE(SCHEMA_NAME, ''), DIGEST, COUNT_STAR, SUM_TIMER_WAIT / 1000000000,
  MAX_TIMER_WAIT / 1000000000, SUM_ROWS_SENT, SUM_ROWS_EXAMINED, SUM_ERRORS
FROM performance_schema.events_statements_summary_by_digest WHERE DIGEST IS NOT NULL`)
	if err != nil {
		e.notes.set("statements", "Cannot read statement digests: GRANT SELECT ON performance_schema.* TO the agent's user ("+clipRunes(err.Error(), 160)+").")
		return nil, err
	}
	defer rows.Close()
	var out []stmtRow
	for rows.Next() {
		r := stmtRow{mysql: true}
		if err := rows.Scan(&r.db, &r.id, &r.c.calls, &r.c.totalMs, &r.c.maxMs, &r.c.rows, &r.c.examined, &r.c.errors); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		e.notes.set("statements", "No statement digests: turn performance_schema on (performance_schema=ON in the server's config; it is off by default on MariaDB).")
	} else {
		e.notes.clear("statements")
	}
	return out, nil
}

func (e *mysqlEngine) texts(ctx context.Context, db *sql.DB, ids []string) (map[string]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := db.QueryContext(ctx, `SELECT DIGEST, COALESCE(MAX(DIGEST_TEXT), '') FROM performance_schema.events_statements_summary_by_digest
WHERE DIGEST IN (?`+strings.Repeat(", ?", len(ids)-1)+`) GROUP BY DIGEST`, args...)
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
