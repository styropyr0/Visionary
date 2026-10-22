package analytics

import (
	"net/http"
	"slices"
	"strconv"

	"golang.org/x/sync/errgroup"
)

// spanBands are the latency distribution bands of one call, in ms (upper
// bounds; the last band is open). Calls are much faster than requests, so the
// bands start lower than latencyBuckets.
var spanBands = []int{1, 5, 10, 25, 50, 100, 250, 500, 1000}

func spanBand() string {
	s := "CASE"
	for i, le := range spanBands {
		s += " WHEN duration_ms < " + strconv.Itoa(le) + " THEN " + strconv.Itoa(i)
	}
	return s + " ELSE " + strconv.Itoa(len(spanBands)) + " END"
}

// handleSpanDetail is everything about one span name (a statement like
// "SELECT users", or an outbound host) in the range: how it behaves over time,
// which statements or URLs it covers, the routes that make it and how often
// per request, its slowest calls and its errors.
func (s *Server) handleSpanDetail(w http.ResponseWriter, r *http.Request) {
	tr, ok := s.rangeOf(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	kind, name := q.Get("kind"), q.Get("name")
	switch kind {
	case "db", "http", "cache", "queue", "custom", "graphql", "background":
	default:
		writeError(w, http.StatusBadRequest, "kind must be db, http, cache, queue, custom, graphql or background")
		return
	}
	if name == "" || len(name) > 300 {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	where := `created_at >= {{from}} AND created_at < {{to}} AND kind = ? AND name = ?`
	args := []any{kind, name}
	// One statement (or URL) of the name, as the query catalogue lists them.
	if detail := q.Get("detail"); detail != "" {
		where += ` AND COALESCE(detail, '') = ?`
		args = append(args, detail)
	}
	pct := func(p string) string { return "MIN(CASE WHEN rn >= " + p + " * n THEN duration_ms END)" }
	summary := `
WITH sp AS (
  SELECT duration_ms, status, request_id, row_count,
    ROW_NUMBER() OVER (ORDER BY duration_ms) AS rn,
    COUNT(*) OVER () AS n
  FROM spans WHERE ` + where + `
)
SELECT
  COUNT(*) AS calls,
  ` + s.q.round("SUM(duration_ms)", 1) + ` AS total_ms,
  ` + s.q.round("AVG(duration_ms)", 2) + ` AS avg_ms,
  ` + pct("0.50") + ` AS p50,
  ` + pct("0.95") + ` AS p95,
  ` + pct("0.99") + ` AS p99,
  MAX(duration_ms) AS max_ms,
  ` + countIf("status = 'error'") + ` AS errors,
  COUNT(DISTINCT request_id) AS requests,
  ` + countIf("request_id IS NULL") + ` AS background,
  ` + s.q.round("AVG(row_count)", 1) + ` AS avg_rows,
  MAX(row_count) AS max_rows
FROM sp`

	var cur, prev, timeline, bands, variants, routes, slowest, errs *Result
	g, ctx := errgroup.WithContext(r.Context())
	g.Go(func() (err error) { cur, err = s.telemetryQuery(ctx, summary, tr, args...); return })
	g.Go(func() (err error) { prev, err = s.telemetryQuery(ctx, summary, tr.previous(), args...); return })
	g.Go(func() (err error) {
		timeline, err = s.telemetryQuery(ctx, `
WITH b AS (
  SELECT {{t}} AS t, duration_ms, status FROM spans WHERE `+where+`
),
r AS (
  SELECT t, duration_ms, status,
    ROW_NUMBER() OVER (PARTITION BY t ORDER BY duration_ms) AS rn,
    COUNT(*) OVER (PARTITION BY t) AS n
  FROM b
)
SELECT t, COUNT(*) AS calls, `+s.q.round("AVG(duration_ms)", 2)+` AS avg_ms, `+pct("0.95")+` AS p95,
  `+countIf("status = 'error'")+` AS errors
FROM r GROUP BY t ORDER BY t`, tr, args...)
		return
	})
	g.Go(func() (err error) {
		bands, err = s.telemetryQuery(ctx, `
SELECT `+spanBand()+` AS band, COUNT(*) AS calls
FROM spans WHERE `+where+`
GROUP BY 1 ORDER BY 1`, tr, args...)
		return
	})
	// Statements (db) or URLs (http) under the name: "SELECT users" covers
	// every statement whose first keyword and table match.
	g.Go(func() (err error) {
		variants, err = s.telemetryQuery(ctx, `
WITH sp AS (
  SELECT COALESCE(detail, '') AS detail, target, duration_ms, status, row_count,
    ROW_NUMBER() OVER (PARTITION BY COALESCE(detail, '') ORDER BY duration_ms) AS rn,
    COUNT(*) OVER (PARTITION BY COALESCE(detail, '')) AS n
  FROM spans WHERE `+where+`
)
SELECT detail, MAX(target) AS target, COUNT(*) AS calls,
  `+s.q.round("SUM(duration_ms)", 1)+` AS total_ms,
  `+s.q.round("AVG(duration_ms)", 2)+` AS avg_ms,
  `+pct("0.95")+` AS p95,
  MAX(duration_ms) AS max_ms,
  `+countIf("status = 'error'")+` AS errors,
  `+s.q.round("AVG(row_count)", 1)+` AS avg_rows
FROM sp GROUP BY detail ORDER BY total_ms DESC LIMIT 20`, tr, args...)
		return
	})
	// Routes making the call, and how many times per request: one call per
	// request is normal, ten is a loop.
	g.Go(func() (err error) {
		routes, err = s.telemetryQuery(ctx, `
WITH per AS (
  SELECT request_id, COUNT(*) AS calls, SUM(duration_ms) AS ms
  FROM spans WHERE `+where+` AND request_id IS NOT NULL
  GROUP BY request_id
)
SELECT a.method, COALESCE(a.route, a.path) AS route,
  COUNT(*) AS requests,
  SUM(per.calls) AS calls,
  `+s.q.round("AVG(per.calls)", 1)+` AS calls_per_request,
  MAX(per.calls) AS max_per_request,
  `+s.q.round("SUM(per.ms)", 1)+` AS total_ms,
  `+s.q.round("100.0 * SUM(per.ms) / NULLIF(SUM(a.duration_ms), 0)", 1)+` AS time_share
FROM per JOIN api_logs a ON a.request_id = per.request_id
WHERE a.created_at >= ? AND a.created_at < {{to}}
GROUP BY a.method, COALESCE(a.route, a.path)
ORDER BY total_ms DESC LIMIT 20`, tr, append(slices.Clone(args), tr.requestsFrom())...)
		return
	})
	g.Go(func() (err error) {
		slowest, err = s.telemetryQuery(ctx, `
SELECT created_at, duration_ms, status, error, row_count, offset_ms, request_id, detail
FROM spans WHERE `+where+`
ORDER BY duration_ms DESC LIMIT 20`, tr, args...)
		return
	})
	g.Go(func() (err error) {
		errs, err = s.telemetryQuery(ctx, `
SELECT COALESCE(error, '') AS error, COUNT(*) AS calls, MAX(created_at) AS last_seen
FROM spans WHERE `+where+` AND status = 'error'
GROUP BY COALESCE(error, '') ORDER BY calls DESC LIMIT 10`, tr, args...)
		return
	})
	if err := g.Wait(); err != nil {
		s.fail(w, err)
		return
	}
	s.addSlowestRoutes(r, slowest)
	writeJSON(w, http.StatusOK, map[string]any{
		"kind": kind, "name": name,
		"summary": cur, "previous": prev, "timeline": timeline, "bands": bandLabels(bands),
		"variants": variants, "routes": routes, "slowest": slowest, "errors": errs,
	})
}

// addSlowestRoutes labels each slow call with its request's method and route.
// Looked up per request id rather than joined, so the span scan stays on its
// own index.
func (s *Server) addSlowestRoutes(r *http.Request, res *Result) {
	ri := indexOf(res.Columns, "request_id")
	ids := []any{}
	for _, row := range res.Rows {
		if row[ri] != nil {
			ids = append(ids, row[ri])
		}
	}
	routeOf := map[string]string{}
	if len(ids) > 0 {
		marks := "?"
		for range ids[1:] {
			marks += ", ?"
		}
		if lr, err := s.telemetryQuery(r.Context(), `SELECT request_id, method, COALESCE(route, path) AS route FROM api_logs WHERE request_id IN (`+marks+`)`, timeRange{}, ids...); err == nil {
			for _, m := range rowMaps(lr) {
				routeOf[toString(m["request_id"])] = toString(m["method"]) + " " + toString(m["route"])
			}
		}
	}
	res.Columns = append(res.Columns, "route")
	for i, row := range res.Rows {
		res.Rows[i] = append(row, routeOf[toString(row[ri])])
	}
}

// bandLabels names the band numbers ("<1 ms", "1–5 ms", ..., "≥1 s") and fills
// in empty bands so the distribution has no holes.
func bandLabels(res *Result) *Result {
	counts := map[int64]any{}
	for _, row := range res.Rows {
		counts[toInt(row[0])] = row[1]
	}
	label := func(ms int) string {
		if ms >= 1000 {
			return strconv.Itoa(ms/1000) + " s"
		}
		return strconv.Itoa(ms) + " ms"
	}
	out := &Result{Columns: []string{"band", "calls"}, SQL: res.SQL, From: res.From, To: res.To}
	for i := 0; i <= len(spanBands); i++ {
		var name string
		switch {
		case i == 0:
			name = "<" + label(spanBands[0])
		case i == len(spanBands):
			name = "≥" + label(spanBands[i-1])
		default:
			name = label(spanBands[i-1]) + "–" + label(spanBands[i])
		}
		n, ok := counts[int64(i)]
		if !ok {
			n = 0
		}
		out.Rows = append(out.Rows, []any{name, n})
	}
	return out
}

func indexOf(cols []string, name string) int {
	for i, c := range cols {
		if c == name {
			return i
		}
	}
	return -1
}
