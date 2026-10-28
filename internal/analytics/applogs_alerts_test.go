package analytics

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/styropyr0/visionary/sdks/go/logrules"
)

func TestLogAlerts(t *testing.T) {
	s := logsTestServer(t, nil)
	ctx := context.Background()
	now := time.Now().UTC()
	_, err := s.ingestLogLines(ctx, []*logLine{
		{at: now.Add(-2 * time.Minute), service: "checkout", sev: logrules.Error, message: "card declined for order 1"},
		{at: now.Add(-time.Minute), service: "checkout", sev: logrules.Error, message: "card declined for order 2"},
		{at: now.Add(-time.Minute), service: "web", sev: logrules.Error, message: "card declined elsewhere"},
		{at: now.Add(-time.Minute), service: "checkout", sev: logrules.Warn, message: "slow gateway"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.patternPass(ctx); err != nil {
		t.Fatal(err)
	}
	rule := func(metric, query, op string, th float64, service string) alertRule {
		return alertRule{ID: 1, Kind: "threshold", Spec: ruleSpec{Metric: metric, Query: query, Op: op, Threshold: th, Window: "5m", Scope: alertScope{Service: service}}}
	}
	obs, err := s.observe(ctx, rule("log_matches", "level:error declined", ">", 1, "checkout"), now)
	if err != nil || len(obs) != 1 || *obs[0].Value != 2 || !obs[0].Breach {
		t.Fatalf("matches %+v %v", obs, err)
	}
	if !strings.Contains(obs[0].Message, "`level:error declined` in checkout") || !strings.Contains(obs[0].Message, "Latest: checkout: ERROR card declined for order 2") {
		t.Fatalf("message %q", obs[0].Message)
	}
	obs, err = s.observe(ctx, rule("log_absent", `"backup finished"`, "<", 1, ""), now)
	if err != nil || len(obs) != 1 || *obs[0].Value != 0 || !obs[0].Breach {
		t.Fatalf("absent %+v %v", obs, err)
	}
	obs, err = s.observe(ctx, rule("log_new_patterns", "", ">", 0, "checkout"), now)
	if err != nil || len(obs) != 1 || *obs[0].Value != 2 || !strings.Contains(obs[0].Message, "card declined for order <*>") {
		t.Fatalf("new patterns %+v %v", obs, err)
	}
	obs, err = s.observe(ctx, rule("log_error_lines", "", ">", 2, ""), now)
	if err != nil || len(obs) == 0 || *obs[0].Value != 3 {
		t.Fatalf("error lines %+v %v", obs, err)
	}
	// a rule on a pattern means the volume page no longer lists it as unwatched
	res, _ := s.db.Query(ctx, `SELECT pattern FROM app_logs WHERE message = 'slow gateway'`)
	pid := toString(res.Rows[0][0])
	on := true
	saved, err := s.checkRule(ctx, ruleInput{Name: "slow gateway again", Kind: "threshold", Severity: "warning", Enabled: &on,
		Spec: ruleSpec{Metric: "log_matches", Query: "pattern:" + pid, Op: ">", Threshold: 0, Window: "5m"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.insertRule(ctx, &saved); err != nil {
		t.Fatal(err)
	}
	s.alertEngine().invalidate(0)
	if !s.alertedPatterns(ctx)[pid] {
		t.Fatalf("pattern %s not watched", pid)
	}
	if _, err := s.checkRule(ctx, ruleInput{Name: "x", Kind: "threshold", Severity: "warning",
		Spec: ruleSpec{Metric: "log_matches", Query: "level:loud", Op: ">", Window: "5m"}}); err == nil || !strings.Contains(err.Error(), "log search") {
		t.Fatalf("bad search accepted: %v", err)
	}
}
