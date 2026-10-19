package analytics

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Security metrics for alert rules:
//
//   - security_events: findings in the window (SUM(count), so a row standing
//     for 12 failed SSH logins counts 12), narrowed by kind, minimum severity,
//     source and host, and fired per host, IP or account. updates_pending is
//     a level, not occurrences, so it reads each host's latest report.
//   - failed_logins: rejected sign-ins on login routes, from api_logs, with
//     the request scopes (route, method, service).

var securityScopes = []string{"event_kind", "event_severity", "source", "host"}

func (s *Server) securityMetrics() []metricDef {
	st := &s.security().st
	failed := st.loginCond(s.q) + " AND " + authFailCond(s.q)
	return []metricDef{
		{ID: "security_events", Label: "Security findings", Group: "security", Unit: "count",
			Description: "Occurrences of security findings (security_events) in the window, from the API detectors and the host agent. Fires per host, IP or account. For pending updates, the count in each host's latest report.",
			Scopes:      securityScopes, Op: ">=", Threshold: 1, UpIsBad: true, table: "security_events"},
		{ID: "failed_logins", Label: "Failed logins", Group: "security", Unit: "count",
			Description: "Rejected sign-ins on login routes (401, 403, or an invalid-credentials error key) over the window, across all IPs.",
			Scopes:      requestScopes, Op: ">", Threshold: 50, UpIsBad: true, table: "api_logs",
			agg: func(int64) string { return "COALESCE(" + countIf(failed) + ", 0)" }},
	}
}

// allMetrics is every metric a rule can watch.
func (s *Server) allMetrics() []metricDef {
	return append(append(alertMetrics(s.cfg.ApdexMs), s.securityMetrics()...), containerMetrics()...)
}

// securityClause narrows security_events to a rule's scope, bound.
func securityClause(sc alertScope) (string, []any) {
	var b strings.Builder
	var args []any
	if sc.EventKind != "" {
		b.WriteString(" AND kind = ?")
		args = append(args, sc.EventKind)
	}
	if levels := severitiesFrom(sc.EventSeverity); levels != nil {
		b.WriteString(" AND severity IN (" + strings.TrimSuffix(strings.Repeat("?, ", len(levels)), ", ") + ")")
		for _, l := range levels {
			args = append(args, l)
		}
	}
	if sc.Source != "" {
		b.WriteString(" AND source = ?")
		args = append(args, sc.Source)
	}
	if sc.Host != "" {
		b.WriteString(" AND host = ?")
		args = append(args, sc.Host)
	}
	return b.String(), args
}

// seriesKey is who a finding is about: the host, else the IP, else the actor.
const securitySeriesKey = `COALESCE(NULLIF(host, ''), NULLIF(ip, ''), NULLIF(actor, ''), '')`

// observeSecurity sums findings in the window per host, IP or account. With
// none, it reports one empty series at 0 so the rule shows as OK.
func (s *Server) observeSecurity(ctx context.Context, r alertRule, now time.Time) ([]observation, error) {
	w := specWindow(r.Spec)
	scope, sargs := securityClause(r.Spec.Scope)
	args := append([]any{now.Add(-w).UTC().Format(tsLayout), now.UTC().Format(tsLayout)}, sargs...)
	sql := `SELECT ` + securitySeriesKey + ` AS k, SUM(count) AS value, COUNT(*) AS n,
  MAX(CASE WHEN host IS NOT NULL AND host <> '' THEN 'host' WHEN ip IS NOT NULL AND ip <> '' THEN 'IP' ELSE 'account' END) AS what,
  MIN(kind) AS kind, COUNT(DISTINCT kind) AS kinds
FROM security_events WHERE created_at >= ? AND created_at < ?` + scope + `
GROUP BY ` + securitySeriesKey + `
ORDER BY value DESC
LIMIT 50`
	if r.Spec.Scope.EventKind == "updates_pending" {
		// A count of updates waiting, not occurrences: each host's latest
		// report in the window counts, so a host that caught up resolves.
		sql = `SELECT e.host AS k, MAX(e.count) AS value, COUNT(*) AS n, 'host' AS what, 'updates_pending' AS kind, 1 AS kinds
FROM security_events AS e
JOIN (SELECT host, MAX(created_at) AS last_at FROM security_events
      WHERE created_at >= ? AND created_at < ?` + scope + ` GROUP BY host) AS l
  ON l.host = e.host AND l.last_at = e.created_at
WHERE e.kind = 'updates_pending'
GROUP BY e.host
ORDER BY value DESC
LIMIT 50`
	}
	res, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		if missingTable(err, "security_events") {
			return nil, nil
		}
		return nil, err
	}
	win := fallback(r.Spec.Window, "5m")
	var out []observation
	for _, row := range res.Rows {
		if len(row) != 6 {
			continue
		}
		key := toString(row[0])
		v, _ := cellFloat(row[1])
		kinds := strings.ReplaceAll(toString(row[4]), "_", " ")
		if n := toInt(row[5]); n > 1 {
			kinds += " and " + strconv.FormatInt(n-1, 10) + " other kind(s)"
		}
		label := key
		if key != "" {
			label = toString(row[3]) + " " + key
		}
		ob := observation{Key: key, Label: fallback(label, "all findings"), Value: ptr(v), Threshold: ptr(r.Spec.Threshold),
			Breach: compare(r.Spec.Op, v, r.Spec.Threshold)}
		ob.Message = fmt.Sprintf("%s security finding occurrence(s) (%s) for %s over the last %s (threshold %s %s).",
			formatUnit(v, "count"), kinds, ob.Label, win, r.Spec.Op, formatUnit(r.Spec.Threshold, "count"))
		if r.Spec.Scope.EventKind == "updates_pending" {
			ob.Message = fmt.Sprintf("%s update(s) pending on %s in its latest report (threshold %s %s).",
				formatUnit(v, "count"), ob.Label, r.Spec.Op, formatUnit(r.Spec.Threshold, "count"))
		}
		out = append(out, ob)
	}
	if len(out) == 0 {
		zero := 0.0
		out = append(out, observation{Key: "", Label: "all findings", Value: &zero, Threshold: ptr(r.Spec.Threshold),
			Breach:  compare(r.Spec.Op, 0, r.Spec.Threshold),
			Message: "No matching security findings over the last " + win + "."})
	}
	return out, nil
}

// previewSecurity charts matching findings per hour over the day, by kind.
func (s *Server) previewSecurity(ctx context.Context, sp ruleSpec, day []timeSpan, tr timeRange) (*Result, error) {
	where, args := spansClause(day)
	scope, sargs := securityClause(sp.Scope)
	t := s.q.bucketOf("3600")
	res, err := s.db.Query(ctx, `SELECT `+t+` AS t, kind AS series, SUM(count) AS value
FROM security_events WHERE `+where+scope+` GROUP BY `+t+`, kind ORDER BY t`, append(args, sargs...)...)
	if err != nil {
		return nil, err
	}
	tr.Bucket = 3600
	return newResult(res, s.cfg.MaxRows, "", tr), nil
}

// checkSecurityScope validates the security_events scope fields.
func checkSecurityScope(sc alertScope) error {
	if sc.EventKind != "" && !findingKind.MatchString(sc.EventKind) {
		return badInput("finding kind must be a name like auth_bruteforce")
	}
	if sc.EventSeverity != "" && severitiesFrom(sc.EventSeverity) == nil {
		return badInput("minimum severity must be info, warning or critical")
	}
	if sc.Source != "" && sc.Source != "api" && sc.Source != "host" {
		return badInput("source must be api or host")
	}
	if len(sc.Host) > 255 {
		return badInput("host is too long")
	}
	return nil
}
