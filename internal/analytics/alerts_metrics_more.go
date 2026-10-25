package analytics

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// More metrics a rule can watch, beyond the core request and host set in
// alerts_metrics.go: request costs, more host and runtime signals,
// exceptions, browser and app metrics (client_events), calls to databases
// and other services (spans), and uptime timings and certificates.

var (
	errorScopes  = []string{"route", "method", "service"}
	clientScopes = []string{"app"}
	spanScopes   = []string{"service", "instance"}
	checkScopes  = []string{"check"}
)

func moreMetrics(rule apdexRule) []metricDef {
	t4 := rule.sql4()
	req := func(id, label, unit, desc string, agg func(int64) string, op string, th float64) metricDef {
		return metricDef{ID: id, Label: label, Group: "request", Unit: unit, Description: desc, Scopes: requestScopes,
			Op: op, Threshold: th, UpIsBad: true, table: "api_logs", agg: agg, minRequests: 5}
	}
	fixed := func(sql string) func(int64) string { return func(int64) string { return sql } }
	ratio := func(cond string) func(int64) string {
		return fixed("100.0 * " + countIf(cond) + " / NULLIF(COUNT(*), 0)")
	}
	sys := func(id, label, unit, desc, agg string, op string, th float64) metricDef {
		return metricDef{ID: id, Label: label, Group: "system", Unit: unit, Description: desc, Scopes: systemScopes,
			Op: op, Threshold: th, UpIsBad: true, table: "system_metrics", perInstance: true, agg: fixed(agg)}
	}
	errs := func(id, label, desc, agg string, th float64) metricDef {
		return metricDef{ID: id, Label: label, Group: "errors", Unit: "count", Description: desc, Scopes: errorScopes,
			Op: ">", Threshold: th, UpIsBad: true, table: "errors", agg: fixed("COALESCE(" + agg + ", 0)"), zeroFill: true}
	}
	vital := func(name, label string, unit string, th float64) metricDef {
		return metricDef{ID: "web_" + strings.ToLower(name), Label: label, Group: "frontend", Unit: unit,
			Description: "75th percentile of " + name + " from the browser SDK over the window, as Google rates Web Vitals.",
			Scopes:      clientScopes, Op: ">", Threshold: th, UpIsBad: true, table: "client_events",
			filter: "kind = 'web_vital' AND name = '" + name + "'", pct: 0.75, pctCol: "value", minRequests: 5}
	}
	span := func(id, label, kind string, th float64) metricDef {
		return metricDef{ID: id, Label: label, Group: "dependency", Unit: "ms",
			Description: "95th percentile of " + kind + " call time inside requests over the window, from spans.",
			Scopes:      spanScopes, Op: ">", Threshold: th, UpIsBad: true, table: "spans",
			filter: "kind = '" + kind + "'", pct: 0.95, minRequests: 5}
	}
	up := func(id, label, unit, desc, agg, op string, th float64) metricDef {
		return metricDef{ID: id, Label: label, Group: "uptime", Unit: unit, Description: desc, Scopes: checkScopes,
			Op: op, Threshold: th, UpIsBad: op == ">", table: "uptime_results", perInstance: true, keyCol: "check_id", agg: fixed(agg)}
	}

	avg := req("latency_avg", "Average latency", "ms", "Mean server time over the window.", fixed("AVG(duration_ms)"), ">", 500)
	avg.Anomaly = true
	count := req("request_count", "Requests", "count", "Requests in the window. Use < to catch a route that stopped being called.", fixed("COUNT(*)"), "<", 1)
	count.UpIsBad, count.minRequests, count.zeroFill = false, 0, true
	e5 := req("error_count_5xx", "5xx errors", "count", "Requests answered with a 5xx status in the window.", fixed("COALESCE("+countIf("status >= 500")+", 0)"), ">", 10)
	e5.minRequests, e5.zeroFill, e5.Anomaly = 0, true, true
	exc := errs("exceptions", "Exceptions", "Exceptions and panics captured in the window.", "COUNT(*)", 10)
	exc.Anomaly = true
	jsErr := metricDef{ID: "client_errors", Label: "Client errors", Group: "frontend", Unit: "count",
		Description: "Errors the browser and mobile SDKs reported in the window.", Scopes: clientScopes, Op: ">", Threshold: 20,
		UpIsBad: true, table: "client_events", filter: "kind = 'error'", agg: fixed("COUNT(*)"), zeroFill: true, Anomaly: true}

	return []metricDef{
		avg,
		count,
		e5,
		req("rate_limited", "Rate-limited requests", "percent", "Share of requests answered 429 Too Many Requests.", ratio("status = 429"), ">", 5),
		req("frustrated_requests", "Frustrated requests", "percent", "Share of requests slower than 4T (the Apdex frustration point) or failing with a 5xx.",
			ratio("status >= 500 OR duration_ms > "+t4), ">", 10),
		req("db_time_avg", "Database time per request", "ms", "Mean time a request spent in database calls (SDKs that report it).", fixed("AVG(db_ms)"), ">", 200),
		req("db_calls_avg", "Database calls per request", "none", "Mean database calls per request. A jump often means an N+1 query.", fixed("AVG(db_count)"), ">", 20),
		req("outbound_time_avg", "Outbound HTTP time per request", "ms", "Mean time a request spent calling other services.", fixed("AVG(http_ms)"), ">", 500),

		sys("cpu_iowait", "CPU I/O wait", "percent", "CPU time spent waiting for disk I/O (Linux).", "AVG(cpu_iowait_percent)", ">", 20),
		sys("cpu_steal", "CPU steal", "percent", "CPU time the hypervisor gave to other guests: a noisy neighbour.", "AVG(cpu_steal_percent)", ">", 10),
		func() metricDef {
			m := sys("disk_free_gb", "Disk free", "gb", "Least free space on the service's filesystem over the window.", "MIN(disk_free_gb)", "<", 5)
			m.UpIsBad = false
			return m
		}(),
		sys("disk_inodes_percent", "Inodes used", "percent", "Inodes in use on the filesystem. At 100% no file can be created, whatever the free space.", "MAX(disk_inodes_percent)", ">", 90),
		sys("disk_await_ms", "Disk latency", "ms", "Mean time an I/O request waited and was served (Linux).", "AVG(disk_await_ms)", ">", 50),
		sys("proc_cpu", "Process CPU", "percent", "CPU used by the service process itself.", "AVG(proc_cpu_percent)", ">", 90),
		sys("proc_rss_mb", "Process memory", "mb", "Largest resident memory of the service process over the window.", "MAX(proc_rss_mb)", ">", 2048),
		sys("heap_inuse_mb", "Heap in use", "mb", "Largest heap in use over the window. A steady climb is often a leak.", "MAX(heap_inuse_mb)", ">", 1024),
		sys("goroutines", "Goroutines", "count", "Most goroutines at once (Go SDK). A steady climb is often a leak.", "MAX(goroutines)", ">", 10000),
		sys("gc_pause_ms", "GC pause", "ms", "Longest garbage-collection pause over the window.", "MAX(gc_pause_ms)", ">", 100),
		sys("fd_usage_percent", "File descriptors used", "percent", "Open file descriptors as a share of the process limit. At 100% new connections fail.",
			"100.0 * MAX(proc_open_fds) / NULLIF(MAX(proc_max_fds), 0)", ">", 80),
		sys("db_pool_usage", "DB pool in use", "percent", "Connections in use as a share of the pool's maximum.",
			"100.0 * MAX(db_acquired_conns) / NULLIF(MAX(db_max_conns), 0)", ">", 90),
		sys("net_errors", "Network errors and drops", "count", "Interface errors and dropped packets over the window.",
			"SUM(COALESCE(net_errors, 0) + COALESCE(net_drops, 0))", ">", 100),
		sys("tcp_retrans", "TCP retransmits", "count", "Retransmitted TCP segments over the window: packet loss on the path.", "SUM(tcp_retrans)", ">", 1000),
		sys("procs_blocked", "Blocked processes", "count", "Most processes blocked on I/O at once.", "MAX(procs_blocked)", ">", 5),

		exc,
		errs("unhandled_exceptions", "Unhandled exceptions", "Exceptions nothing caught (crashes and panics) in the window.", countIf("handled = 0"), 0),
		{ID: "new_error_types", Label: "New error types", Group: "errors", Unit: "count",
			Description: "Error fingerprints seen in the window for the first time in 30 days: a new bug, often from a deploy.",
			Scopes:      []string{"service"}, Op: ">", Threshold: 0, UpIsBad: true, table: "errors"},

		vital("LCP", "Largest Contentful Paint (p75)", "ms", 4000),
		vital("INP", "Interaction to Next Paint (p75)", "ms", 500),
		vital("CLS", "Cumulative Layout Shift (p75)", "none", 0.25),
		vital("TTFB", "Time to first byte (p75)", "ms", 1800),
		{ID: "page_load_p75", Label: "Page and screen load (p75)", Group: "frontend", Unit: "ms",
			Description: "75th percentile of full page loads (web) and screen loads (mobile) over the window.",
			Scopes:      clientScopes, Op: ">", Threshold: 4000, UpIsBad: true, table: "client_events",
			filter: "kind IN ('page_load', 'screen') AND duration_ms IS NOT NULL", pct: 0.75, minRequests: 5},
		jsErr,
		{ID: "client_api_failures", Label: "Client API failures", Group: "frontend", Unit: "percent",
			Description: "Share of API calls from browsers and apps that failed: a 5xx, or no answer at all.",
			Scopes:      clientScopes, Op: ">", Threshold: 5, UpIsBad: true, table: "client_events", filter: "kind = 'api'",
			agg: ratio("status >= 500 OR status = 0 OR status IS NULL"), minRequests: 5},

		span("db_query_p95", "Database query time (p95)", "db", 500),
		span("outbound_http_p95", "Outbound HTTP call time (p95)", "http", 2000),
		{ID: "dependency_errors", Label: "Failing calls to dependencies", Group: "dependency", Unit: "percent",
			Description: "Share of database and outbound HTTP calls inside requests that failed.",
			Scopes:      spanScopes, Op: ">", Threshold: 5, UpIsBad: true, table: "spans", filter: "kind IN ('db', 'http')",
			agg: ratio("status = 'error'"), minRequests: 5},

		up("uptime_latency", "Uptime response time", "ms", "Mean response time of successful checks over the window. Fires per check.",
			"AVG(CASE WHEN ok = 1 THEN duration_ms END)", ">", 2000),
		up("uptime_availability", "Uptime availability", "percent", "Share of check runs that passed over the window. Fires per check.",
			"100.0 * "+countIf("ok = 1")+" / NULLIF(COUNT(*), 0)", "<", 99),
		{ID: "uptime_cert_days", Label: "TLS certificate expiry", Group: "uptime", Unit: "days",
			Description: "Days until the certificate an HTTPS check last saw expires. Fires per check.",
			Scopes:      checkScopes, Op: "<", Threshold: 14, table: "uptime_results"},
	}
}

// checkNames maps uptime check ids to names, for series keyed by check.
func (s *Server) checkNames(ctx context.Context) map[string]string {
	out := map[string]string{}
	checks, err := s.listUptimeChecks(ctx)
	if err != nil {
		return out
	}
	for _, c := range checks {
		out[strconv.FormatInt(c.ID, 10)] = c.Name
	}
	return out
}

// observeCertExpiry reads each check's latest certificate expiry.
func (s *Server) observeCertExpiry(ctx context.Context, r alertRule, now time.Time) ([]observation, error) {
	args := []any{now.Add(-48 * time.Hour).Format(tsLayout)}
	scope := ""
	if r.Spec.Scope.Check != "" {
		scope, args = " AND check_id = ?", append(args, r.Spec.Scope.Check)
	}
	res, err := s.db.Query(ctx, `SELECT check_id, MAX(check_name), MAX(url), MAX(cert_expires_at) FROM uptime_results
WHERE created_at >= ? AND cert_expires_at IS NOT NULL AND cert_expires_at <> ''`+scope+`
GROUP BY check_id ORDER BY check_id`, args...)
	if err != nil {
		if missingTable(err, "uptime_results") {
			return nil, nil
		}
		return nil, err
	}
	var out []observation
	for _, row := range res.Rows {
		if len(row) != 4 {
			continue
		}
		at, err := time.Parse(tsLayout, toString(row[3]))
		if err != nil {
			if at, err = time.Parse(time.RFC3339, toString(row[3])); err != nil {
				continue
			}
		}
		days := math.Floor(at.Sub(now).Hours()/24*10) / 10
		ob := observation{Key: toString(row[0]), Label: fallback(toString(row[1]), toString(row[0])), Value: ptr(days),
			Threshold: ptr(r.Spec.Threshold), Breach: compare(r.Spec.Op, days, r.Spec.Threshold)}
		when := "expires in " + formatUnit(days, "days")
		if days < 0 {
			when = "expired " + formatUnit(-days, "days") + " ago"
		}
		ob.Message = fmt.Sprintf("The certificate of %s (%s) %s, on %s UTC (threshold %s %s).", ob.Label, toString(row[2]), when,
			at.UTC().Format("Jan 2 2006"), r.Spec.Op, formatUnit(r.Spec.Threshold, "days"))
		out = append(out, ob)
	}
	return out, nil
}

// observeNewErrors counts fingerprints first seen in the window, looking 30
// days back.
func (s *Server) observeNewErrors(ctx context.Context, r alertRule, now time.Time) ([]observation, error) {
	w := specWindow(r.Spec)
	from := now.Add(-w).UTC().Format(tsLayout)
	args := []any{from, now.UTC().Format(tsLayout)}
	scope := ""
	if r.Spec.Scope.Service != "" {
		scope, args = " AND e.service = ?", append(args, r.Spec.Scope.Service)
	}
	args = append(args, now.Add(-30*24*time.Hour).UTC().Format(tsLayout), from)
	res, err := s.db.Query(ctx, `SELECT e.fingerprint, MIN(e.type), MIN(e.message), COUNT(*) FROM errors e
WHERE e.created_at >= ? AND e.created_at < ?`+scope+`
  AND NOT EXISTS (SELECT 1 FROM errors p WHERE p.fingerprint = e.fingerprint AND p.created_at >= ? AND p.created_at < ?)
GROUP BY e.fingerprint ORDER BY COUNT(*) DESC LIMIT 20`, args...)
	if err != nil {
		return nil, err
	}
	v := float64(len(res.Rows))
	ob := observation{Value: &v, Threshold: ptr(r.Spec.Threshold), Breach: compare(r.Spec.Op, v, r.Spec.Threshold)}
	var names []string
	for _, row := range head(res.Rows, 3) {
		if len(row) == 4 {
			names = append(names, fmt.Sprintf("%s: %s (%d×)", toString(row[1]), truncate(toString(row[2]), 80), toInt(row[3])))
		}
	}
	ob.Message = fmt.Sprintf("%s new error type(s) over the last %s (threshold %s %s).", formatUnit(v, "count"), fallback(r.Spec.Window, "5m"),
		r.Spec.Op, formatUnit(r.Spec.Threshold, "count"))
	if len(names) > 0 {
		ob.Message += " " + strings.Join(names, "; ")
		if len(res.Rows) > 3 {
			ob.Message += "; …"
		}
		ob.Message += "."
	}
	return []observation{ob}, nil
}
