package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// readQueries are the requests behind the main dashboard pages.
var readQueries = []struct{ name, path string }{
	{"Overview: stats", "/api/overview/stats"},
	{"Overview: traffic", "/api/overview/traffic"},
	{"Overview: latency", "/api/overview/latency"},
	{"Overview: routes", "/api/overview/routes"},
	{"Performance: summary", "/api/performance/summary"},
	{"Performance: percentiles", "/api/performance/percentiles"},
	{"Performance: routes", "/api/performance/routes"},
	{"Logs: latest page", "/api/applogs?limit=100"},
	{"Logs: text search", "/api/applogs?limit=100&q=" + url.QueryEscape(`"order failed"`)},
	{"Logs: errors", "/api/applogs?limit=100&q=" + url.QueryEscape(`level:error`)},
	{"Logs: histogram", "/api/applogs/histogram"},
	{"Traces: search", "/api/traces"},
	{"Traces: errors only", "/api/traces?errors=1"},
	{"Metrics: PromQL rate", "/api/metrics/promql?query=" + url.QueryEscape(`sum by (route) (rate(bench_requests[5m]))`)},
}

func runRead(ctx context.Context, o options) (string, error) {
	base := strings.TrimRight(o.server, "/")
	client := &http.Client{Timeout: 2 * time.Minute}
	var ranges []time.Duration
	for _, s := range strings.Split(o.ranges, ",") {
		d, err := parseRange(strings.TrimSpace(s))
		if err != nil {
			return "", err
		}
		ranges = append(ranges, d)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\nRead: %s, each query %d times; median / slowest in ms.\n\n| Query |", base, o.reads)
	for _, s := range strings.Split(o.ranges, ",") {
		fmt.Fprintf(&b, " %s |", strings.TrimSpace(s))
	}
	b.WriteString("\n|---|" + strings.Repeat("---|", len(ranges)) + "\n")
	for _, q := range readQueries {
		fmt.Fprintf(&b, "| %s |", q.name)
		for _, rng := range ranges {
			to := time.Now().UTC()
			sep := "?"
			if strings.Contains(q.path, "?") {
				sep = "&"
			}
			u := base + q.path + sep + "from=" + url.QueryEscape(to.Add(-rng).Format(time.RFC3339)) + "&to=" + url.QueryEscape(to.Format(time.RFC3339))
			times := make([]float64, 0, o.reads)
			failed := ""
			for range o.reads {
				ms, err := timeGet(ctx, client, u, o.token)
				if err != nil {
					failed = err.Error()
					break
				}
				times = append(times, ms)
			}
			if failed != "" {
				fmt.Fprintf(&b, " failed: %s |", failed)
				continue
			}
			slices.Sort(times)
			fmt.Fprintf(&b, " %.0f / %.0f |", times[len(times)/2], times[len(times)-1])
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

func timeGet(ctx context.Context, client *http.Client, u, token string) (float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	ms := float64(time.Since(start).Microseconds()) / 1000
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return ms, nil
}

// parseRange reads 15m, 1h, 24h or 7d.
func parseRange(s string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(s, "d"); ok {
		var n int
		if _, err := fmt.Sscan(days, &n); err != nil || n <= 0 {
			return 0, fmt.Errorf("bad range %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("bad range %q", s)
	}
	return d, nil
}
