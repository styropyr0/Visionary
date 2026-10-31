package analytics

import (
	"context"
	"errors"
)

// A mistake in the expression comes back as a query error the model can fix
func (s *Server) analystPromQL(ctx context.Context, query string, tr timeRange) (*Result, error) {
	res, err := s.promResult(ctx, query, tr)
	var pe *promError
	if errors.As(err, &pe) || errors.Is(err, errPromTooLarge) {
		return nil, &queryError{err}
	}
	return res, err
}

func promqlPrompt() string {
	return `PromQL: an alternative to SQL for metrics, Prometheus's query language over the same data.
- Custom metrics by name with their labels plus service and instance: orders_placed{region="eu"}. Counters read as running totals; use rate(x[5m]) (per second) or increase(x[1h]). Gauges read as their latest value. A histogram h is h_bucket{le}, h_sum and h_count: histogram_quantile(0.95, sum by (le) (rate(h_bucket[5m]))).
- Built-in series: visionary_http_requests_total{service, route, method, status} (counter), visionary_http_request_duration_ms_bucket{service, route, method, le} with _sum and _count (ms histogram), visionary_spans_total{service, kind, status}, visionary_span_duration_ms_bucket{service, kind, le}, visionary_errors_total{service, type, route}, and gauges visionary_system_<column>{service, instance} for host_cpu_percent, load_1, host_mem_percent, host_mem_used_mb, disk_used_percent, disk_free_gb, net_rx_kbps, net_tx_kbps, proc_cpu_percent, proc_rss_mb, goroutines, heap_alloc_mb, gc_pause_ms, db_acquired_conns, db_max_conns, db_acquire_wait_ms, http_rps, http_in_flight, container_cpu_cores, container_mem_used_mb.
- Supported: selectors with =, !=, =~, !~; offset; rate, irate, increase, delta, *_over_time (avg, min, max, sum, count, last); sum, avg, min, max, count, topk, bottomk, quantile with by/without; + - * / % ^ and comparisons (with bool), between vectors with on/ignoring and group_left/group_right; and, or, unless; abs, ceil, floor, round, clamp_min, clamp_max, histogram_quantile, scalar, vector, absent. No subqueries and no @.
- Examples: 5xx share per route: sum by (route) (rate(visionary_http_requests_total{status=~"5.."}[5m])) / sum by (route) (rate(visionary_http_requests_total[5m])). p95 latency: histogram_quantile(0.95, sum by (le) (rate(visionary_http_request_duration_ms_bucket[5m]))).`
}
