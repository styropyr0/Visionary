package analytics

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/styropyr0/visionary/sdks/go"
)

// Playwright checks are uptime checks of kind "playwright" whose runs are
// reported, never started here: the visionary-playwright-reporter package
// posts each test of a Playwright run in the customer's CI or on their own
// host. Running customer code on this server would let any editor execute
// code on it, so there is no scheduler for them. A check is keyed by its
// Playwright project and title path (file › describe › test) and is made on
// its first report unless it was declared first. Each run writes an
// uptime_results row (uptime %, incidents, alerts and the status page work as
// for other checks) and its steps and small attachments to
// uptime_playwright_results. A check whose interval passes without a run is
// stale: the watcher records a failed result, as a heartbeat would.

const (
	pwMaxBody       = 3 << 20
	pwMaxRuns       = 100
	pwMaxSteps      = 100
	pwMaxScreenshot = 256 << 10
	pwMaxTrace      = 768 << 10
	pwMinInterval   = 60
	pwMaxInterval   = 7 * 24 * 3600
	pwStaleTick     = 30 * time.Second
	pwStalePrefix   = "No run reported in "
)

var traceIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

func init() {
	scopedTables["UPTIME_PLAYWRIGHT_CHECKS"] = scopeRule{by: byCheck}
	scopedTables["UPTIME_PLAYWRIGHT_RESULTS"] = scopeRule{by: byCheck}
}

// PlaywrightTest names the test a playwright check follows.
type PlaywrightTest struct {
	Project string `json:"project"`
	Title   string `json:"title"`
}

func (p PlaywrightTest) key() string {
	sum := sha256.Sum256([]byte(p.Project + "\x00" + p.Title))
	return hex.EncodeToString(sum[:16])
}

func (s *Server) uptimePlaywrightSchema() []string {
	id, key, ts, big, num, txt := "INTEGER PRIMARY KEY AUTOINCREMENT", "INTEGER", "TEXT", "TEXT", "REAL", "TEXT"
	tail, ine := "", "IF NOT EXISTS "
	switch s.q.d {
	case sdk.Postgres:
		id, key, num = "BIGSERIAL PRIMARY KEY", "BIGINT", "DOUBLE PRECISION"
	case sdk.MySQL:
		id, key, ts, big, num, txt, tail, ine = "BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY", "BIGINT", "VARCHAR(32)", "MEDIUMTEXT", "DOUBLE", "VARCHAR(64)", " CHARACTER SET utf8mb4", ""
	}
	return []string{`CREATE TABLE IF NOT EXISTS uptime_playwright_checks (
    check_id ` + key + ` NOT NULL PRIMARY KEY,
    test_key ` + txt + ` NOT NULL,
    project  TEXT NOT NULL,
    title    TEXT NOT NULL
)` + tail + `;
CREATE INDEX ` + ine + `idx_uptime_playwright_checks_key ON uptime_playwright_checks (test_key);
CREATE TABLE IF NOT EXISTS uptime_playwright_results (
    id          ` + id + `,
    created_at  ` + ts + ` NOT NULL,
    check_id    ` + key + ` NOT NULL,
    ok          INTEGER NOT NULL,
    status      ` + txt + ` NOT NULL,
    outcome     ` + txt + `,
    retry       INTEGER NOT NULL DEFAULT 0,
    duration_ms ` + num + `,
    started_at  ` + ts + `,
    error       TEXT,
    steps       ` + big + `,
    attachments TEXT,
    screenshot  ` + big + `,
    trace       ` + big + `,
    browser     TEXT,
    project     TEXT,
    trace_id    ` + txt + `,
    release_name TEXT,
    commit_sha  ` + txt + `
)` + tail + `;
CREATE INDEX ` + ine + `idx_uptime_playwright_results_check ON uptime_playwright_results (check_id, created_at);
CREATE INDEX ` + ine + `idx_uptime_playwright_results_created ON uptime_playwright_results (created_at)`}
}

func (s *Server) uptimePlaywrightRoutes(mux *http.ServeMux) {
	mux.Handle("POST /api/uptime/playwright", s.authed(s.handlePlaywrightReport))
	mux.Handle("GET /api/uptime/checks/{id}/playwright", s.authed(s.handlePlaywrightRuns))
	mux.Handle("GET /api/uptime/checks/{id}/playwright/{run}", s.authed(s.handlePlaywrightRun))
	mux.Handle("GET /api/uptime/checks/{id}/playwright/{run}/trace", s.authed(s.handlePlaywrightTrace))
}

// normalizePlaywright validates a playwright check. Interval is how often a
// run is expected; a check without one for longer is stale.
func (c *UptimeCheck) normalizePlaywright() error {
	c.Method, c.ExpectStatus, c.BodyContains, c.Headers, c.FollowRedirects = "GET", "200-299", "", map[string]string{}, true
	c.Steps, c.Secrets, c.FinalScreenshot, c.TimeoutMs = nil, nil, false, 0
	// Runs come from wherever the tests run, so there is no location to pick.
	c.Locations, c.LocationRule, c.StatusShowLocations = []string{serverLocation}, "majority", false
	if c.Playwright == nil {
		return errors.New("a Playwright check needs its project and test title")
	}
	p := c.Playwright
	p.Project, p.Title = strings.TrimSpace(p.Project), strings.TrimSpace(p.Title)
	if len(p.Project) > 200 || p.Title == "" || len(p.Title) > 1000 {
		return errors.New("a Playwright check needs a test title (up to 1000 characters) and a project of up to 200")
	}
	c.URL = strings.TrimSpace(c.URL)
	if c.URL != "" {
		u, err := url.Parse(c.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || len(c.URL) > 2048 {
			return errors.New("the URL must be an absolute http:// or https:// address, or empty")
		}
	}
	if c.IntervalS == 0 {
		c.IntervalS = 3600
	}
	if c.IntervalS < pwMinInterval || c.IntervalS > pwMaxInterval {
		return errors.New("the expected interval must be between 1 minute and 7 days")
	}
	if c.DegradedMs < 0 || c.DegradedMs > 3600000 {
		return errors.New("slow threshold must be between 0 and 3600000 ms")
	}
	if !c.OnStatusPage {
		c.StatusShowURL = false
	}
	return nil
}

// --- the extras every check carries: tags, and a playwright check's test ---

func (s *Server) loadCheckExtras(ctx context.Context, checks []UptimeCheck) error {
	if err := s.loadCheckTags(ctx, checks); err != nil {
		return err
	}
	ids := []string{}
	for _, c := range checks {
		if c.Kind == "playwright" {
			ids = append(ids, strconv.FormatInt(c.ID, 10))
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.db.Query(ctx, `SELECT check_id, project, title FROM uptime_playwright_checks WHERE check_id IN (`+strings.Join(ids, ", ")+`)`)
	if err != nil {
		if missingTable(err, "uptime_playwright_checks") {
			return nil
		}
		return err
	}
	byID := map[int64]*PlaywrightTest{}
	for _, row := range rows.Rows {
		if len(row) == 3 {
			byID[toInt(row[0])] = &PlaywrightTest{Project: toString(row[1]), Title: toString(row[2])}
		}
	}
	for i := range checks {
		if checks[i].Kind == "playwright" {
			checks[i].Playwright = byID[checks[i].ID]
		}
	}
	return nil
}

func (s *Server) saveCheckExtras(ctx context.Context, c UptimeCheck) error {
	if err := s.saveCheckTags(ctx, c); err != nil {
		return err
	}
	if err := s.db.Exec(ctx, `DELETE FROM uptime_playwright_checks WHERE check_id = ?`, c.ID); err != nil {
		return err
	}
	if c.Kind != "playwright" || c.Playwright == nil {
		return nil
	}
	return s.db.Exec(ctx, `INSERT INTO uptime_playwright_checks (check_id, test_key, project, title) VALUES (?, ?, ?, ?)`,
		c.ID, c.Playwright.key(), c.Playwright.Project, c.Playwright.Title)
}

func (s *Server) deleteCheckExtras(ctx context.Context, id int64) {
	s.deleteCheckTags(ctx, id)
	for _, t := range []string{"uptime_playwright_checks", "uptime_playwright_results"} {
		if err := s.db.Exec(ctx, `DELETE FROM `+t+` WHERE check_id = ?`, id); err != nil && !missingTable(err, t) {
			s.logger.Warn("playwright check data not deleted", "check_id", id, "table", t, "err", err)
		}
	}
}

// --- reports from the reporter ---

type pwRun struct {
	Project           string         `json:"project"`
	Title             string         `json:"title"`
	Name              string         `json:"name"`
	OK                *bool          `json:"ok"`
	Status            string         `json:"status"`
	Outcome           string         `json:"outcome"`
	DurationMs        float64        `json:"duration_ms"`
	StartedAt         string         `json:"started_at"`
	Retry             int            `json:"retry"`
	Error             string         `json:"error"`
	Steps             []pwStep       `json:"steps"`
	Attachments       []pwAttachment `json:"attachments"`
	Browser           string         `json:"browser"`
	TraceID           string         `json:"trace_id"`
	URL               string         `json:"url"`
	Release           string         `json:"release"`
	Commit            string         `json:"commit"`
	ExpectedIntervalS int            `json:"expected_interval_s"`
	Tags              []string       `json:"tags"`
}

type pwStep struct {
	Title      string  `json:"title"`
	Category   string  `json:"category,omitempty"`
	DurationMs float64 `json:"duration_ms"`
	Error      string  `json:"error,omitempty"`
	Depth      int     `json:"depth,omitempty"`
}

type pwAttachment struct {
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	Body        string `json:"body,omitempty"`
	Stored      bool   `json:"stored,omitempty"`
}

type pwOutcome struct {
	Project string `json:"project"`
	Title   string `json:"title"`
	CheckID int64  `json:"check_id,omitempty"`
	Status  string `json:"status"` // recorded, created, skipped, paused, error
	Error   string `json:"error,omitempty"`
}

func (s *Server) handlePlaywrightReport(w http.ResponseWriter, r *http.Request) {
	if !s.canTrigger(r) {
		writeError(w, http.StatusForbidden, "you do not have permission to report test runs")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, pwMaxBody+1))
	if err != nil || len(raw) > pwMaxBody {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("send at most %d MB at once", pwMaxBody>>20))
		return
	}
	var runs []pwRun
	var wrapped struct {
		Runs []pwRun `json:"runs"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil && wrapped.Runs != nil {
		runs = wrapped.Runs
	} else {
		var one pwRun
		if err := json.Unmarshal(raw, &one); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		runs = []pwRun{one}
	}
	if len(runs) == 0 || len(runs) > pwMaxRuns {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("send 1 to %d test runs", pwMaxRuns))
		return
	}
	checks, err := s.listUptimeChecks(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]pwOutcome, 0, len(runs))
	for _, run := range runs {
		o := s.recordPlaywright(r, &checks, run)
		if o.Status == "error" {
			s.logger.Info("playwright report refused", "title", o.Title, "err", o.Error)
		}
		out = append(out, o)
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": out})
}

func (s *Server) recordPlaywright(r *http.Request, checks *[]UptimeCheck, run pwRun) pwOutcome {
	ctx := r.Context()
	t := PlaywrightTest{Project: strings.TrimSpace(run.Project), Title: strings.TrimSpace(run.Title)}
	o := pwOutcome{Project: t.Project, Title: t.Title}
	fail := func(msg string) pwOutcome {
		o.Status, o.Error = "error", msg
		return o
	}
	if t.Title == "" {
		return fail("title is required")
	}
	if run.Status == "skipped" || run.Outcome == "skipped" {
		o.Status = "skipped"
		return o
	}
	var c *UptimeCheck
	for i := range *checks {
		if k := &(*checks)[i]; k.Kind == "playwright" && k.Playwright != nil && *k.Playwright == t {
			c = k
			break
		}
	}
	if c == nil {
		if len(*checks) >= uptimeMaxChecks {
			return fail(fmt.Sprintf("at most %d checks", uptimeMaxChecks))
		}
		nc := UptimeCheck{Name: pwName(run), Kind: "playwright", URL: run.URL, IntervalS: run.ExpectedIntervalS, Enabled: true,
			Playwright: &t, Tags: pwTags(run.Tags)}
		if err := nc.normalize(nil); err != nil {
			return fail(err.Error())
		}
		if err := s.saveUptimeCheck(ctx, &nc); err != nil {
			return fail(userMessage(err))
		}
		s.audit(r, "uptime.create", nc.Name, map[string]any{"id": nc.ID, "kind": "playwright", "reported": true})
		if _, err := s.ensureDefaultUptimeRule(r); err != nil {
			s.logger.Warn("default uptime rule not made", "err", err)
		}
		*checks = append(*checks, nc)
		c = &(*checks)[len(*checks)-1]
		o.Status = "created"
	}
	o.CheckID = c.ID
	if !c.Enabled {
		o.Status = "paused"
		return o
	}
	ok := run.Status == "passed"
	if run.OK != nil {
		ok = *run.OK
	}
	res := uptimeResult{CreatedAt: uptimeNow(), OK: ok, DurationMs: max(run.DurationMs, 0), Retried: run.Retry > 0}
	if !ok {
		res.Error = ciClip(fallback(run.Error, "test "+fallback(run.Status, "failed")), 300)
	}
	res.Degraded = ok && c.DegradedMs > 0 && res.DurationMs > float64(c.DegradedMs)
	recordCtx := context.WithoutCancel(ctx)
	if err := runnerFor(s).record(recordCtx, *c, res); err != nil {
		return fail(userMessage(err))
	}
	if err := s.recordPlaywrightDetail(recordCtx, *c, res, run); err != nil {
		s.logger.Warn("playwright run detail not saved", "check_id", c.ID, "err", err)
	}
	if o.Status == "" {
		o.Status = "recorded"
	}
	return o
}

// pwName is a new check's name: the title without its file, and the project.
func pwName(run pwRun) string {
	parts := strings.Split(run.Title, " › ")
	if len(parts) > 1 {
		parts = parts[1:]
	}
	name := strings.TrimSpace(fallback(strings.TrimSpace(run.Name), strings.Join(parts, " › ")))
	if run.Project != "" {
		name += " (" + run.Project + ")"
	}
	return ciClip(name, 118)
}

// pwTags are Playwright's tags (@smoke) as check tags, dropping any that
// don't fit.
func pwTags(in []string) []string {
	var out []string
	for _, t := range in {
		if norm, err := normalizeTags([]string{strings.TrimPrefix(t, "@")}); err == nil && len(norm) == 1 && len(out) < uptimeMaxTags {
			out = append(out, norm[0])
		}
	}
	return out
}

func (s *Server) recordPlaywrightDetail(ctx context.Context, c UptimeCheck, res uptimeResult, run pwRun) error {
	steps := run.Steps
	if len(steps) > pwMaxSteps {
		steps = steps[:pwMaxSteps]
	}
	for i := range steps {
		steps[i].Title = ciClip(steps[i].Title, 300)
		steps[i].Error = ciClip(steps[i].Error, 1000)
	}
	var shot, trace string
	meta := make([]pwAttachment, 0, len(run.Attachments))
	for _, a := range head(run.Attachments, 20) {
		a.Name, a.ContentType = ciClip(a.Name, 200), ciClip(a.ContentType, 100)
		body, err := base64.StdEncoding.DecodeString(a.Body)
		if err == nil && len(body) > 0 {
			switch {
			case shot == "" && (a.ContentType == "image/png" || a.ContentType == "image/jpeg") && len(body) <= pwMaxScreenshot:
				shot, a.Stored = a.ContentType+";base64,"+a.Body, true
			case trace == "" && a.Name == "trace" && a.ContentType == "application/zip" && len(body) <= pwMaxTrace:
				trace, a.Stored = a.Body, true
			}
			a.Size = int64(len(body))
		}
		a.Body = ""
		meta = append(meta, a)
	}
	stepsJSON, _ := json.Marshal(steps)
	metaJSON, _ := json.Marshal(meta)
	traceID := strings.ToLower(strings.TrimSpace(run.TraceID))
	if !traceIDRe.MatchString(traceID) {
		traceID = ""
	}
	commit := strings.ToLower(strings.TrimSpace(run.Commit))
	if !commitRe.MatchString(commit) {
		commit = ""
	}
	cols := []string{"created_at", "check_id", "ok", "status", "outcome", "retry", "duration_ms", "error", "steps", "attachments", "browser", "project",
		"trace_id", "release_name", "commit_sha"}
	vals := []any{res.CreatedAt, c.ID, boolInt(res.OK), ciClip(fallback(run.Status, "failed"), 32), ciClip(run.Outcome, 32), run.Retry, res.DurationMs,
		ciClip(run.Error, 4000), string(stepsJSON), string(metaJSON), ciClip(run.Browser, 100), ciClip(run.Project, 200), traceID,
		ciClip(run.Release, 200), commit}
	if t, err := time.Parse(time.RFC3339Nano, run.StartedAt); err == nil {
		cols, vals = append(cols, "started_at"), append(vals, t.UTC().Format(tsLayout))
	}
	if shot != "" {
		cols, vals = append(cols, "screenshot"), append(vals, shot)
	}
	if trace != "" {
		cols, vals = append(cols, "trace"), append(vals, trace)
	}
	return s.db.Exec(ctx, "INSERT INTO uptime_playwright_results ("+strings.Join(cols, ", ")+") VALUES (?"+strings.Repeat(", ?", len(cols)-1)+")", vals...)
}

// --- stale checks ---

// watchPlaywright records a failure for each playwright check whose expected
// interval passed without a run, once per interval, so incidents and alerts
// open as they would for a check that stopped answering.
func (s *Server) watchPlaywright(ctx context.Context) {
	tick := time.NewTicker(pwStaleTick)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if err := s.markStale(ctx, time.Now().UTC()); err != nil && ctx.Err() == nil {
				s.logger.Warn("stale playwright checks not checked", "err", err)
			}
		}
	}
}

func (s *Server) markStale(ctx context.Context, now time.Time) error {
	checks, err := s.listUptimeChecks(ctx)
	if err != nil {
		return err
	}
	var pw []UptimeCheck
	for _, c := range checks {
		if c.Kind == "playwright" && c.Enabled {
			pw = append(pw, c)
		}
	}
	if len(pw) == 0 {
		return nil
	}
	rows, err := s.db.Query(ctx, `SELECT check_id, MAX(created_at) FROM uptime_results WHERE check_id IN (`+idList(pw)+`) GROUP BY check_id`)
	if err != nil {
		return err
	}
	last := map[int64]string{}
	for _, row := range rows.Rows {
		last[toInt(row[0])] = toString(row[1])
	}
	run := runnerFor(s)
	for _, c := range pw {
		since := fallback(last[c.ID], c.UpdatedAt)
		at, err := time.Parse(tsLayout, since)
		if err != nil {
			continue
		}
		every := time.Duration(c.IntervalS) * time.Second
		if now.Sub(at) <= every+max(time.Minute, every/10) {
			continue
		}
		res := uptimeResult{CreatedAt: now.Format(tsLayout), Error: pwStalePrefix + formatEvery(every) + "; the test is stale"}
		if err := run.record(ctx, c, res); err != nil {
			return err
		}
	}
	return nil
}

func idList(checks []UptimeCheck) string {
	ids := make([]string, len(checks))
	for i, c := range checks {
		ids[i] = strconv.FormatInt(c.ID, 10)
	}
	return strings.Join(ids, ", ")
}

func formatEvery(d time.Duration) string {
	switch {
	case d%(24*time.Hour) == 0:
		return strconv.Itoa(int(d/(24*time.Hour))) + "d"
	case d%time.Hour == 0:
		return strconv.Itoa(int(d/time.Hour)) + "h"
	case d%time.Minute == 0:
		return strconv.Itoa(int(d/time.Minute)) + "m"
	}
	return d.String()
}

// --- the check page ---

func (s *Server) handlePlaywrightRuns(w http.ResponseWriter, r *http.Request) {
	id, ok := checkID(w, r)
	if !ok {
		return
	}
	tr, err := parseRange(r.URL.Query(), s.maxRangeDays())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT id, created_at, ok, status, outcome, retry, duration_ms, error, steps, browser, project, trace_id,
  release_name, commit_sha, CASE WHEN screenshot IS NULL THEN 0 ELSE 1 END, CASE WHEN trace IS NULL THEN 0 ELSE 1 END
FROM uptime_playwright_results WHERE check_id = ? AND created_at >= ? AND created_at < ?
ORDER BY created_at DESC LIMIT 500`, id, tr.From.UTC().Format(tsLayout), tr.To.UTC().Format(tsLayout))
	if err != nil {
		if missingTable(err, "uptime_playwright_results") {
			writeJSON(w, http.StatusOK, map[string]any{"runs": []any{}, "steps": []any{}})
			return
		}
		s.fail(w, err)
		return
	}
	type stepStat struct {
		Label  string    `json:"label"`
		Runs   int       `json:"runs"`
		Failed int       `json:"failed"`
		AvgMs  float64   `json:"avg_ms"`
		P95Ms  float64   `json:"p95_ms"`
		times  []float64 `json:"-"`
	}
	stats := map[string]*stepStat{}
	var order []string
	runs := make([]map[string]any, 0, len(rows.Rows))
	for _, row := range rows.Rows {
		var steps []pwStep
		_ = json.Unmarshal([]byte(toString(row[8])), &steps)
		failedStep := ""
		for _, st := range steps {
			if st.Depth > 0 {
				continue
			}
			ss := stats[st.Title]
			if ss == nil {
				ss = &stepStat{Label: st.Title}
				stats[st.Title] = ss
				order = append(order, st.Title)
			}
			ss.Runs++
			if st.Error != "" {
				ss.Failed++
				if failedStep == "" {
					failedStep = st.Title
				}
			} else {
				ss.times = append(ss.times, st.DurationMs)
			}
		}
		runs = append(runs, map[string]any{
			"id": toInt(row[0]), "created_at": toString(row[1]), "ok": toInt(row[2]) == 1, "status": toString(row[3]), "outcome": toString(row[4]),
			"retry": toInt(row[5]), "duration_ms": row[6], "error": toString(row[7]), "failed_step": failedStep, "steps": len(steps),
			"browser": toString(row[9]), "project": toString(row[10]), "trace_id": toString(row[11]), "release": toString(row[12]),
			"commit": toString(row[13]), "screenshot": toInt(row[14]) == 1, "trace": toInt(row[15]) == 1,
		})
	}
	out := make([]*stepStat, 0, len(order))
	for _, k := range order {
		ss := stats[k]
		if n := len(ss.times); n > 0 {
			sort.Float64s(ss.times)
			sum := 0.0
			for _, t := range ss.times {
				sum += t
			}
			ss.AvgMs, ss.P95Ms = sum/float64(n), ss.times[min(n-1, int(0.95*float64(n)))]
		}
		out = append(out, ss)
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs, "steps": head(out, 50)})
}

func pwRunID(w http.ResponseWriter, r *http.Request) (int64, int64, bool) {
	id, ok := checkID(w, r)
	if !ok {
		return 0, 0, false
	}
	rid, err := strconv.ParseInt(r.PathValue("run"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid run id")
		return 0, 0, false
	}
	return id, rid, true
}

func (s *Server) handlePlaywrightRun(w http.ResponseWriter, r *http.Request) {
	id, rid, ok := pwRunID(w, r)
	if !ok {
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT created_at, ok, status, outcome, retry, duration_ms, started_at, error, steps, attachments, screenshot,
  browser, project, trace_id, release_name, commit_sha, CASE WHEN trace IS NULL THEN 0 ELSE 1 END
FROM uptime_playwright_results WHERE id = ? AND check_id = ?`, rid, id)
	if err != nil && !missingTable(err, "uptime_playwright_results") {
		s.fail(w, err)
		return
	}
	if err != nil || len(rows.Rows) == 0 {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	row := rows.Rows[0]
	steps, atts := []pwStep{}, []pwAttachment{}
	_ = json.Unmarshal([]byte(toString(row[8])), &steps)
	_ = json.Unmarshal([]byte(toString(row[9])), &atts)
	shot := ""
	if v := toString(row[10]); v != "" {
		shot = "data:" + v
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": rid, "created_at": toString(row[0]), "ok": toInt(row[1]) == 1, "status": toString(row[2]), "outcome": toString(row[3]),
		"retry": toInt(row[4]), "duration_ms": row[5], "started_at": toString(row[6]), "error": toString(row[7]), "steps": steps,
		"attachments": atts, "screenshot": shot, "browser": toString(row[11]), "project": toString(row[12]), "trace_id": toString(row[13]),
		"release": toString(row[14]), "commit": toString(row[15]), "trace": toInt(row[16]) == 1,
	})
}

// handlePlaywrightTrace downloads a run's trace, to open in trace.playwright.dev.
func (s *Server) handlePlaywrightTrace(w http.ResponseWriter, r *http.Request) {
	id, rid, ok := pwRunID(w, r)
	if !ok {
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT trace FROM uptime_playwright_results WHERE id = ? AND check_id = ?`, rid, id)
	if err != nil && !missingTable(err, "uptime_playwright_results") {
		s.fail(w, err)
		return
	}
	var body []byte
	if err == nil && len(rows.Rows) == 1 {
		body, _ = base64.StdEncoding.DecodeString(toString(rows.Rows[0][0]))
	}
	if len(body) == 0 {
		writeError(w, http.StatusNotFound, "no trace kept for this run")
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="trace-%d-%d.zip"`, id, rid))
	_, _ = w.Write(body)
}
