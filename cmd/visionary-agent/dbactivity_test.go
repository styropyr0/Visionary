package main

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/styropyr0/visionary/internal/agentapi"
)

func TestParseDatabases(t *testing.T) {
	ts, err := parseDatabases("postgres://mon:s3cret@db1:5432/app?sslmode=disable&name=main,mysql://mon:pw@db2/shop postgresql://u@h1,h2/x")
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 3 {
		t.Fatalf("got %d targets", len(ts))
	}
	pg := ts[0]
	if pg.name != "main" || pg.engine != "postgres" || pg.secret != "s3cret" || strings.Contains(pg.dsn, "name=main") ||
		!strings.Contains(pg.dsn, "default_transaction_read_only=on") || !strings.Contains(pg.dsn, "sslmode=disable") {
		t.Fatalf("postgres target %+v", pg)
	}
	my := ts[1]
	if my.name != "db2/shop" || my.engine != "mysql" || !strings.Contains(my.dsn, "tcp(db2:3306)/shop") {
		t.Fatalf("mysql target %+v", my)
	}
	if ts[2].name != "h1-h2/x" {
		t.Fatalf("multi-host name %q", ts[2].name)
	}
	if _, err := parseDatabases("postgres://a@x/db postgres://b@x/db"); err == nil {
		t.Fatal("duplicate names accepted")
	}
	if _, err := parseDatabases("redis://x"); err == nil || strings.Contains(err.Error(), "pw") {
		t.Fatalf("bad scheme: %v", err)
	}
	if _, err := parseDatabases("ftp://u:topsecret@x/"); err == nil || strings.Contains(err.Error(), "topsecret") {
		t.Fatalf("password in error: %v", err)
	}
	if ts, _ := parseDatabases(""); len(ts) != 0 {
		t.Fatal("empty list")
	}
}

func TestCounterDeltas(t *testing.T) {
	got := counterDeltas(map[string]float64{"commits": 100, "deadlocks": 5}, map[string]float64{"commits": 130, "deadlocks": 2, "rollbacks": 9})
	if got["commits"] != 30 || len(got) != 1 {
		t.Fatalf("%v", got)
	}
	if len(counterDeltas(nil, map[string]float64{"commits": 1})) != 0 {
		t.Fatal("first sample has deltas")
	}
}

func TestStmtTracker(t *testing.T) {
	tr := newStmtTracker()
	t0 := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	row := func(id string, calls, ms float64) stmtRow {
		return stmtRow{id: id, db: "app", user: "web", c: stmtCounters{calls: calls, totalMs: ms, rows: calls, maxMs: 9}}
	}
	if d, _ := tr.update([]stmtRow{row("1", 10, 100), row("2", 5, 50)}, t0, 10); len(d) != 0 {
		t.Fatalf("baseline returned %v", d)
	}
	// 1 ran 5 more times, 2 did nothing, 3 is new, and PostgreSQL's nested
	// row of 1 adds to it.
	d, iv := tr.update([]stmtRow{row("1", 14, 180), row("2", 5, 50), row("3", 2, 400), row("1", 1, 20)}, t0.Add(time.Minute), 10)
	if iv != time.Minute || len(d) != 2 {
		t.Fatalf("interval %v deltas %+v", iv, d)
	}
	if d[0].id != "3" || d[0].c.calls != 2 || d[0].c.totalMs != 400 {
		t.Fatalf("new statement %+v", d[0])
	}
	if d[1].id != "1" || d[1].c.calls != 5 || d[1].c.totalMs != 100 || d[1].c.rows != 5 {
		t.Fatalf("delta %+v", d[1])
	}
	// A reset (counters went down) counts from zero; 3 was evicted.
	d, _ = tr.update([]stmtRow{row("1", 3, 30), row("2", 6, 60)}, t0.Add(2*time.Minute), 1)
	if len(d) != 1 || d[0].id != "1" || d[0].c.calls != 3 {
		t.Fatalf("after reset %+v", d)
	}
	if _, ok := tr.prev["3|app|web"]; ok {
		t.Fatal("evicted statement kept")
	}
}

func TestMergeSessions(t *testing.T) {
	seen := map[string]*agentapi.DBSession{}
	s1 := agentapi.DBSession{PID: 7, State: "active", Query: "SELECT ?", QueryStart: "2026-11-18T10:00:00Z", ElapsedS: 15}
	mergeSessions(seen, []agentapi.DBSession{s1}, "a")
	s1.ElapsedS = 30
	mergeSessions(seen, []agentapi.DBSession{s1}, "b")
	if len(seen) != 1 || seen["7|active|SELECT ?"].ElapsedS != 30 || seen["7|active|SELECT ?"].SeenAt != "b" {
		t.Fatalf("same execution: %+v", seen)
	}
	// The same text run again later is another execution; MySQL's start
	// moving by a second is not.
	s2 := s1
	s2.QueryStart, s2.ElapsedS = "2026-11-18T10:01:00Z", 2
	mergeSessions(seen, []agentapi.DBSession{s2}, "c")
	s3 := s2
	s3.QueryStart, s3.ElapsedS = "2026-11-18T10:01:01Z", 16
	mergeSessions(seen, []agentapi.DBSession{s3, {PID: 9, State: "idle in transaction", ElapsedS: 99}}, "d")
	if len(seen) != 3 {
		t.Fatalf("executions: %d", len(seen))
	}
	out := reportSessions(seen, "d", 2)
	if len(out) != 2 || out[0].PID != 9 || out[1].ElapsedS != 16 {
		t.Fatalf("report %+v", out)
	}
}

func TestMySQLSummary(t *testing.T) {
	now := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	tx := func(n int64) sql.NullInt64 { return sql.NullInt64{Int64: n, Valid: true} }
	procs := []mysqlProc{
		{id: 1, user: "event_scheduler", command: "Daemon", time: 9999},
		{id: 2, user: "web", host: "10.0.0.5:53422", db: "shop", command: "Sleep", time: 400},
		{id: 3, user: "web", host: "10.0.0.6:1000", db: "shop", command: "Sleep", time: 120, trxAge: tx(130)},
		{id: 4, user: "web", host: "10.0.0.7:1000", db: "shop", command: "Query", state: "updating", info: "UPDATE t SET a = 1", time: 30, trxAge: tx(31), trxState: "LOCK WAIT"},
		{id: 5, user: "batch", host: "localhost", command: "Query", state: "Waiting for table metadata lock", info: "ALTER TABLE t ADD c INT", time: 5},
	}
	v, ss := mysqlSummary(procs, now)
	if v["active"] != 2 || v["idle_in_tx"] != 1 || v["waiting"] != 2 || v["longest_query_s"] != 30 || v["longest_tx_s"] != 130 || v["longest_idle_tx_s"] != 120 {
		t.Fatalf("values %v", v)
	}
	if len(ss) != 3 || ss[0].PID != 3 || ss[0].State != "idle in transaction" || ss[0].Client != "10.0.0.6" || ss[0].QueryStart != "" {
		t.Fatalf("sessions %+v", ss)
	}
	if ss[1].PID != 4 || ss[1].QueryStart != "2026-11-18T09:59:30Z" || *ss[1].TxAgeS != 31 {
		t.Fatalf("running %+v", ss[1])
	}
	if !mysqlMetadataWaits(ss) {
		t.Fatal("metadata wait not seen")
	}
	applyLockWaits(ss, []lockWait{{waiting: 4, blocking: 3, object: "`shop`.`t`", lockType: "RECORD", lockMod: "X"}, {waiting: 4, blocking: 3}, {waiting: 5, blocking: 4, object: "shop.t", lockType: "metadata", lockMod: "EXCLUSIVE"}})
	if len(ss[1].BlockedBy) != 1 || ss[1].BlockedBy[0] != 3 || ss[1].LockObject != "shop.t" || ss[1].LockType != "record" {
		t.Fatalf("lock wait %+v", ss[1])
	}
	if ss[2].BlockedBy[0] != 4 || ss[2].LockMode != "EXCLUSIVE" {
		t.Fatalf("metadata wait %+v", ss[2])
	}
}

func TestSmallParsers(t *testing.T) {
	if got := parsePIDs("12, 7,,x"); len(got) != 2 || got[0] != 12 || got[1] != 7 {
		t.Fatalf("pids %v", got)
	}
	for in, want := range map[string]string{"10.0.0.5:53422": "10.0.0.5", "localhost": "localhost", "[::1]:3306": "::1", "host:abc": "host:abc"} {
		if got := stripPort(in); got != want {
			t.Errorf("stripPort(%q) = %q", in, got)
		}
	}
	if got := splitDSNs("mysql://a@h/x,mysql://b@h/y\npostgres://c@h1,h2/z,"); len(got) != 3 || got[2] != "postgres://c@h1,h2/z" {
		t.Fatalf("split %q", got)
	}
}
