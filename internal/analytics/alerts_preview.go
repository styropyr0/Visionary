package analytics

import (
	"context"
	"net/http"
	"strconv"
	"time"
)

// The rule editor previews the metric over the last 24 hours, with the line
// the rule would compare it against, and what the rule would say right now.

const previewSpan = 24 * time.Hour

type previewResponse struct {
	Unit      string        `json:"unit"`
	Result    *Result       `json:"result"`
	Threshold *float64      `json:"threshold"`
	Lower     *float64      `json:"lower,omitempty"`
	Baseline  *baseline     `json:"baseline,omitempty"`
	SLO       *sloStatus    `json:"slo,omitempty"`
	Current   []observation `json:"current"`
	Note      string        `json:"note,omitempty"`
}

func (s *Server) handlePreviewRule(w http.ResponseWriter, r *http.Request) {
	var in ruleInput
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	in.Name, in.Channels = fallback(in.Name, "Preview"), nil
	rule, err := s.checkRule(r.Context(), in)
	if err != nil {
		s.fail(w, err)
		return
	}
	now := time.Now().UTC()
	out, err := s.preview(r.Context(), rule, now)
	if err != nil {
		s.fail(w, dbErr(err))
		return
	}
	if out.Current, err = s.observe(r.Context(), rule, now); err != nil {
		s.fail(w, dbErr(err))
		return
	}
	if out.Current == nil {
		out.Current = []observation{}
	}
	writeJSON(w, http.StatusOK, out)
}

func previewBucket(w time.Duration) int64 {
	b := int64(w.Seconds())
	return min(max(b, 300), 3600)
}

func (s *Server) preview(ctx context.Context, rule alertRule, now time.Time) (*previewResponse, error) {
	sp := rule.Spec
	day := []timeSpan{{now.Add(-previewSpan), now}}
	out := &previewResponse{Unit: "none"}
	tr := timeRange{From: day[0].From, To: now}

	switch {
	case rule.Kind == "slo":
		out.Unit = "percent"
		bucket := int64(3600)
		scope, sargs := scopeClause(sloMetric, sp.Scope)
		where, args := spansClause(day)
		t := s.q.bucketOf(strconv.FormatInt(bucket, 10))
		res, err := s.db.Query(ctx, `SELECT `+t+` AS t, '' AS series, 100.0 * `+countIf("NOT "+s.sloBad())+` / NULLIF(COUNT(*), 0) AS value
FROM api_logs WHERE `+where+scope+` GROUP BY `+t+` ORDER BY t`, append(args, sargs...)...)
		if err != nil {
			return nil, err
		}
		tr.Bucket = bucket
		out.Result = newResult(res, s.cfg.MaxRows, "", tr)
		st, err := s.sloStatus(ctx, rule, now)
		if err != nil {
			return nil, err
		}
		out.SLO, out.Threshold = st, ptr(st.Target)
		out.Note = "Share of requests per hour that succeeded within " + strconv.Itoa(s.cfg.ApdexMs) + " ms, against the target."

	case rule.Kind == "anomaly":
		b, m, err := s.anomalyBaseline(ctx, rule, now)
		if err != nil {
			return nil, err
		}
		out.Unit = m.Unit
		bucket := int64(anomalyWindow(sp).Seconds())
		pts, err := s.metricSeries(ctx, m, sp.Scope, day, bucket, false)
		if err != nil {
			return nil, err
		}
		tr.Bucket = bucket
		out.Result = pointsResult(pts, tr)
		out.Baseline = &b
		if b.Points > 0 {
			dir := anomalyDirection(m, sp)
			if dir != "down" {
				out.Threshold = ptr(b.Upper)
			}
			if dir != "up" {
				out.Lower = ptr(b.Lower)
			}
			out.Note = "Dashed lines are the band of normal for this time of week; readings outside it for the \"for\" duration fire."
		} else {
			out.Note = "Not enough history for a baseline yet: anomaly rules need at least six past windows."
		}

	case sp.Metric == "custom":
		res, err := s.runTemplate(ctx, sp.SQL, timeRange{From: tr.From, To: now, Bucket: previewBucket(specWindow(sp))}, false)
		if err != nil {
			return nil, err
		}
		out.Threshold = ptr(sp.Threshold)
		if ti, vi := colIndex(res, "t"), colIndex(res, "value"); ti >= 0 && vi >= 0 {
			var pts []point
			for _, row := range res.Rows {
				if v, ok := cellFloat(row[vi]); ok {
					pts = append(pts, point{T: toInt(row[ti]), Value: v})
				}
			}
			tr.Bucket = res.Bucket
			out.Result = pointsResult(pts, tr)
		} else {
			out.Note = "The query returns a single number, so there is no history to chart; return t and value columns to see one."
		}

	case sp.Metric == "instance_silent":
		out.Unit, out.Threshold = "minutes", ptr(sp.Threshold)
		out.Note = "Each instance's minutes since its last sample are below; this rule has no history to chart."

	case sp.Metric == "security_events":
		out.Unit, out.Threshold = "count", ptr(sp.Threshold)
		res, err := s.previewSecurity(ctx, sp, day, tr)
		if err != nil {
			return nil, err
		}
		out.Result = res
		out.Note = "Matching findings per hour, by kind. The rule sums them over its window per host, IP or account."

	case sp.Metric == "container_events":
		out.Unit, out.Threshold = "count", ptr(sp.Threshold)
		res, err := s.previewContainerEvents(ctx, sp, day, tr)
		if err != nil {
			if missingTable(err, "container_events") {
				out.Note = "No container events yet: the host agent reports them from Docker or Podman."
				return out, nil
			}
			return nil, err
		}
		out.Result = res
		out.Note = "Matching container events per hour, by kind. The rule sums them over its window per container."

	case sp.Metric == "uptime_failing":
		out.Unit, out.Threshold = "count", ptr(sp.Threshold)
		where, args := spansClause(day)
		t := s.q.bucketOf("3600")
		res, err := s.db.Query(ctx, `SELECT `+t+` AS t, check_name AS series, `+countIf("ok = 0")+` AS value
FROM uptime_results WHERE `+where+` GROUP BY `+t+`, check_name ORDER BY t`, args...)
		if err != nil {
			if missingTable(err, "uptime_results") {
				out.Note = "No uptime results yet: add an uptime check first."
				return out, nil
			}
			return nil, err
		}
		tr.Bucket = 3600
		out.Result = newResult(res, s.cfg.MaxRows, "", tr)
		out.Note = "Failed checks per hour. The rule counts consecutive failures, newest first."

	default:
		m, ok := s.metricByID(sp.Metric)
		if !ok {
			return out, nil
		}
		out.Unit, out.Threshold = m.Unit, ptr(sp.Threshold)
		bucket := previewBucket(specWindow(sp))
		pts, err := s.metricSeries(ctx, m, sp.Scope, day, bucket, m.perInstance)
		if err != nil {
			if missingTable(err, "system_metrics") {
				out.Note = errNoSystemMetrics
				return out, nil
			}
			return nil, err
		}
		tr.Bucket = bucket
		out.Result = pointsResult(pts, tr)
		if int64(specWindow(sp).Seconds()) != bucket {
			out.Note = "Charted in " + fmtSpan(time.Duration(bucket)*time.Second) + " buckets; the rule itself looks at the last " + sp.Window + " every minute."
		}
	}
	return out, nil
}

func pointsResult(pts []point, tr timeRange) *Result {
	rows := make([][]any, 0, len(pts))
	for _, p := range pts {
		rows = append(rows, []any{p.T, p.Series, p.Value})
	}
	return &Result{Columns: []string{"t", "series", "value"}, Rows: rows, Bucket: tr.Bucket,
		From: tr.From.Format(time.RFC3339Nano), To: tr.To.Format(time.RFC3339Nano)}
}

func colIndex(res *Result, name string) int {
	for i, c := range res.Columns {
		if c == name {
			return i
		}
	}
	return -1
}
