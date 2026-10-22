package analytics

import (
	"regexp"
	"sort"
	"strings"
	"time"
)

// Rollup variants of the fixed panels. Each returns the same columns as the
// raw query; percentiles are estimated from the latency histogram after the
// query (see histQuantile), so the SQL selects the histogram instead.

const (
	needRequests = 1 << iota
	needSystem
)

// Ranges at least this long read rollups (when they cover the range).
const (
	requestsRollupMin = 48 * time.Hour
	systemRollupMin   = 24 * time.Hour
)

type rollupPanel struct {
	sql   string // reads {{requests}} and/or {{system}}
	needs int
	min   time.Duration
	// out lists the final columns; names in pct are percentiles computed from
	// lo_ms, hi_ms and h00.., the rest are copied from the query's columns.
	out []string
	pct map[string]float64
	// bands turns histogram rows into (…, band, requests) rows, for the
	// heatmap and histogram panels.
	bands bool
	// slowest marks the slowest-requests panel: it stays raw, narrowed to the
	// buckets whose slowest request ranks in the top 25.
	slowest bool
}

// histSelect is the histogram of a group, for histQuantile.
func histSelect() string {
	parts := []string{"MIN(min_ms) AS lo_ms", "MAX(max_ms) AS hi_ms"}
	for _, c := range histCols() {
		parts = append(parts, "SUM("+c+") AS "+c)
	}
	return strings.Join(parts, ", ")
}

func requestPanel(sql string, out ...string) *rollupPanel {
	return &rollupPanel{sql: sql, needs: needRequests, min: requestsRollupMin, out: out}
}

func (p *rollupPanel) quantiles(pct map[string]float64) *rollupPanel {
	p.pct = pct
	return p
}

var p50p95p99 = map[string]float64{"p50": 0.5, "p95": 0.95, "p99": 0.99}
var p50to99 = map[string]float64{"p50": 0.5, "p90": 0.9, "p95": 0.95, "p99": 0.99}

// rollupPanels maps each raw panel query to its rollup variant. Panels that
// need individual rows (distinct users, single requests) have none.
func rollupPanels(q sqlDialect, rule apdexRule) map[string]*rollupPanel {
	ov := overviewPanels(q)
	pf := performancePanels(q, rule)
	sy := systemPanels(q)

	rate := func(col string) string {
		return q.round("100.0 * SUM("+col+") / NULLIF(SUM(requests), 0)", 2)
	}
	apdex := q.round("(SUM(apdex_satisfied) + 0.5 * SUM(apdex_tolerating)) / NULLIF(SUM(requests), 0)", 3)
	good := q.round("100.0 * SUM(apdex_satisfied) / NULLIF(SUM(requests), 0)", 2)
	busy := q.round("SUM(sum_ms) / 1000.0", 1)
	h := histSelect()

	m := map[string]*rollupPanel{
		ov["stats"]: requestPanel(`
SELECT
  COALESCE(SUM(requests), 0) AS requests,
  COALESCE(SUM(errors_5xx), 0) AS server_errors,
  COALESCE(SUM(errors_4xx), 0) AS client_errors,
  (SELECT COUNT(DISTINCT user_id) FROM api_logs WHERE `+window+`) AS users,
  `+h+`
FROM {{requests}}`, "requests", "server_errors", "client_errors", "users", "p50", "p95", "p99").quantiles(p50p95p99),

		ov["traffic"]: requestPanel(`
SELECT
  {{t}} AS t,
  CASE
    WHEN status >= 500 THEN '5xx'
    WHEN status >= 400 THEN '4xx'
    WHEN status >= 300 THEN '3xx'
    ELSE '2xx'
  END AS series,
  SUM(requests) AS value
FROM {{requests}}
GROUP BY 1, 2
ORDER BY 1`, "t", "series", "value"),

		ov["latency"]: requestPanel(`
SELECT {{t}} AS t, `+h+`
FROM {{requests}}
GROUP BY 1
ORDER BY 1`, "t", "p50", "p95", "p99").quantiles(p50p95p99),

		ov["error_rate"]: requestPanel(`
SELECT
  {{t}} AS t,
  `+rate("errors_5xx")+` AS server_errors,
  `+rate("errors_4xx")+` AS client_errors
FROM {{requests}}
GROUP BY 1
ORDER BY 1`, "t", "server_errors", "client_errors"),

		ov["status_codes"]: requestPanel(`
SELECT `+q.text("rr.status")+` AS status, SUM(rr.requests) AS requests
FROM {{requests}}
GROUP BY rr.status
ORDER BY 2 DESC
LIMIT 12`, "status", "requests"),

		ov["routes"]: requestPanel(`
SELECT
  method,
  route,
  SUM(requests) AS requests,
  `+rate("errors_5xx")+` AS error_rate,
  `+rate("errors_4xx")+` AS client_error_rate,
  `+q.round("SUM(sum_ms) / NULLIF(SUM(requests), 0)", 1)+` AS avg_ms,
  `+h+`
FROM {{requests}}
GROUP BY route, method
ORDER BY 3 DESC
LIMIT 50`, "method", "route", "requests", "error_rate", "client_error_rate", "avg_ms", "p50_ms", "p95_ms").
			quantiles(map[string]float64{"p50_ms": 0.5, "p95_ms": 0.95}),

		ov["errors"]: requestPanel(`
SELECT
  status,
  error_key AS error,
  route,
  SUM(requests) AS occurrences,
  MAX(last_at) AS last_seen
FROM {{requests}}
WHERE status >= 400
GROUP BY 1, 2, 3
ORDER BY 4 DESC
LIMIT 25`, "status", "error", "route", "occurrences", "last_seen"),

		pf["summary"]: requestPanel(`
SELECT
  COALESCE(SUM(requests), 0) AS requests,
  `+apdex+` AS apdex,
  `+good+` AS good_percent,
  `+rate("errors_5xx")+` AS error_rate,
  COALESCE(SUM(slow_requests), 0) AS frustrated,
  `+busy+` AS busy_seconds,
  MAX(max_ms) AS max_ms,
  `+h+`
FROM {{requests}}`, "requests", "apdex", "good_percent", "error_rate", "frustrated", "busy_seconds", "p50", "p90", "p95", "p99", "max_ms").
			quantiles(p50to99),

		pf["percentiles"]: requestPanel(`
SELECT {{t}} AS t, MAX(max_ms) AS max_ms, `+h+`
FROM {{requests}}
GROUP BY 1 ORDER BY 1`, "t", "p50", "p90", "p95", "p99", "max_ms").quantiles(p50to99),

		pf["apdex"]: requestPanel(`
SELECT {{t}} AS t,
  `+apdex+` AS apdex,
  `+good+` AS good_percent
FROM {{requests}}
GROUP BY 1 ORDER BY 1`, "t", "apdex", "good_percent"),

		pf["heatmap"]: {sql: `
SELECT {{t}} AS t, ` + h + `
FROM {{requests}}
GROUP BY 1 ORDER BY 1`, needs: needRequests, min: requestsRollupMin, out: []string{"t", "band", "requests"}, bands: true},

		pf["histogram"]: {sql: `
SELECT ` + h + `
FROM {{requests}}`, needs: needRequests, min: requestsRollupMin, out: []string{"band", "requests"}, bands: true},

		pf["routes"]: requestPanel(`
SELECT
  method,
  route,
  SUM(requests) AS requests,
  MAX(max_ms) AS max_ms,
  `+rate("errors_5xx")+` AS error_rate,
  `+apdex+` AS apdex,
  `+q.round("100.0 * SUM(sum_ms) / NULLIF(SUM(SUM(sum_ms)) OVER (), 0)", 1)+` AS time_share,
  `+busy+` AS busy_seconds,
  `+q.round("1.0 * SUM(sum_response_bytes) / NULLIF(SUM(requests), 0)", 0)+` AS avg_response_bytes,
  `+h+`
FROM {{requests}}
GROUP BY route, method
ORDER BY busy_seconds DESC
LIMIT 50`, "method", "route", "requests", "p50", "p90", "p95", "p99", "max_ms", "error_rate", "apdex", "time_share", "busy_seconds", "avg_response_bytes").
			quantiles(p50to99),

		// Same ranking as the raw panel: the five routes with the most
		// requests, ties by name.
		pf["route_p95"]: requestPanel(`
WITH g AS (
  SELECT {{t}} AS t, route AS route_name, `+h+`, SUM(requests) AS n
  FROM {{requests}}
  GROUP BY 1, 2
),
totals AS (
  SELECT g.*, SUM(n) OVER (PARTITION BY route_name) AS route_total FROM g
),
ranked AS (
  SELECT totals.*, DENSE_RANK() OVER (ORDER BY route_total DESC, route_name) AS route_rank FROM totals
)
SELECT t, route_name AS series, `+strings.Join(append([]string{"lo_ms", "hi_ms"}, histCols()...), ", ")+`
FROM ranked WHERE route_rank <= 5
ORDER BY t`, "t", "series", "value").quantiles(map[string]float64{"value": 0.95}),

		pf["slowest"]: {sql: pf["slowest"], needs: needRequests, min: requestsRollupMin, slowest: true},

		pf["saturation"]: {sql: `
WITH a AS (
  SELECT {{t}} AS t, SUM(requests) AS requests, ` + h + `
  FROM {{requests}}
  GROUP BY 1
),
s AS (
  SELECT {{t}} AS t,
    MAX(http_in_flight_max) AS in_flight,
    ` + q.round("SUM(db_acquire_wait_ms_sum)", 1) + ` AS db_wait_ms,
    MAX(db_acquired_conns_max) AS db_in_use,
    MAX(host_cpu_percent_max) AS cpu
  FROM {{system}} WHERE ` + window + `
  GROUP BY 1
)
SELECT a.*, s.in_flight, s.db_wait_ms, s.db_in_use, s.cpu
FROM a LEFT JOIN s ON s.t = a.t
ORDER BY a.t`, needs: needRequests | needSystem, min: requestsRollupMin,
			out: []string{"t", "requests", "p95", "in_flight", "db_wait_ms", "db_in_use", "cpu"}, pct: map[string]float64{"p95": 0.95}},

		sy["spikes"]: {sql: `
WITH s AS (
  SELECT {{t}} AS t,
    MAX(host_cpu_percent_max) AS cpu,
    MAX(host_mem_percent_max) AS memory,
    MAX(db_acquired_conns_max) AS db_in_use,
    MAX(http_in_flight_max) AS in_flight,
    MAX(goroutines_max) AS goroutines
  FROM {{system}} WHERE ` + sysWindow + `
  GROUP BY t
),
a AS (
  SELECT {{t}} AS t,
    SUM(requests) AS requests,
    SUM(errors_5xx) AS errors,
    MAX(max_ms) AS slowest_ms
  FROM {{requests}}
  GROUP BY t
)
SELECT s.t, s.cpu, s.memory, s.db_in_use, s.in_flight, s.goroutines,
  COALESCE(a.requests, 0) AS requests, COALESCE(a.errors, 0) AS errors, a.slowest_ms
FROM s LEFT JOIN a ON a.t = s.t
ORDER BY s.cpu DESC
LIMIT 12`, needs: needRequests | needSystem, min: systemRollupMin},
	}

	for name, raw := range sy {
		if name == "spikes" {
			continue
		}
		if sql, ok := rollupSystemSQL(raw); ok {
			m[raw] = &rollupPanel{sql: sql, needs: needSystem, min: systemRollupMin}
		}
	}
	// System panels for all instances arrive with {{instance}} rendered empty.
	for _, raw := range sy {
		if p := m[raw]; p != nil && strings.Contains(raw, "{{instance}}") {
			m[strings.ReplaceAll(raw, "{{instance}}", "")] = p
		}
	}
	return m
}

// rollupSystemSQL rewrites a system panel for rollup_system: AVG, MAX and SUM
// of a metric read the matching aggregate columns, and a metric's IS NOT NULL
// filter becomes one on an aggregate. It reports false when a raw column is
// left over (a panel the rollups cannot answer).
func rollupSystemSQL(raw string) (string, bool) {
	metrics := append([]sysMetric(nil), sysMetrics...)
	// Longer expressions first, so load_1 / NULLIF(cpu_count, 0) is rewritten
	// before load_1.
	sort.SliceStable(metrics, func(i, j int) bool { return len(metrics[i].source()) > len(metrics[j].source()) })
	var pairs []string
	for _, m := range metrics {
		e := m.source()
		first := ""
		if m.aggs&sysAvg != 0 {
			pairs = append(pairs, "AVG("+e+")", "(SUM("+m.name+"_avg * "+m.name+"_n) / NULLIF(SUM("+m.name+"_n), 0))")
			first = m.name + "_avg"
		}
		if m.aggs&sysMax != 0 {
			pairs = append(pairs, "MAX("+e+")", "MAX("+m.name+"_max)")
			if first == "" {
				first = m.name + "_max"
			}
		}
		if m.aggs&sysSum != 0 {
			pairs = append(pairs, "SUM("+e+")", "SUM("+m.name+"_sum)")
			if first == "" {
				first = m.name + "_sum"
			}
		}
		pairs = append(pairs, " "+e+" IS NOT NULL", " "+first+" IS NOT NULL")
	}
	pairs = append(pairs,
		"COUNT(*)", "SUM(samples)",
		"MAX(created_at)", "MAX(last_at)",
		"FROM system_metrics WHERE", "FROM {{system}} WHERE")
	out := strings.NewReplacer(pairs...).Replace(raw)
	// Output names may repeat a column name, and the stats panel reads its
	// latest values from one raw row; neither reads raw aggregates.
	check := latestRef.ReplaceAllString(outputName.ReplaceAllString(out, ""), "")
	return out, !rawSystemColumn.MatchString(check)
}

var outputName = regexp.MustCompile(`\bAS [a-z0-9_]+`)

var latestRef = regexp.MustCompile(`\(SELECT [a-z0-9_]+ FROM latest\)`)

// rawSystemColumn matches a system_metrics column name standing alone (the
// rollup columns carry suffixes, so they do not match).
var rawSystemColumn = func() *regexp.Regexp {
	names := []string{"cpu_count", "interval_s", "uptime_s", "disk_free_gb", "host_mem_total_mb", "heap_alloc_mb", "heap_objects", "gc_count", "gc_cpu_percent", "db_idle_conns", "db_acquires", "disk_total_gb", "host_procs",
		// Not rolled up: the container panels always read raw rows.
		"container_cpu_cores", "container_cpu_limit", "container_cpu_throttled_percent", "container_mem_used_mb", "container_mem_limit_mb", "container_oom_kills"}
	for _, m := range sysMetrics {
		if m.expr == "" {
			names = append(names, m.name)
		}
	}
	return regexp.MustCompile(`\b(` + strings.Join(names, "|") + `)\b`)
}()
