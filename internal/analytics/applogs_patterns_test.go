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

func TestTemplateOf(t *testing.T) {
	for in, want := range map[string]string{
		"payment 4512 failed for user u_93 after 30s":                         "payment <*> failed for user <*> after <*>",
		`user "alice@example.com" signed in from 203.0.113.9:4431`:            "user <*> signed in from <*>",
		"order 3f2a9c1e-1b2c-4d5e-8f90-123456789abc shipped":                  "order <*> shipped",
		"GET https://api.stripe.com/v1/charges?limit=3 took 120ms":            "GET https://api.stripe.com/<*> took <*>",
		"cache miss key=session:abc123 ttl=300":                               "cache miss key=<*> ttl=<*>",
		"retry 1/3, 2/3":                                                      "retry <*>",
		"panic: runtime error\ngoroutine 1 [running]:\nmain.main()":           "panic: runtime error",
		"connected to Postgres":                                               "connected to Postgres",
		"2026-10-28T10:00:00Z worker started":                                 "<*> worker started",
		"hash 0xdeadbeefcafe and sha a3f9c2b18e7d6f50":                        "hash <*> and sha <*>",
	} {
		if got := templateOf(in); got != want {
			t.Errorf("%q:\n got %q\nwant %q", in, got, want)
		}
	}
	if patternID("api", "x") == patternID("web", "x") || len(patternID("api", "x")) != 16 {
		t.Fatal("pattern ids")
	}
}

func TestPatternJobAndAnalysis(t *testing.T) {
	s := logsTestServer(t, nil)
	ctx := context.Background()
	base := time.Now().UTC().Add(-20 * time.Minute)
	var lines []*logLine
	for i := range 30 {
		v := "v1"
		if i >= 15 {
			v = "v2"
		}
		lines = append(lines, &logLine{at: base.Add(time.Duration(i) * time.Second), service: "api", version: v, sev: logrules.Info,
			message: "served order " + strings.Repeat("9", i%3+1), file: "orders.go", line: 40})
	}
	lines = append(lines,
		&logLine{at: base.Add(40 * time.Second), service: "api", version: "v2", sev: logrules.Error, message: "cache unavailable: dial tcp 10.0.0.5:6379", requestID: "rq-ok", file: "cache.go", line: 12},
		&logLine{at: base.Add(41 * time.Second), service: "api", version: "v1", sev: logrules.Warn, message: "legacy path used"},
		&logLine{at: base.Add(42 * time.Second), service: "api", version: "v2", sev: logrules.Info, message: "charge with card 4111 1111 1111 1111 ok", file: "pay.go", line: 77},
	)
	if _, err := s.ingestLogLines(ctx, lines); err != nil {
		t.Fatal(err)
	}
	// A request that answered 200 although it logged an error.
	if err := s.db.Exec(ctx, `INSERT INTO api_logs (request_id, created_at, method, path, route, status, duration_ms) VALUES ('rq-ok', ?, 'GET', '/orders/1', '/orders/{id}', 200, 12)`,
		base.Add(39*time.Second).Format(tsLayout)); err != nil {
		t.Fatal(err)
	}
	n, err := s.patternPass(ctx)
	if err != nil || n != 33 {
		t.Fatalf("pass: %d %v", n, err)
	}
	if n, _ := s.patternPass(ctx); n != 0 {
		t.Fatalf("second pass read %d", n)
	}
	res, _ := s.db.Query(ctx, `SELECT template, lines, first_version, severity, code_file FROM log_patterns ORDER BY lines DESC`)
	if len(res.Rows) != 4 || res.Rows[0][0] != "served order <*>" || toInt(res.Rows[0][1]) != 30 || res.Rows[0][2] != "v1" || res.Rows[0][4] != "orders.go" {
		t.Fatalf("patterns %+v", res.Rows)
	}
	// The card number was redacted in place and reported.
	res, _ = s.db.Query(ctx, `SELECT message FROM app_logs WHERE code_file = 'pay.go'`)
	if !strings.Contains(toString(res.Rows[0][0]), "[REDACTED:card-number]") {
		t.Fatalf("card not redacted: %v", res.Rows)
	}
	res, _ = s.db.Query(ctx, `SELECT source, kind, service, target, count, detail FROM security_events`)
	if len(res.Rows) != 1 || res.Rows[0][0] != "logs" || res.Rows[0][1] != "secret_in_logs" || res.Rows[0][3] != "pay.go:77" || !strings.Contains(toString(res.Rows[0][5]), "card number") {
		t.Fatalf("finding %+v", res.Rows)
	}

	qs := "?" + rangeQS(base, "")
	do := func(handler string, path string) map[string]any {
		r := httptest.NewRequest("GET", path, nil)
		r = r.WithContext(context.WithValue(r.Context(), sessionKey{}, &session{UserID: "u", Role: "admin"}))
		w := httptest.NewRecorder()
		switch handler {
		case "patterns":
			s.handleLogPatterns(w, r)
		case "compare":
			s.handleLogCompare(w, r)
		case "volume":
			s.handleLogVolume(w, r)
		case "hidden":
			s.handleHiddenFailures(w, r)
		}
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", handler, w.Code, w.Body.String())
		}
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	pats := do("patterns", "/api/applogs/patterns"+qs)["patterns"].([]any)
	first := pats[0].(map[string]any)
	if len(pats) != 4 || first["template"] != "served order <*>" || first["count"] != float64(30) || first["new"] != true {
		t.Fatalf("patterns %+v", pats)
	}
	trend := first["trend"].([]any)
	var sum float64
	for _, x := range trend {
		sum += x.(float64)
	}
	if sum != 30 {
		t.Fatalf("trend sums to %v", sum)
	}
	cmp := do("compare", "/api/applogs/compare"+qs+"&a=v1&b=v2&service=api")["changes"].([]any)
	kinds := map[string]string{}
	for _, c := range cmp {
		m := c.(map[string]any)
		kinds[toString(m["template"])] = toString(m["kind"])
	}
	if kinds["cache unavailable: dial tcp <*>"] != "new" || kinds["legacy path used"] != "gone" {
		t.Fatalf("compare %+v", kinds)
	}
	vol := do("volume", "/api/applogs/volume"+qs)
	if vol["lines"] != float64(33) {
		t.Fatalf("volume %+v", vol)
	}
	code := vol["code"].(map[string]any)["rows"].([]any)
	if top := code[0].([]any); top[1] != "orders.go" || !strings.HasPrefix(toString(top[len(top)-1]), "served order") {
		t.Fatalf("code volume %+v", code)
	}
	if unread := vol["unread"].([]any); len(unread) == 0 || unread[0].(map[string]any)["template"] != "served order <*>" {
		t.Fatalf("unread %+v", vol["unread"])
	}
	hidden := do("hidden", "/api/applogs/hidden-failures"+qs)["failures"].([]any)
	if len(hidden) != 1 || hidden[0].(map[string]any)["route"] != "/orders/{id}" || hidden[0].(map[string]any)["template"] != "cache unavailable: dial tcp <*>" {
		t.Fatalf("hidden %+v", hidden)
	}
}

func TestRedactSecrets(t *testing.T) {
	for in, kind := range map[string]string{
		"card 4111-1111-1111-1111":                         "card number",
		"tok ghp_" + strings.Repeat("a", 36):               "GitHub token",
		"slack xoxb-1234567890-abcdef":                     "Slack token",
		"stripe sk_live_" + strings.Repeat("x", 24):        "Stripe key",
		"google AIza" + strings.Repeat("B", 35):            "Google API key",
		"password=hunter2":                                 "credential",
	} {
		out, kinds := redactSecrets(in)
		if len(kinds) == 0 || kinds[0] != kind || out == in {
			t.Errorf("%q: %q %v", in, out, kinds)
		}
	}
	if _, kinds := redactSecrets("order 1234567890123 shipped"); len(kinds) != 0 {
		t.Errorf("non-Luhn number flagged: %v", kinds)
	}
}

func TestLogSettingsAPI(t *testing.T) {
	s := logsTestServer(t, nil)
	call := func(role, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r = r.WithContext(context.WithValue(r.Context(), sessionKey{}, &session{UserID: "u", Name: "Ed", Role: role}))
		if i := strings.LastIndexByte(path, '/'); i >= 0 {
			r.SetPathValue("i", path[i+1:])
		}
		w := httptest.NewRecorder()
		switch {
		case method == "GET":
			s.handleLogSettings(w, r)
		case method == "PUT":
			s.handlePutLogSettings(w, r)
		case strings.HasSuffix(path, "/overrides") && method == "POST":
			s.handleAddLogOverride(w, r)
		case strings.Contains(path, "/overrides/"):
			s.handleDeleteLogOverride(w, r)
		case strings.HasSuffix(path, "/drops"):
			s.handleAddLogDrop(w, r)
		case strings.Contains(path, "/drops/"):
			s.handleDeleteLogDrop(w, r)
		case strings.HasSuffix(path, "/test"):
			s.handleTestParseRule(w, r)
		}
		return w
	}
	w := call("viewer", "GET", "/api/settings/logs", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"level":"info"`) || !strings.Contains(w.Body.String(), `"can_edit":false`) {
		t.Fatalf("get %d %s", w.Code, w.Body.String())
	}
	put := `{"enabled":true,"level":"warn","buffer":true,"buffer_level":"debug","slow_ms":800,"rate":1000,"overrides":[],"drops":[],
	  "server":{"rate":5000,"redact":["ssn"],"rules":[{"name":"r","pattern":"%{WORD:verb} %{INT:n}"}],"syslog":"","access":{"hidden_services":["hr"],"masked_fields":["email"]}}}`
	if w := call("editor", "PUT", "/api/settings/logs", put); w.Code != 403 {
		t.Fatalf("editor put: %d", w.Code)
	}
	if w := call("admin", "PUT", "/api/settings/logs", strings.Replace(put, `"%{WORD:verb} %{INT:n}"`, `"%{NOPE:x}"`, 1)); w.Code != 400 || !strings.Contains(w.Body.String(), "unknown placeholder") {
		t.Fatalf("bad rule: %d %s", w.Code, w.Body.String())
	}
	if w := call("admin", "PUT", "/api/settings/logs", put); w.Code != 200 {
		t.Fatalf("put %d %s", w.Code, w.Body.String())
	}
	if l := s.logRules(); l.Level != logrules.Warn || l.Rate != 1000 || s.logServer().Rate != 5000 || s.logAccess().HiddenServices[0] != "hr" {
		t.Fatalf("not applied: %+v %+v", l, s.logServer())
	}
	// Stored where the SDKs read them.
	res, _ := s.db.Query(context.Background(), `SELECT value FROM visionary_settings WHERE name = 'logs.level'`)
	if len(res.Rows) != 1 || res.Rows[0][0] != "warn" {
		t.Fatalf("stored %+v", res.Rows)
	}
	w = call("editor", "POST", "/api/settings/logs/overrides", `{"route":"/checkout","level":"debug","minutes":15}`)
	if w.Code != 200 {
		t.Fatalf("override %d %s", w.Code, w.Body.String())
	}
	if got := s.logRules().Floor("api", "/checkout", "", time.Now()); got != logrules.Debug {
		t.Fatalf("override floor %d", got)
	}
	if w := call("editor", "POST", "/api/settings/logs/overrides", `{"route":"/x","level":"debug","minutes":0}`); w.Code != 400 {
		t.Fatalf("zero minutes: %d", w.Code)
	}
	if w := call("editor", "POST", "/api/settings/logs/drops", `{"contains":"healthz"}`); w.Code != 200 || len(s.logRules().Drops) != 1 {
		t.Fatalf("drop %d", w.Code)
	}
	if w := call("editor", "POST", "/api/settings/logs/drops", `{}`); w.Code != 400 {
		t.Fatalf("empty drop: %d", w.Code)
	}
	if w := call("editor", "DELETE", "/api/settings/logs/drops/0", ""); w.Code != 200 || len(s.logRules().Drops) != 0 {
		t.Fatalf("drop delete %d", w.Code)
	}
	if w := call("editor", "DELETE", "/api/settings/logs/overrides/0", ""); w.Code != 200 || len(s.logRules().Overrides) != 0 {
		t.Fatalf("override delete %d", w.Code)
	}
	w = call("viewer", "POST", "/api/settings/logs/rules/test", `{"pattern":"%{WORD:verb} took %{INT:ms}ms","sample":"GET took 42ms"}`)
	if !strings.Contains(w.Body.String(), `"matched":true`) || !strings.Contains(w.Body.String(), `"ms":42`) {
		t.Fatalf("rule test %s", w.Body.String())
	}
	// Audit entries are queued for the audit writer.
	var actions []string
	for len(s.team().audits) > 0 {
		if a := (<-s.team().audits).action; strings.HasPrefix(a, "logs.") {
			actions = append(actions, a)
		}
	}
	if strings.Join(actions, ",") != "logs.settings,logs.override_add,logs.drop_add,logs.drop_remove,logs.override_remove" {
		t.Fatalf("audit %v", actions)
	}
}
