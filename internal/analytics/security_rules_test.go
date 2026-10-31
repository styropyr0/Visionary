package analytics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func addRule(t *testing.T, s *Server, r detectionRule) detectionRule {
	t.Helper()
	r.Enabled = true
	if err := s.checkDetectionRule(&r); err != nil {
		t.Fatal(err)
	}
	if err := s.insertDetectionRule(context.Background(), &r, "test"); err != nil {
		t.Fatal(err)
	}
	return r
}

func fromTemplate(t *testing.T, id string) detectionRule {
	t.Helper()
	tpl, ok := templateByID(id)
	if !ok {
		t.Fatalf("no template %s", id)
	}
	return tpl.rule()
}

func signalsOf(t *testing.T, s *Server, kind string) []storedFinding {
	t.Helper()
	var out []storedFinding
	for _, f := range findingsOf(t, s)[kind] {
		if f.source == "rule" {
			out = append(out, f)
		}
	}
	return out
}

func TestRuleTemplatesValid(t *testing.T) {
	s := securityTestServer(t, nil)
	seen := map[string]bool{}
	for _, tpl := range ruleTemplates {
		if seen[tpl.ID] {
			t.Fatalf("duplicate template %s", tpl.ID)
		}
		seen[tpl.ID] = true
		r := tpl.rule()
		if err := s.checkDetectionRule(&r); err != nil {
			t.Errorf("%s: %v", tpl.ID, err)
		}
		if mitreName(tpl.Tactic) == tpl.Tactic || mitreName(tpl.Technique) == tpl.Technique {
			t.Errorf("%s: MITRE ids without names", tpl.ID)
		}
	}
	bad := []detectionRule{
		{Name: "x", Source: "nope", Logic: "threshold", Window: "10m", Threshold: 1},
		{Name: "x", Source: "requests", Logic: "sequence", Window: "10m", GroupBy: []string{"ip"}},
		{Name: "x", Source: "requests", Logic: "threshold", Window: "10m", Threshold: 1, GroupBy: []string{"country"}},
		{Name: "x", Source: "requests", Logic: "threshold", Window: "10m", Threshold: 1, Filter: map[string]string{"status": "teapot"}},
		{Name: "x", Source: "logs", Logic: "threshold", Window: "10m", Threshold: 1, Filter: map[string]string{"query": `"open`}},
		{Name: "x", Source: "requests", Logic: "new_value", Window: "10m", GroupBy: []string{"user"}, Field: "user"},
		{Name: "x", Source: "requests", Logic: "threshold", Window: "48h", Threshold: 1},
		{Name: "x", Source: "requests", Logic: "threshold", Window: "10m", Threshold: 1, GroupBy: []string{"ip"},
			Suppress: []ruleSuppression{{Field: "user", Value: "bob"}}},
	}
	for i, r := range bad {
		if err := s.checkDetectionRule(&r); err == nil {
			t.Errorf("bad rule %d passed", i)
		}
	}
}

func TestRuleThreshold(t *testing.T) {
	s := securityTestServer(t, nil)
	ctx := context.Background()
	now := time.Now().UTC()
	at := now.Add(-2 * time.Minute)
	hook := make(chan map[string]any, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		hook <- body
	}))
	defer srv.Close()
	ch := alertChannel{Name: "hook", Kind: "webhook", Config: channelConfig{URL: srv.URL}}
	if err := s.saveChannel(ctx, &ch); err != nil {
		t.Fatal(err)
	}

	var rows []req
	for i := 0; i < 25; i++ {
		rows = append(rows, req{at: at, method: "POST", path: "/api/login", route: "/api/login", status: 401, ip: "203.0.113.9"})
	}
	for i := 0; i < 3; i++ {
		rows = append(rows, req{at: at, method: "POST", path: "/api/login", route: "/api/login", status: 401, ip: "198.51.100.1"})
	}
	rows = append(rows, req{at: at, method: "GET", path: "/api/orders", status: 200, ip: "203.0.113.9"})
	addReqs(t, s, rows...)
	r := fromTemplate(t, "login_bruteforce")
	r.Channels = []int64{ch.ID}
	r = addRule(t, s, r)
	s.runRules(ctx, now)

	got := signalsOf(t, s, r.Kind)
	if len(got) != 1 || got[0].ip != "203.0.113.9" || got[0].count != 25 {
		t.Fatalf("signals = %+v", got)
	}
	d := got[0].detail
	if d["technique"] != "T1110.001" || d["rule"] != r.Name || d["samples"] == nil {
		t.Fatalf("detail = %v", d)
	}
	select {
	case body := <-hook:
		if body["event"] != "security.signal" || !strings.Contains(body["title"].(string), r.Name) {
			t.Fatalf("webhook = %v", body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no webhook")
	}

	// The same burst going on grows the open signal instead of opening one.
	more := make([]req, 5)
	for i := range more {
		more[i] = req{at: now.Add(time.Minute), method: "POST", path: "/api/login", route: "/api/login", status: 401, ip: "203.0.113.9"}
	}
	addReqs(t, s, more...)
	s.runRules(ctx, now.Add(2*time.Minute))
	got = signalsOf(t, s, r.Kind)
	if len(got) != 1 || got[0].count != 30 {
		t.Fatalf("after more: %+v", got)
	}

	// A suppressed group writes nothing.
	r.Suppress = []ruleSuppression{{Field: "ip", Value: "198.51.100.1"}}
	r.Threshold = 2
	if err := s.checkDetectionRule(&r); err != nil {
		t.Fatal(err)
	}
	if err := s.updateDetectionRule(ctx, &r); err != nil {
		t.Fatal(err)
	}
	s.runRules(ctx, now.Add(4*time.Minute))
	for _, f := range signalsOf(t, s, r.Kind) {
		if f.ip == "198.51.100.1" {
			t.Fatal("suppressed group signalled")
		}
	}
}

func TestRuleDistinctAndLogs(t *testing.T) {
	s := securityTestServer(t, nil)
	ctx := context.Background()
	now := time.Now().UTC()
	at := now.Add(-3 * time.Minute)
	var rows []req
	for i := 0; i < 12; i++ {
		rows = append(rows, req{at: at, method: "POST", path: "/api/login", route: "/api/login", status: 401, ip: "203.0.113.5", login: "user" + string(rune('a'+i)) + "@x.io"})
	}
	addReqs(t, s, rows...)
	stuffing := addRule(t, s, fromTemplate(t, "credential_stuffing"))

	for i := 0; i < 4; i++ {
		if err := s.db.Exec(ctx, `INSERT INTO app_logs (created_at, service, source, level, severity, message, size) VALUES (?, 'auth', 'sdk', 'warn', 4, ?, 10)`,
			at.Format(tsLayout), "authentication failed for user 42"); err != nil {
			t.Fatal(err)
		}
	}
	logs := fromTemplate(t, "log_auth_failures")
	logs.Threshold = 3
	logs = addRule(t, s, logs)
	s.runRules(ctx, now)

	got := signalsOf(t, s, stuffing.Kind)
	if len(got) != 1 || got[0].detail["distinct"] != float64(12) {
		t.Fatalf("stuffing = %+v", got)
	}
	got = signalsOf(t, s, logs.Kind)
	if len(got) != 1 || got[0].count != 4 || !strings.Contains(got[0].target, "service=auth") {
		t.Fatalf("logs = %+v", got)
	}
}

func TestRuleNewValue(t *testing.T) {
	s := securityTestServer(t, nil)
	ctx := context.Background()
	now := time.Now().UTC()

	// With nothing to learn from yet, a rule stays unlearned rather than
	// flagging everything that comes first.
	early := addRule(t, s, fromTemplate(t, "app_user_new_ip_admin_route"))
	s.runRules(ctx, now.Add(-time.Hour))
	if res, err := s.db.Query(ctx, `SELECT COUNT(*) FROM security_known WHERE kind = ?`, ruleKnownKind(early.ID)); err != nil || toInt(res.Rows[0][0]) != 0 {
		t.Fatalf("learned from nothing: %v %v", res, err)
	}
	if err := s.db.Exec(ctx, `DELETE FROM security_rules WHERE id = ?`, early.ID); err != nil {
		t.Fatal(err)
	}

	addReqs(t, s,
		req{at: now.Add(-48 * time.Hour), path: "/api/admin/users", route: "/api/admin/users", status: 200, ip: "10.0.0.1", handle: "bob"},
		req{at: now.Add(-2 * time.Minute), path: "/api/admin/users", route: "/api/admin/users", status: 200, ip: "10.0.0.1", handle: "bob"},
		req{at: now.Add(-time.Minute), path: "/api/admin/users", route: "/api/admin/users", status: 200, ip: "10.9.9.9", handle: "bob"},
	)
	r := addRule(t, s, fromTemplate(t, "app_user_new_ip_admin_route"))
	s.runRules(ctx, now)
	got := signalsOf(t, s, r.Kind)
	if len(got) != 1 || got[0].ip != "10.9.9.9" || got[0].actor != "bob" || got[0].detail["new_value"] != "10.9.9.9" {
		t.Fatalf("signals = %+v", got)
	}

	// Seen once, a value is not new again.
	addReqs(t, s, req{at: now.Add(time.Minute), path: "/api/admin/users", route: "/api/admin/users", status: 200, ip: "10.9.9.9", handle: "bob"})
	s.runRules(ctx, now.Add(2*time.Minute))
	if got := signalsOf(t, s, r.Kind); len(got) != 1 {
		t.Fatalf("repeat value signalled: %+v", got)
	}

	// The preview finds the same new value over the last day.
	hits, _, err := s.previewRule(ctx, &r, now.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Value != "10.9.9.9" {
		t.Fatalf("preview = %+v", hits)
	}
}

func TestRuleSequence(t *testing.T) {
	s := securityTestServer(t, nil)
	ctx := context.Background()
	now := time.Now().UTC()
	var rows []req
	for i := 0; i < 6; i++ {
		rows = append(rows, req{at: now.Add(-4*time.Minute + time.Duration(i)*time.Second), method: "POST", path: "/api/login", route: "/api/login", status: 401, ip: "203.0.113.7"})
	}
	rows = append(rows, req{at: now.Add(-2 * time.Minute), method: "POST", path: "/api/login", route: "/api/login", status: 200, ip: "203.0.113.7"})
	// Success first, failures after: not a guessed password.
	rows = append(rows, req{at: now.Add(-4 * time.Minute), method: "POST", path: "/api/login", route: "/api/login", status: 200, ip: "203.0.113.8"})
	for i := 0; i < 6; i++ {
		rows = append(rows, req{at: now.Add(-3 * time.Minute), method: "POST", path: "/api/login", route: "/api/login", status: 401, ip: "203.0.113.8"})
	}
	addReqs(t, s, rows...)
	r := addRule(t, s, fromTemplate(t, "failed_then_success"))
	// The first run counts only its last interval as new; start the cursor
	// earlier, as a rule that has been running would have.
	rs := s.ruleRuns()
	rs.runs[r.ID] = &ruleRun{cursor: now.Add(-10 * time.Minute), active: map[string]bool{}, baseline: map[string][]float64{}}
	s.runRules(ctx, now)
	got := signalsOf(t, s, r.Kind)
	if len(got) != 1 || got[0].ip != "203.0.113.7" || got[0].detail["first_step"] != float64(6) {
		t.Fatalf("signals = %+v", got)
	}

	// Host events: SSH failures, then a login from the same address.
	for i := 0; i < 10; i++ {
		if err := s.db.Exec(ctx, `INSERT INTO security_events (created_at, source, kind, severity, host, ip, count) VALUES (?, 'host', 'ssh_failed', 'warning', 'web-1', '198.51.100.4', 2)`,
			now.Add(-5*time.Minute).Format(tsLayout)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.db.Exec(ctx, `INSERT INTO security_events (created_at, source, kind, severity, host, ip, actor, count) VALUES (?, 'host', 'ssh_login', 'info', 'web-1', '198.51.100.4', 'root', 1)`,
		now.Add(-time.Minute).Format(tsLayout)); err != nil {
		t.Fatal(err)
	}
	ssh := fromTemplate(t, "ssh_bruteforce_then_login")
	hits, _, err := s.previewRule(ctx, &ssh, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Group["host"] != "web-1" || hits[0].Count != 21 {
		t.Fatalf("ssh preview = %+v", hits)
	}
}

func TestRuleAnomalyAndPreview(t *testing.T) {
	s := securityTestServer(t, nil)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(10 * time.Minute)
	var rows []req
	for h := 12; h >= 1; h-- {
		for i := 0; i < 3; i++ {
			rows = append(rows, req{at: now.Add(-time.Duration(h) * time.Hour), path: "/api/me", status: 401, ip: "10.0.0.2"})
		}
	}
	for i := 0; i < 80; i++ {
		rows = append(rows, req{at: now.Add(-25 * time.Minute), path: "/api/me", status: 403, ip: "10.0.0.3"})
	}
	addReqs(t, s, rows...)
	r := fromTemplate(t, "auth_error_spike")
	if err := s.checkDetectionRule(&r); err != nil {
		t.Fatal(err)
	}
	hits, _, err := s.previewRule(ctx, &r, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Count != 80 {
		t.Fatalf("anomaly preview = %+v", hits)
	}

	th := fromTemplate(t, "forbidden_per_user")
	th.GroupBy = []string{"ip"}
	if err := s.checkDetectionRule(&th); err != nil {
		t.Fatal(err)
	}
	hits, _, err = s.previewRule(ctx, &th, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Group["ip"] != "10.0.0.3" {
		t.Fatalf("threshold preview = %+v", hits)
	}

	// The job's in-memory baseline: quiet runs, then a burst.
	run := &ruleRun{baseline: map[string][]float64{}}
	for i := 0; i < ruleWarmup; i++ {
		run.record(map[string]float64{"": 4}, nil)
	}
	if base, warm := run.baselineOf(""); !warm || base != 4 {
		t.Fatalf("baseline = %v %v", base, warm)
	}
}

func TestRuleSuppressionExpiry(t *testing.T) {
	r := detectionRule{Suppress: []ruleSuppression{
		{Field: "ip", Value: "1.2.3.4", Until: time.Now().Add(-time.Hour).UTC().Format(tsLayout)},
		{Field: "user", Value: "Bob"},
	}}
	now := time.Now()
	if r.suppressedBy(map[string]string{"ip": "1.2.3.4"}, now) != nil {
		t.Fatal("expired suppression applied")
	}
	if r.suppressedBy(map[string]string{"user": "bob"}, now) == nil {
		t.Fatal("suppression missed")
	}
}

// A filter binds few enough values for D1's 100 parameters per statement.
func TestRuleFilterArgsCapped(t *testing.T) {
	s := securityTestServer(t, nil)
	ips := make([]string, maxFilterArgs+1)
	for i := range ips {
		ips[i] = "10.0.0." + strconv.Itoa(i)
	}
	tr := timeRange{From: time.Now().Add(-time.Hour), To: time.Now()}
	if _, _, err := s.filterSQL("requests", map[string]string{"ip": strings.Join(ips, ",")}, tr); err == nil {
		t.Fatal("too many values accepted")
	}
	if _, args, err := s.filterSQL("requests", map[string]string{"ip": strings.Join(ips[:10], ",")}, tr); err != nil || len(args) != 10 {
		t.Fatalf("%v %v", args, err)
	}
}
