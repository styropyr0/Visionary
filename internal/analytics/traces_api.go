package analytics

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
)

const (
	maxTraceRequests = 300
	maxTraceSpans    = 3000
	maxTraceLogs     = 500
	maxTraceResults  = 50
)

func (s *Server) handleTrace(w http.ResponseWriter, r *http.Request) {
	tid := r.PathValue("trace_id")
	if !traceIDPattern.MatchString(tid) {
		writeError(w, http.StatusBadRequest, "invalid trace id")
		return
	}
	var reqs, spans, lines *Result
	tr := timeRange{}
	g, ctx := errgroup.WithContext(r.Context())
	g.Go(func() (err error) {
		reqs, err = s.telemetryQuery(ctx, `
SELECT id, request_id, created_at, service, version, method, route, path, status, duration_ms,
  user_id, span_id, parent_span_id, db_count, http_count
FROM api_logs WHERE trace_id = ?
ORDER BY created_at, id
LIMIT `+strconv.Itoa(maxTraceRequests), tr, tid)
		return
	})
	g.Go(func() (err error) {
		spans, err = s.telemetryQuery(ctx, `
SELECT id, created_at, request_id, service, instance, kind, name, target, detail, duration_ms,
  status, error, row_count, span_id, parent_span_id
FROM spans WHERE trace_id = ?
ORDER BY created_at, id
LIMIT `+strconv.Itoa(maxTraceSpans), tr, tid)
		return
	})
	g.Go(func() (err error) {
		q, args := `SELECT id, created_at, service, level, logger, message, request_id, span_id
FROM app_logs WHERE trace_id = ?`, []any{tid}
		if scope, sargs := s.logScope(r); scope != "" {
			q, args = q+" AND "+scope, append(args, sargs...)
		}
		lines, err = s.telemetryQuery(ctx, q+` ORDER BY created_at, id LIMIT `+strconv.Itoa(maxTraceLogs), tr, args...)
		if err != nil && missingTable(err, "app_logs") {
			lines, err = &Result{Columns: []string{}, Rows: [][]any{}}, nil
		}
		return
	})
	if err := g.Wait(); err != nil {
		s.fail(w, err)
		return
	}

	var rids []string
	for _, m := range rowMaps(reqs) {
		if id := toString(m["request_id"]); id != "" {
			rids = append(rids, id)
		}
	}
	errs := &Result{Columns: []string{"id", "created_at", "service", "request_id", "type", "message", "fingerprint", "handled"}, Rows: [][]any{}}
	for _, ids := range chunks(rids, 90) {
		res, err := s.telemetryQuery(r.Context(), `
SELECT id, created_at, service, request_id, type, message, fingerprint, handled
FROM errors WHERE request_id IN (`+inMarks(len(ids))+`)
ORDER BY created_at, id
LIMIT 100`, tr, anys(ids)...)
		if err != nil {
			s.fail(w, err)
			return
		}
		errs.Rows = append(errs.Rows, res.Rows...)
	}
	writeJSON(w, http.StatusOK, map[string]any{"trace_id": tid, "requests": reqs, "spans": spans, "logs": lines, "errors": errs})
}

func (s *Server) handleTraceSearch(w http.ResponseWriter, r *http.Request) {
	tr, ok := s.rangeOf(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	inRange := "created_at >= {{from}} AND created_at < {{to}}"
	where := []string{inRange, "trace_id IS NOT NULL"}
	var args []any
	for _, svc := range strings.Split(q.Get("service"), ",") {
		if svc = strings.TrimSpace(svc); svc == "" {
			continue
		}
		where = append(where, `trace_id IN (
  SELECT trace_id FROM api_logs WHERE `+inRange+` AND service = ?
  UNION SELECT trace_id FROM spans WHERE `+inRange+` AND service = ?)`)
		args = append(args, svc, svc)
	}
	if route := q.Get("route"); route != "" {
		where = append(where, `trace_id IN (SELECT trace_id FROM api_logs WHERE `+inRange+` AND COALESCE(route, path) = ?)`)
		args = append(args, route)
	}
	if span := q.Get("span"); span != "" {
		where = append(where, `trace_id IN (SELECT trace_id FROM spans WHERE `+inRange+` AND (name = ? OR target = ?))`)
		args = append(args, span, span)
	}
	if q.Get("errors") == "1" || q.Get("errors") == "true" {
		where = append(where, `trace_id IN (
  SELECT trace_id FROM api_logs WHERE `+inRange+` AND status >= 500
  UNION SELECT trace_id FROM spans WHERE `+inRange+` AND status = 'error')`)
	}
	minMs := 0.0
	if v := q.Get("min_ms"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f < 0 {
			writeError(w, http.StatusBadRequest, "min_ms must be a number of milliseconds")
			return
		}
		minMs = f
	}
	args = append(args, minMs)
	found, err := s.telemetryQuery(r.Context(), `
SELECT trace_id, MIN(created_at) AS started, MAX(duration_ms) AS longest
FROM api_logs
WHERE `+strings.Join(where, "\n  AND ")+`
GROUP BY trace_id
HAVING MAX(duration_ms) >= ?
ORDER BY started DESC
LIMIT `+strconv.Itoa(maxTraceResults), tr, args...)
	if err != nil {
		s.fail(w, err)
		return
	}
	var ids []string
	for _, row := range found.Rows {
		ids = append(ids, toString(row[0]))
	}
	out := &Result{
		Columns: []string{"trace_id", "started", "service", "method", "route", "status", "services", "requests", "spans", "errors", "duration_ms"},
		Rows:    [][]any{},
		SQL:     found.SQL,
		From:    found.From,
		To:      found.To,
	}
	if len(ids) == 0 {
		writeJSON(w, http.StatusOK, out)
		return
	}

	var reqs, calls *Result
	g, ctx := errgroup.WithContext(r.Context())
	g.Go(func() (err error) {
		reqs, err = s.telemetryQuery(ctx, `
SELECT trace_id, created_at, service, method, COALESCE(route, path) AS route, status, duration_ms, parent_span_id
FROM api_logs WHERE trace_id IN (`+inMarks(len(ids))+`)
ORDER BY created_at, id`, timeRange{}, anys(ids)...)
		return
	})
	g.Go(func() (err error) {
		calls, err = s.telemetryQuery(ctx, `
SELECT trace_id, service, COUNT(*) AS calls, `+countIf("status = 'error'")+` AS failed
FROM spans WHERE trace_id IN (`+inMarks(len(ids))+`)
GROUP BY trace_id, service`, timeRange{}, anys(ids)...)
		return
	})
	if err := g.Wait(); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, traceSummaries(out, ids, reqs, calls))
}

type traceSummary struct {
	root                    map[string]any
	services                []string
	requests, spans, errors int64
	start, end              time.Time
}

func traceSummaries(out *Result, ids []string, reqs, calls *Result) *Result {
	by := map[string]*traceSummary{}
	get := func(id string) *traceSummary {
		t := by[id]
		if t == nil {
			t = &traceSummary{}
			by[id] = t
		}
		return t
	}
	addService := func(t *traceSummary, svc string) {
		if !slices.Contains(t.services, svc) {
			t.services = append(t.services, svc)
		}
	}
	// Rows come oldest first, so the first request without a parent (else
	// the first of all) is the root
	for _, m := range rowMaps(reqs) {
		t := get(toString(m["trace_id"]))
		t.requests++
		if toInt(m["status"]) >= 500 {
			t.errors++
		}
		if t.root == nil || (toString(t.root["parent_span_id"]) != "" && toString(m["parent_span_id"]) == "") {
			t.root = m
		}
		addService(t, toString(m["service"]))
		start, err := time.Parse(tsLayout, toString(m["created_at"]))
		if err != nil {
			continue
		}
		end := start.Add(time.Duration(toFloat(m["duration_ms"]) * float64(time.Millisecond)))
		if t.start.IsZero() || start.Before(t.start) {
			t.start = start
		}
		if end.After(t.end) {
			t.end = end
		}
	}
	for _, m := range rowMaps(calls) {
		t := get(toString(m["trace_id"]))
		t.spans += toInt(m["calls"])
		t.errors += toInt(m["failed"])
		addService(t, toString(m["service"]))
	}
	for _, id := range ids {
		t := by[id]
		if t == nil || t.root == nil {
			continue
		}
		slices.Sort(t.services)
		out.Rows = append(out.Rows, []any{
			id, toString(t.root["created_at"]), t.root["service"], t.root["method"], t.root["route"], t.root["status"],
			t.services, t.requests, t.spans, t.errors,
			float64(t.end.Sub(t.start).Microseconds()) / 1000,
		})
	}
	return out
}
