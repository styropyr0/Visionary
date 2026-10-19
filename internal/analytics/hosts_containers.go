package analytics

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/styropyr0/visionary/internal/agentapi"
)

// Containers: the host agent reads the Docker (or Podman) API and reports
// each container's state, a sample per running container with every host
// sample, and the restarts, OOM kills, exits and health changes since its
// previous report. They land in host_containers, container_metrics and
// container_events (hosts_schema.go) and fill the Containers view.

var (
	containerTable = &otlpTable{"host_containers", []string{"created_at", "host", "container_id", "name", "image", "project", "state", "health",
		"restarts", "exit_code", "oom_killed", "started_at", "finished_at", "cpu_limit_cores", "mem_limit_mb", "ports"}}
	containerMetricTable = &otlpTable{"container_metrics", append([]string{"created_at", "host", "container"}, agentapi.ContainerColumns...)}
	containerEventTable  = &otlpTable{"container_events", []string{"created_at", "host", "container", "image", "kind", "severity", "exit_code", "detail", "count"}}
)

// containerEventKinds are the kinds an agent may report.
var containerEventKinds = map[string]bool{"restarted": true, "oom_killed": true, "exited": true, "unhealthy": true, "healthy": true}

// containerRows adds a report's containers, samples and events to rows.
func containerRows(rep *agentapi.Report, at string, rows *otlpRows, n map[string]int) {
	host := rep.Host
	now := time.Now()
	for _, c := range capped(rep.Containers, hostMaxContainers) {
		name, state := clip(c.Name, 128), clip(c.State, 32)
		if !instanceName.MatchString(name) || state == "" {
			continue
		}
		var exit any
		if c.ExitCode != nil {
			exit = int64(*c.ExitCode)
		}
		rows.add(containerTable, []any{at, host, clip(c.ID, 128), name, otlpNullStr(clip(c.Image, 300)), otlpNullStr(clip(c.Project, 128)),
			state, otlpNullStr(clip(c.Health, 32)), int64(max(c.Restarts, 0)), exit, int64(boolInt(c.OOMKilled)),
			containerTime(c.StartedAt), containerTime(c.FinishedAt), positive(c.CPULimit, 2), positive(c.MemLimitMB, 1),
			otlpNullStr(clip(c.Ports, 500))})
		n["containers"]++
	}
	for _, smp := range capped(rep.ContainerSamples, hostMaxContainerSamples) {
		ts, ok := reportTime(smp.At, now)
		if !ok || !instanceName.MatchString(smp.Container) || len(smp.Values) == 0 {
			continue
		}
		row := []any{ts, host, smp.Container}
		for _, col := range agentapi.ContainerColumns {
			v, ok := smp.Values[col]
			switch {
			case !ok || math.IsNaN(v) || math.IsInf(v, 0):
				row = append(row, nil)
			case col == "pids":
				row = append(row, int64(math.Round(v)))
			default:
				row = append(row, finite(v, 2))
			}
		}
		rows.add(containerMetricTable, row)
		n["container_samples"]++
	}
	for _, e := range capped(rep.ContainerEvents, hostMaxEvents) {
		if !containerEventKinds[e.Kind] || !instanceName.MatchString(e.Container) {
			continue
		}
		var exit any
		if e.ExitCode != nil {
			exit = int64(*e.ExitCode)
		}
		rows.add(containerEventTable, []any{at, host, e.Container, otlpNullStr(clip(e.Image, 300)), e.Kind, containerSeverity(e),
			exit, otlpNullStr(clip(e.Detail, 500)), int64(max(e.Count, 1))})
		n["container_events"]++
	}
}

// containerSeverity grades a container event: an OOM kill is critical; a
// crash, a restart or a failed health check a warning; a clean stop
// (exit 0, or 143 from docker stop's SIGTERM) and recovering health info.
func containerSeverity(e agentapi.ContainerEvent) string {
	switch e.Kind {
	case "oom_killed":
		return "critical"
	case "restarted", "unhealthy":
		return "warning"
	case "exited":
		if e.ExitCode != nil && *e.ExitCode != 0 && *e.ExitCode != 143 {
			return "warning"
		}
	}
	return "info"
}

func positive(v float64, prec int) any {
	if v <= 0 {
		return nil
	}
	return finite(v, prec)
}

// containerTime is a Docker time in tsLayout; Docker's zero time is NULL.
func containerTime(v string) any {
	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(v))
	if err != nil || t.Year() < 2000 {
		return nil
	}
	return t.UTC().Format(tsLayout)
}

const containersPrompt = `Containers: Docker or Podman containers on hosts the agent watches.
- host_containers: created_at, host, container_id, name, image, project (Compose project), state ('running', 'exited', 'restarting', 'paused', 'created', 'dead'), health ('healthy', 'unhealthy', 'starting' or NULL), restarts (by the restart policy since created), exit_code, oom_killed (1 when the last exit was an OOM kill), started_at, finished_at, cpu_limit_cores, mem_limit_mb (NULL: no limit), ports. A snapshot of every container per report (about a minute); for the current state use each host's latest created_at.
- container_metrics: one row per running container per sample (15 s): created_at, host, container (the name), cpu_percent (of its CPU limit, or of all host CPUs without one), cpu_cores, cpu_throttled_percent (share of CFS periods throttled), mem_used_mb, mem_limit_mb (the host's memory without a limit), mem_percent, net_rx_kbps, net_tx_kbps, blk_read_kbps, blk_write_kbps, pids.
- container_events: created_at, host, container, image, kind ('restarted' with count = restarts, 'oom_killed', 'exited' with exit_code, 'unhealthy', 'healthy'), severity ('info', 'warning', 'critical'), exit_code, detail, count.`

func (s *Server) containerRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/containers", s.authed(s.handleContainers))
	mux.Handle("GET /api/containers/events", s.authed(s.handleContainerEvents))
	mux.Handle("GET /api/containers/panels/{panel}", s.authed(s.handleContainerPanel))
}

// containerFilter narrows container_metrics or container_events to the
// container and host query parameters, validated before they are inlined.
func containerFilter(r *http.Request) (string, error) {
	var b strings.Builder
	for _, k := range []string{"host", "container"} {
		v := strings.TrimSpace(r.URL.Query().Get(k))
		if v == "" {
			continue
		}
		if !instanceName.MatchString(v) {
			return "", errors.New("invalid " + k + " name")
		}
		b.WriteString(" AND " + k + " = '" + v + "'")
	}
	return b.String(), nil
}

// containerListColumns is GET /api/containers, one row per container.
var containerListColumns = []string{
	"host", "name", "image", "project", "state", "health", "restarts", "exit_code", "oom_killed", "started_at", "finished_at",
	"cpu_percent", "cpu_cores", "cpu_limit_cores", "mem_used_mb", "mem_limit_mb", "mem_percent", "pids", "ports",
	"events", "warnings", "last_seen", "source",
}

// GET /api/containers?from=&to=: every container in each host's latest
// report, with its latest sample and its events in the range, then the
// containers the SDKs report (containers_sdk.go).
func (s *Server) handleContainers(w http.ResponseWriter, r *http.Request) {
	tr, err := parseRange(r.URL.Query(), s.cfg.MaxRangeDays)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := r.Context()
	out := &Result{Columns: containerListColumns, Rows: [][]any{}}
	if err := s.agentContainers(ctx, tr, out); err != nil {
		s.fail(w, err)
		return
	}
	out.Rows = append(out.Rows, s.sdkContainers(ctx, tr)...)
	writeJSON(w, http.StatusOK, out)
}

// agentContainers adds the containers in the agents' latest reports to out.
func (s *Server) agentContainers(ctx context.Context, tr timeRange, out *Result) error {
	// The latest report of each host within the last day: a host whose
	// engine went away keeps its last known containers for a while.
	recent := time.Now().UTC().Add(-24 * time.Hour).Format(tsLayout)
	snap, err := s.db.Query(ctx, `
SELECT c.host, c.name, c.image, c.project, c.state, c.health, c.restarts, c.exit_code, c.oom_killed, c.started_at, c.finished_at,
  c.cpu_limit_cores, c.mem_limit_mb, c.ports, c.created_at
FROM host_containers AS c
JOIN (SELECT host, MAX(created_at) AS at FROM host_containers WHERE created_at >= ? GROUP BY host) AS l
  ON l.host = c.host AND l.at = c.created_at
ORDER BY c.project, c.name
LIMIT 2000`, recent)
	if err != nil {
		if missingTable(err, "host_containers") {
			return nil
		}
		return err
	}
	since := time.Now().UTC().Add(-10 * time.Minute).Format(tsLayout)
	latest := s.containerLookup(ctx, `
SELECT m.host, m.container, m.cpu_percent, m.cpu_cores, m.mem_used_mb, m.mem_limit_mb, m.mem_percent, m.pids
FROM container_metrics AS m
JOIN (SELECT host, container, MAX(created_at) AS at FROM container_metrics WHERE created_at >= ? GROUP BY host, container) AS l
  ON l.host = m.host AND l.container = m.container AND l.at = m.created_at`, since)
	events := s.containerLookup(ctx, `
SELECT host, container, SUM(count), `+countIf("severity <> 'info'")+`
FROM container_events WHERE created_at >= ? AND created_at < ? GROUP BY host, container`,
		tr.From.Format(tsLayout), tr.To.Format(tsLayout))
	for _, row := range snap.Rows {
		key := toString(row[0]) + "\x00" + toString(row[1])
		m, e := latest[key], events[key]
		cell := func(vals []any, i int) any {
			if i < len(vals) {
				return vals[i]
			}
			return nil
		}
		memLimit := row[12]
		if memLimit == nil {
			memLimit = cell(m, 3) // the host's memory: what it may use without a limit
		}
		evCount, evWarn := cell(e, 0), cell(e, 1)
		if evCount == nil {
			evCount, evWarn = int64(0), int64(0)
		}
		out.Rows = append(out.Rows, []any{
			row[0], row[1], row[2], row[3], row[4], row[5], row[6], row[7], row[8], row[9], row[10],
			cell(m, 0), cell(m, 1), row[11], cell(m, 2), memLimit, cell(m, 4), cell(m, 5), row[13],
			evCount, evWarn, row[14], "agent",
		})
	}
	return nil
}

// containerLookup runs a query whose first two columns are host and
// container, and returns the other columns by "host\x00container".
func (s *Server) containerLookup(ctx context.Context, sql string, args ...any) map[string][]any {
	out := map[string][]any{}
	res, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		if ctx.Err() == nil && !missingTable(err, "container_") {
			s.logger.Warn("containers lookup", "err", err)
		}
		return out
	}
	for _, row := range res.Rows {
		if len(row) > 2 {
			out[toString(row[0])+"\x00"+toString(row[1])] = row[2:]
		}
	}
	return out
}

// GET /api/containers/events?from=&to=&host=&container=&source=: newest
// first, at most 500. source=sdk reads one SDK container's OOM kills; without
// a container the SDKs' join the agents'.
func (s *Server) handleContainerEvents(w http.ResponseWriter, r *http.Request) {
	tr, err := parseRange(r.URL.Query(), s.cfg.MaxRangeDays)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cols := []string{"created_at", "host", "container", "image", "kind", "severity", "exit_code", "detail", "count"}
	query := r.URL.Query()
	out := emptyResult(cols)
	if query.Get("source") != "sdk" {
		filter, err := containerFilter(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		q := `SELECT ` + strings.Join(cols, ", ") + ` FROM container_events
WHERE created_at >= ? AND created_at < ?` + filter + ` ORDER BY created_at DESC LIMIT 500`
		rows, err := s.db.Query(r.Context(), q, tr.From.Format(tsLayout), tr.To.Format(tsLayout))
		switch {
		case err == nil:
			out = dbResult(rows, q)
		case !missingTable(err, "container_events"):
			s.fail(w, err)
			return
		}
		if query.Get("container") != "" {
			writeJSON(w, http.StatusOK, out)
			return
		}
	}
	sdk, err := s.sdkContainerEvents(r, tr)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	mergeByTime(out, sdk, true)
	if len(out.Rows) > 500 {
		out.Rows = out.Rows[:500]
	}
	writeJSON(w, http.StatusOK, out)
}

// containerPanel is one chart: per container (series) across containers,
// and the named columns for one container.
type containerPanel struct{ all, one string }

const containerWindow = `created_at >= {{from}} AND created_at < {{to}}{{filter}}`

func containerPanels(q sqlDialect) map[string]containerPanel {
	perContainer := func(expr string) string {
		return `SELECT {{t}} AS t, container AS series, ` + expr + ` AS value
FROM container_metrics WHERE ` + containerWindow + `
GROUP BY {{t}}, container ORDER BY t`
	}
	one := func(cols string) string {
		return `SELECT {{t}} AS t, ` + cols + `
FROM container_metrics WHERE ` + containerWindow + `
GROUP BY t ORDER BY t`
	}
	return map[string]containerPanel{
		"cpu": {perContainer(q.round("AVG(cpu_percent)", 1)),
			one(q.round("AVG(cpu_percent)", 1) + ` AS avg_cpu, MAX(cpu_percent) AS peak_cpu`)},
		"memory": {perContainer(q.round("AVG(mem_used_mb) * 1048576.0", 0)),
			one(q.round("AVG(mem_used_mb) * 1048576.0", 0) + ` AS used, ` + q.round("MAX(mem_limit_mb) * 1048576.0", 0) + ` AS limit_bytes`)},
		"memory_percent": {perContainer(q.round("MAX(mem_percent)", 1)),
			one(q.round("AVG(mem_percent)", 1) + ` AS avg_used, MAX(mem_percent) AS peak_used`)},
		"throttled": {perContainer(q.round("AVG(cpu_throttled_percent)", 1)),
			one(q.round("AVG(cpu_throttled_percent)", 1) + ` AS throttled`)},
		"network": {perContainer(q.round("AVG(COALESCE(net_rx_kbps, 0) + COALESCE(net_tx_kbps, 0)) * 1024", 0)),
			one(q.round("AVG(net_rx_kbps) * 1024", 0) + ` AS received, ` + q.round("AVG(net_tx_kbps) * 1024", 0) + ` AS sent`)},
		"disk": {perContainer(q.round("AVG(COALESCE(blk_read_kbps, 0) + COALESCE(blk_write_kbps, 0)) * 1024", 0)),
			one(q.round("AVG(blk_read_kbps) * 1024", 0) + ` AS read_bytes, ` + q.round("AVG(blk_write_kbps) * 1024", 0) + ` AS written_bytes`)},
		"pids": {perContainer("MAX(pids)"), one(`MAX(pids) AS pids`)},
	}
}

// GET /api/containers/panels/{panel}?from=&to=&host=&container=&source=:
// source=sdk charts one SDK container; without a container the SDKs' series
// join the agents'.
func (s *Server) handleContainerPanel(w http.ResponseWriter, r *http.Request) {
	panel := r.PathValue("panel")
	p, ok := containerPanels(s.q)[panel]
	if !ok {
		writeError(w, http.StatusNotFound, "unknown panel")
		return
	}
	tr, err := parseRange(r.URL.Query(), s.cfg.MaxRangeDays)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	one := r.URL.Query().Get("container") != ""
	if r.URL.Query().Get("source") == "sdk" {
		res, err := s.sdkContainerPanel(r, panel, one, tr)
		switch {
		case err != nil:
			writeError(w, http.StatusBadRequest, err.Error())
		case res == nil:
			writeJSON(w, http.StatusOK, emptyResult([]string{"t"}))
		default:
			writeJSON(w, http.StatusOK, res)
		}
		return
	}
	filter, err := containerFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sql := p.all
	if one {
		sql = p.one
	}
	res, err := s.execTemplate(r.Context(), strings.ReplaceAll(sql, "{{filter}}", filter), tr, true)
	if err != nil && !missingTable(err, "container_metrics") {
		s.fail(w, err)
		return
	}
	if !one {
		sdk, serr := s.sdkContainerPanel(r, panel, false, tr)
		if serr != nil {
			writeError(w, http.StatusBadRequest, serr.Error())
			return
		}
		switch {
		case sdk == nil:
		case res == nil:
			res = sdk
		default:
			mergeByTime(res, sdk.Rows, false)
		}
	}
	if res == nil {
		res = emptyResult([]string{"t", "series", "value"})
	}
	writeJSON(w, http.StatusOK, res)
}

// Alert metrics over containers. CPU and memory fire per container; events
// sum container_events per container, narrowed by kind.

var containerScopes = []string{"container", "host"}

func containerMetrics() []metricDef {
	ctr := func(id, label, unit, desc, agg, op string, th float64) metricDef {
		return metricDef{ID: id, Label: label, Group: "container", Unit: unit, Description: desc, Scopes: containerScopes,
			Op: op, Threshold: th, UpIsBad: true, table: "container_metrics", perInstance: true, keyCol: "container",
			agg: func(int64) string { return agg }}
	}
	return []metricDef{
		ctr("container_cpu", "Container CPU", "percent", "Average CPU over the window, as a percent of the container's CPU limit (of all host CPUs without one).", "AVG(cpu_percent)", ">", 90),
		ctr("container_memory", "Container memory", "percent", "Highest memory use over the window, as a percent of the container's limit. Near 100% the kernel kills it (OOM).", "MAX(mem_percent)", ">", 90),
		ctr("container_throttled", "Container CPU throttling", "percent", "Share of CPU periods in which the container hit its CPU limit and had to wait.", "AVG(cpu_throttled_percent)", ">", 25),
		{ID: "container_events", Label: "Container events", Group: "container", Unit: "count",
			Description: "Restarts, OOM kills, exits and failed health checks in the window, per container. Pick a kind to watch one.",
			Scopes:      []string{"container", "host", "event_kind"}, Op: ">=", Threshold: 1, UpIsBad: true, table: "container_events"},
	}
}

// observeContainerEvents sums matching events in the window per container.
func (s *Server) observeContainerEvents(ctx context.Context, r alertRule, now time.Time) ([]observation, error) {
	w := specWindow(r.Spec)
	scope, sargs := containerEventClause(r.Spec.Scope)
	args := append([]any{now.Add(-w).UTC().Format(tsLayout), now.UTC().Format(tsLayout)}, sargs...)
	res, err := s.db.Query(ctx, `SELECT container, SUM(count) AS value, MIN(kind) AS kind, COUNT(DISTINCT kind) AS kinds
FROM container_events WHERE created_at >= ? AND created_at < ?`+scope+`
GROUP BY container ORDER BY value DESC LIMIT 50`, args...)
	if err != nil {
		if missingTable(err, "container_events") {
			return nil, nil
		}
		return nil, err
	}
	win := fallback(r.Spec.Window, "5m")
	what := "container event"
	if k := r.Spec.Scope.EventKind; k != "" {
		what = strings.ReplaceAll(k, "_", " ") + " event"
	}
	var out []observation
	for _, row := range res.Rows {
		if len(row) != 4 {
			continue
		}
		key := toString(row[0])
		v, _ := cellFloat(row[1])
		kinds := strings.ReplaceAll(toString(row[2]), "_", " ")
		if n := toInt(row[3]); n > 1 {
			kinds += " and " + strconv.FormatInt(n-1, 10) + " other kind(s)"
		}
		ob := observation{Key: key, Label: "container " + key, Value: ptr(v), Threshold: ptr(r.Spec.Threshold),
			Breach: compare(r.Spec.Op, v, r.Spec.Threshold)}
		if r.Spec.Scope.EventKind == "" {
			what += "(s) (" + kinds + ")"
		} else {
			what += "(s)"
		}
		ob.Message = fmt.Sprintf("%s %s for container %s over the last %s (threshold %s %s).",
			formatUnit(v, "count"), what, key, win, r.Spec.Op, formatUnit(r.Spec.Threshold, "count"))
		out = append(out, ob)
	}
	if len(out) == 0 {
		zero := 0.0
		out = append(out, observation{Key: "", Label: "all containers", Value: &zero, Threshold: ptr(r.Spec.Threshold),
			Breach:  compare(r.Spec.Op, 0, r.Spec.Threshold),
			Message: "No matching container events over the last " + win + "."})
	}
	return out, nil
}

// previewContainerEvents charts matching events per hour over the day, by kind.
func (s *Server) previewContainerEvents(ctx context.Context, sp ruleSpec, day []timeSpan, tr timeRange) (*Result, error) {
	where, args := spansClause(day)
	scope, sargs := containerEventClause(sp.Scope)
	t := s.q.bucketOf("3600")
	res, err := s.db.Query(ctx, `SELECT `+t+` AS t, kind AS series, SUM(count) AS value
FROM container_events WHERE `+where+scope+` GROUP BY `+t+`, kind ORDER BY t`, append(args, sargs...)...)
	if err != nil {
		return nil, err
	}
	tr.Bucket = 3600
	return newResult(res, s.cfg.MaxRows, "", tr), nil
}

func containerEventClause(sc alertScope) (string, []any) {
	var b strings.Builder
	var args []any
	for _, f := range []struct{ col, v string }{{"kind", sc.EventKind}, {"container", sc.Container}, {"host", sc.Host}} {
		if f.v != "" {
			b.WriteString(" AND " + f.col + " = ?")
			args = append(args, f.v)
		}
	}
	return b.String(), args
}
