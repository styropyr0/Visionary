package analytics

// Alert rules, notification channels, anomaly detection, SLO burn alerts and
// digests. The pieces:
//
//	alerts_store.go    rules, series state and events in the database
//	alerts_metrics.go  built-in metrics and threshold evaluation
//	anomaly.go         seasonal baselines and robust scores
//	alerts_slo.go      SLO burn rates and error budgets
//	alerts_eval.go     the scheduler and state machine (alertsStart)
//	alerts_explain.go  the "likely cause" on a firing alert
//	notify*.go         Slack, Discord, webhook and email delivery
//	digest.go          the weekly digest
//	alerts_api.go, alerts_channels.go, alerts_preview.go  HTTP handlers (alertsRoutes)

func (s *Server) alertsSchema() []string { return []string{s.q.alertTables()} }

func (s *Server) alertsPrompt() string {
	return `Table alert_events: one row each time an alert rule starts firing or resolves. Use it to answer "what alerted and when", and to line incidents up with traffic, errors and server metrics.
- id INTEGER, created_at TEXT (same format as api_logs), rule_id INTEGER, rule_name TEXT
- kind TEXT: threshold (a metric against a fixed threshold), anomaly (against a seasonal baseline) or slo (error-budget burn rate)
- severity TEXT: info, warning or critical
- state TEXT: 'firing' when the alert started, 'resolved' when it cleared; pair a firing row with the next resolved row of the same rule_id and series_key for its duration
- series_key TEXT: the instance or uptime check the row is about, '' for rules with one series
- value REAL: the reading that triggered the change (the metric, or the burn rate for slo); threshold REAL: what it was compared with
- message TEXT: a sentence describing the reading; explanation TEXT: the likely cause written when it fired ('' when none)
- notified INTEGER: 1 when a notification was delivered`
}
