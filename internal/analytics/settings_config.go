package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// Settings → Server: the server's options, kept in visionary_settings as
// config.<key> and edited in the dashboard. Only the database connection,
// VISIONARY_ADDR and VISIONARY_ENABLE_AUTH come from the environment.
//
// A value is read as the stored setting, else the default.

type confKind string

const (
	confText     confKind = "text"
	confLong     confKind = "long" // several lines
	confSecret   confKind = "secret"
	confInt      confKind = "int"
	confFloat    confKind = "float"
	confBool     confKind = "bool"
	confDuration confKind = "duration"
	confChoice   confKind = "choice"
)

// confDef is one setting, named by its key.
type confDef struct {
	Key     string   `json:"key"`
	Group   string   `json:"group"`
	Label   string   `json:"label"`
	Help    string   `json:"help"`
	Kind    confKind `json:"kind"`
	Default string   `json:"default"`
	Choices []string `json:"choices,omitempty"`
	// Min and Max bound numbers (and durations, in seconds); 0 is unbounded.
	Min float64 `json:"min,omitempty"`
	Max float64 `json:"max,omitempty"`
	// Restart marks values read once at start.
	Restart bool `json:"restart,omitempty"`
}

const settingConfig = "config."

var confGroups = []struct {
	Key, Label, Help string
}{
	{"analyst", "AI analyst", "The model behind Explore and Ask, and what it should know about your data."},
	{"sessions", "Sessions", "How long sign-ins last and how the server sits behind a proxy."},
	{"email", "Email", "The SMTP server for email alerts and digests."},
	{"alerts", "Alerts", "How often alert rules are checked, and the time zone of digests."},
	{"ingest", "Ingest", "Keys, origins and rate limits for SDKs, OpenTelemetry, host agents and frontend apps. Keys created under API keys work as well."},
	{"security", "Security detectors", "What the request log is scanned for, and how sensitive each detector is."},
	{"other", "Other", ""},
}

var confDefs = []confDef{
	{Key: "openai_api_key", Group: "analyst", Label: "OpenAI API key", Help: "Turns the analyst on. Any OpenAI-compatible API works with its base URL below.", Kind: confSecret},
	{Key: "openai_api_base", Group: "analyst", Label: "API base URL", Kind: confText, Default: "https://api.openai.com/v1"},
	{Key: "model", Group: "analyst", Label: "Model", Kind: confText, Default: "gpt-4.1"},
	{Key: "reasoning", Group: "analyst", Label: "Reasoning effort", Help: "For reasoning models; leave empty for others.", Kind: confChoice, Choices: []string{"", "low", "medium", "high"}},
	{Key: "app_name", Group: "analyst", Label: "Product name", Help: "The product whose API is analysed, used in answers.", Kind: confText, Default: "the application"},
	{Key: "data_notes", Group: "analyst", Label: "Notes about your data", Help: "Anything the analyst should know, such as what each user_role means.", Kind: confLong},
	{Key: "max_rows", Group: "analyst", Label: "Rows per query", Help: "The most rows a query returns.", Kind: confInt, Default: "5000", Min: 1, Max: 100000},
	{Key: "max_range_days", Group: "analyst", Label: "Longest time range (days)", Kind: confInt, Default: "90", Min: 1, Max: 3650},

	{Key: "session_ttl", Group: "sessions", Label: "Session length", Help: "Sign-ins last this long at most, or until the API's token expires.", Kind: confDuration, Default: "12h", Min: 300},
	{Key: "role_check_interval", Group: "sessions", Label: "Re-check roles every", Help: "How often a signed-in user's role is confirmed with your API.", Kind: confDuration, Default: "5m", Min: 30},
	{Key: "trust_proxy", Group: "sessions", Label: "Behind a trusted proxy", Help: "Read the client IP and scheme from X-Forwarded-For and X-Forwarded-Proto.", Kind: confBool, Default: "false"},
	{Key: "secure_cookie", Group: "sessions", Label: "Secure cookie", Help: "auto sets it on HTTPS requests.", Kind: confChoice, Choices: []string{"auto", "true", "false"}, Default: "auto"},

	{Key: "smtp_host", Group: "email", Label: "SMTP host", Kind: confText},
	{Key: "smtp_port", Group: "email", Label: "Port", Kind: confInt, Default: "587", Min: 1, Max: 65535},
	{Key: "smtp_user", Group: "email", Label: "User", Kind: confText},
	{Key: "smtp_password", Group: "email", Label: "Password", Kind: confSecret},
	{Key: "smtp_from", Group: "email", Label: "From address", Help: "Defaults to the user.", Kind: confText},

	{Key: "alerts_interval", Group: "alerts", Label: "Check rules every", Kind: confDuration, Default: "1m", Min: 10, Restart: true},
	{Key: "digest_tz", Group: "alerts", Label: "Digest time zone", Help: "An IANA name such as Asia/Kolkata; UTC when empty.", Kind: confText},

	{Key: "ingest_keys", Group: "ingest", Label: "Server keys", Help: "Comma-separated keys SDKs, OpenTelemetry and host agents may send with.", Kind: confSecret},
	{Key: "client_keys", Group: "ingest", Label: "Client keys", Help: "Comma-separated keys for browser and mobile apps.", Kind: confSecret},
	{Key: "client_origins", Group: "ingest", Label: "Allowed web origins", Help: "Comma-separated, e.g. https://app.example.com. Empty allows any.", Kind: confText, Restart: true},
	{Key: "client_rate_per_ip", Group: "ingest", Label: "Frontend events per IP per minute", Help: "0 turns the limit off.", Kind: confInt, Default: "120", Max: 1000000, Restart: true},
	{Key: "client_rate_per_key", Group: "ingest", Label: "Frontend events per key per minute", Help: "0 turns the limit off.", Kind: confInt, Default: "6000", Max: 10000000, Restart: true},
	{Key: "otlp_rate", Group: "ingest", Label: "OpenTelemetry requests per second", Kind: confInt, Default: "50", Max: 100000, Restart: true},
	{Key: "agent_rate", Group: "ingest", Label: "Host agent reports per second", Kind: confInt, Default: "50", Max: 100000, Restart: true},
	{Key: "spans", Group: "ingest", Label: "Keep OpenTelemetry spans", Kind: confBool, Default: "true", Restart: true},
	{Key: "errors", Group: "ingest", Label: "Keep OpenTelemetry errors", Kind: confBool, Default: "true", Restart: true},
	{Key: "span_threshold_ms", Group: "ingest", Label: "Span threshold outside requests (ms)", Kind: confInt, Default: "100", Max: 600000, Restart: true},
	{Key: "span_max", Group: "ingest", Label: "Spans kept per request", Kind: confInt, Default: "100", Max: 10000, Restart: true},

	{Key: "security", Group: "security", Label: "Detectors on", Kind: confBool, Default: "true", Restart: true},
	{Key: "security_interval", Group: "security", Label: "Scan every", Kind: confDuration, Default: "1m", Min: 10, Restart: true},
	{Key: "security_window", Group: "security", Label: "Look back", Kind: confDuration, Default: "5m", Min: 60, Max: 3600, Restart: true},
	{Key: "security_dedupe", Group: "security", Label: "Repeat a finding after", Kind: confDuration, Default: "10m", Restart: true},
	{Key: "security_bruteforce", Group: "security", Label: "Failed logins from one IP", Kind: confInt, Default: "10", Min: 1, Restart: true},
	{Key: "security_stuffing_accounts", Group: "security", Label: "Accounts tried from one IP", Kind: confInt, Default: "5", Min: 1, Restart: true},
	{Key: "security_targeted_ips", Group: "security", Label: "IPs failing on one account", Kind: confInt, Default: "5", Min: 1, Restart: true},
	{Key: "security_scanner_hits", Group: "security", Label: "Scanner paths from one IP", Kind: confInt, Default: "3", Min: 1, Restart: true},
	{Key: "security_rate_min", Group: "security", Label: "Requests per minute before a rate finding", Kind: confInt, Default: "120", Min: 1, Restart: true},
	{Key: "security_rate_factor", Group: "security", Label: "Times the usual rate", Kind: confFloat, Default: "5", Min: 1, Restart: true},
	{Key: "security_forbidden", Group: "security", Label: "403s from one IP", Kind: confInt, Default: "20", Min: 1, Restart: true},
	{Key: "security_login_routes", Group: "security", Label: "Login routes", Help: "Comma-separated words or routes; empty matches login, signin, auth, token, otp, password and session.", Kind: confText, Restart: true},
	{Key: "security_ignore_ips", Group: "security", Label: "Ignore IPs", Help: "Comma-separated addresses or CIDR ranges, such as your own monitors.", Kind: confText, Restart: true},

	{Key: "rollups", Group: "other", Label: "Chart summaries", Help: "Pre-aggregate requests and system metrics so long ranges load fast.", Kind: confBool, Default: "true", Restart: true},
	{Key: "uptime_allow_private", Group: "other", Label: "Uptime checks may reach private addresses", Kind: confBool, Default: "false", Restart: true},
	{Key: "update_check", Group: "other", Label: "Check for new versions", Kind: confBool, Default: "true"},
}

var confByKey = func() map[string]*confDef {
	m := make(map[string]*confDef, len(confDefs))
	for i := range confDefs {
		m[confDefs[i].Key] = &confDefs[i]
	}
	return m
}()

// conf returns a setting's value: stored, else the default. key must name a
// confDef.
func (s *Server) conf(key string) string {
	d, ok := confByKey[key]
	if !ok {
		panic("analytics: no setting for " + key)
	}
	if v, ok := s.live().Config[key]; ok {
		return v
	}
	return d.Default
}

func (s *Server) maxRows() int      { return s.confInt("max_rows") }
func (s *Server) maxRangeDays() int { return s.confInt("max_range_days") }

func (s *Server) confInt(key string) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s.conf(key))); err == nil {
		return n
	}
	n, _ := strconv.Atoi(confByKey[key].Default)
	return n
}

func (s *Server) confFloat(key string) float64 {
	if f, err := strconv.ParseFloat(strings.TrimSpace(s.conf(key)), 64); err == nil {
		return f
	}
	f, _ := strconv.ParseFloat(confByKey[key].Default, 64)
	return f
}

func (s *Server) confBool(key string) bool {
	if b, err := strconv.ParseBool(strings.TrimSpace(s.conf(key))); err == nil {
		return b
	}
	b, _ := strconv.ParseBool(confByKey[key].Default)
	return b
}

func (s *Server) confDuration(key string) time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(s.conf(key))); err == nil && d > 0 {
		return d
	}
	d, _ := time.ParseDuration(confByKey[key].Default)
	return d
}

// normalize checks a value for d and returns it as stored.
func (d *confDef) normalize(v string) (string, error) {
	v = strings.TrimSpace(v)
	bounds := func(f float64, unit string) error {
		if d.Min != 0 && f < d.Min || d.Max != 0 && f > d.Max || f < 0 {
			lo, hi := strconv.FormatFloat(d.Min, 'f', -1, 64), strconv.FormatFloat(d.Max, 'f', -1, 64)
			if d.Max == 0 {
				return fmt.Errorf("%s is at least %s%s", d.Label, lo, unit)
			}
			return fmt.Errorf("%s is %s to %s%s", d.Label, lo, hi, unit)
		}
		return nil
	}
	switch d.Kind {
	case confInt:
		n, err := strconv.Atoi(v)
		if err != nil {
			return "", fmt.Errorf("%s must be a whole number", d.Label)
		}
		return strconv.Itoa(n), bounds(float64(n), "")
	case confFloat:
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return "", fmt.Errorf("%s must be a number", d.Label)
		}
		return strconv.FormatFloat(f, 'f', -1, 64), bounds(f, "")
	case confBool:
		b, err := strconv.ParseBool(v)
		if err != nil {
			return "", fmt.Errorf("%s must be true or false", d.Label)
		}
		return strconv.FormatBool(b), nil
	case confDuration:
		t, err := time.ParseDuration(v)
		if err != nil || t <= 0 {
			return "", fmt.Errorf("%s must be a duration such as 30s, 5m or 12h", d.Label)
		}
		return t.String(), bounds(t.Seconds(), " seconds")
	case confChoice:
		for _, c := range d.Choices {
			if strings.EqualFold(v, c) {
				return c, nil
			}
		}
		return "", fmt.Errorf("%s must be one of %s", d.Label, strings.Join(d.Choices, ", "))
	}
	if len(v) > 20000 {
		return "", fmt.Errorf("%s is too long", d.Label)
	}
	if d.Key == "security_ignore_ips" {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p == "" {
				continue
			}
			if _, err := netip.ParsePrefix(p); err != nil {
				if _, err := netip.ParseAddr(p); err != nil {
					return "", fmt.Errorf("%q is not an IP address or CIDR range", p)
				}
			}
		}
	}
	if d.Key == "digest_tz" && v != "" {
		if _, err := time.LoadLocation(v); err != nil {
			return "", fmt.Errorf("%q is not a time zone", v)
		}
	}
	return v, nil
}

// confValue is one setting as the dashboard sees it. Secrets are never sent
// back: only whether one is set.
type confValue struct {
	confDef
	Value  string `json:"value"`
	Set    bool   `json:"set"`
	Stored bool   `json:"stored"`
}

func (s *Server) confView() []confValue {
	stored := s.live().Config
	out := make([]confValue, 0, len(confDefs))
	for _, d := range confDefs {
		v := confValue{confDef: d}
		raw, ok := stored[d.Key]
		v.Stored = ok
		if !ok {
			raw = d.Default
		}
		v.Set = raw != ""
		if d.Kind != confSecret {
			v.Value = raw
		}
		out = append(out, v)
	}
	return out
}

func (s *Server) handleConfigSettings(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": confGroups, "settings": s.confView()})
}

// handleRevealSecret shows one stored secret to an admin who asks for it. The
// settings list never carries secrets; each reveal is audited instead.
func (s *Server) handleRevealSecret(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	key := r.PathValue("key")
	if d, ok := confByKey[key]; !ok || d.Kind != confSecret {
		writeError(w, http.StatusNotFound, "no secret setting "+key)
		return
	}
	s.audit(r, "settings.secret_viewed", key, nil)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"value": s.conf(key)})
}

// handlePutConfig saves settings by key; null resets one to its default.
// A secret left out stays as it is.
func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	var body map[string]*string
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	set, reset := map[string]string{}, []string{}
	var changed []string
	for k, v := range body {
		d, ok := confByKey[k]
		if !ok {
			writeError(w, http.StatusBadRequest, "unknown setting "+k)
			return
		}
		if v == nil {
			reset = append(reset, settingConfig+k)
			changed = append(changed, k)
			continue
		}
		val, err := d.normalize(*v)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		set[settingConfig+k] = val
		changed = append(changed, k)
	}
	if err := s.writeSettings(r.Context(), set); err != nil {
		s.fail(w, err)
		return
	}
	if err := s.deleteSettings(r.Context(), reset); err != nil {
		s.fail(w, err)
		return
	}
	// Names only: values may be secrets.
	s.audit(r, "settings.server", "", map[string]any{"changed": changed})
	if err := s.refreshLive(r.Context()); err != nil {
		s.fail(w, err)
		return
	}
	s.applyConfig()
	s.handleConfigSettings(w, r)
}

// applyConfig rebuilds what is made from settings rather than read at each
// use: the analyst's client.
func (s *Server) applyConfig() {
	next := s.newLLM()
	prev := s.llmP.Load()
	if prev != nil && next != nil && *prev == *next {
		return
	}
	switch {
	case prev == nil && next == nil && !s.analystLogged.Swap(true):
		s.logger.Info("the AI analyst is off until an OpenAI API key is added in Settings → Server")
	case prev != nil && next == nil:
		s.logger.Info("the AI analyst is off: its API key was removed")
	case next != nil:
		s.logger.Info("AI analyst", "model", next.model)
	}
	s.llmP.Store(next)
}

// deleteSettings removes rows, so their defaults apply again.
func (s *Server) deleteSettings(ctx context.Context, names []string) error {
	for _, n := range names {
		if err := s.db.Exec(ctx, `DELETE FROM visionary_settings WHERE name = ?`, n); err != nil {
			return dbErr(err)
		}
	}
	return nil
}
