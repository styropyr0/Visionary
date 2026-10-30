package analytics

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
)

// The CI page: pipelines with their success rate, durations and queue time,
// slow and failing jobs, tests, flaky tests, one run in detail, and the
// deploys a run's commit became.

// ciFilter narrows a CI table by the page's filters that it has.
func ciFilter(q url.Values, cols ...string) (string, []any) {
	var b strings.Builder
	var args []any
	for _, c := range cols {
		if v := strings.TrimSpace(q.Get(c)); v != "" {
			b.WriteString(" AND " + c + " = ?")
			args = append(args, v)
		}
	}
	return b.String(), args
}

// ciPct is the nearest-rank percentile of sorted values, as the SQL panels
// compute it.
func ciPct(sorted []float64, p float64) any {
	if len(sorted) == 0 {
		return nil
	}
	i := int(math.Ceil(p*float64(len(sorted)))) - 1
	return sorted[min(max(i, 0), len(sorted)-1)]
}

// ciBucket aims at about 30 points: CI runs are far fewer than requests.
func ciBucket(tr timeRange) int64 {
	want := int64(tr.To.Sub(tr.From).Seconds()) / 30
	for _, step := range bucketSteps {
		if step >= want {
			return max(step, 3600)
		}
	}
	return 86400
}

func ciTable(cols []string, rows [][]any, tr timeRange, bucket int64) *Result {
	if rows == nil {
		rows = [][]any{}
	}
	return &Result{Columns: cols, Rows: rows, Bucket: bucket, From: tr.From.Format(time.RFC3339Nano), To: tr.To.Format(time.RFC3339Nano)}
}

type ciAgg struct {
	runs, success, failure, cancelled int
	durations, queues                 []float64
	lastAt, lastConclusion            string
	lastID                            int64
}

func (a *ciAgg) add(status, conclusion, at string, id int64, queue, duration any) {
	a.runs++
	if status == "completed" {
		switch conclusion {
		case "success":
			a.success++
		case "failure", "timed_out":
			a.failure++
		case "cancelled":
			a.cancelled++
		}
		if d, ok := cellFloat(duration); ok {
			a.durations = append(a.durations, d)
		}
	}
	if q, ok := cellFloat(queue); ok {
		a.queues = append(a.queues, q)
	}
	if at >= a.lastAt {
		a.lastAt, a.lastConclusion, a.lastID = at, fallback(conclusion, status), id
	}
}

// rate is the success rate of finished runs that passed or failed; cancelled
// and skipped runs say nothing about the code.
func (a *ciAgg) rate() any {
	if a.success+a.failure == 0 {
		return nil
	}
	return math.Round(1000*float64(a.success)/float64(a.success+a.failure)) / 10
}

func (a *ciAgg) pct(vals []float64, p float64) any {
	sort.Float64s(vals)
	return ciPct(vals, p)
}

func (s *Server) handleCISummary(w http.ResponseWriter, r *http.Request) {
	tr, ok := s.rangeOf(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	from, to := tr.From.Format(tsLayout), tr.To.Format(tsLayout)
	bucket := ciBucket(tr)
	var runs, jobs, opts [][]any
	g, ctx := errgroup.WithContext(r.Context())
	g.Go(func() error {
		f, args := ciFilter(q, "service", "repo", "workflow", "branch")
		res, err := s.db.Query(ctx, `SELECT id, created_at, workflow, branch, status, conclusion, queue_ms, duration_ms FROM ci_pipelines
WHERE created_at >= ? AND created_at < ?`+f+` ORDER BY created_at LIMIT 20000`, append([]any{from, to}, args...)...)
		if err == nil {
			runs = res.Rows
		}
		return ciQuiet(err, "ci_pipelines")
	})
	g.Go(func() error {
		f, args := ciFilter(q, "service", "repo", "workflow", "branch")
		res, err := s.db.Query(ctx, `SELECT workflow, name, status, conclusion, queue_ms, duration_ms, created_at, id FROM ci_jobs
WHERE created_at >= ? AND created_at < ?`+f+` LIMIT 50000`, append([]any{from, to}, args...)...)
		if err == nil {
			jobs = res.Rows
		}
		return ciQuiet(err, "ci_jobs")
	})
	g.Go(func() error {
		res, err := s.db.Query(ctx, `SELECT DISTINCT service, repo, workflow, branch FROM ci_pipelines WHERE created_at >= ? AND created_at < ? LIMIT 1000`, from, to)
		if err == nil {
			opts = res.Rows
		}
		return ciQuiet(err, "ci_pipelines")
	})
	if err := g.Wait(); err != nil {
		s.fail(w, err)
		return
	}

	total := &ciAgg{}
	byFlow := map[string]*ciAgg{}
	flowKey := map[string][2]string{}
	perBucket := map[int64]*ciAgg{}
	perFlowBucket := map[string]map[int64]*ciAgg{}
	for _, row := range runs {
		id, at, flow, branch, status, conclusion := toInt(row[0]), toString(row[1]), toString(row[2]), toString(row[3]), toString(row[4]), toString(row[5])
		total.add(status, conclusion, at, id, row[6], row[7])
		k := flow + "\x00" + branch
		if byFlow[k] == nil {
			byFlow[k], flowKey[k] = &ciAgg{}, [2]string{flow, branch}
		}
		byFlow[k].add(status, conclusion, at, id, row[6], row[7])
		t, err := time.Parse(tsLayout, at)
		if err != nil {
			continue
		}
		b := t.Unix() / bucket * bucket
		if perBucket[b] == nil {
			perBucket[b] = &ciAgg{}
		}
		perBucket[b].add(status, conclusion, at, id, row[6], row[7])
		if perFlowBucket[flow] == nil {
			perFlowBucket[flow] = map[int64]*ciAgg{}
		}
		if perFlowBucket[flow][b] == nil {
			perFlowBucket[flow][b] = &ciAgg{}
		}
		perFlowBucket[flow][b].add(status, conclusion, at, id, row[6], row[7])
	}

	buckets := make([]int64, 0, len(perBucket))
	for b := range perBucket {
		buckets = append(buckets, b)
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i] < buckets[j] })
	var outcome, duration [][]any
	for _, b := range buckets {
		a := perBucket[b]
		outcome = append(outcome, []any{b, "Passed", a.success}, []any{b, "Failed", a.failure}, []any{b, "Cancelled", a.cancelled},
			[]any{b, "Running or other", a.runs - a.success - a.failure - a.cancelled})
		durs := append([]float64{}, a.durations...)
		queues := append([]float64{}, a.queues...)
		duration = append(duration, []any{b, "p50", a.pct(durs, 0.5)}, []any{b, "p95", a.pct(durs, 0.95)}, []any{b, "queue p50", a.pct(queues, 0.5)})
	}

	// Success rate over time for the busiest workflows.
	flows := make([]string, 0, len(perFlowBucket))
	flowRuns := map[string]int{}
	for f, m := range perFlowBucket {
		flows = append(flows, f)
		for _, a := range m {
			flowRuns[f] += a.runs
		}
	}
	sort.Slice(flows, func(i, j int) bool {
		return flowRuns[flows[i]] > flowRuns[flows[j]] || flowRuns[flows[i]] == flowRuns[flows[j]] && flows[i] < flows[j]
	})
	var rate [][]any
	for _, f := range head(flows, 8) {
		for _, b := range buckets {
			if a := perFlowBucket[f][b]; a != nil && a.rate() != nil {
				rate = append(rate, []any{b, fallback(f, "(unnamed)"), a.rate()})
			}
		}
	}

	keys := make([]string, 0, len(byFlow))
	for k := range byFlow {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return byFlow[keys[i]].runs > byFlow[keys[j]].runs || byFlow[keys[i]].runs == byFlow[keys[j]].runs && keys[i] < keys[j]
	})
	var flowRows [][]any
	for _, k := range head(keys, 100) {
		a := byFlow[k]
		flowRows = append(flowRows, []any{flowKey[k][0], flowKey[k][1], a.runs, a.rate(), a.pct(a.durations, 0.5), a.pct(a.durations, 0.95),
			a.pct(a.queues, 0.5), a.lastConclusion, a.lastAt, a.lastID})
	}

	type jobAgg struct {
		ciAgg
		flow, name string
	}
	byJob := map[string]*jobAgg{}
	for _, row := range jobs {
		flow, name := toString(row[0]), toString(row[1])
		k := flow + "\x00" + name
		if byJob[k] == nil {
			byJob[k] = &jobAgg{flow: flow, name: name}
		}
		byJob[k].add(toString(row[2]), toString(row[3]), toString(row[6]), toInt(row[7]), row[4], row[5])
	}
	jobList := make([]*jobAgg, 0, len(byJob))
	for _, j := range byJob {
		sort.Float64s(j.durations)
		sort.Float64s(j.queues)
		jobList = append(jobList, j)
	}
	jobRow := func(j *jobAgg) []any {
		var failRate any
		if n := j.success + j.failure; n > 0 {
			failRate = math.Round(1000*float64(j.failure)/float64(n)) / 10
		}
		return []any{j.name, j.flow, j.runs, j.failure, failRate, ciPct(j.durations, 0.5), ciPct(j.durations, 0.95), ciPct(j.queues, 0.5), ciPct(j.queues, 0.95), j.lastAt}
	}
	jobCols := []string{"job", "workflow", "runs", "failures", "failure_rate", "p50_ms", "p95_ms", "queue_p50_ms", "queue_p95_ms", "last_run"}
	sort.Slice(jobList, func(i, k int) bool {
		a, b := toFloat(ciPct(jobList[i].durations, 0.95)), toFloat(ciPct(jobList[k].durations, 0.95))
		return a > b || a == b && jobList[i].name < jobList[k].name
	})
	var slowest [][]any
	for _, j := range head(jobList, 25) {
		if len(j.durations) > 0 {
			slowest = append(slowest, jobRow(j))
		}
	}
	sort.SliceStable(jobList, func(i, k int) bool { return jobList[i].failure > jobList[k].failure })
	var failing [][]any
	for _, j := range jobList {
		if j.failure == 0 || len(failing) == 25 {
			break
		}
		failing = append(failing, jobRow(j))
	}

	options := map[string][]string{"service": {}, "repo": {}, "workflow": {}, "branch": {}}
	seen := map[string]bool{}
	for _, row := range opts {
		for i, k := range []string{"service", "repo", "workflow", "branch"} {
			if v := toString(row[i]); v != "" && !seen[k+v] {
				seen[k+v] = true
				options[k] = append(options[k], v)
			}
		}
	}
	for _, v := range options {
		sort.Strings(v)
	}

	durs := append([]float64{}, total.durations...)
	queues := append([]float64{}, total.queues...)
	writeJSON(w, http.StatusOK, map[string]any{
		"totals": map[string]any{
			"runs": total.runs, "success": total.success, "failure": total.failure, "cancelled": total.cancelled, "success_rate": total.rate(),
			"p50_ms": total.pct(durs, 0.5), "p95_ms": total.pct(durs, 0.95), "queue_p50_ms": total.pct(queues, 0.5), "queue_p95_ms": total.pct(queues, 0.95),
		},
		"outcomes":  ciTable([]string{"t", "outcome", "runs"}, outcome, tr, bucket),
		"durations": ciTable([]string{"t", "series", "duration_ms"}, duration, tr, bucket),
		"rate":      ciTable([]string{"t", "workflow", "success_rate"}, rate, tr, bucket),
		"workflows": ciTable([]string{"workflow", "branch", "runs", "success_rate", "p50_ms", "p95_ms", "queue_p50_ms", "last", "last_at", "last_id"}, flowRows, tr, 0),
		"slow_jobs": ciTable(jobCols, slowest, tr, 0),
		"fail_jobs": ciTable(jobCols, failing, tr, 0),
		"options":   options,
	})
}

// ciQuiet treats a CI table that does not exist yet as empty.
func ciQuiet(err error, table string) error {
	if err != nil && missingTable(err, table) {
		return nil
	}
	return err
}

type ciRunRow struct {
	ID            int64     `json:"id"`
	Provider      string    `json:"provider"`
	Repo          string    `json:"repo"`
	Workflow      string    `json:"workflow"`
	RunID         string    `json:"run_id"`
	Number        int64     `json:"number"`
	Attempt       int64     `json:"attempt"`
	Event         string    `json:"event"`
	Branch        string    `json:"branch"`
	DefaultBranch string    `json:"default_branch"`
	Commit        string    `json:"commit"`
	CommitMessage string    `json:"commit_message"`
	Actor         string    `json:"actor"`
	Status        string    `json:"status"`
	Conclusion    string    `json:"conclusion"`
	URL           string    `json:"url"`
	Service       string    `json:"service"`
	CreatedAt     string    `json:"created_at"`
	StartedAt     string    `json:"started_at"`
	FinishedAt    string    `json:"finished_at"`
	QueueMs       *float64  `json:"queue_ms"`
	DurationMs    *float64  `json:"duration_ms"`
	Deploys       []release `json:"deploys"`
	runKey        string
}

const ciRunColumns = `id, provider, repo, workflow, run_id, run_number, attempt, event, branch, default_branch, commit_sha, commit_message, actor,
status, conclusion, url, service, created_at, started_at, finished_at, queue_ms, duration_ms, run_key`

func scanCIRun(row []any) ciRunRow {
	return ciRunRow{
		ID: toInt(row[0]), Provider: toString(row[1]), Repo: toString(row[2]), Workflow: toString(row[3]), RunID: toString(row[4]),
		Number: toInt(row[5]), Attempt: toInt(row[6]), Event: toString(row[7]), Branch: toString(row[8]), DefaultBranch: toString(row[9]),
		Commit: toString(row[10]), CommitMessage: toString(row[11]), Actor: toString(row[12]), Status: toString(row[13]), Conclusion: toString(row[14]),
		URL: toString(row[15]), Service: toString(row[16]), CreatedAt: toString(row[17]), StartedAt: toString(row[18]), FinishedAt: toString(row[19]),
		QueueMs: cellPtr(row[20]), DurationMs: cellPtr(row[21]), runKey: toString(row[22]), Deploys: []release{},
	}
}

// versionCommits are the commit SHA prefixes a version names: "a1b2c3d",
// "1.4.0+a1b2c3d", git describe's "v1.4.0-3-ga1b2c3d".
func versionCommits(version string) []string {
	var out []string
	for _, tok := range strings.FieldsFunc(strings.ToLower(version), func(r rune) bool {
		return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z')
	}) {
		if len(tok) >= 8 && tok[0] == 'g' && isHex(tok[1:]) {
			tok = tok[1:]
		}
		if len(tok) >= 7 && len(tok) <= 40 && isHex(tok) && strings.ContainsAny(tok, "abcdef") {
			out = append(out, tok)
		}
	}
	return out
}

func isHex(s string) bool {
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return s != ""
}

// deploysOf are the releases whose version names commit.
func deploysOf(all []release, commit string) []release {
	out := []release{}
	commit = strings.ToLower(commit)
	if len(commit) < 7 {
		return out
	}
	for _, rel := range all {
		for _, tok := range versionCommits(rel.Version) {
			if strings.HasPrefix(commit, tok) {
				out = append(out, rel)
				break
			}
		}
	}
	return out
}

func (s *Server) handleCIRunList(w http.ResponseWriter, r *http.Request) {
	tr, ok := s.rangeOf(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	where, args := ciFilter(q, "service", "repo", "workflow", "branch")
	switch q.Get("outcome") {
	case "failure":
		where += " AND status = 'completed' AND conclusion IN ('failure', 'timed_out')"
	case "success":
		where += " AND status = 'completed' AND conclusion = 'success'"
	case "running":
		where += " AND status <> 'completed'"
	}
	if term := strings.TrimSpace(q.Get("q")); term != "" && len(term) <= 200 {
		like := "%" + strings.ToLower(term) + "%"
		where += " AND (LOWER(COALESCE(commit_sha, '')) LIKE ? OR LOWER(COALESCE(commit_message, '')) LIKE ? OR LOWER(COALESCE(actor, '')) LIKE ? OR LOWER(COALESCE(branch, '')) LIKE ?)"
		args = append(args, like, like, like, like)
	}
	res, err := s.db.Query(r.Context(), `SELECT `+ciRunColumns+` FROM ci_pipelines WHERE created_at >= ? AND created_at < ?`+where+`
ORDER BY created_at DESC, id DESC LIMIT 200`, append([]any{tr.From.Format(tsLayout), tr.To.Format(tsLayout)}, args...)...)
	if err != nil {
		if missingTable(err, "ci_pipelines") {
			writeJSON(w, http.StatusOK, map[string]any{"runs": []ciRunRow{}})
			return
		}
		s.fail(w, err)
		return
	}
	all, _ := s.allReleases(r.Context())
	runs := make([]ciRunRow, 0, len(res.Rows))
	for _, row := range res.Rows {
		run := scanCIRun(row)
		run.Deploys = deploysOf(all, run.Commit)
		runs = append(runs, run)
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

type ciJobRow struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	Runner     string    `json:"runner"`
	URL        string    `json:"url"`
	CreatedAt  string    `json:"created_at"`
	StartedAt  string    `json:"started_at"`
	FinishedAt string    `json:"finished_at"`
	QueueMs    *float64  `json:"queue_ms"`
	DurationMs *float64  `json:"duration_ms"`
	Steps      []ciStep  `json:"steps"`
	Tests      *ciCounts `json:"tests,omitempty"`
}

type ciCounts struct {
	Tests   int `json:"tests"`
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
}

type ciTestRow struct {
	Hash       string   `json:"hash"`
	Test       string   `json:"test"`
	Suite      string   `json:"suite"`
	File       string   `json:"file"`
	Job        string   `json:"job"`
	Status     string   `json:"status"`
	DurationMs *float64 `json:"duration_ms"`
	Message    string   `json:"message"`
	Flaky      bool     `json:"flaky"`
}

// deployImpact is a service's traffic in the hour before and after a deploy.
type deployImpact struct {
	release
	Before map[string]any `json:"before"`
	After  map[string]any `json:"after"`
}

func (s *Server) handleCIRun(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid run id")
		return
	}
	ctx := r.Context()
	res, err := s.db.Query(ctx, `SELECT `+ciRunColumns+` FROM ci_pipelines WHERE id = ?`, id)
	if err != nil && !missingTable(err, "ci_pipelines") {
		s.fail(w, err)
		return
	}
	if err != nil || len(res.Rows) == 0 {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	run := scanCIRun(res.Rows[0])

	jobs := []ciJobRow{}
	jr, err := s.db.Query(ctx, `SELECT id, name, status, conclusion, runner, url, created_at, started_at, finished_at, queue_ms, duration_ms, steps
FROM ci_jobs WHERE run_key = ? ORDER BY created_at, started_at, id LIMIT 500`, run.runKey)
	if err != nil && !missingTable(err, "ci_jobs") {
		s.fail(w, err)
		return
	}
	if err == nil {
		for _, row := range jr.Rows {
			j := ciJobRow{ID: toInt(row[0]), Name: toString(row[1]), Status: toString(row[2]), Conclusion: toString(row[3]), Runner: toString(row[4]),
				URL: toString(row[5]), CreatedAt: toString(row[6]), StartedAt: toString(row[7]), FinishedAt: toString(row[8]),
				QueueMs: cellPtr(row[9]), DurationMs: cellPtr(row[10]), Steps: []ciStep{}}
			if v := toString(row[11]); v != "" {
				_ = json.Unmarshal([]byte(v), &j.Steps)
			}
			jobs = append(jobs, j)
		}
	}

	// Tests uploaded with this run, or else with its commit and no run.
	where, args := "run_key = ?", []any{run.runKey}
	if run.Commit != "" {
		cnt, err := s.db.Query(ctx, `SELECT COUNT(*) FROM ci_tests WHERE run_key = ?`, run.runKey)
		if err == nil && len(cnt.Rows) > 0 && toInt(cnt.Rows[0][0]) == 0 {
			where, args = "run_key IS NULL AND commit_sha = ?", []any{run.Commit}
		}
	}
	counts := ciCounts{}
	failed, slow := []ciTestRow{}, []ciTestRow{}
	tr, err := s.db.Query(ctx, `SELECT test_hash, test, suite, file, job, status, duration_ms, message FROM ci_tests WHERE `+where+` LIMIT 50000`, args...)
	if err != nil && !missingTable(err, "ci_tests") {
		s.fail(w, err)
		return
	}
	perJob := map[string]*ciCounts{}
	var all []ciTestRow
	if err == nil {
		for _, row := range tr.Rows {
			t := ciTestRow{Hash: toString(row[0]), Test: toString(row[1]), Suite: toString(row[2]), File: toString(row[3]), Job: toString(row[4]),
				Status: toString(row[5]), DurationMs: cellPtr(row[6]), Message: toString(row[7])}
			c := perJob[t.Job]
			if c == nil {
				c = &ciCounts{}
				perJob[t.Job] = c
			}
			for _, k := range []*ciCounts{&counts, c} {
				k.Tests++
				switch {
				case ciFailed(t.Status):
					k.Failed++
				case t.Status == "skipped":
					k.Skipped++
				default:
					k.Passed++
				}
			}
			all = append(all, t)
		}
	}
	flaky := map[string]bool{}
	var hashes []any
	for _, t := range all {
		if ciFailed(t.Status) {
			failed = append(failed, t)
			hashes = append(hashes, t.Hash)
		}
	}
	for len(hashes) > 0 {
		chunk := head(hashes, 200)
		hashes = hashes[len(chunk):]
		fr, err := s.db.Query(ctx, `SELECT test_hash FROM ci_flaky_tests WHERE test_hash IN (`+placeholders(len(chunk))+`)`, chunk...)
		if err != nil {
			break
		}
		for _, row := range fr.Rows {
			flaky[toString(row[0])] = true
		}
	}
	for i := range failed {
		failed[i].Flaky = flaky[failed[i].Hash]
	}
	failed = head(failed, 200)
	sort.SliceStable(all, func(i, j int) bool { return toFloat(ptrAny(all[i].DurationMs)) > toFloat(ptrAny(all[j].DurationMs)) })
	for _, t := range head(all, 20) {
		if t.DurationMs != nil {
			slow = append(slow, t)
		}
	}
	for i := range jobs {
		if c := perJob[jobs[i].Name]; c != nil {
			jobs[i].Tests = c
		}
	}

	releases, _ := s.allReleases(ctx)
	deploys := []deployImpact{}
	for _, rel := range head(deploysOf(releases, run.Commit), 5) {
		d := deployImpact{release: rel}
		if at, err := time.Parse(tsLayout, rel.DeployedAt); err == nil {
			d.Before, d.After = s.trafficAround(ctx, rel.Service, at.Add(-time.Hour), at), s.trafficAround(ctx, rel.Service, at, at.Add(time.Hour))
		}
		deploys = append(deploys, d)
	}
	run.Deploys = nil
	writeJSON(w, http.StatusOK, map[string]any{"run": run, "jobs": jobs, "tests": counts, "failed": failed, "slowest": slow, "deploys": deploys})
}

func ptrAny(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

// trafficAround measures a service between from and to: requests, p95,
// the 5xx rate and exceptions.
func (s *Server) trafficAround(ctx context.Context, service string, from, to time.Time) map[string]any {
	out := map[string]any{"requests": 0, "p95_ms": nil, "error_rate": nil, "errors": 0}
	args := []any{from.UTC().Format(tsLayout), to.UTC().Format(tsLayout), service}
	res, err := s.db.Query(ctx, `WITH r AS (
  SELECT status, duration_ms, ROW_NUMBER() OVER (ORDER BY duration_ms) AS rn, COUNT(*) OVER () AS n
  FROM api_logs WHERE created_at >= ? AND created_at < ? AND COALESCE(service, '') = ?
)
SELECT COUNT(*), MIN(CASE WHEN rn >= 0.95 * n THEN duration_ms END), `+s.q.round("100.0 * "+countIf("status >= 500")+" / NULLIF(COUNT(*), 0)", 2)+` FROM r`, args...)
	if err == nil && len(res.Rows) > 0 {
		out["requests"], out["p95_ms"], out["error_rate"] = toInt(res.Rows[0][0]), cellPtr(res.Rows[0][1]), cellPtr(res.Rows[0][2])
	}
	res, err = s.db.Query(ctx, `SELECT COUNT(*) FROM errors WHERE created_at >= ? AND created_at < ? AND COALESCE(service, '') = ?`, args...)
	if err == nil && len(res.Rows) > 0 {
		out["errors"] = toInt(res.Rows[0][0])
	}
	return out
}

func (s *Server) handleCITests(w http.ResponseWriter, r *http.Request) {
	tr, ok := s.rangeOf(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f, fargs := ciFilter(q, "service", "repo", "branch")
	from, to := tr.From.Format(tsLayout), tr.To.Format(tsLayout)
	mid := tr.From.Add(tr.To.Sub(tr.From) / 2).Format(tsLayout)
	bucket := ciBucket(tr)
	base := append([]any{from, to}, fargs...)
	var byStatus, failing, slowest, trend *Result
	g, ctx := errgroup.WithContext(r.Context())
	run := func(dst **Result, sql string, args ...any) {
		g.Go(func() error {
			res, err := s.db.Query(ctx, sql, args...)
			if err != nil {
				if missingTable(err, "ci_tests") {
					*dst = ciTable([]string{}, nil, tr, bucket)
					return nil
				}
				return dbErr(err)
			}
			*dst = newResult(res, 5000, "", tr)
			return nil
		})
	}
	t := s.q.bucketOf(strconv.FormatInt(bucket, 10))
	run(&byStatus, `SELECT `+t+` AS t, status, COUNT(*) AS results FROM ci_tests WHERE created_at >= ? AND created_at < ?`+f+`
GROUP BY `+t+`, status ORDER BY t`, base...)
	run(&failing, `SELECT test_hash AS hash, MAX(test) AS test, MAX(service) AS service, MAX(file) AS file, COUNT(*) AS runs,
  `+countIf("status IN ('failed', 'error')")+` AS failures, MAX(CASE WHEN status IN ('failed', 'error') THEN created_at END) AS last_failed
FROM ci_tests WHERE created_at >= ? AND created_at < ? AND status <> 'skipped'`+f+`
GROUP BY test_hash HAVING `+countIf("status IN ('failed', 'error')")+` > 0 ORDER BY failures DESC, last_failed DESC LIMIT 100`, base...)
	run(&slowest, `SELECT test_hash AS hash, MAX(test) AS test, MAX(service) AS service, MAX(file) AS file, COUNT(*) AS runs,
  `+s.q.round("AVG(duration_ms)", 2)+` AS avg_ms, MAX(duration_ms) AS max_ms, `+s.q.round("SUM(duration_ms)", 2)+` AS total_ms
FROM ci_tests WHERE created_at >= ? AND created_at < ? AND status = 'passed' AND duration_ms IS NOT NULL`+f+`
GROUP BY test_hash ORDER BY avg_ms DESC LIMIT 50`, base...)
	run(&trend, `SELECT test_hash AS hash, MAX(test) AS test, MAX(service) AS service,
  `+s.q.round("AVG(CASE WHEN created_at < ? THEN duration_ms END)", 2)+` AS before_ms,
  `+s.q.round("AVG(CASE WHEN created_at >= ? THEN duration_ms END)", 2)+` AS after_ms,
  `+countIf("created_at < ?")+` AS runs_before, `+countIf("created_at >= ?")+` AS runs_after
FROM ci_tests WHERE created_at >= ? AND created_at < ? AND status = 'passed' AND duration_ms IS NOT NULL`+f+`
GROUP BY test_hash LIMIT 20000`, append([]any{mid, mid, mid, mid}, base...)...)
	var totals map[string]any
	g.Go(func() error {
		res, err := s.db.Query(ctx, `SELECT COUNT(DISTINCT test_hash), COUNT(*), `+countIf("status IN ('failed', 'error')")+`, `+countIf("status = 'skipped'")+`,
  COUNT(DISTINCT CASE WHEN status IN ('failed', 'error') THEN test_hash END)
FROM ci_tests WHERE created_at >= ? AND created_at < ?`+f, base...)
		if err != nil {
			return ciQuiet(err, "ci_tests")
		}
		if len(res.Rows) > 0 {
			row := res.Rows[0]
			totals = map[string]any{"tests": toInt(row[0]), "results": toInt(row[1]), "failures": toInt(row[2]), "skipped": toInt(row[3]), "failing_tests": toInt(row[4])}
		}
		return nil
	})
	if err := g.Wait(); err != nil {
		s.fail(w, err)
		return
	}
	if totals == nil {
		totals = map[string]any{"tests": 0, "results": 0, "failures": 0, "skipped": 0, "failing_tests": 0}
	}
	// Getting slower: at least three passing runs in each half, 20% and 5 ms slower.
	var slower [][]any
	for _, m := range rowMaps(trend) {
		b, a := toFloat(m["before_ms"]), toFloat(m["after_ms"])
		if toInt(m["runs_before"]) < 3 || toInt(m["runs_after"]) < 3 || b <= 0 || a < b*1.2 || a-b < 5 {
			continue
		}
		slower = append(slower, []any{m["hash"], m["test"], m["service"], b, a, math.Round(1000*(a-b)/b) / 10})
	}
	sort.Slice(slower, func(i, j int) bool { return toFloat(slower[i][5]) > toFloat(slower[j][5]) })
	writeJSON(w, http.StatusOK, map[string]any{
		"totals":   totals,
		"results":  byStatus,
		"failing":  failing,
		"slowest":  slowest,
		"slower":   ciTable([]string{"hash", "test", "service", "before_ms", "after_ms", "change_pct"}, head(slower, 50), tr, 0),
		"midpoint": mid,
	})
}

func (s *Server) handleCITestHistory(w http.ResponseWriter, r *http.Request) {
	hash := strings.TrimSpace(r.URL.Query().Get("hash"))
	if hash == "" || len(hash) > 64 {
		writeError(w, http.StatusBadRequest, "pick a test")
		return
	}
	res, err := s.db.Query(r.Context(), `SELECT t.created_at, t.status, t.duration_ms, t.branch, t.commit_sha, t.job, t.message, t.test, t.file, t.service, p.id, p.workflow, p.run_number
FROM ci_tests t LEFT JOIN ci_pipelines p ON p.run_key = t.run_key
WHERE t.test_hash = ? ORDER BY t.created_at DESC, t.id DESC LIMIT 100`, hash)
	if err != nil {
		if missingTable(err, "ci_tests") {
			writeJSON(w, http.StatusOK, map[string]any{"results": []any{}})
			return
		}
		s.fail(w, err)
		return
	}
	out := make([]map[string]any, 0, len(res.Rows))
	test, file, service := "", "", ""
	for _, row := range res.Rows {
		test, file, service = toString(row[7]), fallback(toString(row[8]), file), toString(row[9])
		out = append(out, map[string]any{
			"at": toString(row[0]), "status": toString(row[1]), "duration_ms": cellPtr(row[2]), "branch": toString(row[3]), "commit": toString(row[4]),
			"job": toString(row[5]), "message": toString(row[6]), "run_id": nullable(row[10]), "workflow": toString(row[11]), "number": toInt(row[12]),
		})
	}
	var flaky any
	fr, err := s.db.Query(r.Context(), `SELECT created_at, reason, commit_sha FROM ci_flaky_tests WHERE test_hash = ?`, hash)
	if err == nil && len(fr.Rows) > 0 {
		flaky = map[string]any{"at": toString(fr.Rows[0][0]), "reason": toString(fr.Rows[0][1]), "commit": toString(fr.Rows[0][2])}
	}
	writeJSON(w, http.StatusOK, map[string]any{"test": test, "file": file, "service": service, "flaky": flaky, "results": out})
}

func (s *Server) handleCIFlaky(w http.ResponseWriter, r *http.Request) {
	tr, ok := s.rangeOf(w, r)
	if !ok {
		return
	}
	f, args := ciFilter(r.URL.Query(), "service", "repo")
	ctx := r.Context()
	rows, err := s.ciResults(ctx, tr.From.Format(tsLayout), tr.To.Format(tsLayout), nil, f, args)
	if err != nil {
		if missingTable(err, "ci_tests") {
			writeJSON(w, http.StatusOK, map[string]any{"tests": []*flakyTest{}, "new": 0})
			return
		}
		s.fail(w, err)
		return
	}
	if branch := strings.TrimSpace(r.URL.Query().Get("branch")); branch != "" {
		keep := rows[:0]
		for _, row := range rows {
			if row.branch == branch {
				keep = append(keep, row)
			}
		}
		rows = keep
	}
	flaky := findFlaky(rows, s.ciDefaultBranches(ctx))
	ids := make([]any, 0, len(flaky))
	for _, ft := range flaky {
		ids = append(ids, ft.Hash)
	}
	first := map[string]string{}
	for len(ids) > 0 {
		chunk := head(ids, 200)
		ids = ids[len(chunk):]
		res, err := s.db.Query(ctx, `SELECT test_hash, created_at FROM ci_flaky_tests WHERE test_hash IN (`+placeholders(len(chunk))+`)`, chunk...)
		if err != nil {
			break
		}
		for _, row := range res.Rows {
			first[toString(row[0])] = toString(row[1])
		}
	}
	fresh := 0
	from := tr.From.Format(tsLayout)
	for _, ft := range flaky {
		ft.FirstFlagged = first[ft.Hash]
		if ft.FirstFlagged >= from {
			fresh++
		}
	}
	if flaky == nil {
		flaky = []*flakyTest{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tests": head(flaky, 200), "new": fresh})
}

// handleCIBuilds finds the run that built each version, by the commit its
// version names, for the deploy markers and the version comparison.
func (s *Server) handleCIBuilds(w http.ResponseWriter, r *http.Request) {
	versions := r.URL.Query()["version"]
	out := map[string]any{}
	for _, v := range head(versions, 20) {
		for _, tok := range versionCommits(v) {
			res, err := s.db.Query(r.Context(), `SELECT `+ciRunColumns+` FROM ci_pipelines WHERE commit_sha LIKE ?
ORDER BY CASE WHEN conclusion = 'success' THEN 0 ELSE 1 END, created_at DESC LIMIT 1`, tok+"%")
			if err != nil {
				if missingTable(err, "ci_pipelines") {
					writeJSON(w, http.StatusOK, map[string]any{"builds": out})
					return
				}
				s.fail(w, err)
				return
			}
			if len(res.Rows) > 0 {
				out[v] = scanCIRun(res.Rows[0])
				break
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"builds": out})
}

func (s *Server) handleCISetup(w http.ResponseWriter, r *http.Request) {
	runs, tests := int64(0), int64(0)
	if res, err := s.db.Query(r.Context(), `SELECT COUNT(*) FROM (SELECT id FROM ci_pipelines LIMIT 1) AS x`); err == nil && len(res.Rows) > 0 {
		runs = toInt(res.Rows[0][0])
	}
	if res, err := s.db.Query(r.Context(), `SELECT COUNT(*) FROM (SELECT id FROM ci_tests LIMIT 1) AS x`); err == nil && len(res.Rows) > 0 {
		tests = toInt(res.Rows[0][0])
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"has_runs":   runs > 0,
		"has_tests":  tests > 0,
		"secret_set": s.conf("github_webhook_secret") != "",
		"webhook":    "/api/ingest/ci/github",
	})
}
