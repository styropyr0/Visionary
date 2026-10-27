package analytics

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// A route's detail counts only its own requests, and picks an example of
// them with recorded calls.
func TestRouteDetail(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	if err := s.db.ExecScript(ctx, `CREATE TABLE IF NOT EXISTS spans (
    id INTEGER PRIMARY KEY AUTOINCREMENT, created_at TEXT NOT NULL, request_id TEXT, service TEXT, instance TEXT NOT NULL,
    kind TEXT NOT NULL, name TEXT NOT NULL, target TEXT, detail TEXT, offset_ms REAL, duration_ms REAL NOT NULL,
    status TEXT NOT NULL, error TEXT, row_count INTEGER)`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Minute)
	ts := func(d time.Duration) string { return now.Add(d).Format(tsLayout) }
	for i, req := range []struct{ id, route string }{{"a", "/pay"}, {"b", "/pay"}, {"c", "/other"}} {
		if err := s.db.Exec(ctx, `INSERT INTO api_logs (request_id, created_at, method, path, route, status, duration_ms) VALUES (?, ?, 'GET', ?, ?, 200, 40)`,
			req.id, ts(time.Duration(i)*time.Millisecond), req.route, req.route); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if err := s.db.Exec(ctx, `INSERT INTO spans (created_at, request_id, instance, kind, name, target, detail, duration_ms, status) VALUES (?, ?, 'i', 'db', 'SELECT users', 'postgres', 'SELECT * FROM users WHERE id = $1', 5, 'ok')`,
				ts(time.Duration(i)*time.Millisecond), req.id); err != nil {
				t.Fatal(err)
			}
		}
	}

	q := url.Values{"method": {"GET"}, "route": {"/pay"}, "from": {now.Add(-time.Hour).Format(time.RFC3339)}, "to": {now.Add(time.Hour).Format(time.RFC3339)}}
	w := httptest.NewRecorder()
	s.handleRouteDetail(w, httptest.NewRequest("GET", "/api/telemetry/routes/detail?"+q.Encode(), nil))
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var out struct {
		Summary  *Result
		Examples []routeExample
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if got := rowMaps(out.Summary)[0]["requests"]; toInt(got) != 2 {
		t.Errorf("requests = %v, want 2", got)
	}
	// Both requests take 40 ms, so one example stands for typical, slow and slowest.
	if len(out.Examples) != 1 || toString(out.Examples[0].Log["request_id"]) == "c" {
		t.Errorf("examples = %+v, want one request of /pay", out.Examples)
	}
}
