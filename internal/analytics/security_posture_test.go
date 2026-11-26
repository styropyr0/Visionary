package analytics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPosture(t *testing.T) {
	s := securityTestServer(t, nil)
	ctx := context.Background()
	at := time.Now().UTC().Add(-time.Hour)
	addReqs(t, s,
		req{at: at, path: "/.env", status: 200, ip: "203.0.113.1"},
		req{at: at, path: "/.env", status: 404, ip: "203.0.113.2"},
		req{at: at, path: "/debug/pprof/heap", status: 200, ip: "203.0.113.3"},
	)
	for i := 0; i < 3; i++ {
		addReqs(t, s, req{at: at, method: "POST", path: "/admin/users", route: "/admin/users", status: 201, ip: "203.0.113.4"})
	}
	if err := s.db.Exec(ctx, `INSERT INTO api_logs (request_id, created_at, method, path, status, duration_ms, user_id, service) VALUES ('u1', ?, 'GET', '/me', 200, 1, '7', 'shop')`, at.Format(tsLayout)); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Exec(ctx, `INSERT INTO security_events (created_at, source, kind, severity, host, target, count) VALUES (?, 'host', 'container_privileged', 'critical', 'web-1', 'redis', 1)`, at.Format(tsLayout)); err != nil {
		t.Fatal(err)
	}
	items, err := s.posture(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]postureItem{}
	for _, it := range items {
		got[it.Kind] = it
	}
	if got["exposed_path"].Target != "/.env" || got["debug_endpoint"].Target == "" || got["anonymous_change"].Target != "POST /admin/users" || got["container_privileged"].Severity != "critical" {
		t.Fatalf("%+v", items)
	}
	if items[0].Severity != "critical" {
		t.Fatal("critical first")
	}

	osv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/querybatch":
			var body struct {
				Queries []map[string]any `json:"queries"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			var results []map[string]any
			for _, q := range body.Queries {
				if q["version"] == "1.0.0" {
					results = append(results, map[string]any{"vulns": []map[string]string{{"id": "GHSA-x"}}})
				} else {
					results = append(results, map[string]any{})
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
		case strings.HasPrefix(r.URL.Path, "/vulns/"):
			_, _ = w.Write([]byte(`{"id":"GHSA-x","summary":"RCE","aliases":["CVE-2026-1"],"database_specific":{"severity":"HIGH"},
"affected":[{"package":{"ecosystem":"npm","name":"left"},"ranges":[{"events":[{"introduced":"0"},{"fixed":"1.0.3"},{"fixed":"1.2.0"}]}]}]}`))
		}
	}))
	defer osv.Close()
	pkgs := []osvPackage{{"npm", "left", "1.0.0"}, {"npm", "right", "2.0.0"}}
	vulns, err := osvLookup(ctx, osv.Client(), osv.URL, pkgs, map[osvPackage][]string{pkgs[0]: {"shop"}})
	if err != nil || len(vulns) != 1 {
		t.Fatalf("%v %+v", err, vulns)
	}
	if v := vulns[0]; v.Severity != "critical" || v.Detail["fixed"] != "1.0.3" || !strings.Contains(v.Fix, "1.0.3") {
		t.Fatalf("%+v", v)
	}
	if err := s.replaceScans(ctx, []string{"vulnerable_package"}, vulns); err != nil {
		t.Fatal(err)
	}
	if items, _ := s.posture(ctx); items[0].Kind != "vulnerable_package" && items[1].Kind != "vulnerable_package" {
		t.Fatalf("scans not read back: %+v", items)
	}

	resp := &http.Response{Header: http.Header{"Content-Type": {"text/html"}}}
	if hs := headerItems("https://shop.example", resp); len(hs) != 1 || hs[0].Severity != "warning" || len(hs[0].Detail["missing"].([]string)) != 4 {
		t.Fatalf("%+v", hs)
	}
	if hs := headerItems("http://shop.example", resp); hs[0].Kind != "no_tls" {
		t.Fatal("plain http")
	}
	if !versionLess("1.2.9", "1.10.0") || versionLess("2.0.0", "1.9.9") || !versionLess("v1.0.0", "1.0.1") {
		t.Fatal("versionLess")
	}
}
