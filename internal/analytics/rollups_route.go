package analytics

import (
	"context"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/styropyr0/visionary/sdks/go"
)

// Query routing: a fixed panel over a long range reads the rollups when they
// cover it. The range is cut into an aligned middle read from rollup rows and
// ragged edges read from raw rows (the start before the first whole bucket
// and the minutes since the job last ran), so counts match the raw query
// exactly.

type planKey struct {
	d     sdk.Dialect
	apdex int
}

var rollupPlans sync.Map // planKey -> map[string]*rollupPanel

func (s *Server) rollupPlan(sql string) (*rollupPanel, string) {
	key := planKey{s.q.d, s.cfg.ApdexMs}
	v, ok := rollupPlans.Load(key)
	if !ok {
		v, _ = rollupPlans.LoadOrStore(key, rollupPanels(s.q, s.cfg.ApdexMs))
	}
	plans := v.(map[string]*rollupPanel)
	if p := plans[sql]; p != nil {
		return p, ""
	}
	// System panels arrive with their instance filter already filled in.
	if clause := instanceFilter.FindString(sql); clause != "" {
		if p := plans[strings.ReplaceAll(sql, clause, "{{instance}}")]; p != nil {
			return p, clause
		}
	}
	return nil, ""
}

var instanceFilter = regexp.MustCompile(` AND instance = '[A-Za-z0-9._:-]{1,128}'`)

// segment is a part of a range and where it is read from (grain 0 is raw).
type segment struct {
	grain    int64
	from, to time.Time
}

func floorTo(t time.Time, grain int64) time.Time {
	return t.Truncate(time.Duration(grain) * time.Second)
}

func ceilTo(t time.Time, grain int64) time.Time {
	f := floorTo(t, grain)
	if f.Before(t) {
		return f.Add(time.Duration(grain) * time.Second)
	}
	return f
}

// segments splits tr for one rollup kind, or reports that the rollups do not
// cover it. Time buckets must be whole multiples of the grain so a rollup row
// never straddles two of them.
func (rt *rollupRuntime) segments(kind string, tr timeRange) ([]segment, bool) {
	for _, g := range []int64{3600, 60} {
		if tr.Bucket%g != 0 {
			continue
		}
		sp := rt.span(kind, g)
		if sp.empty() {
			continue
		}
		a := ceilTo(tr.From, g)
		b := minTime(floorTo(tr.To, g), parseTS(sp.high))
		if !b.After(a) || !sp.covers(a, b) {
			continue
		}
		segs := []segment{{0, tr.From, a}, {g, a, b}}
		c := b
		if g == 3600 {
			mp := rt.span(kind, 60)
			if m := minTime(floorTo(tr.To, 60), parseTS(mp.high)); m.After(b) && mp.covers(b, m) {
				segs = append(segs, segment{60, b, m})
				c = m
			}
		}
		return append(segs, segment{0, c, tr.To}), true
	}
	return nil, false
}

// unionSQL is the derived table for segments: rollup rows and raw rows in
// the same columns.
func unionSQL(t rollupTable, segs []segment, alias string) string {
	var legs []string
	for _, sg := range segs {
		if !sg.from.Before(sg.to) {
			continue
		}
		a, b := sg.from.Format(tsLayout), sg.to.Format(tsLayout)
		if sg.grain == 0 {
			legs = append(legs, t.rawRowsSQL(a, b))
		} else {
			legs = append(legs, t.rollupRowsSQL(sg.grain, a, b))
		}
	}
	return "(\n" + strings.Join(legs, "\nUNION ALL\n") + "\n) AS " + alias
}

func sourceLabel(segs ...[]segment) string {
	coarsest := int64(0)
	for _, ss := range segs {
		for _, sg := range ss {
			if sg.grain > coarsest && sg.from.Before(sg.to) {
				coarsest = sg.grain
			}
		}
	}
	if coarsest == 3600 {
		return "rollup_1h"
	}
	return "rollup_1m"
}

// runRollup answers a fixed panel from the rollups. ok is false when the
// panel, range or coverage calls for the raw query.
func (s *Server) runRollup(ctx context.Context, sql string, tr timeRange) (res *Result, ok bool, err error) {
	rt := s.rollupRuntime()
	if rt == nil {
		return nil, false, nil
	}
	p, clause := s.rollupPlan(sql)
	if p == nil || tr.To.Sub(tr.From) < p.min {
		return nil, false, nil
	}
	var reqSegs, sysSegs []segment
	if p.needs&needRequests != 0 {
		if reqSegs, ok = rt.segments("requests", tr); !ok {
			return nil, false, nil
		}
	}
	if p.needs&needSystem != 0 {
		if sysSegs, ok = rt.segments("system", tr); !ok {
			return nil, false, nil
		}
	}
	if p.slowest {
		return s.runSlowest(ctx, sql, tr, reqSegs)
	}
	q := p.sql
	if reqSegs != nil {
		q = strings.ReplaceAll(q, "{{requests}}", unionSQL(requestsTable(s.cfg.ApdexMs, nil), reqSegs, "rr"))
	}
	if sysSegs != nil {
		q = strings.ReplaceAll(q, "{{system}}", unionSQL(systemTable(), sysSegs, "rs"))
	}
	q = strings.ReplaceAll(q, "{{instance}}", clause)
	res, err = s.execTemplate(ctx, q, tr, true)
	if err != nil {
		// A rollup problem must not take the dashboard down: answer raw.
		s.logger.Warn("rollup query failed; reading raw rows", "err", err)
		return nil, false, nil
	}
	p.finish(res)
	res.Source = sourceLabel(reqSegs, sysSegs)
	return res, true, nil
}

// runSlowest finds the slowest requests among raw rows, but only in the
// buckets that can hold them: a bucket whose slowest request is not among
// the 25 largest bucket maxima holds no top-25 request, since those 25
// buckets each have a slower one. The ragged edges are searched whole.
func (s *Server) runSlowest(ctx context.Context, sql string, tr timeRange, segs []segment) (*Result, bool, error) {
	var legs []string
	var ranges []segment
	for _, sg := range segs {
		if !sg.from.Before(sg.to) {
			continue
		}
		if sg.grain == 0 {
			ranges = append(ranges, sg)
			continue
		}
		legs = append(legs, "SELECT created_at, bucket_s, max_ms FROM rollup_requests WHERE bucket_s = "+
			strconv.FormatInt(sg.grain, 10)+" AND created_at >= '"+sg.from.Format(tsLayout)+"' AND created_at < '"+sg.to.Format(tsLayout)+"'")
	}
	top, err := s.db.Query(ctx, "SELECT created_at, bucket_s FROM (\n"+strings.Join(legs, "\nUNION ALL\n")+"\n) AS b ORDER BY max_ms DESC LIMIT 25")
	if err != nil {
		s.logger.Warn("rollup query failed; reading raw rows", "err", err)
		return nil, false, nil
	}
	for _, r := range top.Rows {
		start := parseTS(toString(r[0]))
		if start.IsZero() {
			continue
		}
		ranges = append(ranges, segment{0, start, start.Add(time.Duration(toInt(r[1])) * time.Second)})
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].from.Before(ranges[j].from) })
	var merged []segment
	for _, r := range ranges {
		if n := len(merged); n > 0 && !r.from.After(merged[n-1].to) {
			merged[n-1].to = maxTime(merged[n-1].to, r.to)
			continue
		}
		merged = append(merged, r)
	}
	var ors []string
	for _, r := range merged {
		ors = append(ors, "(created_at >= '"+r.from.Format(tsLayout)+"' AND created_at < '"+r.to.Format(tsLayout)+"')")
	}
	if len(ors) == 0 {
		ors = []string{"1 = 0"}
	}
	narrowed := strings.Replace(sql, "WHERE "+window, "WHERE "+window+" AND ("+strings.Join(ors, " OR ")+")", 1)
	res, err := s.execTemplate(ctx, narrowed, tr, true)
	if err != nil {
		return nil, true, err
	}
	if res.RowsRead >= 0 && top.RowsRead >= 0 {
		res.RowsRead += top.RowsRead
	}
	res.DurationMs += top.DurationMs
	res.Source = "raw_narrowed_by_" + sourceLabel(segs)
	return res, true, nil
}

// finish turns the query's columns into the panel's: percentiles from the
// histogram, latency bands from its buckets.
func (p *rollupPanel) finish(res *Result) {
	if p.out == nil {
		return
	}
	idx := map[string]int{}
	for i, c := range res.Columns {
		idx[c] = i
	}
	hist := make([]int, len(rollupEdges)+1)
	for i, c := range histCols() {
		j, ok := idx[c]
		if !ok {
			hist = nil
			break
		}
		hist[i] = j
	}
	counts := func(row []any) []float64 {
		h := make([]float64, len(hist))
		for i, j := range hist {
			h[i], _ = asFloat(row[j])
		}
		return h
	}

	rows := make([][]any, 0, len(res.Rows))
	for _, row := range res.Rows {
		if p.bands {
			rows = append(rows, bandRows(row, idx, counts(row))...)
			continue
		}
		out := make([]any, len(p.out))
		for i, name := range p.out {
			if q, ok := p.pct[name]; ok && hist != nil {
				lo, _ := asFloat(row[idx["lo_ms"]])
				hi, _ := asFloat(row[idx["hi_ms"]])
				out[i] = histQuantile(counts(row), lo, hi, q)
			} else if j, ok := idx[name]; ok {
				out[i] = row[j]
			}
		}
		rows = append(rows, out)
	}
	res.Columns, res.Rows = p.out, rows
}

// bandRows spreads one histogram row over the latencyBuckets bands, keeping
// only bands with requests, as the raw GROUP BY does.
func bandRows(row []any, idx map[string]int, h []float64) [][]any {
	sums := make([]int64, len(latencyBuckets)+1)
	for i, c := range h {
		band := len(latencyBuckets)
		if i < len(rollupEdges) {
			band = sort.SearchInts(latencyBuckets, int(rollupEdges[i]))
		}
		sums[band] += int64(c)
	}
	var out [][]any
	for band, n := range sums {
		if n == 0 {
			continue
		}
		if j, ok := idx["t"]; ok {
			out = append(out, []any{row[j], int64(band), n})
		} else {
			out = append(out, []any{int64(band), n})
		}
	}
	return out
}

// histQuantile estimates the q-quantile with the raw panels' definition (the
// smallest value ranked at or above q·n) from a histogram: it finds the
// bucket holding that rank and interpolates linearly inside it, with the
// bucket's bounds clamped to the group's exact min and max.
func histQuantile(h []float64, lo, hi, q float64) any {
	var n float64
	for _, c := range h {
		n += c
	}
	if n == 0 {
		return nil
	}
	k := math.Max(1, math.Ceil(q*n-1e-9))
	switch {
	case k <= 1:
		return lo
	case k >= n:
		return hi
	}
	var cum float64
	for i, c := range h {
		if c == 0 || cum+c < k {
			cum += c
			continue
		}
		low, high := lo, hi
		if i > 0 {
			low = math.Max(lo, rollupEdges[i-1])
		}
		if i < len(rollupEdges) {
			high = math.Min(hi, rollupEdges[i])
		}
		v := low + (high-low)*(k-cum-0.5)/c
		return math.Round(math.Min(math.Max(v, lo), hi)*100) / 100
	}
	return hi
}
