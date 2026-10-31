package uptimecore

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// BrowserLimits are a check's optional limits, zero is no limit
type BrowserLimits struct {
	MaxLCPMs      int     `json:"max_lcp_ms,omitempty"`
	MaxCLS        float64 `json:"max_cls,omitempty"`
	MaxTTFBMs     int     `json:"max_ttfb_ms,omitempty"`
	MaxDurationMs int     `json:"max_duration_ms,omitempty"`
	WarnLCPMs     int     `json:"warn_lcp_ms,omitempty"`
	WarnCLS       float64 `json:"warn_cls,omitempty"`
	WarnTTFBMs    int     `json:"warn_ttfb_ms,omitempty"`
}

func (l BrowserLimits) Validate(degradedMs int) error {
	for _, v := range []struct {
		name      string
		max, warn int
	}{{"LCP", l.MaxLCPMs, l.WarnLCPMs}, {"TTFB", l.MaxTTFBMs, l.WarnTTFBMs}, {"total time", l.MaxDurationMs, degradedMs}} {
		if v.max < 0 || v.max > MaxBrowserTimeoutMs || v.warn < 0 || v.warn > MaxBrowserTimeoutMs {
			return fmt.Errorf("%s limits must be between 0 and %d ms", v.name, MaxBrowserTimeoutMs)
		}
		if v.max > 0 && v.warn >= v.max {
			return fmt.Errorf("the %s warning must be below its limit", v.name)
		}
	}
	if l.MaxCLS < 0 || l.MaxCLS > 10 || l.WarnCLS < 0 || l.WarnCLS > 10 {
		return errors.New("CLS limits must be between 0 and 10")
	}
	if l.MaxCLS > 0 && l.WarnCLS >= l.MaxCLS {
		return errors.New("the CLS warning must be below its limit")
	}
	return nil
}

func msText(v float64) string { return groupInt(int64(v+0.5)) + " ms" }

func groupInt(n int64) string {
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func clsText(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func (c Check) overLimit(r Result) string {
	l := c.BrowserLimits
	switch {
	case l.MaxDurationMs > 0 && r.DurationMs > float64(l.MaxDurationMs):
		return fmt.Sprintf("the run took %s, over the %s limit", msText(r.DurationMs), msText(float64(l.MaxDurationMs)))
	case l.MaxLCPMs > 0 && r.LCPMs != nil && *r.LCPMs > float64(l.MaxLCPMs):
		return fmt.Sprintf("LCP %s is over the %s limit", msText(*r.LCPMs), msText(float64(l.MaxLCPMs)))
	case l.MaxCLS > 0 && r.CLS != nil && *r.CLS > l.MaxCLS:
		return fmt.Sprintf("CLS %s is over the %s limit", clsText(round3(*r.CLS)), clsText(l.MaxCLS))
	case l.MaxTTFBMs > 0 && r.TTFBMs != nil && *r.TTFBMs > float64(l.MaxTTFBMs):
		return fmt.Sprintf("TTFB %s is over the %s limit", msText(*r.TTFBMs), msText(float64(l.MaxTTFBMs)))
	}
	return ""
}

func (c Check) warnings(r Result) string {
	l := c.BrowserLimits
	var out []string
	if c.DegradedMs > 0 && r.DurationMs > float64(c.DegradedMs) {
		out = append(out, fmt.Sprintf("the run took %s, above %s", msText(r.DurationMs), msText(float64(c.DegradedMs))))
	}
	if l.WarnLCPMs > 0 && r.LCPMs != nil && *r.LCPMs > float64(l.WarnLCPMs) {
		out = append(out, fmt.Sprintf("LCP %s is above %s", msText(*r.LCPMs), msText(float64(l.WarnLCPMs))))
	}
	if l.WarnCLS > 0 && r.CLS != nil && *r.CLS > l.WarnCLS {
		out = append(out, fmt.Sprintf("CLS %s is above %s", clsText(round3(*r.CLS)), clsText(l.WarnCLS)))
	}
	if l.WarnTTFBMs > 0 && r.TTFBMs != nil && *r.TTFBMs > float64(l.WarnTTFBMs) {
		out = append(out, fmt.Sprintf("TTFB %s is above %s", msText(*r.TTFBMs), msText(float64(l.WarnTTFBMs))))
	}
	return clip(strings.Join(out, "; "), 250)
}

func round3(v float64) float64 { return float64(int64(v*1000+0.5)) / 1000 }
