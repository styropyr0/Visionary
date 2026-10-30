package analytics

import (
	"bytes"
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

func metricsTestServer(t *testing.T) *Server {
	s := logsTestServer(t, nil)
	t.Cleanup(func() { metricIngests.Delete(s) })
	return s
}

func TestSnappyDecode(t *testing.T) {
	// "abc" as a literal, then a 6-byte copy from 3 back.
	got, err := snappyDecode([]byte{9, 0x08, 'a', 'b', 'c', 0x09, 3}, 100)
	if err != nil || string(got) != "abcabcabc" {
		t.Fatalf("got %q, %v", got, err)
	}
	for _, bad := range [][]byte{{9, 0x08, 'a', 'b', 'c'}, {3, 0x09, 9}, {200, 1}} {
		if _, err := snappyDecode(bad, 100); err == nil {
			t.Errorf("%v decoded", bad)
		}
	}
}

func TestBucketQuantile(t *testing.T) {
	b := map[float64]float64{10: 50, 100: 40, math.Inf(1): 10}
	if v, _ := bucketQuantile(b, 0.5); v != 10 {
		t.Errorf("p50 = %v", v)
	}
	if v, _ := bucketQuantile(b, 0.7); v != 55 {
		t.Errorf("p70 = %v, want halfway through 10..100", v)
	}
	if v, _ := bucketQuantile(b, 0.99); v != 100 {
		t.Errorf("p99 in the +Inf bucket = %v, want the last bound", v)
	}
}

// Prometheus remote_write: a counter, a gauge and a classic histogram, sent
// twice so the cumulative ones have a baseline.
func TestRemoteWrite(t *testing.T) {
	s := metricsTestServer(t)
	base := time.Now().Add(-time.Minute).Truncate(time.Second)
	send := func(at time.Time, requests, temp float64, le map[float64]float64, sum float64) {
		var req []byte
		series := func(labels [][2]string, v float64) {
			var ts []byte
			for _, l := range labels {
				var lb []byte
				lb = protowire.AppendTag(lb, 1, protowire.BytesType)
				lb = protowire.AppendString(lb, l[0])
				lb = protowire.AppendTag(lb, 2, protowire.BytesType)
				lb = protowire.AppendString(lb, l[1])
				ts = protowire.AppendTag(ts, 1, protowire.BytesType)
				ts = protowire.AppendBytes(ts, lb)
			}
			var sm []byte
			sm = protowire.AppendTag(sm, 1, protowire.Fixed64Type)
			sm = protowire.AppendFixed64(sm, math.Float64bits(v))
			sm = protowire.AppendTag(sm, 2, protowire.VarintType)
			sm = protowire.AppendVarint(sm, uint64(at.UnixMilli()))
			ts = protowire.AppendTag(ts, 2, protowire.BytesType)
			ts = protowire.AppendBytes(ts, sm)
			req = protowire.AppendTag(req, 1, protowire.BytesType)
			req = protowire.AppendBytes(req, ts)
		}
		job := [][2]string{{"job", "shop"}, {"instance", "web-1"}}
		series(append([][2]string{{"__name__", "http_requests_total"}, {"code", "200"}}, job...), requests)
		series(append([][2]string{{"__name__", "room_temp"}}, job...), temp)
		total := 0.0
		for bound, n := range le {
			series(append([][2]string{{"__name__", "latency_seconds_bucket"}, {"le", map[bool]string{true: "+Inf", false: fnum(bound)}[math.IsInf(bound, 1)]}}, job...), n)
			total = math.Max(total, n)
		}
		series(append([][2]string{{"__name__", "latency_seconds_sum"}}, job...), sum)
		series(append([][2]string{{"__name__", "latency_seconds_count"}}, job...), total)

		r := httptest.NewRequest("POST", "/api/v1/write", bytes.NewReader(snappyLiteral(req)))
		r.Header.Set("Content-Encoding", "snappy")
		r.Header.Set("Content-Type", "application/x-protobuf")
		r.Header.Set("Authorization", "Bearer k1")
		w := httptest.NewRecorder()
		s.handleRemoteWrite(w, r)
		if w.Code != http.StatusNoContent {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}
	}
	send(base, 100, 21, map[float64]float64{0.1: 5, 1: 8, math.Inf(1): 10}, 4)
	send(base.Add(15*time.Second), 130, 22.5, map[float64]float64{0.1: 15, 1: 28, math.Inf(1): 30}, 10)

	ctx := context.Background()
	from, to := base.Add(-time.Minute), base.Add(time.Minute)
	check := func(mq metricQuery, want float64) {
		t.Helper()
		pts, info, err := s.runMetricQuery(ctx, mq, from, to, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(pts) != 1 || math.Abs(pts[0].Value-want) > 1e-9 {
			t.Fatalf("%s %s (%s): %+v, want %v", mq.Agg, mq.Name, info.Kind, pts, want)
		}
	}
	check(metricQuery{Name: "http_requests_total", Labels: map[string]string{"code": "200"}, Service: "shop"}, 30)
	check(metricQuery{Name: "room_temp", Agg: "max"}, 22.5)
	check(metricQuery{Name: "latency_seconds", Agg: "count"}, 20)
	check(metricQuery{Name: "latency_seconds", Agg: "avg"}, 6.0/20)
	// 10 of the 20 new observations are <= 0.1, 13 more by 1: p50 is the 0.1 bound.
	check(metricQuery{Name: "latency_seconds", Agg: "p50"}, 0.1)

	r := httptest.NewRequest("POST", "/api/v1/write", nil)
	w := httptest.NewRecorder()
	s.handleRemoteWrite(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no key: %d", w.Code)
	}
}

// snappyLiteral encodes b as one snappy literal, which every decoder reads.
func snappyLiteral(b []byte) []byte {
	out := protowire.AppendVarint(nil, uint64(len(b)))
	n := len(b) - 1
	out = append(out, 62<<2, byte(n), byte(n>>8), byte(n>>16))
	return append(out, b...)
}

func TestOTLPCustomMetrics(t *testing.T) {
	s := metricsTestServer(t)
	ctx := context.Background()
	o := s.otlp()
	start := time.Now().Add(-time.Hour)
	at := time.Now().Add(-time.Minute)
	attrs := []*commonpb.KeyValue{{Key: "queue", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "mail"}}}}
	export := func(at time.Time, jobs int64, depth float64, hist []uint64, sum float64) {
		ns, st := uint64(at.UnixNano()), uint64(start.UnixNano())
		var count uint64
		for _, c := range hist {
			count += c
		}
		rm := &metricspb.ResourceMetrics{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{{Key: "service.name", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "worker"}}}}},
			ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{
				{Name: "jobs.done", Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{IsMonotonic: true, AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
					DataPoints: []*metricspb.NumberDataPoint{{Attributes: attrs, StartTimeUnixNano: st, TimeUnixNano: ns, Value: &metricspb.NumberDataPoint_AsInt{AsInt: jobs}}}}}},
				{Name: "queue.depth", Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{
					DataPoints: []*metricspb.NumberDataPoint{{Attributes: attrs, TimeUnixNano: ns, Value: &metricspb.NumberDataPoint_AsDouble{AsDouble: depth}}}}}},
				{Name: "job.duration", Unit: "ms", Data: &metricspb.Metric_Histogram{Histogram: &metricspb.Histogram{AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA,
					DataPoints: []*metricspb.HistogramDataPoint{{TimeUnixNano: ns, Count: count, Sum: &sum, ExplicitBounds: []float64{10, 100}, BucketCounts: hist}}}}},
				// A system metric keeps going to system_metrics.
				{Name: "system.cpu.load_average.1m", Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{
					DataPoints: []*metricspb.NumberDataPoint{{TimeUnixNano: ns, Value: &metricspb.NumberDataPoint_AsDouble{AsDouble: 1.5}}}}}},
			}}},
		}
		if _, err := o.ingestMetrics(ctx, []*metricspb.ResourceMetrics{rm}); err != nil {
			t.Fatal(err)
		}
	}
	export(at, 40, 3, []uint64{1, 2, 1}, 300)
	export(at.Add(15*time.Second), 47, 5, []uint64{4, 0, 0}, 20)

	res, err := s.db.Query(ctx, `SELECT COUNT(*) FROM metrics WHERE name LIKE 'system.%'`)
	if err != nil || toInt(res.Rows[0][0]) != 0 {
		t.Fatalf("system metrics leaked into metrics: %v %v", res, err)
	}
	from, to := at.Add(-time.Minute), at.Add(time.Minute)
	pts, info, err := s.runMetricQuery(ctx, metricQuery{Name: "jobs.done", Group: "queue"}, from, to, 0)
	if err != nil || len(pts) != 1 || pts[0].Value != 7 || pts[0].Series != "mail" || info.Kind != "counter" {
		t.Fatalf("counter: %+v %+v %v", pts, info, err)
	}
	pts, _, err = s.runMetricQuery(ctx, metricQuery{Name: "queue.depth", Group: ":service"}, from, to, 0)
	if err != nil || len(pts) != 1 || pts[0].Value != 4 || pts[0].Series != "worker" {
		t.Fatalf("gauge: %+v %v", pts, err)
	}
	pts, info, err = s.runMetricQuery(ctx, metricQuery{Name: "job.duration", Agg: "p50"}, from, to, 0)
	if err != nil || len(pts) != 1 || pts[0].Value != 8 || info.Unit != "ms" {
		t.Fatalf("histogram: %+v %+v %v", pts, info, err)
	}
	if _, _, err := s.runMetricQuery(ctx, metricQuery{Name: "queue.depth", Agg: "p95"}, from, to, 0); err == nil {
		t.Fatal("p95 of a gauge should be refused")
	}

	// The alert reads the same numbers.
	rule := alertRule{ID: 1, Kind: "threshold", Spec: ruleSpec{Metric: "custom_metric", Op: ">", Threshold: 5, Window: "1h",
		MetricQuery: &metricQuery{Name: "jobs.done", Labels: map[string]string{"queue": "mail"}}}}
	obs, err := s.observe(ctx, rule, time.Now())
	if err != nil || len(obs) != 1 || obs[0].Value == nil || *obs[0].Value != 7 || !obs[0].Breach {
		t.Fatalf("alert: %+v %v", obs, err)
	}
}
