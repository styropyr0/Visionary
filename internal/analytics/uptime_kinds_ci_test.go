package analytics

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestKindsFromCI(t *testing.T) {
	s, _ := kindRunner(t)
	ctx := context.Background()
	preview := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"env":"preview"}`)
	}))
	defer preview.Close()
	api := normalized(t, UptimeCheck{Name: "API", Kind: "api", FollowRedirects: true, API: &APIConfig{Requests: []APIRequest{
		{Method: "GET", URL: "http://127.0.0.1:1/health", Assert: []APIAssertion{{Source: "json", Path: "$.env", Op: "equals", Value: "preview"}}},
	}}})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()
	tcp := normalized(t, UptimeCheck{Name: "Port", Kind: "tcp", TimeoutMs: 2000, TCP: &TCPConfig{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port}})
	hb := normalized(t, UptimeCheck{Name: "Job", Kind: "heartbeat", Heartbeat: &HeartbeatConfig{PeriodS: 600}})
	for _, c := range []*UptimeCheck{&api, &tcp, &hb} {
		if err := s.saveUptimeCheck(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	override, err := s.parseOverride(preview.URL, []UptimeCheck{api, tcp, hb})
	if err != nil {
		t.Fatal(err)
	}
	run := runnerFor(s)
	if it := s.runOne(ctx, run, api, override); it.Status != "passed" || !strings.Contains(string(it.Detail), "requests") {
		t.Fatalf("api against the preview: %+v %s", it, it.Detail)
	}
	if it := s.runOne(ctx, run, tcp, override); it.Status != "passed" {
		t.Fatalf("tcp: %+v", it)
	}
	rows, _ := s.db.Query(ctx, `SELECT COUNT(*) FROM uptime_results WHERE check_id = ?`, tcp.ID)
	if toInt(rows.Rows[0][0]) != 1 {
		t.Fatal("the tcp run, against its own host, was not recorded")
	}
	if it := s.runOne(ctx, run, hb, override); it.Status != "skipped" || !strings.Contains(it.Error, "pings") {
		t.Fatalf("heartbeat: %+v", it)
	}
}
