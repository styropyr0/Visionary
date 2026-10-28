package analytics

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

func logMetrics() []metricDef {
	fixed := func(sql string) func(int64) string { return func(int64) string { return sql } }
	logScopes := []string{"service"}
	lines := metricDef{ID: "log_lines", Label: "Log lines", Group: "logs", Unit: "count",
		Description: "Application log lines in the window. The anomaly mode catches floods and logs that stop.",
		Scopes:      logScopes, Op: ">", Threshold: 100000, UpIsBad: true, table: "app_logs", agg: fixed("COUNT(*)"), zeroFill: true, Anomaly: true}
	errLines := metricDef{ID: "log_error_lines", Label: "Error log lines", Group: "logs", Unit: "count",
		Description: "Log lines at error or fatal in the window.",
		Scopes:      logScopes, Op: ">", Threshold: 50, UpIsBad: true, table: "app_logs", filter: "severity >= 5",
		agg: fixed("COALESCE(COUNT(*), 0)"), zeroFill: true, Anomaly: true}
	return []metricDef{
		{ID: "log_matches", Label: "Log lines matching a search", Group: "logs", Unit: "count",
			Description: "Lines matching the rule's search in the window, e.g. level:error service:checkout, or pattern:<id> from a line.",
			Scopes:      logScopes, Op: ">", Threshold: 0, UpIsBad: true},
		{ID: "log_absent", Label: "Expected log line missing", Group: "logs", Unit: "count",
			Description: "Lines matching the rule's search in the window; < 1 alerts when a line that should appear (a job's \"done\") did not.",
			Scopes:      logScopes, Op: "<", Threshold: 1, UpIsBad: false},
		{ID: "log_new_patterns", Label: "New log messages", Group: "logs", Unit: "count",
			Description: "Message templates at warn or worse seen for the first time in the window.",
			Scopes:      logScopes, Op: ">", Threshold: 0, UpIsBad: true},
		lines,
		errLines,
	}
}

func logQueryMetric(id string) bool { return id == "log_matches" || id == "log_absent" }

func (s *Server) observeLogMatches(ctx context.Context, r alertRule, now time.Time) ([]observation, error) {
	w := specWindow(r.Spec)
	cq, err := compileLogQuery(r.Spec.Query, s.q, s.onD1())
	if err != nil {
		return nil, err
	}
	where := "created_at >= ? AND created_at < ? AND " + cq.where
	args := append([]any{now.Add(-w).UTC().Format(tsLayout), now.UTC().Format(tsLayout)}, cq.args...)
	if r.Spec.Scope.Service != "" {
		where += " AND service = ?"
		args = append(args, r.Spec.Scope.Service)
	}
	res, err := s.db.Query(ctx, `SELECT COUNT(*) FROM app_logs WHERE `+where, args...)
	if err != nil {
		if missingTable(err, "app_logs") {
			return nil, nil
		}
		return nil, err
	}
	v := float64(0)
	if len(res.Rows) > 0 {
		v = float64(toInt(res.Rows[0][0]))
	}
	ob := observation{Value: &v, Threshold: ptr(r.Spec.Threshold), Breach: compare(r.Spec.Op, v, r.Spec.Threshold)}
	what := "line(s) matching " + quoteSearch(r.Spec.Query)
	if r.Spec.Scope.Service != "" {
		what += " in " + r.Spec.Scope.Service
	}
	ob.Message = fmt.Sprintf("%s %s over the last %s (threshold %s %s).", formatUnit(v, "count"), what, fallback(r.Spec.Window, "5m"),
		r.Spec.Op, formatUnit(r.Spec.Threshold, "count"))
	if v > 0 {
		ob.Message += s.sampleLines(ctx, where, args)
	}
	return []observation{ob}, nil
}

func quoteSearch(q string) string {
	if strings.TrimSpace(q) == "" {
		return "any search"
	}
	return "`" + q + "`"
}

func (s *Server) sampleLines(ctx context.Context, where string, args []any) string {
	res, err := s.db.Query(ctx, `SELECT level, service, message FROM app_logs WHERE `+where+` ORDER BY created_at DESC, id DESC LIMIT 3`, args...)
	if err != nil || len(res.Rows) == 0 {
		return ""
	}
	var parts []string
	for _, row := range res.Rows {
		line := strings.ToUpper(toString(row[0])) + " " + truncate(firstLine(toString(row[2])), 160)
		if svc := toString(row[1]); svc != "" {
			line = svc + ": " + line
		}
		parts = append(parts, line)
	}
	return " Latest: " + strings.Join(parts, " | ")
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// observeLogNewPatterns counts warn-or-worse templates first seen in the window.
func (s *Server) observeLogNewPatterns(ctx context.Context, r alertRule, now time.Time) ([]observation, error) {
	w := specWindow(r.Spec)
	args := []any{now.Add(-w).UTC().Format(tsLayout), now.UTC().Format(tsLayout)}
	where := "first_seen >= ? AND first_seen < ? AND severity >= 4"
	if r.Spec.Scope.Service != "" {
		where += " AND service = ?"
		args = append(args, r.Spec.Scope.Service)
	}
	res, err := s.db.Query(ctx, `SELECT service, level, template, lines FROM log_patterns WHERE `+where+` ORDER BY severity DESC, lines DESC LIMIT 20`, args...)
	if err != nil {
		if missingTable(err, "log_patterns") {
			return nil, nil
		}
		return nil, err
	}
	v := float64(len(res.Rows))
	ob := observation{Value: &v, Threshold: ptr(r.Spec.Threshold), Breach: compare(r.Spec.Op, v, r.Spec.Threshold)}
	ob.Message = fmt.Sprintf("%s new log message(s) at warn or worse over the last %s (threshold %s %s).", formatUnit(v, "count"),
		fallback(r.Spec.Window, "5m"), r.Spec.Op, formatUnit(r.Spec.Threshold, "count"))
	var names []string
	for _, row := range head(res.Rows, 3) {
		names = append(names, fmt.Sprintf("%s %s: %s (%d×)", toString(row[0]), strings.ToUpper(toString(row[1])), truncate(toString(row[2]), 100), toInt(row[3])))
	}
	if len(names) > 0 {
		ob.Message += " " + strings.Join(names, "; ") + "."
	}
	return []observation{ob}, nil
}

var patternTerm = regexp.MustCompile(`\bpattern:([0-9a-f]{16})\b`)

func (s *Server) logAlertPatterns(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	rules, err := s.alertEngine().enabledRules(ctx)
	if err != nil {
		return out
	}
	for _, r := range rules {
		if logQueryMetric(r.Spec.Metric) {
			for _, m := range patternTerm.FindAllStringSubmatch(r.Spec.Query, -1) {
				out[m[1]] = true
			}
		}
	}
	return out
}

func (s *Server) previewLogQuery(ctx context.Context, sp ruleSpec, day timeSpan, tr timeRange) (*Result, error) {
	cq, err := compileLogQuery(sp.Query, s.q, s.onD1())
	if err != nil {
		return nil, err
	}
	t := s.q.bucketOf("3600")
	where := "created_at >= ? AND created_at < ? AND " + cq.where
	args := append([]any{day.From.UTC().Format(tsLayout), day.To.UTC().Format(tsLayout)}, cq.args...)
	if sp.Scope.Service != "" {
		where += " AND service = ?"
		args = append(args, sp.Scope.Service)
	}
	res, err := s.db.Query(ctx, `SELECT `+t+` AS t, '' AS series, COUNT(*) AS value FROM app_logs WHERE `+where+` GROUP BY `+t+` ORDER BY t`, args...)
	if err != nil {
		return nil, err
	}
	tr.Bucket = 3600
	return newResult(res, s.maxRows(), "", tr), nil
}
