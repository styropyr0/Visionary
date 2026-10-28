package analytics

import (
	"context"
	"net/http"
	"strconv"
	"time"
)

func (s *Server) applogsAnalysisRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/applogs/patterns", s.authed(s.handleLogPatterns))
	mux.Handle("GET /api/applogs/compare", s.authed(s.handleLogCompare))
	mux.Handle("GET /api/applogs/volume", s.authed(s.handleLogVolume))
	mux.Handle("GET /api/applogs/hidden-failures", s.authed(s.handleHiddenFailures))
}

// handleLogVolume says where log volume comes from: by service, source and
// level, the code lines that log the most, and the busy patterns nobody
// looks at (candidates to lower or drop).
func (s *Server) handleLogVolume(w http.ResponseWriter, r *http.Request) {
	ls, err := s.logSearchOf(r)
	if err != nil {
		s.logFail(w, err)
		return
	}
	ctx := r.Context()
	group := func(cols string, extra string, limit int) (*Result, error) {
		q := `SELECT ` + cols + `, COUNT(*) AS lines, SUM(size) AS bytes FROM app_logs WHERE ` + ls.sql() + extra +
			` GROUP BY ` + cols + ` ORDER BY bytes DESC LIMIT ` + strconv.Itoa(limit)
		rows, err := s.db.Query(ctx, q, ls.args...)
		if err != nil {
			return nil, dbErr(err)
		}
		return &Result{Columns: rows.Columns, Rows: rows.Rows}, nil
	}
	out := map[string]any{}
	for key, spec := range map[string][3]string{
		"services": {"service", "", "50"},
		"sources":  {"source", "", "30"},
		"levels":   {"level", "", "6"},
		"code":     {"service, code_file, code_line", " AND code_file IS NOT NULL", "30"},
	} {
		n, _ := strconv.Atoi(spec[2])
		res, err := group(spec[0], spec[1], n)
		if err != nil {
			s.logFail(w, err)
			return
		}
		out[key] = res
	}
	var total, bytes int64
	if res, err := s.db.Query(ctx, `SELECT COUNT(*), SUM(size) FROM app_logs WHERE `+ls.sql(), ls.args...); err == nil && len(res.Rows) > 0 {
		total, bytes = toInt(res.Rows[0][0]), toInt(res.Rows[0][1])
	}
	out["lines"], out["bytes"] = total, bytes
	// A sample message per code line, so the list reads as code, not paths.
	if code, ok := out["code"].(*Result); ok && len(code.Rows) > 0 {
		code.Columns = append(code.Columns, "sample")
		for i, row := range code.Rows {
			sample := ""
			res, err := s.db.Query(ctx, `SELECT message FROM app_logs WHERE `+ls.sql()+` AND service `+eqOrNull(row[0])+` AND code_file = ? AND code_line `+eqOrNull(row[2])+` ORDER BY id DESC LIMIT 1`,
				append(append(append([]any{}, ls.args...), nonNilArgs(row[0])...), append([]any{row[1]}, nonNilArgs(row[2])...)...)...)
			if err == nil && len(res.Rows) > 0 {
				sample = clip(toString(res.Rows[0][0]), 200)
			}
			code.Rows[i] = append(row, sample)
		}
	}
	quiet, err := s.quietPatterns(ctx, ls, bytes)
	if err != nil {
		s.logFail(w, err)
		return
	}
	out["unread"] = quiet
	writeJSON(w, http.StatusOK, out)
}

func eqOrNull(v any) string {
	if v == nil {
		return "IS NULL"
	}
	return "= ?"
}

func nonNilArgs(v any) []any {
	if v == nil {
		return nil
	}
	return []any{v}
}

// quietPatterns are patterns that make up at least 2% of the bytes in the
// range, that nobody has opened in 30 days and no alert watches.
func (s *Server) quietPatterns(ctx context.Context, ls *logSearch, total int64) ([]map[string]any, error) {
	if total == 0 {
		return []map[string]any{}, nil
	}
	rows, err := s.db.Query(ctx, `SELECT pattern, COUNT(*) AS lines, SUM(size) AS bytes FROM app_logs WHERE `+ls.sql()+
		` AND pattern IS NOT NULL GROUP BY pattern ORDER BY bytes DESC LIMIT 20`, ls.args...)
	if err != nil {
		return nil, dbErr(err)
	}
	watched := s.alertedPatterns(ctx)
	cutoff := time.Now().UTC().AddDate(0, 0, -30).Format(tsLayout)
	out := []map[string]any{}
	for _, row := range rows.Rows {
		id, b := toString(row[0]), toInt(row[2])
		if float64(b) < float64(total)*0.02 || watched[id] {
			continue
		}
		pr, err := s.db.Query(ctx, `SELECT service, template, level, viewed_at, code_file, code_line FROM log_patterns WHERE id = ?`, id)
		if err != nil || len(pr.Rows) == 0 {
			continue
		}
		p := pr.Rows[0]
		if v := toString(p[3]); v != "" && v >= cutoff {
			continue
		}
		out = append(out, map[string]any{
			"id": id, "service": p[0], "template": p[1], "level": p[2], "code_file": p[4], "code_line": p[5],
			"lines": toInt(row[1]), "bytes": b, "share": float64(b) / float64(total),
		})
	}
	return out, nil
}

// handleHiddenFailures finds errors logged inside requests that answered
// 2xx or 3xx: failures the API didn't report to its client.
func (s *Server) handleHiddenFailures(w http.ResponseWriter, r *http.Request) {
	ls, err := s.logSearchOf(r)
	if err != nil {
		s.logFail(w, err)
		return
	}
	where := ls.sql()
	// The search applies to the log lines; qualify their columns.
	q := `SELECT l.pattern, a.method, a.route, COUNT(*) AS lines, COUNT(DISTINCT a.request_id) AS requests, MAX(l.id) AS sample_id,
  MAX(a.request_id) AS sample_request, MAX(l.created_at) AS last, MAX(a.status) AS status
FROM (SELECT * FROM app_logs WHERE ` + where + ` AND severity >= 5 AND request_id IS NOT NULL) AS l
JOIN api_logs a ON a.request_id = l.request_id AND a.created_at >= ? AND a.created_at < ?
WHERE a.status < 400
GROUP BY l.pattern, a.method, a.route ORDER BY lines DESC LIMIT 50`
	args := append(append([]any{}, ls.args...), ls.tr.requestsFrom(), ls.tr.To.Format(tsLayout))
	rows, err := s.db.Query(r.Context(), q, args...)
	if err != nil {
		s.logFail(w, dbErr(err))
		return
	}
	ids := []string{}
	for _, row := range rows.Rows {
		if p := toString(row[0]); p != "" {
			ids = append(ids, p)
		}
	}
	templates := s.patternTemplates(r.Context(), ids)
	out := []map[string]any{}
	for _, row := range rows.Rows {
		out = append(out, map[string]any{
			"pattern": row[0], "template": templates[toString(row[0])], "method": row[1], "route": row[2], "lines": toInt(row[3]),
			"requests": toInt(row[4]), "sample_id": toInt(row[5]), "sample_request": row[6], "last": row[7], "status": toInt(row[8]),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"failures": out})
}

// alertedPatterns are the patterns enabled alert rules watch.
func (s *Server) alertedPatterns(ctx context.Context) map[string]bool {
	return s.logAlertPatterns(ctx)
}
