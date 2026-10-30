package analytics

import (
	"encoding/json"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	sdk "github.com/styropyr0/visionary/sdks/go"
)

// Rows for the metrics table from OTLP and remote_write. Cumulative counters
// and histograms become increases against the previous point of the same
// series; the first point of a series only sets the baseline.

const (
	// maxSeriesPerMetric matches the SDKs' default cap on label sets.
	maxSeriesPerMetric = 1000
	maxMetricSeries    = 200000
	maxMetricNames     = 10000
)

var metricsTable = &otlpTable{"metrics", []string{
	"created_at", "service", "instance", "name", "kind", "unit", "labels", "series", "value", "count", "sum", "min", "max", "buckets",
}}

// histPoint is a histogram reading: per-bucket counts, one more than bounds
// for values above the last bound. min and max are NaN when unknown.
type histPoint struct {
	count    float64
	sum      float64
	min, max float64
	bounds   []float64
	counts   []float64
}

// metricPoint is one reading before it becomes a row.
type metricPoint struct {
	at                time.Time
	service, instance string
	name, kind, unit  string
	labels            map[string]string
	value             float64
	hist              *histPoint
	// cumulative marks counter and histogram totals since a start.
	cumulative bool
	start      time.Time
}

type metricBaseline struct {
	at     time.Time
	start  time.Time
	value  float64
	hist   *histPoint
	usedAt time.Time
}

// metricIngest is the ingestion's memory: series seen per name, for the cap,
// and the last cumulative point per series.
type metricIngest struct {
	mu      sync.Mutex
	perName map[string]map[string]bool
	total   int
	dropped map[string]int64
	prev    map[string]*metricBaseline
	// promTypes are metric family types from remote_write metadata.
	promTypes map[string]string
}

var metricIngests sync.Map // *Server -> *metricIngest

func (s *Server) metricIngest() *metricIngest {
	v, ok := metricIngests.Load(s)
	if !ok {
		v, _ = metricIngests.LoadOrStore(s, &metricIngest{
			perName: map[string]map[string]bool{}, dropped: map[string]int64{},
			prev: map[string]*metricBaseline{}, promTypes: map[string]string{},
		})
	}
	return v.(*metricIngest)
}

// droppedSeries is how many new series each name lost to the cap since start.
func (mi *metricIngest) droppedSeries() map[string]int64 {
	mi.mu.Lock()
	defer mi.mu.Unlock()
	out := make(map[string]int64, len(mi.dropped))
	for k, v := range mi.dropped {
		out[k] = v
	}
	return out
}

// add turns p into a row in out, or nothing for a baseline, a dropped
// series or a value that is not a number.
func (mi *metricIngest) add(out *otlpRows, p metricPoint) bool {
	p.name = strings.TrimSpace(p.name)
	if p.name == "" || len(p.name) > 200 {
		return false
	}
	labels, series := sdk.MetricSeries(p.name, p.labels)
	mi.mu.Lock()
	defer mi.mu.Unlock()
	known := mi.perName[p.name]
	if !known[series] {
		if known == nil && len(mi.perName) >= maxMetricNames || len(known) >= maxSeriesPerMetric || mi.total >= maxMetricSeries {
			mi.dropped[p.name]++
			return false
		}
		if known == nil {
			known = map[string]bool{}
			mi.perName[p.name] = known
		}
		known[series] = true
		mi.total++
	}
	if p.cumulative {
		key := p.instance + "\n" + p.service + "\n" + p.kind + "\n" + series
		prev := mi.prev[key]
		mi.prev[key] = &metricBaseline{at: p.at, start: p.start, value: p.value, hist: p.hist, usedAt: time.Now()}
		if len(mi.prev) > maxMetricSeries {
			mi.trimBaselines()
		}
		if prev == nil || !p.at.After(prev.at) {
			return false
		}
		reset := !p.start.IsZero() && !prev.start.IsZero() && p.start.After(prev.start)
		if p.hist != nil {
			if !reset && p.hist.count < prev.hist.count {
				reset = true
			}
			if !reset {
				p.hist = histDelta(p.hist, prev.hist)
			}
		} else {
			if !reset && p.value < prev.value {
				reset = true
			}
			if !reset {
				p.value -= prev.value
			}
		}
	}
	if math.IsNaN(p.value) || math.IsInf(p.value, 0) {
		return false
	}
	row := []any{p.at.UTC().Format(tsLayout), otlpNullStr(p.service), otlpNullStr(p.instance), p.name, p.kind,
		otlpNullStr(p.unit), labels, series, p.value, nil, nil, nil, nil, nil}
	if h := p.hist; h != nil {
		if h.count <= 0 {
			return false
		}
		row[8] = h.sum / h.count
		row[9], row[10] = int64(math.Round(h.count)), h.sum
		if !math.IsNaN(h.min) {
			row[11] = h.min
		}
		if !math.IsNaN(h.max) {
			row[12] = h.max
		}
		row[13] = encodeHist(h.bounds, h.counts)
	} else if p.kind == "counter" && p.value <= 0 {
		// Nothing happened in the interval; the SDKs skip these too.
		return false
	}
	out.add(metricsTable, row)
	return true
}

// trimBaselines forgets the half of the baselines used longest ago. The
// caller holds mi.mu.
func (mi *metricIngest) trimBaselines() {
	times := make([]time.Time, 0, len(mi.prev))
	for _, b := range mi.prev {
		times = append(times, b.usedAt)
	}
	sort.Slice(times, func(i, j int) bool { return times[i].Before(times[j]) })
	cut := times[len(times)/2]
	for k, b := range mi.prev {
		if b.usedAt.Before(cut) {
			delete(mi.prev, k)
		}
	}
}

// histDelta is cur minus prev, bucket by bucket on matching upper bounds.
// min and max of an interval are unknown from totals.
func histDelta(cur, prev *histPoint) *histPoint {
	before := map[float64]float64{}
	for i, c := range prev.counts {
		before[bucketBound(prev.bounds, i)] += c
	}
	out := &histPoint{count: cur.count - prev.count, sum: cur.sum - prev.sum, min: math.NaN(), max: math.NaN(),
		bounds: cur.bounds, counts: make([]float64, len(cur.counts))}
	for i, c := range cur.counts {
		out.counts[i] = math.Max(0, c-before[bucketBound(cur.bounds, i)])
	}
	return out
}

// bucketBound is bucket i's upper bound, +Inf for the last.
func bucketBound(bounds []float64, i int) float64 {
	if i < len(bounds) {
		return bounds[i]
	}
	return math.Inf(1)
}

func encodeHist(bounds, counts []float64) string {
	ints := make([]int64, len(counts))
	for i, c := range counts {
		ints[i] = int64(math.Round(c))
	}
	if bounds == nil {
		bounds = []float64{}
	}
	b, _ := json.Marshal(struct {
		Bounds []float64 `json:"bounds"`
		Counts []int64   `json:"counts"`
	}{bounds, ints})
	return string(b)
}
