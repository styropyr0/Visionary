package analytics

import (
	"net/http"
	"sort"
	"strconv"

	"golang.org/x/sync/errgroup"
)

// Spans: one request's waterfall, the busiest statements and outbound hosts,
// N+1 suspects, and each route's database and HTTP totals from api_logs.

// nPlusOneCalls is how often one span name must repeat in a request to be
// flagged (sdks/SPEC.md, "Spans").
const nPlusOneCalls = "10"

const maxRequestSpans = 500

// handleRequestTelemetry answers a request's spans and errors, and the id of
// its log entry so error occurrences can link to it.
func (s *Server) handleRequestTelemetry(w http.ResponseWriter, r *http.Request) {
	rid := r.PathValue("request_id")
	if rid == "" || len(rid) > 200 {
		writeError(w, http.StatusBadRequest, "invalid request id")
		return
	}
	var logID, spans, errs, lines *Result
	tr := timeRange{}
	g, ctx := errgroup.WithContext(r.Context())
	g.Go(func() (err error) {
		logID, err = s.telemetryQuery(ctx, `SELECT MAX(id) AS id FROM api_logs WHERE request_id = ?`, tr, rid)
		return
	})
	g.Go(func() (err error) {
		spans, err = s.telemetryQuery(ctx, `
SELECT id, created_at, kind, name, target, detail, offset_ms, duration_ms, status, error, row_count
FROM spans WHERE request_id = ?
ORDER BY offset_ms, created_at, id
LIMIT `+strconv.Itoa(maxRequestSpans), tr, rid)
		return
	})
	g.Go(func() (err error) {
		errs, err = s.telemetryQuery(ctx, `
SELECT id, created_at, type, message, fingerprint, handled
FROM errors WHERE request_id = ?
ORDER BY created_at, id
LIMIT 20`, tr, rid)
		return
	})
	g.Go(func() (err error) {
		// The lines the request logged, for the waterfall and its Logs tab.
		q, args := `SELECT id, created_at, level, severity, logger, message, attrs, code_file, code_line, pattern
FROM app_logs WHERE request_id = ?`, []any{rid}
		if scope, sargs := s.logScope(r); scope != "" {
			q, args = q+" AND "+scope, append(args, sargs...)
		}
		lines, err = s.telemetryQuery(ctx, q+` ORDER BY created_at, id LIMIT 300`, tr, args...)
		if err != nil && missingTable(err, "app_logs") {
			lines, err = &Result{Columns: []string{}, Rows: [][]any{}}, nil
		}
		if lines != nil {
			s.maskLogRows(r, lines)
		}
		return
	})
	if err := g.Wait(); err != nil {
		s.fail(w, err)
		return
	}
	var id any
	if len(logID.Rows) > 0 && len(logID.Rows[0]) > 0 {
		id = logID.Rows[0][0]
	}
	writeJSON(w, http.StatusOK, map[string]any{"log_id": id, "spans": spans, "errors": errs, "logs": lines})
}

// handleSpanGroups ranks span names of one kind (db by default) by total
// time, with p95, errors and the routes they run in.
func (s *Server) handleSpanGroups(w http.ResponseWriter, r *http.Request) {
	tr, ok := s.rangeOf(w, r)
	if !ok {
		return
	}
	kind := r.URL.Query().Get("kind")
	switch kind {
	case "":
		kind = "db"
	case "db", "http", "cache", "queue", "custom", "graphql", "background":
	default:
		writeError(w, http.StatusBadRequest, "kind must be db, http, cache, queue, custom, graphql or background")
		return
	}
	var groups, routes *Result
	g, ctx := errgroup.WithContext(r.Context())
	g.Go(func() (err error) {
		groups, err = s.telemetryQuery(ctx, `
WITH sp AS (
  SELECT name, target, detail, duration_ms, status, request_id,
    ROW_NUMBER() OVER (PARTITION BY name ORDER BY duration_ms) AS rn,
    COUNT(*) OVER (PARTITION BY name) AS n
  FROM spans
  WHERE created_at >= {{from}} AND created_at < {{to}} AND kind = ?
)
SELECT name,
  MAX(target) AS target,
  COUNT(*) AS calls,
  `+s.q.round("SUM(duration_ms)", 1)+` AS total_ms,
  `+s.q.round("AVG(duration_ms)", 2)+` AS avg_ms,
  MIN(CASE WHEN rn >= 0.95 * n THEN duration_ms END) AS p95,
  MAX(duration_ms) AS max_ms,
  `+countIf("status = 'error'")+` AS errors,
  COUNT(DISTINCT request_id) AS requests,
  `+countIf("request_id IS NULL")+` AS background,
  MAX(CASE WHEN rn = n THEN detail END) AS slowest_detail
FROM sp
GROUP BY name
ORDER BY total_ms DESC
LIMIT 25`, tr, kind)
		return
	})
	g.Go(func() (err error) {
		routes, err = s.telemetryQuery(ctx, `
SELECT sp.name, a.method, COALESCE(a.route, a.path) AS route, COUNT(*) AS calls
FROM spans sp JOIN api_logs a ON a.request_id = sp.request_id
WHERE sp.created_at >= {{from}} AND sp.created_at < {{to}} AND sp.kind = ?
  AND a.created_at >= ? AND a.created_at < {{to}}
GROUP BY sp.name, a.method, COALESCE(a.route, a.path)`, tr, kind, tr.requestsFrom())
		return
	})
	if err := g.Wait(); err != nil {
		s.fail(w, err)
		return
	}

	// The top three routes of each name, as "METHOD route".
	type routeCalls struct {
		route string
		calls int64
	}
	byName := map[string][]routeCalls{}
	for _, m := range rowMaps(routes) {
		name := toString(m["name"])
		byName[name] = append(byName[name], routeCalls{toString(m["method"]) + " " + toString(m["route"]), toInt(m["calls"])})
	}
	groups.Columns = append(groups.Columns, "routes")
	for i, row := range groups.Rows {
		rc := byName[toString(row[0])]
		sort.Slice(rc, func(a, b int) bool { return rc[a].calls > rc[b].calls })
		top := []string{}
		for _, x := range head(rc, 3) {
			top = append(top, x.route)
		}
		groups.Rows[i] = append(row, top)
	}
	writeJSON(w, http.StatusOK, groups)
}

// handleNPlusOne lists routes where one span name repeats many times per
// request: the classic N+1 query, or a loop of outbound calls.
func (s *Server) handleNPlusOne(w http.ResponseWriter, r *http.Request) {
	tr, ok := s.rangeOf(w, r)
	if !ok {
		return
	}
	res, err := s.telemetryQuery(r.Context(), `
WITH per AS (
  SELECT request_id, name, MAX(kind) AS kind, COUNT(*) AS calls, SUM(duration_ms) AS ms
  FROM spans
  WHERE created_at >= {{from}} AND created_at < {{to}} AND request_id IS NOT NULL
  GROUP BY request_id, name
),
j AS (
  SELECT a.method, COALESCE(a.route, a.path) AS route, per.name, per.kind, per.calls, per.ms,
    a.duration_ms, per.request_id
  FROM per JOIN api_logs a ON a.request_id = per.request_id
  WHERE a.created_at >= ? AND a.created_at < {{to}}
)
SELECT method, route, name, MAX(kind) AS kind,
  COUNT(*) AS requests,
  `+countIf("calls >= "+nPlusOneCalls)+` AS flagged,
  `+s.q.round("AVG(calls)", 1)+` AS avg_calls,
  MAX(calls) AS max_calls,
  `+s.q.round("AVG(ms)", 1)+` AS avg_ms,
  `+s.q.round("100.0 * SUM(ms) / NULLIF(SUM(duration_ms), 0)", 1)+` AS time_share,
  MAX(CASE WHEN calls >= `+nPlusOneCalls+` THEN request_id END) AS example_request
FROM j
GROUP BY method, route, name
HAVING MAX(calls) >= `+nPlusOneCalls+`
ORDER BY flagged DESC, avg_calls DESC
LIMIT 25`, tr, tr.requestsFrom())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleRouteCalls is each route's share of time in database and outbound
// HTTP calls, from the per-request totals. Rows from older SDKs have NULL
// totals and are left out of both sides of each share.
func (s *Server) handleRouteCalls(w http.ResponseWriter, r *http.Request) {
	tr, ok := s.rangeOf(w, r)
	if !ok {
		return
	}
	share := func(col string) string {
		return s.q.round("100.0 * SUM("+col+") / NULLIF(SUM(CASE WHEN "+col+" IS NOT NULL THEN duration_ms END), 0)", 1)
	}
	res, err := s.telemetryQuery(r.Context(), `
SELECT method, COALESCE(route, path) AS route,
  COUNT(db_ms) AS measured,
  `+share("db_ms")+` AS db_share,
  `+s.q.round("AVG(db_count)", 1)+` AS db_calls,
  `+s.q.round("AVG(db_ms)", 2)+` AS db_ms,
  `+share("http_ms")+` AS http_share,
  `+s.q.round("AVG(http_count)", 1)+` AS http_calls
FROM api_logs
WHERE created_at >= {{from}} AND created_at < {{to}}
GROUP BY method, COALESCE(route, path)
HAVING COUNT(db_ms) > 0 OR COUNT(http_ms) > 0`, tr)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
