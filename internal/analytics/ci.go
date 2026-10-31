package analytics

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	sdk "github.com/styropyr0/visionary/sdks/go"
)

// CI visibility: pipelines, their jobs and the tests they ran. GitHub Actions
// posts to a signed webhook; other CI systems post JSON and JUnit XML with a
// server key.

func init() {
	for _, t := range []string{"CI_PIPELINES", "CI_JOBS", "CI_TESTS", "CI_FLAKY_TESTS"} {
		allowedTables[t] = true
	}
}

func (s *Server) ciSchema() []string {
	id, key, ts, num := "INTEGER PRIMARY KEY AUTOINCREMENT", "TEXT", "TEXT", "REAL"
	tail, index, unique := "", "CREATE INDEX IF NOT EXISTS", "CREATE UNIQUE INDEX IF NOT EXISTS"
	switch s.q.d {
	case sdk.Postgres:
		id, num = "BIGSERIAL PRIMARY KEY", "DOUBLE PRECISION"
	case sdk.MySQL:
		id, key, ts, num = "BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY", "VARCHAR(191)", "VARCHAR(32)", "DOUBLE"
		tail, index, unique = " CHARACTER SET utf8mb4", "CREATE INDEX", "CREATE UNIQUE INDEX"
	}
	ddl := `CREATE TABLE IF NOT EXISTS ci_pipelines (
    id             {id},
    created_at     {ts} NOT NULL,
    updated_at     {ts},
    run_key        {key} NOT NULL,
    provider       TEXT,
    repo           TEXT,
    workflow       TEXT,
    run_id         TEXT,
    run_number     BIGINT,
    attempt        INTEGER,
    event          TEXT,
    branch         TEXT,
    default_branch TEXT,
    commit_sha     {key},
    commit_message TEXT,
    actor          TEXT,
    status         TEXT,
    conclusion     TEXT,
    url            TEXT,
    started_at     {ts},
    finished_at    {ts},
    queue_ms       BIGINT,
    duration_ms    BIGINT,
    service        TEXT
){tail};
{unique} idx_ci_pipelines_key ON ci_pipelines (run_key);
{index} idx_ci_pipelines_created ON ci_pipelines (created_at);
{index} idx_ci_pipelines_commit ON ci_pipelines (commit_sha);
CREATE TABLE IF NOT EXISTS ci_jobs (
    id          {id},
    created_at  {ts} NOT NULL,
    updated_at  {ts},
    job_key     {key} NOT NULL,
    run_key     {key},
    provider    TEXT,
    repo        TEXT,
    workflow    TEXT,
    name        TEXT,
    branch      TEXT,
    commit_sha  TEXT,
    service     TEXT,
    status      TEXT,
    conclusion  TEXT,
    runner      TEXT,
    url         TEXT,
    started_at  {ts},
    finished_at {ts},
    queue_ms    BIGINT,
    duration_ms BIGINT,
    steps       TEXT
){tail};
{unique} idx_ci_jobs_key ON ci_jobs (job_key);
{index} idx_ci_jobs_run ON ci_jobs (run_key);
{index} idx_ci_jobs_created ON ci_jobs (created_at);
CREATE TABLE IF NOT EXISTS ci_tests (
    id          {id},
    created_at  {ts} NOT NULL,
    run_key     {key},
    provider    TEXT,
    repo        TEXT,
    service     TEXT,
    branch      TEXT,
    commit_sha  TEXT,
    job         TEXT,
    suite       TEXT,
    test        TEXT NOT NULL,
    test_hash   {key} NOT NULL,
    file        TEXT,
    status      TEXT NOT NULL,
    duration_ms {num},
    message     TEXT
){tail};
{index} idx_ci_tests_created ON ci_tests (created_at);
{index} idx_ci_tests_hash ON ci_tests (test_hash, created_at);
{index} idx_ci_tests_run ON ci_tests (run_key);
CREATE TABLE IF NOT EXISTS ci_flaky_tests (
    id         {id},
    created_at {ts} NOT NULL,
    test_hash  {key} NOT NULL,
    test       TEXT NOT NULL,
    service    TEXT,
    repo       TEXT,
    reason     TEXT,
    commit_sha TEXT
){tail};
{unique} idx_ci_flaky_hash ON ci_flaky_tests (test_hash);
{index} idx_ci_flaky_created ON ci_flaky_tests (created_at)`
	return []string{strings.NewReplacer("{id}", id, "{key}", key, "{ts}", ts, "{num}", num, "{tail}", tail, "{index}", index, "{unique}", unique).Replace(ddl)}
}

func (s *Server) ciRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/ingest/ci/github", s.handleGitHubWebhook)
	mux.HandleFunc("POST /api/ingest/ci/runs", s.handleCIRuns)
	mux.HandleFunc("POST /api/ingest/ci/junit", s.handleJUnit)
	mux.Handle("GET /api/ci/summary", s.authed(s.handleCISummary))
	mux.Handle("GET /api/ci/runs", s.authed(s.handleCIRunList))
	mux.Handle("GET /api/ci/runs/{id}", s.authed(s.handleCIRun))
	mux.Handle("GET /api/ci/tests", s.authed(s.handleCITests))
	mux.Handle("GET /api/ci/tests/history", s.authed(s.handleCITestHistory))
	mux.Handle("GET /api/ci/flaky", s.authed(s.handleCIFlaky))
	mux.Handle("GET /api/ci/builds", s.authed(s.handleCIBuilds))
	mux.Handle("GET /api/ci/setup", s.authed(s.handleCISetup))
}

func (s *Server) ciPrompt() string {
	return `Table ci_pipelines: CI workflow runs (GitHub Actions and any CI that posts runs), one row per run attempt.
- id, created_at TEXT (when the run was queued, same format as everywhere), provider ('github', 'gitlab', ...), repo ('org/name'), workflow, run_id, run_number, attempt, event ('push', 'pull_request', ...), branch, default_branch, commit_sha (full 40-hex SHA), actor, service
- status ('queued', 'in_progress', 'completed'), conclusion ('success', 'failure', 'cancelled', 'skipped', 'timed_out'; NULL until completed), url, started_at, finished_at TEXT, queue_ms (queued until started), duration_ms (started until finished)
- Success rate = conclusion = 'success' over status = 'completed' AND conclusion IN ('success', 'failure', 'timed_out').

Table ci_jobs: the jobs of each run. job_key, run_key (= ci_pipelines.run_key), name, workflow, branch, commit_sha, status, conclusion, runner, started_at, finished_at, queue_ms, duration_ms, steps (JSON list of {name, conclusion, started_at, finished_at}).

Table ci_tests: test results from JUnit uploads, one row per test case per upload. created_at, run_key, service, repo, branch, commit_sha, job, suite, test (class and name), test_hash (groups one test), file, status ('passed', 'failed', 'error', 'skipped'), duration_ms REAL, message (the failure, clipped).

Table ci_flaky_tests: each test the first time it was seen flaky (passing and failing on one commit, or flip-flopping on the default branch). created_at, test_hash, test, service, repo, reason ('same_commit', 'flip_flop'), commit_sha.
- A deploy is a releases row whose version starts with (or contains) a commit SHA prefix of at least 7 characters: join a run to what it shipped that way, then compare api_logs and errors before and after the deploy.`
}

func ciRunKey(provider, repo, runID string, attempt int64) string {
	return provider + ":" + repo + ":" + runID + ":" + strconv.FormatInt(max(attempt, 1), 10)
}

// ciTestHash groups one test's results: the same name in another service
// or repository is another test.
func ciTestHash(scope, test string) string {
	sum := sha256.Sum256([]byte(scope + "\x00" + test))
	return hex.EncodeToString(sum[:16])
}

// ciClip shortens free text to n bytes on a rune boundary.
func ciClip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
