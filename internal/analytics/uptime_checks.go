package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// checkWithAlert is a check as the form sends it, with its alert settings
// (uptime_alerts.go); without them the alerting is left as it is.
type checkWithAlert struct {
	UptimeCheck
	Alert *checkAlertInput `json:"alert"`
}

// UptimeCheck is one HTTP probe. Header values with secret-looking names are
// masked in API responses; sending the mask back keeps the stored value.
type UptimeCheck struct {
	ID              int64             `json:"id"`
	Name            string            `json:"name"`
	URL             string            `json:"url"`
	Method          string            `json:"method"`
	ExpectStatus    string            `json:"expect_status"`
	BodyContains    string            `json:"body_contains"`
	Headers         map[string]string `json:"headers"`
	IntervalS       int               `json:"interval_s"`
	TimeoutMs       int               `json:"timeout_ms"`
	DegradedMs      int               `json:"degraded_ms"`
	FollowRedirects bool              `json:"follow_redirects"`
	Enabled         bool              `json:"enabled"`
	OnStatusPage    bool              `json:"on_status_page"`
	StatusShowURL   bool              `json:"status_show_url"`
	CreatedAt       string            `json:"created_at"`
	UpdatedAt       string            `json:"updated_at"`
}

const (
	uptimeMaxChecks   = 200
	uptimeMaxHeaders  = 20
	uptimeMinInterval = 30
	uptimeMaxInterval = 3600
	uptimeMask        = "********"
)

var (
	uptimeMethods    = map[string]bool{"GET": true, "HEAD": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "OPTIONS": true}
	headerNameRe     = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+.^_|~-]{1,64}$`)
	secretHeaderRe   = regexp.MustCompile(`(?i)(auth|token|secret|key|cookie|session|password|signature|credential)`)
	expectStatusPart = regexp.MustCompile(`^([1-5])xx$|^(\d{3})(?:-(\d{3}))?$`)
)

const uptimeCheckCols = `id, name, url, method, expect_status, body_contains, headers, interval_s, timeout_ms, degraded_ms,
  follow_redirects, enabled, on_status_page, status_show_url, created_at, updated_at`

func scanUptimeCheck(row []any) UptimeCheck {
	c := UptimeCheck{
		ID:              toInt(row[0]),
		Name:            toString(row[1]),
		URL:             toString(row[2]),
		Method:          toString(row[3]),
		ExpectStatus:    toString(row[4]),
		BodyContains:    toString(row[5]),
		IntervalS:       int(toInt(row[7])),
		TimeoutMs:       int(toInt(row[8])),
		DegradedMs:      int(toInt(row[9])),
		FollowRedirects: toInt(row[10]) == 1,
		Enabled:         toInt(row[11]) == 1,
		OnStatusPage:    toInt(row[12]) == 1,
		StatusShowURL:   toInt(row[13]) == 1,
		CreatedAt:       toString(row[14]),
		UpdatedAt:       toString(row[15]),
	}
	_ = json.Unmarshal([]byte(toString(row[6])), &c.Headers)
	if c.Headers == nil {
		c.Headers = map[string]string{}
	}
	return c
}

func (s *Server) listUptimeChecks(ctx context.Context) ([]UptimeCheck, error) {
	rows, err := s.db.Query(ctx, `SELECT `+uptimeCheckCols+` FROM uptime_checks ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	out := make([]UptimeCheck, 0, len(rows.Rows))
	for _, row := range rows.Rows {
		if len(row) == 16 {
			out = append(out, scanUptimeCheck(row))
		}
	}
	return out, nil
}

var errCheckNotFound = errors.New("check not found")

func (s *Server) getUptimeCheck(ctx context.Context, id int64) (UptimeCheck, error) {
	rows, err := s.db.Query(ctx, `SELECT `+uptimeCheckCols+` FROM uptime_checks WHERE id = ?`, id)
	if err != nil {
		return UptimeCheck{}, err
	}
	if len(rows.Rows) == 0 || len(rows.Rows[0]) != 16 {
		return UptimeCheck{}, errCheckNotFound
	}
	return scanUptimeCheck(rows.Rows[0]), nil
}

// normalize fills defaults and validates. prev is the stored check on update,
// whose secret header values survive a masked round trip.
func (c *UptimeCheck) normalize(prev *UptimeCheck) error {
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" || len(c.Name) > 120 {
		return errors.New("name must be 1–120 characters")
	}
	c.URL = strings.TrimSpace(c.URL)
	if len(c.URL) > 2048 {
		return errors.New("URL is too long")
	}
	u, err := url.Parse(c.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return errors.New("URL must be an absolute http:// or https:// address")
	}
	c.Method = strings.ToUpper(strings.TrimSpace(c.Method))
	if c.Method == "" {
		c.Method = "GET"
	}
	if !uptimeMethods[c.Method] {
		return fmt.Errorf("method %q is not supported", c.Method)
	}
	c.ExpectStatus = strings.ReplaceAll(strings.TrimSpace(c.ExpectStatus), " ", "")
	if c.ExpectStatus == "" {
		c.ExpectStatus = "200-299"
	}
	if _, err := parseExpectStatus(c.ExpectStatus); err != nil {
		return err
	}
	if len(c.BodyContains) > 512 {
		return errors.New("expected body text is limited to 512 characters")
	}
	if c.IntervalS == 0 {
		c.IntervalS = 60
	}
	if c.IntervalS < uptimeMinInterval || c.IntervalS > uptimeMaxInterval {
		return errors.New("interval must be between 30 seconds and 1 hour")
	}
	if c.TimeoutMs == 0 {
		c.TimeoutMs = 10000
	}
	if c.TimeoutMs < 1000 || c.TimeoutMs > 60000 {
		return errors.New("timeout must be between 1 and 60 seconds")
	}
	if c.TimeoutMs > c.IntervalS*1000 {
		return errors.New("timeout must be shorter than the interval")
	}
	if c.DegradedMs < 0 || c.DegradedMs > 60000 {
		return errors.New("latency threshold must be between 0 and 60000 ms")
	}
	if !c.OnStatusPage {
		c.StatusShowURL = false
	}
	if len(c.Headers) > uptimeMaxHeaders {
		return fmt.Errorf("at most %d headers", uptimeMaxHeaders)
	}
	headers := make(map[string]string, len(c.Headers))
	for k, v := range c.Headers {
		k = strings.TrimSpace(k)
		if !headerNameRe.MatchString(k) {
			return fmt.Errorf("header name %q is not valid", k)
		}
		if strings.ContainsAny(v, "\r\n") || len(v) > 4096 {
			return fmt.Errorf("header %s has an invalid value", k)
		}
		if v == uptimeMask {
			old, ok := "", false
			if prev != nil {
				old, ok = headerValue(prev.Headers, k)
			}
			if !ok {
				return fmt.Errorf("enter the value of header %s again", k)
			}
			v = old
		}
		headers[k] = v
	}
	c.Headers = headers
	return nil
}

func headerValue(h map[string]string, name string) (string, bool) {
	for k, v := range h {
		if strings.EqualFold(k, name) {
			return v, true
		}
	}
	return "", false
}

// masked is the check as the API shows it.
func (c UptimeCheck) masked() UptimeCheck {
	h := make(map[string]string, len(c.Headers))
	for k, v := range c.Headers {
		if secretHeaderRe.MatchString(k) && v != "" {
			v = uptimeMask
		}
		h[k] = v
	}
	c.Headers = h
	return c
}

// statusRange is an inclusive range of accepted HTTP statuses.
type statusRange struct{ lo, hi int }

// parseExpectStatus reads "200-299", "2xx", "200,204,301" or a mix.
func parseExpectStatus(spec string) ([]statusRange, error) {
	var out []statusRange
	for _, part := range strings.Split(spec, ",") {
		m := expectStatusPart.FindStringSubmatch(strings.ToLower(part))
		if m == nil {
			return nil, fmt.Errorf("expected status %q: use a code (200), a range (200-299) or a class (2xx), comma separated", part)
		}
		var r statusRange
		switch {
		case m[1] != "":
			d, _ := strconv.Atoi(m[1])
			r = statusRange{d * 100, d*100 + 99}
		default:
			r.lo, _ = strconv.Atoi(m[2])
			r.hi = r.lo
			if m[3] != "" {
				r.hi, _ = strconv.Atoi(m[3])
			}
		}
		if r.lo < 100 || r.hi > 599 || r.hi < r.lo {
			return nil, fmt.Errorf("expected status %q is out of range", part)
		}
		out = append(out, r)
	}
	return out, nil
}

func statusAccepted(rs []statusRange, code int) bool {
	for _, r := range rs {
		if code >= r.lo && code <= r.hi {
			return true
		}
	}
	return false
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Server) saveUptimeCheck(ctx context.Context, c *UptimeCheck) error {
	headers, err := json.Marshal(c.Headers)
	if err != nil {
		return err
	}
	now := uptimeNow()
	c.UpdatedAt = now
	args := []any{c.Name, c.URL, c.Method, c.ExpectStatus, c.BodyContains, string(headers), c.IntervalS, c.TimeoutMs, c.DegradedMs,
		boolInt(c.FollowRedirects), boolInt(c.Enabled), boolInt(c.OnStatusPage), boolInt(c.StatusShowURL)}
	if c.ID == 0 {
		c.CreatedAt = now
		c.ID, err = s.db.InsertID(ctx, `INSERT INTO uptime_checks (name, url, method, expect_status, body_contains, headers, interval_s,
  timeout_ms, degraded_ms, follow_redirects, enabled, on_status_page, status_show_url, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, append(args, now, now)...)
		return err
	}
	return s.db.Exec(ctx, `UPDATE uptime_checks SET name = ?, url = ?, method = ?, expect_status = ?, body_contains = ?, headers = ?,
  interval_s = ?, timeout_ms = ?, degraded_ms = ?, follow_redirects = ?, enabled = ?, on_status_page = ?, status_show_url = ?,
  updated_at = ? WHERE id = ?`, append(args, now, c.ID)...)
}

// --- handlers ---

func checkID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid check id")
		return 0, false
	}
	return id, true
}

// loadCheck answers 404 itself when the check is gone.
func (s *Server) loadCheck(w http.ResponseWriter, r *http.Request) (UptimeCheck, bool) {
	id, ok := checkID(w, r)
	if !ok {
		return UptimeCheck{}, false
	}
	c, err := s.getUptimeCheck(r.Context(), id)
	if errors.Is(err, errCheckNotFound) {
		writeError(w, http.StatusNotFound, "check not found")
		return c, false
	}
	if err != nil {
		s.fail(w, err)
		return c, false
	}
	return c, true
}

func (s *Server) handleUptimeList(w http.ResponseWriter, r *http.Request) {
	checks, err := s.listUptimeChecks(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	states, err := s.uptimeStates(r.Context(), checks, 90)
	if err != nil {
		s.fail(w, err)
		return
	}
	alerts, err := s.uptimeAlertViews(r.Context(), checks)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]map[string]any, 0, len(checks))
	for _, c := range checks {
		out = append(out, map[string]any{"check": c.masked(), "state": states[c.ID], "alert": alerts[c.ID]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"checks": out, "can_edit": s.can(r, "edit")})
}

func (s *Server) handleUptimeGet(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadCheck(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"check": c.masked(), "can_edit": s.can(r, "edit")})
}

func (s *Server) handleUptimeCreate(w http.ResponseWriter, r *http.Request) {
	if !s.can(r, "edit") {
		writeError(w, http.StatusForbidden, "you do not have permission to change checks")
		return
	}
	// Fields left out of the body keep these defaults.
	body := checkWithAlert{UptimeCheck: UptimeCheck{FollowRedirects: true, Enabled: true}}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	c := body.UptimeCheck
	c.ID = 0
	if err := c.normalize(nil); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	existing, err := s.listUptimeChecks(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	if len(existing) >= uptimeMaxChecks {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("at most %d checks", uptimeMaxChecks))
		return
	}
	if err := s.saveUptimeCheck(r.Context(), &c); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "uptime.create", c.Name, map[string]any{"id": c.ID, "url": redactURL(c.URL)})
	wakeUptime(s)
	// A new check is alerted on from the start: by the settings sent with it,
	// else by the rule for every check (made now if there is none).
	if body.Alert != nil {
		err = s.setCheckAlert(r, c, *body.Alert)
	} else {
		_, err = s.ensureDefaultUptimeRule(r)
	}
	if err != nil {
		// The check exists; say what went wrong with its alert.
		writeJSON(w, http.StatusCreated, map[string]any{"check": c.masked(), "alert_error": userMessage(err)})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"check": c.masked()})
}

func (s *Server) handleUptimeUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.can(r, "edit") {
		writeError(w, http.StatusForbidden, "you do not have permission to change checks")
		return
	}
	prev, ok := s.loadCheck(w, r)
	if !ok {
		return
	}
	var body checkWithAlert
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	c := body.UptimeCheck
	c.ID, c.CreatedAt = prev.ID, prev.CreatedAt
	if err := c.normalize(&prev); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.saveUptimeCheck(r.Context(), &c); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "uptime.update", c.Name, map[string]any{"id": c.ID, "url": redactURL(c.URL), "changed": checkChanges(prev, c)})
	wakeUptime(s)
	alert := body.Alert
	if alert == nil && prev.Name != c.Name {
		// The check's own rule is named after it.
		if views, err := s.uptimeAlertViews(r.Context(), []UptimeCheck{c}); err == nil {
			if v := views[c.ID]; v.Mode == "custom" || v.Mode == "off" {
				alert = &checkAlertInput{Mode: v.Mode, Channels: v.ChannelIDs, Failures: v.Failures, Severity: v.Severity}
			}
		}
	}
	if alert != nil {
		if err := s.setCheckAlert(r, c, *alert); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"check": c.masked(), "alert_error": userMessage(err)})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"check": c.masked()})
}

// checkChanges names the fields an update changed, for the audit log; values
// are left out because headers and URLs can hold secrets.
func checkChanges(a, b UptimeCheck) []string {
	var out []string
	add := func(name string, changed bool) {
		if changed {
			out = append(out, name)
		}
	}
	ha, _ := json.Marshal(a.Headers)
	hb, _ := json.Marshal(b.Headers)
	add("name", a.Name != b.Name)
	add("url", a.URL != b.URL)
	add("method", a.Method != b.Method)
	add("expect_status", a.ExpectStatus != b.ExpectStatus)
	add("body_contains", a.BodyContains != b.BodyContains)
	add("headers", string(ha) != string(hb))
	add("interval_s", a.IntervalS != b.IntervalS)
	add("timeout_ms", a.TimeoutMs != b.TimeoutMs)
	add("degraded_ms", a.DegradedMs != b.DegradedMs)
	add("follow_redirects", a.FollowRedirects != b.FollowRedirects)
	add("enabled", a.Enabled != b.Enabled)
	add("on_status_page", a.OnStatusPage != b.OnStatusPage)
	add("status_show_url", a.StatusShowURL != b.StatusShowURL)
	sort.Strings(out)
	return out
}

func (s *Server) handleUptimeEnable(w http.ResponseWriter, r *http.Request) {
	if !s.can(r, "edit") {
		writeError(w, http.StatusForbidden, "you do not have permission to change checks")
		return
	}
	c, ok := s.loadCheck(w, r)
	if !ok {
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.db.Exec(r.Context(), `UPDATE uptime_checks SET enabled = ?, updated_at = ? WHERE id = ?`, boolInt(body.Enabled), uptimeNow(), c.ID); err != nil {
		s.fail(w, err)
		return
	}
	action := "uptime.pause"
	if body.Enabled {
		action = "uptime.resume"
	}
	s.audit(r, action, c.Name, map[string]any{"id": c.ID})
	wakeUptime(s)
	c.Enabled = body.Enabled
	writeJSON(w, http.StatusOK, map[string]any{"check": c.masked()})
}

func (s *Server) handleUptimeDelete(w http.ResponseWriter, r *http.Request) {
	if !s.can(r, "edit") {
		writeError(w, http.StatusForbidden, "you do not have permission to change checks")
		return
	}
	c, ok := s.loadCheck(w, r)
	if !ok {
		return
	}
	if err := s.db.Exec(r.Context(), `DELETE FROM uptime_checks WHERE id = ?`, c.ID); err != nil {
		s.fail(w, err)
		return
	}
	// Results go too: they would otherwise linger under a name nobody can open.
	if err := s.db.Exec(r.Context(), `DELETE FROM uptime_results WHERE check_id = ?`, c.ID); err != nil {
		s.logger.Warn("uptime results not deleted", "check_id", c.ID, "err", err)
	}
	s.dropCheckRules(r, c.ID)
	s.audit(r, "uptime.delete", c.Name, map[string]any{"id": c.ID})
	wakeUptime(s)
	w.WriteHeader(http.StatusNoContent)
}

// handleUptimeRun probes the check now (paused or not) and records the result.
func (s *Server) handleUptimeRun(w http.ResponseWriter, r *http.Request) {
	if !s.can(r, "edit") {
		writeError(w, http.StatusForbidden, "you do not have permission to run checks")
		return
	}
	c, ok := s.loadCheck(w, r)
	if !ok {
		return
	}
	run := runnerFor(s)
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Duration(c.TimeoutMs)*time.Millisecond+5*time.Second)
	defer cancel()
	res := run.probeWithRetry(ctx, c)
	if err := run.record(ctx, c, res); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "uptime.run", c.Name, map[string]any{"id": c.ID, "ok": res.OK})
	writeJSON(w, http.StatusOK, map[string]any{"result": res})
}
