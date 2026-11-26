package analytics

import (
	"context"
	"strings"
	"testing"
	"time"

	sdk "github.com/styropyr0/visionary/sdks/go"
)

func TestSystemKey(t *testing.T) {
	cases := []struct{ inst, svc, sdk, want string }{
		{"web-1", "", "go", "web-1"},
		{"a1b2c3d4e5f6", "api", "go", "service:api"},
		{"a1b2c3d4e5f6", "", "go", "app:go"},
		{"go-app", "", "go", "app:go"},
		{"node-app", "", "go", "node-app"},
		{strings.Repeat("ab12", 16), "", "node", "app:node"},
		{"a1b2c3d4e5f6", "", "agent", "a1b2c3d4e5f6"},
		{"a1b2c3d4e5f6", "", "", "a1b2c3d4e5f6"},
		{"host-1", "web", "agent", "host-1"},
	}
	for _, c := range cases {
		if got := systemKey(c.inst, c.svc, c.sdk); got != c.want {
			t.Errorf("systemKey(%q, %q, %q) = %q, want %q", c.inst, c.svc, c.sdk, got, c.want)
		}
	}
}

func TestSystemSettingName(t *testing.T) {
	if _, ok := confByKey["sdk_system_metrics"]; !ok || settingConfig+"sdk_system_metrics" != sdk.SystemSetting {
		t.Fatalf("the SDKs read %s", sdk.SystemSetting)
	}
}

// An app without a service name, rebuilt in a container: every hostname is new
func TestSystemGroupsRebuiltContainers(t *testing.T) {
	s := newTestServer(t, "sysgroup.db", nil)
	t.Cleanup(func() { _ = s.db.Close() })
	ctx := context.Background()
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	add := func(inst, svc, sdk string, from, to time.Duration) {
		for at := from; at <= to; at += 15 * time.Second {
			if err := s.db.Exec(ctx, `INSERT INTO system_metrics (created_at, instance, interval_s, service, sdk) VALUES (?, ?, 15, NULLIF(?, ''), ?)`,
				now.Add(-at).Format(tsLayout), inst, svc, sdk); err != nil {
				t.Fatal(err)
			}
		}
	}
	add("a1b2c3d4e5f6", "", "go", 90*time.Minute, 2*time.Hour)
	add("0f1e2d3c4b5a", "", "go", 30*time.Minute, 85*time.Minute)
	add("go-app", "", "go", 0, 25*time.Minute)
	add("laptop", "", "go", 0, 2*time.Hour)
	add("ffffffffffff", "", "agent", 0, 2*time.Hour)

	tr := timeRange{From: now.Add(-3 * time.Hour), To: now.Add(time.Minute)}
	res, err := s.runTemplate(ctx, strings.ReplaceAll(s.system["instances"], "{{instance}}", ""), tr, true)
	if err != nil {
		t.Fatal(err)
	}
	withSystemKeys(res)
	withSystemKeys(res)
	keys := map[string]int{}
	for _, row := range res.Rows {
		keys[toString(row[len(row)-1])]++
	}
	if len(res.Columns) != 7 || keys["app:go"] != 3 || keys["laptop"] != 1 || keys["ffffffffffff"] != 1 {
		t.Fatalf("columns %v, systems %v", res.Columns, keys)
	}

	clause, err := s.systemClause(ctx, tr, "app:go")
	if err != nil {
		t.Fatal(err)
	}
	for _, inst := range []string{"a1b2c3d4e5f6", "0f1e2d3c4b5a", "go-app"} {
		if !strings.Contains(clause, "'"+inst+"'") {
			t.Errorf("%s missing from %s", inst, clause)
		}
	}
	if strings.Contains(clause, "laptop") || strings.Contains(clause, "ffffffffffff") {
		t.Errorf("clause takes other systems: %s", clause)
	}
	if _, err := s.systemClause(ctx, tr, "app:go'--"); err == nil {
		t.Error("bad name accepted")
	}

	r := alertRule{ID: 1, Spec: ruleSpec{Op: ">", Threshold: 5}}
	obs, err := s.observeLiveness(ctx, r, now)
	if err != nil {
		t.Fatal(err)
	}
	breach := map[string]bool{}
	for _, ob := range obs {
		breach[ob.Key] = ob.Breach
	}
	want := map[string]bool{"a1b2c3d4e5f6": false, "0f1e2d3c4b5a": false, "go-app": false, "laptop": false}
	for k, v := range want {
		if got, ok := breach[k]; !ok || got != v {
			t.Errorf("%s: breach %v (seen %v), want %v", k, got, ok, v)
		}
	}
}
