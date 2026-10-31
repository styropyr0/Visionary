package analytics

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

// A server on a database whose api_logs and spans predate the trace
// columns, with one trace: shop calls stock over HTTP, stock queries its
// database, and shop also calls Stripe.
func traceTestServer(t *testing.T) (*Server, time.Time) {
	t.Helper()
	s := newTestServer(t, "traces.db", nil)
	t.Cleanup(func() { _ = s.db.Close() })
	ctx := context.Background()
	old := `CREATE TABLE api_logs (id INTEGER PRIMARY KEY AUTOINCREMENT, request_id TEXT NOT NULL, created_at TEXT NOT NULL,
    method TEXT NOT NULL, path TEXT NOT NULL, route TEXT, query TEXT, status INTEGER NOT NULL, duration_ms REAL NOT NULL,
    ip TEXT, user_agent TEXT, user_id INTEGER, user_handle TEXT, user_role TEXT, request_headers TEXT,
    request_content_type TEXT, request_body TEXT, request_body_truncated INTEGER NOT NULL DEFAULT 0,
    request_size INTEGER NOT NULL DEFAULT 0, response_content_type TEXT, response_body TEXT,
    response_body_truncated INTEGER NOT NULL DEFAULT 0, response_size INTEGER NOT NULL DEFAULT 0, error_code TEXT, error_key TEXT);
CREATE TABLE spans (id INTEGER PRIMARY KEY AUTOINCREMENT, created_at TEXT NOT NULL, request_id TEXT, service TEXT,
    instance TEXT NOT NULL, kind TEXT NOT NULL, name TEXT NOT NULL, target TEXT, detail TEXT, offset_ms REAL,
    duration_ms REAL NOT NULL, status TEXT NOT NULL, error TEXT, row_count INTEGER)`
	if onSharedTestDatabase() {
		old = ""
	}
	if err := s.db.ExecScript(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}

	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	ts := func(ms int) string { return at.Add(time.Duration(ms) * time.Millisecond).Format(tsLayout) }
	const tid = "4bf92f3577b34da6a3ce929d0e0e4736"
	exec := func(q string, args ...any) {
		t.Helper()
		if err := s.db.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	req := `INSERT INTO api_logs (request_id, created_at, method, path, route, status, duration_ms, service, trace_id, span_id, parent_span_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	span := `INSERT INTO spans (created_at, request_id, service, instance, kind, name, target, offset_ms, duration_ms, status, trace_id, span_id, parent_span_id)
VALUES (?, ?, ?, 'i', ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	exec(req, "r1", ts(0), "POST", "/checkout", "/checkout", 200, 120.0, "shop", tid, "a000000000000001", nil)
	exec(span, ts(10), "r1", "shop", "http", "GET stock:8080", "stock:8080", 10.0, 60.0, "ok", tid, "a000000000000002", "a000000000000001")
	exec(span, ts(80), "r1", "shop", "http", "POST api.stripe.com", "api.stripe.com", 80.0, 30.0, "error", tid, "a000000000000003", "a000000000000001")
	exec(req, "r2", ts(15), "GET", "/stock/1", "/stock/{id}", 503, 50.0, "stock", tid, "b000000000000001", "a000000000000002")
	exec(span, ts(20), "r2", "stock", "db", "SELECT items", "postgresql/stock", 5.0, 20.0, "ok", tid, "b000000000000002", "b000000000000001")
	exec(`INSERT INTO errors (created_at, service, instance, request_id, type, message, fingerprint, handled) VALUES (?, 'stock', 'i', 'r2', 'Timeout', 'db slow', 'fp1', 0)`, ts(60))
	exec(`INSERT INTO api_logs (request_id, created_at, method, path, status, duration_ms) VALUES ('old', ?, 'GET', '/', 200, 5)`, ts(1))
	return s, at
}

func TestTraceViews(t *testing.T) {
	s, at := traceTestServer(t)
	rng := "?from=" + at.Add(-time.Hour).Format(time.RFC3339) + "&to=" + at.Add(time.Hour).Format(time.RFC3339)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/traces/4bf92f3577b34da6a3ce929d0e0e4736", nil)
	r.SetPathValue("trace_id", "4bf92f3577b34da6a3ce929d0e0e4736")
	s.handleTrace(w, r)
	var tr struct {
		Requests, Spans, Errors Result
	}
	if err := json.Unmarshal(w.Body.Bytes(), &tr); err != nil || w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if len(tr.Requests.Rows) != 2 || len(tr.Spans.Rows) != 3 || len(tr.Errors.Rows) != 1 {
		t.Fatalf("trace: %d requests, %d spans, %d errors", len(tr.Requests.Rows), len(tr.Spans.Rows), len(tr.Errors.Rows))
	}

	w = httptest.NewRecorder()
	s.handleTraceSearch(w, httptest.NewRequest("GET", "/api/traces"+rng+"&service=stock&errors=1", nil))
	var found Result
	if err := json.Unmarshal(w.Body.Bytes(), &found); err != nil || w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if len(found.Rows) != 1 {
		t.Fatalf("search: %v", found.Rows)
	}
	row := map[string]any{}
	for i, c := range found.Columns {
		row[c] = found.Rows[0][i]
	}
	if row["route"] != "/checkout" || row["service"] != "shop" || toInt(row["requests"]) != 2 || toInt(row["spans"]) != 3 || toInt(row["errors"]) != 2 || toFloat(row["duration_ms"]) != 120 {
		t.Fatalf("summary %v", row)
	}
	if svcs, _ := row["services"].([]any); len(svcs) != 2 {
		t.Fatalf("services %v", row["services"])
	}
	w = httptest.NewRecorder()
	s.handleTraceSearch(w, httptest.NewRequest("GET", "/api/traces"+rng+"&min_ms=500", nil))
	if err := json.Unmarshal(w.Body.Bytes(), &found); err != nil || len(found.Rows) != 0 {
		t.Fatalf("min_ms: %s", w.Body)
	}

	w = httptest.NewRecorder()
	s.handleServiceMap(w, httptest.NewRequest("GET", "/api/traces/map"+rng, nil))
	var m struct {
		Nodes []mapNode
		Edges []mapEdge
	}
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil || w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	edges := map[string]mapEdge{}
	for _, e := range m.Edges {
		edges[e.Source+">"+e.Target] = e
	}
	if e := edges["service:shop>service:stock"]; e.Calls != 1 || e.Errors != 1 || e.P95 == nil || *e.P95 != 60 {
		t.Fatalf("shop>stock %+v in %v", e, m.Edges)
	}
	if e := edges["service:shop>http:api.stripe.com"]; e.Calls != 1 || e.ErrorRate != 100 {
		t.Fatalf("stripe %+v", e)
	}
	if e := edges["service:stock>db:postgresql/stock"]; e.Calls != 1 {
		t.Fatalf("db %+v", e)
	}
	if _, ok := edges["service:shop>http:stock:8080"]; ok || len(m.Edges) != 3 {
		t.Fatalf("a call a traced service answered is not external: %v", m.Edges)
	}
}
