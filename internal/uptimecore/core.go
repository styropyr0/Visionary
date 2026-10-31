// Package uptimecore runs uptime checks: HTTP probes, scripted browser
// checks and the schedule that feeds them. The Visionary server runs checks
// with it from its own location, and the host agent runs it as a location
// runner (VISIONARY_AGENT_LOCATION), so a check behaves the same wherever it
// runs. Storing results is up to the caller.
package uptimecore

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// TimeLayout is how result times are written.
const TimeLayout = "2006-01-02T15:04:05.000Z"

// Mask replaces secret values in anything a run records.
const Mask = "********"

// SecretNameRe matches header and query names whose values are secret.
var SecretNameRe = regexp.MustCompile(`(?i)(auth|token|secret|key|cookie|session|password|signature|credential)`)

// Check is what running a check needs.
type Check struct {
	ID              int64             `json:"id"`
	Name            string            `json:"name"`
	Kind            string            `json:"kind"`
	URL             string            `json:"url"`
	Method          string            `json:"method"`
	ExpectStatus    string            `json:"expect_status"`
	BodyContains    string            `json:"body_contains"`
	Headers         map[string]string `json:"headers"`
	IntervalS       int               `json:"interval_s"`
	TimeoutMs       int               `json:"timeout_ms"`
	DegradedMs      int               `json:"degraded_ms"`
	FollowRedirects bool              `json:"follow_redirects"`
	Steps           []BrowserStep     `json:"steps,omitempty"`
	Secrets         map[string]string `json:"secrets,omitempty"`
	FinalScreenshot bool              `json:"final_screenshot,omitempty"`
	API             *APIConfig        `json:"api,omitempty"`
	TCP             *TCPConfig        `json:"tcp,omitempty"`
	DNS             *DNSConfig        `json:"dns,omitempty"`
	Ping            *PingConfig       `json:"ping,omitempty"`
	SSL             *SSLConfig        `json:"ssl,omitempty"`
}

// Result is one run of a check.
type Result struct {
	CreatedAt     string   `json:"created_at"`
	OK            bool     `json:"ok"`
	Status        int      `json:"status"`
	DurationMs    float64  `json:"duration_ms"`
	Error         string   `json:"error,omitempty"`
	Degraded      bool     `json:"degraded"`
	Retried       bool     `json:"retried"`
	DNSMs         *float64 `json:"dns_ms"`
	ConnectMs     *float64 `json:"connect_ms"`
	TLSMs         *float64 `json:"tls_ms"`
	TTFBMs        *float64 `json:"ttfb_ms"`
	CertExpiresAt string   `json:"cert_expires_at,omitempty"`
	LCPMs         *float64 `json:"lcp_ms,omitempty"`
	CLS           *float64 `json:"cls,omitempty"`
	// Detail is what a run of an api, tcp, dns, ping or ssl check found
	// beyond this row (kinds.go).
	Detail any `json:"detail,omitempty"`
	// Skip says why nothing ran or there is nothing to record yet.
	Skip string `json:"-"`

	blocked bool // refused by the address guard; retrying cannot help
}

// Blocked is whether the address guard refused the run.
func (r Result) Blocked() bool { return r.blocked }

// BrowserStep is one step of a browser check.
type BrowserStep struct {
	Action   string `json:"action"`
	Selector string `json:"selector,omitempty"`
	Text     string `json:"text,omitempty"`
	Value    string `json:"value,omitempty"`
	Count    int    `json:"count,omitempty"`
	Op       string `json:"op,omitempty"`
	Ms       int    `json:"ms,omitempty"`
	Label    string `json:"label,omitempty"`
}

// Describe names a step for results and errors.
func (b BrowserStep) Describe() string { return b.describe() }

func (b BrowserStep) describe() string {
	if b.Label != "" {
		return b.Label
	}
	what := b.Selector
	if what == "" && b.Text != "" {
		what = "“" + b.Text + "”"
	}
	switch b.Action {
	case "goto":
		return "go to " + RedactURL(b.Value)
	case "click":
		return "click " + what
	case "type":
		return "type into " + b.Selector
	case "press":
		return "press " + b.Value
	case "select":
		return "select " + fmt.Sprintf("%q", b.Value) + " in " + b.Selector
	case "wait_selector":
		return "wait for " + what
	case "wait_text":
		return "wait for text “" + b.Text + "”"
	case "wait_navigation":
		return "wait for the next page"
	case "assert_text":
		return "text “" + b.Text + "” is shown"
	case "assert_no_text":
		return "text “" + b.Text + "” is not shown"
	case "assert_url":
		return "URL contains " + fmt.Sprintf("%q", b.Value)
	case "assert_count":
		return fmt.Sprintf("%s %d of %s", opWord(b.Op), b.Count, b.Selector)
	case "wait":
		return fmt.Sprintf("wait %d ms", b.Ms)
	case "script":
		return "script"
	}
	return b.Action
}

// RedactURL drops credentials and secret-looking query values, for anything
// people other than the check's author can read.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	u.User = nil
	if u.RawQuery != "" {
		q := u.Query()
		changed := false
		for k := range q {
			if SecretNameRe.MatchString(k) || strings.EqualFold(k, "sig") {
				q[k] = []string{"REDACTED"}
				changed = true
			}
		}
		if changed {
			u.RawQuery = q.Encode()
		}
	}
	return u.String()
}

// clip trims s and cuts it to at most n bytes on a rune boundary, dropping
// invalid UTF-8 and control characters other than newlines and tabs.
func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	bad := func(r rune) bool { return r < 0x20 && r != '\n' && r != '\t' || r == 0x7f }
	if !utf8.ValidString(s) || strings.ContainsFunc(s, bad) {
		s = strings.Map(func(r rune) rune {
			if r == utf8.RuneError || bad(r) {
				return -1
			}
			return r
		}, s)
	}
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
