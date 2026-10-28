package analytics

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/styropyr0/visionary/internal/db"
)

// The Logs page's API. Every endpoint takes the dashboard range (from, to)
// and the search (q), and only reads app_logs rows the viewer may see (see
// logScope).
//
//	GET /api/applogs                 a page of lines, newest first (before=id for the next)
//	GET /api/applogs/histogram       lines per bucket and level
//	GET /api/applogs/fields          the fields seen, or the top values of one (field=)
//	GET /api/applogs/{id}            one line in full
//	GET /api/applogs/{id}/context    the lines around it from the same source
//	GET /api/applogs/tail            new lines as they arrive (server-sent events)
//	GET /api/applogs/export          the matching lines as CSV or NDJSON
//	GET/POST/DELETE /api/applogs/views  saved searches

const (
	logsPage       = 200
	logsExportMax  = 50000
	logsFieldScan  = 5000
	logsTailWindow = 30 * time.Minute
)

// logListColumns are what the list shows; the detail has everything.
const logListColumns = `id, created_at, level, severity, service, source, host, container, logger, message, attrs, request_id, trace_id, user_id, code_file, code_line, pattern`

func (s *Server) applogsAPIRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/applogs", s.authed(s.handleAppLogs))
	mux.Handle("GET /api/applogs/histogram", s.authed(s.handleAppLogsHistogram))
	mux.Handle("GET /api/applogs/fields", s.authed(s.handleAppLogsFields))
	mux.Handle("GET /api/applogs/tail", s.authed(s.handleAppLogsTail))
	mux.Handle("GET /api/applogs/export", s.authed(s.handleAppLogsExport))
	mux.Handle("GET /api/applogs/views", s.authed(s.handleLogViews))
	mux.Handle("POST /api/applogs/views", s.authed(s.handleCreateLogView))
	mux.Handle("DELETE /api/applogs/views/{id}", s.authed(s.handleDeleteLogView))
	mux.Handle("GET /api/applogs/around", s.authed(s.handleAppLogsAround))
	mux.Handle("GET /api/applogs/{id}", s.authed(s.handleAppLog))
	mux.Handle("GET /api/applogs/{id}/context", s.authed(s.handleAppLogContext))
}

// logSearch is a compiled search over a range: the WHERE clause and its
// arguments, ready to be extended.
type logSearch struct {
	tr    timeRange
	where []string
	args  []any
	terms []string
}

func (ls *logSearch) sql() string { return strings.Join(ls.where, " AND ") }

// logSearchOf reads range, q and the viewer's limits from the request.
func (s *Server) logSearchOf(r *http.Request) (*logSearch, error) {
	tr, err := parseRange(r.URL.Query(), s.maxRangeDays())
	if err != nil {
		return nil, &logQueryError{err.Error()}
	}
	return s.logSearchFor(r, tr, r.URL.Query().Get("q"))
}

func (s *Server) logSearchFor(r *http.Request, tr timeRange, q string) (*logSearch, error) {
	cq, err := compileLogQuery(q, s.q, s.onD1())
	if err != nil {
		return nil, err
	}
	ls := &logSearch{
		tr:    tr,
		where: []string{"created_at >= ?", "created_at < ?", cq.where},
		args:  append([]any{tr.From.Format(tsLayout), tr.To.Format(tsLayout)}, cq.args...),
		terms: cq.terms,
	}
	if scope, args := s.logScope(r); scope != "" {
		ls.where = append(ls.where, scope)
		ls.args = append(ls.args, args...)
	}
	return ls, nil
}

// onD1 reports a Cloudflare D1 database, which lacks some SQLite features.
func (s *Server) onD1() bool { return strings.Contains(s.db.Name(), "D1") }

// logFail answers a search error as 400 and anything else as fail does.
func (s *Server) logFail(w http.ResponseWriter, err error) {
	var qe *logQueryError
	if errors.As(err, &qe) {
		writeError(w, http.StatusBadRequest, qe.msg)
		return
	}
	s.fail(w, err)
}

// logResult is a page of lines with what the page needs to show them.
type logResult struct {
	*Result
	Terms []string `json:"terms"`
	// Masked are the fields hidden from this viewer.
	Masked []string `json:"masked,omitempty"`
}

func (s *Server) handleAppLogs(w http.ResponseWriter, r *http.Request) {
	ls, err := s.logSearchOf(r)
	if err != nil {
		s.logFail(w, err)
		return
	}
	if b := r.URL.Query().Get("before"); b != "" {
		id, err := strconv.ParseInt(b, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "before must be a log id")
			return
		}
		ls.where = append(ls.where, "id < ?")
		ls.args = append(ls.args, id)
	}
	limit := logsPage
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n < limit {
		limit = n
	}
	order := "created_at DESC, id DESC"
	if r.URL.Query().Get("order") == "asc" {
		order = "created_at, id"
	}
	q := `SELECT ` + logListColumns + ` FROM app_logs WHERE ` + ls.sql() + ` ORDER BY ` + order + ` LIMIT ` + strconv.Itoa(limit+1)
	rows, err := s.db.Query(r.Context(), q, ls.args...)
	if err != nil {
		s.logFail(w, dbErr(err))
		return
	}
	res := newResult(rows, limit, "", ls.tr)
	masked := s.maskLogRows(r, res)
	writeJSON(w, http.StatusOK, logResult{Result: res, Terms: ls.terms, Masked: masked})
}

func (s *Server) handleAppLogsHistogram(w http.ResponseWriter, r *http.Request) {
	ls, err := s.logSearchOf(r)
	if err != nil {
		s.logFail(w, err)
		return
	}
	bucket := strconv.FormatInt(ls.tr.Bucket, 10)
	q := `SELECT ` + s.q.bucketOf(bucket) + ` AS t, level, COUNT(*) AS n FROM app_logs WHERE ` + ls.sql() + ` GROUP BY 1, level ORDER BY 1`
	rows, err := s.db.Query(r.Context(), q, ls.args...)
	if err != nil {
		s.logFail(w, dbErr(err))
		return
	}
	writeJSON(w, http.StatusOK, newResult(rows, 100000, "", ls.tr))
}

// fieldColumns are the columns the field list offers, in its order.
var fieldColumns = []string{"level", "service", "source", "host", "container", "logger", "version", "instance", "code_file", "user_id", "request_id", "trace_id", "pattern"}

// handleAppLogsFields lists the fields of matching lines with how many
// carry each, or, with field=, the top values of one field.
func (s *Server) handleAppLogsFields(w http.ResponseWriter, r *http.Request) {
	ls, err := s.logSearchOf(r)
	if err != nil {
		s.logFail(w, err)
		return
	}
	ctx := r.Context()
	if field := strings.TrimSpace(r.URL.Query().Get("field")); field != "" {
		values, total, err := s.topLogValues(ctx, ls, field, r)
		if err != nil {
			s.logFail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"field": field, "values": values, "total": total})
		return
	}
	// Sample the newest matching lines for their fields.
	q := `SELECT ` + strings.Join(fieldColumns[1:], ", ") + `, attrs FROM app_logs WHERE ` + ls.sql() + ` ORDER BY created_at DESC LIMIT ` + strconv.Itoa(logsFieldScan)
	rows, err := s.db.Query(ctx, q, ls.args...)
	if err != nil {
		s.logFail(w, dbErr(err))
		return
	}
	masked := map[string]bool{}
	for _, k := range s.maskedLogFields(r) {
		masked[k] = true
	}
	type fieldCount struct {
		Name   string `json:"name"`
		Count  int    `json:"count"`
		Column bool   `json:"column"`
	}
	counts := map[string]*fieldCount{"level": {Name: "level", Count: len(rows.Rows), Column: true}}
	for _, row := range rows.Rows {
		for i, c := range fieldColumns[1:] {
			if row[i] != nil && toString(row[i]) != "" {
				if counts[c] == nil {
					counts[c] = &fieldCount{Name: c, Column: true}
				}
				counts[c].Count++
			}
		}
		var a map[string]any
		if raw := toString(row[len(row)-1]); raw != "" && json.Unmarshal([]byte(raw), &a) == nil {
			for k := range a {
				if masked[k] {
					continue
				}
				if counts[k] == nil {
					if len(counts) > 300 {
						continue
					}
					counts[k] = &fieldCount{Name: k}
				}
				counts[k].Count++
			}
		}
	}
	out := make([]*fieldCount, 0, len(counts))
	for _, c := range fieldColumns {
		if fc := counts[c]; fc != nil {
			out = append(out, fc)
			delete(counts, c)
		}
	}
	var rest []*fieldCount
	for _, fc := range counts {
		rest = append(rest, fc)
	}
	sort.Slice(rest, func(i, j int) bool {
		if rest[i].Count != rest[j].Count {
			return rest[i].Count > rest[j].Count
		}
		return rest[i].Name < rest[j].Name
	})
	writeJSON(w, http.StatusOK, map[string]any{"fields": append(out, rest...), "sampled": len(rows.Rows)})
}

// topLogValues counts the ten most common values of a column or field.
func (s *Server) topLogValues(ctx context.Context, ls *logSearch, field string, r *http.Request) ([]map[string]any, int64, error) {
	for _, k := range s.maskedLogFields(r) {
		if k == field {
			return []map[string]any{}, 0, nil
		}
	}
	expr := ""
	args := append([]any{}, ls.args...)
	f := strings.ToLower(field)
	if col, ok := logColumns[f]; ok {
		expr = col
		if col == "severity" {
			expr = "level"
		}
	} else {
		p := &queryParser{d: s.q}
		expr = p.attr(strings.TrimPrefix(field, "attrs."))
		// The field's path is bound before the WHERE arguments.
		args = append(p.args, args...)
	}
	q := `SELECT v, COUNT(*) AS n FROM (SELECT ` + expr + ` AS v FROM app_logs WHERE ` + ls.sql() + ` ORDER BY created_at DESC LIMIT ` + strconv.Itoa(logsFieldScan*4) + `) AS s WHERE v IS NOT NULL GROUP BY v ORDER BY n DESC LIMIT 10`
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, dbErr(err)
	}
	out := []map[string]any{}
	var total int64
	for _, row := range rows.Rows {
		n := toInt(row[1])
		total += n
		out = append(out, map[string]any{"value": row[0], "count": n})
	}
	return out, total, nil
}

func (s *Server) handleAppLog(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid log id")
		return
	}
	q := `SELECT * FROM app_logs WHERE id = ?`
	args := []any{id}
	if scope, sargs := s.logScope(r); scope != "" {
		q += " AND " + scope
		args = append(args, sargs...)
	}
	rows, err := s.db.Query(r.Context(), q, args...)
	if err != nil {
		s.fail(w, dbErr(err))
		return
	}
	if len(rows.Rows) == 0 {
		writeError(w, http.StatusNotFound, "log line not found")
		return
	}
	res := &Result{Columns: rows.Columns, Rows: rows.Rows}
	s.maskLogRows(r, res)
	out := map[string]any{"line": rowMap(res, 0)}
	if p := toString(rowMap(res, 0)["pattern"]); p != "" {
		pr, err := s.db.Query(r.Context(), `SELECT id, template, first_seen, last_seen, first_version, lines FROM log_patterns WHERE id = ?`, p)
		if err == nil && len(pr.Rows) > 0 {
			out["pattern"] = rowMap(&Result{Columns: pr.Columns, Rows: pr.Rows}, 0)
			_ = s.db.Exec(r.Context(), `UPDATE log_patterns SET viewed_at = ? WHERE id = ?`, time.Now().UTC().Format(tsLayout), p)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func rowMap(res *Result, i int) map[string]any {
	m := map[string]any{}
	for j, c := range res.Columns {
		m[c] = res.Rows[i][j]
	}
	return m
}

// handleAppLogContext returns the lines logged around one: by the same
// container, else host, else instance, else service; n before and after.
func (s *Server) handleAppLogContext(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid log id")
		return
	}
	n := 25
	if v, err := strconv.Atoi(r.URL.Query().Get("n")); err == nil && v > 0 && v <= 200 {
		n = v
	}
	ctx := r.Context()
	scope, sargs := s.logScope(r)
	base := `SELECT created_at, service, instance, host, container FROM app_logs WHERE id = ?`
	if scope != "" {
		base += " AND " + scope
	}
	row, err := s.db.Query(ctx, base, append([]any{id}, sargs...)...)
	if err != nil {
		s.fail(w, dbErr(err))
		return
	}
	if len(row.Rows) == 0 {
		writeError(w, http.StatusNotFound, "log line not found")
		return
	}
	at := toString(row.Rows[0][0])
	by, val := "service", row.Rows[0][1]
	for i, c := range []string{"", "", "instance", "host", "container"} {
		if i >= 2 && row.Rows[0][i] != nil && toString(row.Rows[0][i]) != "" {
			by, val = c, row.Rows[0][i]
		}
	}
	match := by + " = ?"
	if val == nil {
		match = by + " IS NULL"
	}
	extra := ""
	if scope != "" {
		extra = " AND " + scope
	}
	t, _ := time.Parse(tsLayout, at)
	window := []any{t.Add(-time.Hour).Format(tsLayout), t.Add(time.Hour).Format(tsLayout)}
	args := func(cmp []any) []any {
		a := append([]any{}, window...)
		if val != nil {
			a = append(a, val)
		}
		a = append(a, cmp...)
		return append(a, sargs...)
	}
	before := `SELECT ` + logListColumns + ` FROM app_logs WHERE created_at >= ? AND created_at < ? AND ` + match +
		` AND (created_at < ? OR (created_at = ? AND id < ?))` + extra + ` ORDER BY created_at DESC, id DESC LIMIT ` + strconv.Itoa(n)
	after := `SELECT ` + logListColumns + ` FROM app_logs WHERE created_at >= ? AND created_at < ? AND ` + match +
		` AND (created_at > ? OR (created_at = ? AND id >= ?))` + extra + ` ORDER BY created_at, id LIMIT ` + strconv.Itoa(n+1)
	b, err := s.db.Query(ctx, before, args([]any{at, at, id})...)
	if err != nil {
		s.fail(w, dbErr(err))
		return
	}
	a, err := s.db.Query(ctx, after, args([]any{at, at, id})...)
	if err != nil {
		s.fail(w, dbErr(err))
		return
	}
	// Oldest first: the earlier lines reversed, then the line and later ones.
	out := &Result{Columns: a.Columns}
	for i := len(b.Rows) - 1; i >= 0; i-- {
		out.Rows = append(out.Rows, b.Rows[i])
	}
	out.Rows = append(out.Rows, a.Rows...)
	if out.Columns == nil {
		out.Columns = b.Columns
	}
	s.maskLogRows(r, out)
	writeJSON(w, http.StatusOK, map[string]any{"result": out, "by": by, "value": val, "id": id})
}

// handleAppLogsAround returns the lines logged shortly before and after a
// moment by one source (service, instance, host or container, as given):
// what a service said around an error, a failed uptime check, an OOM kill
// or a crash.
func (s *Server) handleAppLogsAround(w http.ResponseWriter, r *http.Request) {
	qs := r.URL.Query()
	at, err := time.Parse(time.RFC3339Nano, qs.Get("at"))
	if err != nil {
		if at, err = time.Parse(tsLayout, qs.Get("at")); err != nil {
			writeError(w, http.StatusBadRequest, "at must be a timestamp")
			return
		}
	}
	before, after := 2*time.Minute, 30*time.Second
	if v, err := strconv.Atoi(qs.Get("before_s")); err == nil && v >= 0 && v <= 3600 {
		before = time.Duration(v) * time.Second
	}
	if v, err := strconv.Atoi(qs.Get("after_s")); err == nil && v >= 0 && v <= 3600 {
		after = time.Duration(v) * time.Second
	}
	tr := timeRange{From: at.Add(-before).UTC(), To: at.Add(after).UTC(), Bucket: 60}
	ls, err := s.logSearchFor(r, tr, qs.Get("q"))
	if err != nil {
		s.logFail(w, err)
		return
	}
	for _, c := range []string{"service", "instance", "host", "container", "request_id"} {
		if v := qs.Get(c); v != "" {
			ls.where = append(ls.where, c+" = ?")
			ls.args = append(ls.args, v)
		}
	}
	rows, err := s.db.Query(r.Context(), `SELECT `+logListColumns+` FROM app_logs WHERE `+ls.sql()+` ORDER BY created_at, id LIMIT 300`, ls.args...)
	if err != nil {
		if missingTable(err, "app_logs") {
			writeJSON(w, http.StatusOK, newResult(&db.Rows{Columns: []string{}}, 0, "", tr))
			return
		}
		s.logFail(w, dbErr(err))
		return
	}
	res := newResult(rows, 300, "", tr)
	s.maskLogRows(r, res)
	writeJSON(w, http.StatusOK, res)
}

// handleAppLogsTail streams lines newer than the last one sent, polling
// the table every second, for at most 30 minutes per connection.
func (s *Server) handleAppLogsTail(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	now := time.Now().UTC()
	ls, err := s.logSearchFor(r, timeRange{From: now.Add(-time.Minute), To: now.Add(24 * time.Hour)}, q)
	if err != nil {
		s.logFail(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	ctx := r.Context()
	var last int64
	if res, err := s.db.Query(ctx, `SELECT MAX(id) FROM app_logs`); err == nil && len(res.Rows) > 0 {
		last = toInt(res.Rows[0][0])
	}
	if v, err := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64); err == nil && v > 0 {
		last = v
	}
	s.audit(r, "logs.tail", "", map[string]any{"q": q})
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	out := sse{w: w, f: flusher}
	out.send("ready", map[string]any{"after": last})
	stop := time.After(logsTailWindow)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	quiet := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			out.send("end", map[string]string{"reason": "Live tail stops after 30 minutes; start it again to keep watching."})
			return
		case <-tick.C:
		}
		where := append(append([]string{}, ls.where[2:]...), "id > ?")
		args := append(append([]any{}, ls.args[2:]...), last)
		rows, err := s.db.Query(ctx, `SELECT `+logListColumns+` FROM app_logs WHERE `+strings.Join(where, " AND ")+` ORDER BY id LIMIT 500`, args...)
		if err != nil {
			if ctx.Err() == nil {
				out.send("error", map[string]string{"message": "reading new lines failed; retrying"})
			}
			continue
		}
		if len(rows.Rows) == 0 {
			if quiet++; quiet%15 == 0 {
				out.send("ping", map[string]any{})
			}
			continue
		}
		quiet = 0
		res := &Result{Columns: rows.Columns, Rows: rows.Rows}
		s.maskLogRows(r, res)
		last = toInt(rows.Rows[len(rows.Rows)-1][0])
		out.send("lines", res)
	}
}

// handleAppLogsExport writes the matching lines, newest first, as CSV or
// NDJSON (format=), at most 50 000.
func (s *Server) handleAppLogsExport(w http.ResponseWriter, r *http.Request) {
	ls, err := s.logSearchOf(r)
	if err != nil {
		s.logFail(w, err)
		return
	}
	format := r.URL.Query().Get("format")
	if format != "csv" {
		format = "ndjson"
	}
	rows, err := s.db.Query(r.Context(), `SELECT `+logListColumns+` FROM app_logs WHERE `+ls.sql()+` ORDER BY created_at DESC, id DESC LIMIT `+strconv.Itoa(logsExportMax), ls.args...)
	if err != nil {
		s.logFail(w, dbErr(err))
		return
	}
	res := &Result{Columns: rows.Columns, Rows: rows.Rows}
	s.maskLogRows(r, res)
	s.audit(r, "logs.export", "", map[string]any{"q": r.URL.Query().Get("q"), "lines": len(res.Rows), "format": format})
	name := "logs-" + time.Now().UTC().Format("20060102-150405")
	if format == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.csv"`)
		cw := csv.NewWriter(w)
		_ = cw.Write(res.Columns)
		for _, row := range res.Rows {
			rec := make([]string, len(row))
			for i, v := range row {
				rec[i] = toString(v)
			}
			_ = cw.Write(rec)
		}
		cw.Flush()
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.ndjson"`)
	enc := json.NewEncoder(w)
	for i := range res.Rows {
		m := rowMap(res, i)
		if a := toString(m["attrs"]); a != "" {
			var parsed map[string]any
			if json.Unmarshal([]byte(a), &parsed) == nil {
				m["attrs"] = parsed
			}
		}
		_ = enc.Encode(m)
	}
}

// Saved searches.

type logView struct {
	ID        int64    `json:"id"`
	Name      string   `json:"name"`
	Query     string   `json:"query"`
	Columns   []string `json:"columns"`
	CreatedBy string   `json:"created_by"`
	CreatedAt string   `json:"created_at"`
}

func (s *Server) handleLogViews(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), `SELECT id, name, query, columns, created_by, created_at FROM log_views ORDER BY name`)
	if err != nil {
		s.fail(w, dbErr(err))
		return
	}
	out := []logView{}
	for _, row := range rows.Rows {
		v := logView{ID: toInt(row[0]), Name: toString(row[1]), Query: toString(row[2]), CreatedBy: toString(row[4]), CreatedAt: toString(row[5]), Columns: []string{}}
		_ = json.Unmarshal([]byte(toString(row[3])), &v.Columns)
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"views": out})
}

func (s *Server) handleCreateLogView(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "edit") {
		return
	}
	var v logView
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	v.Name, v.Query = strings.TrimSpace(v.Name), strings.TrimSpace(v.Query)
	if v.Name == "" || len(v.Name) > 120 {
		writeError(w, http.StatusBadRequest, "a saved search needs a name of up to 120 characters")
		return
	}
	if _, err := compileLogQuery(v.Query, s.q, s.onD1()); err != nil {
		s.logFail(w, err)
		return
	}
	if len(v.Columns) > 30 {
		writeError(w, http.StatusBadRequest, "at most 30 columns")
		return
	}
	cols, _ := json.Marshal(v.Columns)
	v.CreatedAt = time.Now().UTC().Format(tsLayout)
	if sess := sessionFrom(r); sess != nil {
		v.CreatedBy = sess.Name
	}
	id, err := s.db.InsertID(r.Context(), `INSERT INTO log_views (name, query, columns, created_by, created_at) VALUES (?, ?, ?, ?, ?)`, v.Name, v.Query, string(cols), v.CreatedBy, v.CreatedAt)
	if err != nil {
		s.fail(w, dbErr(err))
		return
	}
	v.ID = id
	s.audit(r, "logs.view_save", strconv.FormatInt(id, 10), map[string]any{"name": v.Name})
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleDeleteLogView(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "edit") {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.db.Exec(r.Context(), `DELETE FROM log_views WHERE id = ?`, id); err != nil {
		s.fail(w, dbErr(err))
		return
	}
	s.audit(r, "logs.view_delete", strconv.FormatInt(id, 10), nil)
	w.WriteHeader(http.StatusNoContent)
}

// logScope limits what a viewer reads (Settings → Logs → Access): viewers
// don't see the hidden services. Editors and admins see everything.
func (s *Server) logScope(r *http.Request) (string, []any) {
	if r == nil || s.can(r, "edit") {
		return "", nil
	}
	hidden := s.logAccess().HiddenServices
	if len(hidden) == 0 {
		return "", nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?, ", len(hidden)), ", ")
	args := make([]any, len(hidden))
	for i, h := range hidden {
		args[i] = h
	}
	return "(service IS NULL OR service NOT IN (" + marks + "))", args
}

// maskedLogFields are the fields viewers see as [hidden].
func (s *Server) maskedLogFields(r *http.Request) []string {
	if r == nil || s.can(r, "edit") {
		return nil
	}
	return s.logAccess().MaskedFields
}

// maskLogRows hides masked fields in attrs for viewers; it returns them.
func (s *Server) maskLogRows(r *http.Request, res *Result) []string {
	masked := s.maskedLogFields(r)
	if len(masked) == 0 {
		return nil
	}
	col := -1
	for i, c := range res.Columns {
		if c == "attrs" {
			col = i
		}
	}
	set := map[string]bool{}
	for _, k := range masked {
		set[k] = true
	}
	if col < 0 {
		return masked
	}
	for _, row := range res.Rows {
		raw := toString(row[col])
		if raw == "" {
			continue
		}
		var a map[string]any
		if json.Unmarshal([]byte(raw), &a) != nil {
			continue
		}
		changed := false
		for k := range a {
			if set[k] {
				a[k], changed = "[hidden]", true
			}
		}
		if changed {
			b, _ := json.Marshal(a)
			row[col] = string(b)
		}
	}
	return masked
}

// logAccessSettings are Settings → Logs → Access.
type logAccessSettings struct {
	HiddenServices []string `json:"hidden_services"`
	MaskedFields   []string `json:"masked_fields"`
}

func (s *Server) logAccess() logAccessSettings {
	if l := s.live().LogServer; l != nil {
		return l.Access
	}
	return logAccessSettings{}
}
