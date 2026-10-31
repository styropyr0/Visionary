package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"

	sdk "github.com/styropyr0/visionary/sdks/go"

	"github.com/styropyr0/visionary/internal/uptimecore"
)

// Check kinds beyond http and browser: API (uptime_api.go), TCP, DNS and SSL
// (uptime_net.go), ping (uptime_ping.go) and heartbeat (uptime_heartbeat.go).
// Settings live in uptime_check_settings, per-run findings in uptime_run_details.

var settingsKinds = map[string]bool{"api": true, "tcp": true, "dns": true, "ping": true, "ssl": true, "heartbeat": true}

const (
	kindMaxTimeoutMs = 120000
	detailsMaxRuns   = 500
	detailsMaxBytes  = 64 << 10
)

// The kinds' settings are uptimecore's, which runs them for the server and
// for location runners alike.
type (
	APIConfig    = uptimecore.APIConfig
	APIRequest   = uptimecore.APIRequest
	APIExtract   = uptimecore.APIExtract
	APIAssertion = uptimecore.APIAssertion
	TCPConfig    = uptimecore.TCPConfig
	DNSConfig    = uptimecore.DNSConfig
	PingConfig   = uptimecore.PingConfig
	SSLConfig    = uptimecore.SSLConfig
)

func (s *Server) uptimeKindsSchema() []string {
	id, key, ts, big, num, tok, real := "INTEGER PRIMARY KEY AUTOINCREMENT", "INTEGER", "TEXT", "TEXT", "INTEGER", "TEXT", "REAL"
	ine := "IF NOT EXISTS "
	switch s.q.d {
	case sdk.Postgres:
		id, key, real = "BIGSERIAL PRIMARY KEY", "BIGINT", "DOUBLE PRECISION"
	case sdk.MySQL:
		id, key, ts, big, num, tok, real, ine = "BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY", "BIGINT", "VARCHAR(32)", "MEDIUMTEXT", "INT", "VARCHAR(64)", "DOUBLE", ""
	}
	tail := ""
	if s.q.d == sdk.MySQL {
		tail = " CHARACTER SET utf8mb4"
	}
	return []string{`CREATE TABLE IF NOT EXISTS uptime_check_settings (
    check_id ` + key + ` NOT NULL PRIMARY KEY,
    settings ` + big + ` NOT NULL,
    secrets  ` + big + ` NOT NULL,
    token    ` + tok + `
)` + tail + `;
CREATE INDEX ` + ine + `idx_uptime_check_settings_token ON uptime_check_settings (token);
CREATE TABLE IF NOT EXISTS uptime_run_details (
    id         ` + id + `,
    created_at ` + ts + ` NOT NULL,
    check_id   ` + key + ` NOT NULL,
    ok         ` + num + ` NOT NULL,
    detail     ` + big + ` NOT NULL,
    location   ` + tok + `
)` + tail + `;
CREATE INDEX ` + ine + `idx_uptime_run_details_check ON uptime_run_details (check_id, created_at);
CREATE INDEX ` + ine + `idx_uptime_run_details_created ON uptime_run_details (created_at);
CREATE TABLE IF NOT EXISTS uptime_heartbeat_pings (
    id          ` + id + `,
    created_at  ` + ts + ` NOT NULL,
    check_id    ` + key + ` NOT NULL,
    kind        ` + tok + ` NOT NULL,
    exit_code   ` + num + `,
    message     TEXT,
    duration_ms ` + real + `
)` + tail + `;
CREATE INDEX ` + ine + `idx_uptime_heartbeat_pings_check ON uptime_heartbeat_pings (check_id, created_at);
CREATE INDEX ` + ine + `idx_uptime_heartbeat_pings_created ON uptime_heartbeat_pings (created_at)`}
}

func (s *Server) uptimeKindsRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/uptime/checks/{id}/details", s.authed(s.handleUptimeDetails))
	mux.Handle("GET /api/uptime/checks/{id}/heartbeat", s.authed(s.handleHeartbeatView))
	mux.Handle("POST /api/uptime/checks/{id}/heartbeat/token", s.authed(s.handleHeartbeatToken))
	mux.Handle("GET /api/uptime/ping", s.authed(s.handlePingStatus))
	mux.HandleFunc("/api/heartbeat/{token}", s.handleHeartbeatPing)
	mux.HandleFunc("/api/heartbeat/{token}/{action}", s.handleHeartbeatPing)
}

// normalizeKind validates a check of one of the kinds above. prev is the
// stored check on update, whose masked secrets and heartbeat token survive.
func (c *UptimeCheck) normalizeKind(prev *UptimeCheck) error {
	c.ExpectStatus, c.BodyContains, c.Headers, c.Steps, c.FinalScreenshot = "200-299", "", map[string]string{}, nil, false
	c.Method, c.BrowserOptions = "GET", BrowserOptions{}
	if c.Kind != "api" {
		c.Secrets, c.FollowRedirects = nil, true
	}
	keep := func(k string) bool { return c.Kind == k }
	if !keep("api") {
		c.API = nil
	}
	if !keep("tcp") {
		c.TCP = nil
	}
	if !keep("dns") {
		c.DNS = nil
	}
	if !keep("ping") {
		c.Ping = nil
	}
	if !keep("ssl") {
		c.SSL = nil
	}
	if !keep("heartbeat") {
		c.Heartbeat = nil
	}
	timeout, maxTimeout, minTimeout := 10000, 60000, 1000
	var err error
	switch c.Kind {
	case "api":
		timeout, maxTimeout = 30000, kindMaxTimeoutMs
		if c.API == nil {
			return errors.New("an API check needs at least one request")
		}
		var prevAPI *APIConfig
		var prevSecrets map[string]string
		if prev != nil {
			prevAPI, prevSecrets = prev.API, prev.Secrets
		}
		if c.Secrets, err = normalizeSecrets(c.Secrets, prevSecrets); err == nil {
			err = c.API.Normalize(prevAPI, c.Secrets)
		}
		if err == nil {
			c.URL = c.API.Requests[0].URL
		}
	case "tcp":
		if c.TCP == nil {
			return errors.New("a TCP check needs a host and port")
		}
		err = c.TCP.Normalize()
		c.URL = "tcp://" + net.JoinHostPort(c.TCP.Host, strconv.Itoa(c.TCP.Port))
	case "dns":
		if c.DNS == nil {
			return errors.New("a DNS check needs a name to resolve")
		}
		err = c.DNS.Normalize()
		c.URL = "dns://" + c.DNS.Name + "?type=" + c.DNS.RecordType
	case "ping":
		timeout, maxTimeout, minTimeout = 2000, 10000, 100
		if c.Ping == nil {
			return errors.New("a ping check needs a host")
		}
		err = c.Ping.Normalize()
		c.URL = "ping://" + uptimecore.BracketHost(c.Ping.Host)
	case "ssl":
		if c.SSL == nil {
			return errors.New("an SSL check needs a host")
		}
		err = c.SSL.Normalize()
		c.URL = "ssl://" + net.JoinHostPort(c.SSL.Host, strconv.Itoa(c.SSL.Port))
	case "heartbeat":
		if c.Heartbeat == nil {
			return errors.New("a heartbeat check needs a period or a cron schedule")
		}
		var old *HeartbeatConfig
		if prev != nil {
			old = prev.Heartbeat
		}
		err = c.Heartbeat.normalize(old)
		// The server looks for missed pings every minute; nothing is probed,
		// so locations do not apply.
		c.URL, c.IntervalS, c.TimeoutMs, c.DegradedMs = "", 60, 1000, 0
		c.Locations, c.StatusShowLocations = []string{serverLocation}, false
	}
	if err != nil {
		return err
	}
	if c.IntervalS == 0 {
		c.IntervalS = 60
	}
	if c.TimeoutMs == 0 {
		c.TimeoutMs = timeout
	}
	if c.IntervalS < uptimeMinInterval || c.IntervalS > uptimeMaxInterval {
		return errors.New("interval must be between 30 seconds and 1 hour")
	}
	if c.TimeoutMs < minTimeout || c.TimeoutMs > maxTimeout {
		return fmt.Errorf("timeout must be between %d and %d ms", minTimeout, maxTimeout)
	}
	budget := c.TimeoutMs
	if c.Kind == "ping" {
		budget = c.Ping.Packets * (c.TimeoutMs + uptimecore.PingGapMs)
	}
	if budget > c.IntervalS*1000 {
		return errors.New("timeout must be shorter than the interval")
	}
	if c.DegradedMs < 0 || c.DegradedMs > kindMaxTimeoutMs {
		return errors.New("slow threshold must be between 0 and 120000 ms")
	}
	if !c.OnStatusPage {
		c.StatusShowURL = false
	}
	return nil
}

// normalizeSecrets checks secret names and puts back values sent as the
// mask, from prev.
func normalizeSecrets(secrets map[string]string, prev map[string]string) (map[string]string, error) {
	if len(secrets) > browserMaxSecrets {
		return nil, fmt.Errorf("at most %d secrets", browserMaxSecrets)
	}
	out := make(map[string]string, len(secrets))
	for k, v := range secrets {
		if !secretNameRe.MatchString(k) {
			return nil, fmt.Errorf("secret name %q: use letters, digits and _", k)
		}
		if len(v) > 1000 {
			return nil, fmt.Errorf("secret %s is too long", k)
		}
		if v == uptimeMask {
			old, ok := prev[k]
			if !ok {
				return nil, fmt.Errorf("enter the value of secret %s again", k)
			}
			v = old
		}
		out[k] = v
	}
	return out, nil
}

func (c UptimeCheck) kindSettings() any {
	switch c.Kind {
	case "api":
		return c.API
	case "tcp":
		return c.TCP
	case "dns":
		return c.DNS
	case "ping":
		return c.Ping
	case "ssl":
		return c.SSL
	case "heartbeat":
		hb := *c.Heartbeat
		hb.Token = ""
		return hb
	}
	return nil
}

func (s *Server) loadKindSettings(ctx context.Context, checks []UptimeCheck) error {
	ids := []string{}
	for _, c := range checks {
		if settingsKinds[c.Kind] {
			ids = append(ids, strconv.FormatInt(c.ID, 10))
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.db.Query(ctx, `SELECT check_id, settings, secrets, token FROM uptime_check_settings WHERE check_id IN (`+strings.Join(ids, ", ")+`)`)
	if err != nil {
		return err
	}
	byID := map[int64][]any{}
	for _, row := range rows.Rows {
		if len(row) == 4 {
			byID[toInt(row[0])] = row
		}
	}
	for i := range checks {
		c := &checks[i]
		if !settingsKinds[c.Kind] {
			continue
		}
		row := byID[c.ID]
		raw := []byte("{}")
		if row != nil {
			raw = []byte(toString(row[1]))
		}
		switch c.Kind {
		case "api":
			c.API = &APIConfig{}
			_ = json.Unmarshal(raw, c.API)
			c.Secrets = map[string]string{}
			if row != nil {
				_ = json.Unmarshal([]byte(toString(row[2])), &c.Secrets)
			}
		case "tcp":
			c.TCP = &TCPConfig{}
			_ = json.Unmarshal(raw, c.TCP)
		case "dns":
			c.DNS = &DNSConfig{}
			_ = json.Unmarshal(raw, c.DNS)
		case "ping":
			c.Ping = &PingConfig{}
			_ = json.Unmarshal(raw, c.Ping)
		case "ssl":
			c.SSL = &SSLConfig{}
			_ = json.Unmarshal(raw, c.SSL)
		case "heartbeat":
			c.Heartbeat = &HeartbeatConfig{}
			_ = json.Unmarshal(raw, c.Heartbeat)
			if row != nil {
				c.Heartbeat.Token = toString(row[3])
			}
		}
	}
	return nil
}

func (s *Server) saveKindSettings(ctx context.Context, c UptimeCheck) error {
	if err := s.db.Exec(ctx, `DELETE FROM uptime_check_settings WHERE check_id = ?`, c.ID); err != nil {
		return err
	}
	if !settingsKinds[c.Kind] {
		return nil
	}
	settings, err := json.Marshal(c.kindSettings())
	if err != nil {
		return err
	}
	secrets := []byte("{}")
	if c.Kind == "api" && len(c.Secrets) > 0 {
		if secrets, err = json.Marshal(c.Secrets); err != nil {
			return err
		}
	}
	if c.Kind == "heartbeat" {
		return s.db.Exec(ctx, `INSERT INTO uptime_check_settings (check_id, settings, secrets, token) VALUES (?, ?, ?, ?)`,
			c.ID, string(settings), string(secrets), c.Heartbeat.Token)
	}
	return s.db.Exec(ctx, `INSERT INTO uptime_check_settings (check_id, settings, secrets) VALUES (?, ?, ?)`, c.ID, string(settings), string(secrets))
}

func (s *Server) deleteKindData(ctx context.Context, id int64) {
	for _, t := range []string{"uptime_check_settings", "uptime_run_details", "uptime_heartbeat_pings"} {
		if err := s.db.Exec(ctx, `DELETE FROM `+t+` WHERE check_id = ?`, id); err != nil && !missingTable(err, t) {
			s.logger.Warn("check data not deleted", "check_id", id, "table", t, "err", err)
		}
	}
}

// maskKind hides what the API must not show of the kinds above: secret
// header values of API requests and the heartbeat token, which is fetched
// on its own by editors.
func (c *UptimeCheck) maskKind() {
	if c.API != nil {
		api := *c.API
		api.Requests = make([]APIRequest, len(c.API.Requests))
		for i, rq := range c.API.Requests {
			rq.Headers = maskHeaders(rq.Headers)
			api.Requests[i] = rq
		}
		c.API = &api
	}
	if c.Heartbeat != nil {
		hb := *c.Heartbeat
		hb.Token = ""
		c.Heartbeat = &hb
	}
}

func maskHeaders(h map[string]string) map[string]string {
	if h == nil {
		return nil
	}
	out := make(map[string]string, len(h))
	for k, v := range h {
		if secretHeaderRe.MatchString(k) && v != "" && !onlyTemplates(v) {
			v = uptimeMask
		}
		out[k] = v
	}
	return out
}

// onlyTemplates is true for a value that holds no literal secret, such as
// "Bearer {{secret.TOKEN}}".
func onlyTemplates(v string) bool {
	rest := uptimecore.TemplateRefRe.ReplaceAllString(v, "")
	rest = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rest), "Bearer"))
	return rest == "" || strings.EqualFold(rest, "basic")
}

func (c UptimeCheck) kindSettingsOrNil() any {
	if !settingsKinds[c.Kind] || (c.Kind == "heartbeat" && c.Heartbeat == nil) {
		return nil
	}
	return c.kindSettings()
}

func (s *Server) recordDetail(ctx context.Context, c UptimeCheck, r uptimeResult, location string) error {
	if r.Detail == nil || !settingsKinds[c.Kind] {
		return nil
	}
	b, err := json.Marshal(r.Detail)
	if err != nil || len(b) > detailsMaxBytes {
		return err
	}
	return s.db.Exec(ctx, `INSERT INTO uptime_run_details (created_at, check_id, ok, detail, location) VALUES (?, ?, ?, ?, ?)`,
		r.CreatedAt, c.ID, boolInt(r.OK), string(b), location)
}

func (s *Server) handleUptimeDetails(w http.ResponseWriter, r *http.Request) {
	id, ok := checkID(w, r)
	if !ok {
		return
	}
	tr, err := parseRange(r.URL.Query(), s.maxRangeDays())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT d.created_at, d.ok, d.detail, u.duration_ms, u.error, u.status, u.retried, u.degraded, d.location
FROM uptime_run_details d LEFT JOIN uptime_results u ON u.check_id = d.check_id AND u.created_at = d.created_at
  AND COALESCE(u.location, 'server') = COALESCE(d.location, 'server')
WHERE d.check_id = ? AND d.created_at >= ? AND d.created_at < ?
ORDER BY d.created_at DESC LIMIT `+strconv.Itoa(detailsMaxRuns), id, tr.From.UTC().Format(tsLayout), tr.To.UTC().Format(tsLayout))
	if err != nil {
		if missingTable(err, "uptime_run_details") {
			writeJSON(w, http.StatusOK, map[string]any{"runs": []any{}})
			return
		}
		s.fail(w, err)
		return
	}
	runs := make([]map[string]any, 0, len(rows.Rows))
	for _, row := range rows.Rows {
		var detail json.RawMessage = []byte(toString(row[2]))
		if !json.Valid(detail) {
			detail = []byte("{}")
		}
		runs = append(runs, map[string]any{"created_at": toString(row[0]), "ok": toInt(row[1]) == 1, "detail": detail,
			"duration_ms": row[3], "error": toString(row[4]), "status": toInt(row[5]), "retried": toInt(row[6]) == 1, "degraded": toInt(row[7]) == 1, "location": fallback(toString(row[8]), serverLocation)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (s *Server) handlePingStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, uptimecore.PingStatus())
}
