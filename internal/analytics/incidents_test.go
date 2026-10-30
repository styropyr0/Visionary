package analytics

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// incidentMux serves the incident handlers as a user with the given role.
func incidentMux(s *Server, role string) http.Handler {
	as := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			h(w, r.WithContext(context.WithValue(r.Context(), sessionKey{}, &session{UserID: "u-" + role, Name: "Ann " + role, Role: role})))
		}
	}
	mux := http.NewServeMux()
	for pattern, h := range map[string]http.HandlerFunc{
		"GET /api/incidents":                        s.handleListIncidents,
		"POST /api/incidents":                       s.handleDeclareIncident,
		"GET /api/incidents/stats":                  s.handleIncidentStats,
		"GET /api/incidents/{id}":                   s.handleGetIncident,
		"POST /api/incidents/{id}/status":           s.handleIncidentStatus,
		"POST /api/incidents/{id}/roles":            s.handleAssignRole,
		"POST /api/incidents/{id}/notes":            s.handleIncidentNote,
		"POST /api/incidents/{id}/updates":          s.handleIncidentUpdate,
		"POST /api/incidents/{id}/actions":          s.handleAddAction,
		"POST /api/incidents/{id}/postmortem/draft": s.handleDraftPostmortem,
		"GET /api/incidents/{id}/postmortem.md":     s.handlePostmortemMarkdown,
	} {
		mux.HandleFunc(pattern, as(h))
	}
	return mux
}

func incidentDo(t *testing.T, h http.Handler, method, path, body string, want int) []byte {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, path, rd)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s = %d %s, want %d", method, path, w.Code, w.Body, want)
	}
	return w.Body.Bytes()
}

func TestIncidentLifecycle(t *testing.T) {
	s := logsTestServer(t, nil)
	ctx := context.Background()
	editor, viewer := incidentMux(s, "editor"), incidentMux(s, "viewer")

	var mu sync.Mutex
	var got []map[string]any
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		mu.Lock()
		got = append(got, m)
		mu.Unlock()
	}))
	defer hook.Close()
	ch := &alertChannel{Name: "ops hook", Kind: "webhook", Config: channelConfig{URL: hook.URL}}
	if err := s.saveChannel(ctx, ch); err != nil {
		t.Fatal(err)
	}

	incidentDo(t, viewer, "POST", "/api/incidents", `{"title":"x","severity":"sev2"}`, http.StatusForbidden)
	incidentDo(t, editor, "POST", "/api/incidents", `{"title":"x","severity":"sev9"}`, http.StatusBadRequest)

	started := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339)
	var in declaredIncident
	b := incidentDo(t, editor, "POST", "/api/incidents", `{"title":"Checkout failing","severity":"sev2","services":["shop","shop",""],"impact":"Card payments fail","started_at":"`+started+`"}`, http.StatusCreated)
	if err := json.Unmarshal(b, &in); err != nil || in.ID == 0 || in.Status != "investigating" || len(in.Services) != 1 || in.TTA != nil {
		t.Fatalf("declared = %s", b)
	}
	base := "/api/incidents/" + strconv.FormatInt(in.ID, 10)

	b = incidentDo(t, editor, "POST", base+"/roles", `{"role":"commander","user":"Bo"}`, http.StatusOK)
	_ = json.Unmarshal(b, &in)
	if in.Commander != "Bo" || in.TTA == nil || *in.TTA < 590 {
		t.Fatalf("after commander = %s", b)
	}
	incidentDo(t, editor, "POST", base+"/roles", `{"role":"responder"}`, http.StatusOK)
	incidentDo(t, editor, "POST", base+"/notes", `{"body":"Stripe answers 502","link":"#/dependencies"}`, http.StatusNoContent)
	incidentDo(t, editor, "POST", base+"/notes", `{"body":"x","link":"javascript:alert(1)"}`, http.StatusBadRequest)
	incidentDo(t, editor, "POST", base+"/status", `{"status":"identified"}`, http.StatusOK)
	incidentDo(t, editor, "POST", base+"/updates", `{"body":"Rolling back","status":"monitoring","channels":[`+strconv.FormatInt(ch.ID, 10)+`],"status_page":true}`, http.StatusOK)
	b = incidentDo(t, editor, "POST", base+"/status", `{"status":"resolved"}`, http.StatusOK)
	_ = json.Unmarshal(b, &in)
	if in.TTM == nil || in.TTR == nil || in.ResolvedAt == "" || !in.OnStatusPage {
		t.Fatalf("resolved = %s", b)
	}
	incidentDo(t, viewer, "POST", base+"/status", `{"status":"investigating"}`, http.StatusForbidden)

	for i := 0; i < 50; i++ {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	if len(got) != 1 || got[0]["event"] != "incident.update" || got[0]["message"] != "Rolling back" {
		t.Fatalf("webhook got %v", got)
	}
	mu.Unlock()

	var detail struct {
		Timeline []incidentEntry `json:"timeline"`
	}
	b = incidentDo(t, viewer, "GET", base, "", http.StatusOK)
	if err := json.Unmarshal(b, &detail); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, e := range detail.Timeline {
		kinds[e.Kind]++
	}
	if kinds["declared"] != 1 || kinds["role"] != 2 || kinds["note"] != 1 || kinds["status"] != 3 || kinds["update"] != 1 {
		t.Fatalf("timeline kinds = %v", kinds)
	}

	var draft struct {
		Postmortem postmortem `json:"postmortem"`
		Note       string     `json:"note"`
	}
	b = incidentDo(t, editor, "POST", base+"/postmortem/draft", `{}`, http.StatusOK)
	if err := json.Unmarshal(b, &draft); err != nil || draft.Postmortem.Drafted != "data" || draft.Note == "" || !strings.Contains(draft.Postmortem.Timeline, "Rolling back") {
		t.Fatalf("draft = %s", b)
	}
	incidentDo(t, editor, "POST", base+"/actions", `{"body":"Alert on Stripe 502s","owner":"Bo"}`, http.StatusOK)
	md := string(incidentDo(t, viewer, "GET", base+"/postmortem.md", "", http.StatusOK))
	if !strings.Contains(md, "# Postmortem: Checkout failing") || !strings.Contains(md, "- [ ] Alert on Stripe 502s — Bo") || !strings.Contains(md, "| Commander | Bo |") {
		t.Fatalf("markdown = %s", md)
	}

	page, err := s.buildStatus(ctx)
	if err != nil || len(page.Notices) != 1 || len(page.Notices[0].Updates) != 1 || page.Notices[0].ResolvedAt == "" {
		t.Fatalf("status notices = %+v %v", page, err)
	}

	var stats struct {
		Total      int            `json:"total"`
		BySeverity map[string]int `json:"by_severity"`
		MTTR       *int64         `json:"mttr_s"`
	}
	b = incidentDo(t, viewer, "GET", "/api/incidents/stats?from="+time.Now().Add(-24*time.Hour).UTC().Format(time.RFC3339)+"&to="+time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "", http.StatusOK)
	if err := json.Unmarshal(b, &stats); err != nil || stats.Total != 1 || stats.BySeverity["sev2"] != 1 || stats.MTTR == nil {
		t.Fatalf("stats = %s", b)
	}
}

func TestStatusNoticeLabel(t *testing.T) {
	p := &statusPage{State: "operational", Label: "All systems operational", Notices: []statusNotice{{Status: "identified"}}}
	applyNotices(p)
	if p.State != "degraded" || !strings.Contains(p.Label, "identified") {
		t.Fatalf("page = %+v", p)
	}
	p = &statusPage{State: "major_outage", Label: "Major outage", Notices: []statusNotice{{Status: "investigating"}}}
	if applyNotices(p); p.State != "major_outage" {
		t.Fatalf("an outage the checks see stays: %+v", p)
	}
}
