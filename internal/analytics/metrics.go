package analytics

import (
	"net/http"
	"regexp"
)

func init() {
	allowedTables["METRICS"] = true
	chartRefs = append(chartRefs, regexp.MustCompile(`^metrics/(series|promql)$`))
}

func (s *Server) metricsSchema() []string { return []string{s.q.d.MetricsSchema()} }

func (s *Server) metricsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/write", s.handleRemoteWrite)
	mux.Handle("GET /api/metrics/names", s.authed(s.handleMetricNames))
	mux.Handle("GET /api/metrics/labels", s.authed(s.handleMetricLabels))
	mux.Handle("GET /api/metrics/series", s.authed(s.handleMetricSeries))
	s.promRoutes(mux)
}

func (s *Server) metricsPrompt() string {
	return `Table metrics: custom metrics the services define (counters, gauges, histograms), from the SDKs, OpenTelemetry metrics that are not host or runtime metrics, and Prometheus remote_write. One row per series per interval (15 s from the SDKs).
- id, created_at TEXT (same format), service, instance TEXT, name TEXT (e.g. 'orders_placed'), kind TEXT ('counter', 'gauge', 'histogram'), unit TEXT (e.g. 'ms', 'By'; may be NULL)
- labels TEXT: the series' labels as a JSON object of strings, keys sorted (e.g. '{"region":"eu","status":"ok"}'); read one with the dialect's JSON function. series TEXT: a 16-hex id of name + labels, for grouping one series' rows.
- value REAL: counters hold the increase over the row's interval (SUM(value) is the total over a range; divide by seconds for a rate), gauges the last value (average or take the latest per series, never sum over time), histograms the mean of the interval.
- count INTEGER, sum, min, max REAL: histograms only (NULL otherwise): observations in the interval, their sum, smallest and largest. Average = SUM(sum) / SUM(count).
- buckets TEXT: histograms only, JSON {"bounds": [upper bounds], "counts": [per bucket, one more than bounds for values above the last]}; percentiles need these merged, so prefer averages, min and max in SQL.
- Find what exists with SELECT name, kind, COUNT(DISTINCT series) FROM metrics WHERE created_at >= ... GROUP BY name, kind.

` + promqlPrompt()
}
