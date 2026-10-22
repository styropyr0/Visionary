package analytics

import (
	"context"
	"net/http"
	"regexp"
	"time"
)

// Spans, errors and releases written by the SDKs (sdks/SPEC.md, "Telemetry").
// The tables are created with the core schema in ensureSchema; this feature
// only reads them: the Errors view, request waterfalls, query and outbound
// call breakdowns, N+1 detection, deploy markers and version comparison.

func (s *Server) telemetrySchema() []string          { return nil }
func (s *Server) telemetryStart(ctx context.Context) {}

func (s *Server) telemetryRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/telemetry/errors", s.authed(s.handleErrorIssues))
	mux.Handle("GET /api/telemetry/errors/{fingerprint}", s.authed(s.handleErrorIssue))
	mux.Handle("GET /api/telemetry/requests/{request_id}", s.authed(s.handleRequestTelemetry))
	mux.Handle("GET /api/telemetry/spans", s.authed(s.handleSpanGroups))
	mux.Handle("GET /api/telemetry/spans/detail", s.authed(s.handleSpanDetail))
	mux.Handle("GET /api/telemetry/nplusone", s.authed(s.handleNPlusOne))
	mux.Handle("GET /api/telemetry/routes", s.authed(s.handleRouteCalls))
	mux.Handle("GET /api/telemetry/releases", s.authed(s.handleReleases))
	mux.Handle("GET /api/telemetry/versions", s.authed(s.handleVersions))
	mux.Handle("GET /api/telemetry/compare", s.authed(s.handleCompareVersions))
}

func (s *Server) telemetryPrompt() string {
	return `Table spans: calls a service made while handling a request (or in background work). Stored only for requests slower than the SDK's threshold (100 ms by default) or answering 5xx, at most 100 per request, so they describe slow and failed requests, not all traffic.
- id, created_at TEXT (span start, same format), request_id TEXT (NULL outside a request), service TEXT, instance TEXT
- kind TEXT: 'db', 'http', 'cache', 'queue' or 'custom'
- name TEXT: low-cardinality grouping key. db: first keyword + main table ('SELECT users', 'INSERT bookings', 'BEGIN'); http: method + host ('POST api.stripe.com')
- target TEXT (db: system/database, http: host); detail TEXT (db: the statement with literals replaced by ?; http: method and URL without query string)
- offset_ms REAL (start relative to the request start), duration_ms REAL, status TEXT ('ok' or 'error'), error TEXT, row_count INTEGER
- Join to requests on spans.request_id = api_logs.request_id (indexed on both sides). Filter api_logs.created_at to the same window, starting a few minutes earlier, because a request is logged at its start, before its calls.
- N+1 queries are one name repeated 10 or more times in one request. Find them with WITH per AS (SELECT request_id, name, COUNT(*) AS calls, SUM(duration_ms) AS ms FROM spans WHERE <window> AND request_id IS NOT NULL GROUP BY request_id, name HAVING COUNT(*) >= 10), then join per to api_logs on request_id and group by route and name.
- Every request, not only slow ones, has totals in api_logs: db_count and db_ms, http_count and http_ms. Use those for how much time goes to the database across all traffic (SUM(db_ms) / SUM(duration_ms)), and spans for which statement or host it is.

Table errors: exceptions recorded by the SDKs, either unhandled (handled = 0: the request crashed) or reported by the application (handled = 1).
- id, created_at TEXT, service TEXT, instance TEXT, version TEXT (the release), request_id TEXT (join to api_logs.request_id), method, route, path TEXT, user_id TEXT
- type TEXT (exception class, e.g. 'ValueError', '*pgconn.PgError'), message TEXT, stack TEXT, runtime TEXT
- fingerprint TEXT groups occurrences of the same error (same type and code location); an "issue" is one fingerprint. Occurrences are COUNT(*), affected users COUNT(DISTINCT user_id).
- At most 20 rows per fingerprint per minute per process are kept, so very frequent errors are undercounted.

Table releases: one row each time a service instance starts with a known version (a deploy).
- created_at TEXT, service TEXT, instance TEXT, version TEXT, sdk TEXT, sdk_version TEXT, runtime TEXT
- A version was deployed at the MIN(created_at) of its rows: SELECT service, version, MIN(created_at) AS deployed_at FROM releases GROUP BY service, version. To check whether a deploy caused a change, compare api_logs or errors before and after deployed_at, or group api_logs by version.`
}

// errorFingerprint accepts the SDKs' 16 hex characters and the similar keys
// other writers produce, nothing more.
var errorFingerprint = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)

// telemetryQuery renders the dashboard placeholders ({{from}}, {{to}}, {{t}})
// and runs one of this file set's own statements with bound arguments.
func (s *Server) telemetryQuery(ctx context.Context, sql string, tr timeRange, args ...any) (*Result, error) {
	rendered := tr.render(sql, s.q)
	rows, err := s.db.Query(ctx, rendered, args...)
	if err != nil {
		return nil, dbErr(err)
	}
	return newResult(rows, s.cfg.MaxRows, rendered, tr), nil
}

// rangeOf parses the dashboard range, answering 400 itself when it is bad.
func (s *Server) rangeOf(w http.ResponseWriter, r *http.Request) (timeRange, bool) {
	tr, err := parseRange(r.URL.Query(), s.cfg.MaxRangeDays)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return tr, false
	}
	return tr, true
}

// joinLead is how much earlier than its calls a request may have started; joins
// from spans back to api_logs start the request window this much earlier.
const joinLead = 10 * time.Minute

func (tr timeRange) requestsFrom() string { return tr.From.Add(-joinLead).Format(tsLayout) }

// rowMaps turns a result into one map per row, keyed by column.
func rowMaps(res *Result) []map[string]any {
	out := make([]map[string]any, 0, len(res.Rows))
	for _, row := range res.Rows {
		m := make(map[string]any, len(res.Columns))
		for i, c := range res.Columns {
			if i < len(row) {
				m[c] = row[i]
			}
		}
		out = append(out, m)
	}
	return out
}

func toFloat(v any) float64 {
	f, _ := asFloat(v)
	return f
}
