package analytics

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	sdk "github.com/styropyr0/visionary/sdks/go"
	"github.com/styropyr0/visionary/sdks/go/ratelimit"
)

// Rate limits (Security → Rate limits): rules the SDKs enforce in their
// middleware. The server only stores them, in visionary_settings as
// ratelimit.config with a ratelimit.version the SDKs poll (sdks/SPEC.md,
// "Rate limits"), and reads back what the SDKs recorded in
// rate_limit_events: one row per rule, outcome, key and IP per SDK flush.

func (s *Server) rateLimitSchema() []string {
	id, key, ts := "INTEGER PRIMARY KEY AUTOINCREMENT", "TEXT", "TEXT"
	tail, index := "", "CREATE INDEX IF NOT EXISTS"
	switch s.q.d {
	case sdk.Postgres:
		id = "BIGSERIAL PRIMARY KEY"
	case sdk.MySQL:
		id, key, ts = "BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY", "VARCHAR(191)", "VARCHAR(32)"
		tail, index = " CHARACTER SET utf8mb4", "CREATE INDEX"
	}
	ddl := `CREATE TABLE IF NOT EXISTS rate_limit_events (
    id         {id},
    created_at {ts} NOT NULL,
    service    {key},
    instance   TEXT,
    rule_id    {key} NOT NULL,
    rule_name  TEXT,
    outcome    {key} NOT NULL,
    key_value  TEXT,
    ip         {key},
    country    TEXT,
    method     TEXT,
    path       TEXT,
    user_id    TEXT,
    hits       INTEGER NOT NULL DEFAULT 1
){tail};
{index} idx_rate_limit_events_created_at ON rate_limit_events (created_at);
{index} idx_rate_limit_events_rule ON rate_limit_events (rule_id, created_at)`
	return []string{strings.NewReplacer("{id}", id, "{key}", key, "{ts}", ts, "{tail}", tail, "{index}", index).Replace(ddl)}
}

func (s *Server) rateLimitRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/ratelimits", s.authed(s.handleRateLimits))
	mux.Handle("PUT /api/ratelimits", s.authed(s.handlePutRateLimits))
	mux.Handle("GET /api/ratelimits/activity", s.authed(s.handleRateLimitActivity))
}

type rateLimitsView struct {
	Config    ratelimit.Config `json:"config"`
	Version   string           `json:"version"`
	UpdatedAt string           `json:"updated_at"`
	CanEdit   bool             `json:"can_edit"`
	// RouteLimits are the detector's Route limits, offered as rules to enforce.
	RouteLimits    []routeLimitView `json:"route_limits"`
	CountryHeaders []string         `json:"default_country_headers"`
	// AppBlocks counts the addresses Security blocks through managed rules,
	// which are left out of Config
	AppBlocks int `json:"app_blocks"`
}

type routeLimitView struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	PerMin int    `json:"per_min"`
}

func (s *Server) loadRateLimits(ctx context.Context) (ratelimit.Config, string, string, error) {
	rows, err := s.db.Query(ctx, `SELECT name, value, updated_at FROM visionary_settings WHERE name IN (?, ?)`, ratelimit.SettingConfig, ratelimit.SettingVersion)
	if err != nil {
		return ratelimit.Config{}, "", "", dbErr(err)
	}
	var cfg ratelimit.Config
	version, updated := "", ""
	for _, r := range rows.Rows {
		switch toString(r[0]) {
		case ratelimit.SettingConfig:
			cfg, _ = ratelimit.Parse([]byte(toString(r[1])))
			updated = toString(r[2])
		case ratelimit.SettingVersion:
			version = toString(r[1])
		}
	}
	if cfg.Rules == nil {
		cfg.Rules = []ratelimit.Rule{}
	}
	return cfg, version, updated, nil
}

func (s *Server) rateLimitsView(r *http.Request) (rateLimitsView, error) {
	cfg, version, updated, err := s.loadRateLimits(r.Context())
	if err != nil {
		return rateLimitsView{}, err
	}
	v := rateLimitsView{Config: cfg, Version: version, UpdatedAt: updated, CanEdit: s.can(r, "admin") && scopeFrom(r.Context()) == nil,
		RouteLimits: []routeLimitView{}, CountryHeaders: ratelimit.DefaultCountryHeaders}
	kept := v.Config.Rules[:0:0]
	for _, rule := range v.Config.Rules {
		if isBlockRule(rule.ID) {
			v.AppBlocks += len(rule.IPs)
		} else {
			kept = append(kept, rule)
		}
	}
	v.Config.Rules = kept
	for _, rl := range parseRouteLimits(s.conf("security_route_limits")) {
		v.RouteLimits = append(v.RouteLimits, routeLimitView{Method: rl.method, Path: rl.glob, PerMin: rl.perMin})
	}
	return v, nil
}

func (s *Server) handleRateLimits(w http.ResponseWriter, r *http.Request) {
	v, err := s.rateLimitsView(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// handlePutRateLimits saves the whole rule list; services pick it up within
// about 15 seconds.
func (s *Server) handlePutRateLimits(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") || !s.requireAllServices(w, r) {
		return
	}
	var cfg ratelimit.Config
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&cfg); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	normalizeRateLimits(&cfg)
	current, _, _, err := s.loadRateLimits(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	var blocked []string
	for _, rule := range current.Rules {
		if isBlockRule(rule.ID) {
			blocked = append(blocked, rule.IPs...)
		}
	}
	cfg.Rules = withBlockRules(cfg.Rules, blocked)
	if err := ratelimit.Validate(cfg); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	raw, _ := json.Marshal(cfg)
	if err := s.writeSettings(r.Context(), map[string]string{ratelimit.SettingConfig: string(raw), ratelimit.SettingVersion: newRateLimitVersion()}); err != nil {
		s.fail(w, err)
		return
	}
	on := 0
	for _, rule := range cfg.Rules {
		if rule.On() {
			on++
		}
	}
	s.audit(r, "ratelimits.update", "", map[string]any{"rules": len(cfg.Rules), "enabled": on, "on": cfg.On()})
	s.handleRateLimits(w, r)
}

// normalizeRateLimits trims what people type and gives new rules an id.
func normalizeRateLimits(c *ratelimit.Config) {
	c.IPHeader = strings.TrimSpace(c.IPHeader)
	headers := c.CountryHeaders[:0]
	for _, h := range c.CountryHeaders {
		if h = strings.TrimSpace(h); h != "" {
			headers = append(headers, h)
		}
	}
	c.CountryHeaders = headers
	if c.Enabled != nil && *c.Enabled {
		c.Enabled = nil
	}
	for i := range c.Rules {
		r := &c.Rules[i]
		if r.ID == "" {
			r.ID = newRuleID()
		}
		r.Name = strings.TrimSpace(r.Name)
		if r.Enabled != nil && *r.Enabled {
			r.Enabled = nil
		}
		if r.Mode == "enforce" {
			r.Mode = ""
		}
		r.Services = cleanList(r.Services, false)
		r.Methods = cleanList(r.Methods, true)
		r.Paths = cleanList(r.Paths, false)
		r.IPs = cleanList(r.IPs, false)
		r.Countries = cleanList(r.Countries, true)
		for k, v := range r.Match {
			r.Match[k] = cleanList(v, false)
		}
		if len(r.Match) == 0 {
			r.Match = nil
		}
		if r.Action != "limit" {
			r.Key, r.Limit, r.WindowS, r.Algorithm, r.Burst, r.LimitBy, r.Limits = "", 0, 0, "", 0, "", nil
			continue
		}
		if r.Key == "ip" {
			r.Key = ""
		}
		if r.Algorithm == "sliding" {
			r.Algorithm = ""
		}
		if r.Algorithm != "token_bucket" {
			r.Burst = 0
		}
		if r.LimitBy == "" || len(r.Limits) == 0 {
			r.LimitBy, r.Limits = "", nil
		}
	}
}

func cleanList(in []string, upper bool) []string {
	var out []string
	seen := map[string]bool{}
	for _, v := range in {
		v = strings.TrimSpace(v)
		if upper {
			v = strings.ToUpper(v)
		}
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func newRuleID() string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func newRateLimitVersion() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return time.Now().UTC().Format(tsLayout) + "-" + hex.EncodeToString(b)
}

// handleRateLimitActivity sums what the SDKs recorded in the range: per
// rule and outcome, over time, by client, and the latest rows.
func (s *Server) handleRateLimitActivity(w http.ResponseWriter, r *http.Request) {
	tr, err := parseRange(r.URL.Query(), s.maxRangeDays())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	where := "created_at >= ? AND created_at < ?"
	args := []any{tr.From.Format(tsLayout), tr.To.Format(tsLayout)}
	if rule := strings.TrimSpace(r.URL.Query().Get("rule")); rule != "" {
		if !ratelimit.ValidID(rule) {
			writeError(w, http.StatusBadRequest, "invalid rule")
			return
		}
		where += " AND rule_id = ?"
		args = append(args, rule)
	}
	ctx := r.Context()
	out := map[string]any{"rules": []any{}, "series": []any{}, "clients": []any{}, "recent": []any{}, "bucket": tr.Bucket}
	enforced := "outcome IN ('limited', 'blocked')"

	byRule, err := s.db.Query(ctx, `SELECT rule_id, MAX(rule_name) AS rule_name,
  COALESCE(SUM(CASE WHEN outcome = 'limited' THEN hits END), 0) AS limited,
  COALESCE(SUM(CASE WHEN outcome = 'blocked' THEN hits END), 0) AS blocked,
  COALESCE(SUM(CASE WHEN outcome = 'would_limit' THEN hits END), 0) AS would_limit,
  COALESCE(SUM(CASE WHEN outcome = 'would_block' THEN hits END), 0) AS would_block,
  COUNT(DISTINCT ip) AS ips, MAX(created_at) AS last
FROM rate_limit_events WHERE `+where+` GROUP BY rule_id ORDER BY 3 DESC, 4 DESC LIMIT 200`, args...)
	if err != nil {
		if missingTable(err, "rate_limit_events") {
			writeJSON(w, http.StatusOK, out)
			return
		}
		s.fail(w, err)
		return
	}
	rules := []map[string]any{}
	for _, row := range byRule.Rows {
		rules = append(rules, map[string]any{"rule_id": toString(row[0]), "rule_name": toString(row[1]), "limited": toInt(row[2]), "blocked": toInt(row[3]),
			"would_limit": toInt(row[4]), "would_block": toInt(row[5]), "ips": toInt(row[6]), "last": toString(row[7])})
	}
	out["rules"] = rules

	series, err := s.db.Query(ctx, tr.render(`SELECT {{t}} AS t,
  COALESCE(SUM(CASE WHEN `+enforced+` THEN hits END), 0) AS enforced,
  COALESCE(SUM(CASE WHEN outcome IN ('would_limit', 'would_block') THEN hits END), 0) AS monitored
FROM rate_limit_events WHERE `+where+` GROUP BY 1 ORDER BY 1`, s.q), args...)
	if err != nil {
		s.fail(w, err)
		return
	}
	points := []map[string]any{}
	for _, row := range series.Rows {
		points = append(points, map[string]any{"t": toInt(row[0]), "enforced": toInt(row[1]), "monitored": toInt(row[2])})
	}
	out["series"] = points

	clients, err := s.db.Query(ctx, `SELECT ip, MAX(country) AS country, MAX(user_id) AS user_id, SUM(hits) AS hits,
  COALESCE(SUM(CASE WHEN `+enforced+` THEN hits END), 0) AS enforced, COUNT(DISTINCT rule_id) AS rules, MAX(created_at) AS last
FROM rate_limit_events WHERE `+where+` AND ip IS NOT NULL AND ip <> '' GROUP BY ip ORDER BY 4 DESC LIMIT 20`, args...)
	if err != nil {
		s.fail(w, err)
		return
	}
	top := []map[string]any{}
	for _, row := range clients.Rows {
		c := map[string]any{"ip": toString(row[0]), "country": toString(row[1]), "user_id": toString(row[2]), "hits": toInt(row[3]),
			"enforced": toInt(row[4]), "rules": toInt(row[5]), "last": toString(row[6])}
		if g := s.lookupIP(toString(row[0])); g.Country != "" || g.Org != "" {
			c["place"], c["org"] = g.place(), g.Org
			if c["country"] == "" {
				c["country"] = g.Country
			}
		}
		top = append(top, c)
	}
	out["clients"] = top

	recent, err := s.db.Query(ctx, `SELECT created_at, service, instance, rule_id, rule_name, outcome, key_value, ip, country, method, path, user_id, hits
FROM rate_limit_events WHERE `+where+` ORDER BY created_at DESC LIMIT 100`, args...)
	if err != nil {
		s.fail(w, err)
		return
	}
	rows := []map[string]any{}
	for _, row := range recent.Rows {
		rows = append(rows, map[string]any{"created_at": toString(row[0]), "service": toString(row[1]), "instance": toString(row[2]), "rule_id": toString(row[3]),
			"rule_name": toString(row[4]), "outcome": toString(row[5]), "key_value": toString(row[6]), "ip": toString(row[7]), "country": toString(row[8]),
			"method": toString(row[9]), "path": toString(row[10]), "user_id": toString(row[11]), "hits": toInt(row[12])})
	}
	out["recent"] = rows
	writeJSON(w, http.StatusOK, out)
}

// detectRateLimited turns enforced rate limits into findings: an IP the
// SDKs kept refusing in the window, so it shows in Findings and security
// alerts can fire on it.
func (s *Server) detectRateLimited(ctx context.Context, from, to, cursor time.Time) ([]finding, error) {
	st := &s.security().st
	cur := cursor.Format(tsLayout)
	res, err := s.db.Query(ctx, `SELECT ip, MAX(service) AS service, MAX(user_id) AS user_id, SUM(hits) AS n,
  COALESCE(SUM(CASE WHEN created_at >= ? THEN hits END), 0) AS n_new,
  COALESCE(SUM(CASE WHEN outcome = 'blocked' THEN hits END), 0) AS blocked,
  COUNT(DISTINCT rule_id) AS rules, MAX(rule_name) AS rule_name
FROM rate_limit_events WHERE created_at >= ? AND created_at < ? AND outcome IN ('limited', 'blocked') AND ip IS NOT NULL AND ip <> ''
GROUP BY ip HAVING SUM(hits) >= ? ORDER BY 4 DESC LIMIT 200`, cur, from.Format(tsLayout), to.Format(tsLayout), st.rateLimitedHits)
	if err != nil {
		if missingTable(err, "rate_limit_events") {
			return nil, nil
		}
		return nil, err
	}
	win := fmtSpan(st.window)
	var out []finding
	for _, row := range res.Rows {
		if len(row) != 8 || st.ignored(toString(row[0])) {
			continue
		}
		ip, n, nNew, blocked, rules := toString(row[0]), toInt(row[3]), toInt(row[4]), toInt(row[5]), toInt(row[6])
		what := fmt.Sprintf("rule “%s”", toString(row[7]))
		if rules > 1 {
			what = fmt.Sprintf("%d rate-limit rules", rules)
		}
		out = append(out, finding{kind: "rate_limited", severity: grade(n, st.rateLimitedHits, false), ip: ip, actor: toString(row[2]),
			service: toString(row[1]), n: n, nNew: nNew,
			detail: map[string]any{"refused": n, "blocked": blocked, "rules": rules, "window": win,
				"summary": fmt.Sprintf("%s was refused %s times by %s in %s.", ip, fnum(float64(n)), what, win)}})
	}
	return out, nil
}
