package analytics

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"
)

// Sorted pages cover the whole range once each, in order, ties included
func TestLogsSorted(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Add(-time.Minute)
	const n = 230
	for i := range n {
		if err := s.db.Exec(ctx, `INSERT INTO api_logs (request_id, created_at, method, path, route, status, duration_ms, request_size, response_size) VALUES (?, ?, 'GET', '/x', '/x', 200, ?, ?, ?)`,
			"r"+strconv.Itoa(i), now.Add(time.Duration(i)*time.Millisecond).Format(tsLayout), float64(i%7)+0.5, i%5, (i*37)%11); err != nil {
			t.Fatal(err)
		}
	}
	rng := url.Values{"from": {now.Add(-time.Hour).Format(time.RFC3339)}, "to": {now.Add(time.Hour).Format(time.RFC3339)}}
	page := func(q url.Values) (Result, int) {
		t.Helper()
		w := httptest.NewRecorder()
		s.handleLogs(w, httptest.NewRequest("GET", "/api/logs?"+q.Encode(), nil))
		var res Result
		if w.Code == 200 {
			if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
				t.Fatal(err)
			}
		}
		return res, w.Code
	}

	for _, c := range []struct{ sort, dir, col string }{
		{"duration", "desc", "duration_ms"},
		{"size", "asc", "response_size"},
		{"request_size", "desc", "request_size"},
		{"", "", "id"},
	} {
		q := url.Values{"from": rng["from"], "to": rng["to"], "sort": {c.sort}, "dir": {c.dir}}
		seen := map[float64]bool{}
		var last []any
		for pages := 0; ; pages++ {
			if pages > 5 {
				t.Fatalf("%s: too many pages", c.sort)
			}
			res, code := page(q)
			if code != 200 {
				t.Fatalf("%s: status %d", c.sort, code)
			}
			vi, ii := -1, -1
			for i, name := range res.Columns {
				if name == c.col {
					vi = i
				}
				if name == "id" {
					ii = i
				}
			}
			for _, row := range res.Rows {
				id := row[ii].(float64)
				if seen[id] {
					t.Fatalf("%s: id %v twice", c.sort, id)
				}
				seen[id] = true
				if last != nil {
					a, b := last[vi].(float64), row[vi].(float64)
					if c.dir == "asc" && (a > b || a == b && last[ii].(float64) > id) || c.dir != "asc" && (a < b || a == b && last[ii].(float64) < id) {
						t.Fatalf("%s %s: %v then %v out of order", c.sort, c.dir, last, row)
					}
				}
				last = row
			}
			if !res.Truncated {
				break
			}
			q.Set("before", strconv.FormatFloat(last[ii].(float64), 'f', -1, 64))
			q.Set("before_value", strconv.FormatFloat(last[vi].(float64), 'f', -1, 64))
		}
		if len(seen) != n {
			t.Fatalf("%s: %d rows across pages, want %d", c.sort, len(seen), n)
		}
	}

	for _, bad := range []url.Values{{"sort": {"path; DROP TABLE api_logs"}}, {"dir": {"sideways"}}, {"sort": {"duration"}, "before": {"5"}}} {
		bad.Set("from", rng.Get("from"))
		bad.Set("to", rng.Get("to"))
		if _, code := page(bad); code != 400 {
			t.Fatalf("%v: status %d, want 400", bad, code)
		}
	}
}
