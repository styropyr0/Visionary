package analytics

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"
)

// Anomaly rules compare the current window with what is normal for this time
// of the week: the same hour of the same weekday over the previous four weeks,
// or the previous 24 hours while there is less history. The score is robust
// (median and MAD), so one past incident does not widen what counts as normal.

// sensitivityScore is the robust z-score a reading must pass. 3.5 is the
// usual outlier cutoff for MAD scores (Iglewicz and Hoaglin).
var sensitivityScore = map[string]float64{"low": 5, "medium": 3.5, "high": 2.5}

const (
	seasonalWeeks     = 4
	seasonalHalfWidth = time.Hour // around the same time of day
	minSeasonalWeeks  = 2
	minBaselinePoints = 6
)

type baseline struct {
	Source  string   `json:"source"` // seasonal or 24h
	Median  float64  `json:"median"`
	Scale   float64  `json:"scale"` // 1.4826 × MAD, floored
	Points  int      `json:"points"`
	Weeks   int      `json:"weeks,omitempty"`
	Score   float64  `json:"score"`
	Cutoff  float64  `json:"cutoff"`
	Upper   float64  `json:"upper"`
	Lower   float64  `json:"lower"`
	Current *float64 `json:"current"`
}

func anomalyDirection(m metricDef, sp ruleSpec) string {
	switch sp.Direction {
	case "up", "down", "both":
		return sp.Direction
	}
	if !m.UpIsBad {
		return "both"
	}
	return "up"
}

func anomalyWindow(sp ruleSpec) time.Duration {
	w := specWindow(sp)
	if sp.Window == "" {
		w = 15 * time.Minute
	}
	return min(max(w, 5*time.Minute), 2*time.Hour)
}

func (s *Server) observeAnomaly(ctx context.Context, r alertRule, now time.Time) ([]observation, error) {
	b, m, err := s.anomalyBaseline(ctx, r, now)
	if err != nil {
		return nil, err
	}
	ob := observation{Value: b.Current}
	where := describeScope(r.Spec.Scope, "")
	switch {
	case b.Current == nil:
		ob.Message = m.Label + where + ": no data in the current window."
	case b.Points == 0:
		ob.Message = m.Label + where + ": not enough history for a baseline yet."
	default:
		dir := anomalyDirection(m, r.Spec)
		up := b.Score >= b.Cutoff && dir != "down"
		down := b.Score <= -b.Cutoff && dir != "up"
		ob.Breach = up || down
		th := b.Upper
		if b.Score < 0 && dir != "up" {
			th = b.Lower
		}
		ob.Threshold = ptr(th)
		src := "the same hour on the previous " + fmt.Sprint(b.Weeks) + " weeks"
		if b.Source == "24h" {
			src = "the previous 24 hours"
		}
		ob.Message = fmt.Sprintf("%s%s is %s over the last %s; normal is about %s (median of %s, %d samples). Robust score %.1f, cutoff ±%.1f (%s sensitivity).",
			m.Label, where, formatUnit(*b.Current, m.Unit), fmtSpan(anomalyWindow(r.Spec)), formatUnit(b.Median, m.Unit), src, b.Points,
			b.Score, b.Cutoff, fallback(r.Spec.Sensitivity, "medium"))
	}
	return []observation{ob}, nil
}

// anomalyBaseline computes the current value, the baseline and the score.
func (s *Server) anomalyBaseline(ctx context.Context, r alertRule, now time.Time) (baseline, metricDef, error) {
	m, ok := s.metricByID(r.Spec.Metric)
	if !ok || !m.Anomaly {
		return baseline{}, m, errors.New("anomaly rules watch request rate, p95 latency, 5xx rate or host CPU")
	}
	w := anomalyWindow(r.Spec)
	bucket := int64(w.Seconds())
	b := baseline{Cutoff: sensitivityScore[fallback(r.Spec.Sensitivity, "medium")]}
	if b.Cutoff == 0 {
		b.Cutoff = sensitivityScore["medium"]
	}

	sql, args := s.valueSQL(m, r.Spec.Scope, timeSpan{now.Add(-w), now}, false)
	res, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		return b, m, err
	}
	if len(res.Rows) == 1 {
		if v, ok := cellFloat(res.Rows[0][1]); ok && toInt(res.Rows[0][2]) >= m.minRequests {
			b.Current = ptr(v)
		}
	}

	// Seasonal: buckets within an hour of now, on each of the past weeks.
	var spans []timeSpan
	for k := 1; k <= seasonalWeeks; k++ {
		at := now.Add(-time.Duration(k) * 7 * 24 * time.Hour)
		spans = append(spans, timeSpan{floorTo(at.Add(-seasonalHalfWidth), w), floorTo(at.Add(seasonalHalfWidth), w)})
	}
	pts, err := s.metricSeries(ctx, m, r.Spec.Scope, spans, bucket, false)
	if err != nil {
		return b, m, err
	}
	values, weeks := seasonalValues(pts, spans, bucket, m.ID == "requests_per_min")
	b.Source, b.Weeks = "seasonal", weeks
	if weeks < minSeasonalWeeks || len(values) < minBaselinePoints {
		day := []timeSpan{{floorTo(now.Add(-24*time.Hour), w), floorTo(now.Add(-w), w)}}
		pts, err = s.metricSeries(ctx, m, r.Spec.Scope, day, bucket, false)
		if err != nil {
			return b, m, err
		}
		values, _ = seasonalValues(pts, day, bucket, m.ID == "requests_per_min")
		b.Source, b.Weeks = "24h", 0
	}
	if len(values) < minBaselinePoints {
		return b, m, nil
	}
	b.Points = len(values)
	b.Median, b.Scale = robustStats(values, scaleFloor(m))
	b.Upper = b.Median + b.Cutoff*b.Scale
	b.Lower = math.Max(0, b.Median-b.Cutoff*b.Scale)
	if b.Current != nil {
		b.Score = (*b.Current - b.Median) / b.Scale
	}
	return b, m, nil
}

// seasonalValues turns bucket points into baseline samples and counts the
// spans (weeks) that had data. Request rate counts empty buckets as zero
// inside a span that has traffic at all: a quiet bucket is real data.
func seasonalValues(pts []point, spans []timeSpan, bucket int64, fillZero bool) ([]float64, int) {
	var values []float64
	weeks := 0
	for _, sp := range spans {
		from, to := sp.From.Unix(), sp.To.Unix()
		have := map[int64]float64{}
		for _, p := range pts {
			if p.T >= from && p.T < to {
				have[p.T] = p.Value
			}
		}
		if len(have) == 0 {
			continue
		}
		weeks++
		if !fillZero {
			for _, v := range have {
				values = append(values, v)
			}
			continue
		}
		for t := from - from%bucket; t < to; t += bucket {
			values = append(values, have[t])
		}
	}
	return values, weeks
}

// robustStats returns the median and 1.4826 × MAD (comparable to a standard
// deviation for normal data), never below floor so a flat history does not
// turn every small wobble into an anomaly.
func robustStats(values []float64, floor float64) (float64, float64) {
	med := median(values)
	dev := make([]float64, len(values))
	for i, v := range values {
		dev[i] = math.Abs(v - med)
	}
	scale := 1.4826 * median(dev)
	return med, math.Max(scale, math.Max(floor, 0.05*math.Abs(med)))
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// scaleFloor is the smallest spread that counts, in the metric's unit.
func scaleFloor(m metricDef) float64 {
	switch m.Unit {
	case "ms":
		return 5
	case "percent":
		return 0.5
	case "rpm":
		return 0.2
	}
	return 0.01
}

func floorTo(t time.Time, step time.Duration) time.Time {
	sec := int64(step.Seconds())
	u := t.Unix()
	return time.Unix(u-u%sec, 0).UTC()
}

func fmtSpan(d time.Duration) string {
	switch {
	case d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return d.String()
}
