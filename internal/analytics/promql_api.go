package analytics

import (
	"context"
	"errors"
	"math"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const promTimeout = 30 * time.Second

var promAPIPrefixes = []string{"/api/prom/api/v1", "/api/v1"}

func (s *Server) promRoutes(mux *http.ServeMux) {
	for _, p := range promAPIPrefixes {
		mux.Handle(p+"/query", s.promAuthed(s.handlePromQuery))
		mux.Handle(p+"/query_range", s.promAuthed(s.handlePromQueryRange))
		mux.Handle(p+"/series", s.promAuthed(s.handlePromSeries))
		mux.Handle(p+"/labels", s.promAuthed(s.handlePromLabels))
		mux.Handle(p+"/label/{name}/values", s.promAuthed(s.handlePromLabelValues))
		mux.Handle(p+"/metadata", s.promAuthed(s.handlePromMetadata))
		mux.Handle(p+"/query_exemplars", s.promAuthed(func(w http.ResponseWriter, _ *http.Request) { promOK(w, []any{}) }))
		mux.Handle(p+"/status/buildinfo", s.promAuthed(s.handlePromBuildInfo))
	}
	mux.Handle("GET /api/metrics/promql", s.authed(s.handlePromChart))
}

// A form POST reads as the GET it stands for, since every endpoint only reads
func (s *Server) promAuthed(h http.HandlerFunc) http.Handler {
	inner := s.authed(h)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead:
		case http.MethodPost:
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
			if err := r.ParseForm(); err != nil {
				promFail(w, http.StatusBadRequest, "bad_data", "the form body could not be read")
				return
			}
			get := r.Clone(r.Context())
			get.Method = http.MethodGet
			get.URL.RawQuery = r.Form.Encode()
			get.Body, get.ContentLength = http.NoBody, 0
			get.Header.Del("Content-Type")
			r = get
		default:
			promFail(w, http.StatusMethodNotAllowed, "bad_data", "use GET or POST")
			return
		}
		if user, pass, ok := r.BasicAuth(); ok {
			key := pass
			if !strings.HasPrefix(key, keyPrefix) {
				key = user
			}
			if strings.HasPrefix(key, keyPrefix) {
				r = r.Clone(r.Context())
				r.Header.Set("Authorization", "Bearer "+key)
			}
		}
		inner.ServeHTTP(w, r)
	})
}

func promOK(w http.ResponseWriter, data any) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "success", "data": data})
}

func promFail(w http.ResponseWriter, status int, typ, msg string) {
	writeJSON(w, status, map[string]any{"status": "error", "errorType": typ, "error": msg})
}

func (s *Server) promFailErr(w http.ResponseWriter, err error) {
	var pe *promError
	var qe *queryError
	switch {
	case errors.As(err, &pe):
		promFail(w, http.StatusBadRequest, "bad_data", pe.Error())
	case errors.Is(err, errPromTooLarge):
		promFail(w, http.StatusUnprocessableEntity, "execution", err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		promFail(w, http.StatusServiceUnavailable, "timeout", "the query took longer than 30 seconds")
	case errors.Is(err, context.Canceled):
	case errors.As(err, &qe):
		promFail(w, http.StatusUnprocessableEntity, "execution", qe.Error())
	default:
		s.logger.Error("promql failed", "err", err)
		promFail(w, http.StatusUnprocessableEntity, "execution", "the query could not be run")
	}
}

func promTime(v string, def time.Time) (time.Time, error) {
	if v == "" {
		return def, nil
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
		return time.UnixMilli(int64(math.Round(f * 1000))).UTC(), nil
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return time.Time{}, errors.New("cannot parse " + strconv.Quote(v) + " as a timestamp")
	}
	return t.UTC(), nil
}

func promDuration(v string) (time.Duration, error) {
	if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 && !math.IsInf(f, 0) {
		return time.Duration(f * float64(time.Second)), nil
	}
	toks, err := lexProm(v)
	if err == nil && len(toks) == 2 && toks[0].kind == tDuration && toks[0].dur > 0 {
		return toks[0].dur, nil
	}
	return 0, errors.New("cannot parse " + strconv.Quote(v) + " as a duration")
}

func (s *Server) runPromQL(ctx context.Context, query string, start, end time.Time, step time.Duration) (promExpr, promVal, *promEval, error) {
	return s.runPromQLWindows(ctx, query, start, end, step, 0)
}

func (s *Server) runPromQLWindows(ctx context.Context, query string, start, end time.Time, step, minRange time.Duration) (promExpr, promVal, *promEval, error) {
	expr, err := parsePromQL(query)
	if err != nil {
		return nil, promVal{}, nil, err
	}
	if minRange > 0 {
		walkProm(expr, func(x promExpr) {
			if m, ok := x.(*matrixSel); ok && m.rng < minRange {
				m.rng = minRange
			}
		})
	}
	ctx, cancel := context.WithTimeout(ctx, promTimeout)
	defer cancel()
	ev := newPromEval(ctx, &sqlPromSource{s: s}, expr, start.UnixMilli(), end.UnixMilli(), step.Milliseconds())
	v, err := ev.eval(expr)
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		err = context.DeadlineExceeded
	}
	return expr, v, ev, err
}

func promNum(v float64) string {
	switch {
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	case math.IsNaN(v):
		return "NaN"
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func promTS(ms int64) float64 { return float64(ms) / 1000 }

func labelsJSON(l promLabels) map[string]string {
	if l == nil {
		return map[string]string{}
	}
	return l
}

func mergeSeries(vec []*vecSeries) []*vecSeries {
	byKey := map[string]*vecSeries{}
	var out []*vecSeries
	for _, s := range vec {
		k := s.labels.key()
		m := byKey[k]
		if m == nil {
			byKey[k] = s
			out = append(out, s)
			continue
		}
		for i, ok := range s.ok {
			if ok && !m.ok[i] {
				m.v[i], m.ok[i] = s.v[i], true
			}
		}
	}
	return out
}

func (s *Server) handlePromQuery(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	at, err := promTime(q.Get("time"), time.Now().UTC())
	if err != nil {
		promFail(w, http.StatusBadRequest, "bad_data", err.Error())
		return
	}
	_, v, _, err := s.runPromQL(r.Context(), q.Get("query"), at, at, 0)
	if err != nil {
		s.promFailErr(w, err)
		return
	}
	ts := promTS(at.UnixMilli())
	switch {
	case v.str != nil:
		promOK(w, map[string]any{"resultType": "string", "result": []any{ts, *v.str}})
	case v.isScalar:
		promOK(w, map[string]any{"resultType": "scalar", "result": []any{ts, promNum(v.scalar[0])}})
	case v.matrix != nil:
		out := make([]map[string]any, 0, len(v.matrix))
		for _, rs := range v.matrix {
			vals := make([][]any, len(rs.samples))
			for i, smp := range rs.samples {
				vals[i] = []any{promTS(smp.T), promNum(rs.value(i))}
			}
			out = append(out, map[string]any{"metric": labelsJSON(rs.labels), "values": vals})
		}
		promOK(w, map[string]any{"resultType": "matrix", "result": out})
	default:
		vec := mergeSeries(v.vec)
		if v.sort != 0 {
			sort.SliceStable(vec, func(i, j int) bool {
				if v.sort > 0 {
					return vec[i].v[0] < vec[j].v[0]
				}
				return vec[i].v[0] > vec[j].v[0]
			})
		} else {
			sort.SliceStable(vec, func(i, j int) bool { return vec[i].labels.String() < vec[j].labels.String() })
		}
		out := make([]map[string]any, 0, len(vec))
		for _, sr := range vec {
			if sr.ok[0] {
				out = append(out, map[string]any{"metric": labelsJSON(sr.labels), "value": []any{ts, promNum(sr.v[0])}})
			}
		}
		promOK(w, map[string]any{"resultType": "vector", "result": out})
	}
}

func (s *Server) rangeParams(r *http.Request) (time.Time, time.Time, time.Duration, error) {
	q := r.URL.Query()
	start, err := promTime(q.Get("start"), time.Time{})
	if err != nil {
		return start, start, 0, err
	}
	end, err := promTime(q.Get("end"), time.Time{})
	if err != nil {
		return start, end, 0, err
	}
	if start.IsZero() || end.IsZero() {
		return start, end, 0, errors.New("start and end are required")
	}
	if end.Before(start) {
		return start, end, 0, errors.New("end is before start")
	}
	if end.Sub(start) > time.Duration(s.maxRangeDays())*24*time.Hour {
		return start, end, 0, errors.New("the time range is limited to " + strconv.Itoa(s.maxRangeDays()) + " days")
	}
	step, err := promDuration(q.Get("step"))
	if err != nil {
		return start, end, 0, err
	}
	if end.Sub(start)/step > maxPromSteps {
		return start, end, 0, errors.New("exceeded maximum resolution of 11,000 points per series; raise the step")
	}
	return start, end, step, nil
}

func (s *Server) handlePromQueryRange(w http.ResponseWriter, r *http.Request) {
	start, end, step, err := s.rangeParams(r)
	if err != nil {
		promFail(w, http.StatusBadRequest, "bad_data", err.Error())
		return
	}
	_, v, ev, err := s.runPromQL(r.Context(), r.URL.Query().Get("query"), start, end, step)
	if err != nil {
		s.promFailErr(w, err)
		return
	}
	if v.str != nil {
		promFail(w, http.StatusBadRequest, "bad_data", "a range query needs a scalar or an instant vector, not a string")
		return
	}
	if v.isScalar {
		vals := make([][]any, ev.n)
		for i := range vals {
			vals[i] = []any{promTS(ev.at(i)), promNum(v.scalar[i])}
		}
		promOK(w, map[string]any{"resultType": "matrix", "result": []any{map[string]any{"metric": map[string]string{}, "values": vals}}})
		return
	}
	vec := mergeSeries(v.vec)
	sort.SliceStable(vec, func(i, j int) bool { return vec[i].labels.String() < vec[j].labels.String() })
	out := make([]map[string]any, 0, len(vec))
	for _, sr := range vec {
		var vals [][]any
		for i := range sr.v {
			if sr.ok[i] {
				vals = append(vals, []any{promTS(ev.at(i)), promNum(sr.v[i])})
			}
		}
		if len(vals) > 0 {
			out = append(out, map[string]any{"metric": labelsJSON(sr.labels), "values": vals})
		}
	}
	promOK(w, map[string]any{"resultType": "matrix", "result": out})
}

func (s *Server) metaRange(r *http.Request) (time.Time, time.Time, error) {
	q := r.URL.Query()
	now := time.Now().UTC()
	end, err := promTime(q.Get("end"), now)
	if err != nil {
		return end, end, err
	}
	start, err := promTime(q.Get("start"), end.Add(-6*time.Hour))
	if err != nil {
		return start, end, err
	}
	if max := time.Duration(s.maxRangeDays()) * 24 * time.Hour; end.Sub(start) > max {
		start = end.Add(-max)
	}
	return start, end, nil
}

func matchParam(r *http.Request) ([][]labelMatcher, error) {
	var out [][]labelMatcher
	for _, m := range r.URL.Query()["match[]"] {
		e, err := parsePromQL(m)
		if err != nil {
			return nil, err
		}
		vs, ok := unwrapParen(e).(*vectorSel)
		if !ok {
			return nil, &promError{Msg: "match[] must be a series selector like name{label=\"value\"}"}
		}
		out = append(out, vs.matchers)
	}
	return out, nil
}

func (s *Server) promCatalogOf(w http.ResponseWriter, r *http.Request) ([]promLabels, bool) {
	start, end, err := s.metaRange(r)
	if err != nil {
		promFail(w, http.StatusBadRequest, "bad_data", err.Error())
		return nil, false
	}
	sels, err := matchParam(r)
	if err != nil {
		s.promFailErr(w, err)
		return nil, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), promTimeout)
	defer cancel()
	out, _, err := s.promCatalog(ctx, sels, start, end.Add(time.Millisecond))
	if err != nil {
		s.promFailErr(w, err)
		return nil, false
	}
	return out, true
}

func (s *Server) handlePromSeries(w http.ResponseWriter, r *http.Request) {
	if len(r.URL.Query()["match[]"]) == 0 {
		promFail(w, http.StatusBadRequest, "bad_data", "no match[] parameter provided")
		return
	}
	cat, ok := s.promCatalogOf(w, r)
	if !ok {
		return
	}
	out := make([]map[string]string, 0, len(cat))
	for _, l := range cat {
		out = append(out, l)
	}
	promOK(w, out)
}

func (s *Server) handlePromLabels(w http.ResponseWriter, r *http.Request) {
	cat, ok := s.promCatalogOf(w, r)
	if !ok {
		return
	}
	seen := map[string]bool{}
	out := []string{}
	for _, l := range cat {
		for k := range l {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	promOK(w, out)
}

func (s *Server) handlePromLabelValues(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "__name__" && len(r.URL.Query()["match[]"]) == 0 {
		start, end, err := s.metaRange(r)
		if err != nil {
			promFail(w, http.StatusBadRequest, "bad_data", err.Error())
			return
		}
		meta, err := s.promMetadata(r.Context(), start, end)
		if err != nil {
			s.promFailErr(w, err)
			return
		}
		out := make([]string, 0, len(meta))
		for n := range meta {
			out = append(out, n)
		}
		sort.Strings(out)
		promOK(w, out)
		return
	}
	cat, ok := s.promCatalogOf(w, r)
	if !ok {
		return
	}
	seen := map[string]bool{}
	out := []string{}
	for _, l := range cat {
		if v := l[name]; v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	promOK(w, out)
}

type promMeta struct {
	Type string `json:"type"`
	Help string `json:"help"`
	Unit string `json:"unit"`
}

func (s *Server) promMetadata(ctx context.Context, from, to time.Time) (map[string]promMeta, error) {
	out := map[string]promMeta{}
	fromS, toS := from.UTC().Format(tsLayout), to.UTC().Format(tsLayout)
	res, err := s.db.Query(ctx, `SELECT name, kind, MAX(unit) FROM metrics WHERE created_at >= ? AND created_at < ? GROUP BY name, kind ORDER BY name LIMIT 5000`, fromS, toS)
	if err != nil && !missingTable(err, "metrics") {
		return nil, dbErr(err)
	}
	if err == nil {
		for _, row := range res.Rows {
			name, kind, unit := toString(row[0]), toString(row[1]), toString(row[2])
			if kind == "histogram" {
				out[name] = promMeta{Type: "gauge", Help: "The mean of each interval's observations", Unit: unit}
				for _, p := range histParts(name) {
					out[p.name] = promMeta{Type: "histogram", Help: "Custom histogram", Unit: unit}
				}
				continue
			}
			out[name] = promMeta{Type: kind, Help: "Custom " + kind, Unit: unit}
		}
	}
	has := map[string]bool{}
	for _, b := range s.promBuiltins() {
		ok, seen := has[b.table]
		if !seen {
			r, err := s.db.Query(ctx, `SELECT 1 FROM `+b.table+` WHERE created_at >= ? AND created_at < ? LIMIT 1`, fromS, toS)
			ok = err == nil && len(r.Rows) > 0
			has[b.table] = ok
		}
		if !ok {
			continue
		}
		if b.kind == "histogram" {
			for _, p := range histParts(b.name) {
				out[p.name] = promMeta{Type: "histogram", Help: b.help, Unit: b.unit}
			}
			continue
		}
		out[b.name] = promMeta{Type: b.kind, Help: b.help, Unit: b.unit}
	}
	return out, nil
}

func (s *Server) handlePromMetadata(w http.ResponseWriter, r *http.Request) {
	start, end, err := s.metaRange(r)
	if err != nil {
		promFail(w, http.StatusBadRequest, "bad_data", err.Error())
		return
	}
	meta, err := s.promMetadata(r.Context(), start, end)
	if err != nil {
		s.promFailErr(w, err)
		return
	}
	only := r.URL.Query().Get("metric")
	out := map[string][]promMeta{}
	for n, m := range meta {
		if only == "" || n == only {
			out[n] = []promMeta{m}
		}
	}
	promOK(w, out)
}

func (s *Server) handlePromBuildInfo(w http.ResponseWriter, _ *http.Request) {
	_, commit := buildInfo()
	promOK(w, map[string]string{"version": "2.53.0", "revision": commit, "branch": "visionary", "buildUser": "visionary", "buildDate": "", "goVersion": runtime.Version()})
}

func (s *Server) handlePromChart(w http.ResponseWriter, r *http.Request) {
	tr, err := parseRange(r.URL.Query(), s.maxRangeDays())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if st := r.URL.Query().Get("step"); st != "" {
		d, err := promDuration(st)
		if err != nil || d < time.Second {
			writeError(w, http.StatusBadRequest, "step must be a duration of at least 1s")
			return
		}
		tr.Bucket = int64(d / time.Second)
	}
	res, err := s.promResult(r.Context(), r.URL.Query().Get("query"), tr)
	if err != nil {
		var pe *promError
		if errors.As(err, &pe) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": sentence(pe.Msg), "position": pe.Pos})
			return
		}
		if errors.Is(err, errPromTooLarge) || errors.Is(err, context.DeadlineExceeded) {
			writeError(w, http.StatusUnprocessableEntity, map[bool]string{true: err.Error(), false: "the query took longer than 30 seconds"}[errors.Is(err, errPromTooLarge)])
			return
		}
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

const maxChartSeries = 50

func (s *Server) promResult(ctx context.Context, query string, tr timeRange) (*Result, error) {
	start := time.Unix(tr.From.Unix()/tr.Bucket*tr.Bucket, 0)
	end := time.Unix((tr.To.Unix()+tr.Bucket-1)/tr.Bucket*tr.Bucket, 0)
	started := time.Now()
	// A chart's step is often longer than rate(x[5m])'s window, which would leave lone points,
	// so each bucket reads its whole span instead, as Grafana's $__rate_interval does
	step := time.Duration(tr.Bucket) * time.Second
	_, v, ev, err := s.runPromQLWindows(ctx, query, start, end, step, step)
	if err != nil {
		return nil, err
	}
	if v.str != nil || v.matrix != nil {
		return nil, &promError{Msg: "the query must return a number or an instant vector"}
	}
	type line struct {
		name  string
		vals  []float64
		ok    []bool
		total float64
	}
	var lines []line
	if v.isScalar {
		ok := make([]bool, ev.n)
		for i := range ok {
			ok[i] = true
		}
		lines = append(lines, line{name: "scalar", vals: v.scalar, ok: ok})
	}
	for _, sr := range mergeSeries(v.vec) {
		ln := line{name: sr.labels.String(), vals: sr.v, ok: sr.ok}
		for i, x := range sr.v {
			if sr.ok[i] && !math.IsNaN(x) && !math.IsInf(x, 0) {
				ln.total += math.Abs(x)
			}
		}
		lines = append(lines, ln)
	}
	truncated := false
	if len(lines) > maxChartSeries {
		sort.SliceStable(lines, func(i, j int) bool { return lines[i].total > lines[j].total })
		lines, truncated = lines[:maxChartSeries], true
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].name < lines[j].name })
	rows := [][]any{}
	for i := 0; i < ev.n; i++ {
		for _, ln := range lines {
			if x := ln.vals[i]; ln.ok[i] && !math.IsNaN(x) && !math.IsInf(x, 0) {
				rows = append(rows, []any{ev.at(i) / 1000, ln.name, x})
			}
		}
	}
	return &Result{Columns: []string{"t", "series", "value"}, Rows: rows, Truncated: truncated, Bucket: tr.Bucket, SQL: query,
		DurationMs: float64(time.Since(started).Milliseconds()), From: tr.From.Format(time.RFC3339Nano), To: tr.To.Format(time.RFC3339Nano)}, nil
}
