package analytics

import (
	"context"
	"testing"
	"time"
)

func TestIncidents(t *testing.T) {
	s := securityTestServer(t, nil)
	ctx := context.Background()
	now := time.Now().UTC()
	at := now.Add(-40 * time.Minute)
	ev := func(at time.Time, kind, sev, ip, actor, host string) {
		t.Helper()
		if err := s.db.Exec(ctx, `INSERT INTO security_events (created_at, source, kind, severity, ip, actor, host, count) VALUES (?, 'api', ?, ?, ?, ?, ?, 1)`,
			at.Format(tsLayout), kind, sev, nullable(ip), nullable(actor), nullable(host)); err != nil {
			t.Fatal(err)
		}
	}
	ev(at, "auth_bruteforce", "critical", "203.0.113.1", "", "")
	ev(at.Add(10*time.Minute), "credential_stuffing", "warning", "203.0.113.1", "", "")
	addReqs(t, s, req{at: at.Add(15 * time.Minute), method: "POST", path: "/api/login", status: 200, ip: "203.0.113.1", login: "ada@x.io"})
	ev(at, "scanner", "warning", "198.51.100.9", "", "")
	ev(at, "scanner", "warning", "198.51.100.10", "", "")
	addReqs(t, s, req{at: at, path: "/.git/config", status: 200, ip: "198.51.100.10"})
	ev(at, "auth_bruteforce", "warning", "192.0.2.50", "", "")
	ev(at.Add(-3*time.Hour), "auth_bruteforce", "warning", "192.0.2.50", "", "")

	list, noise, err := s.incidents(ctx, timeRange{From: now.Add(-6 * time.Hour), To: now})
	if err != nil {
		t.Fatal(err)
	}
	if noise["incidents"] != 1 || noise["addresses"] != 1 {
		t.Fatalf("noise %v", noise)
	}
	if len(list) != 4 {
		t.Fatalf("want 4 incidents, got %d: %+v", len(list), list)
	}
	byIP := map[string]*secIncident{}
	for _, inc := range list[:2] {
		byIP[inc.Value] = inc
	}
	stuffed, probed := byIP["203.0.113.1"], byIP["198.51.100.10"]
	if stuffed == nil || probed == nil || stuffed.Outcome != "succeeded" || probed.Outcome != "succeeded" {
		t.Fatalf("succeeded ones must come first: %+v", list)
	}
	if len(stuffed.Findings) != 2 || stuffed.Steps[0].Action != "end_sessions" || stuffed.Steps[0].Target != "ada@x.io" {
		t.Fatalf("%+v", stuffed)
	}
	if list[2].Value != "192.0.2.50" || list[2].Outcome != "attempted" || list[3].Value != "192.0.2.50" {
		t.Fatalf("a gap over an hour splits an incident: %+v", list[2:])
	}
}
