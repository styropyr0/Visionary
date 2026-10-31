package analytics

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"
)

// A test is flaky when it passed and failed on the same commit, or when on
// the default branch it failed once between two passes at least twice. Its
// score is the share of the commits it ran on where it flaked

const ciFlakyLookback = 30 * 24 * time.Hour

type ciResult struct {
	hash, test, service, repo, branch, commit, status, file, message string
	at                                                               string
}

type flakyTest struct {
	Hash         string   `json:"hash"`
	Test         string   `json:"test"`
	Service      string   `json:"service"`
	Repo         string   `json:"repo"`
	File         string   `json:"file"`
	Runs         int      `json:"runs"`
	Failures     int      `json:"failures"`
	Commits      int      `json:"commits"`
	MixedCommits int      `json:"mixed_commits"`
	FlipFlops    int      `json:"flip_flops"`
	Score        float64  `json:"score"`
	Reason       string   `json:"reason"`
	LastFailedAt string   `json:"last_failed_at"`
	LastMessage  string   `json:"last_message"`
	FlakyAt      string   `json:"flaky_at"`
	FirstFlagged string   `json:"first_flagged,omitempty"`
	Recent       []string `json:"recent"`
	evidenceAt   string
	evidenceSHA  string
}

func ciFailed(status string) bool { return status == "failed" || status == "error" }

func findFlaky(rows []ciResult, defaults map[string]bool) []*flakyTest {
	var out []*flakyTest
	for start := 0; start < len(rows); {
		end := start
		for end < len(rows) && rows[end].hash == rows[start].hash {
			end++
		}
		if ft := scoreFlaky(rows[start:end], defaults); ft != nil {
			out = append(out, ft)
		}
		start = end
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].LastFailedAt > out[j].LastFailedAt
	})
	return out
}

func scoreFlaky(rows []ciResult, defaults map[string]bool) *flakyTest {
	ft := &flakyTest{Hash: rows[0].hash, Test: rows[0].test, Service: rows[0].service, Repo: rows[0].repo}
	type commitSeen struct{ pass, fail bool }
	commits := map[string]*commitSeen{}
	var onDefault []ciResult
	for _, r := range rows {
		if r.status == "skipped" {
			continue
		}
		ft.Runs++
		if r.file != "" {
			ft.File = r.file
		}
		if ciFailed(r.status) {
			ft.Failures++
			ft.LastFailedAt, ft.LastMessage = r.at, r.message
		}
		if r.commit != "" {
			c := commits[r.commit]
			if c == nil {
				c = &commitSeen{}
				commits[r.commit] = c
			}
			before := c.pass && c.fail
			if ciFailed(r.status) {
				c.fail = true
			} else {
				c.pass = true
			}
			if !before && c.pass && c.fail {
				ft.MixedCommits++
				ft.evidenceAt, ft.evidenceSHA = r.at, r.commit
				if ft.Reason == "" {
					ft.Reason = "same_commit"
				}
			}
		}
		if defaults[r.branch] {
			onDefault = append(onDefault, r)
		}
	}
	for i := 1; i+1 < len(onDefault); i++ {
		if ciFailed(onDefault[i].status) && !ciFailed(onDefault[i-1].status) && !ciFailed(onDefault[i+1].status) &&
			(onDefault[i].commit == "" || onDefault[i].commit != onDefault[i-1].commit && onDefault[i].commit != onDefault[i+1].commit) {
			ft.FlipFlops++
			if ft.FlipFlops >= 2 && (ft.evidenceAt == "" || onDefault[i+1].at > ft.evidenceAt) && ft.MixedCommits == 0 {
				ft.evidenceAt, ft.evidenceSHA = onDefault[i+1].at, onDefault[i+1].commit
			}
		}
	}
	if ft.MixedCommits == 0 && ft.FlipFlops < 2 {
		return nil
	}
	if ft.Reason == "" {
		ft.Reason = "flip_flop"
	}
	ft.Commits = max(len(commits), 1)
	ft.Score = math.Min(100, math.Round(1000*float64(ft.MixedCommits+ft.FlipFlops)/float64(ft.Commits))/10)
	ft.FlakyAt = ft.evidenceAt
	for _, r := range rows[max(0, len(rows)-20):] {
		ft.Recent = append(ft.Recent, r.status)
	}
	return ft
}

func (s *Server) ciDefaultBranches(ctx context.Context) map[string]bool {
	out := map[string]bool{"main": true, "master": true}
	res, err := s.db.Query(ctx, `SELECT DISTINCT default_branch FROM ci_pipelines WHERE default_branch IS NOT NULL LIMIT 50`)
	if err == nil {
		for _, row := range res.Rows {
			if b := toString(row[0]); b != "" {
				out[b] = true
			}
		}
	}
	return out
}

func (s *Server) ciResults(ctx context.Context, from, to string, hashes []string, extra string, extraArgs []any) ([]ciResult, error) {
	var failing []any
	if hashes == nil {
		res, err := s.db.Query(ctx, `SELECT DISTINCT test_hash FROM ci_tests WHERE created_at >= ? AND created_at < ? AND status IN ('failed', 'error')`+extra+` LIMIT 2000`,
			append([]any{from, to}, extraArgs...)...)
		if err != nil {
			return nil, err
		}
		for _, row := range res.Rows {
			failing = append(failing, toString(row[0]))
		}
	} else {
		for len(hashes) > 0 {
			chunk := head(hashes, maxInArgs)
			hashes = hashes[len(chunk):]
			args := []any{from, to}
			for _, h := range chunk {
				args = append(args, h)
			}
			res, err := s.db.Query(ctx, `SELECT DISTINCT test_hash FROM ci_tests WHERE created_at >= ? AND created_at < ? AND status IN ('failed', 'error')
  AND test_hash IN (`+placeholders(len(chunk))+`)`, args...)
			if err != nil {
				return nil, err
			}
			for _, row := range res.Rows {
				failing = append(failing, toString(row[0]))
			}
		}
	}
	var out []ciResult
	for len(failing) > 0 && len(out) < 100000 {
		chunk := head(failing, maxInArgs)
		failing = failing[len(chunk):]
		res, err := s.db.Query(ctx, `SELECT test_hash, test, service, repo, branch, commit_sha, status, file, message, created_at FROM ci_tests
WHERE created_at >= ? AND created_at < ? AND test_hash IN (`+placeholders(len(chunk))+`)
ORDER BY test_hash, created_at, id LIMIT 50000`, append([]any{from, to}, chunk...)...)
		if err != nil {
			return nil, err
		}
		for _, row := range res.Rows {
			out = append(out, ciResult{hash: toString(row[0]), test: toString(row[1]), service: toString(row[2]), repo: toString(row[3]),
				branch: toString(row[4]), commit: toString(row[5]), status: toString(row[6]), file: toString(row[7]), message: toString(row[8]), at: toString(row[9])})
		}
	}
	return out, nil
}

func (s *Server) noteFlaky(ctx context.Context, tests map[string]string, up ciUpload) error {
	if len(tests) == 0 {
		return nil
	}
	hashes := make([]string, 0, len(tests))
	for h := range tests {
		hashes = append(hashes, h)
	}
	sort.Strings(hashes)
	to := up.at.Add(time.Second)
	rows, err := s.ciResults(ctx, to.Add(-ciFlakyLookback).Format(tsLayout), to.Format(tsLayout), hashes, "", nil)
	if err != nil || len(rows) == 0 {
		return err
	}
	flaky := findFlaky(rows, s.ciDefaultBranches(ctx))
	if len(flaky) == 0 {
		return nil
	}
	known := map[string]bool{}
	ids := make([]any, 0, len(flaky))
	for _, f := range flaky {
		ids = append(ids, f.Hash)
	}
	for len(ids) > 0 {
		chunk := head(ids, maxInArgs)
		ids = ids[len(chunk):]
		res, err := s.db.Query(ctx, `SELECT test_hash FROM ci_flaky_tests WHERE test_hash IN (`+placeholders(len(chunk))+`)`, chunk...)
		if err != nil {
			return err
		}
		for _, row := range res.Rows {
			known[toString(row[0])] = true
		}
	}
	for _, f := range flaky {
		if known[f.Hash] {
			continue
		}
		err := s.db.Exec(ctx, `INSERT INTO ci_flaky_tests (created_at, test_hash, test, service, repo, reason, commit_sha) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			fallback(f.evidenceAt, up.at.Format(tsLayout)), f.Hash, f.Test, otlpNullStr(f.Service), otlpNullStr(f.Repo), f.Reason, otlpNullStr(f.evidenceSHA))
		if err != nil && !strings.Contains(strings.ToLower(err.Error()), "unique") && !strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			return err
		}
	}
	return nil
}
