package analytics

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// A firing alert gets a short "likely cause" from the model. The analyst's
// own investigation streams to a browser and may run five queries, so this is
// a smaller, fixed one: three comparison queries of the 30 minutes before the
// alert against the same 30 minutes a day earlier, then one model call, all
// inside one 20-second budget. When anything fails the alert goes out
// without an explanation.

const (
	explainBudget = 20 * time.Second
	explainWindow = 30 * time.Minute
)

func (s *Server) explainAlert(parent context.Context, r alertRule, ev *alertEvent, at time.Time) string {
	if s.llm == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(parent, explainBudget)
	defer cancel()

	w := timeSpan{at.Add(-explainWindow), at}
	b := timeSpan{w.From.Add(-24 * time.Hour), w.To.Add(-24 * time.Hour)}
	where, args := spansClause([]timeSpan{w, b})
	inWindow := "created_at >= '" + w.From.UTC().Format(tsLayout) + "'"
	inBase := "created_at < '" + b.To.UTC().Format(tsLayout) + "'"

	queries := []struct{ title, sql string }{
		{"Traffic, latency and 5xx per route: alert window vs the same time yesterday", `SELECT COALESCE(route, path) AS route,
  ` + countIf(inWindow) + ` AS window_requests, ` + countIf(inBase) + ` AS baseline_requests,
  ` + s.q.round("AVG(CASE WHEN "+inWindow+" THEN duration_ms END)", 1) + ` AS window_avg_ms,
  ` + s.q.round("AVG(CASE WHEN "+inBase+" THEN duration_ms END)", 1) + ` AS baseline_avg_ms,
  MAX(CASE WHEN ` + inWindow + ` THEN duration_ms END) AS window_max_ms,
  ` + countIf(inWindow+" AND status >= 500") + ` AS window_5xx, ` + countIf(inBase+" AND status >= 500") + ` AS baseline_5xx
FROM api_logs WHERE ` + where + `
GROUP BY COALESCE(route, path) ORDER BY window_requests DESC LIMIT 15`},
		{"Top error keys and statuses: alert window vs yesterday", `SELECT COALESCE(error_key, ` + s.q.text("status") + `) AS error_key, status,
  ` + countIf(inWindow) + ` AS window_count, ` + countIf(inBase) + ` AS baseline_count
FROM api_logs WHERE ` + where + ` AND status >= 400
GROUP BY COALESCE(error_key, ` + s.q.text("status") + `), status ORDER BY window_count DESC LIMIT 10`},
		{"Server metrics per instance: alert window vs yesterday (window_goroutines counts goroutines in Go, threads or event-loop handles elsewhere; runtime says which)", `SELECT instance, MAX(runtime) AS runtime,
  ` + s.q.round("AVG(CASE WHEN "+inWindow+" THEN host_cpu_percent END)", 1) + ` AS window_cpu,
  ` + s.q.round("AVG(CASE WHEN "+inBase+" THEN host_cpu_percent END)", 1) + ` AS baseline_cpu,
  ` + s.q.round("MAX(CASE WHEN "+inWindow+" THEN host_mem_percent END)", 1) + ` AS window_mem_max,
  ` + s.q.round("SUM(CASE WHEN "+inWindow+" THEN db_acquire_wait_ms END)", 0) + ` AS window_db_wait_ms,
  ` + s.q.round("SUM(CASE WHEN "+inBase+" THEN db_acquire_wait_ms END)", 0) + ` AS baseline_db_wait_ms,
  MAX(CASE WHEN ` + inWindow + ` THEN db_acquired_conns END) AS window_db_in_use, MAX(db_max_conns) AS db_limit,
  MAX(CASE WHEN ` + inWindow + ` THEN goroutines END) AS window_goroutines, MAX(CASE WHEN ` + inBase + ` THEN goroutines END) AS baseline_goroutines
FROM system_metrics WHERE ` + where + `
GROUP BY instance ORDER BY window_cpu DESC LIMIT 10`},
	}

	var evidence strings.Builder
	for i, q := range queries {
		res, err := s.db.Query(ctx, q.sql, args...)
		if err != nil {
			if ctx.Err() != nil {
				return ""
			}
			evidence.WriteString(q.title + ": not available.\n\n")
			continue
		}
		evidence.WriteString(q.title + "\n")
		evidence.WriteString(describeResult(newResult(res, 40, "", timeRange{}), i+1))
		evidence.WriteString("\n")
	}

	prompt := `You explain production alerts for ` + s.cfg.AppName + `'s API. An alert just fired. You get three comparisons of the 30 minutes before it (window) with the same 30 minutes a day earlier (baseline).
Reply with one JSON object: {"cause": "<2 to 4 short lines, separated by \n>"}.
- Lead with the most likely cause, naming the route, error key, instance or resource that moved with the alert, with numbers.
- Say "likely", not certain. If the data does not point anywhere, say so in one line.
- Plain text, no markdown headings, no SQL.
- Readers are in different time zones: never write clock times; say "in the last 30 minutes", "since about 10 minutes before the alert" and so on.`
	alert := "Alert: " + r.Name + " (" + r.Kind + ", severity " + r.Severity + ")\n" + ev.Message +
		"\nFired at " + at.UTC().Format(tsLayout) + " UTC.\n\n" + evidence.String()
	content, err := s.llm.chatJSON(ctx, []chatMessage{{Role: "system", Content: prompt}, {Role: "user", Content: alert}})
	if err != nil {
		s.logger.Info("alerts: explanation skipped", "rule_id", r.ID, "err", userMessage(err))
		return ""
	}
	var out struct {
		Cause string `json:"cause"`
	}
	if json.Unmarshal([]byte(content), &out) != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(out.Cause), "\n")
	var kept []string
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			kept = append(kept, truncate(l, 300))
		}
	}
	return strings.Join(head(kept, 4), "\n")
}
