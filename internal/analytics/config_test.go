package analytics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func configMux(s *Server, role string) http.Handler {
	as := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			h(w, r.WithContext(context.WithValue(r.Context(), sessionKey{}, &session{UserID: "u-" + role, Name: "Ann", Role: role})))
		}
	}
	mux := http.NewServeMux()
	for pattern, h := range map[string]http.HandlerFunc{
		"GET /api/config/export":    s.handleConfigExport,
		"POST /api/config/import":   s.handleConfigImport,
		"POST /api/alerts/channels": s.handleCreateChannel,
		"POST /api/alerts/rules":    s.handleCreateRule,
		"POST /api/alerts/silences": s.handleCreateSilence,
		"POST /api/uptime/checks":   s.handleUptimeCreate,
		"POST /api/dashboards":      s.handleCreateDashboard,
		"POST /api/team/keys":       s.handleCreateKey,
	} {
		mux.HandleFunc(pattern, as(h))
	}
	return mux
}

func importDoc(t *testing.T, h http.Handler, doc string, dry bool) (counts map[string]int, changes []configChange) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"document": doc, "dry_run": dry})
	var out struct {
		Counts  map[string]int `json:"counts"`
		Changes []configChange `json:"changes"`
	}
	if err := json.Unmarshal(incidentDo(t, h, "POST", "/api/config/import", string(body), http.StatusOK), &out); err != nil {
		t.Fatal(err)
	}
	return out.Counts, out.Changes
}

func TestConfigExportImport(t *testing.T) {
	src := logsTestServer(t, nil)
	a := configMux(src, "admin")
	incidentDo(t, configMux(src, "editor"), "GET", "/api/config/export", "", http.StatusForbidden)

	var ch struct{ ID int64 }
	_ = json.Unmarshal(incidentDo(t, a, "POST", "/api/alerts/channels", `{"name":"ops","kind":"email","to":["ops@example.com"]}`, http.StatusCreated), &ch)
	incidentDo(t, a, "POST", "/api/alerts/channels", `{"name":"hook","kind":"webhook","url":"https://example.com/h/secret"}`, http.StatusCreated)
	incidentDo(t, a, "POST", "/api/alerts/rules", `{"name":"Slow","kind":"threshold","severity":"critical","channels":[`+itoa(ch.ID)+`],
		"spec":{"metric":"latency_p95","op":">","threshold":800,"window":"5m","for":"2m"}}`, http.StatusCreated)
	incidentDo(t, a, "POST", "/api/alerts/silences", `{"name":"Nights","matchers":{"severities":["info"]},
		"schedule":{"kind":"weekly","days":[1,2],"from":"22:00","to":"06:00","tz":"UTC"}}`, http.StatusCreated)
	incidentDo(t, a, "POST", "/api/uptime/checks", `{"name":"Home","url":"https://example.com","tags":["Smoke","deploy","smoke"],"alert":{"mode":"custom","channels":[`+itoa(ch.ID)+`],"failures":3}}`, http.StatusCreated)
	incidentDo(t, a, "POST", "/api/uptime/checks", `{"name":"API","url":"https://example.com/api","headers":{"Authorization":"Bearer x"}}`, http.StatusCreated)
	incidentDo(t, a, "POST", "/api/dashboards", `{"name":"Main","layout":{"panels":[{"id":"p1","kind":"builtin","ref":"overview/latency","x":0,"y":0,"w":6,"h":4}]}}`, http.StatusCreated)

	doc := string(incidentDo(t, a, "GET", "/api/config/export", "", http.StatusOK))
	if strings.Contains(doc, "secret") || strings.Contains(doc, "Bearer x") {
		t.Fatalf("export leaks a secret:\n%s", doc)
	}
	if !strings.Contains(doc, "- deploy\n") || !strings.Contains(doc, "- smoke\n") {
		t.Fatalf("tags not exported:\n%s", doc)
	}
	if !strings.Contains(doc, "kind: visionary-config") || !strings.Contains(doc, "check: ") && !strings.Contains(doc, "mode: custom") {
		t.Fatalf("unexpected export:\n%s", doc)
	}

	dst := logsTestServer(t, nil)
	b := configMux(dst, "admin")
	counts, changes := importDoc(t, b, doc, true)
	if counts["create"] == 0 || counts["update"] != 0 {
		t.Fatalf("dry run: %v %+v", counts, changes)
	}
	if rules, _ := dst.listRules(context.Background(), false); len(rules) != 0 {
		t.Fatal("a dry run wrote rules")
	}
	skipped := map[string]bool{}
	for _, c := range changes {
		if c.Action == "skip" {
			skipped[c.Name] = true
		}
	}
	if !skipped["hook"] || !skipped["API"] || len(skipped) != 2 {
		t.Fatalf("skipped %v", skipped)
	}
	importDoc(t, b, doc, false)
	counts, changes = importDoc(t, b, doc, true)
	if counts["create"] != 0 || counts["update"] != 0 {
		t.Fatalf("second import should change nothing: %v %+v", counts, changes)
	}

	edited := strings.Replace(doc, "threshold: 800", "threshold: 900", 1)
	_, changes = importDoc(t, b, edited, true)
	found := false
	for _, c := range changes {
		if c.Type == "rule" && c.Action == "update" && strings.Join(c.Fields, ",") == "spec.threshold" {
			found = true
		}
	}
	if !found {
		t.Fatalf("threshold change not found: %+v", changes)
	}
}

func TestEditorKeyWrites(t *testing.T) {
	s := logsTestServer(t, nil)
	a := configMux(s, "admin")
	key := func(scope string) string {
		var out struct{ Secret string }
		_ = json.Unmarshal(incidentDo(t, a, "POST", "/api/team/keys", `{"name":"`+scope+`","scope":"`+scope+`"}`, http.StatusCreated), &out)
		return out.Secret
	}
	call := func(secret, method, path, body string) int {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+secret)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux := http.NewServeMux()
		mux.Handle("POST /api/alerts/channels", s.authed(s.handleCreateChannel))
		mux.Handle("POST /api/team/keys", s.authed(s.handleCreateKey))
		mux.ServeHTTP(w, r)
		return w.Code
	}
	ch := `{"name":"x","kind":"email","to":["a@example.com"]}`
	if code := call(key("read"), "POST", "/api/alerts/channels", ch); code != http.StatusForbidden {
		t.Fatalf("read key wrote: %d", code)
	}
	editor := key("editor")
	if code := call(editor, "POST", "/api/alerts/channels", ch); code != http.StatusCreated {
		t.Fatalf("editor key: %d", code)
	}
	if code := call(editor, "POST", "/api/team/keys", `{"name":"y","scope":"read"}`); code != http.StatusForbidden {
		t.Fatalf("editor key made a key: %d", code)
	}
	if code := call(key("admin"), "POST", "/api/team/keys", `{"name":"y","scope":"read"}`); code != http.StatusCreated {
		t.Fatalf("admin key: %d", code)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }
