package analytics

import (
	"math"
	"net/http"
	"strconv"
	"strings"

	"golang.org/x/sync/errgroup"
)

// handleRouteDetail is everything about one route in the range: its totals
// against the previous period, latency and traffic over time, the status
// codes it answers, and example requests to show call by call.
func (s *Server) handleRouteDetail(w http.ResponseWriter, r *http.Request) {
	tr, ok := s.rangeOf(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	method, route := strings.ToUpper(strings.TrimSpace(q.Get("method"))), q.Get("route")
	if !httpMethods[method] || route == "" || len(route) > 500 {
		writeError(w, http.StatusBadRequest, "method and route are required")
		return
	}
	where := window + ` AND method = ? AND COALESCE(route, path) = ?`
	args := []any{method, route}
	rule := s.apdex()
	t, t4 := rule.sql(), rule.sql4()
	pct := func(p string) string { return "MIN(CASE WHEN rn >= " + p + " * n THEN duration_ms END)" }
	share := func(col string) string {
		return s.q.round("100.0 * SUM("+col+") / NULLIF(SUM(CASE WHEN "+col+" IS NOT NULL THEN duration_ms END), 0)", 1)
	}
	summary := `
WITH o AS (
  SELECT method, route, path, status, duration_ms, response_size, db_ms, db_count, http_ms,
    ROW_NUMBER() OVER (ORDER BY duration_ms) AS rn,
    COUNT(*) OVER () AS n
  FROM api_logs WHERE ` + where + `
)
SELECT
  COUNT(*) AS requests,` + percentiles + `,
  ` + s.q.round("100.0 * "+countIf("status >= 500")+" / NULLIF(COUNT(*), 0)", 2) + ` AS error_rate,
  ` + s.q.round("100.0 * "+countIf("status >= 400 AND status < 500")+" / NULLIF(COUNT(*), 0)", 2) + ` AS client_error_rate,
  ` + s.q.round("SUM(CASE WHEN status < 500 AND duration_ms <= "+t+" THEN 1.0 WHEN status < 500 AND duration_ms <= "+t4+" THEN 0.5 ELSE 0 END) / NULLIF(COUNT(*), 0)", 3) + ` AS apdex,
  ` + s.q.round("SUM(duration_ms)", 1) + ` AS busy_ms,
  ` + s.q.round("AVG(response_size)", 0) + ` AS avg_response_bytes,
  ` + share("db_ms") + ` AS db_share,
  ` + s.q.round("AVG(db_count)", 1) + ` AS db_calls,
  ` + share("http_ms") + ` AS http_share
FROM o`

	var cur, prev, timeline, statuses, recorded *Result
	g, ctx := errgroup.WithContext(r.Context())
	g.Go(func() (err error) { cur, err = s.telemetryQuery(ctx, summary, tr, args...); return })
	g.Go(func() (err error) { prev, err = s.telemetryQuery(ctx, summary, tr.previous(), args...); return })
	g.Go(func() (err error) {
		timeline, err = s.telemetryQuery(ctx, `
WITH b AS (
  SELECT {{t}} AS t, duration_ms, status FROM api_logs WHERE `+where+`
),
r AS (
  SELECT t, duration_ms, status,
    ROW_NUMBER() OVER (PARTITION BY t ORDER BY duration_ms) AS rn,
    COUNT(*) OVER (PARTITION BY t) AS n
  FROM b
)
SELECT t, COUNT(*) AS requests, `+pct("0.50")+` AS p50, `+pct("0.95")+` AS p95,
  `+countIf("status >= 500")+` AS errors
FROM r GROUP BY t ORDER BY t`, tr, args...)
		return
	})
	g.Go(func() (err error) {
		statuses, err = s.telemetryQuery(ctx, `
SELECT status, COUNT(*) AS requests, `+s.q.round("AVG(duration_ms)", 1)+` AS avg_ms
FROM api_logs WHERE `+where+`
GROUP BY status ORDER BY requests DESC LIMIT 12`, tr, args...)
		return
	})
	// Requests with recorded calls, to show one as a waterfall.
	g.Go(func() (err error) {
		recorded, err = s.telemetryQuery(ctx, `
SELECT id, request_id, created_at, path, status, duration_ms, db_count, db_ms, http_count, http_ms
FROM api_logs a WHERE `+where+` AND request_id IS NOT NULL
  AND EXISTS (SELECT 1 FROM spans sp WHERE sp.request_id = a.request_id)
ORDER BY duration_ms DESC LIMIT `+strconv.Itoa(maxRouteExamples), tr, args...)
		return
	})
	if err := g.Wait(); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"method": method, "route": route,
		"summary": cur, "previous": prev, "timeline": timeline, "statuses": statuses,
		"examples": routeExamples(cur, recorded),
	})
}

// maxRouteExamples bounds the requests read to pick examples from; they are
// the slowest, which is where recorded calls are anyway.
const maxRouteExamples = 500

// routeExample is one request of a route to show call by call.
type routeExample struct {
	Label  string         `json:"label"` // typical, slow or slowest
	Target float64        `json:"target_ms"`
	Log    map[string]any `json:"log"`
}

// routeExamples picks the recorded request closest to the route's p50, p95
// and slowest time. SDKs record calls for slow and failed requests only, so
// a typical example may be slower than the p50 itself; the target says so.
func routeExamples(summary, recorded *Result) []routeExample {
	out := []routeExample{}
	if summary == nil || len(summary.Rows) == 0 || recorded == nil || len(recorded.Rows) == 0 {
		return out
	}
	sum := rowMaps(summary)[0]
	rows := rowMaps(recorded)
	seen := map[string]bool{}
	for _, t := range []struct{ label, col string }{{"slowest", "max_ms"}, {"slow", "p95"}, {"typical", "p50"}} {
		target := toFloat(sum[t.col])
		best := -1
		for i, r := range rows {
			if best < 0 || math.Abs(toFloat(r["duration_ms"])-target) < math.Abs(toFloat(rows[best]["duration_ms"])-target) {
				best = i
			}
		}
		id := toString(rows[best]["request_id"])
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, routeExample{Label: t.label, Target: target, Log: rows[best]})
	}
	return out
}
