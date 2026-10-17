package analytics

import (
	"net/http"
	"strconv"
	"strings"
)

// Pre-aggregated rollups (rollup_requests, rollup_system) for long ranges:
// the tables and their description for the analyst. The job is in
// rollups_job.go, query routing in rollups_route.go.

func (s *Server) rollupsSchema() []string {
	return []string{
		s.q.rollupDDL(requestsTable(s.cfg.ApdexMs, nil)),
		s.q.rollupDDL(systemTable()),
		s.q.rollupStateDDL(),
	}
}

func (s *Server) rollupsRoutes(mux *http.ServeMux) {}

func (s *Server) rollupsPrompt() string {
	var edges []string
	for _, e := range rollupEdges {
		edges = append(edges, strconv.FormatFloat(e, 'f', -1, 64))
	}
	var metrics []string
	for _, m := range sysMetrics {
		var aggs []string
		if m.aggs&sysAvg != 0 {
			aggs = append(aggs, "_avg/_n")
		}
		if m.aggs&sysMax != 0 {
			aggs = append(aggs, "_max")
		}
		if m.aggs&sysSum != 0 {
			aggs = append(aggs, "_sum")
		}
		name := m.name
		if m.expr != "" {
			name += " (= " + m.expr + ")"
		}
		metrics = append(metrics, name+" "+strings.Join(aggs, " "))
	}
	last := strconv.Itoa(len(rollupEdges))
	return `Rollups: pre-aggregated copies of api_logs and system_metrics, much cheaper for ranges longer than a day or two. created_at is the bucket start (same text format as the raw tables, so {{from}}, {{to}} and {{t}} work when the bucket is a multiple of bucket_s); bucket_s is 60 (1-minute rows, kept ` + strconv.Itoa(getInt("VISIONARY_ROLLUP_MINUTE_DAYS", 14)) + ` days) or 3600 (1-hour rows, kept ` + strconv.Itoa(getInt("VISIONARY_ROLLUP_HOUR_DAYS", 400)) + ` days). ALWAYS filter on one bucket_s, or rows are counted twice. The newest minute or two (and, for 1-hour rows, the current hour) are only in the raw tables.

rollup_requests: one row per bucket_s, created_at, service ('' when unset), route (COALESCE(route, path) for the ~200 busiest, else 'other'), method, status, error_key (COALESCE(error_key, error_code, '—') when status >= 400, '' otherwise). Measures: requests, errors_5xx, errors_4xx, sum_ms, min_ms, max_ms (duration_ms), apdex_satisfied (status < 500 and duration_ms <= apdex_ms), apdex_tolerating (status < 500 and apdex_ms < duration_ms <= 4 * apdex_ms), slow_requests (duration_ms > 4 * apdex_ms), apdex_ms, sum_response_bytes, db_requests (requests that reported db_ms), sum_db_ms, sum_db_count, last_at (latest raw created_at), and a latency histogram h00..h` + last + `: h00 counts duration_ms < ` + edges[0] + `, hNN counts edge[NN-1] <= duration_ms < edge[NN], h` + last + ` is open-ended; the edges in ms are ` + strings.Join(edges, ", ") + `.
- Totals and rates: SUM(requests); 100.0 * SUM(errors_5xx) / SUM(requests); mean latency SUM(sum_ms) / SUM(requests). Apdex: (SUM(apdex_satisfied) + 0.5 * SUM(apdex_tolerating)) / SUM(requests).
- Percentiles come from the histogram: sum each hNN over the group, then the pth percentile lies in the first bucket where the running total reaches p * total; interpolate linearly between that bucket's edges, clamped to MIN(min_ms) and MAX(max_ms). In SQL, unpivot the buckets with UNION ALL (SELECT 0 AS b, SUM(h00) AS c FROM ... UNION ALL SELECT 1, SUM(h01) FROM ... and so on), take SUM(c) OVER (ORDER BY b) as the running total, and pick the smallest b whose running total >= p * total; report that bucket's upper edge, or interpolate. Accurate to a few percent.
- Distinct users and individual requests are not in the rollups: read api_logs for those.

rollup_system: one row per bucket_s, created_at, instance, with samples (raw rows), last_at, and per metric <name>_avg with <name>_n (non-null samples; recombine as SUM(x_avg * x_n) / SUM(x_n)), <name>_max and <name>_sum. Metrics: ` + strings.Join(metrics, "; ") + `.`
}
