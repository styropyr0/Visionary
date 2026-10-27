package analytics

import (
	"context"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// Every metric's queries run on a real database: the threshold reading, the
// editor's preview, and the anomaly baseline where it has one.
func TestEveryMetricQueries(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	addRequests(t, s, now.Add(-time.Minute), 10, 200)
	for _, m := range s.allMetrics() {
		rule := alertRule{ID: 1, Name: m.Label, Kind: "threshold", Spec: ruleSpec{Metric: m.ID, Op: m.Op, Threshold: m.Threshold, Window: "5m"}}
		if _, err := s.observe(ctx, rule, now); err != nil {
			t.Errorf("%s: observe: %v", m.ID, err)
		}
		if _, err := s.preview(ctx, rule, now); err != nil {
			t.Errorf("%s: preview: %v", m.ID, err)
		}
		if m.Anomaly {
			rule.Kind, rule.Spec.Window = "anomaly", "15m"
			if _, err := s.observe(ctx, rule, now); err != nil {
				t.Errorf("%s: anomaly: %v", m.ID, err)
			}
		}
	}
}

// The rule for every check leaves out a check with its own rule, so each
// check is alerted on once; turning a check's alerts off silences it.
func TestUptimeAlertModes(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	req := httptest.NewRequest("POST", "/api/uptime/checks", nil)
	a := UptimeCheck{Name: "API", URL: "https://api.example.com/health", Method: "GET", ExpectStatus: "200-299", IntervalS: 60, TimeoutMs: 1000, Enabled: true, Headers: map[string]string{}}
	b := UptimeCheck{Name: "Site", URL: "https://example.com", Method: "GET", ExpectStatus: "200-299", IntervalS: 60, TimeoutMs: 1000, Enabled: true, Headers: map[string]string{}}
	for _, c := range []*UptimeCheck{&a, &b} {
		if err := s.saveUptimeCheck(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		for _, c := range []UptimeCheck{a, b} {
			if err := s.db.Exec(ctx, `INSERT INTO uptime_results (created_at, check_id, check_name, url, ok, status, duration_ms, error) VALUES (?, ?, ?, ?, 0, 503, 12, 'down')`,
				now.Add(-time.Duration(i)*time.Minute).Format(tsLayout), c.ID, c.Name, c.URL); err != nil {
				t.Fatal(err)
			}
		}
	}
	views, err := s.uptimeAlertViews(ctx, []UptimeCheck{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if views[a.ID].Mode != "none" {
		t.Fatalf("before any rule: %+v", views[a.ID])
	}
	def, err := s.ensureDefaultUptimeRule(req)
	if err != nil || def == nil {
		t.Fatalf("default rule: %v %v", def, err)
	}
	if again, _ := s.ensureDefaultUptimeRule(req); again != nil {
		t.Fatal("a second default rule was made")
	}
	if err := s.setCheckAlert(req, a, checkAlertInput{Mode: "custom", Failures: 1}); err != nil {
		t.Fatal(err)
	}
	views, _ = s.uptimeAlertViews(ctx, []UptimeCheck{a, b})
	if views[a.ID].Mode != "custom" || views[a.ID].Failures != 1 || views[b.ID].Mode != "default" {
		t.Fatalf("modes: %+v %+v", views[a.ID], views[b.ID])
	}
	obs, err := s.observe(ctx, *def, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 1 || obs[0].Key != strconv.FormatInt(b.ID, 10) {
		t.Fatalf("the default rule should only watch the check without its own rule: %+v", obs)
	}
	if err := s.setCheckAlert(req, a, checkAlertInput{Mode: "off"}); err != nil {
		t.Fatal(err)
	}
	views, _ = s.uptimeAlertViews(ctx, []UptimeCheck{a})
	if views[a.ID].Mode != "off" {
		t.Fatalf("off: %+v", views[a.ID])
	}
	if obs, _ = s.observe(ctx, *def, now); len(obs) != 1 {
		t.Fatalf("a check with alerts off came back to the default rule: %+v", obs)
	}
	if err := s.setCheckAlert(req, a, checkAlertInput{Mode: "default"}); err != nil {
		t.Fatal(err)
	}
	if obs, _ = s.observe(ctx, *def, now); len(obs) != 2 {
		t.Fatalf("back to default: %+v", obs)
	}
	// A deleted check takes its own rule with it.
	if err := s.setCheckAlert(req, b, checkAlertInput{Mode: "custom"}); err != nil {
		t.Fatal(err)
	}
	s.dropCheckRules(req, b.ID)
	if own := s.checksWithOwnRule(ctx); own[strconv.FormatInt(b.ID, 10)] {
		t.Fatal("rule of a deleted check kept")
	}
}

// A deleted channel leaves no reference behind in rules.
func TestDropChannelFromRules(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	rule := alertRule{Name: "x", Kind: "threshold", Severity: "info", Channels: []int64{3, 4}, Enabled: true,
		Spec: ruleSpec{Metric: "apdex", Op: "<", Threshold: 0.8, Window: "5m", Escalation: []escalationStep{{After: "5m", Channels: []int64{4}}}}}
	if err := s.insertRule(ctx, &rule); err != nil {
		t.Fatal(err)
	}
	n, err := s.dropChannelFromRules(ctx, 4)
	if err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	got, _ := s.getRule(ctx, rule.ID)
	if len(got.Channels) != 1 || got.Channels[0] != 3 || len(got.Spec.Escalation[0].Channels) != 0 {
		t.Fatalf("%+v", got)
	}
}

// Choosing the rule for every check makes it one that alerts someone, even
// when it was switched off and had no channels.
func TestDefaultUptimeRuleRevived(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	req := httptest.NewRequest("PUT", "/api/uptime/checks/1/alert", nil)
	c := UptimeCheck{Name: "API", URL: "https://api.example.com/health", Method: "GET", ExpectStatus: "200-299", IntervalS: 60, TimeoutMs: 1000, Enabled: true, Headers: map[string]string{}}
	if err := s.saveUptimeCheck(ctx, &c); err != nil {
		t.Fatal(err)
	}
	def, err := s.ensureDefaultUptimeRule(req)
	if err != nil || def == nil {
		t.Fatalf("default rule: %v %v", def, err)
	}
	def.Enabled, def.Channels = false, []int64{}
	if err := s.updateRule(ctx, def); err != nil {
		t.Fatal(err)
	}
	ch := alertChannel{Name: "Ops", Kind: "webhook"}
	if err := s.saveChannel(ctx, &ch); err != nil {
		t.Fatal(err)
	}
	if views, _ := s.uptimeAlertViews(ctx, []UptimeCheck{c}); views[c.ID].Mode != "none" {
		t.Fatalf("with the rule off: %+v", views[c.ID])
	}
	if err := s.setCheckAlert(req, c, checkAlertInput{Mode: "default", Failures: 3}); err != nil {
		t.Fatal(err)
	}
	v := mustViews(t, s, c)[c.ID]
	if v.Mode != "default" || len(v.ChannelIDs) != 1 || v.ChannelIDs[0] != ch.ID || v.Failures != 3 {
		t.Fatalf("after choosing the default rule: %+v", v)
	}
	if err := s.setCheckAlert(req, c, checkAlertInput{Mode: "default", Channels: []int64{ch.ID + 99}}); err == nil {
		t.Fatal("an unknown channel was accepted")
	}
}

func mustViews(t *testing.T, s *Server, checks ...UptimeCheck) map[int64]checkAlertView {
	t.Helper()
	views, err := s.uptimeAlertViews(context.Background(), checks)
	if err != nil {
		t.Fatal(err)
	}
	return views
}
