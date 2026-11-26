package analytics

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIPActivity(t *testing.T) {
	s := securityTestServer(t, nil)
	ctx := context.Background()
	now := time.Now().UTC()
	at := now.Add(-30 * time.Minute)
	addReqs(t, s, req{at: at, path: "/.env", status: 200, ip: "203.0.113.5"}, req{at: at, path: "/api/orders", status: 401, ip: "203.0.113.5"})
	exec := func(q string, args ...any) {
		t.Helper()
		if err := s.db.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO security_events (created_at, source, kind, severity, host, actor, ip, count) VALUES (?, 'host', 'ssh_login', 'info', 'web-1', 'deploy', '203.0.113.5', 1)`, at.Format(tsLayout))
	exec(`INSERT INTO security_events (created_at, source, kind, severity, host, actor, target, count) VALUES (?, 'host', 'sudo', 'info', 'web-1', 'deploy', 'cat /etc/shadow', 1)`, at.Add(time.Minute).Format(tsLayout))
	exec(`INSERT INTO security_events (created_at, source, kind, severity, host, actor, target, count) VALUES (?, 'host', 'sudo', 'info', 'web-2', 'deploy', 'ls', 1)`, at.Add(time.Minute).Format(tsLayout))
	exec(`INSERT INTO app_logs (created_at, source, level, severity, message, request_id) VALUES (?, 'app', 'error', 17, 'secret file served', 'r0')`, at.Format(tsLayout))

	r := httptest.NewRequest("GET", "/api/security/ip/203.0.113.5/activity?from="+now.Add(-time.Hour).Format(time.RFC3339)+"&to="+now.Format(time.RFC3339), nil)
	r.SetPathValue("ip", "203.0.113.5")
	w := httptest.NewRecorder()
	s.handleIPActivity(w, r)
	var out struct {
		Items   []activityItem `json:"items"`
		Verdict map[string]any `json:"verdict"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	got := map[string]int{}
	for _, it := range out.Items {
		got[it.Type+":"+it.Kind]++
		if it.Kind == "sudo" && (it.Host != "web-1" || it.Via == "") {
			t.Fatalf("sudo on the wrong host or without a reason: %+v", it)
		}
	}
	if got["request:"] != 2 || got["host:ssh_login"] != 1 || got["host:sudo"] != 1 || got["log:"] != 1 {
		t.Fatalf("%v", got)
	}
	if out.Verdict["outcome"] != "succeeded" || out.Verdict["probes_answered"] != float64(1) {
		t.Fatalf("%v", out.Verdict)
	}
	if !isProbePath("/.ENV") || !isProbePath("/a/../b") || isProbePath("/api/orders") || !isProbePath("/.DS_Store") {
		t.Fatal("isProbePath")
	}
}
