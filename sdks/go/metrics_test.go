package visionary

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func freshRegistry(t *testing.T) {
	t.Helper()
	old := reg
	reg = newRegistry()
	t.Cleanup(func() { reg = old })
}

func TestMetricSeriesCanonical(t *testing.T) {
	text, id := MetricSeries("orders", map[string]string{"region": "eu", "a\"b": "x\ny", "": "dropped"})
	if want := `{"a\"b":"x\u000ay","region":"eu"}`; text != want {
		t.Fatalf("labels = %s, want %s", text, want)
	}
	if len(id) != 16 {
		t.Fatalf("id %q is not 16 hex digits", id)
	}
	if text, id := MetricSeries("orders_placed", map[string]string{"region": "eu"}); text != `{"region":"eu"}` || id != "da4f5c6244c4f3e4" {
		t.Fatalf("got %s %s", text, id)
	}
	if text, _ := MetricSeries("x", nil); text != "{}" {
		t.Fatalf("no labels = %s", text)
	}
	if text, _ := MetricSeries("x", map[string]string{"k": "<é>"}); text != `{"k":"<é>"}` {
		t.Fatalf("non-ASCII and HTML characters must stay literal, got %s", text)
	}
}

func TestMetricsCollect(t *testing.T) {
	freshRegistry(t)
	Counter("orders", "region", "eu").Add(2)
	Counter("orders", "region", "eu").Inc()
	Counter("orders", "region", "eu").Add(-5)
	Counter("orders", "region", "us")
	Gauge("queue").Set(7)
	DescribeMetric("took", MetricOptions{Unit: "ms", Buckets: []float64{100, 10}})
	h := Histogram("took")
	for _, v := range []float64{5, 10, 50, 500} {
		h.Observe(v)
	}

	recs, _ := reg.collect(time.Now())
	got := map[string]*metricRecord{}
	for _, r := range recs {
		got[r.name] = r
	}
	if len(recs) != 3 {
		t.Fatalf("want 3 rows (the idle counter skipped), got %d", len(recs))
	}
	if r := got["orders"]; r.value != 3 || r.labels != `{"region":"eu"}` || r.kind != "counter" {
		t.Fatalf("counter row %+v", r)
	}
	if r := got["queue"]; r.value != 7 || r.kind != "gauge" {
		t.Fatalf("gauge row %+v", r)
	}
	r := got["took"]
	if r.count != 4 || r.sum != 565 || r.min != 5 || r.max != 500 || r.unit != "ms" {
		t.Fatalf("histogram row %+v", r)
	}
	var b struct {
		Bounds []float64 `json:"bounds"`
		Counts []int     `json:"counts"`
	}
	if err := json.Unmarshal([]byte(r.buckets), &b); err != nil {
		t.Fatal(err)
	}
	if len(b.Bounds) != 2 || b.Counts[0] != 2 || b.Counts[1] != 1 || b.Counts[2] != 1 {
		t.Fatalf("buckets %s", r.buckets)
	}

	recs, _ = reg.collect(time.Now())
	if len(recs) != 1 || recs[0].name != "queue" {
		t.Fatalf("second interval: %+v", recs)
	}
}

func TestMetricsSeriesLimit(t *testing.T) {
	freshRegistry(t)
	reg.maxSeries.Store(2)
	for _, u := range []string{"a", "b", "c", "d"} {
		Counter("hits", "user", u).Inc()
	}
	Counter("hits", "user", "a").Inc()
	recs, dropped := reg.collect(time.Now())
	if len(recs) != 2 || dropped["hits"] != 2 {
		t.Fatalf("rows %d, dropped %v", len(recs), dropped)
	}
}

func TestMetricsNilSafe(t *testing.T) {
	var c *CounterSeries
	c.Add(1)
	var g GaugeSeries
	g.Set(1)
	var h *HistogramSeries
	h.Observe(1)
	if Counter("  ").s != nil {
		t.Fatal("an empty name records nothing")
	}
}

func TestMetricsRecorderWrites(t *testing.T) {
	freshRegistry(t)
	st := &fakeStore{}
	ident := identity{service: "api", instance: "i-1"}
	w := newWriter(RequestsConfig{QueueSize: 100}, false, ident, st, slog.Default())
	w.metricsOnly = true
	m := newMetricsRecorder(MetricsConfig{Interval: time.Hour}, w, st, slog.Default())
	m.start(context.Background())
	Counter("jobs_done", "queue", "mail").Add(4)
	m.close(context.Background())
	if len(st.written) != 1 {
		t.Fatalf("want one insert, got %d", len(st.written))
	}
	s := st.written[0]
	if !strings.HasPrefix(s.sql, "INSERT INTO metrics (created_at, service, instance, name, kind") {
		t.Fatalf("sql %s", s.sql)
	}
	if s.args[1] != "api" || s.args[3] != "jobs_done" || s.args[4] != "counter" || s.args[8] != 4.0 || s.args[9] != nil {
		t.Fatalf("args %v", s.args)
	}
	if st.scripts != 1 {
		t.Fatalf("the schema should be created once, got %d", st.scripts)
	}
}
