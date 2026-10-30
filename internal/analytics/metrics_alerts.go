package analytics

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// The custom_metric alert reads one custom metric, narrowed by labels and
// service, aggregated over the rule's window: one reading, or one per value
// of a label when the rule groups by it.

func customMetricDefs() []metricDef {
	return []metricDef{{ID: "custom_metric", Label: "Custom metric", Group: "metrics", Unit: "none",
		Description: "A counter, gauge or histogram your services record, aggregated over the window: sum, average, max, min, rate per second, or p50/p95/p99 of a histogram. Fires per label value when grouped.",
		Scopes:      []string{"service"}, Op: ">", Threshold: 0, UpIsBad: true}}
}

// checkMetricRule cleans a custom_metric rule's query.
func checkMetricRule(sp *ruleSpec) error {
	mq := sp.MetricQuery
	if mq == nil || strings.TrimSpace(mq.Name) == "" {
		return badInput("pick a custom metric")
	}
	mq.Name = strings.TrimSpace(mq.Name)
	mq.Group = strings.TrimSpace(mq.Group)
	if mq.Agg != "" && !slices.Contains(metricAggs, mq.Agg) {
		return badInput("aggregation must be one of " + strings.Join(metricAggs, ", "))
	}
	if len(mq.Labels) > 20 {
		return badInput("at most 20 label filters")
	}
	return nil
}

func (s *Server) observeCustomMetric(ctx context.Context, r alertRule, now time.Time) ([]observation, error) {
	if r.Spec.MetricQuery == nil {
		return nil, nil
	}
	mq := *r.Spec.MetricQuery
	mq.Service = r.Spec.Scope.Service
	w := specWindow(r.Spec)
	pts, info, err := s.runMetricQuery(ctx, mq, now.Add(-w), now, 0)
	if err != nil {
		if missingTable(err, "metrics") {
			return nil, nil
		}
		return nil, err
	}
	what := metricQueryLabel(mq, info.Agg)
	if len(pts) == 0 && mq.Group == "" {
		ob := observation{Threshold: ptr(r.Spec.Threshold), Label: what}
		ob.Message = thresholdMessage(what, "none", ob, r.Spec, describeScope(r.Spec.Scope, ""))
		return []observation{ob}, nil
	}
	var out []observation
	for _, p := range pts {
		key := ""
		if mq.Group != "" {
			key = p.Series
		}
		ob := observation{Key: key, Label: key, Threshold: ptr(r.Spec.Threshold), Value: ptr(p.Value), Breach: compare(r.Spec.Op, p.Value, r.Spec.Threshold)}
		ob.Message = thresholdMessage(what, "none", ob, r.Spec, describeScope(r.Spec.Scope, ob.Label))
		out = append(out, ob)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// metricQueryLabel names what a rule measures, e.g. "p95 of checkout_ms {region=eu}".
func metricQueryLabel(mq metricQuery, agg string) string {
	s := agg + " of " + mq.Name
	if len(mq.Labels) > 0 {
		keys := make([]string, 0, len(mq.Labels))
		for k := range mq.Labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = k + "=" + mq.Labels[k]
		}
		s += " {" + strings.Join(parts, ", ") + "}"
	}
	if mq.Group != "" {
		s += " by " + strings.TrimPrefix(mq.Group, ":")
	}
	return s
}

func (s *Server) previewCustomMetric(ctx context.Context, sp ruleSpec, day timeSpan, tr timeRange) (*Result, string, error) {
	if sp.MetricQuery == nil {
		return nil, "Pick a metric to see its last day.", nil
	}
	mq := *sp.MetricQuery
	mq.Service = sp.Scope.Service
	bucket := previewBucket(specWindow(sp))
	pts, info, err := s.runMetricQuery(ctx, mq, day.From, day.To, bucket)
	if err != nil {
		if missingTable(err, "metrics") {
			return nil, "No custom metrics yet: record one with an SDK, OpenTelemetry or Prometheus remote_write.", nil
		}
		return nil, "", err
	}
	tr.Bucket = bucket
	note := fmt.Sprintf("%s per %s; the rule reads it over its window of %s.", metricQueryLabel(mq, info.Agg), fmtSpan(time.Duration(bucket)*time.Second), fallback(sp.Window, "5m"))
	if info.Series == 0 {
		note = "No series of " + mq.Name + " match in the last day."
	}
	return pointsResult(pts, tr), note, nil
}
