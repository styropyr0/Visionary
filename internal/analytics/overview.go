package analytics

// Overview panels are fixed queries. They use the same {{from}}/{{to}}/{{t}}
// placeholders as generated queries, rendered by timeRange.render.

const window = `created_at >= {{from}} AND created_at < {{to}}`

// Percentiles: not every dialect has a percentile function, so rows are ranked
// inside each group and the pth percentile is the smallest value ranked at or
// above p * n.
func overviewPanels(q sqlDialect) map[string]string {
	return map[string]string{
		// One pass over the window: D1 bills by rows read, and separate
		// subqueries each re-scan the range.
		"stats": `
WITH o AS (
  SELECT status, duration_ms, user_id,
    ROW_NUMBER() OVER (ORDER BY duration_ms) AS rn,
    COUNT(*) OVER () AS n
  FROM api_logs WHERE ` + window + `
)
SELECT
  COUNT(*) AS requests,
  COALESCE(` + countIf("status >= 500") + `, 0) AS server_errors,
  COALESCE(` + countIf("status >= 400 AND status < 500") + `, 0) AS client_errors,
  COUNT(DISTINCT user_id) AS users,
  MIN(CASE WHEN rn >= 0.50 * n THEN duration_ms END) AS p50,
  MIN(CASE WHEN rn >= 0.95 * n THEN duration_ms END) AS p95,
  MIN(CASE WHEN rn >= 0.99 * n THEN duration_ms END) AS p99
FROM o`,

		"traffic": `
SELECT
  {{t}} AS t,
  CASE
    WHEN status >= 500 THEN '5xx'
    WHEN status >= 400 THEN '4xx'
    WHEN status >= 300 THEN '3xx'
    ELSE '2xx'
  END AS series,
  COUNT(*) AS value
FROM api_logs
WHERE ` + window + `
GROUP BY 1, 2
ORDER BY 1`,

		"latency": `
WITH b AS (
  SELECT {{t}} AS t, duration_ms FROM api_logs WHERE ` + window + `
),
r AS (
  SELECT t, duration_ms,
    ROW_NUMBER() OVER (PARTITION BY t ORDER BY duration_ms) AS rn,
    COUNT(*) OVER (PARTITION BY t) AS n
  FROM b
)
SELECT
  t,
  MIN(CASE WHEN rn >= 0.50 * n THEN duration_ms END) AS p50,
  MIN(CASE WHEN rn >= 0.95 * n THEN duration_ms END) AS p95,
  MIN(CASE WHEN rn >= 0.99 * n THEN duration_ms END) AS p99
FROM r
GROUP BY t
ORDER BY t`,

		"error_rate": `
SELECT
  {{t}} AS t,
  ` + q.pct("status >= 500") + ` AS server_errors,
  ` + q.pct("status >= 400 AND status < 500") + ` AS client_errors
FROM api_logs
WHERE ` + window + `
GROUP BY 1
ORDER BY 1`,

		"status_codes": `
SELECT ` + q.text("status") + ` AS status, COUNT(*) AS requests
FROM api_logs
WHERE ` + window + `
GROUP BY api_logs.status
ORDER BY requests DESC
LIMIT 12`,

		"routes": `
WITH r AS (
  SELECT COALESCE(route, path) AS route, method, status, duration_ms
  FROM api_logs WHERE ` + window + `
),
ranked AS (
  SELECT route, method, status, duration_ms,
    ROW_NUMBER() OVER (PARTITION BY route, method ORDER BY duration_ms) AS rn,
    COUNT(*) OVER (PARTITION BY route, method) AS n
  FROM r
)
SELECT
  method,
  route,
  COUNT(*) AS requests,
  ` + q.pct("status >= 500") + ` AS error_rate,
  ` + q.pct("status >= 400 AND status < 500") + ` AS client_error_rate,
  ` + q.round("AVG(duration_ms)", 1) + ` AS avg_ms,
  MIN(CASE WHEN rn >= 0.50 * n THEN duration_ms END) AS p50_ms,
  MIN(CASE WHEN rn >= 0.95 * n THEN duration_ms END) AS p95_ms
FROM ranked
GROUP BY route, method
ORDER BY requests DESC
LIMIT 50`,

		"errors": `
SELECT
  status,
  COALESCE(error_key, error_code, '—') AS error,
  COALESCE(route, path) AS route,
  COUNT(*) AS occurrences,
  MAX(created_at) AS last_seen
FROM api_logs
WHERE ` + window + ` AND status >= 400
GROUP BY 1, 2, 3
ORDER BY occurrences DESC
LIMIT 25`,

		"users": `
SELECT
  user_id,
  COALESCE(MAX(user_handle), '') AS handle,
  COUNT(*) AS requests,
  ` + countIf("status >= 400") + ` AS errors,
  ` + q.round("AVG(duration_ms)", 1) + ` AS avg_ms,
  MAX(created_at) AS last_seen
FROM api_logs
WHERE ` + window + ` AND user_id IS NOT NULL
GROUP BY user_id
ORDER BY requests DESC
LIMIT 25`,
	}
}
