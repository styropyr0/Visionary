package analytics

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWithOrigin(t *testing.T) {
	c := UptimeCheck{Kind: "browser", URL: "https://app.example.com/login?next=/x", Steps: []BrowserStep{
		{Action: "goto", Value: "https://app.example.com/login?next=/x"},
		{Action: "goto", Value: "https://accounts.google.com/o/auth"},
		{Action: "assert_url", Value: "https://api.example.com"},
		{Action: "goto", Value: "https://app.example.com.evil.net/"},
	}}
	got, err := withOrigin(c, "https://pr-12.example.com")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"https://pr-12.example.com/login?next=/x", "https://accounts.google.com/o/auth", "https://pr-12.example.com", "https://app.example.com.evil.net/"}
	if got.URL != want[0] {
		t.Errorf("url = %s", got.URL)
	}
	for i, st := range got.Steps {
		if st.Value != want[i] {
			t.Errorf("step %d = %s, want %s", i, st.Value, want[i])
		}
	}
}

// The newer steps and trace origins follow the override too.
func TestWithOriginBrowserOptions(t *testing.T) {
	c := UptimeCheck{Kind: "browser", URL: "https://app.example.com/", Steps: []BrowserStep{
		{Action: "goto", Value: "https://app.example.com/"},
		{Action: "set_cookie", Name: "a", Value: "1", Scope: ".example.com"},
		{Action: "set_cookie", Name: "b", Value: "1", Scope: "https://app.example.com/shop"},
		{Action: "set_cookie", Name: "c", Value: "1", Scope: ".other.org"},
		{Action: "grant_permission", Permission: "notifications", Scope: "https://app.example.com"},
	}, BrowserOptions: BrowserOptions{TraceOrigins: []string{"https://api.example.com", "https://*.example.com", "https://cdn.other.org"}}}
	got, err := withOrigin(c, "https://pr-12.example.com")
	if err != nil {
		t.Fatal(err)
	}
	scopes := []string{"", "pr-12.example.com", "https://pr-12.example.com/shop", ".other.org", "https://pr-12.example.com"}
	for i, st := range got.Steps {
		if st.Scope != scopes[i] {
			t.Errorf("step %d scope = %s, want %s", i, st.Scope, scopes[i])
		}
	}
	if want := []string{"https://pr-12.example.com", "https://pr-12.example.com", "https://cdn.other.org"}; strings.Join(got.TraceOrigins, " ") != strings.Join(want, " ") {
		t.Errorf("trace origins %v", got.TraceOrigins)
	}
}

func TestParseOverride(t *testing.T) {
	s := logsTestServer(t, nil)
	checks := []UptimeCheck{{Name: "a", Kind: "http", URL: "https://app.example.co.uk/health"}}
	for raw, ok := range map[string]bool{
		"https://pr-1.example.co.uk":   true,
		"https://pr-1.example.co.uk/":  true,
		"https://evil.co.uk":           false,
		"https://pr-1.vercel.app":      false,
		"https://pr-1.example.co.uk/x": false,
		"ftp://pr-1.example.co.uk":     false,
	} {
		if _, err := s.parseOverride(raw, checks); (err == nil) != ok {
			t.Errorf("%s: err = %v", raw, err)
		}
	}
	s.live().Config["uptime_override_hosts"] = "*.vercel.app"
	if o, err := s.parseOverride("https://pr-1.vercel.app:8443", checks); err != nil || o != "https://pr-1.vercel.app:8443" {
		t.Errorf("allowed host: %q %v", o, err)
	}
}

func TestRunsFromCI(t *testing.T) {
	s := logsTestServer(t, nil)
	s.live().Config["uptime_allow_private"] = "true"
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/down" {
			w.WriteHeader(http.StatusInternalServerError)
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer site.Close()
	admin := configMux(s, "admin")
	var key struct{ Secret string }
	_ = json.Unmarshal(incidentDo(t, admin, "POST", "/api/team/keys", `{"name":"pipeline","scope":"ci"}`, http.StatusCreated), &key)
	incidentDo(t, admin, "POST", "/api/uptime/checks", `{"name":"Home","url":"`+site.URL+`/","tags":["smoke"]}`, http.StatusCreated)
	incidentDo(t, admin, "POST", "/api/uptime/checks", `{"name":"Broken","url":"`+site.URL+`/down","tags":["deep"]}`, http.StatusCreated)

	mux := http.NewServeMux()
	s.uptimeRunsRoutes(mux)
	mux.Handle("POST /api/uptime/checks", s.authed(s.handleUptimeCreate))
	call := func(method, path, body string) (int, map[string]any) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+key.Secret)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	if code, _ := call("POST", "/api/uptime/checks", `{"name":"x","url":"https://example.com"}`); code != http.StatusForbidden {
		t.Fatalf("CI key changed a check: %d", code)
	}
	code, out := call("POST", "/api/uptime/runs", `{"tags":["smoke"],"wait":true,"timeout_s":30,"commit":"ABCDEF1234567","release":"v1.2.3"}`)
	if code != http.StatusOK {
		t.Fatalf("run: %d %v", code, out)
	}
	b := out["batch"].(map[string]any)
	if b["status"] != "passed" || b["total"] != 1.0 || b["commit"] != "abcdef1234567" {
		t.Fatalf("batch %v", b)
	}
	code, out = call("POST", "/api/uptime/runs", `{"tags":["smoke","deep"],"wait":true,"timeout_s":30}`)
	if b = out["batch"].(map[string]any); code != http.StatusOK || b["status"] != "failed" || b["failed"] != 1.0 || b["passed"] != 1.0 {
		t.Fatalf("failing run: %d %v", code, out)
	}
	id := int64(b["id"].(float64))
	if code, out = call("GET", "/api/uptime/runs/"+itoa(id), ""); code != http.StatusOK || len(out["batch"].(map[string]any)["items"].([]any)) != 2 {
		t.Fatalf("get: %d %v", code, out)
	}
	if code, _ = call("POST", "/api/uptime/runs", `{"tags":["nothing"]}`); code != http.StatusBadRequest {
		t.Fatalf("no match: %d", code)
	}
	if list, _ := s.listBatches(context.Background(), "abcdef1", 10); len(list) != 1 || list[0].Release != "v1.2.3" {
		t.Fatalf("by commit: %+v", list)
	}

	var before, after int64
	count := func() int64 {
		rows, _ := s.db.Query(context.Background(), `SELECT COUNT(*) FROM uptime_results`)
		return toInt(rows.Rows[0][0])
	}
	before = count()
	code, out = call("POST", "/api/uptime/runs", `{"tags":["smoke"],"wait":true,"environment_url_override":"`+site.URL+`"}`)
	if b = out["batch"].(map[string]any); code != http.StatusOK || b["status"] != "passed" || b["environment_url"] != site.URL {
		t.Fatalf("override run: %d %v", code, out)
	}
	if after = count(); after != before {
		t.Fatalf("override run was recorded: %d -> %d", before, after)
	}

	elsewhere := UptimeCheck{ID: 99, Name: "EU only", Kind: "http", URL: site.URL + "/", Method: "GET", ExpectStatus: "200-299", TimeoutMs: 5000, Locations: []string{"eu-west"}}
	if it := s.runOne(context.Background(), runnerFor(s), elsewhere, ""); it.Status != "passed" {
		t.Fatalf("elsewhere %s: %+v", elsewhere.URL, it)
	}
	if after = count(); after != before {
		t.Fatalf("a run from the server was recorded for an EU-only check")
	}
}
