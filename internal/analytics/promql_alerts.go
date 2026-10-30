package analytics

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
)

// The promql alert evaluates an expression at each check and compares every
// series it returns with the threshold, so it fires per series.

func promqlMetricDefs() []metricDef {
	return []metricDef{{ID: "promql", Label: "PromQL expression", Group: "metrics", Unit: "none",
		Description: "Any PromQL over custom metrics and Visionary's own series, e.g. sum by (route) (rate(visionary_http_requests_total{status=~\"5..\"}[5m])). Fires per series it returns.",
		Scopes:      []string{}, Op: ">", Threshold: 0, UpIsBad: true}}
}

func checkPromRule(sp *ruleSpec) error {
	sp.PromQL = strings.TrimSpace(sp.PromQL)
	if sp.PromQL == "" {
		return badInput("write a PromQL expression")
	}
	if len(sp.PromQL) > 2000 {
		return badInput("the expression is longer than 2000 characters")
	}
	expr, err := parsePromQL(sp.PromQL)
	if err != nil {
		return badInput("the expression: " + err.Error())
	}
	if t := exprType(expr); t != typVector && t != typScalar {
		return badInput("the expression must return a number or an instant vector, not " + article(t))
	}
	sp.Scope = alertScope{}
	return nil
}

func (s *Server) observePromQL(ctx context.Context, r alertRule, now time.Time) ([]observation, error) {
	if r.Spec.PromQL == "" {
		return nil, nil
	}
	_, v, _, err := s.runPromQL(ctx, r.Spec.PromQL, now, now, 0)
	if err != nil {
		var pe *promError
		if errors.As(err, &pe) {
			return nil, badInput(pe.Error())
		}
		return nil, err
	}
	what := "the PromQL expression"
	var out []observation
	add := func(key string, val float64) {
		ob := observation{Key: key, Label: key, Threshold: ptr(r.Spec.Threshold), Value: ptr(val), Breach: compare(r.Spec.Op, val, r.Spec.Threshold)}
		ob.Message = thresholdMessage(what, "none", ob, r.Spec, describeScope(r.Spec.Scope, ob.Label))
		out = append(out, ob)
	}
	if v.isScalar {
		add("", v.scalar[0])
		return out, nil
	}
	for _, sr := range mergeSeries(v.vec) {
		if sr.ok[0] {
			key := sr.labels.String()
			if key == "{}" {
				key = ""
			}
			add(key, sr.v[0])
		}
	}
	if len(out) == 0 {
		ob := observation{Threshold: ptr(r.Spec.Threshold), Label: what}
		ob.Message = thresholdMessage(what, "none", ob, r.Spec, describeScope(r.Spec.Scope, ""))
		return []observation{ob}, nil
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (s *Server) previewPromQL(ctx context.Context, sp ruleSpec, day timeSpan, tr timeRange) (*Result, string, error) {
	if strings.TrimSpace(sp.PromQL) == "" {
		return nil, "Write an expression to see its last day.", nil
	}
	tr.From, tr.To, tr.Bucket = day.From, day.To, 300
	res, err := s.promResult(ctx, sp.PromQL, tr)
	if err != nil {
		var pe *promError
		if errors.As(err, &pe) {
			return nil, "The expression: " + pe.Error(), nil
		}
		if errors.Is(err, errPromTooLarge) {
			return nil, err.Error(), nil
		}
		return nil, "", err
	}
	note := "The expression every 5 minutes; the rule evaluates it at each check and fires per series."
	if res.Truncated {
		note += " Showing the 50 largest series."
	}
	return res, note, nil
}
