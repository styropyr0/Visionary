package analytics

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/styropyr0/visionary/sdks/go"
)

// Where PromQL selectors read from: custom metrics in the metrics table,
// histograms exposed Prometheus-style as name_bucket{le}, name_sum and
// name_count, and Visionary's own data as built-in visionary_* series
// aggregated from api_logs, system_metrics, spans and errors.

const (
	maxPromRows    = 500000
	maxPromCatalog = 20000
)

// promBuiltin is a series family computed from one of Visionary's tables.
type promBuiltin struct {
	name, kind, help, unit string
	table                  string
	labels                 [][2]string
	// value is a gauge's column; dur a histogram's.
	value, dur string
}

// durationBounds are the le bounds of the built-in duration histograms, in ms.
var durationBounds = []float64{5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000}

var systemGauges = []struct{ col, help, unit string }{
	{"host_cpu_percent", "Host CPU use", "percent"}, {"load_1", "Load average over 1 minute", ""},
	{"host_mem_percent", "Host memory in use", "percent"}, {"host_mem_used_mb", "Host memory used", "MB"},
	{"disk_used_percent", "Disk space in use", "percent"}, {"disk_free_gb", "Free disk space", "GB"},
	{"net_rx_kbps", "Network received", "kB/s"}, {"net_tx_kbps", "Network sent", "kB/s"},
	{"proc_cpu_percent", "The process's CPU, percent of one core", "percent"}, {"proc_rss_mb", "The process's resident memory", "MB"},
	{"goroutines", "Goroutines, or the runtime's threads in other languages", ""}, {"heap_alloc_mb", "Heap allocated", "MB"},
	{"gc_pause_ms", "GC pause per sample", "ms"}, {"db_acquired_conns", "Database connections in use", ""},
	{"db_max_conns", "Database pool size", ""}, {"db_acquire_wait_ms", "Wait for a database connection per sample", "ms"},
	{"http_rps", "Requests per second", ""}, {"http_in_flight", "Requests in flight", ""},
	{"container_cpu_cores", "The container's CPU in cores", ""}, {"container_mem_used_mb", "The container's memory", "MB"},
}

func (s *Server) promBuiltins() []promBuiltin {
	status := s.q.text("status")
	out := []promBuiltin{
		{name: "visionary_http_requests_total", kind: "counter", help: "HTTP requests served", table: "api_logs",
			labels: [][2]string{{"service", "service"}, {"route", "COALESCE(route, '')"}, {"method", "method"}, {"status", status}}},
		{name: "visionary_http_request_duration_ms", kind: "histogram", unit: "ms", help: "HTTP request duration", table: "api_logs", dur: "duration_ms",
			labels: [][2]string{{"service", "service"}, {"route", "COALESCE(route, '')"}, {"method", "method"}}},
		{name: "visionary_spans_total", kind: "counter", help: "Spans: database, HTTP and custom calls", table: "spans",
			labels: [][2]string{{"service", "service"}, {"kind", "kind"}, {"status", "status"}}},
		{name: "visionary_span_duration_ms", kind: "histogram", unit: "ms", help: "Span duration", table: "spans", dur: "duration_ms",
			labels: [][2]string{{"service", "service"}, {"kind", "kind"}}},
		{name: "visionary_errors_total", kind: "counter", help: "Errors reported by the SDKs", table: "errors",
			labels: [][2]string{{"service", "service"}, {"type", "type"}, {"route", "COALESCE(route, '')"}}},
	}
	for _, g := range systemGauges {
		out = append(out, promBuiltin{name: "visionary_system_" + g.col, kind: "gauge", help: g.help, unit: g.unit, table: "system_metrics", value: g.col,
			labels: [][2]string{{"service", "service"}, {"instance", "instance"}}})
	}
	return out
}

// promTarget is one exposed metric name and where it comes from.
type promTarget struct {
	name    string
	base    string
	kind    string
	part    string // "", "bucket", "sum" or "count" of a histogram
	builtin *promBuiltin
}

func (t promTarget) counter() bool {
	return t.kind == "counter" || t.part == "bucket" || t.part == "sum" || t.part == "count"
}

func histParts(name string) []promTarget {
	return []promTarget{{name: name + "_bucket", part: "bucket"}, {name: name + "_sum", part: "sum"}, {name: name + "_count", part: "count"}}
}

func nameMatches(ms []labelMatcher, name string) bool {
	for _, m := range ms {
		if m.name == "__name__" && !m.matches(name) {
			return false
		}
	}
	return true
}

// promTargets resolves the names a selector can read in [from, to).
func (s *Server) promTargets(ctx context.Context, ms []labelMatcher, from, to time.Time) ([]promTarget, error) {
	fixed := ""
	for _, m := range ms {
		if m.name == "__name__" && m.op == "=" {
			fixed = m.value
		}
	}
	var out []promTarget
	for _, b := range s.promBuiltins() {
		parts := []promTarget{{name: b.name}}
		if b.kind == "histogram" {
			parts = histParts(b.name)
		}
		for _, p := range parts {
			if nameMatches(ms, p.name) {
				p.base, p.kind, p.builtin = b.name, b.kind, &b
				out = append(out, p)
			}
		}
	}
	where := "created_at >= ? AND created_at < ?"
	args := []any{from.UTC().Format(tsLayout), to.UTC().Format(tsLayout)}
	if fixed != "" {
		names := []any{fixed}
		for _, suf := range []string{"_bucket", "_sum", "_count"} {
			if base, ok := strings.CutSuffix(fixed, suf); ok {
				names = append(names, base)
			}
		}
		where += " AND name IN (?" + strings.Repeat(", ?", len(names)-1) + ")"
		args = append(args, names...)
	}
	res, err := s.db.Query(ctx, `SELECT DISTINCT name, kind FROM metrics WHERE `+where+` LIMIT 5000`, args...)
	if err != nil {
		if missingTable(err, "metrics") {
			return out, nil
		}
		return nil, dbErr(err)
	}
	for _, row := range res.Rows {
		name, kind := toString(row[0]), toString(row[1])
		if nameMatches(ms, name) {
			out = append(out, promTarget{name: name, base: name, kind: kind})
		}
		if kind == "histogram" {
			for _, p := range histParts(name) {
				if nameMatches(ms, p.name) {
					p.base, p.kind = name, kind
					out = append(out, p)
				}
			}
		}
	}
	return out, nil
}

type sqlPromSource struct {
	s    *Server
	rows int
}

func (src *sqlPromSource) load(ctx context.Context, vs *vectorSel, w promWindow) ([]*rawSeries, error) {
	targets, err := src.s.promTargets(ctx, vs.matchers, time.UnixMilli(w.from), time.UnixMilli(w.to))
	if err != nil {
		return nil, err
	}
	var out []*rawSeries
	for _, t := range targets {
		var series []*rawSeries
		if t.builtin != nil {
			series, err = src.loadBuiltin(ctx, t, vs.matchers, w)
		} else {
			series, err = src.loadCustom(ctx, t, vs.matchers, w)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, series...)
		if len(out) > maxPromSeries {
			return nil, errPromTooLarge
		}
	}
	return out, nil
}

// bucketPhase is created_at floored to res seconds counted from phase, as epoch seconds.
func (q sqlDialect) bucketPhase(res, phase int64) string {
	e := q.epoch()
	if phase != 0 {
		e = "(" + e + " - " + strconv.FormatInt(phase, 10) + ")"
	}
	div := " / "
	if q.d == sdk.MySQL {
		div = " DIV "
	}
	r := strconv.FormatInt(res, 10)
	return "((" + e + div + r + ") * " + r + " + " + strconv.FormatInt(phase, 10) + ")"
}

// windowArgs are the created_at bounds of the buckets ending in (from, to].
func windowArgs(w promWindow) []any {
	return []any{time.UnixMilli(w.from).UTC().Format(tsLayout), time.UnixMilli(w.to).UTC().Format(tsLayout)}
}

func (src *sqlPromSource) query(ctx context.Context, sql string, args ...any) ([][]any, error) {
	res, err := src.s.db.Query(ctx, sql+" LIMIT "+strconv.Itoa(maxPromRows+1), args...)
	if err != nil {
		return nil, err
	}
	src.rows += len(res.Rows)
	if len(res.Rows) > maxPromRows || src.rows > 4*maxPromRows {
		return nil, errPromTooLarge
	}
	return res.Rows, nil
}

func labelsMatchAll(l promLabels, ms []labelMatcher) bool {
	for _, m := range ms {
		if m.name != "__name__" && !m.matches(l[m.name]) {
			return false
		}
	}
	return true
}

type customSeries struct {
	id, service, instance string
	labels                promLabels
	bounds                []float64
}

// customCatalog lists the series of custom metric names in [from, to).
func (s *Server) customCatalog(ctx context.Context, names []string, from, to time.Time) (map[string][]customSeries, error) {
	where := "created_at >= ? AND created_at < ?"
	args := []any{from.UTC().Format(tsLayout), to.UTC().Format(tsLayout)}
	if len(names) > 0 {
		where += " AND name IN (?" + strings.Repeat(", ?", len(names)-1) + ")"
		for _, n := range names {
			args = append(args, n)
		}
	}
	res, err := s.db.Query(ctx, `SELECT name, kind, series, service, instance, MAX(labels), MAX(buckets) FROM metrics WHERE `+where+
		` GROUP BY name, kind, series, service, instance LIMIT `+strconv.Itoa(maxPromCatalog+1), args...)
	if err != nil {
		if missingTable(err, "metrics") {
			return nil, nil
		}
		return nil, dbErr(err)
	}
	if len(res.Rows) > maxPromCatalog {
		return nil, errPromTooLarge
	}
	out := map[string][]customSeries{}
	for _, row := range res.Rows {
		cs := customSeries{id: toString(row[2]), service: toString(row[3]), instance: toString(row[4])}
		var raw map[string]string
		_ = json.Unmarshal([]byte(toString(row[5])), &raw)
		cs.labels = promLabels{}
		for k, v := range raw {
			if v != "" {
				cs.labels[k] = v
			}
		}
		for k, v := range map[string]string{"service": cs.service, "instance": cs.instance} {
			if _, ok := cs.labels[k]; !ok && v != "" {
				cs.labels[k] = v
			}
		}
		if b := toString(row[6]); b != "" {
			var h struct {
				Bounds []float64 `json:"bounds"`
			}
			_ = json.Unmarshal([]byte(b), &h)
			cs.bounds = h.Bounds
		}
		k := toString(row[0]) + "\n" + toString(row[1])
		out[k] = append(out[k], cs)
	}
	return out, nil
}

func leLabel(b float64) string {
	if math.IsInf(b, 1) {
		return "+Inf"
	}
	return strconv.FormatFloat(b, 'f', -1, 64)
}

func (src *sqlPromSource) loadCustom(ctx context.Context, t promTarget, ms []labelMatcher, w promWindow) ([]*rawSeries, error) {
	s := src.s
	cat, err := s.customCatalog(ctx, []string{t.base}, time.UnixMilli(w.from), time.UnixMilli(w.to))
	if err != nil {
		return nil, err
	}
	var leMatchers, others []labelMatcher
	for _, m := range ms {
		if m.name == "le" && t.part == "bucket" {
			leMatchers = append(leMatchers, m)
		} else {
			others = append(others, m)
		}
	}
	byKey := map[string]*customSeries{}
	idSet := map[string]bool{}
	var ids []string
	all := cat[t.base+"\n"+t.kind]
	for i := range all {
		cs := &all[i]
		if !labelsMatchAll(cs.labels, others) {
			continue
		}
		byKey[cs.id+"\n"+cs.service+"\n"+cs.instance] = cs
		if seriesID.MatchString(cs.id) && !idSet[cs.id] {
			idSet[cs.id] = true
			ids = append(ids, "'"+cs.id+"'")
		}
	}
	if len(byKey) == 0 {
		return nil, nil
	}
	where := "name = ? AND kind = ? AND created_at >= ? AND created_at < ?"
	args := append([]any{t.base, t.kind}, windowArgs(w)...)
	if len(byKey) < len(all) {
		where += " AND series IN (" + strings.Join(ids, ", ") + ")"
	}
	tcol := s.q.bucketPhase(w.res, w.phase)
	labelsOf := func(cs *customSeries, le string) promLabels {
		l := promLabels{"__name__": t.name}
		for k, v := range cs.labels {
			l[k] = v
		}
		if le != "" {
			l["le"] = le
		}
		return l
	}
	out := map[string]*rawSeries{}
	var order []string
	get := func(key string, cs *customSeries, le string) *rawSeries {
		k := key + "\n" + le
		rs := out[k]
		if rs == nil {
			rs = &rawSeries{labels: labelsOf(cs, le), counter: t.counter()}
			out[k] = rs
			order = append(order, k)
		}
		return rs
	}
	if t.part == "bucket" {
		rows, err := src.query(ctx, `SELECT `+tcol+` AS t, series, service, instance, buckets FROM metrics WHERE `+where+` AND buckets IS NOT NULL ORDER BY 1`, args...)
		if err != nil {
			return nil, err
		}
		type cellKey struct {
			key string
			t   int64
		}
		cells := map[cellKey]map[float64]float64{}
		var cellOrder []cellKey
		for _, row := range rows {
			key := toString(row[1]) + "\n" + toString(row[2]) + "\n" + toString(row[3])
			if byKey[key] == nil {
				continue
			}
			var h struct {
				Bounds []float64 `json:"bounds"`
				Counts []float64 `json:"counts"`
			}
			if json.Unmarshal([]byte(toString(row[4])), &h) != nil || len(h.Counts) != len(h.Bounds)+1 {
				continue
			}
			ck := cellKey{key, toInt(row[0])}
			c := cells[ck]
			if c == nil {
				c = map[float64]float64{}
				cells[ck] = c
				cellOrder = append(cellOrder, ck)
			}
			for i, n := range h.Counts {
				c[bucketBound(h.Bounds, i)] += n
			}
		}
		for _, ck := range cellOrder {
			c := cells[ck]
			bounds := make([]float64, 0, len(c))
			for b := range c {
				bounds = append(bounds, b)
			}
			sort.Float64s(bounds)
			cum := 0.0
			for _, b := range bounds {
				cum += c[b]
				le := leLabel(b)
				if !matchAll(leMatchers, le) {
					continue
				}
				rs := get(ck.key, byKey[ck.key], le)
				rs.samples = append(rs.samples, rawSample{T: (ck.t + w.res) * 1000, V: cum, Min: cum, Max: cum, N: 1})
			}
		}
	} else {
		rows, err := src.query(ctx, `SELECT `+tcol+` AS t, series, service, instance, SUM(value), COUNT(*), MIN(value), MAX(value), SUM(sum), SUM(count), MIN(min), MAX(max)
FROM metrics WHERE `+where+` GROUP BY 1, 2, 3, 4 ORDER BY 1`, args...)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			key := toString(row[1]) + "\n" + toString(row[2]) + "\n" + toString(row[3])
			cs := byKey[key]
			if cs == nil {
				continue
			}
			f := func(i int) float64 { v, _ := cellFloat(row[i]); return v }
			n := f(5)
			smp := rawSample{T: (toInt(row[0]) + w.res) * 1000, N: n}
			switch {
			case t.part == "sum":
				smp.V = f(8)
			case t.part == "count":
				smp.V = f(9)
			case t.kind == "histogram":
				if f(9) <= 0 {
					continue
				}
				smp.V, smp.Min, smp.Max, smp.N = f(8)/f(9), f(10), f(11), f(9)
			case t.kind == "counter":
				smp.V = f(4)
			default:
				smp.V, smp.Min, smp.Max = f(4)/math.Max(n, 1), f(6), f(7)
			}
			if t.counter() {
				smp.Min, smp.Max = smp.V, smp.V
			}
			rs := get(key, cs, "")
			rs.samples = append(rs.samples, smp)
		}
	}
	res := make([]*rawSeries, 0, len(order))
	for _, k := range order {
		res = append(res, out[k])
	}
	return res, nil
}

func matchAll(ms []labelMatcher, v string) bool {
	for _, m := range ms {
		if !m.matches(v) {
			return false
		}
	}
	return true
}

func (src *sqlPromSource) loadBuiltin(ctx context.Context, t promTarget, ms []labelMatcher, w promWindow) ([]*rawSeries, error) {
	b := t.builtin
	where, args := builtinWhere(b, ms)
	where = "created_at >= ? AND created_at < ?" + where
	args = append(windowArgs(w), args...)
	cols := []string{src.s.q.bucketPhase(w.res, w.phase) + " AS t"}
	group := []string{"1"}
	for i, l := range b.labels {
		cols = append(cols, l[1])
		group = append(group, strconv.Itoa(i+2))
	}
	nl := len(b.labels)
	var bounds []float64
	switch {
	case b.kind == "gauge":
		cols = append(cols, "AVG("+b.value+")", "MIN("+b.value+")", "MAX("+b.value+")", "COUNT("+b.value+")")
		where += " AND " + b.value + " IS NOT NULL"
	case t.part == "sum":
		cols = append(cols, "SUM("+b.dur+")")
	case t.part == "bucket":
		cols = append(cols, "COUNT(*)")
		for _, bd := range durationBounds {
			cols = append(cols, countIf(b.dur+" <= "+strconv.FormatFloat(bd, 'f', -1, 64)))
		}
		bounds = append(slices.Clone(durationBounds), math.Inf(1))
	default:
		cols = append(cols, "COUNT(*)")
	}
	rows, err := src.query(ctx, `SELECT `+strings.Join(cols, ", ")+` FROM `+b.table+` WHERE `+where+` GROUP BY `+strings.Join(group, ", ")+` ORDER BY 1`, args...)
	if err != nil {
		if missingTable(err, b.table) {
			return nil, nil
		}
		return nil, dbErr(err)
	}
	var leMatchers []labelMatcher
	for _, m := range ms {
		if m.name == "le" && t.part == "bucket" {
			leMatchers = append(leMatchers, m)
		}
	}
	out := map[string]*rawSeries{}
	var order []string
	add := func(l promLabels, smp rawSample) {
		k := l.key()
		rs := out[k]
		if rs == nil {
			rs = &rawSeries{labels: l, counter: t.counter()}
			out[k] = rs
			order = append(order, k)
		}
		rs.samples = append(rs.samples, smp)
	}
	for _, row := range rows {
		l := promLabels{"__name__": t.name}
		for i, lc := range b.labels {
			if v := toString(row[i+1]); v != "" {
				l[lc[0]] = v
			}
		}
		if !labelsMatchAll(l, filterOut(ms, "le", t.part == "bucket")) {
			continue
		}
		T := (toInt(row[0]) + w.res) * 1000
		f := func(i int) float64 { v, _ := cellFloat(row[nl+1+i]); return v }
		switch {
		case b.kind == "gauge":
			add(l, rawSample{T: T, V: f(0), Min: f(1), Max: f(2), N: f(3)})
		case t.part == "bucket":
			for i, bd := range bounds {
				le := leLabel(bd)
				if !matchAll(leMatchers, le) {
					continue
				}
				v := f(0)
				if i < len(durationBounds) {
					v = f(i + 1)
				}
				bl := l.without()
				bl["le"] = le
				add(bl, rawSample{T: T, V: v, Min: v, Max: v, N: 1})
			}
		default:
			v := f(0)
			add(l, rawSample{T: T, V: v, Min: v, Max: v, N: 1})
		}
	}
	res := make([]*rawSeries, 0, len(order))
	for _, k := range order {
		res = append(res, out[k])
	}
	return res, nil
}

func filterOut(ms []labelMatcher, name string, drop bool) []labelMatcher {
	if !drop {
		return ms
	}
	return slices.DeleteFunc(slices.Clone(ms), func(m labelMatcher) bool { return m.name == name })
}

// builtinWhere pushes equality matchers on a family's labels into SQL.
func builtinWhere(b *promBuiltin, ms []labelMatcher) (string, []any) {
	var where string
	var args []any
	for _, m := range ms {
		if m.op != "=" || m.value == "" {
			continue
		}
		for _, l := range b.labels {
			if l[0] == m.name {
				where += " AND " + l[1] + " = ?"
				args = append(args, m.value)
			}
		}
	}
	return where, args
}

// promCatalog lists the label sets of the series the selectors match in
// [from, to), or of every series without selectors.
func (s *Server) promCatalog(ctx context.Context, sels [][]labelMatcher, from, to time.Time) ([]promLabels, bool, error) {
	if len(sels) == 0 {
		sels = [][]labelMatcher{nil}
	}
	seen := map[string]bool{}
	var out []promLabels
	truncated := false
	add := func(l promLabels, ms []labelMatcher) {
		if !labelsMatchAll(l, ms) || seen[l.key()] {
			return
		}
		if len(out) >= maxPromCatalog {
			truncated = true
			return
		}
		seen[l.key()] = true
		out = append(out, l)
	}
	for _, ms := range sels {
		targets, err := s.promTargets(ctx, ms, from, to)
		if err != nil {
			return nil, false, err
		}
		var custom []string
		builtinRows := map[string][][]any{}
		for _, t := range targets {
			if t.builtin == nil {
				if !slices.Contains(custom, t.base) {
					custom = append(custom, t.base)
				}
				continue
			}
			b := t.builtin
			rows, ok := builtinRows[b.name]
			if !ok {
				where, args := builtinWhere(b, ms)
				cols := make([]string, len(b.labels))
				for i, l := range b.labels {
					cols[i] = l[1]
				}
				res, err := s.db.Query(ctx, `SELECT DISTINCT `+strings.Join(cols, ", ")+` FROM `+b.table+` WHERE created_at >= ? AND created_at < ?`+where+` LIMIT 5000`,
					append([]any{from.UTC().Format(tsLayout), to.UTC().Format(tsLayout)}, args...)...)
				if err != nil && !missingTable(err, b.table) {
					return nil, false, dbErr(err)
				}
				if res != nil {
					rows = res.Rows
				}
				builtinRows[b.name] = rows
			}
			for _, row := range rows {
				l := promLabels{"__name__": t.name}
				for i, lc := range b.labels {
					if v := toString(row[i]); v != "" {
						l[lc[0]] = v
					}
				}
				if t.part == "bucket" {
					for _, bd := range append(slices.Clone(durationBounds), math.Inf(1)) {
						bl := l.without()
						bl["le"] = leLabel(bd)
						add(bl, ms)
					}
					continue
				}
				add(l, ms)
			}
		}
		if len(custom) == 0 {
			continue
		}
		cat, err := s.customCatalog(ctx, head(custom, 90), from, to)
		if err != nil {
			return nil, false, err
		}
		for _, t := range targets {
			if t.builtin != nil {
				continue
			}
			for _, cs := range cat[t.base+"\n"+t.kind] {
				l := promLabels{"__name__": t.name}
				for k, v := range cs.labels {
					l[k] = v
				}
				if t.part == "bucket" {
					for i := 0; i <= len(cs.bounds); i++ {
						bl := l.without()
						bl["le"] = leLabel(bucketBound(cs.bounds, i))
						add(bl, ms)
					}
					continue
				}
				add(l, ms)
			}
		}
	}
	return out, truncated, nil
}
