package analytics

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/styropyr0/visionary/sdks/go/logrules"
)

func TestDigestLogs(t *testing.T) {
	s := logsTestServer(t, nil)
	ctx := context.Background()
	now := time.Now().UTC()
	mid, start := now.Add(-7*24*time.Hour), now.Add(-14*24*time.Hour)
	if _, err := s.ingestLogLines(ctx, []*logLine{
		{at: now.Add(-time.Hour), service: "api", sev: logrules.Error, message: "ledger out of balance by 3"},
		{at: now.Add(-time.Hour), service: "api", sev: logrules.Info, message: "ok"},
		{at: now.Add(-8 * 24 * time.Hour), service: "api", sev: logrules.Info, message: "older"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.patternPass(ctx); err != nil {
		t.Fatal(err)
	}
	d := s.digestLogsData(ctx, start, mid, now)
	if d == nil || d.Lines != 2 || d.PrevLines != 1 || d.Errors != 1 || len(d.New) != 1 || d.New[0].Template != "ledger out of balance by <*>" {
		t.Fatalf("digest %+v", d)
	}
	sec := digestLogsSection(d)
	if sec.Rows[0].Tone != "bad" || !strings.Contains(sec.Note, "2 lines (1 the week before)") {
		t.Fatalf("section %+v", sec)
	}
	pdf, err := s.digestPDF(&digestData{From: mid.Format(tsLayout), To: now.Format(tsLayout), Logs: d}, "")
	if err != nil || !bytes.HasPrefix(pdf, []byte("%PDF")) {
		t.Fatalf("pdf: %v", err)
	}
}
