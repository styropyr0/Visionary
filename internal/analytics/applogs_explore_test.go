package analytics

import (
	"context"
	"strings"
	"testing"
)

func TestExploreOwnTables(t *testing.T) {
	s := logsTestServer(t, nil)
	ctx := context.Background()
	if err := s.db.Exec(ctx, `CREATE TABLE customers (id TEXT, plan TEXT)`); err != nil {
		t.Fatal(err)
	}
	q := `SELECT c.plan, COUNT(*) FROM app_logs l JOIN customers c ON c.id = l.user_id GROUP BY c.plan`
	if _, err := validateReadOnly(q); err == nil || !strings.Contains(err.Error(), "unknown table customers") {
		t.Fatalf("customers readable without the setting: %v", err)
	}
	defer setOwnTables("")
	setOwnTables("customers, visionary_settings, alert_channels, sqlite_master, bad-name")
	if _, err := validateReadOnly(q); err != nil {
		t.Fatalf("customers not readable: %v", err)
	}
	for _, denied := range []string{"visionary_settings", "alert_channels", "sqlite_master"} {
		if _, err := validateReadOnly(`SELECT * FROM ` + denied); err == nil {
			t.Fatalf("%s readable", denied)
		}
	}
	if p := s.ownTablesPrompt(ctx); !strings.Contains(p, "customers(id, plan)") {
		t.Fatalf("prompt %q", p)
	}
}
