package visionary

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"math"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// Custom metrics: counters, gauges and histograms the application defines.
// Values add up in memory and are written as one row per series every interval
// (15 s by default); recording never waits for the database. Past MaxSeries
// (1000) series per metric name new ones are dropped and counted, so a label
// holding user ids cannot fill the database.

const (
	maxMetricName    = 200
	maxLabels        = 20
	maxLabelKey      = 100
	maxLabelValue    = 200
	defaultMaxSeries = 1000
)

// DefaultBuckets are the histogram bucket upper bounds unless DescribeMetric
// sets others (OpenTelemetry's defaults, which suit milliseconds).
var DefaultBuckets = []float64{0, 5, 10, 25, 50, 75, 100, 250, 500, 750, 1000, 2500, 5000, 7500, 10000}

// MetricOptions describe a metric name: its unit ("ms", "bytes", "requests")
// and, for histograms, the bucket upper bounds in increasing order.
type MetricOptions struct {
	Unit    string
	Buckets []float64
}

// DescribeMetric sets a metric's unit and histogram buckets. Call it before
// the metric's first use; series created earlier keep their buckets.
func DescribeMetric(name string, o MetricOptions) {
	name = cleanMetricName(name)
	if name == "" {
		return
	}
	if len(o.Buckets) > 0 {
		b := slices.Clone(o.Buckets)
		sort.Float64s(b)
		o.Buckets = slices.Compact(b)
	}
	reg.mu.Lock()
	reg.opts[name] = o
	reg.mu.Unlock()
}

// CounterSeries is one counter series. Its zero value and nil are no-ops.
type CounterSeries struct{ s *metricSeries }

// GaugeSeries is one gauge series. Its zero value and nil are no-ops.
type GaugeSeries struct{ s *metricSeries }

// HistogramSeries is one histogram series. Its zero value and nil are no-ops.
type HistogramSeries struct{ s *metricSeries }

// Counter returns the counter name with labels as key/value pairs. Rows hold
// the increase over each interval.
func Counter(name string, labels ...string) *CounterSeries {
	return &CounterSeries{reg.get(name, kindCounter, labels)}
}

// Gauge returns the gauge name with labels as key/value pairs. Rows hold the
// last value set, every interval once it has been set.
func Gauge(name string, labels ...string) *GaugeSeries {
	return &GaugeSeries{reg.get(name, kindGauge, labels)}
}

// Histogram returns the histogram name with labels as key/value pairs. Rows
// hold each interval's count, sum, min, max and bucket counts.
func Histogram(name string, labels ...string) *HistogramSeries {
	return &HistogramSeries{reg.get(name, kindHistogram, labels)}
}

// Add adds n (ignored unless positive and finite).
func (c *CounterSeries) Add(n float64) {
	if c == nil || c.s == nil || !(n > 0) || math.IsInf(n, 0) {
		return
	}
	c.s.mu.Lock()
	c.s.total += n
	c.s.touched = true
	c.s.mu.Unlock()
}

// Inc adds 1.
func (c *CounterSeries) Inc() { c.Add(1) }

// Set sets the gauge (ignored when v is NaN or infinite).
func (g *GaugeSeries) Set(v float64) {
	if g == nil || g.s == nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return
	}
	g.s.mu.Lock()
	g.s.value, g.s.touched = v, true
	g.s.mu.Unlock()
}

// Add moves the gauge by delta, from 0 when it was never set.
func (g *GaugeSeries) Add(delta float64) {
	if g == nil || g.s == nil || math.IsNaN(delta) || math.IsInf(delta, 0) {
		return
	}
	g.s.mu.Lock()
	g.s.value += delta
	g.s.touched = true
	g.s.mu.Unlock()
}

// Observe records one value (ignored when NaN or infinite).
func (h *HistogramSeries) Observe(v float64) {
	if h == nil || h.s == nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return
	}
	s := h.s
	i := sort.SearchFloat64s(s.bounds, v)
	s.mu.Lock()
	s.counts[i]++
	if s.count == 0 || v < s.min {
		s.min = v
	}
	if s.count == 0 || v > s.max {
		s.max = v
	}
	s.count++
	s.sum += v
	s.mu.Unlock()
}

const (
	kindCounter   = "counter"
	kindGauge     = "gauge"
	kindHistogram = "histogram"
)

type metricSeries struct {
	name, kind, unit, labels, id string
	bounds                       []float64

	mu      sync.Mutex
	touched bool
	total   float64
	value   float64
	counts  []uint64
	count   uint64
	sum     float64
	min     float64
	max     float64
}

type registry struct {
	mu        sync.Mutex
	series    map[string]*metricSeries
	perName   map[string]int
	dropped   map[string]int64
	opts      map[string]MetricOptions
	maxSeries atomic.Int64
}

var reg = newRegistry()

func newRegistry() *registry {
	r := &registry{series: map[string]*metricSeries{}, perName: map[string]int{}, dropped: map[string]int64{}, opts: map[string]MetricOptions{}}
	r.maxSeries.Store(defaultMaxSeries)
	return r
}

func (r *registry) get(name, kind string, kv []string) *metricSeries {
	name = cleanMetricName(name)
	if name == "" {
		return nil
	}
	labels := pairs(kv)
	key := seriesKey(name, kind, labels)
	r.mu.Lock()
	defer r.mu.Unlock()
	if s := r.series[key]; s != nil {
		return s
	}
	if int64(r.perName[name]) >= r.maxSeries.Load() {
		r.dropped[name]++
		return nil
	}
	text, id := MetricSeries(name, labels)
	s := &metricSeries{name: name, kind: kind, labels: text, id: id, unit: r.opts[name].Unit}
	if kind == kindHistogram {
		s.bounds = r.opts[name].Buckets
		if len(s.bounds) == 0 {
			s.bounds = DefaultBuckets
		}
		s.counts = make([]uint64, len(s.bounds)+1)
	}
	r.series[key] = s
	r.perName[name]++
	return s
}

type metricRecord struct {
	at               time.Time
	name, kind, unit string
	labels, series   string
	value            float64
	count            int64
	sum, min, max    float64
	buckets          string
	histogram        bool
}

// collect takes each series' interval: counters and histograms that moved,
// and every gauge that was ever set. Dropped series counts come back by name.
func (r *registry) collect(now time.Time) ([]*metricRecord, map[string]int64) {
	r.mu.Lock()
	all := make([]*metricSeries, 0, len(r.series))
	for _, s := range r.series {
		all = append(all, s)
	}
	dropped := r.dropped
	if len(dropped) > 0 {
		r.dropped = map[string]int64{}
	}
	r.mu.Unlock()

	var out []*metricRecord
	for _, s := range all {
		s.mu.Lock()
		rec := &metricRecord{at: now, name: s.name, kind: s.kind, unit: s.unit, labels: s.labels, series: s.id}
		keep := false
		switch s.kind {
		case kindCounter:
			if s.total > 0 {
				rec.value, keep = s.total, true
				s.total = 0
			}
		case kindGauge:
			rec.value, keep = s.value, s.touched
		case kindHistogram:
			if s.count > 0 {
				keep, rec.histogram = true, true
				rec.count, rec.sum, rec.min, rec.max = int64(s.count), s.sum, s.min, s.max
				rec.value = s.sum / float64(s.count)
				rec.buckets = encodeBuckets(s.bounds, s.counts)
				s.count, s.sum, s.min, s.max = 0, 0, 0, 0
				clear(s.counts)
			}
		}
		s.mu.Unlock()
		if keep {
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].series < out[j].series })
	return out, dropped
}

func encodeBuckets(bounds []float64, counts []uint64) string {
	b, _ := json.Marshal(struct {
		Bounds []float64 `json:"bounds"`
		Counts []uint64  `json:"counts"`
	}{bounds, counts})
	return string(b)
}

var metricColumns = []string{
	"created_at", "service", "instance", "name", "kind", "unit", "labels", "series", "value", "count", "sum", "min", "max", "buckets",
}

func (w *writer) metricParams(r *metricRecord) []any {
	row := []any{
		r.at.UTC().Format(tsLayout), nullable(w.ident.service), nullable(w.ident.instance),
		r.name, r.kind, nullable(r.unit), r.labels, r.series, r.value,
		nil, nil, nil, nil, nil,
	}
	if r.histogram {
		row[9], row[10], row[11], row[12], row[13] = r.count, r.sum, r.min, r.max, r.buckets
	}
	return row
}

// metricsRecorder writes the registry every interval through its own writer.
type metricsRecorder struct {
	cfg      MetricsConfig
	db       store
	w        *writer
	logger   *slog.Logger
	schemaOK bool
	stop     chan struct{}
	done     chan struct{}
	once     sync.Once
}

func newMetricsRecorder(cfg MetricsConfig, w *writer, db store, logger *slog.Logger) *metricsRecorder {
	if cfg.Interval <= 0 {
		cfg.Interval = 15 * time.Second
	}
	if cfg.MaxSeries > 0 {
		reg.maxSeries.Store(int64(cfg.MaxSeries))
	}
	return &metricsRecorder{cfg: cfg, db: db, w: w, logger: logger, stop: make(chan struct{}), done: make(chan struct{})}
}

func (m *metricsRecorder) start(ctx context.Context) {
	m.w.start(ctx)
	go func() {
		defer close(m.done)
		t := time.NewTicker(m.cfg.Interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-m.stop:
				m.flush(context.Background())
				return
			case <-t.C:
				m.flush(ctx)
			}
		}
	}()
}

func (m *metricsRecorder) flush(ctx context.Context) {
	recs, dropped := reg.collect(time.Now())
	for name, n := range dropped {
		m.logger.Warn("visionary: metric series dropped over the limit", "metric", name, "dropped", n, "limit", reg.maxSeries.Load())
	}
	if len(recs) == 0 {
		return
	}
	if !m.schemaOK {
		sctx, cancel := context.WithTimeout(quiet(ctx), 20*time.Second)
		err := m.db.execScript(sctx, m.db.dialect().MetricsSchema())
		cancel()
		if err != nil {
			m.logger.Error("visionary: create metrics failed", "err", err)
			return
		}
		m.schemaOK = true
	}
	for _, r := range recs {
		m.w.enqueue(r)
	}
}

func (m *metricsRecorder) close(ctx context.Context) {
	m.once.Do(func() { close(m.stop) })
	select {
	case <-m.done:
	case <-ctx.Done():
	}
	m.w.close(ctx)
}

// MetricSeries returns the canonical labels text of a series and its id, as
// the SDK contract defines them: labels as a JSON object with keys in byte
// order, and the first 16 hex digits of SHA-256 over name, a newline and that
// text. Empty keys are dropped, keys and values are cut to 100 and 200 bytes,
// and only the first 20 keys are kept.
func MetricSeries(name string, labels map[string]string) (text, id string) {
	keys := make([]string, 0, len(labels))
	clean := make(map[string]string, len(labels))
	for k, v := range labels {
		k = cutUTF8(strings.TrimSpace(k), maxLabelKey)
		if k == "" {
			continue
		}
		if _, dup := clean[k]; !dup {
			keys = append(keys, k)
		}
		clean[k] = cutUTF8(v, maxLabelValue)
	}
	sort.Strings(keys)
	if len(keys) > maxLabels {
		keys = keys[:maxLabels]
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		writeJSONString(&b, k)
		b.WriteByte(':')
		writeJSONString(&b, clean[k])
	}
	b.WriteByte('}')
	text = b.String()
	sum := sha256.Sum256([]byte(name + "\n" + text))
	return text, hex.EncodeToString(sum[:8])
}

// writeJSONString escapes only what JSON requires (quote, backslash and
// control characters), so every SDK produces the same bytes.
func writeJSONString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' || c == '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c < 0x20:
			b.WriteString(`\u00`)
			b.WriteByte("0123456789abcdef"[c>>4])
			b.WriteByte("0123456789abcdef"[c&0xf])
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
}

func cutUTF8(s string, n int) string {
	s = strings.ToValidUTF8(s, "�")
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func cleanMetricName(name string) string {
	return cutUTF8(strings.TrimSpace(name), maxMetricName)
}

// pairs reads key/value pairs; a key without a value gets "".
func pairs(kv []string) map[string]string {
	if len(kv) == 0 {
		return nil
	}
	m := make(map[string]string, (len(kv)+1)/2)
	for i := 0; i < len(kv); i += 2 {
		v := ""
		if i+1 < len(kv) {
			v = kv[i+1]
		}
		m[kv[i]] = v
	}
	return m
}

// seriesKey identifies a series in the registry without building its JSON.
func seriesKey(name, kind string, labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(name)
	b.WriteByte(0)
	b.WriteString(kind)
	for _, k := range keys {
		b.WriteByte(0)
		b.WriteString(k)
		b.WriteByte(1)
		b.WriteString(labels[k])
	}
	return b.String()
}
