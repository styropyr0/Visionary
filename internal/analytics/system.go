package analytics

import (
	"errors"
	"regexp"
	"strings"
)

// System panels read the system_metrics table written by the API server's
// recorder. {{instance}} narrows to one server; it renders to nothing when all
// instances are shown.

const sysWindow = `created_at >= {{from}} AND created_at < {{to}}{{instance}}`

const mb = `1048576.0`

var systemPanels = map[string]string{
	// Latest values plus peaks over the range.
	"stats": `
WITH r AS (
  SELECT * FROM system_metrics WHERE ` + sysWindow + `
),
latest AS (
  SELECT * FROM r ORDER BY created_at DESC LIMIT 1
)
SELECT
  (SELECT host_cpu_percent FROM latest) AS cpu_now,
  (SELECT ROUND(AVG(host_cpu_percent), 1) FROM r) AS cpu_avg,
  (SELECT MAX(host_cpu_percent) FROM r) AS cpu_max,
  (SELECT host_mem_percent FROM latest) AS mem_now,
  (SELECT MAX(host_mem_percent) FROM r) AS mem_max,
  (SELECT load_1 FROM latest) AS load_now,
  (SELECT MAX(load_1) FROM r) AS load_max,
  (SELECT goroutines FROM latest) AS goroutines_now,
  (SELECT MAX(goroutines) FROM r) AS goroutines_max,
  (SELECT db_acquired_conns FROM latest) AS db_now,
  (SELECT MAX(db_acquired_conns) FROM r) AS db_max,
  (SELECT db_max_conns FROM latest) AS db_limit,
  (SELECT proc_rss_mb FROM latest) AS rss_now,
  (SELECT MAX(proc_rss_mb) FROM r) AS rss_max,
  (SELECT uptime_s FROM latest) AS uptime_s,
  (SELECT created_at FROM latest) AS last_seen,
  (SELECT COUNT(DISTINCT instance) FROM r) AS instances`,

	"cpu": `
SELECT ` + bucketExpr + ` AS t,
  ROUND(AVG(host_cpu_percent), 1) AS avg_cpu,
  MAX(host_cpu_percent) AS max_cpu
FROM system_metrics WHERE ` + sysWindow + `
GROUP BY t ORDER BY t`,

	"process_cpu": `
SELECT ` + bucketExpr + ` AS t,
  ROUND(AVG(proc_cpu_percent), 1) AS avg_process_cpu,
  MAX(proc_cpu_percent) AS max_process_cpu
FROM system_metrics WHERE ` + sysWindow + `
GROUP BY t ORDER BY t`,

	"memory": `
SELECT ` + bucketExpr + ` AS t,
  ROUND(AVG(host_mem_percent), 1) AS host_memory,
  ROUND(AVG(disk_used_percent), 1) AS disk_used
FROM system_metrics WHERE ` + sysWindow + `
GROUP BY t ORDER BY t`,

	"process_memory": `
SELECT ` + bucketExpr + ` AS t,
  ROUND(MAX(proc_rss_mb) * ` + mb + `) AS rss_bytes,
  ROUND(MAX(heap_inuse_mb) * ` + mb + `) AS heap_bytes
FROM system_metrics WHERE ` + sysWindow + `
GROUP BY t ORDER BY t`,

	"load": `
SELECT ` + bucketExpr + ` AS t,
  ROUND(AVG(load_1), 2) AS load_1,
  ROUND(AVG(load_5), 2) AS load_5,
  ROUND(AVG(load_15), 2) AS load_15
FROM system_metrics WHERE ` + sysWindow + ` AND load_1 IS NOT NULL
GROUP BY t ORDER BY t`,

	"goroutines": `
SELECT ` + bucketExpr + ` AS t,
  MAX(goroutines) AS goroutines,
  MAX(proc_threads) AS threads
FROM system_metrics WHERE ` + sysWindow + `
GROUP BY t ORDER BY t`,

	"gc": `
SELECT ` + bucketExpr + ` AS t,
  ROUND(SUM(gc_pause_ms), 2) AS gc_pause_ms
FROM system_metrics WHERE ` + sysWindow + `
GROUP BY t ORDER BY t`,

	"db_pool": `
SELECT ` + bucketExpr + ` AS t,
  MAX(db_acquired_conns) AS in_use,
  MAX(db_total_conns) AS open,
  MAX(db_max_conns) AS pool_limit
FROM system_metrics WHERE ` + sysWindow + `
GROUP BY t ORDER BY t`,

	"db_wait": `
SELECT ` + bucketExpr + ` AS t,
  ROUND(SUM(db_acquire_wait_ms), 1) AS acquire_wait_ms
FROM system_metrics WHERE ` + sysWindow + `
GROUP BY t ORDER BY t`,

	"network": `
SELECT ` + bucketExpr + ` AS t,
  ROUND(AVG(net_rx_kbps) * 1024) AS received_bytes,
  ROUND(AVG(net_tx_kbps) * 1024) AS sent_bytes
FROM system_metrics WHERE ` + sysWindow + `
GROUP BY t ORDER BY t`,

	"throughput": `
SELECT ` + bucketExpr + ` AS t,
  ROUND(AVG(http_rps), 2) AS avg_rps,
  MAX(http_rps) AS peak_rps,
  MAX(http_in_flight) AS max_in_flight
FROM system_metrics WHERE ` + sysWindow + `
GROUP BY t ORDER BY t`,

	// The worst intervals by CPU, with what the API was doing at the time.
	"spikes": `
WITH s AS (
  SELECT ` + bucketExpr + ` AS t,
    MAX(host_cpu_percent) AS cpu,
    MAX(host_mem_percent) AS memory,
    MAX(db_acquired_conns) AS db_in_use,
    MAX(http_in_flight) AS in_flight,
    MAX(goroutines) AS goroutines
  FROM system_metrics WHERE ` + sysWindow + `
  GROUP BY t
),
a AS (
  SELECT ` + bucketExpr + ` AS t,
    COUNT(*) AS requests,
    SUM(status >= 500) AS errors,
    MAX(duration_ms) AS slowest_ms
  FROM api_logs WHERE ` + window + `
  GROUP BY t
)
SELECT s.t, s.cpu, s.memory, s.db_in_use, s.in_flight, s.goroutines,
  COALESCE(a.requests, 0) AS requests, COALESCE(a.errors, 0) AS errors, a.slowest_ms
FROM s LEFT JOIN a ON a.t = s.t
ORDER BY s.cpu DESC
LIMIT 12`,

	"instances": `
SELECT instance, MAX(created_at) AS last_seen, COUNT(*) AS samples
FROM system_metrics WHERE created_at >= {{from}} AND created_at < {{to}}
GROUP BY instance ORDER BY last_seen DESC`,
}

var instanceName = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// instanceClause filters to one instance. The name is validated against a
// strict character set before it is inlined.
func instanceClause(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil
	}
	if !instanceName.MatchString(name) {
		return "", errors.New("invalid instance name")
	}
	return " AND instance = '" + name + "'", nil
}
