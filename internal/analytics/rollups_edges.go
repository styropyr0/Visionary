package analytics

import (
	"context"
	"strings"
	"time"
)

// rollup_edges keeps the service map's calls per bucket: one row per caller,
// kind, callee and the callee's route (calls between services only), with the latency histogram for p95. The source joins
// requests to their callers, so each leg is limited to the window itself;
// {{from}} and {{to}} are filled in by the build and by rawRowsSQL.
const edgesSource = `(
SELECT c.created_at, COALESCE(p.service, '') AS src, 'service' AS kind, COALESCE(c.service, '') AS dst, c.route, c.path, p.duration_ms,
  CASE WHEN c.status >= 500 OR p.status = 'error' THEN 1 ELSE 0 END AS failed
FROM api_logs c JOIN spans p ON p.trace_id = c.trace_id AND p.span_id = c.parent_span_id
WHERE c.created_at >= {{from}} AND c.created_at < {{to}} AND c.parent_span_id IS NOT NULL
  AND COALESCE(p.service, '') <> COALESCE(c.service, '')
UNION ALL
SELECT c.created_at, COALESCE(p.service, ''), 'service', COALESCE(c.service, ''), c.route, c.path, c.duration_ms,
  CASE WHEN c.status >= 500 THEN 1 ELSE 0 END
FROM api_logs c JOIN api_logs p ON p.trace_id = c.trace_id AND p.span_id = c.parent_span_id
WHERE c.created_at >= {{from}} AND c.created_at < {{to}} AND c.parent_span_id IS NOT NULL
  AND COALESCE(p.service, '') <> COALESCE(c.service, '')
UNION ALL
SELECT s.created_at, COALESCE(s.service, ''), s.kind, COALESCE(NULLIF(s.target, ''), s.name), NULL, NULL, s.duration_ms,
  CASE WHEN s.status = 'error' THEN 1 ELSE 0 END
FROM spans s
WHERE s.created_at >= {{from}} AND s.created_at < {{to}} AND s.kind IN ('db', 'http', 'cache', 'queue')
  AND (s.trace_id IS NULL OR NOT EXISTS (
    SELECT 1 FROM api_logs c WHERE c.trace_id = s.trace_id AND c.parent_span_id = s.span_id))
) AS e`

func edgesTable(routes []string) rollupTable {
	cols := []rollupCol{
		{"calls", colInt, "COUNT(*)", "1", "SUM(calls)"},
		{"errors", colInt, "SUM(failed)", "failed", "SUM(errors)"},
		{"min_ms", colFloat, "MIN(duration_ms)", "duration_ms", "MIN(min_ms)"},
		{"max_ms", colFloat, "MAX(duration_ms)", "duration_ms", "MAX(max_ms)"},
	}
	for i := range len(rollupEdges) + 1 {
		cols = append(cols, countCol(histCol(i), histCond(i)))
	}
	return rollupTable{
		name:   "rollup_edges",
		source: edgesSource,
		keys: []rollupKey{
			{"src", colText, 64, "src"},
			{"kind", colText, 16, "kind"},
			{"dst", colText, 255, "dst"},
			{"route", colText, 255, "CASE WHEN kind = 'service' THEN " + routeCap(routes) + " ELSE '' END"},
		},
		cols: cols,
	}
}

// fillWindow puts a build's window into a source that filters inside its legs
func fillWindow(sql, from, to string) string {
	return strings.NewReplacer("{{from}}", "'"+from+"'", "{{to}}", "'"+to+"'").Replace(sql)
}

// Shorter maps read raw rows: they are cheap and their p95 is exact
const mapRollupMin = time.Hour

// serviceMapRollup answers the map's three queries from rollup_requests and
// rollup_edges, in the raw queries' columns. ok is false when the rollups do
// not cover the range or a query failed.
func (s *Server) serviceMapRollup(ctx context.Context, tr timeRange) (services, internal, external *Result, ok bool) {
	rt := s.rollupRuntime()
	if rt == nil || tr.To.Sub(tr.From) < mapRollupMin {
		return nil, nil, nil, false
	}
	reqSegs, ok1 := rt.segments("requests", tr)
	edgeSegs, ok2 := rt.segments("edges", tr)
	if !ok1 || !ok2 {
		return nil, nil, nil, false
	}
	hist := make([]string, 0, len(rollupEdges)+1)
	for i := range len(rollupEdges) + 1 {
		hist = append(hist, "SUM("+histCol(i)+") AS "+histCol(i))
	}
	hs := strings.Join(hist, ", ")
	run := func(sql string, keys int) (*Result, error) {
		res, err := s.telemetryQuery(ctx, sql, tr)
		if err != nil {
			return nil, err
		}
		return withP95(res, keys), nil
	}
	var err error
	if services, err = run(`SELECT service, SUM(requests) AS requests, SUM(errors_5xx) AS errors, MIN(min_ms) AS lo, MAX(max_ms) AS hi, `+hs+`
FROM `+unionSQL(requestsTable(s.apdex(), nil), reqSegs, "rr")+`
GROUP BY service`, 3); err == nil {
		edges := unionSQL(edgesTable(nil), edgeSegs, "re")
		if internal, err = run(`SELECT src, dst, SUM(calls) AS calls, SUM(errors) AS errors, MIN(min_ms) AS lo, MAX(max_ms) AS hi, `+hs+`
FROM `+edges+`
WHERE kind = 'service'
GROUP BY src, dst
ORDER BY calls DESC
LIMIT 200`, 4); err == nil {
			external, err = run(`SELECT src, kind, dst, SUM(calls) AS calls, SUM(errors) AS errors, MIN(min_ms) AS lo, MAX(max_ms) AS hi, `+hs+`
FROM `+edges+`
WHERE kind <> 'service'
GROUP BY src, kind, dst
ORDER BY calls DESC
LIMIT 200`, 5)
		}
	}
	if err != nil {
		s.logger.Warn("service map rollup failed; reading raw rows", "err", err)
		return nil, nil, nil, false
	}
	return services, internal, external, true
}

// withP95 replaces lo, hi and the histogram after the first n columns with p95
func withP95(res *Result, n int) *Result {
	out := &Result{Columns: append(append([]string{}, res.Columns[:n]...), "p95"), Rows: make([][]any, 0, len(res.Rows)), SQL: res.SQL, From: res.From, To: res.To}
	for _, row := range res.Rows {
		h := make([]float64, 0, len(row)-n-2)
		for _, v := range row[n+2:] {
			h = append(h, toFloat(v))
		}
		out.Rows = append(out.Rows, append(append([]any{}, row[:n]...), histQuantile(h, toFloat(row[n]), toFloat(row[n+1]), 0.95)))
	}
	return out
}
