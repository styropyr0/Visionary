package analytics

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/styropyr0/visionary/internal/uptimecore"
)

// Heartbeat checks are pushed to instead of probed: a cron job, backup
// script or worker calls the check's secret URL (/api/heartbeat/<token>)
// when it runs. The check is down when no ping arrives within the expected
// period plus a grace time, when a job reports a failure (/fail, or a
// non-zero exit code), or when a job that said it started (/start) does not
// finish within the grace time. The runner looks every minute and records a
// result, so heartbeats get uptime, incidents and alerts like other checks.
// The ping endpoint needs no sign-in (the token is the key), is rate
// limited, and answers a plain 404 for any token it does not know.

// HeartbeatConfig is the settings of a heartbeat check: PeriodS, or Cron in
// Timezone, says when pings are expected. Token is never stored with the
// rest, nor shown in the check; editors read it on its own.
type HeartbeatConfig struct {
	PeriodS  int    `json:"period_s,omitempty"`
	Cron     string `json:"cron,omitempty"`
	Timezone string `json:"timezone,omitempty"`
	GraceS   int    `json:"grace_s"`
	Token    string `json:"token,omitempty"`
}

const (
	heartbeatMaxPeriod  = 31 * 24 * 3600
	heartbeatMaxGrace   = 7 * 24 * 3600
	heartbeatMaxMessage = 500
	heartbeatMaxPings   = 500
)

var heartbeatTokenRe = regexp.MustCompile(`^[0-9a-f]{48}$`)

func newHeartbeatToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (h *HeartbeatConfig) normalize(old *HeartbeatConfig) error {
	h.Cron, h.Timezone = strings.TrimSpace(h.Cron), strings.TrimSpace(h.Timezone)
	switch {
	case h.Cron != "" && h.PeriodS != 0:
		return errors.New("give a period or a cron schedule, not both")
	case h.Cron != "":
		if _, err := parseCron(h.Cron); err != nil {
			return err
		}
		if h.Timezone == "" {
			h.Timezone = "UTC"
		}
		if _, err := time.LoadLocation(h.Timezone); err != nil {
			return fmt.Errorf("unknown time zone %q", h.Timezone)
		}
	default:
		h.Timezone = ""
		if h.PeriodS < 60 || h.PeriodS > heartbeatMaxPeriod {
			return errors.New("the period must be between 1 minute and 31 days")
		}
	}
	if h.GraceS < 0 || h.GraceS > heartbeatMaxGrace {
		return errors.New("the grace time must be between 0 and 7 days")
	}
	if old != nil && old.Token != "" {
		h.Token = old.Token
	}
	if !heartbeatTokenRe.MatchString(h.Token) {
		h.Token = newHeartbeatToken()
	}
	return nil
}

// expectedAfter is when the next ping is due after one at t.
func (h *HeartbeatConfig) expectedAfter(t time.Time) time.Time {
	if h.Cron == "" {
		return t.Add(time.Duration(h.PeriodS) * time.Second)
	}
	spec, err := parseCron(h.Cron)
	if err != nil {
		return time.Time{}
	}
	loc, err := time.LoadLocation(h.Timezone)
	if err != nil {
		loc = time.UTC
	}
	return spec.next(t.In(loc)).UTC()
}

// heartbeatPings is what the latest pings of a check say.
type heartbeatPings struct {
	success, fail, start time.Time
	failCode             *int64
	failMessage          string
	durationMs           *float64
}

func (s *Server) lastHeartbeats(ctx context.Context, id int64) (heartbeatPings, error) {
	var p heartbeatPings
	rows, err := s.db.Query(ctx, `SELECT kind, MAX(created_at) FROM uptime_heartbeat_pings WHERE check_id = ? GROUP BY kind`, id)
	if err != nil {
		return p, err
	}
	for _, row := range rows.Rows {
		t, err := time.Parse(tsLayout, toString(row[1]))
		if err != nil {
			continue
		}
		switch toString(row[0]) {
		case "success":
			p.success = t
		case "fail":
			p.fail = t
		case "start":
			p.start = t
		}
	}
	if !p.fail.IsZero() && p.fail.After(p.success) {
		rows, err := s.db.Query(ctx, `SELECT exit_code, message FROM uptime_heartbeat_pings WHERE check_id = ? AND kind = 'fail' AND created_at = ?`, id, p.fail.Format(tsLayout))
		if err == nil && len(rows.Rows) > 0 {
			if rows.Rows[0][0] != nil {
				code := toInt(rows.Rows[0][0])
				p.failCode = &code
			}
			p.failMessage = toString(rows.Rows[0][1])
		}
	}
	if !p.success.IsZero() {
		rows, err := s.db.Query(ctx, `SELECT duration_ms FROM uptime_heartbeat_pings WHERE check_id = ? AND kind = 'success' AND created_at = ?`, id, p.success.Format(tsLayout))
		if err == nil && len(rows.Rows) > 0 {
			if v, ok := asFloat(rows.Rows[0][0]); ok {
				p.durationMs = &v
			}
		}
	}
	return p, nil
}

// heartbeatVerdict is a heartbeat check's state at a moment.
type heartbeatVerdict struct {
	ok      bool
	waiting bool // no ping yet, and none is overdue
	reason  string
	next    time.Time // when the next ping is due
	late    time.Time // next plus the grace time
}

func (h *HeartbeatConfig) verdict(p heartbeatPings, created, now time.Time) heartbeatVerdict {
	grace := time.Duration(h.GraceS) * time.Second
	base := p.success
	if base.IsZero() {
		base = created
	}
	v := heartbeatVerdict{next: h.expectedAfter(base)}
	v.late = v.next.Add(grace)
	when := func(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 UTC") }
	switch {
	case !p.fail.IsZero() && p.fail.After(p.success):
		v.reason = "the job reported a failure"
		if p.failCode != nil {
			v.reason += fmt.Sprintf(" (exit code %d)", *p.failCode)
		}
		if p.failMessage != "" {
			v.reason += ": " + clip(p.failMessage, 200)
		}
	case !p.start.IsZero() && p.start.After(p.success) && p.start.After(p.fail) && now.After(p.start.Add(grace)):
		v.reason = "the job started at " + when(p.start) + " but did not report success within the grace time"
	case v.next.IsZero():
		v.reason = "the cron schedule never fires"
	case now.After(v.late):
		if p.success.IsZero() {
			v.reason = "no ping yet; the first was expected by " + when(v.next)
		} else {
			v.reason = "no ping since " + when(p.success) + "; the next was expected by " + when(v.next)
		}
	case p.success.IsZero():
		v.ok, v.waiting = true, true
	default:
		v.ok = true
	}
	return v
}

func (s *Server) heartbeatResult(ctx context.Context, c UptimeCheck, now time.Time) uptimeResult {
	res := uptimeResult{CreatedAt: now.UTC().Format(tsLayout)}
	p, err := s.lastHeartbeats(ctx, c.ID)
	if err != nil {
		res.Error = "could not read the pings"
		res.Skip = res.Error
		return res
	}
	created, _ := time.Parse(tsLayout, c.CreatedAt)
	v := c.Heartbeat.verdict(p, created, now)
	if v.waiting {
		res.Skip = "no ping yet; the first is expected by " + v.next.UTC().Format("2006-01-02 15:04 UTC")
	}
	res.OK, res.Error = v.ok, v.reason
	if p.durationMs != nil {
		res.DurationMs = *p.durationMs
	}
	return res
}

// --- the ping endpoint ---

var heartbeatLimits struct {
	sync.Mutex
	global *rate.Limiter
	tokens map[string]*rate.Limiter
}

// heartbeatAllowed limits pings to 200 a second in all and 1 a second (in
// bursts of 10) per token, known or not.
func heartbeatAllowed(token string) bool {
	heartbeatLimits.Lock()
	defer heartbeatLimits.Unlock()
	if heartbeatLimits.global == nil {
		heartbeatLimits.global = rate.NewLimiter(200, 400)
	}
	if heartbeatLimits.tokens == nil || len(heartbeatLimits.tokens) > 10000 {
		heartbeatLimits.tokens = map[string]*rate.Limiter{}
	}
	l := heartbeatLimits.tokens[token]
	if l == nil {
		l = rate.NewLimiter(1, 10)
		heartbeatLimits.tokens[token] = l
	}
	return heartbeatLimits.global.Allow() && l.Allow()
}

func (s *Server) handleHeartbeatPing(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, POST, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token, action := r.PathValue("token"), r.PathValue("action")
	kind := "success"
	var code *int64
	switch action {
	case "":
	case "start", "fail":
		kind = action
	default:
		n, err := strconv.ParseInt(action, 10, 64)
		if err != nil || n < 0 || n > 255 {
			http.NotFound(w, r)
			return
		}
		code = &n
		if n != 0 {
			kind = "fail"
		}
	}
	if !heartbeatAllowed(token) {
		http.Error(w, "too many pings", http.StatusTooManyRequests)
		return
	}
	if !heartbeatTokenRe.MatchString(token) {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	rows, err := s.db.Query(ctx, `SELECT check_id FROM uptime_check_settings WHERE token = ?`, token)
	if err != nil {
		s.logger.Warn("heartbeat lookup failed", "err", err)
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	if len(rows.Rows) == 0 {
		http.NotFound(w, r)
		return
	}
	id := toInt(rows.Rows[0][0])
	c, err := s.getUptimeCheck(ctx, id)
	if err != nil || c.Kind != "heartbeat" {
		http.NotFound(w, r)
		return
	}
	message := r.URL.Query().Get("msg")
	if r.Method == http.MethodPost {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
		if len(b) > 0 {
			message = string(b)
		}
	}
	message = clip(strings.TrimSpace(uptimecore.Printable([]byte(message))), heartbeatMaxMessage)
	now := time.Now().UTC()
	var duration any
	if kind == "success" {
		// The run's length, from its start ping, if it sent one.
		rows, err := s.db.Query(ctx, `SELECT MAX(created_at) FROM uptime_heartbeat_pings WHERE check_id = ? AND kind = 'start' AND created_at > COALESCE(
  (SELECT MAX(created_at) FROM uptime_heartbeat_pings WHERE check_id = ? AND kind <> 'start'), '')`, id, id)
		if err == nil && len(rows.Rows) > 0 {
			if t, err := time.Parse(tsLayout, toString(rows.Rows[0][0])); err == nil && now.Sub(t) < 24*time.Hour {
				duration = float64(now.Sub(t).Microseconds()) / 1000
			}
		}
	}
	cols := []string{"created_at", "check_id", "kind"}
	vals := []any{now.Format(tsLayout), id, kind}
	if code != nil {
		cols, vals = append(cols, "exit_code"), append(vals, *code)
	}
	if message != "" {
		cols, vals = append(cols, "message"), append(vals, message)
	}
	if duration != nil {
		cols, vals = append(cols, "duration_ms"), append(vals, duration)
	}
	if err := s.db.Exec(ctx, "INSERT INTO uptime_heartbeat_pings ("+strings.Join(cols, ", ")+") VALUES (?"+strings.Repeat(", ?", len(cols)-1)+")", vals...); err != nil {
		s.logger.Warn("heartbeat not saved", "check_id", id, "err", err)
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	// A failure is recorded at once, and so is the first success after one;
	// otherwise the runner's next look records the state.
	if c.Enabled && kind != "start" {
		last, err := s.db.Query(ctx, `SELECT ok FROM uptime_results WHERE check_id = ? ORDER BY created_at DESC LIMIT 1`, id)
		wasUp := err == nil && len(last.Rows) > 0 && toInt(last.Rows[0][0]) == 1
		if kind == "fail" || !wasUp {
			res := s.heartbeatResult(ctx, c, now.Add(time.Millisecond))
			if res.Skip == "" {
				if err := runnerFor(s).record(ctx, c, res); err != nil {
					s.logger.Warn("heartbeat result not saved", "check_id", id, "err", err)
				}
			}
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "OK\n")
}

// --- dashboard ---

// handleHeartbeatView is a heartbeat check's pings in the range, its last
// ping and when the next is due. Editors get its URL too.
func (s *Server) handleHeartbeatView(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadCheck(w, r)
	if !ok {
		return
	}
	if c.Kind != "heartbeat" {
		writeError(w, http.StatusBadRequest, "not a heartbeat check")
		return
	}
	tr, err := parseRange(r.URL.Query(), s.maxRangeDays())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := r.Context()
	p, err := s.lastHeartbeats(ctx, c.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	created, _ := time.Parse(tsLayout, c.CreatedAt)
	v := c.Heartbeat.verdict(p, created, time.Now())
	rows, err := s.db.Query(ctx, `SELECT created_at, kind, exit_code, message, duration_ms FROM uptime_heartbeat_pings
WHERE check_id = ? AND created_at >= ? AND created_at < ? ORDER BY created_at DESC LIMIT `+strconv.Itoa(heartbeatMaxPings),
		c.ID, tr.From.UTC().Format(tsLayout), tr.To.UTC().Format(tsLayout))
	if err != nil {
		s.fail(w, err)
		return
	}
	pings := make([]map[string]any, 0, len(rows.Rows))
	for _, row := range rows.Rows {
		pings = append(pings, map[string]any{"created_at": toString(row[0]), "kind": toString(row[1]), "exit_code": row[2], "message": toString(row[3]), "duration_ms": row[4]})
	}
	ts := func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.UTC().Format(tsLayout)
	}
	last := p.success
	for _, t := range []time.Time{p.fail, p.start} {
		if t.After(last) {
			last = t
		}
	}
	out := map[string]any{
		"last_ping": ts(last), "last_success": ts(p.success), "last_fail": ts(p.fail), "last_start": ts(p.start),
		"next_expected": ts(v.next), "late_after": ts(v.late), "ok": v.ok, "waiting": v.waiting, "reason": v.reason,
		"last_duration_ms": p.durationMs, "pings": pings,
	}
	if s.can(r, "edit") {
		out["path"] = "/api/heartbeat/" + c.Heartbeat.Token
	}
	writeJSON(w, http.StatusOK, out)
}

// handleHeartbeatToken gives a heartbeat check a new URL; the old one stops
// working at once.
func (s *Server) handleHeartbeatToken(w http.ResponseWriter, r *http.Request) {
	if !s.can(r, "edit") {
		writeError(w, http.StatusForbidden, "you do not have permission to change checks")
		return
	}
	c, ok := s.loadCheck(w, r)
	if !ok {
		return
	}
	if c.Kind != "heartbeat" {
		writeError(w, http.StatusBadRequest, "not a heartbeat check")
		return
	}
	token := newHeartbeatToken()
	if err := s.db.Exec(r.Context(), `UPDATE uptime_check_settings SET token = ? WHERE check_id = ?`, token, c.ID); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "uptime.heartbeat_url", c.Name, map[string]any{"id": c.ID})
	writeJSON(w, http.StatusOK, map[string]any{"path": "/api/heartbeat/" + token})
}
