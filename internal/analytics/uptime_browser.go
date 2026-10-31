package analytics

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/styropyr0/visionary/internal/uptimecore"
	sdk "github.com/styropyr0/visionary/sdks/go"
)

// Browser checks are uptime checks of kind "browser": instead of one request
// they play a list of steps in a headless browser (uptime_browser_run.go).
// They share the schedule, results, incidents, alerts and status page of HTTP
// checks. Their steps and secrets live in uptime_browser_checks, and each
// run's step results, console and network errors and screenshots in
// uptime_browser_results, next to its uptime_results row.

// BrowserStep is one step of a browser check.
type BrowserStep struct {
	Action   string `json:"action"`
	Selector string `json:"selector,omitempty"`
	Text     string `json:"text,omitempty"`
	Value    string `json:"value,omitempty"`
	Count    int    `json:"count,omitempty"`
	Op       string `json:"op,omitempty"`
	Ms       int    `json:"ms,omitempty"`
	Label    string `json:"label,omitempty"`
	// MaxMs fails the step when it takes longer.
	MaxMs int `json:"max_ms,omitempty"`
	// Name is the variable of extract, the cookie of set_cookie, the key of
	// set_storage and the file of upload.
	Name string `json:"name,omitempty"`
	// Attr is what extract reads: text (default), value or an attribute.
	Attr  string `json:"attr,omitempty"`
	Regex string `json:"regex,omitempty"`
	// Target is where drag drops.
	Target string `json:"target,omitempty"`
	// Scope is the cookie's URL or domain, local or session storage, or the
	// origin a permission is granted to.
	Scope      string  `json:"scope,omitempty"`
	Permission string  `json:"permission,omitempty"`
	Lat        float64 `json:"lat,omitempty"`
	Lon        float64 `json:"lon,omitempty"`
}

const (
	browserMaxSteps     = 50
	browserMaxSecrets   = 20
	browserMaxTimeoutMs = uptimecore.MaxBrowserTimeoutMs
)

var (
	browserActions = map[string]bool{"goto": true, "click": true, "type": true, "press": true, "select": true, "wait_selector": true,
		"wait_text": true, "wait_navigation": true, "assert_text": true, "assert_no_text": true, "assert_url": true, "assert_count": true,
		"wait": true, "script": true, "extract": true, "hover": true, "drag": true, "upload": true, "frame": true, "set_cookie": true,
		"set_storage": true, "grant_permission": true}
	secretNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	secretRefRe  = regexp.MustCompile(`\{\{secret\.([A-Za-z0-9_]+)\}\}`)
)

// uptimeAddedColumns are columns added to the uptime tables after their
// first release.
var uptimeAddedColumns = map[string][]sdk.Column{
	"uptime_checks":          {{Name: "kind", Kind: "text"}},
	"uptime_results":         {{Name: "lcp_ms", Kind: "real"}, {Name: "cls", Kind: "real"}},
	"uptime_browser_checks":  {{Name: "options", Kind: "bigtext"}},
	"uptime_browser_results": {{Name: "trace_id", Kind: "text"}, {Name: "warning", Kind: "text"}, {Name: "requests", Kind: "bigtext"}, {Name: "step_shots", Kind: "bigtext"}},
}

func (s *Server) uptimeBrowserSchema() []string {
	id, key, ts, big, num := "INTEGER PRIMARY KEY AUTOINCREMENT", "INTEGER", "TEXT", "TEXT", "INTEGER"
	ine := "IF NOT EXISTS "
	switch s.q.d {
	case sdk.Postgres:
		id, key = "BIGSERIAL PRIMARY KEY", "BIGINT"
	case sdk.MySQL:
		id, key, ts, big, num, ine = "BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY", "BIGINT", "VARCHAR(32)", "MEDIUMTEXT", "INT", ""
	}
	tail := ""
	if s.q.d == sdk.MySQL {
		tail = " CHARACTER SET utf8mb4"
	}
	return []string{`CREATE TABLE IF NOT EXISTS uptime_browser_checks (
    check_id         ` + key + ` NOT NULL PRIMARY KEY,
    steps            ` + big + ` NOT NULL,
    secrets          ` + big + ` NOT NULL,
    final_screenshot ` + num + ` NOT NULL DEFAULT 0
)` + tail + `;
CREATE TABLE IF NOT EXISTS uptime_browser_results (
    id               ` + id + `,
    created_at       ` + ts + ` NOT NULL,
    check_id         ` + key + ` NOT NULL,
    ok               ` + num + ` NOT NULL,
    failed_step      ` + num + ` NOT NULL DEFAULT -1,
    steps            ` + big + ` NOT NULL,
    console          ` + big + `,
    network          ` + big + `,
    screenshot       ` + big + `,
    final_screenshot ` + big + `
)` + tail + `;
CREATE INDEX ` + ine + `idx_uptime_browser_results_check ON uptime_browser_results (check_id, created_at);
CREATE INDEX ` + ine + `idx_uptime_browser_results_created ON uptime_browser_results (created_at)`}
}

// normalizeBrowser validates a browser check; the start URL is its first
// step's. prev is the stored check on update, whose masked secrets survive.
func (c *UptimeCheck) normalizeBrowser(prev *UptimeCheck) error {
	c.Method, c.ExpectStatus, c.BodyContains, c.Headers, c.FollowRedirects = "GET", "200-299", "", map[string]string{}, true
	if len(c.Steps) == 0 || len(c.Steps) > browserMaxSteps {
		return fmt.Errorf("a browser check needs 1–%d steps", browserMaxSteps)
	}
	if len(c.Secrets) > browserMaxSecrets {
		return fmt.Errorf("at most %d secrets", browserMaxSecrets)
	}
	secrets := make(map[string]string, len(c.Secrets))
	for k, v := range c.Secrets {
		if !secretNameRe.MatchString(k) {
			return fmt.Errorf("secret name %q: use letters, digits and _", k)
		}
		if len(v) > 1000 {
			return fmt.Errorf("secret %s is too long", k)
		}
		if v == uptimeMask {
			old, ok := "", false
			if prev != nil {
				old, ok = prev.Secrets[k]
			}
			if !ok {
				return fmt.Errorf("enter the value of secret %s again", k)
			}
			v = old
		}
		secrets[k] = v
	}
	c.Secrets = secrets
	if err := c.NormalizeFiles(prevFiles(prev)); err != nil {
		return err
	}
	files := map[string]bool{}
	for _, f := range c.Files {
		files[f.Name] = true
	}
	vars := map[string]bool{}
	for i := range c.Steps {
		st := &c.Steps[i]
		st.Action = strings.TrimSpace(st.Action)
		st.Selector = strings.TrimSpace(st.Selector)
		st.Target = strings.TrimSpace(st.Target)
		st.Label = strings.TrimSpace(st.Label)
		if err := st.validate(); err != nil {
			return fmt.Errorf("step %d: %w", i+1, err)
		}
		if err := st.validateMore(files); err != nil {
			return fmt.Errorf("step %d: %w", i+1, err)
		}
		for _, v := range st.refs() {
			if !vars[v] {
				return fmt.Errorf("step %d uses variable %s, which no earlier extract step sets", i+1, v)
			}
		}
		if st.Action == "extract" {
			vars[st.Name] = true
		}
		for _, m := range secretRefRe.FindAllStringSubmatch(st.Value, -1) {
			if _, ok := secrets[m[1]]; !ok {
				return fmt.Errorf("step %d uses secret %s, which is not defined", i+1, m[1])
			}
		}
	}
	if c.Steps[0].Action != "goto" {
		return errors.New("the first step must go to a URL")
	}
	if err := c.NormalizeTrace(); err != nil {
		return err
	}
	if err := c.NormalizeDevice(); err != nil {
		return err
	}
	c.URL = c.Steps[0].Value
	if c.IntervalS == 0 {
		c.IntervalS = 300
	}
	if c.TimeoutMs == 0 {
		c.TimeoutMs = 30000
	}
	if c.IntervalS < uptimeMinInterval || c.IntervalS > uptimeMaxInterval {
		return errors.New("interval must be between 30 seconds and 1 hour")
	}
	if c.TimeoutMs < 1000 || c.TimeoutMs > browserMaxTimeoutMs {
		return errors.New("timeout must be between 1 and 120 seconds")
	}
	if c.TimeoutMs > c.IntervalS*1000 {
		return errors.New("timeout must be shorter than the interval")
	}
	if c.DegradedMs < 0 || c.DegradedMs > browserMaxTimeoutMs {
		return errors.New("slow threshold must be between 0 and 120000 ms")
	}
	if err := c.BrowserLimits.Validate(c.DegradedMs); err != nil {
		return err
	}
	if !c.OnStatusPage {
		c.StatusShowURL = false
	}
	return nil
}

func (b *BrowserStep) validate() error {
	if !browserActions[b.Action] {
		return fmt.Errorf("unknown action %q", b.Action)
	}
	if len(b.Selector) > 500 || len(b.Text) > 500 || len(b.Label) > 120 || len(b.Value) > 5000 {
		return errors.New("a field is too long")
	}
	if b.MaxMs < 0 || b.MaxMs > browserMaxTimeoutMs {
		return fmt.Errorf("the step's time limit must be between 0 and %d ms", browserMaxTimeoutMs)
	}
	need := func(field, v string) error {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("%s is required", field)
		}
		return nil
	}
	switch b.Action {
	case "goto":
		b.Value = strings.TrimSpace(b.Value)
		u, err := url.Parse(b.Value)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || len(b.Value) > 2048 {
			return errors.New("the URL must be an absolute http:// or https:// address")
		}
	case "click", "wait_selector":
		if b.Selector == "" && strings.TrimSpace(b.Text) == "" {
			return errors.New("give a CSS selector or the element's text")
		}
	case "type":
		return need("selector", b.Selector)
	case "press":
		return need("key", b.Value)
	case "select":
		if err := need("selector", b.Selector); err != nil {
			return err
		}
	case "wait_text", "assert_text", "assert_no_text":
		return need("text", b.Text)
	case "assert_url":
		return need("URL text", b.Value)
	case "assert_count":
		if b.Op == "" {
			b.Op = "eq"
		}
		if b.Op != "eq" && b.Op != "gte" && b.Op != "lte" {
			return errors.New("compare with eq, gte or lte")
		}
		if b.Count < 0 || b.Count > 100000 {
			return errors.New("count out of range")
		}
		return need("selector", b.Selector)
	case "wait":
		if b.Ms < 1 || b.Ms > 30000 {
			return errors.New("wait between 1 and 30000 ms")
		}
	case "script":
		return need("script", b.Value)
	}
	return nil
}

// loadBrowserConfigs fills in the steps and secrets of browser checks.
func (s *Server) loadBrowserConfigs(ctx context.Context, checks []UptimeCheck) error {
	ids := []string{}
	for _, c := range checks {
		if c.Kind == "browser" {
			ids = append(ids, strconv.FormatInt(c.ID, 10))
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.db.Query(ctx, `SELECT check_id, steps, secrets, final_screenshot, options FROM uptime_browser_checks WHERE check_id IN (`+strings.Join(ids, ", ")+`)`)
	if err != nil {
		return err
	}
	byID := map[int64][]any{}
	for _, row := range rows.Rows {
		if len(row) == 5 {
			byID[toInt(row[0])] = row
		}
	}
	for i := range checks {
		c := &checks[i]
		if c.Kind != "browser" {
			continue
		}
		c.Steps, c.Secrets = []BrowserStep{}, map[string]string{}
		if row, ok := byID[c.ID]; ok {
			_ = json.Unmarshal([]byte(toString(row[1])), &c.Steps)
			_ = json.Unmarshal([]byte(toString(row[2])), &c.Secrets)
			c.FinalScreenshot = toInt(row[3]) == 1
			_ = json.Unmarshal([]byte(toString(row[4])), &c.BrowserOptions)
		}
	}
	return nil
}

func (s *Server) saveBrowserConfig(ctx context.Context, c UptimeCheck) error {
	if err := s.db.Exec(ctx, `DELETE FROM uptime_browser_checks WHERE check_id = ?`, c.ID); err != nil {
		return err
	}
	if c.Kind != "browser" {
		return nil
	}
	steps, err := json.Marshal(c.Steps)
	if err != nil {
		return err
	}
	secrets, err := json.Marshal(c.Secrets)
	if err != nil {
		return err
	}
	options, err := json.Marshal(c.BrowserOptions)
	if err != nil {
		return err
	}
	return s.db.Exec(ctx, `INSERT INTO uptime_browser_checks (check_id, steps, secrets, final_screenshot, options) VALUES (?, ?, ?, ?, ?)`,
		c.ID, string(steps), string(secrets), boolInt(c.FinalScreenshot), string(options))
}

func (s *Server) deleteBrowserData(ctx context.Context, id int64) {
	for _, t := range []string{"uptime_browser_checks", "uptime_browser_results"} {
		if err := s.db.Exec(ctx, `DELETE FROM `+t+` WHERE check_id = ?`, id); err != nil && !missingTable(err, t) {
			s.logger.Warn("browser check data not deleted", "check_id", id, "table", t, "err", err)
		}
	}
}

func (u *uptimeRunner) recordBrowser(ctx context.Context, c UptimeCheck, r uptimeResult, b browserRun) error {
	steps, _ := json.Marshal(b.Steps)
	console, _ := json.Marshal(b.Console)
	network, _ := json.Marshal(b.Network)
	cols := []string{"created_at", "check_id", "ok", "failed_step", "steps", "console", "network"}
	vals := []any{r.CreatedAt, c.ID, boolInt(r.OK), b.FailedStep, string(steps), string(console), string(network)}
	if b.TraceID != "" {
		cols, vals = append(cols, "trace_id"), append(vals, b.TraceID)
	}
	if b.Warning != "" {
		cols, vals = append(cols, "warning"), append(vals, b.Warning)
	}
	if b.Requests != nil {
		reqs, _ := json.Marshal(b.Requests)
		cols, vals = append(cols, "requests"), append(vals, string(reqs))
	}
	shots := slices.ContainsFunc(b.StepShots, func(s []byte) bool { return s != nil })
	if shots {
		enc, _ := json.Marshal(encodeShots(b.StepShots))
		cols, vals = append(cols, "step_shots"), append(vals, string(enc))
	}
	defer func() {
		if err := u.s.pruneRunDetail(ctx, c.ID); err != nil {
			u.s.logger.Warn("old browser run detail not dropped", "check_id", c.ID, "err", err)
		}
	}()
	if b.Screenshot != nil {
		cols, vals = append(cols, "screenshot"), append(vals, base64.StdEncoding.EncodeToString(b.Screenshot))
	}
	if b.Final != nil {
		cols, vals = append(cols, "final_screenshot"), append(vals, base64.StdEncoding.EncodeToString(b.Final))
	}
	return u.s.db.Exec(ctx, "INSERT INTO uptime_browser_results ("+strings.Join(cols, ", ")+") VALUES (?"+strings.Repeat(", ?", len(cols)-1)+")", vals...)
}

// --- handlers ---

// handleBrowserRunNow plays a browser check now and records the run.
func (s *Server) handleBrowserRunNow(w http.ResponseWriter, r *http.Request, run *uptimeRunner, c UptimeCheck) {
	if !run.browser.Available() {
		st := run.browser.Status()
		writeError(w, http.StatusConflict, st.Error+". "+st.Help)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Duration(c.TimeoutMs)*time.Millisecond+2*time.Minute)
	defer cancel()
	res, b, err := run.runBrowser(ctx, c)
	if err != nil {
		writeError(w, http.StatusConflict, "the browser did not start: "+userMessage(err))
		return
	}
	if err := run.record(ctx, c, res); err != nil {
		s.fail(w, err)
		return
	}
	if err := run.recordBrowser(ctx, c, res, b); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "uptime.run", c.Name, map[string]any{"id": c.ID, "ok": res.OK})
	writeJSON(w, http.StatusOK, map[string]any{"result": res})
}

func (s *Server) handleUptimeBrowser(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, runnerFor(s).browser.Status())
}

// handleBrowserRuns lists a browser check's runs in the range, with how each
// step did across them.
func (s *Server) handleBrowserRuns(w http.ResponseWriter, r *http.Request) {
	id, ok := checkID(w, r)
	if !ok {
		return
	}
	tr, err := parseRange(r.URL.Query(), s.maxRangeDays())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT b.id, b.created_at, b.ok, b.failed_step, b.steps, u.duration_ms, u.error, u.lcp_ms, u.cls, u.ttfb_ms, u.retried,
  CASE WHEN b.screenshot IS NULL THEN 0 ELSE 1 END, CASE WHEN b.final_screenshot IS NULL THEN 0 ELSE 1 END, u.degraded, b.warning
FROM uptime_browser_results b LEFT JOIN uptime_results u ON u.check_id = b.check_id AND u.created_at = b.created_at
WHERE b.check_id = ? AND b.created_at >= ? AND b.created_at < ?
ORDER BY b.created_at DESC LIMIT 500`, id, tr.From.UTC().Format(tsLayout), tr.To.UTC().Format(tsLayout))
	if err != nil {
		if missingTable(err, "uptime_browser_results") {
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
	var stats []*stepStat
	runs := make([]map[string]any, 0, len(rows.Rows))
	for _, row := range rows.Rows {
		var steps []stepResult
		_ = json.Unmarshal([]byte(toString(row[4])), &steps)
		for i, st := range steps {
			if i >= len(stats) {
				stats = append(stats, &stepStat{Label: st.Label})
			}
			ss := stats[i]
			if st.Status == "skipped" {
				continue
			}
			ss.Runs++
			if st.Status == "failed" {
				ss.Failed++
			} else {
				ss.times = append(ss.times, st.DurationMs)
			}
		}
		failedLabel := ""
		if f := int(toInt(row[3])); f >= 0 && f < len(steps) {
			failedLabel = steps[f].Label
		}
		runs = append(runs, map[string]any{
			"id": toInt(row[0]), "created_at": toString(row[1]), "ok": toInt(row[2]) == 1, "failed_step": toInt(row[3]), "failed_label": failedLabel,
			"duration_ms": row[5], "error": toString(row[6]), "lcp_ms": row[7], "cls": row[8], "ttfb_ms": row[9], "retried": toInt(row[10]) == 1,
			"screenshot": toInt(row[11]) == 1, "final_screenshot": toInt(row[12]) == 1, "steps": len(steps),
			"degraded": toInt(row[13]) == 1, "warning": toString(row[14]),
		})
	}
	for _, ss := range stats {
		if n := len(ss.times); n > 0 {
			sort.Float64s(ss.times)
			sum := 0.0
			for _, t := range ss.times {
				sum += t
			}
			ss.AvgMs = sum / float64(n)
			ss.P95Ms = ss.times[min(n-1, int(0.95*float64(n)))]
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs, "steps": stats})
}

// handleBrowserRun is one run in full, screenshots included.
func (s *Server) handleBrowserRun(w http.ResponseWriter, r *http.Request) {
	id, ok := checkID(w, r)
	if !ok {
		return
	}
	rid, err := strconv.ParseInt(r.PathValue("run"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid run id")
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT b.created_at, b.ok, b.failed_step, b.steps, b.console, b.network, b.screenshot, b.final_screenshot,
  u.duration_ms, u.error, u.lcp_ms, u.cls, u.ttfb_ms, u.status, u.retried, b.trace_id, u.degraded, b.warning, b.requests, b.step_shots
FROM uptime_browser_results b LEFT JOIN uptime_results u ON u.check_id = b.check_id AND u.created_at = b.created_at
WHERE b.id = ? AND b.check_id = ?`, rid, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if len(rows.Rows) == 0 {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	row := rows.Rows[0]
	var steps []stepResult
	var console []string
	var network []netFailure
	_ = json.Unmarshal([]byte(toString(row[3])), &steps)
	_ = json.Unmarshal([]byte(toString(row[4])), &console)
	_ = json.Unmarshal([]byte(toString(row[5])), &network)
	var requests []netRequest
	var stepShots []string
	_ = json.Unmarshal([]byte(toString(row[18])), &requests)
	_ = json.Unmarshal([]byte(toString(row[19])), &stepShots)
	for i, b := range stepShots {
		if b != "" {
			stepShots[i] = "data:image/jpeg;base64," + b
		}
	}
	img := func(v any) string {
		if b := toString(v); b != "" {
			return "data:image/jpeg;base64," + b
		}
		return ""
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": rid, "created_at": toString(row[0]), "ok": toInt(row[1]) == 1, "failed_step": toInt(row[2]),
		"steps": nonNil(steps), "console": nonNil(console), "network": nonNil(network),
		"screenshot": img(row[6]), "final_screenshot": img(row[7]),
		"duration_ms": row[8], "error": toString(row[9]), "lcp_ms": row[10], "cls": row[11], "ttfb_ms": row[12], "status": toInt(row[13]), "retried": toInt(row[14]) == 1,
		"trace_id": toString(row[15]), "trace_found": s.traceHasRows(r.Context(), toString(row[15])),
		"degraded": toInt(row[16]) == 1, "warning": toString(row[17]), "requests": nonNil(requests), "step_shots": nonNil(stepShots),
	})
}
