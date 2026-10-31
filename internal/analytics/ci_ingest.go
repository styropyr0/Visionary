package analytics

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Runs and jobs arrive many times as they move from queued to completed, and
// not always in order: each delivery merges into the stored row, and a
// completed run or job never goes back to running

const (
	ciMaxRunsPerPost = 100
	ciMaxJobsPerRun  = 500
	ciMaxSteps       = 200
)

type ciStep struct {
	Name       string `json:"name"`
	Conclusion string `json:"conclusion,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
}

type ciJob struct {
	ID, Name, Status, Conclusion, Runner, URL string
	Queued, Started, Finished                 time.Time
	Steps                                     []ciStep
}

type ciRun struct {
	Provider, Repo, Workflow, RunID string
	Number, Attempt                 int64
	Event, Branch, DefaultBranch    string
	Commit, CommitMessage, Actor    string
	Status, Conclusion, URL         string
	Service                         string
	Queued, Started, Finished       time.Time
	Jobs                            []ciJob
	jobOnly                         bool
}

func (r *ciRun) key() string { return ciRunKey(r.Provider, r.Repo, r.RunID, r.Attempt) }

func ciStatus(status, conclusion string) (string, string) {
	status = strings.ToLower(strings.TrimSpace(status))
	conclusion = ciConclusion(conclusion)
	switch status {
	case "", "queued", "requested", "waiting", "pending", "created", "scheduled", "preparing", "waiting_for_resource", "blocked", "not_run":
		if conclusion != "" {
			return "completed", conclusion
		}
		return "queued", ""
	case "in_progress", "running", "started":
		return "in_progress", ""
	case "completed", "finished", "done":
		return "completed", conclusion
	}
	if c := ciConclusion(status); c != "" {
		return "completed", c
	}
	return status, conclusion
}

func ciConclusion(c string) string {
	switch c = strings.ToLower(strings.TrimSpace(c)); c {
	case "success", "succeeded", "passed", "ok", "fixed":
		return "success"
	case "failure", "failed", "error", "errored", "broken", "infrastructure_fail", "unstable", "startup_failure":
		return "failure"
	case "cancelled", "canceled", "aborted", "canceling":
		return "cancelled"
	case "timed_out", "timedout", "timeout":
		return "timed_out"
	}
	return string(head([]rune(c), 40))
}

func ciTime(v string) time.Time {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil || t.Year() < 2000 {
		return time.Time{}
	}
	return t.UTC()
}

func ciTS(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(tsLayout)
}

func ciSpan(from, to time.Time) any {
	if from.IsZero() || to.IsZero() || to.Before(from) {
		return nil
	}
	return to.Sub(from).Milliseconds()
}

func (s *Server) saveRun(ctx context.Context, run *ciRun) error {
	if !run.jobOnly {
		if err := s.saveCIPipeline(ctx, run); err != nil {
			return err
		}
	}
	for i := range run.Jobs {
		if err := s.saveCIJob(ctx, run, &run.Jobs[i]); err != nil {
			return err
		}
	}
	return s.ciRunQueue(ctx, run.key())
}

// GitHub starts a run the moment it's created and its jobs then wait for runners,
// so the queue time is the wait until the first job started
func (s *Server) ciRunQueue(ctx context.Context, key string) error {
	res, err := s.db.Query(ctx, `SELECT p.id, p.created_at, MIN(j.started_at) FROM ci_pipelines p JOIN ci_jobs j ON j.run_key = p.run_key
WHERE p.run_key = ? AND j.started_at IS NOT NULL GROUP BY p.id, p.created_at`, key)
	if err != nil || len(res.Rows) == 0 {
		return err
	}
	row := res.Rows[0]
	queue := ciSpan(ciTime(toString(row[1])), ciTime(toString(row[2])))
	if queue == nil {
		return nil
	}
	return s.db.Exec(ctx, `UPDATE ci_pipelines SET queue_ms = ? WHERE id = ?`, queue, toInt(row[0]))
}

func (s *Server) saveCIPipeline(ctx context.Context, run *ciRun) error {
	key := run.key()
	now := time.Now().UTC()
	for try := 0; try < 2; try++ {
		res, err := s.db.Query(ctx, `SELECT id, status, created_at, started_at, finished_at FROM ci_pipelines WHERE run_key = ?`, key)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			queued := run.Queued
			if queued.IsZero() {
				queued = now
			}
			conclusion := run.Conclusion
			if run.Status != "completed" {
				conclusion = ""
			}
			err = s.db.Exec(ctx, `INSERT INTO ci_pipelines (created_at, updated_at, run_key, provider, repo, workflow, run_id, run_number, attempt,
  event, branch, default_branch, commit_sha, commit_message, actor, status, conclusion, url, started_at, finished_at, queue_ms, duration_ms, service)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				ciTS(queued), ciTS(now), key, otlpNullStr(run.Provider), otlpNullStr(run.Repo), otlpNullStr(run.Workflow), otlpNullStr(run.RunID),
				ciNum(run.Number), max(run.Attempt, 1), otlpNullStr(run.Event), otlpNullStr(run.Branch), otlpNullStr(run.DefaultBranch),
				otlpNullStr(run.Commit), otlpNullStr(ciClip(run.CommitMessage, 500)), otlpNullStr(run.Actor), run.Status, otlpNullStr(conclusion),
				otlpNullStr(run.URL), ciTS(run.Started), ciTS(run.Finished), ciSpan(queued, run.Started), ciSpan(run.Started, run.Finished),
				otlpNullStr(run.Service))
			if err == nil {
				return nil
			}
			continue
		}
		row := res.Rows[0]
		id, stored := toInt(row[0]), toString(row[1])
		queued, started, finished := ciTime(toString(row[2])), ciTime(toString(row[3])), ciTime(toString(row[4]))
		if !run.Queued.IsZero() && (queued.IsZero() || run.Queued.Before(queued)) {
			queued = run.Queued
		}
		status, conclusion := run.Status, run.Conclusion
		if stored == "completed" && status != "completed" {
			status, conclusion = stored, ""
		} else {
			if !run.Started.IsZero() {
				started = run.Started
			}
			if !run.Finished.IsZero() {
				finished = run.Finished
			}
		}
		if status != "completed" {
			finished = time.Time{}
		}
		return s.db.Exec(ctx, `UPDATE ci_pipelines SET created_at = ?, updated_at = ?, workflow = COALESCE(?, workflow), run_number = COALESCE(?, run_number),
  event = COALESCE(?, event), branch = COALESCE(?, branch), default_branch = COALESCE(?, default_branch), commit_sha = COALESCE(?, commit_sha),
  commit_message = COALESCE(?, commit_message), actor = COALESCE(?, actor), status = ?, conclusion = COALESCE(?, conclusion), url = COALESCE(?, url),
  started_at = ?, finished_at = ?, queue_ms = ?, duration_ms = ?, service = COALESCE(?, service)
WHERE id = ?`,
			ciTS(queued), ciTS(now), otlpNullStr(run.Workflow), ciNum(run.Number), otlpNullStr(run.Event), otlpNullStr(run.Branch),
			otlpNullStr(run.DefaultBranch), otlpNullStr(run.Commit), otlpNullStr(ciClip(run.CommitMessage, 500)), otlpNullStr(run.Actor),
			status, otlpNullStr(conclusion), otlpNullStr(run.URL), ciTS(started), ciTS(finished), ciSpan(queued, started), ciSpan(started, finished),
			otlpNullStr(run.Service), id)
	}
	return errors.New("the run could not be stored")
}

func ciNum(n int64) any {
	if n <= 0 {
		return nil
	}
	return n
}

func (s *Server) saveCIJob(ctx context.Context, run *ciRun, job *ciJob) error {
	jobKey := run.key() + ":" + job.Name
	if job.ID != "" {
		jobKey = run.Provider + ":" + run.Repo + ":job:" + job.ID
	}
	var steps any
	if len(job.Steps) > 0 {
		b, _ := json.Marshal(head(job.Steps, ciMaxSteps))
		steps = string(b)
	}
	now := time.Now().UTC()
	for try := 0; try < 2; try++ {
		res, err := s.db.Query(ctx, `SELECT id, status, created_at, started_at, finished_at FROM ci_jobs WHERE job_key = ?`, jobKey)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			queued := job.Queued
			if queued.IsZero() {
				queued = fallbackTime(job.Started, now)
			}
			conclusion := job.Conclusion
			if job.Status != "completed" {
				conclusion = ""
			}
			err = s.db.Exec(ctx, `INSERT INTO ci_jobs (created_at, updated_at, job_key, run_key, provider, repo, workflow, name, branch, commit_sha, service,
  status, conclusion, runner, url, started_at, finished_at, queue_ms, duration_ms, steps)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				ciTS(queued), ciTS(now), jobKey, run.key(), otlpNullStr(run.Provider), otlpNullStr(run.Repo), otlpNullStr(run.Workflow),
				otlpNullStr(job.Name), otlpNullStr(run.Branch), otlpNullStr(run.Commit), otlpNullStr(run.Service), job.Status, otlpNullStr(conclusion),
				otlpNullStr(job.Runner), otlpNullStr(job.URL), ciTS(job.Started), ciTS(job.Finished), ciSpan(queued, job.Started),
				ciSpan(job.Started, job.Finished), steps)
			if err == nil {
				return nil
			}
			continue
		}
		row := res.Rows[0]
		id, stored := toInt(row[0]), toString(row[1])
		queued, started, finished := ciTime(toString(row[2])), ciTime(toString(row[3])), ciTime(toString(row[4]))
		if !job.Queued.IsZero() && job.Queued.Before(queued) {
			queued = job.Queued
		}
		status, conclusion := job.Status, job.Conclusion
		if stored == "completed" && status != "completed" {
			status, conclusion, steps = stored, "", nil
		} else {
			if !job.Started.IsZero() {
				started = job.Started
			}
			if !job.Finished.IsZero() {
				finished = job.Finished
			}
		}
		if status != "completed" {
			finished = time.Time{}
		}
		return s.db.Exec(ctx, `UPDATE ci_jobs SET created_at = ?, updated_at = ?, name = COALESCE(?, name), status = ?, conclusion = COALESCE(?, conclusion),
  runner = COALESCE(?, runner), url = COALESCE(?, url), started_at = ?, finished_at = ?, queue_ms = ?, duration_ms = ?, steps = COALESCE(?, steps)
WHERE id = ?`,
			ciTS(queued), ciTS(now), otlpNullStr(job.Name), status, otlpNullStr(conclusion), otlpNullStr(job.Runner), otlpNullStr(job.URL),
			ciTS(started), ciTS(finished), ciSpan(queued, started), ciSpan(started, finished), steps, id)
	}
	return errors.New("the job could not be stored")
}

func fallbackTime(t, def time.Time) time.Time {
	if t.IsZero() {
		return def
	}
	return t
}

type ghActor struct {
	Login string `json:"login"`
}

type ghRepo struct {
	FullName      string `json:"full_name"`
	Name          string `json:"name"`
	DefaultBranch string `json:"default_branch"`
}

type ghRunEvent struct {
	Action      string `json:"action"`
	WorkflowRun struct {
		ID           int64    `json:"id"`
		Name         string   `json:"name"`
		HeadBranch   string   `json:"head_branch"`
		HeadSHA      string   `json:"head_sha"`
		RunNumber    int64    `json:"run_number"`
		RunAttempt   int64    `json:"run_attempt"`
		Event        string   `json:"event"`
		Status       string   `json:"status"`
		Conclusion   *string  `json:"conclusion"`
		HTMLURL      string   `json:"html_url"`
		CreatedAt    string   `json:"created_at"`
		UpdatedAt    string   `json:"updated_at"`
		RunStartedAt string   `json:"run_started_at"`
		Actor        *ghActor `json:"actor"`
		Triggering   *ghActor `json:"triggering_actor"`
		HeadCommit   *struct {
			Message string `json:"message"`
		} `json:"head_commit"`
	} `json:"workflow_run"`
	Repository ghRepo `json:"repository"`
}

type ghJobEvent struct {
	Action      string `json:"action"`
	WorkflowJob struct {
		ID           int64   `json:"id"`
		RunID        int64   `json:"run_id"`
		RunAttempt   int64   `json:"run_attempt"`
		WorkflowName string  `json:"workflow_name"`
		HeadBranch   string  `json:"head_branch"`
		HeadSHA      string  `json:"head_sha"`
		Name         string  `json:"name"`
		Status       string  `json:"status"`
		Conclusion   *string `json:"conclusion"`
		HTMLURL      string  `json:"html_url"`
		CreatedAt    string  `json:"created_at"`
		StartedAt    string  `json:"started_at"`
		CompletedAt  string  `json:"completed_at"`
		RunnerName   string  `json:"runner_name"`
		Steps        []struct {
			Name        string  `json:"name"`
			Status      string  `json:"status"`
			Conclusion  *string `json:"conclusion"`
			StartedAt   string  `json:"started_at"`
			CompletedAt string  `json:"completed_at"`
		} `json:"steps"`
	} `json:"workflow_job"`
	Repository ghRepo `json:"repository"`
}

func strOf(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func ghSignatureValid(secret string, body []byte, header string) bool {
	sig, ok := strings.CutPrefix(strings.TrimSpace(header), "sha256=")
	if !ok || secret == "" {
		return false
	}
	want, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), want)
}

func ghService(r *http.Request, repo ghRepo) string {
	if v := strings.TrimSpace(r.URL.Query().Get("service")); v != "" && len(v) <= 200 {
		return v
	}
	return repo.Name
}

func parseGitHubEvent(event string, body []byte, service func(ghRepo) string) (*ciRun, error) {
	switch event {
	case "workflow_run":
		var ev ghRunEvent
		if err := json.Unmarshal(body, &ev); err != nil {
			return nil, err
		}
		wr := ev.WorkflowRun
		if wr.ID == 0 {
			return nil, errors.New("workflow_run without a run")
		}
		run := &ciRun{
			Provider: "github", Repo: ev.Repository.FullName, Workflow: wr.Name, RunID: strconv.FormatInt(wr.ID, 10),
			Number: wr.RunNumber, Attempt: wr.RunAttempt, Event: wr.Event, Branch: wr.HeadBranch, DefaultBranch: ev.Repository.DefaultBranch,
			Commit: wr.HeadSHA, URL: wr.HTMLURL, Service: service(ev.Repository),
			Queued: ciTime(wr.CreatedAt), Started: ciTime(wr.RunStartedAt),
		}
		if wr.Triggering != nil {
			run.Actor = wr.Triggering.Login
		} else if wr.Actor != nil {
			run.Actor = wr.Actor.Login
		}
		if wr.HeadCommit != nil {
			run.CommitMessage = strings.SplitN(wr.HeadCommit.Message, "\n", 2)[0]
		}
		run.Status, run.Conclusion = ciStatus(wr.Status, strOf(wr.Conclusion))
		if run.Attempt > 1 || run.Queued.IsZero() {
			// A re-run keeps the first attempt's created_at: it was queued
			// when it started
			run.Queued = fallbackTime(run.Started, run.Queued)
		}
		if run.Status == "completed" {
			run.Finished = ciTime(wr.UpdatedAt)
		}
		return run, nil
	case "workflow_job":
		var ev ghJobEvent
		if err := json.Unmarshal(body, &ev); err != nil {
			return nil, err
		}
		wj := ev.WorkflowJob
		if wj.ID == 0 || wj.RunID == 0 {
			return nil, errors.New("workflow_job without a job")
		}
		run := &ciRun{
			Provider: "github", Repo: ev.Repository.FullName, Workflow: wj.WorkflowName, RunID: strconv.FormatInt(wj.RunID, 10),
			Attempt: wj.RunAttempt, Branch: wj.HeadBranch, Commit: wj.HeadSHA, Service: service(ev.Repository), jobOnly: true,
		}
		job := ciJob{ID: strconv.FormatInt(wj.ID, 10), Name: wj.Name, Runner: wj.RunnerName, URL: wj.HTMLURL,
			Queued: ciTime(wj.CreatedAt), Started: ciTime(wj.StartedAt)}
		job.Status, job.Conclusion = ciStatus(wj.Status, strOf(wj.Conclusion))
		if job.Status == "queued" {
			// GitHub fills started_at with the queue time until a runner picks the job up
			job.Started = time.Time{}
		}
		if job.Status == "completed" {
			job.Finished = ciTime(wj.CompletedAt)
		}
		for _, st := range head(wj.Steps, ciMaxSteps) {
			_, c := ciStatus(st.Status, strOf(st.Conclusion))
			job.Steps = append(job.Steps, ciStep{Name: st.Name, Conclusion: c, StartedAt: st.StartedAt, FinishedAt: st.CompletedAt})
		}
		run.Jobs = []ciJob{job}
		return run, nil
	}
	return nil, nil
}

func (s *Server) handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	secret := s.conf("github_webhook_secret")
	if secret == "" {
		writeError(w, http.StatusForbidden, "set the GitHub webhook secret in Settings → Server → CI first")
		return
	}
	body, err := io.ReadAll(io.LimitReader(http.MaxBytesReader(w, r.Body, 10<<20), 10<<20))
	if err != nil {
		writeError(w, otlpReadStatus(err), "the body could not be read")
		return
	}
	if !ghSignatureValid(secret, body, r.Header.Get("X-Hub-Signature-256")) {
		writeError(w, http.StatusUnauthorized, "the signature does not match the webhook secret")
		return
	}
	event := r.Header.Get("X-GitHub-Event")
	if event == "ping" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	run, err := parseGitHubEvent(event, body, func(repo ghRepo) string { return ghService(r, repo) })
	if err != nil {
		writeError(w, http.StatusBadRequest, "the payload could not be read: "+err.Error())
		return
	}
	if run == nil {
		writeJSON(w, http.StatusAccepted, map[string]any{"ignored": event})
		return
	}
	if err := s.saveRun(r.Context(), run); err != nil {
		s.logger.Warn("ci webhook", "err", err)
		writeError(w, http.StatusServiceUnavailable, "the run could not be stored")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"stored": event})
}

type ciRunInput struct {
	Provider      string `json:"provider"`
	Repo          string `json:"repo"`
	Workflow      string `json:"workflow"`
	RunID         any    `json:"run_id"`
	Number        int64  `json:"number"`
	Attempt       int64  `json:"attempt"`
	Event         string `json:"event"`
	Branch        string `json:"branch"`
	DefaultBranch string `json:"default_branch"`
	Commit        string `json:"commit"`
	CommitMessage string `json:"commit_message"`
	Actor         string `json:"actor"`
	Status        string `json:"status"`
	Conclusion    string `json:"conclusion"`
	URL           string `json:"url"`
	Service       string `json:"service"`
	QueuedAt      string `json:"queued_at"`
	StartedAt     string `json:"started_at"`
	FinishedAt    string `json:"finished_at"`
	Jobs          []struct {
		ID         any      `json:"id"`
		Name       string   `json:"name"`
		Status     string   `json:"status"`
		Conclusion string   `json:"conclusion"`
		Runner     string   `json:"runner"`
		URL        string   `json:"url"`
		QueuedAt   string   `json:"queued_at"`
		StartedAt  string   `json:"started_at"`
		FinishedAt string   `json:"finished_at"`
		Steps      []ciStep `json:"steps"`
	} `json:"jobs"`
}

func idString(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	}
	return ""
}

func (in ciRunInput) run() (*ciRun, error) {
	run := &ciRun{
		Provider: strings.ToLower(fallback(strings.TrimSpace(in.Provider), "ci")), Repo: strings.TrimSpace(in.Repo),
		Workflow: strings.TrimSpace(in.Workflow), RunID: idString(in.RunID), Number: in.Number, Attempt: in.Attempt,
		Event: in.Event, Branch: strings.TrimSpace(in.Branch), DefaultBranch: strings.TrimSpace(in.DefaultBranch),
		Commit: strings.ToLower(strings.TrimSpace(in.Commit)), CommitMessage: strings.SplitN(in.CommitMessage, "\n", 2)[0],
		Actor: in.Actor, URL: in.URL, Service: strings.TrimSpace(in.Service),
		Queued: ciTime(in.QueuedAt), Started: ciTime(in.StartedAt), Finished: ciTime(in.FinishedAt),
	}
	if run.RunID == "" {
		return nil, errors.New("each run needs a run_id")
	}
	if len(run.Provider) > 40 || len(run.Repo) > 200 || len(run.RunID) > 100 || len(run.Workflow) > 300 {
		return nil, errors.New("provider, repo, run_id or workflow is too long")
	}
	if run.Queued.IsZero() {
		run.Queued = run.Started
	}
	run.Status, run.Conclusion = ciStatus(in.Status, in.Conclusion)
	if run.Status == "completed" && run.Finished.IsZero() {
		run.Finished = time.Now().UTC()
	}
	if len(in.Jobs) > ciMaxJobsPerRun {
		return nil, errors.New("at most 500 jobs per run")
	}
	for _, j := range in.Jobs {
		job := ciJob{ID: idString(j.ID), Name: ciClip(j.Name, 300), Runner: j.Runner, URL: j.URL,
			Queued: ciTime(j.QueuedAt), Started: ciTime(j.StartedAt), Finished: ciTime(j.FinishedAt), Steps: head(j.Steps, ciMaxSteps)}
		if job.Name == "" && job.ID == "" {
			return nil, errors.New("each job needs a name or an id")
		}
		job.Status, job.Conclusion = ciStatus(j.Status, j.Conclusion)
		run.Jobs = append(run.Jobs, job)
	}
	return run, nil
}

func (s *Server) handleCIRuns(w http.ResponseWriter, r *http.Request) {
	if !s.ingestKeyValid(logIngestKey(r), "server") {
		writeError(w, http.StatusUnauthorized, "missing or invalid ingest key")
		return
	}
	body, status, err := otlpReadBody(w, r)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	var inputs []ciRunInput
	if trimmed := strings.TrimSpace(string(body)); strings.HasPrefix(trimmed, "[") {
		err = json.Unmarshal(body, &inputs)
	} else {
		var one struct {
			ciRunInput
			Runs []ciRunInput `json:"runs"`
		}
		err = json.Unmarshal(body, &one)
		inputs = one.Runs
		if inputs == nil {
			inputs = []ciRunInput{one.ciRunInput}
		}
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(inputs) == 0 || len(inputs) > ciMaxRunsPerPost {
		writeError(w, http.StatusBadRequest, "send 1 to 100 runs")
		return
	}
	runs := make([]*ciRun, 0, len(inputs))
	for _, in := range inputs {
		run, err := in.run()
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		runs = append(runs, run)
	}
	for _, run := range runs {
		if err := s.saveRun(r.Context(), run); err != nil {
			s.logger.Warn("ci runs", "err", err)
			writeError(w, http.StatusServiceUnavailable, "the runs could not be stored")
			return
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"runs": len(runs)})
}
