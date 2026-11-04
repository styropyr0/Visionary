package analytics

import (
	"context"
	"testing"
	"time"
)

func TestLivenessIgnoresRedeploy(t *testing.T) {
	s := newTestServer(t, "liveness.db", nil)
	t.Cleanup(func() { _ = s.db.Close() })
	ctx := context.Background()
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	add := func(inst, svc, sdk string, from, to time.Duration) {
		for at := from; at <= to; at += 15 * time.Second {
			if err := s.db.Exec(ctx, `INSERT INTO system_metrics (created_at, instance, interval_s, service, sdk) VALUES (?, ?, 15, ?, ?)`,
				now.Add(-at).Format(tsLayout), inst, svc, sdk); err != nil {
				t.Fatal(err)
			}
		}
	}
	// api was redeployed 30 minutes ago; worker lost one of two replicas.
	add("a1b2c3d4e5f6", "api", "go", 30*time.Minute, 2*time.Hour)
	add("0f1e2d3c4b5a", "api", "go", 0, 29*time.Minute)
	add("worker-1", "worker", "go", 30*time.Minute, 2*time.Hour)
	add("worker-2", "worker", "go", 0, 2*time.Hour)

	r := alertRule{ID: 1, Spec: ruleSpec{Op: ">", Threshold: 5}}
	obs, err := s.observeLiveness(ctx, r, now)
	if err != nil {
		t.Fatal(err)
	}
	breach := map[string]bool{}
	for _, ob := range obs {
		breach[ob.Key] = ob.Breach
	}
	want := map[string]bool{"a1b2c3d4e5f6": false, "0f1e2d3c4b5a": false, "worker-1": true, "worker-2": false}
	for k, v := range want {
		if got, ok := breach[k]; !ok || got != v {
			t.Errorf("%s: breach %v (seen %v), want %v", k, got, ok, v)
		}
	}
}
