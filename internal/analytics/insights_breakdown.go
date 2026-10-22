package analytics

import (
	"net/http"
	"sort"
	"strconv"

	"golang.org/x/sync/errgroup"
)

// handleDependencies treats each outbound host like a route of your own:
// rate, error rate, latency spread and their change since the previous period,
// the routes that call it, and p95 and errors over time.
func (s *Server) handleDependencies(w http.ResponseWriter, r *http.Request) {
	tr, ok := s.rangeOf(w, r)
	if !ok {
		return
	}
	minutes := strconv.FormatFloat(max(tr.To.Sub(tr.From).Minutes(), 1), 'f', 3, 64)
	hostsSQL := `
WITH sp AS (
  SELECT name, target, duration_ms, status,
    ROW_NUMBER() OVER (PARTITION BY name ORDER BY duration_ms) AS rn,
    COUNT(*) OVER (PARTITION BY name) AS n
  FROM spans WHERE created_at >= {{from}} AND created_at < {{to}} AND kind = 'http'
)
SELECT name, MAX(target) AS host,
  COUNT(*) AS calls,
  ` + s.q.round("COUNT(*) / "+minutes, 2) + ` AS per_min,
  ` + countIf("status = 'error'") + ` AS errors,
  ` + s.q.round("100.0 * "+countIf("status = 'error'")+" / COUNT(*)", 2) + ` AS error_rate,
  ` + s.q.round("AVG(duration_ms)", 2) + ` AS avg_ms,
  ` + pctOf("duration_ms", "0.50") + ` AS p50,
  ` + pctOf("duration_ms", "0.95") + ` AS p95,
  ` + pctOf("duration_ms", "0.99") + ` AS p99,
  ` + s.q.round("SUM(duration_ms)", 1) + ` AS total_ms
FROM sp GROUP BY name ORDER BY total_ms DESC LIMIT 50`
	var cur, prev, timeline, errs, routes *Result
	g, ctx := errgroup.WithContext(r.Context())
	g.Go(func() (err error) { cur, err = s.telemetryQuery(ctx, hostsSQL, tr); return })
	g.Go(func() (err error) {
		p := tr.previous()
		prevSQL := `
WITH sp AS (
  SELECT name, duration_ms, status,
    ROW_NUMBER() OVER (PARTITION BY name ORDER BY duration_ms) AS rn,
    COUNT(*) OVER (PARTITION BY name) AS n
  FROM spans WHERE created_at >= {{from}} AND created_at < {{to}} AND kind = 'http'
)
SELECT name, COUNT(*) AS calls, ` + s.q.round("100.0 * "+countIf("status = 'error'")+" / COUNT(*)", 2) + ` AS error_rate,
  ` + pctOf("duration_ms", "0.95") + ` AS p95
FROM sp GROUP BY name`
		prev, err = s.telemetryQuery(ctx, prevSQL, p)
		return
	})
	// p95 per host over time, for the six hosts with the most calls.
	g.Go(func() (err error) {
		timeline, err = s.telemetryQuery(ctx, `
WITH b AS (
  SELECT {{t}} AS t, name, duration_ms,
    COUNT(*) OVER (PARTITION BY name) AS host_calls
  FROM spans WHERE created_at >= {{from}} AND created_at < {{to}} AND kind = 'http'
),
top AS (
  SELECT t, name, duration_ms, DENSE_RANK() OVER (ORDER BY host_calls DESC, name) AS host_rank FROM b
),
r AS (
  SELECT t, name, duration_ms,
    ROW_NUMBER() OVER (PARTITION BY t, name ORDER BY duration_ms) AS rn,
    COUNT(*) OVER (PARTITION BY t, name) AS n
  FROM top WHERE host_rank <= 6
)
SELECT t, name AS series, `+pctOf("duration_ms", "0.95")+` AS value
FROM r GROUP BY t, name ORDER BY t`, tr)
		return
	})
	g.Go(func() (err error) {
		errs, err = s.telemetryQuery(ctx, `
SELECT {{t}} AS t, COUNT(*) AS calls, `+countIf("status = 'error'")+` AS errors
FROM spans WHERE created_at >= {{from}} AND created_at < {{to}} AND kind = 'http'
GROUP BY 1 ORDER BY 1`, tr)
		return
	})
	g.Go(func() (err error) {
		routes, err = s.telemetryQuery(ctx, `
SELECT sp.name, a.method, COALESCE(a.route, a.path) AS route, COUNT(*) AS calls
FROM spans sp JOIN api_logs a ON a.request_id = sp.request_id
WHERE sp.created_at >= {{from}} AND sp.created_at < {{to}} AND sp.kind = 'http'
  AND a.created_at >= ? AND a.created_at < {{to}}
GROUP BY sp.name, a.method, COALESCE(a.route, a.path)`, tr, tr.requestsFrom())
		return
	})
	if err := g.Wait(); err != nil {
		s.fail(w, err)
		return
	}

	type before struct{ p95, rate any }
	prevBy := map[string]before{}
	for _, m := range rowMaps(prev) {
		prevBy[toString(m["name"])] = before{m["p95"], m["error_rate"]}
	}
	type routeCalls struct {
		route string
		calls int64
	}
	routesBy := map[string][]routeCalls{}
	for _, m := range rowMaps(routes) {
		n := toString(m["name"])
		routesBy[n] = append(routesBy[n], routeCalls{toString(m["method"]) + " " + toString(m["route"]), toInt(m["calls"])})
	}
	cur.Columns = append(cur.Columns, "prev_p95", "prev_error_rate", "routes")
	for i, row := range cur.Rows {
		n := toString(row[0])
		rc := routesBy[n]
		sort.Slice(rc, func(a, b int) bool { return rc[a].calls > rc[b].calls })
		top := []string{}
		for _, x := range head(rc, 3) {
			top = append(top, x.route)
		}
		p := prevBy[n]
		cur.Rows[i] = append(row, p.p95, p.rate, top)
	}
	writeJSON(w, http.StatusOK, map[string]any{"hosts": cur, "timeline": timeline, "errors": errs})
}

// handleBreakdown is where one route's time goes, per bucket: database,
// outbound HTTP, other recorded calls (cache, queue, custom, GraphQL) and the
// rest, the route's own code. Averages per request. Without method and route
// it answers only the routes to choose from.
func (s *Server) handleBreakdown(w http.ResponseWriter, r *http.Request) {
	tr, ok := s.rangeOf(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	routes, err := s.telemetryQuery(r.Context(), `
SELECT method, COALESCE(route, path) AS route, COUNT(*) AS requests,
  `+s.q.round("SUM(duration_ms) / 1000.0", 1)+` AS busy_seconds
FROM api_logs WHERE created_at >= {{from}} AND created_at < {{to}}
GROUP BY method, COALESCE(route, path)
ORDER BY busy_seconds DESC LIMIT 40`, tr)
	if err != nil {
		s.fail(w, err)
		return
	}
	method, route := q.Get("method"), q.Get("route")
	if method == "" || route == "" {
		if len(routes.Rows) == 0 {
			writeJSON(w, http.StatusOK, map[string]any{"routes": routes})
			return
		}
		method, route = toString(routes.Rows[0][0]), toString(routes.Rows[0][1])
	}
	res, err := s.telemetryQuery(r.Context(), `
WITH a AS (
  SELECT {{t}} AS t, request_id, duration_ms, COALESCE(db_ms, 0) AS db_ms, COALESCE(http_ms, 0) AS http_ms
  FROM api_logs
  WHERE created_at >= {{from}} AND created_at < {{to}} AND method = ? AND COALESCE(route, path) = ?
),
o AS (
  -- Other recorded calls the request waited for: not background work it
  -- started, nor calls made after the response.
  SELECT sp.request_id, SUM(sp.duration_ms) AS other_ms
  FROM spans sp JOIN a ON a.request_id = sp.request_id
  WHERE sp.created_at >= {{from}} AND sp.created_at < {{to}} AND sp.kind NOT IN ('db', 'http', 'background')
    AND COALESCE(sp.offset_ms, 0) < a.duration_ms
  GROUP BY sp.request_id
)
SELECT a.t, COUNT(*) AS requests,
  `+s.q.round("AVG(a.duration_ms)", 2)+` AS total_ms,
  `+s.q.round("AVG(a.db_ms)", 2)+` AS db_ms,
  `+s.q.round("AVG(a.http_ms)", 2)+` AS http_ms,
  `+s.q.round("AVG(COALESCE(o.other_ms, 0))", 2)+` AS other_ms
FROM a LEFT JOIN o ON o.request_id = a.request_id
GROUP BY a.t ORDER BY a.t`, tr, method, route)
	if err != nil {
		s.fail(w, err)
		return
	}

	// Long format for a stacked chart. The route's own code is what is left:
	// parallel calls can add up to more than the request, so it never goes
	// below zero.
	series := &Result{Columns: []string{"t", "series", "value"}, Bucket: res.Bucket, From: res.From, To: res.To, SQL: res.SQL}
	var sum struct{ n, total, db, http, other, app float64 }
	for _, m := range rowMaps(res) {
		total, _ := asFloat(m["total_ms"])
		db, _ := asFloat(m["db_ms"])
		ht, _ := asFloat(m["http_ms"])
		other, _ := asFloat(m["other_ms"])
		app := max(total-db-ht-other, 0)
		n, _ := asFloat(m["requests"])
		t := m["t"]
		series.Rows = append(series.Rows,
			[]any{t, "Database", db}, []any{t, "Outbound HTTP", ht},
			[]any{t, "Cache, queue and custom", other}, []any{t, "App code", float64(int(app*100+0.5)) / 100})
		sum.n += n
		sum.total += total * n
		sum.db += db * n
		sum.http += ht * n
		sum.other += other * n
		sum.app += app * n
	}
	avg := func(v float64) any {
		if sum.n == 0 {
			return nil
		}
		return float64(int(v/sum.n*100+0.5)) / 100
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"routes": routes, "method": method, "route": route, "series": series,
		"totals": map[string]any{"requests": sum.n, "total_ms": avg(sum.total), "db_ms": avg(sum.db), "http_ms": avg(sum.http), "other_ms": avg(sum.other), "app_ms": avg(sum.app)},
	})
}
