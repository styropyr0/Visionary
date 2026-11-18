// Package ratelimit holds the rate-limit rules made in the dashboard
// (Security → Rate limits) and the limiter every Go service runs in its
// middleware. The server validates rules with it too. See "Rate limits" in
// sdks/SPEC.md: the other SDKs implement the same behaviour and run the
// cases in testdata/cases.json.
package ratelimit

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
)

// The visionary_settings rows the server writes and the SDKs poll.
const (
	SettingVersion = "ratelimit.version"
	SettingConfig  = "ratelimit.config"
)

// Limits on what a saved config may hold.
const (
	MaxRules       = 100
	MaxListEntries = 1000
	MaxBody        = 4096
	MaxWindow      = 86400
	MaxCounters    = 10000
	MaxAggregates  = 1000
)

// DefaultCountryHeaders are set by CDNs and edge platforms from the client's IP.
var DefaultCountryHeaders = []string{"CF-IPCountry", "CloudFront-Viewer-Country", "X-Vercel-IP-Country", "Fastly-Geo-Country", "X-AppEngine-Country", "X-Country-Code"}

// Config is the ratelimit.config document.
type Config struct {
	Enabled        *bool    `json:"enabled,omitempty"`
	IPHeader       string   `json:"ip_header,omitempty"`
	CountryHeaders []string `json:"country_headers,omitempty"`
	Rules          []Rule   `json:"rules"`
}

// Rule is one rule as stored. Zero values mean the field is missing.
type Rule struct {
	ID          string              `json:"id"`
	Name        string              `json:"name,omitempty"`
	Enabled     *bool               `json:"enabled,omitempty"`
	Mode        string              `json:"mode,omitempty"`
	Action      string              `json:"action"`
	Services    []string            `json:"services,omitempty"`
	Methods     []string            `json:"methods,omitempty"`
	Paths       []string            `json:"paths,omitempty"`
	IPs         []string            `json:"ips,omitempty"`
	Countries   []string            `json:"countries,omitempty"`
	Match       map[string][]string `json:"match,omitempty"`
	Key         string              `json:"key,omitempty"`
	Limit       float64             `json:"limit,omitempty"`
	WindowS     float64             `json:"window_s,omitempty"`
	Algorithm   string              `json:"algorithm,omitempty"`
	Burst       float64             `json:"burst,omitempty"`
	LimitBy     string              `json:"limit_by,omitempty"`
	Limits      map[string]float64  `json:"limits,omitempty"`
	Status      int                 `json:"status,omitempty"`
	Body        string              `json:"body,omitempty"`
	ContentType string              `json:"content_type,omitempty"`
}

// On reports whether the rule is enabled.
func (r Rule) On() bool { return r.Enabled == nil || *r.Enabled }

// On reports whether the config is enabled.
func (c Config) On() bool { return c.Enabled == nil || *c.Enabled }

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// ValidID reports whether id can name a rule.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// Parse reads a stored document leniently, as the SDKs do: a rule that does
// not decode is dropped, the rest are kept. Only invalid JSON is an error.
func Parse(raw []byte) (Config, error) {
	var doc struct {
		Enabled        *bool             `json:"enabled"`
		IPHeader       string            `json:"ip_header"`
		CountryHeaders []string          `json:"country_headers"`
		Rules          []json.RawMessage `json:"rules"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		var any json.RawMessage
		if json.Unmarshal(raw, &any) != nil {
			return Config{}, err
		}
		// valid JSON with the wrong shape somewhere: keep what decodes
		var loose map[string]json.RawMessage
		_ = json.Unmarshal(raw, &loose)
		_ = json.Unmarshal(loose["enabled"], &doc.Enabled)
		_ = json.Unmarshal(loose["ip_header"], &doc.IPHeader)
		_ = json.Unmarshal(loose["country_headers"], &doc.CountryHeaders)
		_ = json.Unmarshal(loose["rules"], &doc.Rules)
	}
	c := Config{Enabled: doc.Enabled, IPHeader: doc.IPHeader, CountryHeaders: doc.CountryHeaders}
	for _, r := range doc.Rules {
		var rule Rule
		if json.Unmarshal(r, &rule) == nil {
			c.Rules = append(c.Rules, rule)
		}
	}
	return c, nil
}

// Validate is the server's strict check before saving.
func Validate(c Config) error {
	if len(c.Rules) > MaxRules {
		return fmt.Errorf("at most %d rules", MaxRules)
	}
	if strings.ContainsAny(c.IPHeader, " :\r\n") {
		return fmt.Errorf("the IP header is a header name, like CF-Connecting-IP")
	}
	for _, h := range c.CountryHeaders {
		if strings.TrimSpace(h) == "" || strings.ContainsAny(h, " :\r\n") {
			return fmt.Errorf("country headers are header names, like CF-IPCountry")
		}
	}
	seen := map[string]bool{}
	for i, r := range c.Rules {
		label := r.Name
		if label == "" {
			label = fmt.Sprintf("rule %d", i+1)
		}
		if err := validateRule(r); err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		if seen[r.ID] {
			return fmt.Errorf("%s: two rules share the id %s", label, r.ID)
		}
		seen[r.ID] = true
	}
	return nil
}

func validateRule(r Rule) error {
	if !ValidID(r.ID) {
		return fmt.Errorf("the id must be 1 to 32 letters, digits, - or _")
	}
	if len(r.Name) > 120 {
		return fmt.Errorf("the name is at most 120 characters")
	}
	switch r.Mode {
	case "", "enforce", "monitor":
	default:
		return fmt.Errorf("mode is enforce or monitor")
	}
	switch r.Action {
	case "limit", "block", "allow":
	default:
		return fmt.Errorf("action is limit, block or allow")
	}
	for _, list := range [][]string{r.Services, r.Methods, r.Paths, r.IPs, r.Countries} {
		if len(list) > MaxListEntries {
			return fmt.Errorf("at most %d entries in a list", MaxListEntries)
		}
	}
	for _, p := range r.Paths {
		if !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "*") {
			return fmt.Errorf("paths start with / or *: %s", p)
		}
		if strings.Contains(p, "?") {
			return fmt.Errorf("paths have no query string: %s", p)
		}
	}
	for _, ip := range r.IPs {
		if _, ok := parsePrefix(ip); !ok {
			return fmt.Errorf("not an IP or CIDR: %s", ip)
		}
	}
	for _, c := range r.Countries {
		if len(c) != 2 {
			return fmt.Errorf("countries are two-letter codes: %s", c)
		}
	}
	for name, values := range r.Match {
		if _, ok := parseProp(name, false); !ok {
			return fmt.Errorf("unknown property %q", name)
		}
		if len(values) > MaxListEntries {
			return fmt.Errorf("at most %d values for %s", MaxListEntries, name)
		}
	}
	if r.LimitBy != "" {
		if _, ok := parseProp(r.LimitBy, false); !ok {
			return fmt.Errorf("unknown property %q", r.LimitBy)
		}
	}
	if r.Status != 0 && (r.Status < 400 || r.Status > 599) {
		return fmt.Errorf("the status is 400 to 599")
	}
	if len(r.Body) > MaxBody {
		return fmt.Errorf("the body is at most %d bytes", MaxBody)
	}
	if strings.ContainsAny(r.ContentType, "\r\n") {
		return fmt.Errorf("bad content type")
	}
	if r.Action != "limit" {
		return nil
	}
	key := r.Key
	if key == "" {
		key = "ip"
	}
	if _, ok := parseProp(key, true); !ok {
		return fmt.Errorf("unknown key %q", key)
	}
	if r.WindowS < 1 || r.WindowS > MaxWindow {
		return fmt.Errorf("the window is 1 second to 1 day")
	}
	switch r.Algorithm {
	case "", "sliding", "fixed", "token_bucket":
	default:
		return fmt.Errorf("algorithm is sliding, fixed or token_bucket")
	}
	if r.Limit < 0 || r.Limit != float64(int64(r.Limit)) || r.Burst < 0 || r.Burst != float64(int64(r.Burst)) {
		return fmt.Errorf("limit and burst are whole numbers, 0 or more")
	}
	for v, n := range r.Limits {
		if n != float64(int64(n)) {
			return fmt.Errorf("the limit for %s is not a whole number", v)
		}
	}
	return nil
}

func parsePrefix(s string) (netip.Prefix, bool) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, false
		}
		if p.Addr().Is4In6() && p.Bits() >= 96 {
			p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
		}
		return p.Masked(), true
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, false
	}
	a = a.Unmap().WithZone("")
	return netip.PrefixFrom(a, a.BitLen()), true
}
