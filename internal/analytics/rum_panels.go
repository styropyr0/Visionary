package analytics

import (
	"errors"
	"math"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Frontend panels over client_events: Web Vitals, page and screen load,
// client-side API latency against the server's own, errors, sessions and
// breakdowns. Every panel takes ?app= (web, android, ios) to narrow it.

// vitalThresholds are Google's "good" and "poor" boundaries.
var vitalThresholds = []struct {
	name       string
	good, poor string
}{
	{"LCP", "2500", "4000"},
	{"INP", "200", "500"},
	{"CLS", "0.1", "0.25"},
	{"FCP", "1800", "3000"},
	{"TTFB", "800", "1800"},
}

// vitalRating is 0 (good), 1 (needs improvement) or 2 (poor). The server
// rates from value, so a client's own attrs.rating cannot skew the bars.
func vitalRating() string {
	s := "CASE name"
	for _, v := range vitalThresholds {
		s += " WHEN '" + v.name + "' THEN CASE WHEN value <= " + v.good + " THEN 0 WHEN value <= " + v.poor + " THEN 1 ELSE 2 END"
	}
	return s + " END"
}

const p75 = `MIN(CASE WHEN rn >= 0.75 * n THEN x END)`

const clientPercentiles = `
  MIN(CASE WHEN rn >= 0.50 * n THEN x END) AS p50,
  ` + p75 + ` AS p75,
  MIN(CASE WHEN rn >= 0.95 * n THEN x END) AS p95`

func (s *Server) rumPanels() map[string]string {
	q := s.q
	views := countIf("kind IN ('page_load', 'screen')")
	errs := countIf("kind = 'error'")
	return map[string]string{
		"summary": `
SELECT
  COUNT(DISTINCT session_id) AS sessions,
  COUNT(DISTINCT user_id) AS users,
  COALESCE(` + views + `, 0) AS views,
  COALESCE(` + errs + `, 0) AS errors,
  COUNT(DISTINCT CASE WHEN kind = 'error' THEN session_id END) AS error_sessions,
  COALESCE(` + countIf("kind = 'api'") + `, 0) AS api_calls,
  COALESCE(` + countIf("kind = 'api' AND (status = 0 OR status >= 500)") + `, 0) AS api_failures
FROM client_events WHERE ` + window + `{{app}}`,

		"vitals": `
WITH v AS (
  SELECT name, value AS x, ` + vitalRating() + ` AS rating,
    ROW_NUMBER() OVER (PARTITION BY name ORDER BY value) AS rn,
    COUNT(*) OVER (PARTITION BY name) AS n
  FROM client_events
  WHERE kind = 'web_vital' AND value IS NOT NULL AND name IN ('LCP', 'INP', 'CLS', 'FCP', 'TTFB') AND ` + window + `{{app}}
)
SELECT name, COUNT(*) AS samples, ` + p75 + ` AS p75,
  ` + countIf("rating = 0") + ` AS good,
  ` + countIf("rating = 1") + ` AS needs_improvement,
  ` + countIf("rating = 2") + ` AS poor
FROM v GROUP BY name`,

		"vitals_series": `
WITH b AS (
  SELECT {{t}} AS t, name, value AS x
  FROM client_events
  WHERE kind = 'web_vital' AND value IS NOT NULL AND name IN ('LCP', 'INP', 'CLS', 'FCP', 'TTFB') AND ` + window + `{{app}}
),
r AS (
  SELECT t, name, x,
    ROW_NUMBER() OVER (PARTITION BY t, name ORDER BY x) AS rn,
    COUNT(*) OVER (PARTITION BY t, name) AS n
  FROM b
)
SELECT t, name AS series, ` + p75 + ` AS value FROM r GROUP BY t, name ORDER BY t`,

		// Per page URL: p75 of LCP, INP and CLS, and of the full load.
		"pages": `
WITH v AS (
  SELECT url, CASE WHEN kind = 'page_load' THEN 'LOAD' ELSE name END AS metric,
    CASE WHEN kind = 'page_load' THEN duration_ms ELSE value END AS x
  FROM client_events
  WHERE url IS NOT NULL AND (kind = 'page_load' OR (kind = 'web_vital' AND name IN ('LCP', 'INP', 'CLS'))) AND ` + window + `{{app}}
),
r AS (
  SELECT url, metric, x,
    ROW_NUMBER() OVER (PARTITION BY url, metric ORDER BY x) AS rn,
    COUNT(*) OVER (PARTITION BY url, metric) AS n
  FROM v WHERE x IS NOT NULL
),
p AS (
  SELECT url, metric, COUNT(*) AS samples, ` + p75 + ` AS p75 FROM r GROUP BY url, metric
)
SELECT url,
  COALESCE(MAX(CASE WHEN metric = 'LOAD' THEN samples END), 0) AS loads,
  MAX(CASE WHEN metric = 'LCP' THEN p75 END) AS lcp_p75,
  MAX(CASE WHEN metric = 'INP' THEN p75 END) AS inp_p75,
  MAX(CASE WHEN metric = 'CLS' THEN p75 END) AS cls_p75,
  MAX(CASE WHEN metric = 'LOAD' THEN p75 END) AS load_p75
FROM p GROUP BY url
HAVING MAX(CASE WHEN metric = 'LCP' THEN p75 END) IS NOT NULL
ORDER BY lcp_p75 DESC
LIMIT 25`,

		// Mobile screens by time to first draw.
		"screens": `
WITH r AS (
  SELECT app, name, duration_ms AS x,
    ROW_NUMBER() OVER (PARTITION BY app, name ORDER BY duration_ms) AS rn,
    COUNT(*) OVER (PARTITION BY app, name) AS n
  FROM client_events
  WHERE kind = 'screen' AND duration_ms IS NOT NULL AND ` + window + `{{app}}
)
SELECT app, name AS screen, COUNT(*) AS views,` + clientPercentiles + `
FROM r GROUP BY app, name
ORDER BY p75 DESC
LIMIT 25`,

		"api_client": `
WITH r AS (
  SELECT name, status, duration_ms AS x,
    ROW_NUMBER() OVER (PARTITION BY name ORDER BY duration_ms) AS rn,
    COUNT(*) OVER (PARTITION BY name) AS n
  FROM client_events
  WHERE kind = 'api' AND name IS NOT NULL AND duration_ms IS NOT NULL AND ` + window + `{{app}}
)
SELECT name AS endpoint, COUNT(*) AS calls,` + clientPercentiles + `,
  ` + countIf("status = 0 OR status >= 500") + ` AS failures,
  ` + countIf("status = 0") + ` AS network_failures
FROM r GROUP BY name
ORDER BY calls DESC
LIMIT 50`,

		"api_server": `
WITH r AS (
  SELECT method, COALESCE(NULLIF(route, ''), path) AS route, duration_ms AS x,
    ROW_NUMBER() OVER (PARTITION BY method, COALESCE(NULLIF(route, ''), path) ORDER BY duration_ms) AS rn,
    COUNT(*) OVER (PARTITION BY method, COALESCE(NULLIF(route, ''), path)) AS n
  FROM api_logs WHERE ` + window + `
)
SELECT method, route, COUNT(*) AS requests, ` + p75 + ` AS p75
FROM r GROUP BY method, route
ORDER BY requests DESC
LIMIT 2000`,

		"errors": `
SELECT fingerprint, MAX(name) AS type, MAX(message) AS message,
  COUNT(*) AS events,
  COUNT(DISTINCT session_id) AS sessions,
  COUNT(DISTINCT user_id) AS users,
  MIN(app) AS app, COUNT(DISTINCT app) AS apps,
  MAX(app_version) AS app_version,
  MIN(created_at) AS first_seen, MAX(created_at) AS last_seen
FROM client_events
WHERE kind = 'error' AND ` + window + `{{app}}
GROUP BY fingerprint
ORDER BY events DESC
LIMIT 50`,

		"errors_series": `
SELECT {{t}} AS t, app AS series, COUNT(*) AS value
FROM client_events WHERE kind = 'error' AND ` + window + `{{app}}
GROUP BY 1, 2 ORDER BY 1`,

		"sessions": `
SELECT {{t}} AS t, COUNT(DISTINCT session_id) AS sessions, COUNT(DISTINCT user_id) AS users
FROM client_events WHERE ` + window + `{{app}}
GROUP BY 1 ORDER BY 1`,

		// {{by}} is one of rumBreakdowns, checked before rendering.
		"breakdown": `
WITH e AS (
  SELECT COALESCE({{by}}, '(unknown)') AS k, kind, session_id, user_id, duration_ms
  FROM client_events WHERE ` + window + `{{app}}
),
c AS (
  SELECT k,
    COUNT(DISTINCT session_id) AS sessions,
    COUNT(DISTINCT user_id) AS users,
    ` + views + ` AS views,
    ` + errs + ` AS errors,
    COUNT(DISTINCT CASE WHEN kind = 'error' THEN session_id END) AS error_sessions
  FROM e GROUP BY k
),
l AS (
  SELECT k, duration_ms AS x,
    ROW_NUMBER() OVER (PARTITION BY k ORDER BY duration_ms) AS rn,
    COUNT(*) OVER (PARTITION BY k) AS n
  FROM e WHERE kind IN ('page_load', 'screen') AND duration_ms IS NOT NULL
),
p AS (SELECT k, ` + p75 + ` AS load_p75 FROM l GROUP BY k)
SELECT c.k AS {{by}}, c.sessions, c.users, c.views, c.errors,
  ` + q.round("100.0 * c.error_sessions / NULLIF(c.sessions, 0)", 1) + ` AS error_session_percent,
  p.load_p75
FROM c LEFT JOIN p ON p.k = c.k
ORDER BY c.sessions DESC, c.views DESC
LIMIT 25`,
	}
}

var rumBreakdowns = map[string]bool{"app": true, "app_version": true, "device": true, "os": true, "country": true}

// rumPanelSQL renders the {{app}} and {{by}} placeholders from the request.
func (s *Server) rumPanelSQL(name string, r *http.Request) (string, error) {
	sql, ok := s.rumPanels()[name]
	if !ok {
		return "", errNotFound
	}
	q := r.URL.Query()
	clause := ""
	if app := strings.TrimSpace(q.Get("app")); app != "" {
		if !appName.MatchString(app) {
			return "", errors.New("invalid app")
		}
		clause = " AND app = '" + app + "'"
	}
	sql = strings.ReplaceAll(sql, "{{app}}", clause)
	if name == "breakdown" {
		by := q.Get("by")
		if by == "" {
			by = "app"
		}
		if !rumBreakdowns[by] {
			return "", errors.New("by must be app, app_version, device, os or country")
		}
		sql = strings.ReplaceAll(sql, "{{by}}", by)
	}
	return sql, nil
}

var errNotFound = errors.New("unknown panel")

func (s *Server) handleRUM(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("panel")
	tr, err := parseRange(r.URL.Query(), s.maxRangeDays())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if name == "api" {
		s.handleRUMAPI(w, r, tr)
		return
	}
	if name == "api_client" || name == "api_server" {
		writeError(w, http.StatusNotFound, "unknown panel")
		return
	}
	sql, err := s.rumPanelSQL(name, r)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errNotFound) {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}
	if name != "summary" && name != "vitals" {
		res, err := s.runTemplate(r.Context(), sql, tr, true)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
		return
	}
	// The headline numbers come with the previous period, for deltas.
	var (
		wg              sync.WaitGroup
		cur, prev       *Result
		curErr, prevErr error
	)
	wg.Add(2)
	go func() { defer wg.Done(); cur, curErr = s.runTemplate(r.Context(), sql, tr, true) }()
	go func() { defer wg.Done(); prev, prevErr = s.runTemplate(r.Context(), sql, tr.previous(), true) }()
	wg.Wait()
	if curErr != nil {
		s.fail(w, curErr)
		return
	}
	if prevErr != nil {
		s.logger.Warn("previous-period rum panel failed", "panel", name, "err", prevErr)
	}
	writeJSON(w, http.StatusOK, map[string]any{"current": cur, "previous": prev})
}

// handleRUMAPI puts each endpoint's latency as the client saw it next to
// the server's own latency for the same route from api_logs: the gap is
// network, queuing and client time, which the user feels but the server
// never sees.
func (s *Server) handleRUMAPI(w http.ResponseWriter, r *http.Request, tr timeRange) {
	clientSQL, err := s.rumPanelSQL("api_client", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	serverSQL, _ := s.rumPanelSQL("api_server", r)
	var (
		wg                  sync.WaitGroup
		client, server      *Result
		clientErr, serveErr error
	)
	wg.Add(2)
	go func() { defer wg.Done(); client, clientErr = s.runTemplate(r.Context(), clientSQL, tr, true) }()
	go func() { defer wg.Done(); server, serveErr = s.runTemplate(r.Context(), serverSQL, tr, true) }()
	wg.Wait()
	if clientErr != nil {
		s.fail(w, clientErr)
		return
	}
	type serverStat struct {
		requests int64
		p75      any
	}
	byRoute := map[string]serverStat{}
	if serveErr != nil {
		s.logger.Warn("server latency for the frontend view", "err", serveErr)
	} else {
		for _, row := range server.Rows {
			if len(row) < 4 {
				continue
			}
			key := strings.ToUpper(toString(row[0])) + " " + routeKey(toString(row[1]))
			if have, ok := byRoute[key]; !ok || toInt(row[2]) > have.requests {
				byRoute[key] = serverStat{requests: toInt(row[2]), p75: row[3]}
			}
		}
	}
	out := &Result{
		Columns: []string{"endpoint", "calls", "p50", "p75", "p95", "server_p75", "gap_ms", "server_requests", "failure_rate", "network_failures"},
		Rows:    make([][]any, 0, len(client.Rows)),
		SQL:     client.SQL + ";\n\n-- server side, matched by method and route\n" + serverSQL,
		Bucket:  client.Bucket, From: client.From, To: client.To,
		RowsRead: client.RowsRead, DurationMs: client.DurationMs, Truncated: client.Truncated,
	}
	if server != nil {
		out.RowsRead += server.RowsRead
		out.DurationMs = max(out.DurationMs, server.DurationMs)
	}
	for _, row := range client.Rows {
		if len(row) < 7 {
			continue
		}
		endpoint := toString(row[0])
		method, path, _ := strings.Cut(endpoint, " ")
		st, matched := byRoute[strings.ToUpper(method)+" "+routeKey(path)]
		var serverP75, gap, serverReq any
		if matched {
			serverP75, serverReq = st.p75, st.requests
			if c, ok := asFloat(row[3]); ok {
				if sv, ok := asFloat(st.p75); ok {
					gap = roundTo(c-sv, 1)
				}
			}
		}
		var failRate any
		if calls := toInt(row[1]); calls > 0 {
			failRate = roundTo(100*float64(toInt(row[5]))/float64(calls), 2)
		}
		out.Rows = append(out.Rows, []any{endpoint, row[1], row[2], row[3], row[4], serverP75, gap, serverReq, failRate, row[6]})
	}
	writeJSON(w, http.StatusOK, out)
}

var routeParam = regexp.MustCompile(`^(\{[^}]*\}|:[A-Za-z_]\w*|<[^>]*>|\*\w*)$`)

// routeKey writes a server route pattern (/trips/{id}, /trips/:tripId,
// /trips/<int:id>) or a concrete path the way the client SDKs name
// endpoints, /trips/:id, so the two can be matched.
func routeKey(route string) string {
	route = stripQuery(route)
	parts := strings.Split(route, "/")
	for i, seg := range parts {
		if routeParam.MatchString(seg) {
			parts[i] = ":id"
		}
	}
	out := normalizePath(strings.Join(parts, "/"))
	if len(out) > 1 {
		out = strings.TrimSuffix(out, "/")
	}
	return out
}

func roundTo(v float64, n int) float64 {
	p := math.Pow(10, float64(n))
	return math.Round(v*p) / p
}

var fingerprintPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)

// handleRUMError lists the latest occurrences of one error, with stacks.
func (s *Server) handleRUMError(w http.ResponseWriter, r *http.Request) {
	fp := r.PathValue("fingerprint")
	if !fingerprintPattern.MatchString(fp) {
		writeError(w, http.StatusBadRequest, "invalid fingerprint")
		return
	}
	tr, err := parseRange(r.URL.Query(), s.maxRangeDays())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	const sql = `SELECT created_at, app, app_version, version, device, os, country, url, name, message, stack, session_id, user_id
FROM client_events
WHERE fingerprint = ? AND kind = 'error' AND created_at >= ? AND created_at < ?
ORDER BY created_at DESC
LIMIT 20`
	start := time.Now()
	rows, err := s.db.Query(r.Context(), sql, fp, tr.From.Format(tsLayout), tr.To.Format(tsLayout))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &Result{
		Columns: rows.Columns, Rows: nonNilRows(rows.Rows), SQL: sql, RowsRead: rows.RowsRead,
		DurationMs: float64(time.Since(start).Microseconds()) / 1000,
		From:       tr.From.Format(tsLayout), To: tr.To.Format(tsLayout),
	})
}

func nonNilRows(rows [][]any) [][]any {
	if rows == nil {
		return [][]any{}
	}
	return rows
}
