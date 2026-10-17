package analytics

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"golang.org/x/sync/errgroup"
)

// Deploy markers and version comparison. A version is deployed at the first
// releases row written for it (one per instance start).

type release struct {
	Service    string `json:"service"`
	Version    string `json:"version"`
	DeployedAt string `json:"deployed_at"`
	Instances  int64  `json:"instances"`
	SDK        string `json:"sdk"`
	Runtime    string `json:"runtime"`
}

// allReleases lists every (service, version) with its deploy time, oldest
// first. The table holds one row per instance start and is kept forever, but
// stays small: a busy service deploys a few times a day.
func (s *Server) allReleases(ctx context.Context) ([]release, error) {
	rows, err := s.db.Query(ctx, `SELECT service, version, MIN(created_at) AS deployed_at,
  COUNT(DISTINCT instance) AS instances, MAX(sdk) AS sdk, MAX(runtime) AS runtime
FROM releases
GROUP BY service, version
ORDER BY deployed_at
LIMIT 2000`)
	if err != nil {
		return nil, dbErr(err)
	}
	out := make([]release, 0, len(rows.Rows))
	for _, r := range rows.Rows {
		if len(r) < 6 {
			continue
		}
		out = append(out, release{
			Service:    toString(r[0]),
			Version:    toString(r[1]),
			DeployedAt: toString(r[2]),
			Instances:  toInt(r[3]),
			SDK:        toString(r[4]),
			Runtime:    toString(r[5]),
		})
	}
	return out, nil
}

// handleReleases answers the deploys inside the range, for chart markers.
func (s *Server) handleReleases(w http.ResponseWriter, r *http.Request) {
	tr, ok := s.rangeOf(w, r)
	if !ok {
		return
	}
	all, err := s.allReleases(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	from, to := tr.From.Format(tsLayout), tr.To.Format(tsLayout)
	in := []release{}
	for _, rel := range all {
		if rel.DeployedAt >= from && rel.DeployedAt < to {
			in = append(in, rel)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"releases": in})
}

// handleVersions lists the versions that served requests in the range, newest
// first, for the compare picker.
func (s *Server) handleVersions(w http.ResponseWriter, r *http.Request) {
	tr, ok := s.rangeOf(w, r)
	if !ok {
		return
	}
	res, err := s.telemetryQuery(r.Context(), `
SELECT version, MAX(service) AS service, COUNT(*) AS requests,
  MIN(created_at) AS first_seen, MAX(created_at) AS last_seen
FROM api_logs
WHERE created_at >= {{from}} AND created_at < {{to}} AND version IS NOT NULL
GROUP BY version
ORDER BY first_seen DESC
LIMIT 50`, tr)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// compareSQL measures p50, p95, the 5xx rate and average DB time and calls
// per version, and per route too when byRoute.
func (s *Server) compareSQL(byRoute bool) string {
	group, cols := "version", "version"
	if byRoute {
		group = "version, method, COALESCE(route, path)"
		cols = "version, method, route"
	}
	return `
WITH r AS (
  SELECT version, method, COALESCE(route, path) AS route, status, duration_ms, db_ms, db_count,
    ROW_NUMBER() OVER (PARTITION BY ` + group + ` ORDER BY duration_ms) AS rn,
    COUNT(*) OVER (PARTITION BY ` + group + `) AS n
  FROM api_logs
  WHERE created_at >= {{from}} AND created_at < {{to}} AND version IN (?, ?)
)
SELECT ` + cols + `,
  COUNT(*) AS requests,
  MIN(CASE WHEN rn >= 0.50 * n THEN duration_ms END) AS p50,
  MIN(CASE WHEN rn >= 0.95 * n THEN duration_ms END) AS p95,
  ` + s.q.round("100.0 * "+countIf("status >= 500")+" / COUNT(*)", 2) + ` AS error_rate,
  ` + s.q.round("AVG(db_ms)", 2) + ` AS db_ms,
  ` + s.q.round("AVG(db_count)", 1) + ` AS db_calls
FROM r
GROUP BY ` + cols
}

var compareMeasures = []string{"requests", "p50", "p95", "error_rate", "db_ms", "db_calls"}

// handleCompareVersions puts two versions side by side, overall and per
// route, over the dashboard range.
func (s *Server) handleCompareVersions(w http.ResponseWriter, r *http.Request) {
	tr, ok := s.rangeOf(w, r)
	if !ok {
		return
	}
	a, b := strings.TrimSpace(r.URL.Query().Get("a")), strings.TrimSpace(r.URL.Query().Get("b"))
	if a == "" || b == "" || len(a) > 255 || len(b) > 255 {
		writeError(w, http.StatusBadRequest, "pick two versions (a and b)")
		return
	}
	var overall, routes *Result
	g, ctx := errgroup.WithContext(r.Context())
	g.Go(func() (err error) { overall, err = s.telemetryQuery(ctx, s.compareSQL(false), tr, a, b); return })
	g.Go(func() (err error) { routes, err = s.telemetryQuery(ctx, s.compareSQL(true), tr, a, b); return })
	if err := g.Wait(); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"overall": pivotVersions(overall, nil, a, b),
		"routes":  pivotVersions(routes, []string{"method", "route"}, a, b),
	})
}

// pivotVersions turns rows per (version, keys...) into one row per key with
// a_<measure> and b_<measure> columns, busiest first.
func pivotVersions(res *Result, keys []string, a, b string) *Result {
	cols := append([]string{}, keys...)
	for _, m := range compareMeasures {
		cols = append(cols, "a_"+m, "b_"+m)
	}
	type entry struct {
		key   []any
		a, b  map[string]any
		total float64
	}
	byKey := map[string]*entry{}
	var order []string
	for _, m := range rowMaps(res) {
		var parts []string
		var key []any
		for _, k := range keys {
			parts = append(parts, toString(m[k]))
			key = append(key, m[k])
		}
		id := strings.Join(parts, "\x00")
		e := byKey[id]
		if e == nil {
			e = &entry{key: key}
			byKey[id] = e
			order = append(order, id)
		}
		switch toString(m["version"]) {
		case a:
			e.a = m
		case b:
			e.b = m
		}
		e.total += toFloat(m["requests"])
	}
	sort.SliceStable(order, func(i, j int) bool { return byKey[order[i]].total > byKey[order[j]].total })
	rows := make([][]any, 0, len(order))
	for _, id := range order {
		e := byKey[id]
		row := append([]any{}, e.key...)
		for _, m := range compareMeasures {
			row = append(row, pick(e.a, m), pick(e.b, m))
		}
		rows = append(rows, row)
		if len(rows) == 100 {
			break
		}
	}
	return &Result{Columns: cols, Rows: rows, SQL: res.SQL, Bucket: res.Bucket, From: res.From, To: res.To, DurationMs: res.DurationMs, RowsRead: res.RowsRead}
}

func pick(m map[string]any, k string) any {
	if m == nil {
		return nil
	}
	return m[k]
}

// releaseOrder lists one service's versions in deploy order.
func releaseOrder(all []release, service string) []string {
	var out []string
	for _, rel := range all {
		if rel.Service == service {
			out = append(out, rel.Version)
		}
	}
	return out
}
