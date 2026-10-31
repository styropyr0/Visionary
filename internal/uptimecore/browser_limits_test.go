package uptimecore

import (
	"strings"
	"testing"
)

func TestBrowserLimits(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	r := Result{OK: true, DurationMs: 4200, LCPMs: f(2600), CLS: f(0.12), TTFBMs: f(300)}
	c := Check{DegradedMs: 4000, BrowserOptions: BrowserOptions{BrowserLimits: BrowserLimits{MaxLCPMs: 2500, WarnCLS: 0.1}}}
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
	if got := (Check{BrowserOptions: BrowserOptions{BrowserLimits: BrowserLimits{MaxTTFBMs: 1}}}).overLimit(Result{}); got != "" {
		t.Errorf("unmeasured: %q", got)
	}
	if err := (BrowserLimits{MaxLCPMs: 2000, WarnLCPMs: 2500}).Validate(0); err == nil {
		t.Error("warning above the limit accepted")
	}
}

func TestBrowserDevice(t *testing.T) {
	o := BrowserOptions{}
	if err := o.NormalizeDevice(); err != nil || o.device().W != 1280 {
		t.Fatalf("default: %v %+v", err, o)
	}
	o = BrowserOptions{Device: "phone", ViewportW: 999}
	if err := o.NormalizeDevice(); err != nil || o.ViewportW != 0 || !o.device().Mobile || o.device().Scale != 3 {
		t.Fatalf("phone: %v %+v", err, o)
	}
	o = BrowserOptions{Device: "custom", ViewportW: 1000, ViewportH: 700}
	if err := o.NormalizeDevice(); err != nil || o.device().W != 1000 {
		t.Fatalf("custom: %v %+v", err, o)
	}
}
