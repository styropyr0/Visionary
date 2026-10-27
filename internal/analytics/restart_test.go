package analytics

import (
	"context"
	"slices"
	"testing"
)

// A restart-only setting saved after start waits for a restart; changing it
// back, or a setting that applies at once, does not.
func TestRestartPending(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	if err := s.refreshLive(ctx); err != nil {
		t.Fatal(err)
	}
	s.noteStartConfig()
	save := func(k, v string) {
		t.Helper()
		if err := s.writeSettings(ctx, map[string]string{settingConfig + k: v}); err != nil {
			t.Fatal(err)
		}
		if err := s.refreshLive(ctx); err != nil {
			t.Fatal(err)
		}
	}
	save("uptime_allow_private", "true") // applies at once
	save("security_interval", "2m")
	if got := s.restartPending(); !slices.Equal(got, []string{"security_interval"}) {
		t.Fatalf("pending = %v", got)
	}
	save("security_interval", confByKey["security_interval"].Default)
	if got := s.restartPending(); len(got) != 0 {
		t.Fatalf("pending after changing it back = %v", got)
	}
}
