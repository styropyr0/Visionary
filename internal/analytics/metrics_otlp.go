package analytics

import (
	"math"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// OTLP metrics that fill no system_metrics column become custom metrics:
// monotonic sums are counters, other sums and gauges are gauges, and
// histograms (exponential ones as explicit buckets) are histograms.
// Summaries are skipped.

func (s *Server) otlpCustomMetric(out *otlpRows, res *otlpResource, m *metricspb.Metric) int64 {
	mi := s.metricIngest()
	base := metricPoint{service: res.service, instance: res.instance, name: m.GetName(), unit: m.GetUnit()}
	var rejected int64
	point := func(ts, start uint64, attrs []*commonpb.KeyValue) (metricPoint, bool) {
		if ts == 0 {
			rejected++
			return metricPoint{}, false
		}
		p := base
		p.at = time.Unix(0, int64(ts))
		if start > 0 {
			p.start = time.Unix(0, int64(start))
		}
		p.labels = otlpLabels(attrs)
		return p, true
	}
	number := func(dp *metricspb.NumberDataPoint) float64 {
		if _, isInt := dp.GetValue().(*metricspb.NumberDataPoint_AsInt); isInt {
			return float64(dp.GetAsInt())
		}
		return dp.GetAsDouble()
	}
	switch {
	case m.GetGauge() != nil:
		for _, dp := range m.GetGauge().GetDataPoints() {
			if p, ok := point(dp.GetTimeUnixNano(), 0, dp.GetAttributes()); ok {
				p.kind, p.value = "gauge", number(dp)
				mi.add(out, p)
			}
		}
	case m.GetSum() != nil:
		sum := m.GetSum()
		cumulative := sum.GetAggregationTemporality() != metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA
		for _, dp := range sum.GetDataPoints() {
			p, ok := point(dp.GetTimeUnixNano(), dp.GetStartTimeUnixNano(), dp.GetAttributes())
			if !ok {
				continue
			}
			p.value = number(dp)
			switch {
			case sum.GetIsMonotonic():
				p.kind, p.cumulative = "counter", cumulative
			case cumulative:
				p.kind = "gauge"
			default:
				continue
			}
			mi.add(out, p)
		}
	case m.GetHistogram() != nil:
		h := m.GetHistogram()
		cumulative := h.GetAggregationTemporality() != metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA
		for _, dp := range h.GetDataPoints() {
			p, ok := point(dp.GetTimeUnixNano(), dp.GetStartTimeUnixNano(), dp.GetAttributes())
			if !ok {
				continue
			}
			hp := &histPoint{count: float64(dp.GetCount()), sum: dp.GetSum(), min: math.NaN(), max: math.NaN(), bounds: dp.GetExplicitBounds()}
			if dp.Min != nil {
				hp.min = dp.GetMin()
			}
			if dp.Max != nil {
				hp.max = dp.GetMax()
			}
			for _, c := range dp.GetBucketCounts() {
				hp.counts = append(hp.counts, float64(c))
			}
			if len(hp.counts) != len(hp.bounds)+1 {
				hp.bounds, hp.counts = nil, []float64{hp.count}
			}
			p.kind, p.hist, p.cumulative = "histogram", hp, cumulative
			mi.add(out, p)
		}
	case m.GetExponentialHistogram() != nil:
		h := m.GetExponentialHistogram()
		cumulative := h.GetAggregationTemporality() != metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA
		for _, dp := range h.GetDataPoints() {
			p, ok := point(dp.GetTimeUnixNano(), dp.GetStartTimeUnixNano(), dp.GetAttributes())
			if !ok {
				continue
			}
			hp := exponentialBuckets(dp)
			if dp.Min != nil {
				hp.min = dp.GetMin()
			}
			if dp.Max != nil {
				hp.max = dp.GetMax()
			}
			p.kind, p.hist, p.cumulative = "histogram", hp, cumulative
			mi.add(out, p)
		}
	}
	return rejected
}

func exponentialBuckets(dp *metricspb.ExponentialHistogramDataPoint) *histPoint {
	hp := &histPoint{count: float64(dp.GetCount()), sum: dp.GetSum(), min: math.NaN(), max: math.NaN()}
	base := math.Pow(2, math.Pow(2, -float64(dp.GetScale())))
	bound := func(i int64) float64 { return math.Pow(base, float64(i)) }
	var bounds, counts []float64
	add := func(b, c float64) {
		if math.IsInf(b, 0) || math.IsNaN(b) || len(bounds) > 0 && b <= bounds[len(bounds)-1] {
			if len(counts) > 0 {
				counts[len(counts)-1] += c
				return
			}
		}
		bounds, counts = append(bounds, b), append(counts, c)
	}
	neg := dp.GetNegative()
	for i := len(neg.GetBucketCounts()) - 1; i >= 0; i-- {
		add(-bound(int64(neg.GetOffset())+int64(i)), float64(neg.GetBucketCounts()[i]))
	}
	add(dp.GetZeroThreshold(), float64(dp.GetZeroCount()))
	pos := dp.GetPositive()
	for i, c := range pos.GetBucketCounts() {
		add(bound(int64(pos.GetOffset())+int64(i)+1), float64(c))
	}
	hp.bounds, hp.counts = bounds, append(counts, 0)
	return hp
}

func otlpLabels(kvs []*commonpb.KeyValue) map[string]string {
	if len(kvs) == 0 {
		return nil
	}
	a := otlpAttrMap(kvs)
	out := make(map[string]string, len(a))
	for k := range a {
		out[k] = a.str(k)
	}
	return out
}
