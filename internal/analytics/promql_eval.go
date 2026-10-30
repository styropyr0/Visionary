package analytics

import (
	"context"
	"errors"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Evaluating PromQL: every node is computed for all steps of the query at
// once. Selectors read series bucketed in SQL at a resolution that divides
// the step, the ranges and the offsets, so windows line up with buckets.
//
// Counters are stored as increases, so rate and increase sum them exactly
// (no extrapolation); a bare counter reads as its running total from the
// start of the data loaded for the query.

const (
	promLookback      = 5 * time.Minute
	maxPromSteps      = 11000
	maxPromBuckets    = 11000
	maxPromSeries     = 10000
	maxPromPoints     = 5_000_000
	promSampleSeconds = 15
)

type promLabels map[string]string

func (l promLabels) key() string {
	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte(0xfe)
		b.WriteString(l[k])
		b.WriteByte(0xff)
	}
	return b.String()
}

// String writes the labels as name{k="v", ...}.
func (l promLabels) String() string {
	keys := make([]string, 0, len(l))
	for k := range l {
		if k != "__name__" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + strconv.Quote(l[k])
	}
	if len(parts) == 0 {
		if n := l["__name__"]; n != "" {
			return n
		}
		return "{}"
	}
	return l["__name__"] + "{" + strings.Join(parts, ", ") + "}"
}

func (l promLabels) without(names ...string) promLabels {
	out := make(promLabels, len(l))
	for k, v := range l {
		if !slices.Contains(names, k) {
			out[k] = v
		}
	}
	return out
}

func (l promLabels) only(names ...string) promLabels {
	out := promLabels{}
	for _, k := range names {
		if v, ok := l[k]; ok && v != "" {
			out[k] = v
		}
	}
	return out
}

// rawSample is one bucket of a series: T is its end in ms. V is the mean
// for gauges and the increase for counters; Min and Max are the extremes of
// the readings and N how many there were.
type rawSample struct {
	T        int64
	V        float64
	Min, Max float64
	N        float64
}

type rawSeries struct {
	labels  promLabels
	counter bool
	samples []rawSample
	// cum is the running total of a counter's increases.
	cum []float64
}

func (rs *rawSeries) value(i int) float64 {
	if rs.counter {
		return rs.cum[i]
	}
	return rs.samples[i].V
}

// promWindow is what a selector reads: buckets of res seconds offset by
// phase, ending in (from, to], both in ms.
type promWindow struct {
	from, to   int64
	res, phase int64
}

type promSource interface {
	load(ctx context.Context, vs *vectorSel, w promWindow) ([]*rawSeries, error)
}

var errPromTooLarge = errors.New("the query reads too many points; narrow the time range, raise the step or add label matchers")

type vecSeries struct {
	labels promLabels
	v      []float64
	ok     []bool
}

type promVal struct {
	isScalar bool
	scalar   []float64
	vec      []*vecSeries
	str      *string
	// sort is 1 or -1 for sort and sort_desc.
	sort int
	// matrix is a bare range selector read at a single instant.
	matrix []*rawSeries
}

type promEval struct {
	ctx              context.Context
	src              promSource
	start, end, step int64
	n                int
	res, phase       int64
	lookback         int64
	points           int
}

// newPromEval plans an evaluation from start to end (ms) every step ms; an
// instant query has start == end and step 0.
func newPromEval(ctx context.Context, src promSource, expr promExpr, start, end, step int64) *promEval {
	e := &promEval{ctx: ctx, src: src, start: start, end: end, step: step, n: 1}
	if step > 0 {
		e.n = int((end-start)/step) + 1
	}
	base := int64(promLookback / time.Second)
	base = gcd(base, promSampleSeconds)
	if step > 0 {
		base = gcd(base, max(step/1000, 1))
	}
	var maxBack int64
	walkProm(expr, func(x promExpr) {
		var off, rng time.Duration
		switch s := x.(type) {
		case *vectorSel:
			off = s.offset
		case *matrixSel:
			rng = s.rng
		default:
			return
		}
		for _, d := range []time.Duration{off, rng} {
			if d < 0 {
				d = -d
			}
			if d > 0 {
				base = gcd(base, max(int64(d/time.Second), 1))
			}
		}
		maxBack = max(maxBack, int64(off/time.Millisecond)+int64(rng/time.Millisecond))
	})
	span := (end-start+maxBack)/1000 + int64(promLookback/time.Second)
	res := base
	if span/res > maxPromBuckets {
		// Coarser buckets, preferably ones that still divide the step.
		need := (span + maxPromBuckets - 1) / maxPromBuckets
		res = ((need + base - 1) / base) * base
		if stepS := step / 1000; stepS > 0 && stepS%base == 0 {
			res = ((need + stepS - 1) / stepS) * stepS
			for d := base; d <= stepS; d += base {
				if stepS%d == 0 && d >= need {
					res = d
					break
				}
			}
		}
	}
	e.res = res
	e.phase = floorDiv(start, 1000) % res
	if e.phase < 0 {
		e.phase += res
	}
	e.lookback = max(int64(promLookback/time.Millisecond), res*1000)
	return e
}

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	if a < 0 {
		return -a
	}
	return a
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

func walkProm(e promExpr, f func(promExpr)) {
	f(e)
	switch x := e.(type) {
	case *matrixSel:
		f(x.vs)
	case *callExpr:
		for _, a := range x.args {
			walkProm(a, f)
		}
	case *aggExpr:
		if x.param != nil {
			walkProm(x.param, f)
		}
		walkProm(x.expr, f)
	case *binaryExpr:
		walkProm(x.lhs, f)
		walkProm(x.rhs, f)
	case *unaryExpr:
		walkProm(x.expr, f)
	case *parenExpr:
		walkProm(x.expr, f)
	}
}

func (e *promEval) at(i int) int64 { return e.start + int64(i)*e.step }

func (e *promEval) newSeries(l promLabels) (*vecSeries, error) {
	e.points += e.n
	if e.points > maxPromPoints {
		return nil, errPromTooLarge
	}
	return &vecSeries{labels: l, v: make([]float64, e.n), ok: make([]bool, e.n)}, nil
}

func (e *promEval) constant(v float64) promVal {
	s := make([]float64, e.n)
	for i := range s {
		s[i] = v
	}
	return promVal{isScalar: true, scalar: s}
}

// read loads a selector's series over the window the query needs, rng
// before the first step.
func (e *promEval) read(vs *vectorSel, rng time.Duration) ([]*rawSeries, error) {
	if err := e.ctx.Err(); err != nil {
		return nil, err
	}
	off := int64(vs.offset / time.Millisecond)
	back := int64(rng / time.Millisecond)
	if rng == 0 {
		back = e.lookback
	}
	res := e.res * 1000
	ph := e.phase * 1000
	from := floorDiv(e.start-off-back-ph, res)*res + ph
	to := floorDiv(e.end-off-ph+res-1, res)*res + ph
	series, err := e.src.load(e.ctx, vs, promWindow{from: from, to: to, res: e.res, phase: e.phase})
	if err != nil {
		return nil, err
	}
	if len(series) > maxPromSeries {
		return nil, errPromTooLarge
	}
	for _, rs := range series {
		sort.Slice(rs.samples, func(i, j int) bool { return rs.samples[i].T < rs.samples[j].T })
		if rs.counter {
			rs.cum = make([]float64, len(rs.samples))
			total := 0.0
			for i, s := range rs.samples {
				total += s.V
				rs.cum[i] = total
			}
		}
	}
	return series, nil
}

func (e *promEval) eval(x promExpr) (promVal, error) {
	if err := e.ctx.Err(); err != nil {
		return promVal{}, err
	}
	switch x := x.(type) {
	case *numberLit:
		return e.constant(x.v), nil
	case *stringLit:
		return promVal{str: &x.v}, nil
	case *parenExpr:
		return e.eval(x.expr)
	case *unaryExpr:
		v, err := e.eval(x.expr)
		if err != nil {
			return v, err
		}
		if v.isScalar {
			for i := range v.scalar {
				v.scalar[i] = -v.scalar[i]
			}
			return v, nil
		}
		for _, s := range v.vec {
			delete(s.labels, "__name__")
			for i := range s.v {
				s.v[i] = -s.v[i]
			}
		}
		return v, nil
	case *vectorSel:
		return e.instant(x)
	case *matrixSel:
		if e.step != 0 {
			return promVal{}, promErrorf(x.pos, "a range vector can only be shown for an instant query; wrap it in a function like rate()")
		}
		series, err := e.read(x.vs, x.rng)
		if err != nil {
			return promVal{}, err
		}
		t := e.start - int64(x.vs.offset/time.Millisecond)
		lo := t - int64(x.rng/time.Millisecond)
		for _, rs := range series {
			i0 := sort.Search(len(rs.samples), func(i int) bool { return rs.samples[i].T > lo })
			i1 := sort.Search(len(rs.samples), func(i int) bool { return rs.samples[i].T > t })
			rs.samples = rs.samples[i0:i1]
			if rs.counter {
				rs.cum = rs.cum[i0:i1]
			}
		}
		return promVal{matrix: slices.DeleteFunc(series, func(rs *rawSeries) bool { return len(rs.samples) == 0 })}, nil
	case *callExpr:
		return e.call(x)
	case *aggExpr:
		return e.aggregate(x)
	case *binaryExpr:
		return e.binary(x)
	}
	return promVal{}, promErrorf(x.position(), "unsupported expression")
}

// instant reads the latest sample at or before each step, within the lookback.
func (e *promEval) instant(vs *vectorSel) (promVal, error) {
	series, err := e.read(vs, 0)
	if err != nil {
		return promVal{}, err
	}
	off := int64(vs.offset / time.Millisecond)
	var out []*vecSeries
	for _, rs := range series {
		vec, err := e.newSeries(rs.labels)
		if err != nil {
			return promVal{}, err
		}
		j, any := 0, false
		for i := 0; i < e.n; i++ {
			t := e.at(i) - off
			for j < len(rs.samples) && rs.samples[j].T <= t {
				j++
			}
			if j > 0 && rs.samples[j-1].T > t-e.lookback {
				vec.v[i], vec.ok[i], any = rs.value(j-1), true, true
			}
		}
		if any {
			out = append(out, vec)
		}
	}
	return promVal{vec: out}, nil
}

// window calls f with each step's samples in (t-rng, t].
func (e *promEval) window(m *matrixSel, f func(rs *rawSeries, lo, hi int, t int64) (float64, bool), keepName bool) (promVal, error) {
	series, err := e.read(m.vs, m.rng)
	if err != nil {
		return promVal{}, err
	}
	off := int64(m.vs.offset / time.Millisecond)
	rng := int64(m.rng / time.Millisecond)
	var out []*vecSeries
	for _, rs := range series {
		l := rs.labels
		if !keepName {
			l = l.without("__name__")
		}
		vec, err := e.newSeries(l)
		if err != nil {
			return promVal{}, err
		}
		lo, hi, any := 0, 0, false
		for i := 0; i < e.n; i++ {
			t := e.at(i) - off
			for lo < len(rs.samples) && rs.samples[lo].T <= t-rng {
				lo++
			}
			for hi < len(rs.samples) && rs.samples[hi].T <= t {
				hi++
			}
			if hi <= lo {
				continue
			}
			if v, ok := f(rs, lo, hi, t); ok {
				vec.v[i], vec.ok[i], any = v, true, true
			}
		}
		if any {
			out = append(out, vec)
		}
	}
	return promVal{vec: out}, nil
}

// increaseOf is a series' increase over samples [lo, hi): exact for
// counters, with resets for anything else.
func increaseOf(rs *rawSeries, lo, hi int) (float64, bool) {
	if rs.counter {
		sum := 0.0
		for _, s := range rs.samples[lo:hi] {
			sum += s.V
		}
		return sum, true
	}
	if hi-lo < 2 {
		return 0, false
	}
	inc := 0.0
	for i := lo + 1; i < hi; i++ {
		d := rs.samples[i].V - rs.samples[i-1].V
		if d < 0 {
			d = rs.samples[i].V
		}
		inc += d
	}
	return inc, true
}

func (e *promEval) call(c *callExpr) (promVal, error) {
	if len(c.args) > 0 {
		if m, ok := c.args[0].(*matrixSel); ok {
			return e.rangeFunc(c.fn, m)
		}
	}
	switch c.fn {
	case "time":
		v := make([]float64, e.n)
		for i := range v {
			v[i] = float64(e.at(i)) / 1000
		}
		return promVal{isScalar: true, scalar: v}, nil
	case "vector":
		s, err := e.eval(c.args[0])
		if err != nil {
			return s, err
		}
		vec, err := e.newSeries(promLabels{})
		if err != nil {
			return promVal{}, err
		}
		copy(vec.v, s.scalar)
		for i := range vec.ok {
			vec.ok[i] = true
		}
		return promVal{vec: []*vecSeries{vec}}, nil
	case "histogram_quantile":
		q, err := e.eval(c.args[0])
		if err != nil {
			return q, err
		}
		v, err := e.eval(c.args[1])
		if err != nil {
			return v, err
		}
		return e.histogramQuantile(q.scalar, v.vec)
	}
	v, err := e.eval(c.args[0])
	if err != nil {
		return v, err
	}
	switch c.fn {
	case "scalar":
		out := make([]float64, e.n)
		for i := range out {
			out[i] = math.NaN()
			n := 0
			for _, s := range v.vec {
				if s.ok[i] {
					out[i] = s.v[i]
					n++
				}
			}
			if n != 1 {
				out[i] = math.NaN()
			}
		}
		return promVal{isScalar: true, scalar: out}, nil
	case "absent":
		l := promLabels{}
		if vs, ok := unwrapParen(c.args[0]).(*vectorSel); ok {
			for _, m := range vs.matchers {
				if m.op == "=" && m.name != "__name__" {
					l[m.name] = m.value
				}
			}
		}
		vec, err := e.newSeries(l)
		if err != nil {
			return promVal{}, err
		}
		any := false
		for i := 0; i < e.n; i++ {
			present := slices.ContainsFunc(v.vec, func(s *vecSeries) bool { return s.ok[i] })
			if !present {
				vec.v[i], vec.ok[i], any = 1, true, true
			}
		}
		if !any {
			return promVal{}, nil
		}
		return promVal{vec: []*vecSeries{vec}}, nil
	case "sort", "sort_desc":
		v.sort = 1
		if c.fn == "sort_desc" {
			v.sort = -1
		}
		return v, nil
	}
	var args [][]float64
	for _, a := range c.args[1:] {
		s, err := e.eval(a)
		if err != nil {
			return s, err
		}
		args = append(args, s.scalar)
	}
	arg := func(k, i int, def float64) float64 {
		if k < len(args) {
			return args[k][i]
		}
		return def
	}
	for _, s := range v.vec {
		delete(s.labels, "__name__")
		for i := range s.v {
			x := s.v[i]
			switch c.fn {
			case "abs":
				x = math.Abs(x)
			case "ceil":
				x = math.Ceil(x)
			case "floor":
				x = math.Floor(x)
			case "sqrt":
				x = math.Sqrt(x)
			case "exp":
				x = math.Exp(x)
			case "ln":
				x = math.Log(x)
			case "log2":
				x = math.Log2(x)
			case "log10":
				x = math.Log10(x)
			case "round":
				to := arg(0, i, 1)
				x = math.Floor(x/to+0.5) * to
			case "clamp_min":
				x = math.Max(x, arg(0, i, 0))
			case "clamp_max":
				x = math.Min(x, arg(0, i, 0))
			case "clamp":
				lo, hi := arg(0, i, 0), arg(1, i, 0)
				if lo > hi {
					s.ok[i] = false
				}
				x = math.Max(lo, math.Min(hi, x))
			}
			s.v[i] = x
		}
	}
	return promVal{vec: dropEmpty(v.vec)}, nil
}

func unwrapParen(e promExpr) promExpr {
	for {
		p, ok := e.(*parenExpr)
		if !ok {
			return e
		}
		e = p.expr
	}
}

func dropEmpty(vec []*vecSeries) []*vecSeries {
	return slices.DeleteFunc(vec, func(s *vecSeries) bool { return !slices.Contains(s.ok, true) })
}

func (e *promEval) rangeFunc(fn string, m *matrixSel) (promVal, error) {
	secs := m.rng.Seconds()
	res := float64(e.res)
	switch fn {
	case "rate", "increase":
		return e.window(m, func(rs *rawSeries, lo, hi int, _ int64) (float64, bool) {
			inc, ok := increaseOf(rs, lo, hi)
			if fn == "rate" {
				inc /= secs
			}
			return inc, ok
		}, false)
	case "irate", "idelta":
		return e.window(m, func(rs *rawSeries, lo, hi int, _ int64) (float64, bool) {
			last := rs.samples[hi-1]
			if rs.counter {
				// A bucket's value is the increase over its own res seconds.
				if fn == "irate" {
					return last.V / res, true
				}
				return last.V, true
			}
			if hi-lo < 2 {
				return 0, false
			}
			prev := rs.samples[hi-2]
			d := last.V - prev.V
			if fn == "idelta" {
				return d, true
			}
			if d < 0 {
				d = last.V
			}
			return d / (float64(last.T-prev.T) / 1000), true
		}, false)
	case "delta":
		return e.window(m, func(rs *rawSeries, lo, hi int, _ int64) (float64, bool) {
			if rs.counter {
				inc, _ := increaseOf(rs, lo, hi)
				return inc, true
			}
			if hi-lo < 2 {
				return 0, false
			}
			return rs.samples[hi-1].V - rs.samples[lo].V, true
		}, false)
	case "resets", "changes":
		return e.window(m, func(rs *rawSeries, lo, hi int, _ int64) (float64, bool) {
			n := 0.0
			for i := lo + 1; i < hi; i++ {
				a, b := rs.value(i-1), rs.value(i)
				if fn == "resets" && b < a || fn == "changes" && b != a {
					n++
				}
			}
			return n, true
		}, false)
	case "avg_over_time", "sum_over_time":
		return e.window(m, func(rs *rawSeries, lo, hi int, _ int64) (float64, bool) {
			var sum, n float64
			for i := lo; i < hi; i++ {
				if rs.counter {
					sum += rs.cum[i]
					n++
					continue
				}
				w := math.Max(rs.samples[i].N, 1)
				sum += rs.samples[i].V * w
				n += w
			}
			if fn == "sum_over_time" {
				return sum, true
			}
			return sum / n, n > 0
		}, false)
	case "min_over_time", "max_over_time":
		return e.window(m, func(rs *rawSeries, lo, hi int, _ int64) (float64, bool) {
			best := math.NaN()
			for i := lo; i < hi; i++ {
				v := rs.value(i)
				if !rs.counter {
					v = rs.samples[i].Min
					if fn == "max_over_time" {
						v = rs.samples[i].Max
					}
				}
				if math.IsNaN(best) || fn == "min_over_time" && v < best || fn == "max_over_time" && v > best {
					best = v
				}
			}
			return best, true
		}, false)
	case "count_over_time":
		return e.window(m, func(rs *rawSeries, lo, hi int, _ int64) (float64, bool) {
			n := 0.0
			for i := lo; i < hi; i++ {
				n += math.Max(rs.samples[i].N, 1)
			}
			return n, true
		}, false)
	case "last_over_time":
		return e.window(m, func(rs *rawSeries, lo, hi int, _ int64) (float64, bool) { return rs.value(hi - 1), true }, true)
	case "present_over_time":
		return e.window(m, func(*rawSeries, int, int, int64) (float64, bool) { return 1, true }, false)
	case "absent_over_time":
		present, err := e.window(m, func(*rawSeries, int, int, int64) (float64, bool) { return 1, true }, false)
		if err != nil {
			return present, err
		}
		l := promLabels{}
		for _, mt := range m.vs.matchers {
			if mt.op == "=" && mt.name != "__name__" {
				l[mt.name] = mt.value
			}
		}
		vec, err := e.newSeries(l)
		if err != nil {
			return promVal{}, err
		}
		for i := 0; i < e.n; i++ {
			if !slices.ContainsFunc(present.vec, func(s *vecSeries) bool { return s.ok[i] }) {
				vec.v[i], vec.ok[i] = 1, true
			}
		}
		return promVal{vec: dropEmpty([]*vecSeries{vec})}, nil
	}
	return promVal{}, promErrorf(m.pos, "%s does not take a range vector", fn)
}

// histogramQuantile reads Prometheus-style cumulative buckets: series that
// differ only in le.
func (e *promEval) histogramQuantile(q []float64, vec []*vecSeries) (promVal, error) {
	type bucket struct {
		le float64
		s  *vecSeries
	}
	groups := map[string][]bucket{}
	labelsOf := map[string]promLabels{}
	var order []string
	for _, s := range vec {
		le, err := strconv.ParseFloat(s.labels["le"], 64)
		if err != nil {
			continue
		}
		l := s.labels.without("le", "__name__")
		k := l.key()
		if _, ok := groups[k]; !ok {
			order = append(order, k)
			labelsOf[k] = l
		}
		groups[k] = append(groups[k], bucket{le, s})
	}
	var out []*vecSeries
	for _, k := range order {
		bs := groups[k]
		sort.Slice(bs, func(i, j int) bool { return bs[i].le < bs[j].le })
		res, err := e.newSeries(labelsOf[k])
		if err != nil {
			return promVal{}, err
		}
		any := false
		for i := 0; i < e.n; i++ {
			counts := map[float64]float64{}
			prevCum := 0.0
			hasInf := false
			for _, b := range bs {
				if !b.s.ok[i] {
					continue
				}
				cum := math.Max(b.s.v[i], prevCum)
				counts[b.le] = cum - prevCum
				prevCum = cum
				hasInf = hasInf || math.IsInf(b.le, 1)
			}
			if !hasInf {
				continue
			}
			var v float64
			switch phi := q[i]; {
			case phi < 0:
				v = math.Inf(-1)
			case phi > 1:
				v = math.Inf(1)
			default:
				var ok bool
				if v, ok = bucketQuantile(counts, phi); !ok {
					v = math.NaN()
				}
			}
			res.v[i], res.ok[i], any = v, true, true
		}
		if any {
			out = append(out, res)
		}
	}
	return promVal{vec: out}, nil
}

func (e *promEval) aggregate(a *aggExpr) (promVal, error) {
	v, err := e.eval(a.expr)
	if err != nil {
		return v, err
	}
	var param []float64
	if a.param != nil {
		p, err := e.eval(a.param)
		if err != nil {
			return p, err
		}
		param = p.scalar
	}
	groupOf := func(l promLabels) promLabels {
		if a.without {
			return l.without(append([]string{"__name__"}, a.grouping...)...)
		}
		return l.only(a.grouping...)
	}
	type group struct {
		labels  promLabels
		members []*vecSeries
	}
	groups := map[string]*group{}
	var order []string
	for _, s := range v.vec {
		gl := groupOf(s.labels)
		k := gl.key()
		g := groups[k]
		if g == nil {
			g = &group{labels: gl}
			groups[k] = g
			order = append(order, k)
		}
		g.members = append(g.members, s)
	}
	var out []*vecSeries
	if a.op == "topk" || a.op == "bottomk" {
		keep := map[*vecSeries][]bool{}
		for _, k := range order {
			g := groups[k]
			for i := 0; i < e.n; i++ {
				n := int(param[i])
				var cand []*vecSeries
				for _, s := range g.members {
					if s.ok[i] {
						cand = append(cand, s)
					}
				}
				sort.SliceStable(cand, func(x, y int) bool {
					if a.op == "topk" {
						return cand[x].v[i] > cand[y].v[i] || math.IsNaN(cand[y].v[i]) && !math.IsNaN(cand[x].v[i])
					}
					return cand[x].v[i] < cand[y].v[i] || math.IsNaN(cand[y].v[i]) && !math.IsNaN(cand[x].v[i])
				})
				for _, s := range head(cand, max(n, 0)) {
					if keep[s] == nil {
						keep[s] = make([]bool, e.n)
					}
					keep[s][i] = true
				}
			}
		}
		for _, s := range v.vec {
			if kk := keep[s]; kk != nil {
				for i := range s.ok {
					s.ok[i] = s.ok[i] && kk[i]
				}
				out = append(out, s)
			}
		}
		return promVal{vec: out}, nil
	}
	for _, k := range order {
		g := groups[k]
		res, err := e.newSeries(g.labels)
		if err != nil {
			return promVal{}, err
		}
		any := false
		vals := make([]float64, 0, len(g.members))
		for i := 0; i < e.n; i++ {
			vals = vals[:0]
			for _, s := range g.members {
				if s.ok[i] {
					vals = append(vals, s.v[i])
				}
			}
			if len(vals) == 0 {
				continue
			}
			var x float64
			switch a.op {
			case "sum":
				for _, y := range vals {
					x += y
				}
			case "avg":
				for _, y := range vals {
					x += y
				}
				x /= float64(len(vals))
			case "min", "max":
				x = vals[0]
				for _, y := range vals[1:] {
					if a.op == "min" && y < x || a.op == "max" && y > x || math.IsNaN(x) {
						x = y
					}
				}
			case "count":
				x = float64(len(vals))
			case "group":
				x = 1
			case "stddev", "stdvar":
				mean := 0.0
				for _, y := range vals {
					mean += y
				}
				mean /= float64(len(vals))
				for _, y := range vals {
					x += (y - mean) * (y - mean)
				}
				x /= float64(len(vals))
				if a.op == "stddev" {
					x = math.Sqrt(x)
				}
			case "quantile":
				x = quantileOf(param[i], vals)
			}
			res.v[i], res.ok[i], any = x, true, true
		}
		if any {
			out = append(out, res)
		}
	}
	return promVal{vec: out}, nil
}

// quantileOf interpolates between the closest ranks, as Prometheus's quantile does.
func quantileOf(q float64, vals []float64) float64 {
	if len(vals) == 0 || math.IsNaN(q) {
		return math.NaN()
	}
	if q < 0 {
		return math.Inf(-1)
	}
	if q > 1 {
		return math.Inf(1)
	}
	s := slices.Clone(vals)
	sort.Float64s(s)
	rank := q * float64(len(s)-1)
	lo := math.Floor(rank)
	hi := math.Min(lo+1, float64(len(s)-1))
	w := rank - lo
	return s[int(lo)]*(1-w) + s[int(hi)]*w
}

func arith(op string, a, b float64) (float64, bool) {
	switch op {
	case "+":
		return a + b, true
	case "-":
		return a - b, true
	case "*":
		return a * b, true
	case "/":
		return a / b, true
	case "%":
		return math.Mod(a, b), true
	case "^":
		return math.Pow(a, b), true
	case "atan2":
		return math.Atan2(a, b), true
	case "==":
		return a, a == b
	case "!=":
		return a, a != b
	case ">":
		return a, a > b
	case "<":
		return a, a < b
	case ">=":
		return a, a >= b
	case "<=":
		return a, a <= b
	}
	return 0, false
}

// binOp applies op to one pair: the value and whether the element is kept.
func binOp(op string, returnBool bool, a, b float64) (float64, bool) {
	v, keep := arith(op, a, b)
	if !isComparison(op) {
		return v, true
	}
	if returnBool {
		if keep {
			return 1, true
		}
		return 0, true
	}
	return v, keep
}

func (e *promEval) binary(b *binaryExpr) (promVal, error) {
	l, err := e.eval(b.lhs)
	if err != nil {
		return l, err
	}
	r, err := e.eval(b.rhs)
	if err != nil {
		return r, err
	}
	dropName := !isComparison(b.op) || b.returnBool
	switch {
	case l.isScalar && r.isScalar:
		out := make([]float64, e.n)
		for i := range out {
			out[i], _ = binOp(b.op, b.returnBool, l.scalar[i], r.scalar[i])
		}
		return promVal{isScalar: true, scalar: out}, nil
	case l.isScalar || r.isScalar:
		vec, sc, scalarLeft := r.vec, l.scalar, true
		if r.isScalar {
			vec, sc, scalarLeft = l.vec, r.scalar, false
		}
		for _, s := range vec {
			if dropName {
				delete(s.labels, "__name__")
			}
			for i := range s.v {
				if !s.ok[i] {
					continue
				}
				x, y := s.v[i], sc[i]
				if scalarLeft {
					x, y = y, x
				}
				v, keep := binOp(b.op, b.returnBool, x, y)
				if isComparison(b.op) && !b.returnBool {
					v = s.v[i]
				}
				s.v[i], s.ok[i] = v, keep
			}
		}
		return promVal{vec: dropEmpty(vec)}, nil
	}
	m := b.matching
	sig := func(lb promLabels) string {
		if m.on {
			return lb.only(m.labels...).key()
		}
		return lb.without(append([]string{"__name__"}, m.labels...)...).key()
	}
	switch b.op {
	case "and", "unless", "or":
		return e.setOp(b.op, l.vec, r.vec, sig)
	}
	many, one := l.vec, r.vec
	swapped := m.card == "one-to-many"
	if swapped {
		many, one = r.vec, l.vec
	}
	oneSigs := make([]string, len(one))
	for i, s := range one {
		oneSigs[i] = sig(s.labels)
	}
	manySigs := make([]string, len(many))
	for i, s := range many {
		manySigs[i] = sig(s.labels)
	}
	results := map[string]*vecSeries{}
	var order []string
	for i := 0; i < e.n; i++ {
		bySig := map[string]*vecSeries{}
		for j, s := range one {
			if !s.ok[i] {
				continue
			}
			if bySig[oneSigs[j]] != nil {
				return promVal{}, promErrorf(b.pos, "many-to-many matching not allowed: matching labels must be unique on one side")
			}
			bySig[oneSigs[j]] = s
		}
		seen := map[string]bool{}
		used := map[string]bool{}
		for j, s := range many {
			if !s.ok[i] {
				continue
			}
			o := bySig[manySigs[j]]
			if o == nil {
				continue
			}
			if m.card == "one-to-one" {
				if seen[manySigs[j]] {
					return promVal{}, promErrorf(b.pos, "found duplicate series for the match group on the left side; use on(), ignoring() or group_left")
				}
				seen[manySigs[j]] = true
			}
			lv, rv := s.v[i], o.v[i]
			if swapped {
				lv, rv = rv, lv
			}
			v, keep := binOp(b.op, b.returnBool, lv, rv)
			if !keep {
				continue
			}
			if isComparison(b.op) && !b.returnBool {
				v = lv
			}
			lb := resultLabels(s.labels, o.labels, m, dropName)
			k := lb.key()
			if used[k] {
				return promVal{}, promErrorf(b.pos, "multiple matches for labels: grouping labels must ensure unique matches")
			}
			used[k] = true
			res := results[k]
			if res == nil {
				res, err = e.newSeries(lb)
				if err != nil {
					return promVal{}, err
				}
				results[k] = res
				order = append(order, k)
			}
			res.v[i], res.ok[i] = v, true
		}
	}
	out := make([]*vecSeries, 0, len(order))
	for _, k := range order {
		out = append(out, results[k])
	}
	return promVal{vec: out}, nil
}

func resultLabels(many, one promLabels, m vectorMatching, dropName bool) promLabels {
	out := many.without()
	if dropName {
		delete(out, "__name__")
	}
	if m.card == "one-to-one" {
		if m.on {
			out = out.only(m.labels...)
		} else {
			out = out.without(m.labels...)
		}
	}
	for _, k := range m.include {
		if v := one[k]; v != "" {
			out[k] = v
		} else {
			delete(out, k)
		}
	}
	return out
}

func (e *promEval) setOp(op string, l, r []*vecSeries, sig func(promLabels) string) (promVal, error) {
	rs := make([]string, len(r))
	for i, s := range r {
		rs[i] = sig(s.labels)
	}
	ls := make([]string, len(l))
	for i, s := range l {
		ls[i] = sig(s.labels)
	}
	present := func(vec []*vecSeries, sigs []string, i int) map[string]bool {
		out := map[string]bool{}
		for j, s := range vec {
			if s.ok[i] {
				out[sigs[j]] = true
			}
		}
		return out
	}
	for i := 0; i < e.n; i++ {
		if op == "or" {
			left := present(l, ls, i)
			for j, s := range r {
				if s.ok[i] && left[rs[j]] {
					s.ok[i] = false
				}
			}
			continue
		}
		right := present(r, rs, i)
		for j, s := range l {
			if s.ok[i] && right[ls[j]] == (op == "unless") {
				s.ok[i] = false
			}
		}
	}
	if op == "or" {
		return promVal{vec: dropEmpty(append(l, r...))}, nil
	}
	return promVal{vec: dropEmpty(l)}, nil
}
