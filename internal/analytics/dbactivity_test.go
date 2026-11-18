package analytics

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/styropyr0/visionary/internal/agentapi"
)

func TestAgentReportStoresDatabases(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	at := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339Nano) }
	tx := 42.0
	ex, er := int64(900), int64(0)
	rep := &agentapi.Report{Host: "db-box", At: at(0), Databases: []agentapi.Database{
		{
			Server: "main", Engine: "postgres", Version: "16.4", Notes: []string{"Other users' sessions are hidden: GRANT pg_monitor TO the agent's user."},
			Samples: []agentapi.DBSample{
				{At: at(-15 * time.Second), Values: map[string]float64{"connections": 10, "max_connections": 100, "waiting": 0}},
				{At: at(0), Values: map[string]float64{"connections": 12, "max_connections": 100, "active": 3, "waiting": 1, "longest_query_s": 75.25, "commits": 40, "deadlocks": -1}},
			},
			Sessions: []agentapi.DBSession{
				// An old agent, or a bug, must not get a literal stored.
				{PID: 101, User: "web", State: "active", Query: "UPDATE accounts SET balance = 10 WHERE email = 'a@b.c'", QueryStart: at(-75 * time.Second),
					ElapsedS: 75.25, TxAgeS: &tx, WaitEvent: "Lock: transactionid", BlockedBy: []int64{202}, LockType: "transactionid", LockMode: "ShareLock", SeenAt: at(0)},
				{PID: 202, User: "batch", State: "idle in transaction", Query: "SELECT 1", ElapsedS: 300, SeenAt: at(0)},
				{PID: 303, User: "web", State: "active", Query: "SELECT pg_sleep($1)", ElapsedS: 20, SeenAt: at(-15 * time.Second)},
				{PID: 0, State: "active", SeenAt: at(0)},
			},
			Statements: []agentapi.DBStatement{
				{At: at(0), IntervalS: 60, ID: "-7042", Database: "app", Query: "SELECT * FROM users WHERE id = $1", Calls: 120, TotalMs: 360, Rows: 120, MaxMs: 40},
				{At: at(0), IntervalS: 60, ID: "", Query: "x", Calls: 1},
				{At: at(0), IntervalS: 60, ID: "1", Query: "x", Calls: 0},
			},
		},
		{Server: "shop", Engine: "mysql", Statements: []agentapi.DBStatement{{At: at(0), ID: "abc", Query: "SELECT * FROM t WHERE name = \"bob\"", Calls: 3, TotalMs: 9, RowsExamined: &ex, Errors: &er}}},
		{Server: "bad name!", Engine: "postgres", Samples: []agentapi.DBSample{{At: at(0), Values: map[string]float64{"connections": 1}}}},
		{Server: "other", Engine: "oracle"},
	}}
	rows, n := hostRows(rep, now.Format(tsLayout))
	if n["db_samples"] != 2 || n["db_sessions"] != 3 || n["db_statements"] != 2 {
		t.Fatalf("counts: %v", n)
	}
	for _, tb := range rows.order {
		for _, st := range otlpInserts(tb, rows.tables[tb], otlpSQLMaxParams, false) {
			if err := s.db.Exec(ctx, st.SQL, st.args...); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.upsertDBServers(ctx, rep, now.Format(tsLayout)); err != nil {
		t.Fatal(err)
	}
	if err := s.upsertDBServers(ctx, rep, now.Add(time.Minute).Format(tsLayout)); err != nil {
		t.Fatal(err)
	}
	one := func(q string) []any {
		t.Helper()
		res, err := s.db.Query(ctx, q)
		if err != nil || len(res.Rows) != 1 {
			t.Fatalf("%s: %v %v", q, res, err)
		}
		return res.Rows[0]
	}
	if r := one(`SELECT connections, active, waiting, longest_query_s, commits, deadlocks FROM db_stats WHERE server = 'main' AND active IS NOT NULL`); toString(r[0]) != "12" || toString(r[3]) != "75.3" || r[5] != nil {
		t.Fatalf("stats %v", r)
	}
	if r := one(`SELECT query, blocked_by, lock_mode, tx_age_s FROM db_sessions WHERE pid = 101`); toString(r[0]) != "UPDATE accounts SET balance = ? WHERE email = ?" || toString(r[1]) != "202" || toString(r[3]) != "42" {
		t.Fatalf("session %v", r)
	}
	if r := one(`SELECT name, query, calls, total_ms, rows_examined FROM db_statements WHERE server = 'main'`); toString(r[0]) != "SELECT users" || toString(r[2]) != "120" || r[4] != nil {
		t.Fatalf("statement %v", r)
	}
	if r := one(`SELECT query, rows_examined FROM db_statements WHERE server = 'shop'`); toString(r[0]) != "SELECT * FROM t WHERE name = ?" || toString(r[1]) != "900" {
		t.Fatalf("mysql statement %v", r)
	}
	if r := one(`SELECT COUNT(*), MIN(first_seen) < MAX(last_seen) FROM db_servers WHERE host = 'db-box'`); toString(r[0]) != "2" {
		t.Fatalf("servers %v", r)
	}

	get := func(path string) map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", path, nil)
		switch {
		case strings.HasPrefix(path, "/api/databases/sessions"):
			s.handleDBSessions(w, r)
		case strings.HasPrefix(path, "/api/databases/statements"):
			s.handleDBStatements(w, r)
		case strings.HasPrefix(path, "/api/databases/panels/"):
			r.SetPathValue("panel", strings.TrimPrefix(strings.SplitN(path, "?", 2)[0], "/api/databases/panels/"))
			s.handleDBPanel(w, r)
		default:
			s.handleDatabases(w, r)
		}
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	rowsOf := func(m map[string]any) []any { return m["rows"].([]any) }

	// Live: the two sessions of the latest sample, not the one before.
	live := rowsOf(get("/api/databases/sessions?live=1&server=main"))
	if len(live) != 2 {
		t.Fatalf("live sessions %v", live)
	}
	hist := rowsOf(get("/api/databases/sessions?server=main"))
	if len(hist) != 3 {
		t.Fatalf("history %v", hist)
	}
	list := get("/api/databases")
	if len(rowsOf(list)) != 2 {
		t.Fatalf("servers %v", list)
	}
	st := get("/api/databases/statements?server=main")
	cols := st["columns"].([]any)
	srows := rowsOf(st)
	if len(srows) != 1 {
		t.Fatalf("statements %v", st)
	}
	row := srows[0].([]any)
	for i, c := range cols {
		switch c {
		case "mean_ms":
			if row[i].(float64) != 3 {
				t.Errorf("mean %v", row[i])
			}
		case "share":
			if row[i].(float64) != 100 {
				t.Errorf("share %v", row[i])
			}
		case "trend":
			sum := 0.0
			for _, v := range row[i].([]any) {
				sum += v.(float64)
			}
			if sum != 360 {
				t.Errorf("trend %v", row[i])
			}
		}
	}
	for _, p := range []string{"connections", "connections_percent", "longest", "waiting", "transactions", "statement_time", "statement_calls"} {
		get("/api/databases/panels/" + p + "?server=main")
		get("/api/databases/panels/" + p)
	}
	w := httptest.NewRecorder()
	s.handleDBSessions(w, httptest.NewRequest("GET", "/api/databases/sessions?server=x'%20OR%201=1", nil))
	if w.Code != 400 {
		t.Fatalf("unvalidated server: %d", w.Code)
	}
}

func TestDatabaseAlertMetrics(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	rep := &agentapi.Report{Host: "db-box", At: now.Format(time.RFC3339Nano), Databases: []agentapi.Database{
		{Server: "main", Engine: "postgres", Samples: []agentapi.DBSample{{At: now.Add(-time.Minute).Format(time.RFC3339Nano),
			Values: map[string]float64{"connections": 90, "max_connections": 100, "longest_query_s": 400, "waiting": 2}}}},
		{Server: "shop", Engine: "mysql", Samples: []agentapi.DBSample{{At: now.Add(-time.Minute).Format(time.RFC3339Nano),
			Values: map[string]float64{"connections": 5, "max_connections": 100, "longest_query_s": 3}}}},
	}}
	rows, _ := hostRows(rep, now.Format(tsLayout))
	for _, tb := range rows.order {
		for _, st := range otlpInserts(tb, rows.tables[tb], otlpSQLMaxParams, false) {
			if err := s.db.Exec(ctx, st.SQL, st.args...); err != nil {
				t.Fatal(err)
			}
		}
	}
	rule := alertRule{Spec: ruleSpec{Metric: "db_long_query", Op: ">", Threshold: 300, Window: "5m"}}
	obs, err := s.observe(ctx, rule, now)
	if err != nil || len(obs) != 2 {
		t.Fatalf("%v %v", obs, err)
	}
	for _, o := range obs {
		if (o.Key == "main") != o.Breach {
			t.Errorf("%s breach %v: %s", o.Key, o.Breach, o.Message)
		}
	}
	rule.Spec.Metric, rule.Spec.Threshold, rule.Spec.Scope = "db_connections", 85, alertScope{DBServer: "main"}
	obs, err = s.observe(ctx, rule, now)
	if err != nil || len(obs) != 1 || !obs[0].Breach || !strings.Contains(obs[0].Message, "main") {
		t.Fatalf("%+v %v", obs, err)
	}
	if err := checkScope(alertScope{DBServer: "x' --"}); err == nil {
		t.Fatal("bad server name accepted")
	}
}

func TestAgentReportEndpointStoresDatabases(t *testing.T) {
	s := logsTestServer(t, nil)
	now := time.Now().UTC()
	rep := agentapi.Report{Host: "db-box", At: now.Format(time.RFC3339Nano), ReportS: 60, Databases: []agentapi.Database{{
		Server: "main", Engine: "postgres", Version: "16.4",
		Samples:  []agentapi.DBSample{{At: now.Format(time.RFC3339Nano), Values: map[string]float64{"connections": 3, "max_connections": 100}}},
		Sessions: []agentapi.DBSession{{PID: 7, State: "active", Query: "SELECT * FROM t WHERE token = 'secret'", ElapsedS: 20, SeenAt: now.Format(time.RFC3339Nano)}},
	}}}
	body, _ := json.Marshal(rep)
	r := httptest.NewRequest("POST", agentapi.Path, strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer k1")
	w := httptest.NewRecorder()
	s.handleAgentReport(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"db_sessions":1`) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	res, err := s.db.Query(context.Background(), `SELECT s.query, v.version FROM db_sessions s JOIN db_servers v ON v.host = s.host AND v.server = s.server`)
	if err != nil || len(res.Rows) != 1 || strings.Contains(toString(res.Rows[0][0]), "secret") || toString(res.Rows[0][1]) != "16.4" {
		t.Fatalf("%v %v", res, err)
	}
}
