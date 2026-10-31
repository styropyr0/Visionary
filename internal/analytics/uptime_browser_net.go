package analytics

import (
	"context"
	"encoding/base64"
	"strconv"
)

// Only the latest runs of a check keep their step screenshots; passing runs
// keep their requests for longer, failed ones for as long as the run is kept.
const (
	browserKeepStepRuns = 24
	browserKeepReqRuns  = 200
)

func encodeShots(shots [][]byte) []string {
	out := make([]string, len(shots))
	for i, b := range shots {
		if b != nil {
			out[i] = base64.StdEncoding.EncodeToString(b)
		}
	}
	return out
}

// pruneRunDetail drops the step screenshots of all but a check's latest
// runs, and the requests of all but its latest passing runs.
func (s *Server) pruneRunDetail(ctx context.Context, checkID int64) error {
	for _, p := range []struct {
		col, where string
		keep       int
	}{{"step_shots", "", browserKeepStepRuns}, {"requests", " AND ok = 1", browserKeepReqRuns}} {
		cond := `check_id = ? AND ` + p.col + ` IS NOT NULL` + p.where
		rows, err := s.db.Query(ctx, `SELECT created_at FROM uptime_browser_results WHERE `+cond+`
ORDER BY created_at DESC LIMIT 1 OFFSET `+strconv.Itoa(p.keep), checkID)
		if err != nil {
			return err
		}
		if len(rows.Rows) == 0 {
			continue
		}
		if err := s.db.Exec(ctx, `UPDATE uptime_browser_results SET `+p.col+` = NULL WHERE `+cond+` AND created_at <= ?`, checkID, toString(rows.Rows[0][0])); err != nil {
			return err
		}
	}
	return nil
}

// traceHasRows reports whether a backend recorded anything under the trace.
func (s *Server) traceHasRows(ctx context.Context, traceID string) bool {
	if traceID == "" {
		return false
	}
	for _, t := range []string{"api_logs", "spans"} {
		rows, err := s.db.Query(ctx, `SELECT 1 FROM `+t+` WHERE trace_id = ? LIMIT 1`, traceID)
		if err == nil && len(rows.Rows) > 0 {
			return true
		}
	}
	return false
}
