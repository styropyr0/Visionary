// Package logrules holds the log rules shared by the Go SDK and the Visionary
// server, so both store the same thing. Other SDKs follow sdks/SPEC.md.
package logrules

import (
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Severities as stored in app_logs.severity.
const (
	Trace = 1 + iota
	Debug
	Info
	Warn
	Error
	Fatal
)

const (
	// MaxMessage and MaxAttrs cap a line's message and its encoded fields.
	MaxMessage = 8 << 10
	MaxAttrs   = 16 << 10
	// Redacted replaces a secret value.
	Redacted = "[REDACTED]"
)

var levelNames = [...]string{"", "trace", "debug", "info", "warn", "error", "fatal"}

// Name is a severity's level name; out-of-range severities are info.
func Name(sev int) string {
	if sev < Trace || sev > Fatal {
		return "info"
	}
	return levelNames[sev]
}

// ParseLevel reads a level name in its usual spellings (warn, warning, err...).
// Unknown names are 0.
func ParseLevel(s string) int {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "trace", "trc", "finest", "finer", "verbose", "vrb":
		return Trace
	case "debug", "dbg", "fine", "config":
		return Debug
	case "info", "inf", "information", "informational", "notice", "log":
		return Info
	case "warn", "warning", "wrn":
		return Warn
	case "error", "err", "eror", "severe":
		return Error
	case "fatal", "critical", "crit", "alert", "emerg", "emergency", "panic", "ftl":
		return Fatal
	}
	return 0
}

// Clamp keeps a severity in range, 0 becoming info.
func Clamp(sev int) int {
	return min(max(sev, Trace), Fatal)
}

// TextLevel finds the level a plain-text line states near its start ("ERROR ...",
// "[warn]", "level=debug"), or 0. A bare lowercase "error rate is low" is not one.
func TextLevel(s string) int {
	head := s[:min(len(s), 200)]
	if m := levelFieldRE.FindStringSubmatch(head); m != nil {
		if n := ParseLevel(m[1]); n > 0 {
			return n
		}
	}
	fields := strings.Fields(head)
	for i, f := range fields {
		if i > 3 {
			break
		}
		if timestampRE.MatchString(f) {
			continue
		}
		word := strings.Trim(f, "[]<>():|")
		n := ParseLevel(word)
		if n == 0 {
			return 0
		}
		marked := strings.HasPrefix(f, "[") || strings.HasPrefix(f, "<") || strings.HasPrefix(f, "(") ||
			strings.HasSuffix(f, ":") || strings.HasSuffix(f, "]") || strings.HasSuffix(f, "|")
		if marked || word == strings.ToUpper(word) {
			return n
		}
		return 0
	}
	return 0
}

var (
	levelFieldRE = regexp.MustCompile(`(?i)\b(?:level|lvl|severity)=["']?([a-z]+)`)
	timestampRE  = regexp.MustCompile(`^[\[(]?[0-9][0-9/:.,\-TZ+]*[\])]?$`)
)

// StripLevelWord removes the leading level word TextLevel read as sev
// ("WARNING: disk" → "disk"), since the level has its own column.
func StripLevelWord(s string, sev int) string {
	word, rest, ok := strings.Cut(s, " ")
	if !ok || strings.TrimSpace(rest) == "" || word != strings.ToUpper(word) {
		return s
	}
	w := strings.ToLower(strings.TrimSuffix(word, ":"))
	if w == Name(sev) || w == "warning" {
		return rest
	}
	return s
}

// Secrets in free text, replaced before a line is stored.
var secretPatterns = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(-----END [A-Z ]*PRIVATE KEY-----|$)`), "[REDACTED:private-key]"},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}`), "[REDACTED:jwt]"},
	{regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`), "$1 [REDACTED]"},
	{regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`), "[REDACTED:aws-key]"},
	{regexp.MustCompile(`(?i)\b(password|passwd|pwd|secret|token|api[_-]?key|access[_-]?token|refresh[_-]?token|client[_-]?secret)\b(["']?\s*[=:]\s*)("[^"]*"|'[^']*'|[^\s,;&"']+)`), "$1$2[REDACTED]"},
}

// Scrub replaces credentials in s.
func Scrub(s string) string {
	for _, p := range secretPatterns {
		if p.re.MatchString(s) {
			s = p.re.ReplaceAllString(s, p.with)
		}
	}
	return s
}

// DefaultSecretKeys are field names whose values are never stored.
var DefaultSecretKeys = []string{
	"password", "passwd", "secret", "client_secret", "token", "access_token", "refresh_token", "id_token",
	"api_key", "apikey", "otp", "pin", "card_number", "cvv", "private_key",
}

// SecretKeys is a lookup of DefaultSecretKeys plus extra, lower case.
func SecretKeys(extra []string) map[string]bool {
	m := map[string]bool{}
	for _, f := range append(append([]string{}, DefaultSecretKeys...), extra...) {
		if f = strings.ToLower(strings.TrimSpace(f)); f != "" {
			m[f] = true
		}
	}
	return m
}

// EncodeAttrs writes attrs as one flat JSON object with secrets redacted, capped
// at MaxAttrs bytes (long values are cut first, then keys dropped).
func EncodeAttrs(attrs map[string]any, secret map[string]bool) string {
	out := make(map[string]any, len(attrs))
	for k, v := range attrs {
		if secret[strings.ToLower(lastKey(k))] {
			out[k] = Redacted
			continue
		}
		if s, ok := v.(string); ok {
			v = Scrub(s)
		}
		out[k] = v
	}
	b, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	if len(b) <= MaxAttrs {
		return string(b)
	}
	for k, v := range out {
		if s, ok := v.(string); ok && len(s) > 1024 {
			out[k] = Truncate(s, 1024) + "…"
		}
	}
	if b, err = json.Marshal(out); err == nil && len(b) <= MaxAttrs {
		return string(b)
	}
	keys := make([]string, 0, len(out))
	for k := range out {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	kept := map[string]any{"_truncated": true}
	for _, k := range keys {
		kept[k] = out[k]
		if b, _ := json.Marshal(kept); len(b) > MaxAttrs {
			delete(kept, k)
			break
		}
	}
	b, _ = json.Marshal(kept)
	return string(b)
}

func lastKey(k string) string {
	if i := strings.LastIndexByte(k, '.'); i >= 0 {
		return k[i+1:]
	}
	return k
}

// Truncate cuts s to at most n bytes without splitting a UTF-8 sequence.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

// Override changes the level for part of the traffic until a time.
type Override struct {
	Service string `json:"service"`
	Route   string `json:"route"`
	User    string `json:"user"`
	Level   string `json:"level"`
	Until   string `json:"until"`

	sev   int
	until time.Time
}

// Drop drops matching lines. Empty fields match anything; a set Level drops
// only lines at or below it.
type Drop struct {
	Service  string `json:"service"`
	Logger   string `json:"logger"`
	Contains string `json:"contains"`
	Level    string `json:"level"`

	sev int
}

// Settings is what Settings → Logs asks for (visionary_settings logs.*).
type Settings struct {
	Enabled     bool
	Level       int
	Buffer      bool
	BufferLevel int
	Slow        time.Duration
	Rate        int
	Overrides   []Override
	Drops       []Drop
}

// Defaults are the settings before any are read; level 0 means info.
func Defaults(level int) *Settings {
	if level == 0 {
		level = Info
	}
	return &Settings{Enabled: true, Level: level, Buffer: true, BufferLevel: Debug, Slow: time.Second, Rate: 6000}
}

// Parse reads (name, value) rows of logs.*; bad or missing ones keep their default.
func Parse(rows [][]string, base int) *Settings {
	s := Defaults(base)
	for _, r := range rows {
		if len(r) < 2 {
			continue
		}
		v := strings.TrimSpace(r[1])
		switch r[0] {
		case "logs.enabled":
			if b, err := strconv.ParseBool(v); err == nil {
				s.Enabled = b
			}
		case "logs.level":
			if n := ParseLevel(v); n > 0 {
				s.Level = n
			}
		case "logs.buffer":
			if b, err := strconv.ParseBool(v); err == nil {
				s.Buffer = b
			}
		case "logs.buffer_level":
			if n := ParseLevel(v); n > 0 {
				s.BufferLevel = n
			}
		case "logs.slow_ms":
			if n, err := strconv.ParseFloat(v, 64); err == nil && n > 0 {
				s.Slow = time.Duration(n * float64(time.Millisecond))
			}
		case "logs.rate":
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				s.Rate = n
			}
		case "logs.overrides":
			var list []Override
			if json.Unmarshal([]byte(v), &list) == nil {
				for _, o := range list {
					if o.sev = ParseLevel(o.Level); o.sev == 0 {
						continue
					}
					if o.Until != "" {
						t, ok := ParseTime(o.Until)
						if !ok {
							continue
						}
						o.until = t
					}
					s.Overrides = append(s.Overrides, o)
				}
			}
		case "logs.drop":
			var list []Drop
			if json.Unmarshal([]byte(v), &list) == nil {
				for _, d := range list {
					d.sev = ParseLevel(d.Level)
					if d.Service == "" && d.Logger == "" && d.Contains == "" && d.sev == 0 {
						continue
					}
					s.Drops = append(s.Drops, d)
				}
			}
		}
	}
	return s
}

var timeLayouts = []string{"2006-01-02T15:04:05.000Z", time.RFC3339Nano, "2006-01-02 15:04:05.000", "2006-01-02 15:04:05", "2006-01-02T15:04:05"}

// ParseTime reads a timestamp in the formats Visionary writes.
func ParseTime(v string) (time.Time, bool) {
	for _, l := range timeLayouts {
		if t, err := time.Parse(l, v); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func (o *Override) active(service string, now time.Time) bool {
	if !o.until.IsZero() && now.After(o.until) {
		return false
	}
	return o.Service == "" || o.Service == service
}

// An override with no route or user applies to its whole service.
func (o *Override) matches(route, user string) bool {
	if o.Route != "" && !RouteMatches(o.Route, route) {
		return false
	}
	return o.User == "" || o.User == user
}

// RouteMatches compares a route pattern, allowing a trailing * for prefixes.
func RouteMatches(pattern, route string) bool {
	if p, ok := strings.CutSuffix(pattern, "*"); ok {
		return strings.HasPrefix(route, p)
	}
	return pattern == route
}

// Floor is the lowest level kept for service on route by user. The most
// specific override wins (a user beats a route), later ones among equals.
func (s *Settings) Floor(service, route, user string, now time.Time) int {
	level, rank := s.Level, 0
	for i := range s.Overrides {
		o := &s.Overrides[i]
		if !o.active(service, now) || !o.matches(route, user) {
			continue
		}
		r := 1
		if o.Route != "" {
			r = 2
		}
		if o.User != "" {
			r = 3
		}
		if r >= rank {
			level, rank = o.sev, r
		}
	}
	return level
}

// HoldBelow is the level under which request lines are held until the
// request ends, since an override or the buffer might still keep them.
func (s *Settings) HoldBelow(service string, now time.Time) int {
	level := s.Level
	for i := range s.Overrides {
		o := &s.Overrides[i]
		if o.active(service, now) && o.Route == "" && o.User == "" {
			level = o.sev
		}
	}
	for i := range s.Overrides {
		o := &s.Overrides[i]
		if o.active(service, now) && (o.Route != "" || o.User != "") {
			level = max(level, o.sev)
		}
	}
	return level
}

// Dropped reports whether a drop rule matches the line.
func (s *Settings) Dropped(service, logger, message string, sev int) bool {
	for _, d := range s.Drops {
		if d.Service != "" && d.Service != service {
			continue
		}
		if d.Logger != "" && d.Logger != logger {
			continue
		}
		if d.sev != 0 && sev > d.sev {
			continue
		}
		if d.Contains != "" && !strings.Contains(message, d.Contains) {
			continue
		}
		return true
	}
	return false
}
