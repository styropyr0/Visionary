package analytics

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

// SLO rules: a target share of requests that succeed (status < 500) within
// the Apdex threshold, over 30 days. They page on multi-window burn rates as
// in Google's SRE workbook: the 1h and 5m windows both burning the budget 14.4
// times too fast (2% of a 30-day budget in an hour), or the 6h and 30m
// windows both at 6× (5% in six hours). The short window makes the alert
// resolve soon after the burn stops.

const sloPeriod = 30 * 24 * time.Hour

type burnPair struct {
	Long, Short time.Duration
	Rate        float64
}

var burnPairs = []burnPair{
	{time.Hour, 5 * time.Minute, 14.4},
	{6 * time.Hour, 30 * time.Minute, 6},
}

// sloStatus is the state of one SLO: burn rates per window and the budget
// left over the 30-day period.
type sloStatus struct {
	Target          float64            `json:"target"`
	Burn            map[string]float64 `json:"burn"`
	Requests        map[string]int64   `json:"requests"`
	Total30d        int64              `json:"total_30d"`
	Bad30d          int64              `json:"bad_30d"`
	BudgetRemaining *float64           `json:"budget_remaining"` // fraction, can go below 0
	Breach          bool               `json:"breach"`
	Pair            string             `json:"pair,omitempty"`
	Value           float64            `json:"value"`
	Threshold       float64            `json:"threshold"`
	BudgetAt        string             `json:"budget_at"`
}

func sloTarget(sp ruleSpec, def float64) float64 {
	if sp.Target > 0 && sp.Target < 100 {
		return sp.Target
	}
	return def
}

// sloBad is a request that spends error budget.
func (s *Server) sloBad() string {
	return "(status >= 500 OR duration_ms > " + strconv.Itoa(s.cfg.ApdexMs) + ")"
}

var sloMetric = metricDef{Scopes: []string{"route", "method", "service"}}

func (s *Server) sloStatus(ctx context.Context, r alertRule, now time.Time) (*sloStatus, error) {
	st := &sloStatus{Target: sloTarget(r.Spec, s.cfg.SLOTarget), Burn: map[string]float64{}, Requests: map[string]int64{}}
	budget := 1 - st.Target/100
	scope, sargs := scopeClause(sloMetric, r.Spec.Scope)
	bad := s.sloBad()

	windows := []time.Duration{5 * time.Minute, 30 * time.Minute, time.Hour, 6 * time.Hour}
	sel := ""
	for _, w := range windows {
		since := "'" + now.Add(-w).UTC().Format(tsLayout) + "'"
		sel += countIf("created_at >= "+since) + ", " + countIf("created_at >= "+since+" AND "+bad) + ", "
	}
	args := append([]any{now.Add(-6 * time.Hour).UTC().Format(tsLayout), now.UTC().Format(tsLayout)}, sargs...)
	res, err := s.db.Query(ctx, `SELECT `+sel+`COUNT(*) FROM api_logs WHERE created_at >= ? AND created_at < ?`+scope, args...)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 1 && len(res.Rows[0]) == 2*len(windows)+1 {
		row := res.Rows[0]
		for i, w := range windows {
			total, nbad := toInt(row[2*i]), toInt(row[2*i+1])
			name := fmtSpan(w)
			st.Requests[name] = total
			if total > 0 && budget > 0 {
				st.Burn[name] = float64(nbad) / float64(total) / budget
			} else {
				st.Burn[name] = 0
			}
		}
	}

	for _, p := range burnPairs {
		v := min(st.Burn[fmtSpan(p.Long)], st.Burn[fmtSpan(p.Short)])
		if v >= p.Rate {
			st.Breach, st.Value, st.Threshold = true, v, p.Rate
			st.Pair = fmtSpan(p.Long) + "+" + fmtSpan(p.Short)
			break
		}
	}
	if !st.Breach {
		slow := burnPairs[len(burnPairs)-1]
		st.Value = min(st.Burn[fmtSpan(slow.Long)], st.Burn[fmtSpan(slow.Short)])
		st.Threshold = slow.Rate
	}

	total, nbad, at, err := s.alertEngine().sloBudget(ctx, r, now, scope, sargs)
	if err != nil {
		return nil, err
	}
	st.Total30d, st.Bad30d, st.BudgetAt = total, nbad, at
	if total > 0 && budget > 0 {
		st.BudgetRemaining = ptr(1 - float64(nbad)/(float64(total)*budget))
	}
	return st, nil
}

// sloBudgetQuery counts the 30-day period. It is the expensive part, so the
// engine caches it for a few minutes.
func (s *Server) sloBudgetQuery(ctx context.Context, now time.Time, scope string, sargs []any) (int64, int64, error) {
	args := append([]any{now.Add(-sloPeriod).UTC().Format(tsLayout), now.UTC().Format(tsLayout)}, sargs...)
	res, err := s.db.Query(ctx, `SELECT COUNT(*), `+countIf(s.sloBad())+` FROM api_logs WHERE created_at >= ? AND created_at < ?`+scope, args...)
	if err != nil {
		return 0, 0, err
	}
	if len(res.Rows) != 1 || len(res.Rows[0]) != 2 {
		return 0, 0, nil
	}
	return toInt(res.Rows[0][0]), toInt(res.Rows[0][1]), nil
}

func (s *Server) observeSLO(ctx context.Context, r alertRule, now time.Time) ([]observation, error) {
	st, err := s.sloStatus(ctx, r, now)
	if err != nil {
		return nil, err
	}
	ob := observation{Value: ptr(st.Value), Threshold: ptr(st.Threshold), Breach: st.Breach}
	where := describeScope(r.Spec.Scope, "")
	left := "no traffic in 30 days"
	if st.BudgetRemaining != nil {
		left = fmt.Sprintf("%.1f%% of the 30-day error budget left", 100**st.BudgetRemaining)
	}
	if st.Breach {
		days := ""
		if st.BudgetRemaining != nil && *st.BudgetRemaining > 0 && st.Value > 0 {
			days = fmt.Sprintf("; at this rate it is gone in %s", humanDuration(time.Duration(*st.BudgetRemaining*float64(sloPeriod)/st.Value)))
		}
		ob.Message = fmt.Sprintf("SLO %.2f%%%s is burning its error budget %.1f× too fast over %s (alert at %.1f×): %s%s.",
			st.Target, where, st.Value, st.Pair, st.Threshold, left, days)
	} else {
		ob.Message = fmt.Sprintf("SLO %.2f%%%s: burn %.1f× (1h), %.1f× (6h); %s.", st.Target, where, st.Burn["1h"], st.Burn["6h"], left)
	}
	return []observation{ob}, nil
}
