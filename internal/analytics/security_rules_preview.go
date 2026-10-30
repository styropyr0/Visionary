package analytics

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Testing a rule before it is saved: what it would have flagged over the
// last 24 hours. Threshold and anomaly rules count in window-sized buckets,
// new-value rules compare each value's first time with the learning period
// before (up to 7 days, to keep the read small), and sequences run as the
// job would over the whole day.

const rulePreviewSpan = 24 * time.Hour

type previewHit struct {
	At         string            `json:"at"`
	Group      map[string]string `json:"group"`
	Value      string            `json:"value,omitempty"`
	Count      int64             `json:"count"`
	Summary    string            `json:"summary"`
	Suppressed bool              `json:"suppressed,omitempty"`
}

func (s *Server) handleTestDetectionRule(w http.ResponseWriter, r *http.Request) {
	var rule detectionRule
	if err := readJSON(r, &rule); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.checkDetectionRule(&rule); err != nil {
		s.fail(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	to := time.Now().UTC().Add(-securityLag)
	hits, note, err := s.previewRule(ctx, &rule, to)
	if err != nil {
		s.fail(w, dbErr(err))
		return
	}
	if hits == nil {
		hits = []previewHit{}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].At > hits[j].At })
	groups := map[string]bool{}
	signals := 0
	for _, h := range hits {
		if !h.Suppressed {
			signals++
			groups[groupKey(rule.GroupBy, h.Group)] = true
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"from": to.Add(-rulePreviewSpan).Format(time.RFC3339), "to": to.Format(time.RFC3339),
		"hits": head(hits, 200), "total": len(hits), "signals": signals, "groups": len(groups), "note": note,
	})
}

func (s *Server) previewRule(ctx context.Context, r *detectionRule, to time.Time) ([]previewHit, string, error) {
	from := to.Add(-rulePreviewSpan)
	mark := func(h previewHit) previewHit {
		values := map[string]string{}
		for k, v := range h.Group {
			values[k] = v
		}
		if h.Value != "" {
			values[r.Field] = h.Value
		}
		h.Suppressed = r.suppressedBy(values, time.Now()) != nil
		return h
	}
	var out []previewHit
	switch r.Logic {
	case "threshold", "anomaly":
		buckets, err := s.previewBuckets(ctx, r, from, to)
		if err != nil {
			return nil, "", err
		}
		note := fmt.Sprintf("Counted in %s buckets; the job slides its window, so it can also catch bursts that straddle two buckets.", fmtSpan(r.window()))
		for _, h := range buckets {
			out = append(out, mark(h))
		}
		return out, note, nil
	case "new_value":
		if r.Field == "country" && !s.geoEnabled() {
			return nil, "", badInput("watching countries needs a location database (Settings → Server → Threat intelligence)")
		}
		learn := min(r.learn(), 7*24*time.Hour)
		pairs, err := s.newValueRows(ctx, r, from.Add(-learn), to, 5000)
		if err != nil {
			return nil, "", err
		}
		first := map[string]string{}
		for _, p := range pairs {
			k := p.key + "\x00" + p.value
			if f, ok := first[k]; !ok || p.first < f {
				first[k] = p.first
			}
		}
		since := from.Format(tsLayout)
		for _, p := range pairs {
			if first[p.key+"\x00"+p.value] != p.first || p.first < since {
				continue
			}
			first[p.key+"\x00"+p.value] = ""
			who := ""
			if p.key != "" {
				who = " for " + p.key
			}
			out = append(out, mark(previewHit{At: p.first, Group: p.group, Value: p.value, Count: p.n,
				Summary: fmt.Sprintf("First %s %q%s.", r.Field, p.value, who)}))
		}
		note := "New compared with the " + fmtSpan(learn) + " before each value appeared."
		if learn < r.learn() {
			note += " The rule itself learns from " + fmtSpan(r.learn()) + ", so it may flag fewer."
		}
		return out, note, nil
	case "sequence":
		sigs, err := s.evalSequence(ctx, r, from, to)
		if err != nil {
			return nil, "", err
		}
		for _, sig := range sigs {
			out = append(out, mark(previewHit{At: sig.last, Group: sig.group, Count: sig.n, Summary: sig.summary}))
		}
		return out, "The first match per group is shown.", nil
	}
	return nil, "", nil
}

// previewBuckets counts each group per window over the day. Threshold rules
// keep buckets at or over the bar; anomaly rules keep buckets at Factor times
// the median of the group's buckets before them.
func (s *Server) previewBuckets(ctx context.Context, r *detectionRule, from, to time.Time) ([]previewHit, error) {
	src := s.q.ruleSources()[r.Source]
	where, fargs, err := s.filterSQL(r.Source, r.Filter, timeRange{From: from, To: to})
	if err != nil {
		return nil, err
	}
	step := int64(r.window().Seconds())
	bucket := s.q.bucketOf(strconv.FormatInt(step, 10))
	groups := s.groupExprs(r)
	measure := "SUM(" + src.count + ")"
	if r.Distinct != "" {
		measure = "COUNT(DISTINCT " + s.q.fieldExpr(r.Source, r.Distinct) + ")"
	}
	sel := ""
	for i, g := range groups {
		sel += g + " AS g" + strconv.Itoa(i) + ", "
	}
	args := append(spanArgs(from, to), fargs...)
	sql := `SELECT ` + bucket + ` AS t, ` + sel + `SUM(` + src.count + `) AS n, ` + measure + ` AS m
FROM ` + src.table + ` WHERE created_at >= ? AND created_at < ? AND ` + where + `
GROUP BY ` + strings.Join(append([]string{bucket}, groups...), ", ")
	limit := 5000
	if r.Logic == "threshold" {
		sql += "\nHAVING " + measure + " >= ?"
		args = append(args, r.Threshold)
		limit = 500
	}
	sql += "\nORDER BY t LIMIT " + strconv.Itoa(limit)
	res, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	ng := len(groups)
	type cell struct {
		t    int64
		n, m int64
	}
	series := map[string][]cell{}
	vals := map[string]map[string]string{}
	var order []string
	for _, row := range res.Rows {
		if len(row) != ng+3 {
			continue
		}
		values := map[string]string{}
		for i, g := range r.GroupBy {
			values[g] = toString(row[i+1])
		}
		key := groupKey(r.GroupBy, values)
		if _, ok := vals[key]; !ok {
			vals[key] = values
			order = append(order, key)
		}
		series[key] = append(series[key], cell{toInt(row[0]), toInt(row[ng+1]), toInt(row[ng+2])})
	}
	var out []previewHit
	at := func(t int64) string { return time.Unix(t, 0).UTC().Format(tsLayout) }
	for _, key := range order {
		who := fallback(key, "everything")
		if r.Logic == "threshold" {
			for _, c := range series[key] {
				out = append(out, previewHit{At: at(c.t), Group: vals[key], Count: c.n,
					Summary: fmt.Sprintf("%d %s in %s for %s.", c.m, s.ruleUnit(r), fmtSpan(r.window()), who)})
			}
			continue
		}
		// Fill the buckets with nothing in them, so quiet stretches count.
		byT := map[int64]int64{}
		for _, c := range series[key] {
			byT[c.t] = c.m
		}
		start := from.Unix() / step * step
		var hist []float64
		for t := start; t < to.Unix(); t += step {
			m := byT[t]
			if len(hist) >= ruleWarmup && m >= int64(r.Threshold) {
				base := median(hist)
				if float64(m) >= r.Factor*max(base, 1) {
					out = append(out, previewHit{At: at(t), Group: vals[key], Count: m,
						Summary: fmt.Sprintf("%d %s in %s for %s, %.1f× the usual %.0f.", m, s.ruleUnit(r), fmtSpan(r.window()), who, float64(m)/max(base, 1), base)})
					continue
				}
			}
			hist = append(hist, float64(m))
			if len(hist) > baselineSamples {
				hist = hist[1:]
			}
		}
	}
	return out, nil
}
