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

func systemPanels(q sqlDialect) map[string]string {
	return map[string]string{
		// Latest values plus peaks over the range. latest reads the raw table
		// itself, so the rollup variant (which swaps r) keeps exact values.
		"stats": `
WITH r AS (
  SELECT * FROM system_metrics WHERE ` + sysWindow + `
),
latest AS (
  SELECT * FROM system_metrics AS m WHERE ` + sysWindow + ` ORDER BY created_at DESC LIMIT 1
)
SELECT
  (SELECT host_cpu_percent FROM latest) AS cpu_now,
  (SELECT ` + q.round("AVG(host_cpu_percent)", 1) + ` FROM r) AS cpu_avg,
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
  (SELECT COUNT(DISTINCT instance) FROM r) AS instances,
  (SELECT cpu_count FROM latest) AS cpu_count,
  (SELECT disk_used_percent FROM latest) AS disk_now,
  (SELECT disk_free_gb FROM latest) AS disk_free_gb,
  (SELECT MAX(disk_busy_percent) FROM r) AS disk_busy_max,
  (SELECT ` + q.round("AVG(disk_read_kbps) * 1024", 0) + ` FROM r) AS disk_read_avg,
  (SELECT ` + q.round("MAX(disk_read_kbps) * 1024", 0) + ` FROM r) AS disk_read_max,
  (SELECT ` + q.round("AVG(disk_write_kbps) * 1024", 0) + ` FROM r) AS disk_write_avg,
  (SELECT ` + q.round("MAX(disk_write_kbps) * 1024", 0) + ` FROM r) AS disk_write_max,
  (SELECT ` + q.round("AVG(disk_busy_percent)", 1) + ` FROM r) AS disk_busy_avg,
  (SELECT swap_used_mb FROM latest) AS swap_now,
  (SELECT swap_total_mb FROM latest) AS swap_total,
  (SELECT tcp_established FROM latest) AS tcp_now,
  (SELECT MAX(tcp_established) FROM r) AS tcp_max,
  (SELECT proc_open_fds FROM latest) AS fds_now,
  (SELECT proc_max_fds FROM latest) AS fds_limit,
  (SELECT ` + q.round("AVG(net_rx_kbps + net_tx_kbps) * 1024", 0) + ` FROM r) AS net_bytes_avg,
  (SELECT ` + q.round("MAX(net_rx_kbps + net_tx_kbps) * 1024", 0) + ` FROM r) AS net_bytes_max,
  (SELECT container_mem_used_mb FROM latest) AS ctr_mem_now,
  (SELECT container_mem_limit_mb FROM latest) AS ctr_mem_limit,
  (SELECT container_cpu_cores FROM latest) AS ctr_cpu_now,
  (SELECT container_cpu_limit FROM latest) AS ctr_cpu_limit,
  (SELECT psi_mem_some_avg10 FROM latest) AS psi_mem_now,
  (SELECT psi_io_some_avg10 FROM latest) AS psi_io_now,
  (SELECT cpu_temp_c FROM latest) AS cpu_temp_now,
  (SELECT gpu_count FROM latest) AS gpu_count,
  (SELECT gpu_util_percent FROM latest) AS gpu_util_now`,

		"cpu": `
SELECT {{t}} AS t,
  ` + q.round("AVG(host_cpu_percent)", 1) + ` AS avg_cpu,
  MAX(host_cpu_percent) AS max_cpu
FROM system_metrics WHERE ` + sysWindow + `
GROUP BY t ORDER BY t`,

		"process_cpu": `
SELECT {{t}} AS t,
  ` + q.round("AVG(proc_cpu_percent)", 1) + ` AS avg_process_cpu,
  MAX(proc_cpu_percent) AS max_process_cpu
FROM system_metrics WHERE ` + sysWindow + `
GROUP BY t ORDER BY t`,

		"memory": `
SELECT {{t}} AS t,
  ` + q.round("AVG(host_mem_percent)", 1) + ` AS host_memory,
  ` + q.round("AVG(disk_used_percent)", 1) + ` AS disk_used
FROM system_metrics WHERE ` + sysWindow + `
GROUP BY t ORDER BY t`,

		"process_memory": `
SELECT {{t}} AS t,
  ` + q.round("MAX(proc_rss_mb) * 1048576.0", 0) + ` AS rss_bytes,
  ` + q.round("MAX(heap_inuse_mb) * 1048576.0", 0) + ` AS heap_bytes
FROM system_metrics WHERE ` + sysWindow + `
GROUP BY t ORDER BY t`,

		"load": `
SELECT {{t}} AS t,
  ` + q.round("AVG(load_1)", 2) + ` AS load_1,
  ` + q.round("AVG(load_5)", 2) + ` AS load_5,
  ` + q.round("AVG(load_15)", 2) + ` AS load_15
FROM system_metrics WHERE ` + sysWindow + ` AND load_1 IS NOT NULL
GROUP BY t ORDER BY t`,

		"goroutines": `
SELECT {{t}} AS t,
  MAX(goroutines) AS goroutines,
  MAX(proc_threads) AS threads
FROM system_metrics WHERE ` + sysWindow + `
GROUP BY t ORDER BY t`,

		"gc": `
SELECT {{t}} AS t,
  ` + q.round("SUM(gc_pause_ms)", 2) + ` AS gc_pause_ms
FROM system_metrics WHERE ` + sysWindow + `
GROUP BY t ORDER BY t`,

		"db_pool": `
SELECT {{t}} AS t,
  MAX(db_acquired_conns) AS in_use,
  MAX(db_total_conns) AS open,
  MAX(db_max_conns) AS pool_limit
FROM system_metrics WHERE ` + sysWindow + `
GROUP BY t ORDER BY t`,

		"db_wait": `
SELECT {{t}} AS t,
  ` + q.round("SUM(db_acquire_wait_ms)", 1) + ` AS acquire_wait_ms
FROM system_metrics WHERE ` + sysWindow + `
GROUP BY t ORDER BY t`,

		"network": `
SELECT {{t}} AS t,
  ` + q.round("AVG(net_rx_kbps) * 1024", 0) + ` AS received_bytes,
  ` + q.round("AVG(net_tx_kbps) * 1024", 0) + ` AS sent_bytes
FROM system_metrics WHERE ` + sysWindow + `
GROUP BY t ORDER BY t`,

		"throughput": `
SELECT {{t}} AS t,
  ` + q.round("AVG(http_rps)", 2) + ` AS avg_rps,
  MAX(http_rps) AS peak_rps,
  MAX(http_in_flight) AS max_in_flight
FROM system_metrics WHERE ` + sysWindow + `
GROUP BY t ORDER BY t`,

		// The worst intervals by CPU, with what the API was doing at the time.
		"spikes": `
WITH s AS (
  SELECT {{t}} AS t,
    MAX(host_cpu_percent) AS cpu,
    MAX(host_mem_percent) AS memory,
    MAX(db_acquired_conns) AS db_in_use,
    MAX(http_in_flight) AS in_flight,
    MAX(goroutines) AS goroutines
  FROM system_metrics WHERE ` + sysWindow + `
  GROUP BY t
),
a AS (
  SELECT {{t}} AS t,
    COUNT(*) AS requests,
    ` + countIf("status >= 500") + ` AS errors,
    MAX(duration_ms) AS slowest_ms
  FROM api_logs WHERE ` + window + `
  GROUP BY t
)
SELECT s.t, s.cpu, s.memory, s.db_in_use, s.in_flight, s.goroutines,
  COALESCE(a.requests, 0) AS requests, COALESCE(a.errors, 0) AS errors, a.slowest_ms
FROM s LEFT JOIN a ON a.t = s.t
ORDER BY s.cpu DESC
LIMIT 12`,

		"cpu_breakdown": `
SELECT {{t}} AS t,
  ` + q.round("AVG(cpu_user_percent)", 1) + ` AS user_time,
  ` + q.round("AVG(cpu_system_percent)", 1) + ` AS system_time,
  ` + q.round("AVG(cpu_iowait_percent)", 1) + ` AS iowait,
  ` + q.round("AVG(cpu_steal_percent)", 1) + ` AS steal
FROM system_metrics WHERE ` + sysWindow + ` AND cpu_user_percent IS NOT NULL
GROUP BY t ORDER BY t`,

		"load_per_core": `
SELECT {{t}} AS t,
  ` + q.round("AVG(load_1 / NULLIF(cpu_count, 0))", 2) + ` AS load_per_core,
  ` + q.round("MAX(load_1 / NULLIF(cpu_count, 0))", 2) + ` AS peak_load_per_core
FROM system_metrics WHERE ` + sysWindow + ` AND load_1 IS NOT NULL
GROUP BY t ORDER BY t`,

		"memory_detail": `
SELECT {{t}} AS t,
  ` + q.round("AVG(host_mem_used_mb) * 1048576.0", 0) + ` AS used,
  ` + q.round("AVG(host_mem_available_mb) * 1048576.0", 0) + ` AS available,
  ` + q.round("AVG(host_mem_cached_mb) * 1048576.0", 0) + ` AS cached
FROM system_metrics WHERE ` + sysWindow + `
GROUP BY t ORDER BY t`,

		// The service's own container (its cgroup), from the SDKs.
		"container_cpu": `
SELECT {{t}} AS t,
  ` + q.round("AVG(container_cpu_cores)", 3) + ` AS cores_used,
  MAX(container_cpu_cores) AS peak_cores,
  MAX(container_cpu_limit) AS cpu_limit
FROM system_metrics WHERE ` + sysWindow + ` AND container_cpu_cores IS NOT NULL
GROUP BY t ORDER BY t`,

		"container_memory": `
SELECT {{t}} AS t,
  ` + q.round("MAX(container_mem_used_mb) * 1048576.0", 0) + ` AS used,
  ` + q.round("MAX(container_mem_limit_mb) * 1048576.0", 0) + ` AS memory_limit
FROM system_metrics WHERE ` + sysWindow + ` AND container_mem_used_mb IS NOT NULL
GROUP BY t ORDER BY t`,

		"container_throttling": `
SELECT {{t}} AS t,
  ` + q.round("AVG(container_cpu_throttled_percent)", 1) + ` AS throttled
FROM system_metrics WHERE ` + sysWindow + ` AND container_cpu_throttled_percent IS NOT NULL
GROUP BY t ORDER BY t`,

		"container_oom": `
SELECT {{t}} AS t,
  SUM(container_oom_kills) AS oom_kills
FROM system_metrics WHERE ` + sysWindow + ` AND container_oom_kills IS NOT NULL
GROUP BY t ORDER BY t`,

		"swap": `
SELECT {{t}} AS t,
  ` + q.round("MAX(swap_used_mb) * 1048576.0", 0) + ` AS swap_used,
  ` + q.round("MAX(swap_total_mb) * 1048576.0", 0) + ` AS swap_total
FROM system_metrics WHERE ` + sysWindow + ` AND swap_total_mb IS NOT NULL
GROUP BY t ORDER BY t`,

		"disk_space": `
SELECT {{t}} AS t,
  ` + q.round("MAX(disk_used_percent)", 1) + ` AS space_used,
  ` + q.round("MAX(disk_inodes_percent)", 1) + ` AS inodes_used
FROM system_metrics WHERE ` + sysWindow + `
GROUP BY t ORDER BY t`,

		"disk_throughput": `
SELECT {{t}} AS t,
  ` + q.round("AVG(disk_read_kbps) * 1024", 0) + ` AS read_speed,
  ` + q.round("AVG(disk_write_kbps) * 1024", 0) + ` AS write_speed
FROM system_metrics WHERE ` + sysWindow + ` AND disk_read_kbps IS NOT NULL
GROUP BY t ORDER BY t`,

		"disk_iops": `
SELECT {{t}} AS t,
  ` + q.round("AVG(disk_read_iops)", 1) + ` AS read_ops,
  ` + q.round("AVG(disk_write_iops)", 1) + ` AS write_ops
FROM system_metrics WHERE ` + sysWindow + ` AND disk_read_iops IS NOT NULL
GROUP BY t ORDER BY t`,

		"disk_latency": `
SELECT {{t}} AS t,
  ` + q.round("AVG(disk_await_ms)", 2) + ` AS avg_await,
  MAX(disk_await_ms) AS peak_await
FROM system_metrics WHERE ` + sysWindow + ` AND disk_await_ms IS NOT NULL
GROUP BY t ORDER BY t`,

		"disk_busy": `
SELECT {{t}} AS t,
  ` + q.round("AVG(disk_busy_percent)", 1) + ` AS avg_busy,
  MAX(disk_busy_percent) AS peak_busy
FROM system_metrics WHERE ` + sysWindow + ` AND disk_busy_percent IS NOT NULL
GROUP BY t ORDER BY t`,

		"network_packets": `
SELECT {{t}} AS t,
  ` + q.round("AVG(net_rx_pps)", 1) + ` AS received,
  ` + q.round("AVG(net_tx_pps)", 1) + ` AS sent
FROM system_metrics WHERE ` + sysWindow + ` AND net_rx_pps IS NOT NULL
GROUP BY t ORDER BY t`,

		"network_errors": `
SELECT {{t}} AS t,
  SUM(net_errors) AS errors,
  SUM(net_drops) AS drops
FROM system_metrics WHERE ` + sysWindow + ` AND net_errors IS NOT NULL
GROUP BY t ORDER BY t`,

		"tcp": `
SELECT {{t}} AS t,
  MAX(tcp_established) AS established
FROM system_metrics WHERE ` + sysWindow + ` AND tcp_established IS NOT NULL
GROUP BY t ORDER BY t`,

		"tcp_retrans": `
SELECT {{t}} AS t,
  SUM(tcp_retrans) AS retransmits
FROM system_metrics WHERE ` + sysWindow + ` AND tcp_retrans IS NOT NULL
GROUP BY t ORDER BY t`,

		"processes": `
SELECT {{t}} AS t,
  MAX(procs_running) AS running,
  MAX(procs_blocked) AS blocked
FROM system_metrics WHERE ` + sysWindow + ` AND procs_running IS NOT NULL
GROUP BY t ORDER BY t`,

		"context_switches": `
SELECT {{t}} AS t,
  ` + q.round("AVG(ctx_switches_ps)", 0) + ` AS per_second
FROM system_metrics WHERE ` + sysWindow + ` AND ctx_switches_ps IS NOT NULL
GROUP BY t ORDER BY t`,

		"process_io": `
SELECT {{t}} AS t,
  ` + q.round("AVG(proc_read_kbps) * 1024", 0) + ` AS read_bytes,
  ` + q.round("AVG(proc_write_kbps) * 1024", 0) + ` AS write_bytes
FROM system_metrics WHERE ` + sysWindow + ` AND proc_read_kbps IS NOT NULL
GROUP BY t ORDER BY t`,

		"file_descriptors": `
SELECT {{t}} AS t,
  MAX(proc_open_fds) AS open_files,
  MAX(proc_max_fds) AS limit_files
FROM system_metrics WHERE ` + sysWindow + ` AND proc_open_fds IS NOT NULL
GROUP BY t ORDER BY t`,

		// Linux pressure, virtual memory, kernel limits, thermals and GPUs
		"pressure": `
SELECT {{t}} AS t,
  ` + q.round("AVG(psi_cpu_some_avg10)", 2) + ` AS cpu_some,
  ` + q.round("AVG(psi_mem_some_avg10)", 2) + ` AS memory_some,
  ` + q.round("AVG(psi_mem_full_avg10)", 2) + ` AS memory_full,
  ` + q.round("AVG(psi_io_some_avg10)", 2) + ` AS io_some,
  ` + q.round("AVG(psi_io_full_avg10)", 2) + ` AS io_full
FROM system_metrics WHERE ` + sysWindow + ` AND psi_cpu_some_avg10 IS NOT NULL
GROUP BY t ORDER BY t`,

		"page_faults": `
SELECT {{t}} AS t,
  ` + q.round("AVG(minor_faults_ps)", 1) + ` AS minor,
  ` + q.round("AVG(major_faults_ps)", 1) + ` AS major
FROM system_metrics WHERE ` + sysWindow + ` AND minor_faults_ps IS NOT NULL
GROUP BY t ORDER BY t`,

		"swap_activity": `
SELECT {{t}} AS t,
  ` + q.round("AVG(swap_in_ps)", 1) + ` AS swapped_in,
  ` + q.round("AVG(swap_out_ps)", 1) + ` AS swapped_out
FROM system_metrics WHERE ` + sysWindow + ` AND swap_in_ps IS NOT NULL
GROUP BY t ORDER BY t`,

		"reclaim": `
SELECT {{t}} AS t,
  ` + q.round("AVG(pages_scanned_ps)", 1) + ` AS scanned,
  ` + q.round("AVG(pages_stolen_ps)", 1) + ` AS reclaimed,
  SUM(host_oom_kills) AS processes_killed
FROM system_metrics WHERE ` + sysWindow + ` AND pages_scanned_ps IS NOT NULL
GROUP BY t ORDER BY t`,

		"dirty_memory": `
SELECT {{t}} AS t,
  ` + q.round("AVG(mem_dirty_mb) * 1048576.0", 0) + ` AS dirty,
  ` + q.round("AVG(mem_writeback_mb) * 1048576.0", 0) + ` AS writeback,
  ` + q.round("AVG(mem_slab_mb) * 1048576.0", 0) + ` AS slab,
  ` + q.round("AVG(mem_shmem_mb) * 1048576.0", 0) + ` AS shared
FROM system_metrics WHERE ` + sysWindow + ` AND mem_dirty_mb IS NOT NULL
GROUP BY t ORDER BY t`,

		"commit": `
SELECT {{t}} AS t,
  ` + q.round("MAX(mem_committed_mb) * 1048576.0", 0) + ` AS committed,
  ` + q.round("MAX(mem_commit_limit_mb) * 1048576.0", 0) + ` AS commit_limit
FROM system_metrics WHERE ` + sysWindow + ` AND mem_committed_mb IS NOT NULL
GROUP BY t ORDER BY t`,

		"file_handles": `
SELECT {{t}} AS t,
  MAX(files_open) AS open_files,
  MAX(files_max) AS limit_files
FROM system_metrics WHERE ` + sysWindow + ` AND files_open IS NOT NULL
GROUP BY t ORDER BY t`,

		"conntrack": `
SELECT {{t}} AS t,
  MAX(conntrack_count) AS tracked,
  MAX(conntrack_max) AS table_limit
FROM system_metrics WHERE ` + sysWindow + ` AND conntrack_count IS NOT NULL
GROUP BY t ORDER BY t`,

		"sockets": `
SELECT {{t}} AS t,
  MAX(sockets_used) AS sockets,
  MAX(tcp_time_wait) AS time_wait,
  MAX(tcp_close_wait) AS close_wait,
  MAX(tcp_orphans) AS orphans
FROM system_metrics WHERE ` + sysWindow + ` AND sockets_used IS NOT NULL
GROUP BY t ORDER BY t`,

		"udp_errors": `
SELECT {{t}} AS t,
  SUM(udp_in_errors) AS undeliverable,
  SUM(udp_buf_errors) AS buffer_full
FROM system_metrics WHERE ` + sysWindow + ` AND udp_in_errors IS NOT NULL
GROUP BY t ORDER BY t`,

		"cpu_temp": `
SELECT {{t}} AS t,
  ` + q.round("AVG(cpu_temp_c)", 1) + ` AS avg_temp,
  MAX(cpu_temp_c) AS peak_temp
FROM system_metrics WHERE ` + sysWindow + ` AND cpu_temp_c IS NOT NULL
GROUP BY t ORDER BY t`,

		"cpu_clock": `
SELECT {{t}} AS t,
  ` + q.round("AVG(cpu_mhz)", 0) + ` AS current_clock,
  MAX(cpu_max_mhz) AS max_clock
FROM system_metrics WHERE ` + sysWindow + ` AND cpu_mhz IS NOT NULL
GROUP BY t ORDER BY t`,

		"cpu_throttling": `
SELECT {{t}} AS t,
  SUM(cpu_throttle_events) AS throttle_events
FROM system_metrics WHERE ` + sysWindow + ` AND cpu_throttle_events IS NOT NULL
GROUP BY t ORDER BY t`,

		"gpu": `
SELECT {{t}} AS t,
  ` + q.round("AVG(gpu_util_percent)", 1) + ` AS gpu_busy,
  ` + q.round("AVG(gpu_mem_percent)", 1) + ` AS gpu_memory
FROM system_metrics WHERE ` + sysWindow + ` AND gpu_count IS NOT NULL
GROUP BY t ORDER BY t`,

		"gpu_temp": `
SELECT {{t}} AS t,
  MAX(gpu_temp_c) AS peak_temp
FROM system_metrics WHERE ` + sysWindow + ` AND gpu_temp_c IS NOT NULL
GROUP BY t ORDER BY t`,

		"container_faults": `
SELECT {{t}} AS t,
  ` + q.round("AVG(container_minor_faults_ps)", 1) + ` AS minor,
  ` + q.round("AVG(container_major_faults_ps)", 1) + ` AS major
FROM system_metrics WHERE ` + sysWindow + ` AND container_minor_faults_ps IS NOT NULL
GROUP BY t ORDER BY t`,

		"container_memory_events": `
SELECT {{t}} AS t,
  SUM(container_mem_high_events) AS over_high,
  SUM(container_mem_max_events) AS hit_max
FROM system_metrics WHERE ` + sysWindow + ` AND container_mem_high_events IS NOT NULL
GROUP BY t ORDER BY t`,

		// sdk and runtime say which language wrote the rows, so runtime
		// charts can use its words (goroutines, threads, heap).
		"instances": `
SELECT instance, MAX(created_at) AS last_seen, COUNT(*) AS samples, MAX(sdk) AS sdk, MAX(runtime) AS runtime
FROM system_metrics WHERE created_at >= {{from}} AND created_at < {{to}}
GROUP BY instance ORDER BY last_seen DESC`,
	}
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
