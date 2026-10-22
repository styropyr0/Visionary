package analytics

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"time"

	"golang.org/x/sync/errgroup"
)

// spanKind reads the kind query parameter (def when empty), answering 400
// itself when it is not a span kind.
func spanKind(w http.ResponseWriter, r *http.Request, def string) (string, bool) {
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = def
	}
	switch kind {
	case "db", "http", "cache", "queue", "custom", "graphql", "background":
		return kind, true
	}
	writeError(w, http.StatusBadRequest, "kind must be db, http, cache, queue, custom, graphql or background")
	return "", false
}

// p is the rank-method percentile of duration_ms over rows ranked rn of n.
func pctOf(col, frac string) string {
	return "MIN(CASE WHEN rn >= " + frac + " * n THEN " + col + " END)"
}

// handleQueryCatalogue lists every distinct statement (or URL, for http) in
// the range: its rate, latency spread, total time, rows, the routes that run
// it, when it was first and last seen, and whether it is new since the
// previous period. Sorted by total time: a 5 ms query run 10,000 times costs
// more than one slow query.
func (s *Server) handleQueryCatalogue(w http.ResponseWriter, r *http.Request) {
	tr, ok := s.rangeOf(w, r)
	if !ok {
		return
	}
	kind, ok := spanKind(w, r, "db")
	if !ok {
		return
	}
	minutes := tr.To.Sub(tr.From).Minutes()
	if minutes < 1 {
		minutes = 1
	}
	var list, routes *Result
	g, ctx := errgroup.WithContext(r.Context())
	g.Go(func() (err error) {
		list, err = s.telemetryQuery(ctx, `
WITH sp AS (
  SELECT name, COALESCE(detail, '') AS detail, target, duration_ms, status, row_count, created_at, request_id,
    ROW_NUMBER() OVER (PARTITION BY COALESCE(detail, '') ORDER BY duration_ms) AS rn,
    COUNT(*) OVER (PARTITION BY COALESCE(detail, '')) AS n
  FROM spans WHERE created_at >= {{from}} AND created_at < {{to}} AND kind = ?
)
SELECT detail, MAX(name) AS name, MAX(target) AS target,
  COUNT(*) AS calls,
  `+s.q.round("COUNT(*) / "+strconv.FormatFloat(minutes, 'f', 3, 64), 2)+` AS per_min,
  `+s.q.round("SUM(duration_ms)", 1)+` AS total_ms,
  `+s.q.round("AVG(duration_ms)", 2)+` AS avg_ms,
  `+pctOf("duration_ms", "0.50")+` AS p50,
  `+pctOf("duration_ms", "0.95")+` AS p95,
  MAX(duration_ms) AS max_ms,
  `+countIf("status = 'error'")+` AS errors,
  `+s.q.round("AVG(row_count)", 1)+` AS avg_rows,
  COUNT(DISTINCT request_id) AS requests,
  MIN(created_at) AS first_seen,
  MAX(created_at) AS last_seen
FROM sp
GROUP BY detail
ORDER BY total_ms DESC
LIMIT 100`, tr, kind)
		return
	})
	g.Go(func() (err error) {
		routes, err = s.telemetryQuery(ctx, `
SELECT COALESCE(sp.detail, '') AS detail, a.method, COALESCE(a.route, a.path) AS route, COUNT(*) AS calls
FROM spans sp JOIN api_logs a ON a.request_id = sp.request_id
WHERE sp.created_at >= {{from}} AND sp.created_at < {{to}} AND sp.kind = ?
  AND a.created_at >= ? AND a.created_at < {{to}}
GROUP BY COALESCE(sp.detail, ''), a.method, COALESCE(a.route, a.path)`, tr, kind, tr.requestsFrom())
		return
	})
	if err := g.Wait(); err != nil {
		s.fail(w, err)
		return
	}

	// Seen in the previous period too? Asked for the listed statements only,
	// in batches that stay under D1's 100 bound parameters.
	di := indexOf(list.Columns, "detail")
	seenBefore := map[string]bool{}
	var details []any
	for _, row := range list.Rows {
		details = append(details, toString(row[di]))
	}
	prev := tr.previous()
	for start := 0; start < len(details); start += 90 {
		batch := details[start:min(start+90, len(details))]
		args := append([]any{kind}, batch...)
		res, err := s.telemetryQuery(r.Context(), `
SELECT DISTINCT COALESCE(detail, '') AS detail FROM spans
WHERE created_at >= {{from}} AND created_at < {{to}} AND kind = ? AND COALESCE(detail, '') IN (`+marks(len(batch))+`)`, prev, args...)
		if err != nil {
			s.fail(w, err)
			return
		}
		for _, row := range res.Rows {
			seenBefore[toString(row[0])] = true
		}
	}

	type routeCalls struct {
		route string
		calls int64
	}
	byDetail := map[string][]routeCalls{}
	for _, m := range rowMaps(routes) {
		d := toString(m["detail"])
		byDetail[d] = append(byDetail[d], routeCalls{toString(m["method"]) + " " + toString(m["route"]), toInt(m["calls"])})
	}
	list.Columns = append(list.Columns, "routes", "new")
	for i, row := range list.Rows {
		d := toString(row[di])
		rc := byDetail[d]
		sort.Slice(rc, func(a, b int) bool { return rc[a].calls > rc[b].calls })
		top := []string{}
		for _, x := range head(rc, 3) {
			top = append(top, x.route)
		}
		list.Rows[i] = append(row, top, !seenBefore[d])
	}
	writeJSON(w, http.StatusOK, list)
}

func marks(n int) string {
	if n <= 0 {
		return "NULL"
	}
	out := "?"
	for i := 1; i < n; i++ {
		out += ", ?"
	}
	return out
}

// depthPerRoute is how many recent requests of each route are read span by
// span to measure how deeply calls nest, up to depthSample in all. Counts and
// shares use every request.
const (
	depthPerRoute = 50
	depthSample   = 4000
)

// handleQueryDepth answers, per route: how many queries a request makes (p50,
// p95, max), how much of its time is database time (overall and at p95), how
// deeply its recorded calls nest, and the p95 query count in the latest
// version next to the version before it.
//
// Depth is how many recorded layers a call sits under: 1 for a query made
// straight from the handler, 2 for one inside a custom span ("load cart"),
// 3 for one inside a custom span inside another, and so on. Only custom and
// GraphQL spans count as layers: database and HTTP calls never contain
// other calls, and calls that merely overlap in time (run in parallel) are
// not nested. Without custom spans every route is at 1.
func (s *Server) handleQueryDepth(w http.ResponseWriter, r *http.Request) {
	tr, ok := s.rangeOf(w, r)
	if !ok {
		return
	}
	var stats, versions *Result
	var depth map[string][]int
	g, ctx := errgroup.WithContext(r.Context())
	g.Go(func() (err error) {
		stats, err = s.telemetryQuery(ctx, `
WITH b AS (
  SELECT method, COALESCE(route, path) AS route, db_count, db_ms, duration_ms, http_count,
    100.0 * db_ms / NULLIF(duration_ms, 0) AS share
  FROM api_logs
  WHERE created_at >= {{from}} AND created_at < {{to}} AND db_count IS NOT NULL
),
r AS (
  SELECT method, route, db_count, db_ms, duration_ms, http_count, share,
    ROW_NUMBER() OVER (PARTITION BY method, route ORDER BY db_count) AS rq,
    ROW_NUMBER() OVER (PARTITION BY method, route ORDER BY share) AS rs,
    COUNT(*) OVER (PARTITION BY method, route) AS n
  FROM b
)
SELECT method, route,
  COUNT(*) AS requests,
  `+s.q.round("AVG(db_count)", 1)+` AS avg_queries,
  MIN(CASE WHEN rq >= 0.50 * n THEN db_count END) AS p50_queries,
  MIN(CASE WHEN rq >= 0.95 * n THEN db_count END) AS p95_queries,
  MAX(db_count) AS max_queries,
  `+s.q.round("100.0 * SUM(db_ms) / NULLIF(SUM(duration_ms), 0)", 1)+` AS db_share,
  `+s.q.round("MIN(CASE WHEN rs >= 0.95 * n THEN share END)", 1)+` AS p95_db_share,
  `+s.q.round("AVG(http_count)", 1)+` AS avg_http
FROM r
GROUP BY method, route
ORDER BY p95_queries DESC, requests DESC
LIMIT 60`, tr)
		return
	})
	g.Go(func() (err error) {
		versions, err = s.telemetryQuery(ctx, `
WITH r AS (
  SELECT method, COALESCE(route, path) AS route, COALESCE(version, '') AS version, db_count, created_at,
    ROW_NUMBER() OVER (PARTITION BY method, COALESCE(route, path), COALESCE(version, '') ORDER BY db_count) AS rn,
    COUNT(*) OVER (PARTITION BY method, COALESCE(route, path), COALESCE(version, '')) AS n
  FROM api_logs
  WHERE created_at >= {{from}} AND created_at < {{to}} AND db_count IS NOT NULL
)
SELECT method, route, version, COUNT(*) AS requests, MAX(created_at) AS last_seen,
  MIN(CASE WHEN rn >= 0.95 * n THEN db_count END) AS p95_queries
FROM r GROUP BY method, route, version`, tr)
		return
	})
	g.Go(func() (err error) { depth, err = s.callDepths(ctx, tr); return })
	if err := g.Wait(); err != nil {
		s.fail(w, err)
		return
	}

	// Latest and previous version of each route, by when each was last seen.
	type ver struct {
		version  string
		last     string
		p95      any
		requests int64
	}
	byRoute := map[string][]ver{}
	for _, m := range rowMaps(versions) {
		k := toString(m["method"]) + " " + toString(m["route"])
		byRoute[k] = append(byRoute[k], ver{toString(m["version"]), toString(m["last_seen"]), m["p95_queries"], toInt(m["requests"])})
	}
	stats.Columns = append(stats.Columns, "p95_depth", "max_depth", "depth_sampled", "version", "prev_version", "prev_p95_queries")
	mi, ri := indexOf(stats.Columns, "method"), indexOf(stats.Columns, "route")
	for i, row := range stats.Rows {
		k := toString(row[mi]) + " " + toString(row[ri])
		var p95d, maxd any
		ds := depth[k]
		if len(ds) > 0 {
			sort.Ints(ds)
			p95d, maxd = ds[int(0.95*float64(len(ds)-1)+0.5)], ds[len(ds)-1]
		}
		vs := byRoute[k]
		sort.Slice(vs, func(a, b int) bool { return vs[a].last > vs[b].last })
		var v, pv, pp95 any
		if len(vs) > 0 && vs[0].version != "" {
			v = vs[0].version
		}
		if len(vs) > 1 && vs[1].version != "" && vs[1].requests >= 5 {
			pv, pp95 = vs[1].version, vs[1].p95
		}
		stats.Rows[i] = append(row, p95d, maxd, len(ds), v, pv, pp95)
	}
	writeJSON(w, http.StatusOK, stats)
}

// callDepths measures, for the most recent requests that made calls, how many
// wrapping spans their deepest call sits under, keyed by "METHOD route".
func (s *Server) callDepths(ctx context.Context, tr timeRange) (map[string][]int, error) {
	rendered := tr.render(`
SELECT sp.request_id, sp.offset_ms, sp.duration_ms, a.method, COALESCE(a.route, a.path) AS route, sp.kind
FROM spans sp JOIN (
  SELECT request_id, method, route, path FROM (
    SELECT request_id, method, route, path,
      ROW_NUMBER() OVER (PARTITION BY method, COALESCE(route, path) ORDER BY created_at DESC) AS k
    FROM api_logs
    WHERE created_at >= {{from}} AND created_at < {{to}} AND db_count > 0 AND request_id IS NOT NULL
  ) recent WHERE k <= `+strconv.Itoa(depthPerRoute)+` LIMIT `+strconv.Itoa(depthSample)+`
) a ON a.request_id = sp.request_id
WHERE sp.created_at >= ? AND sp.created_at < {{to}} AND sp.offset_ms IS NOT NULL`, s.q)
	rows, err := s.db.Query(ctx, rendered, tr.requestsFrom())
	if err != nil {
		return nil, dbErr(err)
	}
	type call struct {
		start, end float64
		wraps      bool // a custom or GraphQL span, which can contain calls
	}
	type req struct {
		route string
		calls []call
	}
	reqs := map[string]*req{}
	for _, row := range rows.Rows {
		id := toString(row[0])
		q := reqs[id]
		if q == nil {
			q = &req{route: toString(row[3]) + " " + toString(row[4])}
			reqs[id] = q
		}
		start, _ := asFloat(row[1])
		d, _ := asFloat(row[2])
		kind := toString(row[5])
		q.calls = append(q.calls, call{start, start + d, kind == "custom" || kind == "graphql" || kind == "background"})
	}
	out := map[string][]int{}
	const eps = 0.001
	for _, q := range reqs {
		// Longest first among calls that start together, so the outer one
		// (a query) is on the stack before the one inside it (its prepare).
		sort.Slice(q.calls, func(a, b int) bool {
			if q.calls[a].start != q.calls[b].start {
				return q.calls[a].start < q.calls[b].start
			}
			return q.calls[a].end > q.calls[b].end
		})
		var stack []float64 // end times of the wrapping spans still open
		deepest := 0
		for _, c := range q.calls {
			for len(stack) > 0 && stack[len(stack)-1] <= c.start+eps {
				stack = stack[:len(stack)-1]
			}
			// A call that outlives the wrapper on top runs beside it, not in it.
			for len(stack) > 0 && c.end > stack[len(stack)-1]+eps {
				stack = stack[:len(stack)-1]
			}
			deepest = max(deepest, len(stack)+1)
			if c.wraps {
				stack = append(stack, c.end)
			}
		}
		out[q.route] = append(out[q.route], deepest)
	}
	return out, nil
}

// handleQueryRegressions compares each statement's latency in equal windows
// before and after a deploy. The release is the one asked for (service and
// version), else the latest one with traffic on both sides.
func (s *Server) handleQueryRegressions(w http.ResponseWriter, r *http.Request) {
	kind, ok := spanKind(w, r, "db")
	if !ok {
		return
	}
	q := r.URL.Query()
	window := 24 * time.Hour
	if h, err := strconv.Atoi(q.Get("hours")); err == nil && h > 0 && h <= 24*14 {
		window = time.Duration(h) * time.Hour
	}
	releases, err := s.allReleases(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	// Newest first; the choice defaults to the newest deploy at least a few
	// minutes old, so there is something after it to compare.
	sort.Slice(releases, func(a, b int) bool { return releases[a].DeployedAt > releases[b].DeployedAt })
	var pick *release
	for i := range releases {
		rel := &releases[i]
		if q.Get("version") != "" {
			if rel.Version == q.Get("version") && rel.Service == q.Get("service") {
				pick = rel
				break
			}
			continue
		}
		if t := parseTS(rel.DeployedAt); !t.IsZero() && time.Since(t) > 5*time.Minute {
			pick = rel
			break
		}
	}
	resp := map[string]any{"releases": head(releases, 50), "release": pick}
	if pick == nil {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	at := parseTS(pick.DeployedAt)
	afterEnd := at.Add(window)
	if now := time.Now().UTC(); afterEnd.After(now) {
		afterEnd = now
	}
	span := afterEnd.Sub(at)
	before := timeRange{From: at.Add(-span), To: at}
	after := timeRange{From: at, To: afterEnd}
	resp["before"] = map[string]string{"from": before.From.Format(tsLayout), "to": before.To.Format(tsLayout)}
	resp["after"] = map[string]string{"from": after.From.Format(tsLayout), "to": after.To.Format(tsLayout)}

	side := func(ctx context.Context, tr timeRange) (*Result, error) {
		svc := ""
		args := []any{kind}
		if pick.Service != "" {
			svc = " AND service = ?"
			args = append(args, pick.Service)
		}
		return s.telemetryQuery(ctx, `
WITH sp AS (
  SELECT name, duration_ms, status,
    ROW_NUMBER() OVER (PARTITION BY name ORDER BY duration_ms) AS rn,
    COUNT(*) OVER (PARTITION BY name) AS n
  FROM spans WHERE created_at >= {{from}} AND created_at < {{to}} AND kind = ?`+svc+`
)
SELECT name, COUNT(*) AS calls, `+s.q.round("AVG(duration_ms)", 2)+` AS avg_ms,
  `+pctOf("duration_ms", "0.50")+` AS p50, `+pctOf("duration_ms", "0.95")+` AS p95,
  `+countIf("status = 'error'")+` AS errors
FROM sp GROUP BY name`, tr, args...)
	}
	var b, a *Result
	g, ctx := errgroup.WithContext(r.Context())
	g.Go(func() (err error) { b, err = side(ctx, before); return })
	g.Go(func() (err error) { a, err = side(ctx, after); return })
	if err := g.Wait(); err != nil {
		s.fail(w, err)
		return
	}

	type stat struct {
		calls, errors int64
		avg, p50, p95 any
	}
	read := func(res *Result) map[string]stat {
		out := map[string]stat{}
		for _, m := range rowMaps(res) {
			out[toString(m["name"])] = stat{toInt(m["calls"]), toInt(m["errors"]), m["avg_ms"], m["p50"], m["p95"]}
		}
		return out
	}
	bs, as := read(b), read(a)
	out := &Result{Columns: []string{"name", "calls_before", "calls_after", "p50_before", "p50_after", "p95_before", "p95_after", "change", "errors_before", "errors_after", "status"}}
	names := map[string]bool{}
	for n := range bs {
		names[n] = true
	}
	for n := range as {
		names[n] = true
	}
	for n := range names {
		x, y := bs[n], as[n]
		var change any
		status := "steady"
		switch {
		case x.calls == 0:
			status = "new"
		case y.calls == 0:
			status = "gone"
		default:
			pb, _ := asFloat(x.p95)
			pa, _ := asFloat(y.p95)
			if pb > 0 {
				c := (pa - pb) / pb * 100
				change = float64(int(c*10+0.5)) / 10
				// Small samples and sub-millisecond queries move a lot on
				// noise alone.
				if x.calls >= 5 && y.calls >= 5 && pa-pb >= 1 {
					if c >= 25 {
						status = "slower"
					} else if c <= -25 {
						status = "faster"
					}
				}
			}
		}
		out.Rows = append(out.Rows, []any{n, x.calls, y.calls, x.p50, y.p50, x.p95, y.p95, change, x.errors, y.errors, status})
	}
	rank := map[string]int{"slower": 0, "new": 1, "steady": 2, "faster": 3, "gone": 4}
	sort.Slice(out.Rows, func(i, j int) bool {
		ri, rj := rank[toString(out.Rows[i][10])], rank[toString(out.Rows[j][10])]
		if ri != rj {
			return ri < rj
		}
		ci, _ := asFloat(out.Rows[i][7])
		cj, _ := asFloat(out.Rows[j][7])
		return ci > cj
	})
	resp["statements"] = out
	writeJSON(w, http.StatusOK, resp)
}
