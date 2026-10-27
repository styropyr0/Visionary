package analytics

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

func sampleDigest() *digestData {
	p := func(v float64) *float64 { return &v }
	d := &digestData{
		From: "2026-10-20T00:00:00.000Z", To: "2026-10-27T00:00:00.000Z",
		This: digestWeek{Requests: 1284512, P95: p(412), ErrorRate: p(0.42), Incidents: 3},
		Last: digestWeek{Requests: 1190220, P95: p(356), ErrorRate: p(0.51), Incidents: 1},
		Slowest: []digestRoute{
			{Route: "POST /api/checkout/complete", Requests: 18211, P95: 1840, PrevP95: p(1320)},
			{Route: "GET /api/search?q=:q&page=:page&sort=relevance&filters=:filters", Requests: 402331, P95: 940},
		},
		Regressed: []digestChange{{Route: "POST /api/checkout/complete", Requests: 18211, P95: 1840, PrevP95: 1320, ChangePct: 39}},
		Improved:  []digestChange{{Route: "GET /api/menu", Requests: 90122, P95: 120, PrevP95: 180, ChangePct: -33}},
		Incidents: []digestIncident{{Rule: "Checkout p95 above 1.5 s", Severity: "critical", Count: 2}, {Rule: "5xx rate", Severity: "warning", Count: 1}},
		Releases:  []digestRelease{{Version: "v2.14.0", Service: "api", FirstSeen: "2026-10-22T09:12:00.000Z"}},
		Uptime:    []digestCheck{{Check: "Storefront", Availability: 99.82, Failures: 4, AvgMs: 231}},
	}
	for i := 0; i < 7; i++ {
		day := time.Date(2026, 10, 20+i, 0, 0, 0, 0, time.UTC)
		d.Days = append(d.Days, digestDay{Day: day.Format(tsLayout), Requests: int64(150000 + i*12000 + (i%3)*20000), Errors: int64(400 + i*90)})
	}
	return d
}

func TestReportPDF(t *testing.T) {
	v := digestEmailData("QShop", sampleDigest(), "Checkout got slower after v2.14.0.\n- Requests up 8%.\n- p95 up 16% to 412 ms, mostly POST /api/checkout/complete.")
	doc := reportDoc{App: "QShop", Kind: "Weekly digest", Title: "Performance and reliability", Period: v.Period, TimeZone: "Asia/Kolkata",
		Generated: time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC), Verdict: digestVerdict(v), Summary: v.Summary, Stats: v.Stats, Sections: v.Sections,
		Footnote: "This week is compared with the week before."}
	d := sampleDigest()
	doc.Chart = &reportChart{Title: "Requests per day", ValueName: "Requests", MarkName: "5xx responses"}
	for _, day := range d.Days {
		tm, _ := time.Parse(tsLayout, day.Day)
		doc.Chart.Labels = append(doc.Chart.Labels, tm.Format("Mon 2"))
		doc.Chart.Values = append(doc.Chart.Values, float64(day.Requests))
		doc.Chart.Marks = append(doc.Chart.Marks, float64(day.Errors))
	}
	pdf, err := renderReportPDF(doc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) || len(pdf) < 2000 {
		t.Fatalf("not a PDF: %d bytes", len(pdf))
	}
	if doc.Verdict == nil || doc.Verdict.Tone != "bad" {
		t.Errorf("verdict = %+v, want bad (a critical incident)", doc.Verdict)
	}
	if out := os.Getenv("VISIONARY_PDF_OUT"); out != "" {
		_ = os.WriteFile(out, pdf, 0o644)
	}
}

func TestSecurityReportDoc(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Kolkata")
	in := securityReportData{
		App: "QShop", From: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC), Generated: time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC), Loc: loc,
		Summary: map[string]any{"findings": 42, "critical": 2, "warning": 15, "info": 25, "failed_logins": 381, "scanner_ips": 9, "requests": 1284512, "blocked_rate": 0.8, "hosts": 2},
		Kinds: []map[string]any{
			{"kind": "credential_stuffing", "findings": 12, "occurrences": 340, "critical": 2, "last_seen": "2026-10-26T21:14:00.000Z"},
			{"kind": "path_probe", "findings": 20, "occurrences": 1204, "critical": 0, "last_seen": "2026-10-26T23:02:00.000Z"},
		},
		TopIPs: []map[string]any{{"ip": "203.0.113.42", "requests": 5120, "failed_logins": 210, "unauthorized": 260, "forbidden": 12, "probes": 880}},
		Paths: []map[string]any{
			{"path": "/.env", "hits": 412, "ips": 7, "answered_2xx": 0},
			{"path": "/wp-admin/admin-ajax.php?action=revslider_show_image&img=../wp-config.php", "hits": 96, "ips": 3, "answered_2xx": 4},
		},
		Hosts: []map[string]any{{"host": "web-1", "ssh_failures": 58, "ssh_logins": 3, "sudo": 4, "ports_opened": 0, "pending_updates": 6}},
		Audit: &securityAudit{Counts: []auditCount{{Action: "key.create", Count: 1}}, Entries: []map[string]any{{"created_at": "2026-10-23T10:00:00.000Z", "action": "key.create", "user_name": "saurav", "ip": "198.51.100.7"}}},
	}
	// Rows from the database hold int64 and float64, not int.
	norm := func(rows ...map[string]any) {
		for _, m := range rows {
			for k, v := range m {
				if i, ok := v.(int); ok {
					m[k] = int64(i)
				}
			}
		}
	}
	norm(in.Summary)
	norm(in.Kinds...)
	norm(in.TopIPs...)
	norm(in.Paths...)
	norm(in.Hosts...)
	doc := securityReportDoc(in)
	if doc.Verdict == nil || doc.Verdict.Tone != "bad" {
		t.Fatalf("verdict = %+v, want bad", doc.Verdict)
	}
	pdf, err := renderReportPDF(doc)
	if err != nil {
		t.Fatal(err)
	}
	if out := os.Getenv("VISIONARY_SEC_PDF_OUT"); out != "" {
		_ = os.WriteFile(out, pdf, 0o644)
	}
}

func TestLogoParses(t *testing.T) {
	paths := logoPaths()
	if len(paths) != 2 {
		t.Fatalf("got %d paths, want 2", len(paths))
	}
	for _, p := range paths {
		if len(p) == 0 || p[0].op != 'M' {
			t.Fatal("path must start with M")
		}
		for _, c := range p {
			if want := map[byte]int{'M': 2, 'L': 2, 'C': 6}[c.op]; len(c.v) != want {
				t.Fatalf("%c has %d numbers", c.op, len(c.v))
			}
		}
	}
}

func TestEmailAttachment(t *testing.T) {
	msg := string(buildMessage("Visionary <v@example.com>", []string{"a@example.com"}, "Digest", "text", "<p>html</p>", attachment{Name: "d.pdf", Type: "application/pdf", Data: []byte("%PDF-1.4")}))
	for _, want := range []string{"multipart/mixed", "multipart/alternative", `filename="d.pdf"`, "JVBERi0xLjQ="} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q", want)
		}
	}
}
