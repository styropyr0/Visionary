package analytics

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Built-in metrics a rule can watch. Request metrics read api_logs and can be
// narrowed to a route, method or service; system metrics read system_metrics
// and are evaluated per instance. Liveness and uptime are computed from the
// latest rows rather than an aggregate.

type metricDef struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Group       string   `json:"group"` // request, system, liveness, uptime
	Unit        string   `json:"unit"`  // ms, percent, rpm, score, minutes, count, none
	Description string   `json:"description"`
	Scopes      []string `json:"scopes"`
	Anomaly     bool     `json:"anomaly"`
	// Op and Threshold prefill a new rule.
	Op        string  `json:"op"`
	Threshold float64 `json:"threshold"`
	// UpIsBad picks the default anomaly direction.
	UpIsBad bool `json:"up_is_bad"`

	table       string
	perInstance bool
	keyCol      string // the column a grouped metric fans out by; empty is instance
	pct         float64                      // percentile of duration_ms, 0 when not one
	agg         func(windowSec int64) string // aggregate over the rows in a window
	minRequests int64                        // fewer rows than this is "no data"
}

var requestScopes = []string{"route", "method", "service"}
var systemScopes = []string{"instance", "service"}

// routeNote mentions the routes with their own Apdex threshold.
func routeNote(rule apdexRule) string {
	if len(rule.Routes) == 0 {
		return ""
	}
	return " (" + strconv.Itoa(len(rule.Routes)) + " routes have their own T)"
}

func alertMetrics(rule apdexRule) []metricDef {
	t := rule.sql()
	t4 := rule.sql4()
	sys := func(id, label, unit, desc, agg string, op string, th float64) metricDef {
		return metricDef{ID: id, Label: label, Group: "system", Unit: unit, Description: desc, Scopes: systemScopes,
			Op: op, Threshold: th, UpIsBad: true, table: "system_metrics", perInstance: true,
			agg: func(int64) string { return agg }}
	}
	req := func(id, label, unit, desc string, agg func(int64) string, op string, th float64) metricDef {
		return metricDef{ID: id, Label: label, Group: "request", Unit: unit, Description: desc, Scopes: requestScopes,
			Op: op, Threshold: th, UpIsBad: true, table: "api_logs", agg: agg, minRequests: 5}
	}
	pctl := func(id, label string, p float64, th float64) metricDef {
		m := req(id, label, "ms", fmt.Sprintf("%.0fth percentile of server time over the window.", p*100), nil, ">", th)
		m.pct = p
		return m
	}
	ratio := func(cond string) func(int64) string {
		return func(int64) string { return "100.0 * " + countIf(cond) + " / NULLIF(COUNT(*), 0)" }
	}

	p95 := pctl("latency_p95", "p95 latency", 0.95, 1000)
	p95.Anomaly = true
	e5 := req("error_rate_5xx", "5xx error rate", "percent", "Share of requests answered with a 5xx status.", ratio("status >= 500"), ">", 2)
	e5.Anomaly = true
	rpm := req("requests_per_min", "Requests per minute", "rpm", "Requests per minute over the window. Use < to catch traffic drops.",
		func(w int64) string { return "COUNT(*) * 60.0 / " + strconv.FormatInt(w, 10) }, "<", 1)
	rpm.Anomaly, rpm.UpIsBad, rpm.minRequests = true, false, 0
	apdex := req("apdex", "Apdex", "score", "Apdex with T = "+strconv.Itoa(rule.DefaultMs)+" ms"+routeNote(rule)+": satisfied within T, tolerating within 4T. 1 is perfect.",
		func(int64) string {
			return "SUM(CASE WHEN status < 500 AND duration_ms <= " + t + " THEN 1.0 WHEN status < 500 AND duration_ms <= " + t4 + " THEN 0.5 ELSE 0 END) / NULLIF(COUNT(*), 0)"
		}, "<", 0.85)
	apdex.UpIsBad = false
	cpu := sys("host_cpu", "Host CPU", "percent", "Average whole-machine CPU use over the window.", "AVG(host_cpu_percent)", ">", 85)
	cpu.Anomaly = true

	return []metricDef{
		pctl("latency_p50", "p50 latency", 0.50, 500),
		p95,
		pctl("latency_p99", "p99 latency", 0.99, 2500),
		e5,
		req("error_rate_4xx", "4xx error rate", "percent", "Share of requests answered with a 4xx status.", ratio("status >= 400 AND status < 500"), ">", 10),
		rpm,
		apdex,
		cpu,
		sys("memory_percent", "Memory used", "percent", "Average host memory in use.", "AVG(host_mem_percent)", ">", 90),
		sys("disk_used_percent", "Disk used", "percent", "Space used on the service's filesystem.", "MAX(disk_used_percent)", ">", 90),
		sys("disk_busy_percent", "Disk busy", "percent", "Time the busiest disk spent on I/O (Linux).", "AVG(disk_busy_percent)", ">", 90),
		sys("load_per_core", "Load per core", "none", "1-minute load average per CPU. Above 1 means work is queuing.", "AVG(load_1 / NULLIF(cpu_count, 0))", ">", 1.5),
		sys("swap_used_percent", "Swap used", "percent", "Swap in use as a share of swap size.", "100.0 * MAX(swap_used_mb) / NULLIF(MAX(swap_total_mb), 0)", ">", 50),
		sys("db_pool_waits", "DB pool waits", "count", "Connection acquires that had to wait for a free connection, summed over the window.", "SUM(db_empty_acquires)", ">", 10),
		sys("db_wait_ms", "DB pool wait time", "ms", "Total time spent waiting for a database connection over the window.", "SUM(db_acquire_wait_ms)", ">", 1000),
		sys("in_flight", "Requests in flight", "count", "Most requests in progress at once.", "MAX(http_in_flight)", ">", 100),
		{ID: "instance_silent", Label: "Instance stopped reporting", Group: "liveness", Unit: "minutes",
			Description: "Minutes since an instance (or a host agent) last wrote a system_metrics row. Fires per instance.",
			Scopes:      systemScopes, Op: ">", Threshold: 5, UpIsBad: true},
		{ID: "uptime_failing", Label: "Uptime check failing", Group: "uptime", Unit: "count",
			Description: "Consecutive failed results of an uptime check, newest first. Fires per check.",
			Scopes:      []string{"check"}, Op: ">=", Threshold: 2, UpIsBad: true},
	}
}

func (s *Server) metricByID(id string) (metricDef, bool) {
	for _, m := range s.allMetrics() {
		if m.ID == id {
			return m, true
		}
	}
	return metricDef{}, false
}

// timeSpan is a half-open range of created_at.
type timeSpan struct{ From, To time.Time }

// scopeClause narrows a metric to the rule's scope with bound parameters.
func scopeClause(m metricDef, sc alertScope) (string, []any) {
	var b strings.Builder
	var args []any
	add := func(name, col, v string) {
		if v == "" || !hasScope(m, name) {
			return
		}
		b.WriteString(" AND " + col + " = ?")
		args = append(args, v)
	}
	add("route", "route", sc.Route)
	add("method", "method", sc.Method)
	add("instance", "instance", sc.Instance)
	add("service", "service", sc.Service)
	add("container", "container", sc.Container)
	add("host", "host", sc.Host)
	return b.String(), args
}

func hasScope(m metricDef, name string) bool {
	for _, s := range m.Scopes {
		if s == name {
			return true
		}
	}
	return false
}

func spansClause(spans []timeSpan) (string, []any) {
	parts := make([]string, len(spans))
	args := make([]any, 0, 2*len(spans))
	for i, sp := range spans {
		parts[i] = "(created_at >= ? AND created_at < ?)"
		args = append(args, sp.From.UTC().Format(tsLayout), sp.To.UTC().Format(tsLayout))
	}
	if len(parts) == 1 {
		return parts[0], args
	}
	return "(" + strings.Join(parts, " OR ") + ")", args
}

// valueSQL is the metric over one window: rows (k, value, n), one per
// instance when grouped, else one row with an empty k.
func (s *Server) valueSQL(m metricDef, sc alertScope, span timeSpan, grouped bool) (string, []any) {
	where, args := spansClause([]timeSpan{span})
	scope, sargs := scopeClause(m, sc)
	args = append(args, sargs...)
	if m.pct > 0 {
		return `WITH o AS (
  SELECT duration_ms, ROW_NUMBER() OVER (ORDER BY duration_ms) AS rn, COUNT(*) OVER () AS n
  FROM api_logs WHERE ` + where + scope + `
)
SELECT '' AS k, MIN(CASE WHEN rn >= ` + fnum(m.pct) + ` * n THEN duration_ms END) AS value, COUNT(*) AS n FROM o`, args
	}
	agg := m.agg(int64(span.To.Sub(span.From).Seconds()))
	if grouped {
		key := m.groupKey()
		return `SELECT ` + key + ` AS k, ` + agg + ` AS value, COUNT(*) AS n FROM ` + m.table + ` WHERE ` + where + scope + `
GROUP BY ` + key + ` ORDER BY ` + key, args
	}
	return `SELECT '' AS k, ` + agg + ` AS value, COUNT(*) AS n FROM ` + m.table + ` WHERE ` + where + scope, args
}

// seriesSQL is the metric per bucket over spans: rows (t, series, value, n).
// series is the instance when grouped, else empty.
func (s *Server) seriesSQL(m metricDef, sc alertScope, spans []timeSpan, bucket int64, grouped bool) (string, []any) {
	where, args := spansClause(spans)
	scope, sargs := scopeClause(m, sc)
	args = append(args, sargs...)
	t := s.q.bucketOf(strconv.FormatInt(bucket, 10))
	if m.pct > 0 {
		return `WITH b AS (
  SELECT ` + t + ` AS t, duration_ms FROM api_logs WHERE ` + where + scope + `
),
r AS (
  SELECT t, duration_ms, ROW_NUMBER() OVER (PARTITION BY t ORDER BY duration_ms) AS rn, COUNT(*) OVER (PARTITION BY t) AS n FROM b
)
SELECT t, '' AS series, MIN(CASE WHEN rn >= ` + fnum(m.pct) + ` * n THEN duration_ms END) AS value, COUNT(*) AS n
FROM r GROUP BY t ORDER BY t`, args
	}
	agg := m.agg(bucket)
	if grouped {
		key := m.groupKey()
		return `SELECT ` + t + ` AS t, ` + key + ` AS series, ` + agg + ` AS value, COUNT(*) AS n FROM ` + m.table + ` WHERE ` + where + scope + `
GROUP BY ` + t + `, ` + key + ` ORDER BY t`, args
	}
	return `SELECT ` + t + ` AS t, '' AS series, ` + agg + ` AS value, COUNT(*) AS n FROM ` + m.table + ` WHERE ` + where + scope + `
GROUP BY ` + t + ` ORDER BY t`, args
}

// groupKey is the column a grouped metric fans out by.
func (m metricDef) groupKey() string {
	if m.keyCol != "" {
		return m.keyCol
	}
	return "instance"
}

func fnum(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// point is one bucket of a metric series.
type point struct {
	T      int64
	Series string
	Value  float64
	N      int64
}

func (s *Server) metricSeries(ctx context.Context, m metricDef, sc alertScope, spans []timeSpan, bucket int64, grouped bool) ([]point, error) {
	sql, args := s.seriesSQL(m, sc, spans, bucket, grouped)
	res, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	out := make([]point, 0, len(res.Rows))
	for _, row := range res.Rows {
		if len(row) != 4 {
			continue
		}
		v, ok := cellFloat(row[2])
		n := toInt(row[3])
		if !ok || n < m.minRequests {
			continue
		}
		out = append(out, point{T: toInt(row[0]), Series: toString(row[1]), Value: v, N: n})
	}
	return out, nil
}

// observation is one series' reading at evaluation time.
type observation struct {
	Key       string   `json:"key"`
	Label     string   `json:"label"`
	Value     *float64 `json:"value"`
	Threshold *float64 `json:"threshold"`
	Breach    bool     `json:"breach"`
	Message   string   `json:"message"`
}

// observe evaluates a rule at now.
func (s *Server) observe(ctx context.Context, r alertRule, now time.Time) ([]observation, error) {
	switch r.Kind {
	case "anomaly":
		return s.observeAnomaly(ctx, r, now)
	case "slo":
		return s.observeSLO(ctx, r, now)
	}
	switch r.Spec.Metric {
	case "custom":
		return s.observeCustom(ctx, r, now)
	case "instance_silent":
		return s.observeLiveness(ctx, r, now)
	case "uptime_failing":
		return s.observeUptime(ctx, r, now)
	case "security_events":
		return s.observeSecurity(ctx, r, now)
	case "container_events":
		return s.observeContainerEvents(ctx, r, now)
	}
	m, ok := s.metricByID(r.Spec.Metric)
	if !ok {
		return nil, errors.New("unknown metric " + r.Spec.Metric)
	}
	w := specWindow(r.Spec)
	sql, args := s.valueSQL(m, r.Spec.Scope, timeSpan{now.Add(-w), now}, m.perInstance)
	res, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		if m.table != "api_logs" && missingTable(err, m.table) {
			return nil, nil
		}
		return nil, err
	}
	var out []observation
	for _, row := range res.Rows {
		if len(row) != 3 {
			continue
		}
		key := toString(row[0])
		ob := observation{Key: key, Label: key, Threshold: ptr(r.Spec.Threshold)}
		if v, ok := cellFloat(row[1]); ok && toInt(row[2]) >= m.minRequests {
			ob.Value = ptr(v)
			ob.Breach = compare(r.Spec.Op, v, r.Spec.Threshold)
		}
		ob.Message = thresholdMessage(m.Label, m.Unit, ob, r.Spec, describeScope(r.Spec.Scope, key))
		out = append(out, ob)
	}
	return out, nil
}

// observeCustom runs the rule's own read-only SQL over the window. It
// returns one number, or (t, value) rows of which the latest counts.
func (s *Server) observeCustom(ctx context.Context, r alertRule, now time.Time) ([]observation, error) {
	w := specWindow(r.Spec)
	res, err := s.runTemplate(ctx, r.Spec.SQL, timeRange{From: now.Add(-w), To: now, Bucket: int64(w.Seconds())}, false)
	if err != nil {
		return nil, err
	}
	ob := observation{Threshold: ptr(r.Spec.Threshold)}
	if v, ok := customValue(res); ok {
		ob.Value = ptr(v)
		ob.Breach = compare(r.Spec.Op, v, r.Spec.Threshold)
	}
	ob.Message = thresholdMessage("Custom query", "none", ob, r.Spec, "")
	return []observation{ob}, nil
}

// customValue reads a custom query's number: the "value" column (or the only
// column) of the row with the latest t.
func customValue(res *Result) (float64, bool) {
	if len(res.Rows) == 0 || len(res.Columns) == 0 {
		return 0, false
	}
	vi, ti := -1, -1
	for i, c := range res.Columns {
		switch strings.ToLower(c) {
		case "value":
			vi = i
		case "t":
			ti = i
		}
	}
	if vi < 0 {
		if len(res.Columns) != 1 && ti < 0 {
			return 0, false
		}
		vi = len(res.Columns) - 1
	}
	row := res.Rows[len(res.Rows)-1]
	if ti >= 0 {
		best := -1.0
		for _, r := range res.Rows {
			if t, ok := cellFloat(r[ti]); ok && t >= best {
				best, row = t, r
			}
		}
	}
	return cellFloat(row[vi])
}

// instanceSeen is an instance that reported within the last day.
type instanceSeen struct {
	Instance, Service string
}

// observeLiveness reports minutes since each instance's last sample. The
// instance list is refreshed every ten minutes; each check is then an indexed
// MAX per instance, so it stays cheap on D1.
func (s *Server) observeLiveness(ctx context.Context, r alertRule, now time.Time) ([]observation, error) {
	known, err := s.alertEngine().knownInstances(ctx, now)
	if err != nil {
		if missingTable(err, "system_metrics") {
			return nil, nil
		}
		return nil, err
	}
	// Instances already firing stay watched after they drop out of the list.
	prev, _ := s.ruleStates(ctx, r.ID)
	names := map[string]bool{}
	for _, in := range known {
		if r.Spec.Scope.Service != "" && in.Service != r.Spec.Scope.Service {
			continue
		}
		names[in.Instance] = true
	}
	for _, st := range prev[r.ID] {
		if st.State == "firing" || st.State == "pending" {
			names[st.Key] = true
		}
	}
	if r.Spec.Scope.Instance != "" {
		names = map[string]bool{r.Spec.Scope.Instance: true}
	}
	list := make([]string, 0, len(names))
	for n := range names {
		list = append(list, n)
	}
	sort.Strings(list)
	out := make([]observation, 0, len(list))
	for _, inst := range head(list, 100) {
		res, err := s.db.Query(ctx, `SELECT MAX(created_at) FROM system_metrics WHERE instance = ?`, inst)
		if err != nil {
			return nil, err
		}
		ob := observation{Key: inst, Label: inst, Threshold: ptr(r.Spec.Threshold)}
		if len(res.Rows) == 1 {
			if last, err := time.Parse(tsLayout, toString(res.Rows[0][0])); err == nil {
				mins := math.Max(0, now.Sub(last).Minutes())
				ob.Value = ptr(math.Round(mins*10) / 10)
				ob.Breach = compare(r.Spec.Op, mins, r.Spec.Threshold)
				ob.Message = fmt.Sprintf("%s last reported %s ago (at %s UTC); threshold %s %s.", inst,
					humanDuration(now.Sub(last)), last.Format("Jan 2 15:04"), r.Spec.Op, formatUnit(r.Spec.Threshold, "minutes"))
			}
		}
		if ob.Message == "" {
			ob.Message = inst + " has no samples."
		}
		out = append(out, ob)
	}
	return out, nil
}

// observeUptime reads uptime_results (written by the uptime checker) and
// counts each check's consecutive failures, newest first.
func (s *Server) observeUptime(ctx context.Context, r alertRule, now time.Time) ([]observation, error) {
	w := specWindow(r.Spec)
	res, err := s.db.Query(ctx, `SELECT check_id, check_name, url, ok, status, error, created_at FROM uptime_results
WHERE created_at >= ? ORDER BY created_at DESC LIMIT 5000`, now.Add(-w).UTC().Format(tsLayout))
	if err != nil {
		if missingTable(err, "uptime_results") {
			return nil, nil
		}
		return nil, err
	}
	type check struct {
		name, url, status, errText string
		fails                      int
		broken                     bool
		total                      int
	}
	checks := map[string]*check{}
	var order []string
	for _, row := range res.Rows {
		if len(row) != 7 {
			continue
		}
		id := toString(row[0])
		if r.Spec.Scope.Check != "" && id != r.Spec.Scope.Check {
			continue
		}
		c := checks[id]
		if c == nil {
			c = &check{name: toString(row[1]), url: toString(row[2]), status: toString(row[4]), errText: toString(row[5])}
			checks[id] = c
			order = append(order, id)
		}
		c.total++
		if c.broken {
			continue
		}
		if toInt(row[3]) == 1 {
			c.broken = true
			continue
		}
		c.fails++
	}
	out := make([]observation, 0, len(order))
	for _, id := range order {
		c := checks[id]
		v := float64(c.fails)
		ob := observation{Key: id, Label: fallback(c.name, id), Value: &v, Threshold: ptr(r.Spec.Threshold),
			Breach: compare(r.Spec.Op, v, r.Spec.Threshold)}
		detail := ""
		if c.fails > 0 {
			detail = "status " + fallback(c.status, "none")
			if c.errText != "" {
				detail += ", " + truncate(c.errText, 160)
			}
		}
		if c.fails == 0 {
			ob.Message = fmt.Sprintf("%s (%s) is passing.", ob.Label, c.url)
		} else {
			ob.Message = fmt.Sprintf("%s (%s) failed its last %d check(s): %s.", ob.Label, c.url, c.fails, detail)
		}
		out = append(out, ob)
	}
	return out, nil
}

func compare(op string, v, t float64) bool {
	switch op {
	case ">":
		return v > t
	case "<":
		return v < t
	case ">=":
		return v >= t
	case "<=":
		return v <= t
	}
	return false
}

func thresholdMessage(label, unit string, ob observation, sp ruleSpec, where string) string {
	if ob.Value == nil {
		return label + where + ": no data in the last " + sp.Window + "."
	}
	return fmt.Sprintf("%s%s is %s over the last %s (threshold %s %s).", label, where, formatUnit(*ob.Value, unit),
		fallback(sp.Window, "5m"), sp.Op, formatUnit(sp.Threshold, unit))
}

func describeScope(sc alertScope, key string) string {
	var parts []string
	if sc.Method != "" || sc.Route != "" {
		parts = append(parts, strings.TrimSpace(sc.Method+" "+sc.Route))
	}
	if sc.Service != "" {
		parts = append(parts, "service "+sc.Service)
	}
	if key != "" {
		parts = append(parts, key)
	} else if sc.Instance != "" {
		parts = append(parts, sc.Instance)
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

// formatUnit writes a value the way the UI does.
func formatUnit(v float64, unit string) string {
	switch unit {
	case "ms":
		if math.Abs(v) >= 1000 {
			return strconv.FormatFloat(v/1000, 'f', 2, 64) + " s"
		}
		return strconv.FormatFloat(v, 'f', 0, 64) + " ms"
	case "percent":
		return strconv.FormatFloat(v, 'f', 2, 64) + "%"
	case "rpm":
		return strconv.FormatFloat(v, 'f', 1, 64) + "/min"
	case "score":
		return strconv.FormatFloat(v, 'f', 2, 64)
	case "minutes":
		return strconv.FormatFloat(v, 'f', 0, 64) + " min"
	case "burn":
		return strconv.FormatFloat(v, 'f', 1, 64) + "×"
	case "count":
		return strconv.FormatFloat(v, 'f', 0, 64)
	}
	return strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64)
}

func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return strconv.Itoa(int(d.Hours()/24)) + "d"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// parseSpan reads durations like 30s, 5m, 2h or 1d.
func parseSpan(v string) (time.Duration, error) {
	v = strings.TrimSpace(v)
	if v == "" || v == "0" {
		return 0, nil
	}
	if strings.HasSuffix(v, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(v, "d"))
		if err != nil || n < 0 {
			return 0, errors.New("invalid duration " + v)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return 0, errors.New("invalid duration " + v)
	}
	return d, nil
}

func specWindow(sp ruleSpec) time.Duration {
	if d, err := parseSpan(sp.Window); err == nil && d >= time.Minute {
		return d
	}
	return 5 * time.Minute
}
