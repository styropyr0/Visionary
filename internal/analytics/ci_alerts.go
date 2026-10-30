package analytics

// Alert metrics over CI: how often pipelines fail and how long they take,
// per workflow, how long jobs wait for a runner, and tests newly found flaky.

func ciMetrics() []metricDef {
	fixed := func(sql string) func(int64) string { return func(int64) string { return sql } }
	finished := "status = 'completed' AND conclusion IN ('success', 'failure', 'timed_out')"
	return []metricDef{
		{ID: "ci_failure_rate", Label: "Pipeline failure rate", Group: "ci", Unit: "percent",
			Description: "Share of finished pipeline runs (queued in the window) that failed or timed out; cancelled runs don't count. Fires per workflow.",
			Scopes:      []string{"service"}, Op: ">", Threshold: 20, UpIsBad: true, table: "ci_pipelines", perInstance: true, keyCol: "workflow",
			filter: finished, agg: fixed("100.0 * " + countIf("conclusion <> 'success'") + " / NULLIF(COUNT(*), 0)")},
		{ID: "ci_duration_p95", Label: "Pipeline duration (p95)", Group: "ci", Unit: "ms",
			Description: "95th percentile of how long finished pipeline runs took, from start to finish, over the window.",
			Scopes:      []string{"service"}, Op: ">", Threshold: 900000, UpIsBad: true, table: "ci_pipelines",
			filter: "status = 'completed' AND duration_ms IS NOT NULL", pct: 0.95, pctCol: "duration_ms"},
		{ID: "ci_queue_p95", Label: "CI job queue time (p95)", Group: "ci", Unit: "ms",
			Description: "95th percentile of how long jobs waited for a runner over the window.",
			Scopes:      []string{"service"}, Op: ">", Threshold: 300000, UpIsBad: true, table: "ci_jobs",
			filter: "queue_ms IS NOT NULL", pct: 0.95, pctCol: "queue_ms"},
		{ID: "ci_new_flaky_tests", Label: "New flaky tests", Group: "ci", Unit: "count",
			Description: "Tests seen flaky for the first time in the window: passing and failing on one commit, or flip-flopping on the default branch.",
			Scopes:      []string{"service"}, Op: ">", Threshold: 0, UpIsBad: true, table: "ci_flaky_tests",
			agg: fixed("COUNT(*)"), zeroFill: true},
	}
}
