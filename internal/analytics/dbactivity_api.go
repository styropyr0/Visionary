package analytics

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/styropyr0/visionary/sdks/go"
)

// The Databases view's endpoints over the tables of dbactivity.go, and the
// alert metrics over db_stats.

func (s *Server) dbActivityRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/databases", s.authed(s.handleDatabases))
	mux.Handle("GET /api/databases/sessions", s.authed(s.handleDBSessions))
	mux.Handle("GET /api/databases/statements", s.authed(s.handleDBStatements))
	mux.Handle("GET /api/databases/panels/{panel}", s.authed(s.handleDBPanel))
}

// dbFilter narrows to the host and server query parameters, validated
// before they are inlined. prefix qualifies the columns ("s." or "").
func dbFilter(r *http.Request, prefix string) (string, error) {
	var b strings.Builder
	for _, k := range []string{"host", "server"} {
		v := strings.TrimSpace(r.URL.Query().Get(k))
		if v == "" {
			continue
		}
		ok := dbServerName.MatchString(v)
		if k == "host" {
			ok = instanceName.MatchString(v)
		}
		if !ok {
			return "", errors.New("invalid " + k + " name")
		}
		b.WriteString(" AND " + prefix + k + " = '" + v + "'")
	}
	return b.String(), nil
}

// dbListColumns is GET /api/databases, one row per agent and server.
var dbListColumns = []string{
	"host", "server", "engine", "version", "status", "notes", "last_seen", "sampled_at",
	"connections", "max_connections", "active", "idle_in_tx", "waiting",
	"longest_query_s", "longest_tx_s", "longest_idle_tx_s", "deadlocks", "peak_waiting", "report_s",
}

// GET /api/databases?from=&to=: every server the agents read, with its
// latest sample and the deadlocks and worst lock waits in the range. status
// is online, stale or offline like a host's, or error when the agent
// reports but cannot read the server.
func (s *Server) handleDatabases(w http.ResponseWriter, r *http.Request) {
	tr, err := parseRange(r.URL.Query(), s.maxRangeDays())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := r.Context()
	out := emptyResult(dbListColumns)
	inv, err := s.db.Query(ctx, `SELECT host, server, engine, version, notes, last_seen FROM db_servers ORDER BY server, host LIMIT 500`)
	if err != nil {
		if missingTable(err, "db_servers") {
			writeJSON(w, http.StatusOK, out)
			return
		}
		s.fail(w, err)
		return
	}
	now := time.Now().UTC()
	recent := now.Add(-time.Hour).Format(tsLayout)
	latest := s.dbLookup(ctx, `
SELECT d.host, d.server, d.created_at, d.connections, d.max_connections, d.active, d.idle_in_tx, d.waiting,
  d.longest_query_s, d.longest_tx_s, d.longest_idle_tx_s
FROM db_stats AS d
JOIN (SELECT host, server, MAX(created_at) AS at FROM db_stats WHERE created_at >= ? GROUP BY host, server) AS l
  ON l.host = d.host AND l.server = d.server AND l.at = d.created_at`, recent)
	ranged := s.dbLookup(ctx, `
SELECT host, server, SUM(deadlocks), MAX(waiting) FROM db_stats
WHERE created_at >= ? AND created_at < ? GROUP BY host, server`, tr.From.Format(tsLayout), tr.To.Format(tsLayout))
	h := s.hosts()
	for _, row := range inv.Rows {
		host, server := toString(row[0]), toString(row[1])
		key := host + "\x00" + server
		l, rg := latest[key], ranged[key]
		cell := func(vals []any, i int) any {
			if i < len(vals) {
				return vals[i]
			}
			return nil
		}
		ri := h.reportInterval(host)
		status, _ := hostStatus(toString(row[5]), ri, now)
		sampled := toString(cell(l, 0))
		if status == "online" {
			if t, err := time.Parse(tsLayout, sampled); err != nil || now.Sub(t).Seconds() > 3*ri {
				status = "error" // reporting, but no recent sample
			}
		}
		out.Rows = append(out.Rows, []any{
			host, server, row[2], row[3], status, row[4], row[5], cell(l, 0),
			cell(l, 1), cell(l, 2), cell(l, 3), cell(l, 4), cell(l, 5), cell(l, 6), cell(l, 7), cell(l, 8),
			cell(rg, 0), cell(rg, 1), ri,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// dbLookup runs a query whose first two columns are host and server, and
// returns the other columns by "host\x00server".
func (s *Server) dbLookup(ctx context.Context, sql string, args ...any) map[string][]any {
	out := map[string][]any{}
	res, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		if ctx.Err() == nil && !missingTable(err, "db_") {
			s.logger.Warn("databases lookup", "err", err)
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

var dbSessionColumns = "s.host, s.server, s.pid, s.username, s.db_name, s.client, s.app, s.state, s.query, s.query_start, " +
	"s.elapsed_s, s.tx_age_s, s.wait_event, s.blocked_by, s.lock_type, s.lock_mode, s.lock_object, s.seen_at"

// GET /api/databases/sessions?live=1&host=&server=: the sessions running at
// each server's latest sample. Without live, the longest statement
// executions seen in from..to (one row each, however many samples saw it).
func (s *Server) handleDBSessions(w http.ResponseWriter, r *http.Request) {
	filter, err := dbFilter(r, "")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sfilter, _ := dbFilter(r, "s.")
	var q string
	var args []any
	if r.URL.Query().Get("live") == "1" {
		// A live session was seen by the server's latest sample, which is
		// the latest db_stats row: the agent stamps both with one time.
		since := time.Now().UTC().Add(-15 * time.Minute).Format(tsLayout)
		q = `SELECT ` + dbSessionColumns + `
FROM db_sessions AS s
JOIN (SELECT host, server, MAX(created_at) AS at FROM db_stats WHERE created_at >= ?` + filter + ` GROUP BY host, server) AS l
  ON l.host = s.host AND l.server = s.server AND l.at = s.seen_at
WHERE s.created_at >= ?` + sfilter + `
ORDER BY s.elapsed_s DESC LIMIT 1000`
		args = []any{since, since}
	} else {
		tr, err := parseRange(r.URL.Query(), s.maxRangeDays())
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		q = `SELECT host, server, pid, MAX(username) AS username, MAX(db_name) AS db_name, MAX(client) AS client, MAX(app) AS app,
  MAX(state) AS state, query, query_start, MAX(elapsed_s) AS elapsed_s, MAX(tx_age_s) AS tx_age_s, MAX(wait_event) AS wait_event,
  MAX(blocked_by) AS blocked_by, MAX(lock_type) AS lock_type, MAX(lock_mode) AS lock_mode, MAX(lock_object) AS lock_object,
  MIN(seen_at) AS first_seen, MAX(seen_at) AS seen_at
FROM db_sessions
WHERE created_at >= ? AND created_at < ?` + filter + `
GROUP BY host, server, pid, query, query_start
ORDER BY elapsed_s DESC LIMIT 200`
		args = []any{tr.From.Format(tsLayout), tr.To.Format(tsLayout)}
	}
	res, err := s.db.Query(r.Context(), q, args...)
	if err != nil {
		if missingTable(err, "db_s") {
			writeJSON(w, http.StatusOK, emptyResult([]string{"host", "server", "pid"}))
			return
		}
		s.fail(w, err)
		return
	}
	// name is the statement's span name, for links to the Queries view.
	out := dbResult(res, q)
	qi := indexOf(out.Columns, "query")
	out.Columns = append(out.Columns, "name")
	for i, row := range out.Rows {
		name := any(nil)
		if text := toString(row[qi]); text != "" {
			name = sdk.SQLName(text)
		}
		out.Rows[i] = append(row, name)
	}
	writeJSON(w, http.StatusOK, out)
}

// dbTrendPoints is how many buckets a statement's trend has.
const dbTrendPoints = 24

// GET /api/databases/statements?from=&to=&host=&server=: the statements
// that took the most time in the range, summed from the per-read deltas, each
// with its time per bucket (trend) and its share of all statement time.
func (s *Server) handleDBStatements(w http.ResponseWriter, r *http.Request) {
	tr, err := parseRange(r.URL.Query(), s.maxRangeDays())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	filter, err := dbFilter(r, "")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := r.Context()
	minutes := max(tr.To.Sub(tr.From).Minutes(), 1)
	list, err := s.telemetryQuery(ctx, `
SELECT server, query_id, MAX(name) AS name, MAX(query) AS query, MAX(db_name) AS db_name, MAX(engine) AS engine,
  SUM(calls) AS calls,
  `+s.q.round("SUM(calls) / "+strconv.FormatFloat(minutes, 'f', 3, 64), 2)+` AS per_min,
  `+s.q.round("SUM(total_ms)", 1)+` AS total_ms,
  `+s.q.round("SUM(total_ms) / NULLIF(SUM(calls), 0)", 2)+` AS mean_ms,
  MAX(max_ms) AS max_ms,
  SUM(row_count) AS row_count, SUM(rows_examined) AS rows_examined, SUM(errors) AS errors,
  MIN(created_at) AS first_seen, MAX(created_at) AS last_seen
FROM db_statements WHERE created_at >= {{from}} AND created_at < {{to}}`+filter+`
GROUP BY server, query_id
ORDER BY total_ms DESC
LIMIT 100`, tr)
	if err != nil {
		if missingTable(err, "db_statements") {
			writeJSON(w, http.StatusOK, emptyResult([]string{"server", "query_id"}))
			return
		}
		s.fail(w, err)
		return
	}
	all, err := s.telemetryQuery(ctx, `SELECT `+s.q.round("SUM(total_ms)", 1)+` FROM db_statements WHERE created_at >= {{from}} AND created_at < {{to}}`+filter, tr)
	allMs := 0.0
	if err == nil && len(all.Rows) > 0 && len(all.Rows[0]) > 0 {
		allMs, _ = cellFloat(all.Rows[0][0])
	}

	// The trend of the listed statements, asked in batches that stay under
	// D1's 100 bound parameters.
	bucket := max(int64(tr.To.Sub(tr.From).Seconds())/dbTrendPoints, 60)
	start := tr.From.Unix() / bucket * bucket
	nb := int((tr.To.Unix()-start)/bucket) + 1
	ttr := tr
	ttr.Bucket = bucket
	si, qi, ti := indexOf(list.Columns, "server"), indexOf(list.Columns, "query_id"), indexOf(list.Columns, "total_ms")
	trends := map[string][]float64{}
	var ids []any
	for _, row := range list.Rows {
		ids = append(ids, toString(row[qi]))
	}
	for from := 0; from < len(ids); from += 90 {
		batch := ids[from:min(from+90, len(ids))]
		res, err := s.telemetryQuery(ctx, `SELECT server, query_id, {{t}} AS t, SUM(total_ms) AS total_ms FROM db_statements
WHERE created_at >= {{from}} AND created_at < {{to}}`+filter+` AND query_id IN (`+marks(len(batch))+`)
GROUP BY server, query_id, {{t}}`, ttr, batch...)
		if err != nil {
			s.fail(w, err)
			return
		}
		for _, row := range res.Rows {
			key := toString(row[0]) + "\x00" + toString(row[1])
			i := int((toInt(row[2]) - start) / bucket)
			if i < 0 || i >= nb {
				continue
			}
			if trends[key] == nil {
				trends[key] = make([]float64, nb)
			}
			v, _ := cellFloat(row[3])
			trends[key][i] += v
		}
	}
	list.Columns = append(list.Columns, "share", "trend")
	for i, row := range list.Rows {
		key := toString(row[si]) + "\x00" + toString(row[qi])
		t := trends[key]
		if t == nil {
			t = make([]float64, nb)
		}
		share := any(nil)
		if v, ok := cellFloat(row[ti]); ok && allMs > 0 {
			share = roundCell(100*v/allMs, 1)
		}
		list.Rows[i] = append(row, share, t)
	}
	list.Bucket = bucket
	writeJSON(w, http.StatusOK, list)
}

// dbPanel is one chart: per server (series) across servers, and the named
// columns for one server.
type dbPanel struct{ all, one string }

const dbWindow = `created_at >= {{from}} AND created_at < {{to}}{{filter}}`

func dbPanels(q sqlDialect) map[string]dbPanel {
	perServer := func(table, expr string) string {
		return `SELECT {{t}} AS t, server AS series, ` + expr + ` AS value
FROM ` + table + ` WHERE ` + dbWindow + `
GROUP BY {{t}}, server ORDER BY t`
	}
	one := func(table, cols string) string {
		return `SELECT {{t}} AS t, ` + cols + `
FROM ` + table + ` WHERE ` + dbWindow + `
GROUP BY {{t}} ORDER BY t`
	}
	return map[string]dbPanel{
		"connections": {perServer("db_stats", "MAX(connections)"),
			one("db_stats", `MAX(connections) AS connections, MAX(active) AS active, MAX(idle_in_tx) AS idle_in_transaction, MAX(max_connections) AS max_connections`)},
		"connections_percent": {perServer("db_stats", q.round("MAX(100.0 * connections / NULLIF(max_connections, 0))", 1)),
			one("db_stats", q.round("MAX(100.0 * connections / NULLIF(max_connections, 0))", 1)+` AS used`)},
		"longest": {perServer("db_stats", "MAX(longest_query_s) * 1000"),
			one("db_stats", `MAX(longest_query_s) * 1000 AS longest_query, MAX(longest_tx_s) * 1000 AS oldest_transaction, MAX(longest_idle_tx_s) * 1000 AS longest_idle_in_transaction`)},
		"waiting": {perServer("db_stats", "MAX(waiting)"),
			one("db_stats", `MAX(waiting) AS waiting_on_locks, SUM(deadlocks) AS deadlocks`)},
		"transactions": {perServer("db_stats", q.round("SUM(commits) * 1.0 / {{bucket}}", 2)),
			one("db_stats", q.round("SUM(commits) * 1.0 / {{bucket}}", 2)+` AS commits, `+q.round("SUM(rollbacks) * 1.0 / {{bucket}}", 2)+` AS rollbacks`)},
		"statement_time": {perServer("db_statements", q.round("SUM(total_ms)", 1)),
			one("db_statements", q.round("SUM(total_ms)", 1)+` AS total_ms`)},
		"statement_calls": {perServer("db_statements", q.round("SUM(calls) * 1.0 / {{bucket}}", 2)),
			one("db_statements", q.round("SUM(calls) * 1.0 / {{bucket}}", 2)+` AS calls`)},
	}
}

// GET /api/databases/panels/{panel}?from=&to=&host=&server=
func (s *Server) handleDBPanel(w http.ResponseWriter, r *http.Request) {
	p, ok := dbPanels(s.q)[r.PathValue("panel")]
	if !ok {
		writeError(w, http.StatusNotFound, "unknown panel")
		return
	}
	tr, err := parseRange(r.URL.Query(), s.maxRangeDays())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	filter, err := dbFilter(r, "")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sql := p.all
	if r.URL.Query().Get("server") != "" {
		sql = p.one
	}
	res, err := s.execTemplate(r.Context(), strings.ReplaceAll(sql, "{{filter}}", filter), tr, true)
	if err != nil {
		if !missingTable(err, "db_st") {
			s.fail(w, err)
			return
		}
		res = emptyResult([]string{"t"})
	}
	writeJSON(w, http.StatusOK, res)
}

// Alert metrics over db_stats, per server.

var dbScopes = []string{"db_server", "host"}

func databaseMetrics() []metricDef {
	m := func(id, label, unit, desc, agg, op string, th float64) metricDef {
		return metricDef{ID: id, Label: label, Group: "database", Unit: unit, Description: desc, Scopes: dbScopes,
			Op: op, Threshold: th, UpIsBad: true, table: "db_stats", perInstance: true, keyCol: "server",
			agg: func(int64) string { return agg }}
	}
	return []metricDef{
		m("db_long_query", "Long-running query", "seconds", "The longest a statement had been running at any sample in the window, per database server.", "MAX(longest_query_s)", ">", 300),
		m("db_idle_in_tx", "Session idle in a transaction", "seconds", "The longest a session sat idle inside an open transaction in the window. It holds its locks and blocks vacuum (PostgreSQL) or purge (MySQL) meanwhile.", "MAX(longest_idle_tx_s)", ">", 300),
		m("db_lock_waits", "Sessions waiting on locks", "count", "Most sessions waiting on a lock at once in the window: a blocking chain.", "MAX(waiting)", ">=", 1),
		m("db_connections", "Database connections used", "percent", "Highest share of max_connections in use in the window. At 100% new connections are refused.", "MAX(100.0 * connections / NULLIF(max_connections, 0))", ">", 85),
		m("db_deadlocks", "Deadlocks", "count", "Deadlocks the server detected and broke by rolling a transaction back, summed over the window.", "SUM(deadlocks)", ">", 0),
	}
}
