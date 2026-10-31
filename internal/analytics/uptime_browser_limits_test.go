package analytics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBrowserLimits(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	r := uptimeResult{OK: true, DurationMs: 4200, LCPMs: f(2600), CLS: f(0.12), TTFBMs: f(300)}
	c := UptimeCheck{DegradedMs: 4000, BrowserOptions: BrowserOptions{BrowserLimits: BrowserLimits{MaxLCPMs: 2500, WarnCLS: 0.1}}}
	if got := c.overLimit(r); got != "LCP 2,600 ms is over the 2,500 ms limit" {
		t.Errorf("over: %q", got)
	}
	if got := c.warnings(r); got != "the run took 4,200 ms, above 4,000 ms; CLS 0.12 is above 0.1" {
		t.Errorf("warnings: %q", got)
	}
	c.MaxLCPMs = 0
	c.MaxDurationMs = 4000
	if got := c.overLimit(r); !strings.HasPrefix(got, "the run took 4,200 ms") {
		t.Errorf("duration: %q", got)
	}
	// Vitals that were not measured are not judged.
	if got := (UptimeCheck{BrowserOptions: BrowserOptions{BrowserLimits: BrowserLimits{MaxTTFBMs: 1}}}).overLimit(uptimeResult{}); got != "" {
		t.Errorf("unmeasured: %q", got)
	}
}

func TestBrowserLimitsRun(t *testing.T) {
	_, u := browserRunner(t)
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<!doctype html><h1>Hello there, this is a page</h1>`)
	}))
	defer site.Close()
	steps := []BrowserStep{{Action: "goto", Value: site.URL}, {Action: "wait", Ms: 300, MaxMs: 100}}

	res, run := playCheck(t, u, UptimeCheck{Steps: steps}, true)
	if run.FailedStep != 1 || !strings.Contains(res.Error, "over its 100 ms limit") || run.Screenshot == nil {
		t.Fatalf("step limit: %q failed=%d", res.Error, run.FailedStep)
	}
	steps[1].MaxMs = 0
	res, _ = playCheck(t, u, UptimeCheck{Steps: steps, BrowserOptions: BrowserOptions{BrowserLimits: BrowserLimits{MaxLCPMs: 1}}}, true)
	if !strings.HasPrefix(res.Error, "LCP ") || !strings.Contains(res.Error, "over the 1 ms limit") {
		t.Fatalf("LCP limit: %q", res.Error)
	}
	res, run = playCheck(t, u, UptimeCheck{Steps: steps, BrowserOptions: BrowserOptions{BrowserLimits: BrowserLimits{WarnLCPMs: 1, MaxLCPMs: 60000}}}, false)
	if !res.Degraded || !strings.HasPrefix(run.Warning, "LCP ") {
		t.Fatalf("LCP warning: %+v %q", res, run.Warning)
	}
}
