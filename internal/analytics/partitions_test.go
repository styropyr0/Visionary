package analytics

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	sdk "github.com/styropyr0/visionary/sdks/go"
)

func TestPartitionNames(t *testing.T) {
	day := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	name := partitionName("api_logs", day)
	if name != "api_logs_p20261030" {
		t.Fatal(name)
	}
	if d, ok := partitionDay("api_logs", name); !ok || !d.Equal(day) {
		t.Fatal(d, ok)
	}
	for _, n := range []string{"api_logs_pdefault", "app_logs_p20261030", "api_logs_p2026103"} {
		if _, ok := partitionDay("api_logs", n); ok {
			t.Fatal("read a day from", n)
		}
	}
	def := "CREATE INDEX idx_api_logs_created_at ON public.api_logs USING btree (created_at)"
	if got := indexTable.ReplaceAllString(def, " ON api_logs_part USING "); got != "CREATE INDEX idx_api_logs_created_at ON api_logs_part USING btree (created_at)" {
		t.Fatal(got)
	}
}

// TestConvertTable runs on PostgreSQL only (VISIONARY_TEST_DATABASE_URL)
func TestConvertTable(t *testing.T) {
	if !strings.HasPrefix(os.Getenv("VISIONARY_TEST_DATABASE_URL"), "postgres") {
		t.Skip("needs VISIONARY_TEST_DATABASE_URL on PostgreSQL")
	}
	s := logsTestServer(t, nil)
	if s.q.d != sdk.Postgres {
		t.Skip("not PostgreSQL")
	}
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.AddDate(0, 0, -3)
	future := now.Truncate(24*time.Hour).AddDate(0, 0, partitionsAhead+5).Add(time.Hour)
	for i, at := range []time.Time{old, old, now, now, now, future} {
		if err := s.db.Exec(ctx, `INSERT INTO api_logs (request_id, created_at, method, path, status, duration_ms) VALUES (?, ?, 'GET', '/', 200, 1)`,
			"r"+string(rune('a'+i)), at.Format(tsLayout)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.convertTable(ctx, "api_logs"); err != nil {
		t.Fatal(err)
	}
	if kind, _ := s.relKind(ctx, "api_logs"); kind != "p" {
		t.Fatal("not partitioned:", kind)
	}
	count := func(q string) int64 {
		res, err := s.db.Query(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		return toInt(res.Rows[0][0])
	}
	if n := count(`SELECT COUNT(*) FROM api_logs`); n != 6 {
		t.Fatal("rows after converting:", n)
	}
	if n := count(`SELECT COUNT(*) FROM api_logs_pdefault`); n != 1 {
		t.Fatal("rows in the default partition:", n)
	}
	id, err := s.db.InsertID(ctx, `INSERT INTO api_logs (request_id, created_at, method, path, status, duration_ms) VALUES ('rz', ?, 'GET', '/', 200, 1)`, now.Format(tsLayout))
	if err != nil || id != 7 {
		t.Fatal(id, err)
	}
	if err := s.createPartition(ctx, "api_logs", "api_logs", future.Truncate(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT COUNT(*) FROM api_logs_pdefault`); n != 0 {
		t.Fatal("default partition kept", n)
	}
	s.dropOldPartitions(ctx, "api_logs", 2)
	if n := count(`SELECT COUNT(*) FROM api_logs`); n != 5 {
		t.Fatal("rows after dropping old days:", n)
	}
	if err := s.db.Exec(ctx, `ALTER TABLE api_logs ADD COLUMN IF NOT EXISTS bench_extra TEXT`); err != nil {
		t.Fatal(err)
	}
}
