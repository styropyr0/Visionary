package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	sdk "github.com/styropyr0/visionary/sdks/go"
)

// Runs from CI: POST /api/uptime/runs runs the checks picked by tag or id
// once, now, outside their schedule, and returns a batch the caller polls
// (or long-polls) for each check's result. A batch remembers the release and
// commit it tested, so the CI page shows it beside the pipeline of that
// commit. With environment_url_override the checks run against another
// origin, such as a preview deployment; those results stay in the batch and
// never count toward the checks' uptime. Batches live in uptime_run_batches
// and their checks in uptime_run_items; execution is in uptime_runs_exec.go.

const (
	runMaxChecks   = 50
	runMaxWaitS    = 900
	runPollCapS    = 170 // under the server's write timeout
	runBatchTTL    = 15 * time.Minute
	runListDefault = 20
)

var commitRe = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

func init() {
	scopedTables["UPTIME_RUN_BATCHES"] = scopeRule{by: ownedOnly}
	scopedTables["UPTIME_RUN_ITEMS"] = scopeRule{by: byCheck}
}

func (s *Server) uptimeRunsSchema() []string {
	id, key, ts, num, txt := "INTEGER PRIMARY KEY AUTOINCREMENT", "INTEGER", "TEXT", "REAL", "TEXT"
	tail, ine := "", "IF NOT EXISTS "
	switch s.q.d {
	case sdk.Postgres:
		id, key, num = "BIGSERIAL PRIMARY KEY", "BIGINT", "DOUBLE PRECISION"
	case sdk.MySQL:
		id, key, ts, num, txt, tail, ine = "BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY", "BIGINT", "VARCHAR(32)", "DOUBLE", "VARCHAR(64)", " CHARACTER SET utf8mb4", ""
	}
	return []string{`CREATE TABLE IF NOT EXISTS uptime_run_batches (
    id              ` + id + `,
    created_at      ` + ts + ` NOT NULL,
    finished_at     ` + ts + `,
    deadline_at     ` + ts + ` NOT NULL,
    status          ` + txt + ` NOT NULL,
    workspace_id    ` + key + `,
    release_name    TEXT,
    commit_sha      ` + txt + `,
    environment_url TEXT,
    tags            TEXT,
    created_by      TEXT,
    total           INTEGER NOT NULL DEFAULT 0,
    passed          INTEGER NOT NULL DEFAULT 0,
    failed          INTEGER NOT NULL DEFAULT 0,
    skipped         INTEGER NOT NULL DEFAULT 0
)` + tail + `;
CREATE INDEX ` + ine + `idx_uptime_run_batches_created ON uptime_run_batches (created_at);
CREATE INDEX ` + ine + `idx_uptime_run_batches_commit ON uptime_run_batches (commit_sha);
CREATE TABLE IF NOT EXISTS uptime_run_items (
    id          ` + id + `,
    batch_id    ` + key + ` NOT NULL,
    created_at  ` + ts + ` NOT NULL,
    check_id    ` + key + ` NOT NULL,
    check_name  TEXT NOT NULL,
    kind        ` + txt + ` NOT NULL,
    status      ` + txt + ` NOT NULL,
    url         TEXT,
    started_at  ` + ts + `,
    finished_at ` + ts + `,
    duration_ms ` + num + `,
    http_status INTEGER,
    error       TEXT,
    detail      TEXT
)` + tail + `;
CREATE INDEX ` + ine + `idx_uptime_run_items_batch ON uptime_run_items (batch_id);
CREATE INDEX ` + ine + `idx_uptime_run_items_created ON uptime_run_items (created_at)`}
}

func (s *Server) uptimeRunsRoutes(mux *http.ServeMux) {
	mux.Handle("POST /api/uptime/runs", s.authed(s.handleRunsCreate))
	mux.Handle("GET /api/uptime/runs", s.authed(s.handleRunsList))
	mux.Handle("GET /api/uptime/runs/{id}", s.authed(s.handleRunsGet))
}

// runBatch is one run from CI (or the dashboard) as the API shows it.
type runBatch struct {
	ID             int64     `json:"id"`
	CreatedAt      string    `json:"created_at"`
	FinishedAt     string    `json:"finished_at,omitempty"`
	Status         string    `json:"status"` // running, passed, failed
	Done           bool      `json:"done"`
	Release        string    `json:"release,omitempty"`
	Commit         string    `json:"commit,omitempty"`
	EnvironmentURL string    `json:"environment_url,omitempty"`
	Tags           []string  `json:"tags"`
	CreatedBy      string    `json:"created_by,omitempty"`
	Total          int64     `json:"total"`
	Passed         int64     `json:"passed"`
	Failed         int64     `json:"failed"`
	Skipped        int64     `json:"skipped"`
	Items          []runItem `json:"items,omitempty"`
	deadline       string
}

// runItem is one check of a batch. Status is queued, running, passed,
// failed, error (it could not run) or skipped.
type runItem struct {
	id         int64
	CheckID    int64           `json:"check_id"`
	CheckName  string          `json:"check_name"`
	Kind       string          `json:"kind"`
	Status     string          `json:"status"`
	URL        string          `json:"url,omitempty"`
	StartedAt  string          `json:"started_at,omitempty"`
	FinishedAt string          `json:"finished_at,omitempty"`
	DurationMs *float64        `json:"duration_ms"`
	HTTPStatus int64           `json:"http_status,omitempty"`
	Error      string          `json:"error,omitempty"`
	Detail     json.RawMessage `json:"detail,omitempty"`
}

func runItemDone(status string) bool { return status != "queued" && status != "running" }

// canTrigger: editors, and CI keys, may run checks.
func (s *Server) canTrigger(r *http.Request) bool {
	return s.can(r, "edit") || isCIKey(r)
}

type runRequest struct {
	Tags                   []string `json:"tags"`
	CheckIDs               []int64  `json:"check_ids"`
	Wait                   bool     `json:"wait"`
	TimeoutS               int      `json:"timeout_s"`
	Release                string   `json:"release"`
	Commit                 string   `json:"commit"`
	EnvironmentURLOverride string   `json:"environment_url_override"`
}

func (s *Server) handleRunsCreate(w http.ResponseWriter, r *http.Request) {
	if !s.canTrigger(r) {
		writeError(w, http.StatusForbidden, "you do not have permission to run checks")
		return
	}
	var body runRequest
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tags, err := normalizeTags(body.Tags)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(tags) == 0 && len(body.CheckIDs) == 0 {
		writeError(w, http.StatusBadRequest, "pick checks with tags or check_ids")
		return
	}
	body.Commit = strings.ToLower(strings.TrimSpace(body.Commit))
	if body.Commit != "" && !commitRe.MatchString(body.Commit) {
		writeError(w, http.StatusBadRequest, "commit must be a hex SHA of 7 to 64 characters")
		return
	}
	body.Release = strings.TrimSpace(body.Release)
	if len(body.Release) > 200 {
		writeError(w, http.StatusBadRequest, "release is limited to 200 characters")
		return
	}
	if body.TimeoutS < 0 || body.TimeoutS > runMaxWaitS {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("timeout_s must be 0 to %d", runMaxWaitS))
		return
	}
	checks, err := s.listUptimeChecks(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	for _, id := range body.CheckIDs {
		if !slices.ContainsFunc(checks, func(c UptimeCheck) bool { return c.ID == id }) {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("no check with id %d", id))
			return
		}
	}
	picked := checksTagged(checks, tags, body.CheckIDs)
	if len(picked) == 0 {
		writeError(w, http.StatusBadRequest, "no check has any of these tags")
		return
	}
	if len(picked) > runMaxChecks {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("%d checks match; a run takes at most %d", len(picked), runMaxChecks))
		return
	}
	override, err := s.parseOverride(body.EnvironmentURLOverride, picked)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	b, items, err := s.createBatch(r, picked, tags, body, override)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "uptime.ci_run", strconv.FormatInt(b.ID, 10), map[string]any{"checks": len(picked), "tags": tags, "commit": b.Commit,
		"release": b.Release, "environment_url": b.EnvironmentURL})
	go s.executeBatch(b, items, picked, override)

	if body.Wait {
		timeout := body.TimeoutS
		if timeout == 0 {
			timeout = 300
		}
		b, err = s.waitBatch(r.Context(), b.ID, min(timeout, runPollCapS))
		if err != nil {
			s.fail(w, err)
			return
		}
	}
	code := http.StatusAccepted
	if b.Done {
		code = http.StatusOK
	}
	writeJSON(w, code, map[string]any{"batch": b})
}

func (s *Server) createBatch(r *http.Request, picked []UptimeCheck, tags []string, body runRequest, override string) (runBatch, []runItem, error) {
	ctx := r.Context()
	now := time.Now().UTC()
	sess := sessionFrom(r)
	b := runBatch{CreatedAt: now.Format(tsLayout), Status: "running", Release: body.Release, Commit: body.Commit, EnvironmentURL: override,
		Tags: tags, CreatedBy: fallback(sess.Name, sess.UserID), Total: int64(len(picked)), deadline: now.Add(runBatchTTL).Format(tsLayout)}
	// The batch belongs to the caller's workspace, so only its members see it.
	ws := "NULL"
	if sc := scopeFrom(ctx); sc != nil {
		if sc.home > 0 {
			ws = strconv.FormatInt(sc.home, 10)
		} else if len(sc.ids) > 0 {
			ws = strconv.FormatInt(sc.ids[0], 10)
		}
	}
	tagsJSON, _ := json.Marshal(tags)
	var err error
	b.ID, err = s.db.InsertID(ctx, `INSERT INTO uptime_run_batches (created_at, deadline_at, status, workspace_id, release_name, commit_sha,
  environment_url, tags, created_by, total) VALUES (?, ?, 'running', `+ws+`, ?, ?, ?, ?, ?, ?)`,
		b.CreatedAt, b.deadline, b.Release, b.Commit, b.EnvironmentURL, string(tagsJSON), b.CreatedBy, b.Total)
	if err != nil {
		return b, nil, err
	}
	// Seven values a row keeps each statement under D1's 100 parameters.
	for chunk := range slices.Chunk(picked, 12) {
		marks := make([]string, len(chunk))
		args := make([]any, 0, 7*len(chunk))
		for i, c := range chunk {
			marks[i] = "(?, ?, ?, ?, ?, 'queued', ?)"
			to := override
			if hostKinds[c.Kind] {
				to = ""
			}
			args = append(args, b.ID, b.CreatedAt, c.ID, c.Name, c.Kind, redactURL(overrideURL(c.URL, to)))
		}
		if err := s.db.Exec(ctx, `INSERT INTO uptime_run_items (batch_id, created_at, check_id, check_name, kind, status, url) VALUES `+
			strings.Join(marks, ", "), args...); err != nil {
			return b, nil, err
		}
	}
	items, err := s.batchItems(unscoped(ctx), b.ID)
	return b, items, err
}

func (s *Server) handleRunsGet(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid run id")
		return
	}
	wait := 0
	if q := r.URL.Query(); q.Get("wait") == "1" || q.Get("wait") == "true" {
		wait = 60
		if n, err := strconv.Atoi(q.Get("timeout_s")); err == nil && n > 0 {
			wait = min(n, runPollCapS)
		}
	}
	var b runBatch
	if wait > 0 {
		b, err = s.waitBatch(r.Context(), id, wait)
	} else {
		b, err = s.loadBatch(r.Context(), id, true)
	}
	if errors.Is(err, errRunNotFound) {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"batch": b})
}

func (s *Server) handleRunsList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = runListDefault
	}
	commit := strings.ToLower(strings.TrimSpace(q.Get("commit")))
	if commit != "" && !commitRe.MatchString(commit) {
		writeError(w, http.StatusBadRequest, "commit must be a hex SHA")
		return
	}
	list, err := s.listBatches(r.Context(), commit, limit)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"batches": list, "can_run": s.canTrigger(r)})
}

var errRunNotFound = errors.New("run not found")

const batchCols = `id, created_at, finished_at, deadline_at, status, release_name, commit_sha, environment_url, tags, created_by, total, passed, failed, skipped`

func scanBatch(row []any) runBatch {
	b := runBatch{ID: toInt(row[0]), CreatedAt: toString(row[1]), FinishedAt: toString(row[2]), deadline: toString(row[3]), Status: toString(row[4]),
		Release: toString(row[5]), Commit: toString(row[6]), EnvironmentURL: toString(row[7]), CreatedBy: toString(row[9]),
		Total: toInt(row[10]), Passed: toInt(row[11]), Failed: toInt(row[12]), Skipped: toInt(row[13]), Tags: []string{}}
	_ = json.Unmarshal([]byte(toString(row[8])), &b.Tags)
	b.Done = b.Status != "running"
	return b
}

// listBatches are the newest batches, those testing commit when it is set
// (either SHA may be the short one).
func (s *Server) listBatches(ctx context.Context, commit string, limit int) ([]runBatch, error) {
	where, args := "", []any{}
	if commit != "" {
		where, args = ` WHERE SUBSTR(commit_sha, 1, 7) = ?`, []any{commit[:7]}
	}
	rows, err := s.db.Query(ctx, `SELECT `+batchCols+` FROM uptime_run_batches`+where+` ORDER BY created_at DESC, id DESC LIMIT `+strconv.Itoa(limit), args...)
	if err != nil {
		if missingTable(err, "uptime_run_batches") {
			return []runBatch{}, nil
		}
		return nil, err
	}
	out := make([]runBatch, 0, len(rows.Rows))
	for _, row := range rows.Rows {
		if len(row) != 14 {
			continue
		}
		b := scanBatch(row)
		if commit != "" && !strings.HasPrefix(b.Commit, commit) && !strings.HasPrefix(commit, b.Commit) {
			continue
		}
		s.expireBatch(ctx, &b)
		out = append(out, b)
	}
	return out, nil
}

func (s *Server) loadBatch(ctx context.Context, id int64, withItems bool) (runBatch, error) {
	rows, err := s.db.Query(ctx, `SELECT `+batchCols+` FROM uptime_run_batches WHERE id = ?`, id)
	if err != nil {
		if missingTable(err, "uptime_run_batches") {
			return runBatch{}, errRunNotFound
		}
		return runBatch{}, err
	}
	if len(rows.Rows) == 0 || len(rows.Rows[0]) != 14 {
		return runBatch{}, errRunNotFound
	}
	b := scanBatch(rows.Rows[0])
	s.expireBatch(ctx, &b)
	if withItems {
		if b.Items, err = s.batchItems(ctx, id); err != nil {
			return b, err
		}
	}
	return b, nil
}

// expireBatch closes a batch whose server stopped before it finished.
func (s *Server) expireBatch(ctx context.Context, b *runBatch) {
	if b.Status != "running" || b.deadline == "" || uptimeNow() < b.deadline {
		return
	}
	cctx := unscoped(ctx)
	_ = s.db.Exec(cctx, `UPDATE uptime_run_items SET status = 'error', error = 'not finished: the server stopped during the run' WHERE batch_id = ? AND status IN ('queued', 'running')`, b.ID)
	if err := s.finishBatch(cctx, b.ID); err == nil {
		if nb, err := s.loadBatch(cctx, b.ID, false); err == nil {
			*b = nb
		}
	}
}

func (s *Server) batchItems(ctx context.Context, id int64) ([]runItem, error) {
	rows, err := s.db.Query(ctx, `SELECT id, check_id, check_name, kind, status, url, started_at, finished_at, duration_ms, http_status, error, detail
FROM uptime_run_items WHERE batch_id = ? ORDER BY check_name, id`, id)
	if err != nil {
		return nil, err
	}
	out := make([]runItem, 0, len(rows.Rows))
	for _, row := range rows.Rows {
		if len(row) != 12 {
			continue
		}
		it := runItem{id: toInt(row[0]), CheckID: toInt(row[1]), CheckName: toString(row[2]), Kind: toString(row[3]), Status: toString(row[4]),
			URL: toString(row[5]), StartedAt: toString(row[6]), FinishedAt: toString(row[7]), DurationMs: cellPtr(row[8]), HTTPStatus: toInt(row[9]),
			Error: toString(row[10])}
		if d := toString(row[11]); d != "" && json.Valid([]byte(d)) {
			it.Detail = json.RawMessage(d)
		}
		out = append(out, it)
	}
	return out, nil
}

// finishBatch counts the items and closes the batch once none is pending.
func (s *Server) finishBatch(ctx context.Context, id int64) error {
	rows, err := s.db.Query(ctx, `SELECT status, COUNT(*) FROM uptime_run_items WHERE batch_id = ? GROUP BY status`, id)
	if err != nil {
		return err
	}
	var passed, failed, skipped, pending int64
	for _, row := range rows.Rows {
		n := toInt(row[1])
		switch toString(row[0]) {
		case "passed":
			passed += n
		case "failed", "error":
			failed += n
		case "skipped":
			skipped += n
		default:
			pending += n
		}
	}
	if pending > 0 {
		return s.db.Exec(ctx, `UPDATE uptime_run_batches SET passed = ?, failed = ?, skipped = ? WHERE id = ?`, passed, failed, skipped, id)
	}
	status := "passed"
	if failed > 0 {
		status = "failed"
	}
	return s.db.Exec(ctx, `UPDATE uptime_run_batches SET status = ?, finished_at = ?, passed = ?, failed = ?, skipped = ? WHERE id = ? AND status = 'running'`,
		status, uptimeNow(), passed, failed, skipped, id)
}

// waitBatch long-polls until the batch is done or timeoutS passes.
func (s *Server) waitBatch(ctx context.Context, id int64, timeoutS int) (runBatch, error) {
	until := time.Now().Add(time.Duration(timeoutS) * time.Second)
	for {
		b, err := s.loadBatch(ctx, id, true)
		if err != nil || b.Done || time.Now().After(until) {
			return b, err
		}
		select {
		case <-ctx.Done():
			return b, nil
		case <-time.After(time.Second):
		}
	}
}

// commitBatches are the batches that tested commit, for the CI page.
func (s *Server) commitBatches(ctx context.Context, commit string) []runBatch {
	commit = strings.ToLower(commit)
	if !commitRe.MatchString(commit) {
		return []runBatch{}
	}
	list, err := s.listBatches(ctx, commit, 20)
	if err != nil {
		return []runBatch{}
	}
	for i := range list {
		list[i].Items, _ = s.batchItems(ctx, list[i].ID)
	}
	return list
}
