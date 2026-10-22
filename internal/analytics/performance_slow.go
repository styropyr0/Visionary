package analytics

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// The slowest requests, with the filters the panel offers: routes left out
// (a nightly export is slow by design and would fill the list) and, when
// grouped, one row per route to expand. It reads raw rows: the rollup
// shortcut of the plain "slowest" panel assumes nothing is filtered out.

const (
	maxSlowExcludes = 50
	slowRequestRows = 25
	slowGroupRows   = 50
)

var httpMethods = map[string]bool{"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "HEAD": true, "OPTIONS": true}

// slowFilters turns the query into WHERE terms with bound arguments.
// exclude is repeatable: a route glob (* matches any run, so /api/admin/*
// covers the whole tree), optionally led by a method ("POST /api/export").
// method and route pick one route exactly, for an expanded group.
func slowFilters(q url.Values) ([]string, []any, error) {
	var where []string
	var args []any
	excludes := q["exclude"]
	if len(excludes) > maxSlowExcludes {
		return nil, nil, &queryError{errors.New("too many exclusions")}
	}
	for _, ex := range excludes {
		ex = strings.TrimSpace(ex)
		if ex == "" {
			continue
		}
		if len(ex) > 300 {
			return nil, nil, &queryError{errors.New("exclusion is too long")}
		}
		method := ""
		if m, rest, ok := strings.Cut(ex, " "); ok && httpMethods[strings.ToUpper(m)] {
			method, ex = strings.ToUpper(m), strings.TrimSpace(rest)
		}
		like := strings.ReplaceAll(likeEscaper.Replace(ex), "*", "%")
		if method == "" {
			where = append(where, "COALESCE(route, path) NOT LIKE ? ESCAPE '!'")
			args = append(args, like)
		} else {
			where = append(where, "NOT (method = ? AND COALESCE(route, path) LIKE ? ESCAPE '!')")
			args = append(args, method, like)
		}
	}
	if m := strings.TrimSpace(q.Get("method")); m != "" {
		where = append(where, "method = ?")
		args = append(args, strings.ToUpper(m))
	}
	if r := q.Get("route"); r != "" {
		where = append(where, "COALESCE(route, path) = ?")
		args = append(args, r)
	}
	return where, args, nil
}

func (s *Server) handleSlowRequests(w http.ResponseWriter, r *http.Request) {
	tr, ok := s.rangeOf(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	where, args, err := slowFilters(q)
	if err != nil {
		s.fail(w, err)
		return
	}
	cond := window
	for _, c := range where {
		cond += " AND " + c
	}

	var sql string
	switch q.Get("group") {
	case "":
		sql = `
SELECT id, created_at, method, COALESCE(route, path) AS route, path, status, duration_ms,
  user_handle, user_id, response_size
FROM api_logs WHERE ` + cond + `
ORDER BY duration_ms DESC
LIMIT ` + strconv.Itoa(slowRequestRows)
	case "route":
		// Routes ranked by their slowest request, with the spread around it,
		// so one outlier reads differently from a route that is always slow.
		sql = `
WITH r AS (
  SELECT method, COALESCE(route, path) AS route, status, duration_ms,
    ROW_NUMBER() OVER (PARTITION BY method, COALESCE(route, path) ORDER BY duration_ms) AS rn,
    COUNT(*) OVER (PARTITION BY method, COALESCE(route, path)) AS n
  FROM api_logs WHERE ` + cond + `
)
SELECT method, route,
  COUNT(*) AS requests,
  MIN(CASE WHEN rn >= 0.50 * n THEN duration_ms END) AS p50,
  MIN(CASE WHEN rn >= 0.95 * n THEN duration_ms END) AS p95,
  MAX(duration_ms) AS max_ms,
  ` + countIf("status >= 500") + ` AS errors
FROM r
GROUP BY method, route
ORDER BY max_ms DESC
LIMIT ` + strconv.Itoa(slowGroupRows)
	default:
		writeError(w, http.StatusBadRequest, "group must be route or empty")
		return
	}
	res, err := s.telemetryQuery(r.Context(), sql, tr, args...)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
