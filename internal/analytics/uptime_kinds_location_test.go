package analytics

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/styropyr0/visionary/internal/agentapi"
	"github.com/styropyr0/visionary/internal/uptimecore"
)

func TestKindsAtLocation(t *testing.T) {
	s, _ := kindRunner(t)
	ctx := context.Background()
	admin := httptest.NewRequest("POST", "/api/uptime/locations", nil)
	_, key, err := s.createLocation(admin, "edge-1", true)
	if err != nil {
		t.Fatal(err)
	}
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
			_, _ = io.WriteString(conn, "220 ready\r\n")
			conn.Close()
		}
	}()
	c := normalized(t, UptimeCheck{Name: "Port", Kind: "tcp", Enabled: true, Locations: []string{"edge-1"},
		TCP: &TCPConfig{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, Expect: "220"}})
	if err := s.saveUptimeCheck(ctx, &c); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+agentapi.LocationPollPath, s.handleLocationPoll)
	mux.HandleFunc("POST "+agentapi.LocationResultsPath, s.handleLocationResults)
	post := func(path string, body any, out any) int {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest("POST", path, strings.NewReader(string(raw)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+key)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if out != nil {
			_ = json.Unmarshal(rec.Body.Bytes(), out)
		}
		return rec.Code
	}
	var got agentapi.LocationChecks
	if code := post(agentapi.LocationPollPath, agentapi.LocationPoll{Location: "edge-1", Instance: "i1", Kinds: []string{"http", "tcp"}}, &got); code != 200 || len(got.Checks) != 1 || got.Checks[0].TCP == nil {
		t.Fatalf("poll: %d %+v", code, got)
	}
	res := uptimecore.ProbeWithRetry(ctx, got.Checks[0], func() bool { return true })
	if !res.OK || res.Detail == nil {
		t.Fatalf("run at the location: %+v", res)
	}
	raw, _ := json.Marshal(res)
	var sent uptimecore.Result
	_ = json.Unmarshal(raw, &sent)
	if code := post(agentapi.LocationResultsPath, agentapi.LocationResults{Location: "edge-1", Instance: "i1",
		Results: []agentapi.LocationResult{{CheckID: c.ID, Result: sent}}}, nil); code != 200 {
		t.Fatalf("results: %d", code)
	}
	rows, err := s.db.Query(ctx, `SELECT location, detail FROM uptime_run_details WHERE check_id = ?`, c.ID)
	if err != nil || len(rows.Rows) != 1 || toString(rows.Rows[0][0]) != "edge-1" || !strings.Contains(toString(rows.Rows[0][1]), "220 ready") {
		t.Fatalf("details: %v %v", err, rows)
	}

	hb := normalized(t, UptimeCheck{Name: "Job", Kind: "heartbeat", Locations: []string{"edge-1"}, Heartbeat: &HeartbeatConfig{PeriodS: 600}})
	if len(hb.Locations) != 1 || hb.Locations[0] != serverLocation || !hb.runsAt(serverLocation) {
		t.Fatalf("heartbeat locations: %v", hb.Locations)
	}
}
