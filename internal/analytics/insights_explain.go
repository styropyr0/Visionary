package analytics

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// A running request that sits longer than this was claimed by an instance
// that went away; asking again queues a fresh one.
const explainStale = 2 * time.Minute

type explainRequest struct {
	ID         int64            `json:"id"`
	CreatedAt  string           `json:"created_at"`
	Target     string           `json:"target"`
	Statement  string           `json:"statement"`
	Status     string           `json:"status"`
	ClaimedBy  string           `json:"claimed_by,omitempty"`
	Plan       string           `json:"plan,omitempty"`
	Error      string           `json:"error,omitempty"`
	FinishedAt string           `json:"finished_at,omitempty"`
	Findings   []explainFinding `json:"findings,omitempty"`
}

type explainFinding struct {
	Severity string `json:"severity"` // "warning" or "info"
	Title    string `json:"title"`
	Detail   string `json:"detail"`
}

func (s *Server) latestExplain(ctx context.Context, target, statement string) (*explainRequest, error) {
	rows, err := s.db.Query(ctx, `SELECT id, created_at, target, statement, status, claimed_by, plan, error, finished_at
FROM explain_requests WHERE target = ? AND statement = ? ORDER BY id DESC LIMIT 1`, target, statement)
	if err != nil {
		return nil, dbErr(err)
	}
	if len(rows.Rows) == 0 {
		return nil, nil
	}
	r := rows.Rows[0]
	e := &explainRequest{ID: toInt(r[0]), CreatedAt: toString(r[1]), Target: toString(r[2]), Statement: toString(r[3]),
		Status: toString(r[4]), ClaimedBy: toString(r[5]), Plan: toString(r[6]), Error: toString(r[7]), FinishedAt: toString(r[8])}
	if e.Status == "done" {
		e.Findings = planFindings(e.Target, e.Plan)
	}
	return e, nil
}

// handleExplainGet answers the latest plan request for a statement on a
// database (any status), with findings once it is done.
func (s *Server) handleExplainGet(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	target, statement := q.Get("target"), q.Get("statement")
	if target == "" || statement == "" {
		writeError(w, http.StatusBadRequest, "target and statement are required")
		return
	}
	e, err := s.latestExplain(r.Context(), target, statement)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"request": e})
}

// handleExplainCreate queues a plan request, unless one for the same
// statement is still waiting or finished in the last hour.
func (s *Server) handleExplainCreate(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "edit") {
		return
	}
	var body struct{ Target, Statement string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	body.Target, body.Statement = strings.TrimSpace(body.Target), strings.TrimSpace(body.Statement)
	if body.Target == "" || body.Statement == "" || len(body.Statement) > 20000 {
		writeError(w, http.StatusBadRequest, "target and statement are required")
		return
	}
	prev, err := s.latestExplain(r.Context(), body.Target, body.Statement)
	if err != nil {
		s.fail(w, err)
		return
	}
	if prev != nil {
		age := time.Since(parseTS(prev.CreatedAt))
		switch {
		case prev.Status == "pending",
			prev.Status == "running" && age < explainStale,
			prev.Status == "done" && age < time.Hour && r.URL.Query().Get("again") == "":
			writeJSON(w, http.StatusOK, map[string]any{"request": prev})
			return
		}
	}
	id, err := s.db.InsertID(r.Context(), `INSERT INTO explain_requests (created_at, target, statement, status) VALUES (?, ?, ?, 'pending')`,
		time.Now().UTC().Format(tsLayout), body.Target, body.Statement)
	if err != nil {
		s.fail(w, dbErr(err))
		return
	}
	s.audit(r, "explain.request", body.Target, map[string]any{"id": id})
	e, err := s.latestExplain(r.Context(), body.Target, body.Statement)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"request": e})
}

// handleExplainSummary asks the model to explain a finished plan in plain
// words and say what would make the statement faster.
func (s *Server) handleExplainSummary(w http.ResponseWriter, r *http.Request) {
	if s.llm() == nil {
		writeError(w, http.StatusBadRequest, "the AI analyst is off: add an OpenAI API key in Settings → Server")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT target, statement, plan FROM explain_requests WHERE id = ? AND status = 'done'`, id)
	if err != nil {
		s.fail(w, dbErr(err))
		return
	}
	if len(rows.Rows) == 0 {
		writeError(w, http.StatusNotFound, "no finished plan with that id")
		return
	}
	target, statement, plan := toString(rows.Rows[0][0]), toString(rows.Rows[0][1]), toString(rows.Rows[0][2])
	findings, _ := json.Marshal(planFindings(target, plan))
	prompt := `You explain database query plans to application developers who are not database specialists.
Reply with one JSON object: {"summary": "<text>"}.
The text: one sentence on how the database runs this statement, then 2 to 4 short lines starting with "- " on what costs the most and what would make it faster. When an index would help, give the exact CREATE INDEX statement. Say plainly when the plan is already fine. Values in the statement were replaced by ? or $n, so the plan is a generic one; do not comment on that. No headings.`
	user := "Database: " + target + "\n\nStatement:\n" + statement + "\n\nPlan:\n" + plan + "\n\nAutomatic findings: " + string(findings)
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	content, err := s.llm().chatJSON(ctx, []chatMessage{{Role: "system", Content: prompt}, {Role: "user", Content: user}})
	if err != nil {
		s.fail(w, err)
		return
	}
	var out struct {
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal([]byte(content), &out); err != nil || out.Summary == "" {
		writeError(w, http.StatusBadGateway, "the model did not return a summary")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

var (
	pgSeqScan   = regexp.MustCompile(`Seq Scan on (\S+)(?: \S+)?\s+\(cost=[\d.]+\.\.([\d.]+) rows=(\d+)`)
	pgFilter    = regexp.MustCompile(`^\s*Filter: (.+)$`)
	pgSort      = regexp.MustCompile(`^\s*(?:->\s*)?Sort\s+\(cost=[\d.]+\.\.[\d.]+ rows=(\d+)`)
	pgTotalCost = regexp.MustCompile(`cost=[\d.]+\.\.([\d.]+) rows=(\d+)`)
	sqliteScan  = regexp.MustCompile(`^\s*SCAN (?:TABLE )?(\S+)(.*)$`)
)

// planFindings points out what usually makes a statement slow: full table
// scans of big tables (with the filter an index could serve), large sorts
// and temporary tables. The dialect comes from the target's prefix.
func planFindings(target, plan string) []explainFinding {
	var out []explainFinding
	lines := strings.Split(plan, "\n")
	switch {
	case strings.HasPrefix(target, "postgres"):
		for i, l := range lines {
			if m := pgSeqScan.FindStringSubmatch(l); m != nil {
				rows, _ := strconv.Atoi(m[3])
				filter := ""
				for _, next := range lines[i+1 : min(i+4, len(lines))] {
					if f := pgFilter.FindStringSubmatch(next); f != nil {
						filter = f[1]
						break
					}
				}
				// The scan's own cost tells a big table from a small one; a
				// filtered scan of a few pages is cheaper than using an index.
				cost, _ := strconv.ParseFloat(m[2], 64)
				sev, why := "info", "Reads the whole table; fine while it stays small."
				if rows >= 1000 || (filter != "" && cost >= 1000) {
					sev = "warning"
					why = "Reads every row of " + m[1] + " to find the ones it needs (the planner expects " + strconv.Itoa(rows) + ")."
					if filter != "" {
						why += " An index on the columns in " + filter + " would let it read only the matching rows."
					}
				}
				out = append(out, explainFinding{sev, "Full scan of " + m[1], why})
			}
			if m := pgSort.FindStringSubmatch(l); m != nil {
				if rows, _ := strconv.Atoi(m[1]); rows >= 10000 {
					out = append(out, explainFinding{"warning", "Large sort", "Sorts about " + m[1] + " rows; an index matching the ORDER BY avoids the sort."})
				}
			}
		}
		for _, l := range lines {
			if strings.HasPrefix(strings.TrimSpace(l), "--") || strings.TrimSpace(l) == "" {
				continue
			}
			if m := pgTotalCost.FindStringSubmatch(l); m != nil {
				out = append(out, explainFinding{"info", "Estimated cost " + m[1], "The planner's estimate for the whole statement, about " + m[2] + " rows returned. Compare it between versions of the query."})
			}
			break
		}
	case strings.HasPrefix(target, "mysql"):
		var table string
		for _, l := range lines {
			k, v, ok := strings.Cut(l, ": ")
			if !ok {
				continue
			}
			switch k {
			case "table":
				table = v
			case "type":
				if v == "ALL" {
					out = append(out, explainFinding{"warning", "Full scan of " + table, "MySQL reads every row of " + table + "; an index on the columns in the WHERE clause would avoid it."})
				}
			case "Extra":
				if strings.Contains(v, "Using filesort") {
					out = append(out, explainFinding{"warning", "Sort without an index", "Rows of " + table + " are sorted after reading; an index matching the ORDER BY avoids it."})
				}
				if strings.Contains(v, "Using temporary") {
					out = append(out, explainFinding{"warning", "Temporary table", "MySQL builds a temporary table for " + table + " (usually GROUP BY or DISTINCT)."})
				}
			}
		}
	default: // SQLite
		for _, l := range lines {
			t := strings.TrimSpace(l)
			if m := sqliteScan.FindStringSubmatch(t); m != nil && !strings.Contains(m[2], "USING") && !strings.Contains(m[2], "INDEX") {
				out = append(out, explainFinding{"warning", "Full scan of " + m[1], "SQLite reads every row of " + m[1] + "; an index on the columns in the WHERE clause would avoid it."})
			}
			if strings.Contains(t, "USE TEMP B-TREE") {
				out = append(out, explainFinding{"warning", "Temporary sort", t + ": an index matching it avoids the extra sort."})
			}
		}
	}
	return out
}
