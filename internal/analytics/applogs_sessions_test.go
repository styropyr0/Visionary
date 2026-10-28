package analytics

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/styropyr0/visionary/sdks/go/logrules"
)

func TestClientLogEvents(t *testing.T) {
	now := time.Now().UTC()
	body := `{"app":"web","app_version":"2.4.1","session_id":"sess-1","user_id":"u7","device":"Chrome 141","events":[
	 {"ts":"` + now.Format(time.RFC3339Nano) + `","kind":"log","name":"warn","message":"cart total mismatch","attrs":{"cart_id":"c9","logger":"cart"}},
	 {"ts":"` + now.Format(time.RFC3339Nano) + `","kind":"page_load","name":"/cart","duration_ms":800}
	]}`
	rows, err := parseClientBatch([]byte(body), now, "IN")
	if err != nil {
		t.Fatal(err)
	}
	rest, lines := clientLogLines(rows)
	if len(rest) != 1 || rest[0].Kind != "page_load" || len(lines) != 1 {
		t.Fatalf("rest %d lines %d", len(rest), len(lines))
	}
	l := lines[0]
	if l.sev != logrules.Warn || l.message != "cart total mismatch" || l.sessionID != "sess-1" || l.userID != "u7" || l.source != "browser" ||
		l.service != "web" || l.version != "2.4.1" || l.logger != "cart" || l.attrs["cart_id"] != "c9" || l.attrs["device"] != "Chrome 141" {
		t.Fatalf("line %+v", l)
	}
}

func TestSessionTimeline(t *testing.T) {
	s := logsTestServer(t, nil)
	ctx := context.Background()
	t0 := time.Now().UTC().Add(-10 * time.Minute)
	at := func(sec int) string { return t0.Add(time.Duration(sec) * time.Second).Format(tsLayout) }
	for _, e := range []struct {
		sec               int
		kind, name, attrs string
		status            any
	}{
		{0, "page_load", "/checkout", "", nil},
		{2, "api", "POST /api/pay", `{"request_id":"rq-77"}`, 500},
		{3, "error", "TypeError", "", nil},
	} {
		if err := s.db.Exec(ctx, `INSERT INTO client_events (created_at, received_at, app, session_id, user_id, kind, name, status, attrs) VALUES (?, ?, 'web', 'sess-9', 'u1', ?, ?, ?, ?)`,
			at(e.sec), at(e.sec), e.kind, e.name, e.status, nullIfEmpty(e.attrs)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.db.Exec(ctx, `INSERT INTO api_logs (request_id, created_at, method, path, route, status, duration_ms, service) VALUES ('rq-77', ?, 'POST', '/api/pay', '/api/pay', 500, 812, 'api')`, at(2)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ingestLogLines(ctx, []*logLine{
		{at: t0.Add(2500 * time.Millisecond), service: "api", sev: logrules.Error, message: "gateway refused the card", requestID: "rq-77"},
		{at: t0.Add(1 * time.Second), service: "web", source: "browser", sev: logrules.Info, message: "pay clicked", sessionID: "sess-9"},
		{at: t0.Add(1 * time.Second), service: "api", sev: logrules.Error, message: "someone else's request", requestID: "rq-other"},
	}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/api/rum/session/sess-9", nil)
	r.SetPathValue("id", "sess-9")
	r = r.WithContext(context.WithValue(r.Context(), sessionKey{}, &session{UserID: "u", Role: "admin"}))
	w := httptest.NewRecorder()
	s.handleSessionTimeline(w, r)
	var out struct {
		Session map[string]any `json:"session"`
		Items   []timelineItem `json:"items"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	var got []string
	for _, it := range out.Items {
		got = append(got, it.Side+":"+it.Kind)
	}
	want := "client:page_load,client:log,client:api,server:request,server:server_log,client:error"
	if w.Code != 200 || strings.Join(got, ",") != want || out.Session["user_id"] != "u1" {
		t.Fatalf("%d %s\n got %s", w.Code, w.Body.String(), strings.Join(got, ","))
	}
	r = httptest.NewRequest("GET", "/api/rum/session-list?from="+t0.Add(-time.Hour).Format(time.RFC3339)+"&to="+time.Now().UTC().Add(time.Minute).Format(time.RFC3339), nil)
	w = httptest.NewRecorder()
	s.handleSessionList(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"sess-9"`) {
		t.Fatalf("list %d %s", w.Code, w.Body.String())
	}
}
