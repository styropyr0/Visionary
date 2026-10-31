package analytics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
