package analytics

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"
)

// The service map's extras: what is on each box (deploys, incidents, firing
// alerts, machine pressure), a service's and an arrow's detail, and every
// arrow's trend for the tooltips. All read rollup rows where they cover the
// range, so a day costs about as much as an hour.

// mapRows is t's rows over tr as a derived table: rollup rows where they
// cover it, raw rows shaped like them otherwise
func (s *Server) mapRows(kind string, t rollupTable, tr timeRange, alias string) string {
	if rt := s.rollupRuntime(); rt != nil {
		if segs, ok := rt.segments(kind, tr); ok {
			return unionSQL(t, segs, alias)
		}
	}
	return "(\n" + t.rawRowsSQL(tr.From.Format(tsLayout), tr.To.Format(tsLayout)) + "\n) AS " + alias
}

// histSums sums the latency histogram, for withP95
func histSums() string {
	hist := make([]string, 0, len(rollupEdges)+1)
	for i := range len(rollupEdges) + 1 {
		hist = append(hist, "SUM("+histCol(i)+") AS "+histCol(i))
	}
	return strings.Join(hist, ", ")
}

type mapHealth struct {
	Deploys      []mapDeploy   `json:"deploys"`
	Incidents    []mapIncident `json:"incidents"`
	Alerts       []mapAlert    `json:"alerts"`
	CPU          *float64      `json:"cpu"`
	Memory       *float64      `json:"memory"`
	ContainerMem *float64      `json:"container_memory"`
}

type mapDeploy struct {
	Version string `json:"version"`
	At      string `json:"at"`
}

type mapIncident struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	Severity string `json:"severity"`
}

type mapAlert struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Severity string `json:"severity"`
	SLO      bool   `json:"slo"`
}

// handleMapOverlay answers per service: deploys in the range, open
// incidents naming it, firing alerts scoped to it, and its machines' CPU and
// memory over the last five minutes of the range. Each part is best effort,
// since some tables only exist once their feature is used.
func (s *Server) handleMapOverlay(w http.ResponseWriter, r *http.Request) {
	tr, ok := s.rangeOf(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	want := map[string]bool{}
	for _, n := range strings.Split(r.URL.Query().Get("services"), ",") {
		if n = strings.TrimSpace(n); n != "" {
			want[n] = true
		}
	}
	out := map[string]*mapHealth{}
	get := func(svc string) *mapHealth {
		if !want[svc] {
			return nil
		}
		h := out[svc]
		if h == nil {
			h = &mapHealth{Deploys: []mapDeploy{}, Incidents: []mapIncident{}, Alerts: []mapAlert{}}
			out[svc] = h
		}
		return h
	}
	soft := func(part string, err error) bool {
		if err != nil && ctx.Err() == nil {
			s.logger.Debug("service map overlay", "part", part, "err", err)
		}
		return err == nil
	}

	if res, err := s.telemetryQuery(ctx, `SELECT COALESCE(service, '') AS service, version, MIN(created_at) AS at
FROM releases WHERE created_at >= {{from}} AND created_at < {{to}}
GROUP BY COALESCE(service, ''), version ORDER BY at DESC LIMIT 200`, tr); soft("deploys", err) {
		for _, m := range rowMaps(res) {
			if h := get(toString(m["service"])); h != nil {
				h.Deploys = append(h.Deploys, mapDeploy{toString(m["version"]), toString(m["at"])})
			}
		}
	}
	if res, err := s.telemetryQuery(ctx, `SELECT id, title, severity, services FROM incidents WHERE resolved_at = '' ORDER BY declared_at DESC LIMIT 100`, timeRange{}); soft("incidents", err) {
		for _, m := range rowMaps(res) {
			var names []string
			_ = json.Unmarshal([]byte(toString(m["services"])), &names)
			for _, n := range names {
				if h := get(n); h != nil {
					h.Incidents = append(h.Incidents, mapIncident{toInt(m["id"]), toString(m["title"]), toString(m["severity"])})
				}
			}
		}
	}
	if res, err := s.telemetryQuery(ctx, `SELECT r.id, r.name, r.kind, r.severity, r.spec FROM alert_rules r
WHERE r.enabled = 1 AND EXISTS (SELECT 1 FROM alert_state a WHERE a.rule_id = r.id AND a.state = 'firing')`, timeRange{}); soft("alerts", err) {
		for _, m := range rowMaps(res) {
			var sp struct {
				Scope alertScope `json:"scope"`
			}
			_ = json.Unmarshal([]byte(toString(m["spec"])), &sp)
			if h := get(sp.Scope.Service); h != nil {
				h.Alerts = append(h.Alerts, mapAlert{toInt(m["id"]), toString(m["name"]), toString(m["severity"]), toString(m["kind"]) == "slo"})
			}
		}
	}
	recent := timeRange{From: tr.To.Add(-5 * time.Minute), To: tr.To}
	if res, err := s.telemetryQuery(ctx, `SELECT COALESCE(service, '') AS service, AVG(host_cpu_percent) AS cpu, AVG(host_mem_percent) AS mem,
  100.0 * AVG(container_mem_used_mb / NULLIF(container_mem_limit_mb, 0)) AS cmem
FROM system_metrics WHERE created_at >= {{from}} AND created_at < {{to}}
GROUP BY COALESCE(service, '')`, recent); soft("pressure", err) {
		for _, m := range rowMaps(res) {
			if h := get(toString(m["service"])); h != nil {
				h.CPU, h.Memory, h.ContainerMem = floatOrNil(m["cpu"]), floatOrNil(m["mem"]), floatOrNil(m["cmem"])
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"services": out})
}

// handleMapService is one service's detail: its trend, slowest routes,
// commonest errors, deploys and instances.
func (s *Server) handleMapService(w http.ResponseWriter, r *http.Request) {
	tr, ok := s.rangeOf(w, r)
	if !ok {
		return
	}
	name := r.URL.Query().Get("name")
	ctx := r.Context()
	rows := s.mapRows("requests", requestsTable(s.apdex(), nil), tr, "rr")
	hs := histSums()
	trend, err := s.telemetryQuery(ctx, `SELECT {{t}} AS t, SUM(requests) AS requests, SUM(errors_5xx) AS errors, MIN(min_ms) AS lo, MAX(max_ms) AS hi, `+hs+`
FROM `+rows+` WHERE service = ? GROUP BY {{t}} ORDER BY t`, tr, name)
	if err != nil {
		s.fail(w, err)
		return
	}
	routes, err := s.telemetryQuery(ctx, `SELECT route, SUM(requests) AS requests, SUM(errors_5xx) AS errors, MIN(min_ms) AS lo, MAX(max_ms) AS hi, `+hs+`
FROM `+rows+` WHERE service = ? GROUP BY route ORDER BY requests DESC LIMIT 100`, tr, name)
	if err != nil {
		s.fail(w, err)
		return
	}
	slowest := withP95(routes, 3)
	sort.SliceStable(slowest.Rows, func(i, j int) bool { return toFloat(slowest.Rows[i][3]) > toFloat(slowest.Rows[j][3]) })
	if len(slowest.Rows) > 10 {
		slowest.Rows = slowest.Rows[:10]
	}
	errs, err := s.telemetryQuery(ctx, `SELECT fingerprint, MAX(type) AS type, MAX(message) AS message, COUNT(*) AS n, MAX(created_at) AS last_at
FROM errors WHERE created_at >= {{from}} AND created_at < {{to}} AND service = ?
GROUP BY fingerprint ORDER BY n DESC LIMIT 10`, tr, name)
	if err != nil && !missingTable(err, "errors") {
		s.fail(w, err)
		return
	}
	deploys, err := s.telemetryQuery(ctx, `SELECT version, MIN(created_at) AS at FROM releases
WHERE created_at >= {{from}} AND created_at < {{to}} AND service = ?
GROUP BY version ORDER BY at DESC LIMIT 20`, tr, name)
	if err != nil && !missingTable(err, "releases") {
		s.fail(w, err)
		return
	}
	// Instances seen in the last hour of the range, for the System links
	inst, err := s.telemetryQuery(ctx, `SELECT instance, MAX(created_at) AS last_at FROM system_metrics
WHERE created_at >= {{from}} AND created_at < {{to}} AND service = ?
GROUP BY instance ORDER BY last_at DESC LIMIT 20`, timeRange{From: tr.To.Add(-time.Hour), To: tr.To}, name)
	if err != nil && !missingTable(err, "system_metrics") {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name": name, "bucket": tr.Bucket,
		"trend": withP95(trend, 3), "routes": slowest, "errors": orEmpty(errs), "deploys": orEmpty(deploys), "instances": orEmpty(inst),
	})
}

// handleMapEdge is one arrow's trend and, between services, the callee's
// routes it calls.
func (s *Server) handleMapEdge(w http.ResponseWriter, r *http.Request) {
	tr, ok := s.rangeOf(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	src, kind, dst := q.Get("src"), q.Get("kind"), q.Get("dst")
	if kind == "" {
		writeError(w, http.StatusBadRequest, "kind is required")
		return
	}
	ctx := r.Context()
	rows := s.mapRows("edges", edgesTable(nil), tr, "re")
	hs := histSums()
	series, err := s.telemetryQuery(ctx, `SELECT {{t}} AS t, SUM(calls) AS calls, SUM(errors) AS errors, MIN(min_ms) AS lo, MAX(max_ms) AS hi, `+hs+`
FROM `+rows+` WHERE src = ? AND kind = ? AND dst = ? GROUP BY {{t}} ORDER BY t`, tr, src, kind, dst)
	if err != nil {
		s.fail(w, err)
		return
	}
	routes := &Result{Columns: []string{"route", "calls", "errors", "p95"}, Rows: [][]any{}}
	if kind == "service" {
		res, err := s.telemetryQuery(ctx, `SELECT COALESCE(route, '') AS route, SUM(calls) AS calls, SUM(errors) AS errors, MIN(min_ms) AS lo, MAX(max_ms) AS hi, `+hs+`
FROM `+rows+` WHERE src = ? AND kind = ? AND dst = ? GROUP BY COALESCE(route, '') ORDER BY calls DESC LIMIT 50`, tr, src, kind, dst)
		if err != nil {
			s.fail(w, err)
			return
		}
		routes = withP95(res, 3)
	}
	writeJSON(w, http.StatusOK, map[string]any{"bucket": tr.Bucket, "series": withP95(series, 3), "routes": routes})
}

// handleMapSeries is every arrow's calls and failures per bucket, about 30
// points over the range, keyed like the map's edges ("service:a>db:b").
func (s *Server) handleMapSeries(w http.ResponseWriter, r *http.Request) {
	tr, ok := s.rangeOf(w, r)
	if !ok {
		return
	}
	tr.Bucket = pickBucket(3 * tr.To.Sub(tr.From))
	sql := tr.render(`SELECT {{t}} AS t, src, kind, dst, SUM(calls) AS calls, SUM(errors) AS errors
FROM `+s.mapRows("edges", edgesTable(nil), tr, "re")+`
GROUP BY {{t}}, src, kind, dst ORDER BY t`, s.q)
	rows, err := s.db.Query(r.Context(), sql)
	if err != nil {
		s.fail(w, dbErr(err))
		return
	}
	out := map[string][][3]any{}
	for _, row := range rows.Rows {
		if len(row) < 6 {
			continue
		}
		key := "service:" + toString(row[1]) + ">" + toString(row[2]) + ":" + toString(row[3])
		if len(out[key]) == 0 && len(out) >= 400 {
			continue
		}
		out[key] = append(out[key], [3]any{toInt(row[0]), toInt(row[4]), toInt(row[5])})
	}
	writeJSON(w, http.StatusOK, map[string]any{"bucket": tr.Bucket, "edges": out})
}

func orEmpty(res *Result) *Result {
	if res == nil {
		return &Result{Columns: []string{}, Rows: [][]any{}}
	}
	return res
}
