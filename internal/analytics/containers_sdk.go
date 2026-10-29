package analytics

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Containers the SDKs report: a service in a container (Railway, Render,
// Kubernetes, Cloud Run, ...) fills the container_* columns of its own
// system_metrics rows from its cgroup. Where no agent can reach a container
// engine, these are the only containers there are, so the Containers view
// lists them next to the agent's, unless an agent already lists the same
// container, with source "sdk": host is the instance
// and name the service. State, health, restarts, ports and I/O need the
// engine and stay empty; OOM kills come from container_oom_kills.

// sdkContainerWindow is how far back an instance still counts: one that
// stopped reporting stays listed as stopped for this long, unless a newer
// instance of the same service replaced it (a redeploy). Running replicas
// are all listed.
const sdkContainerWindow = time.Hour

// sdkCPUPercent and sdkMemPercent are of the container's limit, or of the
// host's CPUs and memory without one, as the agent counts them.
const (
	sdkCPUPercent = "container_cpu_cores * 100.0 / COALESCE(container_cpu_limit, NULLIF(cpu_count, 0))"
	sdkMemPercent = "container_mem_used_mb * 100.0 / COALESCE(container_mem_limit_mb, NULLIF(host_mem_total_mb, 0))"
	sdkSeries     = "COALESCE(service, instance)"
)

// agentCovers tells whether an agent already lists the container behind an
// SDK instance: the instance is the container's name, or its hostname, which
// Docker sets to the first 12 characters of the container ID.
func (s *Server) agentCovers(ctx context.Context) func(instance string) bool {
	res, err := s.db.Query(ctx, `
SELECT c.container_id, c.name
FROM host_containers AS c
JOIN (SELECT host, MAX(created_at) AS at FROM host_containers WHERE created_at >= ? GROUP BY host) AS l
  ON l.host = c.host AND l.at = c.created_at
LIMIT 2000`, time.Now().UTC().Add(-24*time.Hour).Format(tsLayout))
	if err != nil {
		return func(string) bool { return false }
	}
	ids, names := make([]string, 0, len(res.Rows)), map[string]bool{}
	for _, row := range res.Rows {
		ids = append(ids, toString(row[0]))
		names[toString(row[1])] = true
	}
	return func(instance string) bool {
		if names[instance] {
			return true
		}
		if len(instance) < 12 {
			return false
		}
		for _, id := range ids {
			if strings.HasPrefix(id, instance) {
				return true
			}
		}
		return false
	}
}

// sdkCoveredClause leaves out the SDK instances an agent already covers, for
// the charts and events that list agents and SDKs together.
func (s *Server) sdkCoveredClause(ctx context.Context, tr timeRange) string {
	res, err := s.db.Query(ctx, `SELECT DISTINCT instance FROM system_metrics
WHERE created_at >= ? AND created_at < ? AND (container_mem_used_mb IS NOT NULL OR container_oom_kills > 0) LIMIT 500`,
		tr.From.Format(tsLayout), tr.To.Format(tsLayout))
	if err != nil {
		return ""
	}
	covers := s.agentCovers(ctx)
	var names []string
	for _, row := range res.Rows {
		if n := toString(row[0]); instanceName.MatchString(n) && covers(n) {
			names = append(names, "'"+n+"'")
		}
	}
	if len(names) == 0 {
		return ""
	}
	return " AND instance NOT IN (" + strings.Join(names, ",") + ")"
}

// sdkContainers is one list row per instance reporting a container, in
// containerListColumns order plus source. An instance an agent already lists
// is left out: the SDK is the fallback where there is no agent.
func (s *Server) sdkContainers(ctx context.Context, tr timeRange) [][]any {
	now := time.Now().UTC()
	covered := s.agentCovers(ctx)
	snap, err := s.db.Query(ctx, `
SELECT m.instance, COALESCE(m.service, m.instance), m.created_at, m.interval_s, m.uptime_s, m.container_cpu_cores, m.container_cpu_limit, m.cpu_count,
  m.container_mem_used_mb, m.container_mem_limit_mb, m.host_mem_total_mb
FROM system_metrics AS m
JOIN (SELECT instance, MAX(created_at) AS at FROM system_metrics WHERE created_at >= ? AND container_mem_used_mb IS NOT NULL GROUP BY instance) AS l
  ON l.instance = m.instance AND l.at = m.created_at
WHERE m.container_mem_used_mb IS NOT NULL
LIMIT 500`, now.Add(-sdkContainerWindow).Format(tsLayout))
	if err != nil {
		s.quietSDK(ctx, err)
		return nil
	}
	ooms := map[string]int64{}
	if res, err := s.db.Query(ctx, `SELECT instance, SUM(container_oom_kills) FROM system_metrics
WHERE created_at >= ? AND created_at < ? AND container_oom_kills > 0 GROUP BY instance`,
		tr.From.Format(tsLayout), tr.To.Format(tsLayout)); err == nil {
		for _, row := range res.Rows {
			ooms[toString(row[0])] = toInt(row[1])
		}
	}

	type ctr struct {
		row     []any
		service string
		seen    time.Time
		running bool
	}
	var all []ctr
	newest := map[string]time.Time{} // the latest instance per service
	for _, r := range snap.Rows {
		seen, err := time.Parse(time.RFC3339Nano, toString(r[2]))
		if err != nil {
			continue
		}
		interval, _ := cellFloat(r[3])
		running := now.Sub(seen) <= max(2*time.Minute, time.Duration(3*interval*float64(time.Second)))
		var started, finished any
		if up, ok := cellFloat(r[4]); ok {
			started = seen.Add(-time.Duration(up * float64(time.Second))).Format(tsLayout)
		}
		state := "running"
		if !running {
			state, finished = "stopped", toString(r[2])
		}
		cores, limit := r[5], r[6]
		cpuPct := percentOf(cores, limit, r[7])
		memLimit := r[9]
		if memLimit == nil {
			memLimit = r[10]
		}
		inst := toString(r[0])
		if covered(inst) {
			continue
		}
		events := ooms[inst]
		row := []any{
			inst, r[1], nil, nil, state, nil, nil, nil, nil, started, finished,
			cpuPct, cores, limit, r[8], memLimit, percentOf(r[8], memLimit, nil), nil, nil,
			events, events, toString(r[2]), "sdk",
		}
		svc := toString(r[1])
		all = append(all, ctr{row, svc, seen, running})
		if seen.After(newest[svc]) {
			newest[svc] = seen
		}
	}
	var out [][]any
	for _, c := range all {
		if !c.running && c.seen.Before(newest[c.service]) {
			continue // replaced by a newer instance of the service
		}
		out = append(out, c.row)
	}
	sort.SliceStable(out, func(i, j int) bool { return toString(out[i][1]) < toString(out[j][1]) })
	return out
}

// percentOf is v / limit (or / fallback without one) × 100, one decimal; nil
// when either is unknown.
func percentOf(v, limit, fallback any) any {
	n, ok := cellFloat(v)
	if !ok {
		return nil
	}
	d, ok := cellFloat(limit)
	if !ok || d <= 0 {
		if d, ok = cellFloat(fallback); !ok || d <= 0 {
			return nil
		}
	}
	return finite(100*n/d, 1)
}

// quietSDK logs a failed system_metrics read unless the table or the
// container columns are not there yet (no SDK has written, or an old one).
func (s *Server) quietSDK(ctx context.Context, err error) {
	if ctx.Err() != nil || missingTable(err, "system_metrics") || strings.Contains(strings.ToLower(err.Error()), "column") {
		return
	}
	s.logger.Warn("sdk containers", "err", err)
}

// sdkFilter narrows system_metrics to the host (instance) parameter; the
// service name is never inlined, the instance is enough.
func sdkFilter(r *http.Request) (string, error) {
	return instanceClause(r.URL.Query().Get("host"))
}

const sdkWindow = `created_at >= {{from}} AND created_at < {{to}}{{filter}}`

// sdkContainerPanels are the containerPanels the SDK's columns can fill.
func sdkContainerPanels(q sqlDialect) map[string]containerPanel {
	perContainer := func(expr, col string) string {
		return `SELECT {{t}} AS t, ` + sdkSeries + ` AS series, ` + expr + ` AS value
FROM system_metrics WHERE ` + sdkWindow + ` AND ` + col + ` IS NOT NULL
GROUP BY {{t}}, ` + sdkSeries + ` ORDER BY t`
	}
	one := func(cols, col string) string {
		return `SELECT {{t}} AS t, ` + cols + `
FROM system_metrics WHERE ` + sdkWindow + ` AND ` + col + ` IS NOT NULL
GROUP BY t ORDER BY t`
	}
	return map[string]containerPanel{
		"cpu": {perContainer(q.round("AVG("+sdkCPUPercent+")", 1), "container_cpu_cores"),
			one(q.round("AVG("+sdkCPUPercent+")", 1)+` AS avg_cpu, `+q.round("MAX("+sdkCPUPercent+")", 1)+` AS peak_cpu`, "container_cpu_cores")},
		"memory": {perContainer(q.round("AVG(container_mem_used_mb) * 1048576.0", 0), "container_mem_used_mb"),
			one(q.round("AVG(container_mem_used_mb) * 1048576.0", 0)+` AS used, `+
				q.round("MAX(COALESCE(container_mem_limit_mb, host_mem_total_mb)) * 1048576.0", 0)+` AS limit_bytes`, "container_mem_used_mb")},
		"memory_percent": {perContainer(q.round("MAX("+sdkMemPercent+")", 1), "container_mem_used_mb"),
			one(q.round("AVG("+sdkMemPercent+")", 1)+` AS avg_used, `+q.round("MAX("+sdkMemPercent+")", 1)+` AS peak_used`, "container_mem_used_mb")},
		"throttled": {perContainer(q.round("AVG(container_cpu_throttled_percent)", 1), "container_cpu_throttled_percent"),
			one(q.round("AVG(container_cpu_throttled_percent)", 1)+` AS throttled`, "container_cpu_throttled_percent")},
	}
}

// sdkContainerPanel runs an SDK panel; nil when the SDK has no such panel or
// the columns are not there.
func (s *Server) sdkContainerPanel(r *http.Request, panel string, one bool, tr timeRange) (*Result, error) {
	p, ok := sdkContainerPanels(s.q)[panel]
	if !ok {
		return nil, nil
	}
	filter, err := sdkFilter(r)
	if err != nil {
		return nil, err
	}
	if r.URL.Query().Get("source") != "sdk" {
		filter += s.sdkCoveredClause(r.Context(), tr)
	}
	sql := p.all
	if one {
		sql = p.one
	}
	res, err := s.execTemplate(r.Context(), strings.ReplaceAll(sql, "{{filter}}", filter), tr, true)
	if err != nil {
		s.quietSDK(r.Context(), err)
		return nil, nil
	}
	return res, nil
}

// sdkContainerEvents are the OOM kills the SDKs counted, as container_events
// rows: one per sample that saw any.
func (s *Server) sdkContainerEvents(r *http.Request, tr timeRange) ([][]any, error) {
	filter, err := sdkFilter(r)
	if err != nil {
		return nil, err
	}
	if r.URL.Query().Get("source") != "sdk" {
		filter += s.sdkCoveredClause(r.Context(), tr)
	}
	res, err := s.db.Query(r.Context(), `SELECT created_at, instance, `+sdkSeries+`, container_oom_kills FROM system_metrics
WHERE created_at >= ? AND created_at < ? AND container_oom_kills > 0`+filter+` ORDER BY created_at DESC LIMIT 500`,
		tr.From.Format(tsLayout), tr.To.Format(tsLayout))
	if err != nil {
		s.quietSDK(r.Context(), err)
		return nil, nil
	}
	out := make([][]any, 0, len(res.Rows))
	for _, row := range res.Rows {
		out = append(out, []any{row[0], row[1], row[2], nil, "oom_killed", "critical", nil, "Reported by the SDK from the container's cgroup", row[3]})
	}
	return out, nil
}

// mergeByTime appends b's rows to a (same columns) in time order; desc for
// newest first. Times are epoch buckets or timestamps.
func mergeByTime(a *Result, b [][]any, desc bool) {
	if len(b) == 0 {
		return
	}
	a.Rows = append(a.Rows, b...)
	less := func(x, y any) bool {
		fx, okx := asFloat(x)
		fy, oky := asFloat(y)
		if okx && oky {
			return fx < fy
		}
		return toString(x) < toString(y)
	}
	sort.SliceStable(a.Rows, func(i, j int) bool {
		if desc {
			return less(a.Rows[j][0], a.Rows[i][0])
		}
		return less(a.Rows[i][0], a.Rows[j][0])
	})
}
