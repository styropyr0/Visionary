package analytics

// Overview panels are fixed queries. They use the same {{from}}/{{to}}/{{bucket}}
// placeholders as generated queries, rendered by timeRange.render.

const window = `created_at >= {{from}} AND created_at < {{to}}`

const bucketExpr = `(CAST(strftime('%s', created_at) AS INTEGER) / {{bucket}}) * {{bucket}}`

// Percentiles: SQLite has no percentile function, so rows are ranked inside
// each group and the pth percentile is the smallest value ranked at or above
// p * n.
var overviewPanels = map[string]string{
	// One pass over the window: D1 bills by rows read, and separate subqueries
	// each re-scan the range.
	"stats": `
WITH o AS (
  SELECT status, duration_ms, user_id,
    ROW_NUMBER() OVER (ORDER BY duration_ms) AS rn,
    COUNT(*) OVER () AS n
  FROM api_logs WHERE ` + window + `
)
SELECT
  COUNT(*) AS requests,
  COALESCE(SUM(status >= 500), 0) AS server_errors,
  COALESCE(SUM(status >= 400 AND status < 500), 0) AS client_errors,
  COUNT(DISTINCT user_id) AS users,
  MIN(CASE WHEN rn >= 0.50 * n THEN duration_ms END) AS p50,
  MIN(CASE WHEN rn >= 0.95 * n THEN duration_ms END) AS p95,
  MIN(CASE WHEN rn >= 0.99 * n THEN duration_ms END) AS p99
FROM o`,

	"traffic": `
SELECT
  ` + bucketExpr + ` AS t,
  CASE
    WHEN status >= 500 THEN '5xx'
    WHEN status >= 400 THEN '4xx'
    WHEN status >= 300 THEN '3xx'
    ELSE '2xx'
  END AS series,
  COUNT(*) AS value
FROM api_logs
WHERE ` + window + `
GROUP BY t, series
ORDER BY t`,

	"latency": `
WITH b AS (
  SELECT ` + bucketExpr + ` AS t, duration_ms FROM api_logs WHERE ` + window + `
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
  ` + bucketExpr + ` AS t,
  ROUND(100.0 * SUM(status >= 500) / COUNT(*), 2) AS server_errors,
  ROUND(100.0 * SUM(status >= 400 AND status < 500) / COUNT(*), 2) AS client_errors
FROM api_logs
WHERE ` + window + `
GROUP BY t
ORDER BY t`,

	"status_codes": `
SELECT CAST(status AS TEXT) AS status, COUNT(*) AS requests
FROM api_logs
WHERE ` + window + `
GROUP BY status
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
  ROUND(100.0 * SUM(status >= 500) / COUNT(*), 2) AS error_rate,
  ROUND(100.0 * SUM(status >= 400 AND status < 500) / COUNT(*), 2) AS client_error_rate,
  ROUND(AVG(duration_ms), 1) AS avg_ms,
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
GROUP BY status, error, route
ORDER BY occurrences DESC
LIMIT 25`,

	"users": `
SELECT
  user_id,
  COALESCE(MAX(user_handle), '') AS handle,
  COUNT(*) AS requests,
  SUM(status >= 400) AS errors,
  ROUND(AVG(duration_ms), 1) AS avg_ms,
  MAX(created_at) AS last_seen
FROM api_logs
WHERE ` + window + ` AND user_id IS NOT NULL
GROUP BY user_id
ORDER BY requests DESC
LIMIT 25`,
}
