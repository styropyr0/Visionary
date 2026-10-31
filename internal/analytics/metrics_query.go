package analytics

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Group is a label key, or ":service" or ":instance" for those columns
type metricQuery struct {
	Name    string            `json:"name"`
	Agg     string            `json:"agg,omitempty"`
	Group   string            `json:"group,omitempty"`
	Labels  map[string]string `json:"labels,omitempty"`
	Service string            `json:"-"`
}

var metricAggs = []string{"sum", "avg", "max", "min", "rate", "count", "p50", "p95", "p99"}

const (
	maxMetricGroups  = 20
	maxCatalogSeries = 5000
	maxBucketRows    = 200000
)

var seriesID = regexp.MustCompile(`^[0-9a-f]{16}$`)

type metricInfo struct {
	Kind      string `json:"kind"`
	Unit      string `json:"unit"`
	Series    int    `json:"series"`
	Agg       string `json:"agg"`
	SQL       string `json:"-"`
	Truncated bool   `json:"-"`
}

// maxInlineSeries keeps a statement listing series ids well under D1's 100 KB
const maxInlineSeries = 2000

func defaultAgg(kind string) string {
	switch kind {
	case "counter":
		return "sum"
	case "histogram":
		return "p95"
	}
	return "avg"
}

func checkAgg(kind, agg string) error {
	if !slices.Contains(metricAggs, agg) {
		return badInput("aggregation must be one of " + strings.Join(metricAggs, ", "))
	}
	if kind == "histogram" {
		return nil
	}
	switch agg {
	case "p50", "p95", "p99", "count":
		return badInput(agg + " needs a histogram; " + kind + "s take sum, avg, max, min" + map[bool]string{true: " or rate", false: ""}[kind == "counter"])
	case "rate":
		if kind == "gauge" {
			return badInput("rate needs a counter or a histogram; a gauge is already a level")
		}
	}
	return nil
}

type catalogSeries struct {
	labels map[string]string
	kind   string
	unit   string
	rows   int64
}

func (s *Server) metricCatalog(ctx context.Context, name, service string, from, to time.Time) (map[string]*catalogSeries, error) {
	where, args := metricWhere(name, service, from, to)
	res, err := s.db.Query(ctx, `SELECT series, MAX(labels), MAX(kind), MAX(unit), COUNT(*) FROM metrics WHERE `+where+
		` GROUP BY series LIMIT `+strconv.Itoa(maxCatalogSeries), args...)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*catalogSeries, len(res.Rows))
	for _, row := range res.Rows {
		cs := &catalogSeries{kind: toString(row[2]), unit: toString(row[3]), rows: toInt(row[4])}
		_ = json.Unmarshal([]byte(toString(row[1])), &cs.labels)
		out[toString(row[0])] = cs
	}
	return out, nil
}

func metricWhere(name, service string, from, to time.Time) (string, []any) {
	where := "name = ? AND created_at >= ? AND created_at < ?"
	args := []any{name, from.UTC().Format(tsLayout), to.UTC().Format(tsLayout)}
	if service != "" {
		where += " AND service = ?"
		args = append(args, service)
	}
	return where, args
}

type metricCell struct {
	total, n      float64
	sum, count    float64
	max, min      float64
	hasMax, hasMn bool
	buckets       map[float64]float64
}

func (c *metricCell) take(v float64) {
	c.total += v
	c.n++
	if !c.hasMax || v > c.max {
		c.max, c.hasMax = v, true
	}
	if !c.hasMn || v < c.min {
		c.min, c.hasMn = v, true
	}
}

func (s *Server) runMetricQuery(ctx context.Context, mq metricQuery, from, to time.Time, bucket int64) ([]point, metricInfo, error) {
	var info metricInfo
	catalog, err := s.metricCatalog(ctx, mq.Name, mq.Service, from, to)
	if err != nil {
		return nil, info, err
	}
	var matched []string
	kinds := map[string]int64{}
	for id, cs := range catalog {
		if !labelsMatch(cs.labels, mq.Labels) {
			continue
		}
		matched = append(matched, id)
		kinds[cs.kind] += cs.rows
		if info.Unit == "" {
			info.Unit = cs.unit
		}
	}
	info.Series = len(matched)
	for k, n := range kinds {
		if info.Kind == "" || n > kinds[info.Kind] {
			info.Kind = k
		}
	}
	info.Agg = fallback(mq.Agg, defaultAgg(info.Kind))
	if len(matched) == 0 {
		return nil, info, checkAgg(fallback(info.Kind, "gauge"), info.Agg)
	}
	if err := checkAgg(info.Kind, info.Agg); err != nil {
		return nil, info, err
	}
	sort.Strings(matched)

	where, args := metricWhere(mq.Name, mq.Service, from, to)
	where += " AND kind = ?"
	args = append(args, info.Kind)
	var keep map[string]bool
	if len(matched) > maxInlineSeries && len(matched) < len(catalog) {
		keep = map[string]bool{}
		for _, id := range matched {
			keep[id] = true
		}
	} else if len(matched) < len(catalog) {
		ids := make([]string, 0, len(matched))
		for _, id := range matched {
			if seriesID.MatchString(id) {
				ids = append(ids, "'"+id+"'")
			}
		}
		// Series ids are checked hex, so they are written inline: D1 binds 100 params at most
		where += " AND series IN (" + strings.Join(ids, ", ") + ")"
	}
	t := "0"
	if bucket > 0 {
		t = s.q.bucketOf(strconv.FormatInt(bucket, 10))
	}
	col, groupBy := "", "1, 2"
	switch mq.Group {
	case ":service":
		col, groupBy = ", service", "1, 2, 3"
	case ":instance":
		col, groupBy = ", instance", "1, 2, 3"
	}
	pct := map[string]float64{"p50": 0.5, "p95": 0.95, "p99": 0.99}[info.Agg]
	var sql string
	if pct > 0 {
		sql = `SELECT ` + t + ` AS t, series` + col + `, buckets FROM metrics WHERE ` + where + ` AND buckets IS NOT NULL ORDER BY 1 LIMIT ` + strconv.Itoa(maxBucketRows+1)
	} else {
		sql = `SELECT ` + t + ` AS t, series` + col + `, SUM(value), COUNT(*), MAX(value), MIN(value), SUM(count), SUM(sum), MAX(max), MIN(min)
FROM metrics WHERE ` + where + ` GROUP BY ` + groupBy + ` ORDER BY 1`
	}
	info.SQL = sql
	res, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, info, err
	}
	if len(res.Rows) > maxBucketRows {
		res.Rows, info.Truncated = res.Rows[:maxBucketRows], true
	}

	groupOf := func(row []any) string {
		switch {
		case col != "":
			return toString(row[2])
		case mq.Group != "":
			if cs := catalog[toString(row[1])]; cs != nil {
				return cs.labels[mq.Group]
			}
			return ""
		}
		return mq.Name
	}
	type cellKey struct {
		t int64
		g string
	}
	cells := map[cellKey]*metricCell{}
	off := 2
	if col != "" {
		off = 3
	}
	for _, row := range res.Rows {
		if keep != nil && !keep[toString(row[1])] {
			continue
		}
		k := cellKey{toInt(row[0]), groupOf(row)}
		c := cells[k]
		if c == nil {
			c = &metricCell{}
			cells[k] = c
		}
		if pct > 0 {
			addBuckets(c, toString(row[off]))
			continue
		}
		f := func(i int) float64 { v, _ := cellFloat(row[off+i]); return v }
		sumV, n, maxV, minV, hCount, hSum := f(0), f(1), f(2), f(3), f(4), f(5)
		c.count += hCount
		c.sum += hSum
		switch info.Kind {
		case "counter":
			c.take(sumV)
		case "gauge":
			switch info.Agg {
			case "max":
				c.take(maxV)
			case "min":
				c.take(minV)
			default:
				c.take(sumV / math.Max(n, 1))
			}
		case "histogram":
			switch info.Agg {
			case "max":
				if v, ok := cellFloat(row[off+6]); ok {
					c.take(v)
				}
			case "min":
				if v, ok := cellFloat(row[off+7]); ok {
					c.take(v)
				}
			default:
				c.take(hSum)
			}
		}
	}

	secs := float64(bucket)
	if bucket == 0 {
		secs = to.Sub(from).Seconds()
	}
	totals := map[string]float64{}
	var pts []point
	for k, c := range cells {
		var v float64
		ok := true
		switch {
		case pct > 0:
			v, ok = bucketQuantile(c.buckets, pct)
		case info.Agg == "sum":
			v = c.total
		case info.Agg == "count":
			v = c.count
		case info.Agg == "rate" && info.Kind == "histogram":
			v = c.count / secs
		case info.Agg == "rate":
			v = c.total / secs
		case info.Agg == "avg" && info.Kind == "histogram":
			v, ok = c.sum/c.count, c.count > 0
		case info.Agg == "avg":
			v, ok = c.total/c.n, c.n > 0
		case info.Agg == "max":
			v, ok = c.max, c.hasMax
		case info.Agg == "min":
			v, ok = c.min, c.hasMn
		}
		if !ok || math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		pts = append(pts, point{T: k.t, Series: k.g, Value: v, N: int64(c.n)})
		totals[k.g] += math.Abs(v)
	}
	if len(totals) > maxMetricGroups {
		names := make([]string, 0, len(totals))
		for g := range totals {
			names = append(names, g)
		}
		sort.Slice(names, func(i, j int) bool { return totals[names[i]] > totals[names[j]] })
		keep := map[string]bool{}
		for _, g := range names[:maxMetricGroups] {
			keep[g] = true
		}
		pts = slices.DeleteFunc(pts, func(p point) bool { return !keep[p.Series] })
		info.Truncated = true
	}
	if mq.Group != "" {
		for i := range pts {
			if pts[i].Series == "" {
				pts[i].Series = "(none)"
			}
		}
	}
	sort.Slice(pts, func(i, j int) bool {
		if pts[i].T != pts[j].T {
			return pts[i].T < pts[j].T
		}
		return pts[i].Series < pts[j].Series
	})
	return pts, info, nil
}

func labelsMatch(have, want map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

func addBuckets(c *metricCell, raw string) {
	var b struct {
		Bounds []float64 `json:"bounds"`
		Counts []float64 `json:"counts"`
	}
	if json.Unmarshal([]byte(raw), &b) != nil || len(b.Counts) != len(b.Bounds)+1 {
		return
	}
	if c.buckets == nil {
		c.buckets = map[float64]float64{}
	}
	for i, n := range b.Counts {
		c.buckets[bucketBound(b.Bounds, i)] += n
	}
	c.n++
}

func bucketQuantile(buckets map[float64]float64, q float64) (float64, bool) {
	bounds := make([]float64, 0, len(buckets))
	total := 0.0
	for b, n := range buckets {
		bounds = append(bounds, b)
		total += n
	}
	if total <= 0 {
		return 0, false
	}
	sort.Float64s(bounds)
	rank := q * total
	cum := 0.0
	for i, b := range bounds {
		n := buckets[b]
		if cum+n < rank || n == 0 {
			cum += n
			continue
		}
		lower := 0.0
		if i > 0 {
			lower = bounds[i-1]
		} else if b < 0 {
			return b, true
		}
		if math.IsInf(b, 1) {
			return lower, i > 0
		}
		return lower + (b-lower)*(rank-cum)/n, true
	}
	return bounds[len(bounds)-1], !math.IsInf(bounds[len(bounds)-1], 1)
}

func metricQueryOf(r *http.Request) (metricQuery, error) {
	q := r.URL.Query()
	mq := metricQuery{Name: strings.TrimSpace(q.Get("name")), Agg: q.Get("agg"), Group: strings.TrimSpace(q.Get("group")), Service: strings.TrimSpace(q.Get("service"))}
	if mq.Name == "" {
		return mq, badInput("name a metric")
	}
	if raw := strings.TrimSpace(q.Get("labels")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &mq.Labels); err != nil {
			return mq, badInput("labels must be a JSON object of strings")
		}
	}
	return mq, nil
}

func (s *Server) handleMetricSeries(w http.ResponseWriter, r *http.Request) {
	tr, err := parseRange(r.URL.Query(), s.maxRangeDays())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	mq, err := metricQueryOf(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	pts, info, err := s.runMetricQuery(r.Context(), mq, tr.From, tr.To, tr.Bucket)
	if err != nil {
		if missingTable(err, "metrics") {
			writeJSON(w, http.StatusOK, pointsResult(nil, tr))
			return
		}
		s.fail(w, dbErr(err))
		return
	}
	res := pointsResult(pts, tr)
	res.SQL, res.Truncated = info.SQL, info.Truncated
	writeJSON(w, http.StatusOK, res)
}

type metricName struct {
	Name     string   `json:"name"`
	Kind     string   `json:"kind"`
	Unit     string   `json:"unit"`
	Series   int64    `json:"series"`
	Rows     int64    `json:"rows"`
	Value    *float64 `json:"value"`
	LastSeen string   `json:"last_seen"`
	Dropped  int64    `json:"dropped,omitempty"`
}

func (s *Server) handleMetricNames(w http.ResponseWriter, r *http.Request) {
	tr, err := parseRange(r.URL.Query(), s.maxRangeDays())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := r.Context()
	service := strings.TrimSpace(r.URL.Query().Get("service"))
	where := "created_at >= ? AND created_at < ?"
	args := []any{tr.From.Format(tsLayout), tr.To.Format(tsLayout)}
	if service != "" {
		where += " AND service = ?"
		args = append(args, service)
	}
	res, err := s.db.Query(ctx, `SELECT name, kind, MAX(unit), COUNT(DISTINCT series), COUNT(*), MAX(created_at), SUM(value), SUM(count), SUM(sum)
FROM metrics WHERE `+where+` GROUP BY name, kind ORDER BY name LIMIT 2000`, args...)
	if err != nil {
		if missingTable(err, "metrics") {
			writeJSON(w, http.StatusOK, map[string]any{"metrics": []metricName{}, "services": []string{}})
			return
		}
		s.fail(w, dbErr(err))
		return
	}
	latest := map[string]float64{}
	lres, err := s.db.Query(ctx, `SELECT name, AVG(value) FROM (
  SELECT name, value, ROW_NUMBER() OVER (PARTITION BY series, instance ORDER BY created_at DESC, id DESC) AS rn
  FROM metrics WHERE kind = 'gauge' AND `+where+`
) l WHERE rn = 1 GROUP BY name`, args...)
	if err == nil {
		for _, row := range lres.Rows {
			if v, ok := cellFloat(row[1]); ok {
				latest[toString(row[0])] = v
			}
		}
	}
	dropped := s.metricIngest().droppedSeries()
	out := make([]metricName, 0, len(res.Rows))
	for _, row := range res.Rows {
		m := metricName{Name: toString(row[0]), Kind: toString(row[1]), Unit: toString(row[2]), Series: toInt(row[3]), Rows: toInt(row[4]),
			LastSeen: toString(row[5]), Dropped: dropped[toString(row[0])]}
		switch m.Kind {
		case "counter":
			if v, ok := cellFloat(row[6]); ok {
				m.Value = ptr(v)
			}
		case "gauge":
			if v, ok := latest[m.Name]; ok {
				m.Value = ptr(v)
			}
		case "histogram":
			c, _ := cellFloat(row[7])
			if sum, ok := cellFloat(row[8]); ok && c > 0 {
				m.Value = ptr(sum / c)
			}
		}
		out = append(out, m)
	}
	services := []string{}
	sres, err := s.db.Query(ctx, `SELECT DISTINCT service FROM metrics WHERE created_at >= ? AND created_at < ? AND service IS NOT NULL ORDER BY service LIMIT 200`,
		tr.From.Format(tsLayout), tr.To.Format(tsLayout))
	if err == nil {
		for _, row := range sres.Rows {
			services = append(services, toString(row[0]))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"metrics": out, "services": services})
}

type metricLabel struct {
	Key    string   `json:"key"`
	Values []string `json:"values"`
	Series int      `json:"series"`
}

func (s *Server) handleMetricLabels(w http.ResponseWriter, r *http.Request) {
	tr, err := parseRange(r.URL.Query(), s.maxRangeDays())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	mq, err := metricQueryOf(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	catalog, err := s.metricCatalog(r.Context(), mq.Name, mq.Service, tr.From, tr.To)
	if err != nil && !missingTable(err, "metrics") {
		s.fail(w, dbErr(err))
		return
	}
	values := map[string]map[string]int{}
	kinds := map[string]int{}
	unit := ""
	for _, cs := range catalog {
		kinds[cs.kind]++
		unit = fallback(unit, cs.unit)
		for k, v := range cs.labels {
			if values[k] == nil {
				values[k] = map[string]int{}
			}
			values[k][v]++
		}
	}
	kind := ""
	for k, n := range kinds {
		if kind == "" || n > kinds[kind] {
			kind = k
		}
	}
	labels := make([]metricLabel, 0, len(values))
	for k, vs := range values {
		l := metricLabel{Key: k}
		for v, n := range vs {
			l.Values = append(l.Values, v)
			l.Series += n
		}
		sort.Slice(l.Values, func(i, j int) bool {
			if vs[l.Values[i]] != vs[l.Values[j]] {
				return vs[l.Values[i]] > vs[l.Values[j]]
			}
			return l.Values[i] < l.Values[j]
		})
		l.Values = head(l.Values, 100)
		labels = append(labels, l)
	}
	sort.Slice(labels, func(i, j int) bool { return labels[i].Key < labels[j].Key })
	writeJSON(w, http.StatusOK, map[string]any{"name": mq.Name, "kind": kind, "unit": unit, "series": len(catalog), "labels": labels,
		"truncated": len(catalog) >= maxCatalogSeries})
}
