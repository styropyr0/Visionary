package analytics

import (
	"net/http"
	"strconv"
	"sync"
)

// Performance panels: request-level latency analysis over api_logs (RED
// metrics per route, latency distribution, Apdex and SLO), plus saturation
// signals joined from system_metrics.
//
// Apdex with threshold T: a request is satisfied when it succeeds within T,
// tolerating within 4T, and frustrated when slower or a server error. A
// request is "good" for the SLO when it succeeds within T.

// latencyBuckets are the heatmap and histogram bands, in ms (upper bounds;
// the last band is open).
var latencyBuckets = []int{25, 50, 100, 250, 500, 1000, 2500, 5000, 10000}

func latencyBand() string {
	s := "CASE"
	for i, le := range latencyBuckets {
		s += " WHEN duration_ms < " + strconv.Itoa(le) + " THEN " + strconv.Itoa(i)
	}
	return s + " ELSE " + strconv.Itoa(len(latencyBuckets)) + " END"
}

// percentiles reads p50..p99 from rows ranked as rn of n within a group.
const percentiles = `
  MIN(CASE WHEN rn >= 0.50 * n THEN duration_ms END) AS p50,
  MIN(CASE WHEN rn >= 0.90 * n THEN duration_ms END) AS p90,
  MIN(CASE WHEN rn >= 0.95 * n THEN duration_ms END) AS p95,
  MIN(CASE WHEN rn >= 0.99 * n THEN duration_ms END) AS p99,
  MAX(duration_ms) AS max_ms`

func performancePanels(q sqlDialect, apdexMs int) map[string]string {
	t := strconv.Itoa(apdexMs)
	t4 := strconv.Itoa(4 * apdexMs)
	apdex := q.round("SUM(CASE WHEN status < 500 AND duration_ms <= "+t+" THEN 1.0 WHEN status < 500 AND duration_ms <= "+t4+" THEN 0.5 ELSE 0 END) / NULLIF(COUNT(*), 0)", 3)
	good := q.round("100.0 * "+countIf("status < 500 AND duration_ms <= "+t)+" / NULLIF(COUNT(*), 0)", 2)
	errRate := q.round("100.0 * "+countIf("status >= 500")+" / NULLIF(COUNT(*), 0)", 2)

	return map[string]string{
		// One pass: totals, Apdex, SLO and percentiles for the whole window.
		"summary": `
WITH o AS (
  SELECT status, duration_ms,
    ROW_NUMBER() OVER (ORDER BY duration_ms) AS rn,
    COUNT(*) OVER () AS n
  FROM api_logs WHERE ` + window + `
)
SELECT
  COUNT(*) AS requests,
  ` + apdex + ` AS apdex,
  ` + good + ` AS good_percent,
  ` + errRate + ` AS error_rate,
  COALESCE(` + countIf("duration_ms > "+t4) + `, 0) AS frustrated,
  ` + q.round("SUM(duration_ms) / 1000.0", 1) + ` AS busy_seconds,` + percentiles + `
FROM o`,

		"percentiles": `
WITH b AS (
  SELECT {{t}} AS t, duration_ms FROM api_logs WHERE ` + window + `
),
r AS (
  SELECT t, duration_ms,
    ROW_NUMBER() OVER (PARTITION BY t ORDER BY duration_ms) AS rn,
    COUNT(*) OVER (PARTITION BY t) AS n
  FROM b
)
SELECT t,` + percentiles + `
FROM r GROUP BY t ORDER BY t`,

		"apdex": `
SELECT {{t}} AS t,
  ` + apdex + ` AS apdex,
  ` + good + ` AS good_percent
FROM api_logs WHERE ` + window + `
GROUP BY 1 ORDER BY 1`,

		// Requests per time bucket and latency band, for the heatmap.
		"heatmap": `
SELECT {{t}} AS t, ` + latencyBand() + ` AS band, COUNT(*) AS requests
FROM api_logs WHERE ` + window + `
GROUP BY 1, 2 ORDER BY 1, 2`,

		"histogram": `
SELECT ` + latencyBand() + ` AS band, COUNT(*) AS requests
FROM api_logs WHERE ` + window + `
GROUP BY 1 ORDER BY 1`,

		// RED per route, with each route's share of all server time: the
		// routes worth optimizing first.
		"routes": `
WITH r AS (
  SELECT COALESCE(route, path) AS route, method, status, duration_ms, response_size
  FROM api_logs WHERE ` + window + `
),
ranked AS (
  SELECT route, method, status, duration_ms, response_size,
    ROW_NUMBER() OVER (PARTITION BY route, method ORDER BY duration_ms) AS rn,
    COUNT(*) OVER (PARTITION BY route, method) AS n
  FROM r
)
SELECT
  method,
  route,
  COUNT(*) AS requests,` + percentiles + `,
  ` + errRate + ` AS error_rate,
  ` + apdex + ` AS apdex,
  ` + q.round("100.0 * SUM(duration_ms) / NULLIF(SUM(SUM(duration_ms)) OVER (), 0)", 1) + ` AS time_share,
  ` + q.round("SUM(duration_ms) / 1000.0", 1) + ` AS busy_seconds,
  ` + q.round("AVG(response_size)", 0) + ` AS avg_response_bytes
FROM ranked
GROUP BY route, method
ORDER BY busy_seconds DESC
LIMIT 50`,

		// p95 over time for the five busiest routes, in long format. Routes are
		// ranked with window functions in one pass: a join against a top-5 CTE
		// makes SQLite re-run the CTE for every row.
		"route_p95": `
WITH b AS (
  SELECT {{t}} AS t, COALESCE(route, path) AS route_name, duration_ms,
    COUNT(*) OVER (PARTITION BY COALESCE(route, path)) AS route_total
  FROM api_logs WHERE ` + window + `
),
top AS (
  SELECT t, route_name, duration_ms,
    DENSE_RANK() OVER (ORDER BY route_total DESC, route_name) AS route_rank
  FROM b
),
r AS (
  SELECT t, route_name, duration_ms,
    ROW_NUMBER() OVER (PARTITION BY t, route_name ORDER BY duration_ms) AS rn,
    COUNT(*) OVER (PARTITION BY t, route_name) AS n
  FROM top WHERE route_rank <= 5
)
SELECT t, route_name AS series, MIN(CASE WHEN rn >= 0.95 * n THEN duration_ms END) AS value
FROM r GROUP BY t, route_name ORDER BY t`,

		"slowest": `
SELECT id, created_at, method, COALESCE(route, path) AS route, path, status, duration_ms,
  user_handle, user_id, response_size
FROM api_logs WHERE ` + window + `
ORDER BY duration_ms DESC
LIMIT 25`,

		// Users who waited longest: frustrated requests (slower than 4T or 5xx).
		"users": `
SELECT
  user_id,
  COALESCE(MAX(user_handle), '') AS handle,
  COUNT(*) AS requests,
  ` + countIf("duration_ms > "+t4+" OR status >= 500") + ` AS frustrated,
  ` + apdex + ` AS apdex,
  MAX(duration_ms) AS slowest_ms
FROM api_logs
WHERE ` + window + ` AND user_id IS NOT NULL
GROUP BY user_id
HAVING ` + countIf("duration_ms > "+t4+" OR status >= 500") + ` > 0
ORDER BY frustrated DESC
LIMIT 20`,

		// Latency next to load and saturation, per bucket: does latency rise
		// with traffic, in-flight work, pool waits or CPU?
		"saturation": `
WITH b AS (
  SELECT {{t}} AS t, duration_ms FROM api_logs WHERE ` + window + `
),
r AS (
  SELECT t, duration_ms,
    ROW_NUMBER() OVER (PARTITION BY t ORDER BY duration_ms) AS rn,
    COUNT(*) OVER (PARTITION BY t) AS n
  FROM b
),
a AS (
  SELECT t, COUNT(*) AS requests, MIN(CASE WHEN rn >= 0.95 * n THEN duration_ms END) AS p95
  FROM r GROUP BY t
),
s AS (
  SELECT {{t}} AS t,
    MAX(http_in_flight) AS in_flight,
    ` + q.round("SUM(db_acquire_wait_ms)", 1) + ` AS db_wait_ms,
    MAX(db_acquired_conns) AS db_in_use,
    MAX(host_cpu_percent) AS cpu
  FROM system_metrics WHERE ` + window + `
  GROUP BY 1
)
SELECT a.t, a.requests, a.p95, s.in_flight, s.db_wait_ms, s.db_in_use, s.cpu
FROM a LEFT JOIN s ON s.t = a.t
ORDER BY a.t`,
	}
}

// handlePerformance serves the performance panels. summary and routes come
// with the previous period of the same length, for deltas and regressions.
func (s *Server) handlePerformance(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("panel")
	sql, ok := s.performance[name]
	if !ok {
		writeError(w, http.StatusNotFound, "unknown panel")
		return
	}
	tr, err := parseRange(r.URL.Query(), s.cfg.MaxRangeDays)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if name != "summary" && name != "routes" {
		res, err := s.runTemplate(r.Context(), sql, tr, true)
		if err != nil {
			if name == "saturation" && missingTable(err, "system_metrics") {
				writeError(w, http.StatusNotFound, errNoSystemMetrics)
				return
			}
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
		return
	}

	var (
		wg              sync.WaitGroup
		cur, prev       *Result
		curErr, prevErr error
	)
	wg.Add(2)
	go func() { defer wg.Done(); cur, curErr = s.runTemplate(r.Context(), sql, tr, true) }()
	go func() { defer wg.Done(); prev, prevErr = s.runTemplate(r.Context(), sql, tr.previous(), true) }()
	wg.Wait()
	if curErr != nil {
		s.fail(w, curErr)
		return
	}
	if prevErr != nil {
		s.logger.Warn("previous-period performance failed", "panel", name, "err", prevErr)
	}
	writeJSON(w, http.StatusOK, map[string]any{"current": cur, "previous": prev})
}
