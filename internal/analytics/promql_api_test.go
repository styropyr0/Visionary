package analytics

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPromHTTPAPI(t *testing.T) {
	s := metricsTestServer(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Minute)
	for i := 1; i <= 8; i++ {
		at := now.Add(-time.Duration(i*15) * time.Second).Add(time.Second)
		err := s.db.Exec(ctx, `INSERT INTO metrics (created_at, service, instance, name, kind, unit, labels, series, value) VALUES (?, 'shop', 'web-1', 'orders_placed', 'counter', NULL, '{"region":"eu"}', 'da4f5c6244c4f3e4', 2)`, at.Format(tsLayout))
		if err != nil {
			t.Fatal(err)
		}
	}
	secret := "vis_testkey0123456789abcdef"
	if err := s.db.Exec(ctx, `INSERT INTO visionary_api_keys (name, scope, prefix, key_hash, created_by, created_at) VALUES ('grafana', 'read', 'vis_test', ?, 'test', ?)`,
		hashKey(secret), now.Format(tsLayout)); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	call := func(method, path string, form url.Values, auth func(*http.Request)) (int, map[string]any) {
		t.Helper()
		var body io.Reader
		if method == "POST" {
			body = strings.NewReader(form.Encode())
		} else if form != nil {
			path += "?" + form.Encode()
		}
		r := httptest.NewRequest(method, path, body)
		if method == "POST" {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if auth != nil {
			auth(r)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	basic := func(r *http.Request) { r.SetBasicAuth("grafana", secret) }
	bearer := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+secret) }
	end := float64(now.Unix())
	code, out := call("POST", "/api/prom/api/v1/query_range", url.Values{
		"query": {`sum(rate(orders_placed{region="eu"}[1m]))`}, "start": {strconv.FormatFloat(end-300, 'f', 3, 64)},
		"end": {strconv.FormatFloat(end, 'f', 3, 64)}, "step": {"60"},
	}, basic)
	if code != 200 || out["status"] != "success" {
		t.Fatalf("query_range: %d %v", code, out)
	}
	data := out["data"].(map[string]any)
	result := data["result"].([]any)
	if data["resultType"] != "matrix" || len(result) != 1 {
		t.Fatalf("query_range data: %v", data)
	}
	values := result[0].(map[string]any)["values"].([]any)
	last := values[len(values)-1].([]any)
	if last[0].(float64) != end || last[1] != "0.13333333333333333" {
		t.Errorf("last point %v, want [%v 8/60]", last, end)
	}

	code, out = call("GET", "/api/v1/query", url.Values{"query": {`orders_placed`}, "time": {now.Format(time.RFC3339)}},
		bearer)
	if code != 200 || out["data"].(map[string]any)["resultType"] != "vector" {
		t.Fatalf("query: %d %v", code, out)
	}
	code, out = call("GET", "/api/prom/api/v1/query", url.Values{"query": {`1+1`}}, basic)
	if code != 200 || out["data"].(map[string]any)["result"].([]any)[1] != "2" {
		t.Fatalf("1+1: %d %v", code, out)
	}
	code, out = call("POST", "/api/prom/api/v1/query", url.Values{"query": {`sum(orders_placed`}}, basic)
	if code != 400 || out["errorType"] != "bad_data" || !strings.Contains(out["error"].(string), "character 18") {
		t.Fatalf("parse error: %d %v", code, out)
	}
	code, out = call("GET", "/api/prom/api/v1/label/__name__/values", nil, basic)
	if code != 200 || !slices.Contains(out["data"].([]any), any("orders_placed")) {
		t.Fatalf("names: %d %v", code, out)
	}
	code, out = call("POST", "/api/prom/api/v1/series", url.Values{"match[]": {`orders_placed`}, "start": {strconv.FormatFloat(end-3600, 'f', 0, 64)}, "end": {strconv.FormatFloat(end, 'f', 0, 64)}}, basic)
	if code != 200 || len(out["data"].([]any)) != 1 || out["data"].([]any)[0].(map[string]any)["region"] != "eu" {
		t.Fatalf("series: %d %v", code, out)
	}
	code, out = call("GET", "/api/prom/api/v1/labels", url.Values{"match[]": {`orders_placed`}}, basic)
	if code != 200 || !slices.Contains(out["data"].([]any), any("region")) || !slices.Contains(out["data"].([]any), any("service")) {
		t.Fatalf("labels: %d %v", code, out)
	}
	code, out = call("GET", "/api/prom/api/v1/label/region/values", url.Values{"match[]": {`orders_placed`}}, basic)
	if code != 200 || len(out["data"].([]any)) != 1 {
		t.Fatalf("label values: %d %v", code, out)
	}
	code, out = call("GET", "/api/prom/api/v1/metadata", nil, basic)
	if code != 200 || out["data"].(map[string]any)["orders_placed"] == nil {
		t.Fatalf("metadata: %d %v", code, out)
	}
	code, out = call("GET", "/api/prom/api/v1/status/buildinfo", nil, basic)
	if code != 200 || out["data"].(map[string]any)["version"] == "" {
		t.Fatalf("buildinfo: %d %v", code, out)
	}
	code, _ = call("GET", "/api/prom/api/v1/query", url.Values{"query": {`1`}}, func(r *http.Request) { r.SetBasicAuth("x", "vis_wrong") })
	if code != http.StatusUnauthorized {
		t.Fatalf("bad key: %d", code)
	}
	code, _ = call("POST", "/api/v1/write", nil, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("remote_write: %d", code)
	}
	code, out = call("GET", "/api/metrics/promql", url.Values{"query": {`rate(orders_placed)`}}, bearer)
	if code != 400 || out["position"] != float64(5) {
		t.Fatalf("chart error: %d %v", code, out)
	}
	code, out = call("GET", "/api/metrics/promql", url.Values{"query": {`sum(increase(orders_placed[5m]))`},
		"from": {now.Add(-time.Hour).Format(time.RFC3339)}, "to": {now.Format(time.RFC3339)}}, bearer)
	if code != 200 || len(out["rows"].([]any)) == 0 {
		t.Fatalf("chart: %d %v", code, out)
	}
}
