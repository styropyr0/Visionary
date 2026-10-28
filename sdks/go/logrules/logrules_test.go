package logrules

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestTextLevel(t *testing.T) {
	for in, want := range map[string]int{
		"ERROR something":               Error,
		"2026-10-28 WARN disk":          Warn,
		"level=debug msg=x":             Debug,
		"[info] started":                Info,
		"time=x level=WARN msg=y":       Warn,
		"INFO handled":                  Info,
		"payment failed":                0,
		"user error rate is low but ok": 0,
		"error: connection refused":     Error,
		"Error handling request":        0,
		"10:03:44 [ERROR] x":            Error,
		"2026/10/28 10:03:44 FATAL x":   Fatal,
		"<warning> x":                   Warn,
	} {
		if got := TextLevel(in); got != want {
			t.Errorf("%q: %d, want %d", in, got, want)
		}
	}
}

func TestStripLevelWord(t *testing.T) {
	for in, want := range map[string]string{
		"INFO started":     "started",
		"WARNING: disk":    "disk",
		"ERROR":            "ERROR",
		"Error handling":   "Error handling",
		"[warn] slow":      "[warn] slow",
		"WARN":             "WARN",
		"DEBUG   spaced x": "  spaced x",
	} {
		sev := TextLevel(in)
		if sev == 0 {
			sev = Info
		}
		if got := StripLevelWord(in, sev); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestScrub(t *testing.T) {
	in := `login password=hunter2 token: "abc def" Authorization: Bearer abcdefghijkl key AKIAABCDEFGHIJKLMNOP jwt eyJhbGciOiJ.eyJzdWIiOiIx.c2lnbmF0dXJl -----BEGIN RSA PRIVATE KEY-----
MIIE
-----END RSA PRIVATE KEY----- done`
	got := Scrub(in)
	for _, leak := range []string{"hunter2", "abc def", "abcdefghijkl", "AKIAABCDEFGHIJKLMNOP", "eyJhbGciOiJ", "MIIE"} {
		if strings.Contains(got, leak) {
			t.Fatalf("%q leaked in %q", leak, got)
		}
	}
	if !strings.Contains(got, "password=[REDACTED]") || !strings.Contains(got, "Bearer [REDACTED]") || !strings.HasSuffix(got, " done") {
		t.Fatalf("got %q", got)
	}
	if Scrub("plain text") != "plain text" {
		t.Fatal("changed plain text")
	}
}

func TestEncodeAttrs(t *testing.T) {
	s := EncodeAttrs(map[string]any{"user.password": "x", "n": 1, "note": "token=abc", "ssn": "1"}, SecretKeys([]string{"SSN"}))
	if !strings.Contains(s, `"user.password":"[REDACTED]"`) || !strings.Contains(s, `"note":"token=[REDACTED]"`) || !strings.Contains(s, `"ssn":"[REDACTED]"`) {
		t.Fatalf("got %s", s)
	}
	big := map[string]any{}
	for i := range 40 {
		big[strings.Repeat("k", i+1)] = strings.Repeat("v", 2000)
	}
	s = EncodeAttrs(big, nil)
	if len(s) > MaxAttrs || !json.Valid([]byte(s)) {
		t.Fatalf("too big: %d", len(s))
	}
	many := map[string]any{}
	for i := range 2000 {
		many[strings.Repeat("x", 10)+string(rune('a'+i%26))+strings.Repeat("y", i%7)+string(rune('A'+i/26%26))+string(rune('0'+i/676))] = "value"
	}
	s = EncodeAttrs(many, nil)
	if len(s) > MaxAttrs || !json.Valid([]byte(s)) || !strings.Contains(s, `"_truncated":true`) {
		t.Fatalf("many keys: %d bytes", len(s))
	}
}

func TestTruncate(t *testing.T) {
	if got := Truncate("héllo", 2); got != "h" {
		t.Fatalf("%q", got)
	}
	if got := Truncate("abc", 5); got != "abc" {
		t.Fatalf("%q", got)
	}
}

func TestParseAndFloors(t *testing.T) {
	until := time.Now().Add(time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
	past := time.Now().Add(-time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
	ov, _ := json.Marshal([]map[string]string{
		{"route": "/checkout", "level": "debug", "until": until},
		{"user": "42", "level": "trace"},
		{"route": "/health", "level": "warn"},
		{"route": "/admin/*", "level": "debug"},
		{"route": "/old", "level": "debug", "until": past},
		{"service": "other", "level": "debug"},
		{"level": "nonsense"},
		{"route": "/bad", "level": "debug", "until": "tomorrow"},
	})
	s := Parse([][]string{
		{"logs.level", "warning"}, {"logs.overrides", string(ov)}, {"logs.rate", "-1"},
		{"logs.slow_ms", "250"}, {"logs.buffer", "nope"}, {"logs.enabled", "false"},
	}, 0)
	if s.Level != Warn || s.Rate != 6000 || s.Slow != 250*time.Millisecond || !s.Buffer || s.Enabled || len(s.Overrides) != 6 {
		t.Fatalf("parsed %+v", s)
	}
	now := time.Now()
	for _, c := range []struct {
		route, user string
		want        int
	}{
		{"/checkout", "", Debug},
		{"/health", "", Warn},
		{"/admin/users", "", Debug},
		{"/old", "", Warn},
		{"/any", "42", Trace},
		{"/health", "42", Trace},
		{"/any", "", Warn},
	} {
		if got := s.Floor("api", c.route, c.user, now); got != c.want {
			t.Errorf("floor %s %s: %d, want %d", c.route, c.user, got, c.want)
		}
	}
	if got := s.Floor("other", "", "", now); got != Debug {
		t.Errorf("service override: %d", got)
	}
	if got := s.HoldBelow("api", now); got != Warn {
		t.Errorf("hold below: %d", got)
	}
}

func TestDropped(t *testing.T) {
	drops, _ := json.Marshal([]map[string]string{{"contains": "healthcheck"}, {"logger": "noisy", "level": "info"}, {"service": "web", "level": "debug"}, {}})
	s := Parse([][]string{{"logs.drop", string(drops)}}, Info)
	if len(s.Drops) != 3 {
		t.Fatalf("%d rules", len(s.Drops))
	}
	for _, c := range []struct {
		service, logger, msg string
		sev                  int
		want                 bool
	}{
		{"api", "", "healthcheck ok", Error, true},
		{"api", "noisy", "x", Info, true},
		{"api", "noisy", "x", Warn, false},
		{"web", "", "x", Debug, true},
		{"web", "", "x", Info, false},
		{"api", "", "x", Trace, false},
	} {
		if got := s.Dropped(c.service, c.logger, c.msg, c.sev); got != c.want {
			t.Errorf("%+v: %v", c, got)
		}
	}
}

func TestParseLevel(t *testing.T) {
	for in, want := range map[string]int{"WARNING": Warn, "crit": Fatal, "notice": Info, "Severe": Error, "": 0, "loud": 0} {
		if got := ParseLevel(in); got != want {
			t.Errorf("%q: %d", in, got)
		}
	}
	if Name(0) != "info" || Name(Fatal) != "fatal" || Clamp(9) != Fatal || Clamp(-1) != Trace {
		t.Fatal("names")
	}
}
