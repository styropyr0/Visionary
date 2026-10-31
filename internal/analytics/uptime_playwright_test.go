package analytics

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPlaywrightReports(t *testing.T) {
	s := logsTestServer(t, nil)
	ctx := context.Background()
	admin := configMux(s, "admin")
	var key struct{ Secret string }
	_ = json.Unmarshal(incidentDo(t, admin, "POST", "/api/team/keys", `{"name":"pipeline","scope":"ci"}`, http.StatusCreated), &key)
	// Declared before its first run, so the report fills it in.
	var declared struct{ Check UptimeCheck }
	_ = json.Unmarshal(incidentDo(t, admin, "POST", "/api/uptime/checks",
		`{"name":"Checkout","kind":"playwright","interval_s":600,"playwright":{"project":"chromium","title":"checkout.spec.ts › pays by card"}}`, http.StatusCreated), &declared)
	if declared.Check.Playwright == nil || declared.Check.IntervalS != 600 || declared.Check.TimeoutMs != 0 {
		t.Fatalf("declared %+v", declared.Check)
	}

	mux := http.NewServeMux()
	s.uptimePlaywrightRoutes(mux)
	mux.Handle("POST /api/uptime/checks/{id}/run", s.authed(s.handleUptimeRun))
	post := func(path, body string) (int, []byte) {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+key.Secret)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code, w.Body.Bytes()
	}
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG fake"))
	big := base64.StdEncoding.EncodeToString(make([]byte, pwMaxTrace+1))
	code, body := post("/api/uptime/playwright", `{"runs":[
	  {"project":"chromium","title":"checkout.spec.ts › pays by card","status":"failed","ok":false,"duration_ms":2300,"retry":1,
	   "error":"expect(locator).toBeVisible() failed","browser":"chromium","trace_id":"4BF92F3577B34DA6A3CE929D0E0E4736","commit":"abcdef1",
	   "steps":[{"title":"open cart","duration_ms":800},{"title":"pay","duration_ms":1500,"error":"timed out"}],
	   "attachments":[{"name":"screenshot","content_type":"image/png","body":"`+png+`"},{"name":"trace","content_type":"application/zip","body":"`+big+`"}]},
	  {"project":"firefox","title":"home.spec.ts › Home › loads","status":"passed","duration_ms":900,"tags":["@smoke","@Home Page"]},
	  {"project":"firefox","title":"home.spec.ts › Home › later","status":"skipped"}]}`)
	if code != http.StatusOK {
		t.Fatalf("report: %d %s", code, body)
	}
	var out struct{ Results []pwOutcome }
	_ = json.Unmarshal(body, &out)
	if len(out.Results) != 3 || out.Results[0].Status != "recorded" || out.Results[0].CheckID != declared.Check.ID ||
		out.Results[1].Status != "created" || out.Results[2].Status != "skipped" {
		t.Fatalf("results %+v", out.Results)
	}
	created, err := s.getUptimeCheck(ctx, out.Results[1].CheckID)
	if err != nil || created.Name != "Home › loads (firefox)" || strings.Join(created.Tags, ",") != "smoke" || created.IntervalS != 3600 {
		t.Fatalf("created %+v %v", created, err)
	}

	states, _ := s.uptimeStates(ctx, []UptimeCheck{declared.Check}, 1)
	if st := states[declared.Check.ID]; st.State != "down" || !strings.Contains(st.LastError, "toBeVisible") {
		t.Fatalf("state %+v", st)
	}
	runs := incidentDo(t, viewerMux(s), "GET", "/api/uptime/checks/"+itoa(declared.Check.ID)+"/playwright?range=1h", "", http.StatusOK)
	var list struct {
		Runs  []map[string]any
		Steps []map[string]any
	}
	_ = json.Unmarshal(runs, &list)
	if len(list.Runs) != 1 || list.Runs[0]["failed_step"] != "pay" || list.Runs[0]["screenshot"] != true || list.Runs[0]["trace"] != false ||
		list.Runs[0]["trace_id"] != "4bf92f3577b34da6a3ce929d0e0e4736" || len(list.Steps) != 2 {
		t.Fatalf("runs %s", runs)
	}
	rid := itoa(int64(list.Runs[0]["id"].(float64)))
	detail := incidentDo(t, viewerMux(s), "GET", "/api/uptime/checks/"+itoa(declared.Check.ID)+"/playwright/"+rid, "", http.StatusOK)
	if !strings.Contains(string(detail), `"screenshot":"data:image/png;base64,`) || !strings.Contains(string(detail), `"name":"trace"`) {
		t.Fatalf("detail %s", detail)
	}
	incidentDo(t, viewerMux(s), "GET", "/api/uptime/checks/"+itoa(declared.Check.ID)+"/playwright/"+rid+"/trace", "", http.StatusNotFound)

	if code, _ := post("/api/uptime/checks/"+itoa(declared.Check.ID)+"/run", `{}`); code != http.StatusForbidden {
		t.Fatalf("CI key ran a check by id: %d", code)
	}
	if code, _ := post("/api/uptime/playwright", `{"project":"x"}`); code != http.StatusOK {
		t.Fatalf("missing title: %d", code)
	}
}

func viewerMux(s *Server) http.Handler {
	as := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			h(w, r.WithContext(context.WithValue(r.Context(), sessionKey{}, &session{UserID: "u-viewer", Name: "Vee", Role: "viewer"})))
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/uptime/checks/{id}/playwright", as(s.handlePlaywrightRuns))
	mux.HandleFunc("GET /api/uptime/checks/{id}/playwright/{run}", as(s.handlePlaywrightRun))
	mux.HandleFunc("GET /api/uptime/checks/{id}/playwright/{run}/trace", as(s.handlePlaywrightTrace))
	return mux
}

func TestPlaywrightStale(t *testing.T) {
	s := logsTestServer(t, nil)
	ctx := context.Background()
	c := UptimeCheck{Name: "Nightly", Kind: "playwright", IntervalS: 600, Enabled: true, Playwright: &PlaywrightTest{Project: "p", Title: "a › b"}}
	if err := c.normalize(nil); err != nil {
		t.Fatal(err)
	}
	if err := s.saveUptimeCheck(ctx, &c); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-time.Hour).Format(tsLayout)
	if err := runnerFor(s).record(ctx, c, uptimeResult{CreatedAt: old, OK: true, DurationMs: 10}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for range 2 {
		if err := s.markStale(ctx, now); err != nil {
			t.Fatal(err)
		}
	}
	rows, _ := s.db.Query(ctx, `SELECT ok, error FROM uptime_results WHERE check_id = ? ORDER BY created_at`, c.ID)
	if len(rows.Rows) != 2 || toInt(rows.Rows[1][0]) != 0 || !strings.HasPrefix(toString(rows.Rows[1][1]), pwStalePrefix+"10m") {
		t.Fatalf("results %v", rows.Rows)
	}
	// One interval later it is stale again.
	if err := s.markStale(ctx, now.Add(12*time.Minute)); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.db.Query(ctx, `SELECT COUNT(*) FROM uptime_results WHERE check_id = ?`, c.ID)
	if toInt(rows.Rows[0][0]) != 3 {
		t.Fatalf("count %v", rows.Rows)
	}
}
