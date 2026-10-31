package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	sdk "github.com/styropyr0/visionary/sdks/go"
)

// Check kinds beyond http and browser: multi-step API checks (uptime_api.go),
// TCP, DNS and SSL certificate checks (uptime_net.go), ping (uptime_ping.go)
// and heartbeats, which jobs push to instead of being probed
// (uptime_heartbeat.go). They share the schedule, results, incidents, alerts
// and status page of HTTP checks. Their settings live in
// uptime_check_settings, and what each run found beyond the uptime_results
// row (each request of an API check, ping round trips, DNS answers, the
// certificate) in uptime_run_details.

var settingsKinds = map[string]bool{"api": true, "tcp": true, "dns": true, "ping": true, "ssl": true, "heartbeat": true}

const (
	kindMaxTimeoutMs = 120000
	detailsMaxRuns   = 500
)

var (
	hostNameRe = regexp.MustCompile(`(?i)^[a-z0-9_]([a-z0-9_-]{0,62})?(\.[a-z0-9_]([a-z0-9_-]{0,62})?)*\.?$`)
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
    detail     ` + big + ` NOT NULL
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
	c.Method = "GET"
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
		err = c.normalizeAPI(prev)
	case "tcp":
		if c.TCP == nil {
			return errors.New("a TCP check needs a host and port")
		}
		err = c.TCP.normalize()
		c.URL = "tcp://" + net.JoinHostPort(c.TCP.Host, strconv.Itoa(c.TCP.Port))
	case "dns":
		if c.DNS == nil {
			return errors.New("a DNS check needs a name to resolve")
		}
		err = c.DNS.normalize()
		c.URL = "dns://" + c.DNS.Name + "?type=" + c.DNS.RecordType
	case "ping":
		timeout, maxTimeout, minTimeout = 2000, 10000, 100
		if c.Ping == nil {
			return errors.New("a ping check needs a host")
		}
		err = c.Ping.normalize()
		c.URL = "ping://" + bracketHost(c.Ping.Host)
	case "ssl":
		if c.SSL == nil {
			return errors.New("an SSL check needs a host")
		}
		err = c.SSL.normalize()
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
		// The runner looks for missed pings every minute; nothing is probed.
		c.URL, c.IntervalS, c.TimeoutMs, c.DegradedMs = "", 60, 1000, 0
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
		budget = c.Ping.Packets * (c.TimeoutMs + pingGapMs)
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

func bracketHost(h string) string {
	if strings.Contains(h, ":") {
		return "[" + h + "]"
	}
	return h
}

// normalizeHost accepts a host name or an IP address, without a port.
func normalizeHost(h string) (string, error) {
	h = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(h), "["), "]")
	if h == "" {
		return "", errors.New("host is required")
	}
	if a, err := netip.ParseAddr(h); err == nil {
		return a.String(), nil
	}
	if len(h) > 253 || !hostNameRe.MatchString(h) {
		return "", fmt.Errorf("host %q is not a valid name or IP address", h)
	}
	return strings.ToLower(h), nil
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

// kindSettings is what uptime_check_settings stores for a check.
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

// loadKindSettings fills in the settings of checks of the kinds above.
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

var templateRefRe = regexp.MustCompile(`\{\{\s*(var|secret)\.([A-Za-z0-9_]+)\s*\}\}`)

// onlyTemplates is true for a value that holds no literal secret, such as
// "Bearer {{secret.TOKEN}}".
func onlyTemplates(v string) bool {
	rest := templateRefRe.ReplaceAllString(v, "")
	rest = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rest), "Bearer"))
	return rest == "" || strings.EqualFold(rest, "basic")
}

// maskValues replaces each value with the mask, in anything a run records.
func maskValues(vals []string) func(string) string {
	var keep []string
	for _, v := range vals {
		if len(v) >= 3 {
			keep = append(keep, v)
		}
	}
	return func(s string) string {
		for _, v := range keep {
			s = strings.ReplaceAll(s, v, uptimeMask)
			if e := url.QueryEscape(v); e != v {
				s = strings.ReplaceAll(s, e, uptimeMask)
			}
		}
		return s
	}
}

// attemptTimeout is how long one attempt of the check may take: a ping
// check waits for each packet in turn, after looking up the host.
func (c UptimeCheck) attemptTimeout() time.Duration {
	if c.Kind == "ping" && c.Ping != nil {
		return time.Duration(c.Ping.Packets)*(time.Duration(c.TimeoutMs+pingGapMs)*time.Millisecond) + 10*time.Second
	}
	return time.Duration(c.TimeoutMs) * time.Millisecond
}

// kindSettingsOrNil is kindSettings for any check, for comparing two.
func (c UptimeCheck) kindSettingsOrNil() any {
	if !settingsKinds[c.Kind] || (c.Kind == "heartbeat" && c.Heartbeat == nil) {
		return nil
	}
	return c.kindSettings()
}

// probeKind runs one attempt of a check of the kinds above.
func (u *uptimeRunner) probeKind(ctx context.Context, c UptimeCheck) uptimeResult {
	switch c.Kind {
	case "api":
		return u.probeAPI(ctx, c)
	case "tcp":
		return u.probeTCP(ctx, c)
	case "dns":
		return u.probeDNS(ctx, c)
	case "ping":
		return u.probePing(ctx, c)
	case "ssl":
		return u.probeSSL(ctx, c)
	case "heartbeat":
		return u.s.heartbeatResult(ctx, c, time.Now())
	}
	return uptimeResult{CreatedAt: uptimeNow(), Error: "unknown check kind"}
}

// kindDialer connects to a check's target, refusing private addresses
// unless they are allowed.
func (u *uptimeRunner) kindDialer(timeout time.Duration) *net.Dialer {
	d := &net.Dialer{Timeout: timeout}
	if !u.allowPrivate() {
		d.Control = dialGuard
	}
	return d
}

// precheckHost refuses a loopback name or a literal private address before
// anything is sent.
func (u *uptimeRunner) precheckHost(host string) error {
	if u.allowPrivate() {
		return nil
	}
	return precheckURL(&url.URL{Scheme: "http", Host: bracketHost(host)})
}

// resolveGuarded looks a host up and returns its first address (IPv4
// first), refusing private ones unless they are allowed.
func (u *uptimeRunner) resolveGuarded(ctx context.Context, host string) (netip.Addr, error) {
	if err := u.precheckHost(host); err != nil {
		return netip.Addr{}, err
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return netip.Addr{}, err
	}
	if len(addrs) == 0 {
		return netip.Addr{}, fmt.Errorf("%s has no address", host)
	}
	pick := addrs[0]
	for _, a := range addrs {
		if a.Unmap().Is4() {
			pick = a.Unmap()
			break
		}
	}
	if !u.allowPrivate() && blockedAddr(pick) {
		return netip.Addr{}, fmt.Errorf("%w: %s is a private, loopback or link-local address (an admin can allow it in Settings → Server → Other)", errBlockedAddress, pick)
	}
	return pick, nil
}

func msSince(t time.Time) float64 { return float64(time.Since(t).Microseconds()) / 1000 }

func msPtr(v float64) *float64 { return &v }

// recordDetail keeps what a run found beyond its uptime_results row.
func (u *uptimeRunner) recordDetail(ctx context.Context, c UptimeCheck, r uptimeResult) error {
	if r.Detail == nil {
		return nil
	}
	b, err := json.Marshal(r.Detail)
	if err != nil {
		return err
	}
	return u.s.db.Exec(ctx, `INSERT INTO uptime_run_details (created_at, check_id, ok, detail) VALUES (?, ?, ?, ?)`, r.CreatedAt, c.ID, boolInt(r.OK), string(b))
}

// handleUptimeDetails lists a check's run details in the range, newest
// first.
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
	rows, err := s.db.Query(r.Context(), `SELECT d.created_at, d.ok, d.detail, u.duration_ms, u.error, u.status, u.retried, u.degraded
FROM uptime_run_details d LEFT JOIN uptime_results u ON u.check_id = d.check_id AND u.created_at = d.created_at
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
			"duration_ms": row[3], "error": toString(row[4]), "status": toInt(row[5]), "retried": toInt(row[6]) == 1, "degraded": toInt(row[7]) == 1})
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (s *Server) handlePingStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, pingStatus())
}
