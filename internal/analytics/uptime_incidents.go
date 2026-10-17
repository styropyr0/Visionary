package analytics

import (
	"context"
	"net/http"
	"time"
)

// An incident is a run of consecutive failed results of one check. It starts
// at the first failure and ends at the next success; without one it is
// ongoing. Runs are found in SQL (gaps and islands: within a run, the row
// number over all results minus the row number among failures is constant),
// so only the runs leave the database, not every result.
const incidentsSQL = `WITH r AS (
  SELECT created_at, ok, status, error,
    ROW_NUMBER() OVER (ORDER BY created_at) - ROW_NUMBER() OVER (PARTITION BY ok ORDER BY created_at) AS grp
  FROM uptime_results WHERE check_id = {{check}} AND created_at >= {{from}} AND created_at < {{to}}
),
i AS (
  SELECT MIN(created_at) AS started_at, MAX(created_at) AS last_failure_at, COUNT(*) AS failures,
    MAX(status) AS status, MIN(error) AS error
  FROM r WHERE ok = 0 GROUP BY grp
)
SELECT i.started_at,
  (SELECT MIN(u.created_at) FROM uptime_results u
   WHERE u.check_id = {{check}} AND u.ok = 1 AND u.created_at > i.last_failure_at) AS ended_at,
  i.failures, i.status, i.error
FROM i ORDER BY i.started_at DESC LIMIT 200`

type incident struct {
	StartedAt string  `json:"started_at"`
	EndedAt   *string `json:"ended_at"`
	DurationS int64   `json:"duration_s"`
	Failures  int64   `json:"failures"`
	Status    int64   `json:"status"`
	Error     string  `json:"error,omitempty"`
	Ongoing   bool    `json:"ongoing"`
}

func (s *Server) checkIncidents(ctx context.Context, id int64, tr timeRange) ([]incident, *Result, error) {
	res, err := s.runTemplate(ctx, withCheck(incidentsSQL, id), tr, true)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now().UTC()
	out := make([]incident, 0, len(res.Rows))
	for _, row := range res.Rows {
		in := incident{StartedAt: toString(row[0]), Failures: toInt(row[2]), Status: toInt(row[3]), Error: toString(row[4])}
		start, _ := time.Parse(tsLayout, in.StartedAt)
		end := now
		if e := toString(row[1]); e != "" {
			in.EndedAt = &e
			end, _ = time.Parse(tsLayout, e)
		} else {
			in.Ongoing = true
		}
		in.DurationS = int64(end.Sub(start).Seconds())
		out = append(out, in)
	}
	return out, res, nil
}

func (s *Server) handleUptimeIncidents(w http.ResponseWriter, r *http.Request) {
	id, ok := checkID(w, r)
	if !ok {
		return
	}
	tr, err := parseRange(r.URL.Query(), s.cfg.MaxRangeDays)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	list, res, err := s.checkIncidents(r.Context(), id, tr)
	if err != nil {
		s.fail(w, err)
		return
	}
	// The table twin carries the computed duration and state too.
	res.Columns = []string{"started_at", "ended_at", "duration_s", "failures", "status", "error"}
	rows := make([][]any, len(list))
	for i, in := range list {
		var ended any
		if in.EndedAt != nil {
			ended = *in.EndedAt
		}
		rows[i] = []any{in.StartedAt, ended, in.DurationS, in.Failures, in.Status, in.Error}
	}
	res.Rows = rows
	writeJSON(w, http.StatusOK, map[string]any{"incidents": list, "result": res})
}
