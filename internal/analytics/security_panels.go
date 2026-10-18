package analytics

// Security panels: the fixed queries behind the Security page and the
// printable report. API panels read api_logs with the same login, probe and
// scanner conditions the detectors use; finding and host panels read
// security_events (all sources).

const secWindow = `created_at >= {{from}} AND created_at < {{to}}`

func securityPanels(q sqlDialect, st *securitySettings) map[string]string {
	login, fail, probe, agent := st.loginCond(q), authFailCond(q), probeCond(q), scannerAgentCond(q)
	failed := login + " AND " + fail
	blocked := "status IN (401, 403, 429)"
	// The newest updates_pending row of each host says what is waiting now.
	pending := `SELECT e.host, MAX(e.count) AS pending, MAX(e.created_at) AS reported_at
  FROM security_events AS e
  JOIN (SELECT host, MAX(created_at) AS last_at FROM security_events
        WHERE kind = 'updates_pending' AND ` + secWindow + ` GROUP BY host) AS l
    ON l.host = e.host AND l.last_at = e.created_at
  WHERE e.kind = 'updates_pending'
  GROUP BY e.host`
	return map[string]string{
		"summary": `
WITH f AS (
  SELECT severity, kind, source, ip, host, count FROM security_events WHERE ` + secWindow + `
),
a AS (
  SELECT COUNT(*) AS requests, ` + countIf(failed) + ` AS failed_logins, ` + countIf(blocked) + ` AS blocked
  FROM api_logs WHERE ` + window + `
),
p AS (` + pending + `)
SELECT
  (SELECT COUNT(*) FROM f) AS findings,
  (SELECT COUNT(*) FROM f WHERE severity = 'critical') AS critical,
  (SELECT COUNT(*) FROM f WHERE severity = 'warning') AS warning,
  (SELECT COUNT(*) FROM f WHERE severity = 'info') AS info,
  (SELECT COALESCE(failed_logins, 0) FROM a) AS failed_logins,
  (SELECT COUNT(DISTINCT ip) FROM f WHERE kind = 'scanner') AS scanner_ips,
  (SELECT requests FROM a) AS requests,
  (SELECT COALESCE(blocked, 0) FROM a) AS blocked,
  (SELECT ` + q.round("100.0 * blocked / NULLIF(requests, 0)", 2) + ` FROM a) AS blocked_rate,
  (SELECT COALESCE(SUM(count), 0) FROM f WHERE kind = 'ssh_failed') AS ssh_failures,
  (SELECT COUNT(*) FROM p WHERE pending > 0) AS pending_update_hosts,
  (SELECT COUNT(*) FROM host_inventory) AS hosts`,

		"failed_logins": `
SELECT {{t}} AS t,
  ` + countIf(fail) + ` AS failed,
  ` + countIf("status < 400") + ` AS succeeded
FROM api_logs WHERE ` + window + ` AND ` + login + `
GROUP BY t ORDER BY t`,

		"auth_errors": `
SELECT {{t}} AS t,
  ` + countIf("status = 401") + ` AS unauthorized_401,
  ` + countIf("status = 403") + ` AS forbidden_403,
  ` + countIf("status = 429") + ` AS rate_limited_429,
  ` + countIf("status >= 400 AND status < 500 AND status NOT IN (401, 403, 429)") + ` AS other_4xx
FROM api_logs WHERE ` + window + ` AND status >= 400 AND status < 500
GROUP BY t ORDER BY t`,

		// The IPs with the most failed logins, probes and auth errors.
		"top_ips": `
SELECT * FROM (
SELECT ip,
  COUNT(*) AS requests,
  ` + countIf(failed) + ` AS failed_logins,
  ` + countIf("status = 401") + ` AS unauthorized,
  ` + countIf("status = 403") + ` AS forbidden,
  ` + countIf(probe) + ` AS probes,
  ` + countIf("status >= 400 AND status < 500") + ` AS client_errors,
  COUNT(DISTINCT COALESCE(route, path)) AS paths,
  MAX(created_at) AS last_seen
FROM api_logs WHERE ` + window + ` AND ip IS NOT NULL AND ip <> ''
GROUP BY ip
HAVING ` + countIf("status >= 400 AND status < 500") + ` > 0
) AS x
ORDER BY failed_logins + probes DESC, client_errors DESC
LIMIT 20`,

		"probed_paths": `
SELECT path,
  COUNT(*) AS hits,
  COUNT(DISTINCT ip) AS ips,
  ` + countIf("status >= 200 AND status < 300") + ` AS answered_2xx,
  MAX(created_at) AS last_seen
FROM api_logs WHERE ` + window + ` AND ` + probe + `
GROUP BY path
ORDER BY hits DESC
LIMIT 20`,

		"top_agents": `
SELECT user_agent,
  COUNT(*) AS failures,
  COUNT(DISTINCT ip) AS ips,
  ` + countIf(failed) + ` AS failed_logins,
  MAX(CASE WHEN ` + agent + ` THEN 1 ELSE 0 END) AS scanner
FROM api_logs WHERE ` + window + ` AND status >= 400 AND status < 500
GROUP BY user_agent
ORDER BY failures DESC
LIMIT 15`,

		"findings_over_time": `
SELECT {{t}} AS t, severity AS series, COUNT(*) AS value
FROM security_events WHERE ` + secWindow + `
GROUP BY t, severity ORDER BY t`,

		"findings_by_kind": `
SELECT kind, source,
  COUNT(*) AS findings,
  SUM(count) AS occurrences,
  ` + countIf("severity = 'critical'") + ` AS critical,
  ` + countIf("severity = 'warning'") + ` AS warning,
  ` + countIf("severity = 'info'") + ` AS info,
  COUNT(DISTINCT ip) AS ips,
  COUNT(DISTINCT host) AS hosts,
  MAX(created_at) AS last_seen
FROM security_events WHERE ` + secWindow + `
GROUP BY kind, source
ORDER BY findings DESC`,

		// IPs behind the most findings, from any source.
		"finding_ips": `
SELECT ip,
  COUNT(*) AS findings,
  SUM(count) AS occurrences,
  ` + countIf("severity = 'critical'") + ` AS critical,
  COUNT(DISTINCT kind) AS kinds,
  MAX(created_at) AS last_seen
FROM security_events WHERE ` + secWindow + ` AND ip IS NOT NULL AND ip <> ''
GROUP BY ip
ORDER BY critical DESC, findings DESC
LIMIT 20`,

		// One row per host the agent knows or that reported an event.
		"host_status": `
WITH hosts AS (
  SELECT host FROM host_inventory
  UNION
  SELECT host FROM security_events WHERE source = 'host' AND host IS NOT NULL AND ` + secWindow + `
),
e AS (
  SELECT host,
    SUM(CASE WHEN kind = 'ssh_failed' THEN count ELSE 0 END) AS ssh_failures,
    SUM(CASE WHEN kind = 'ssh_login' THEN count ELSE 0 END) AS ssh_logins,
    SUM(CASE WHEN kind = 'sudo' THEN count ELSE 0 END) AS sudo,
    SUM(CASE WHEN kind = 'port_opened' THEN count ELSE 0 END) AS ports_opened,
    SUM(CASE WHEN kind = 'port_closed' THEN count ELSE 0 END) AS ports_closed,
    SUM(CASE WHEN kind = 'reboot' THEN count ELSE 0 END) AS reboots,
    MAX(created_at) AS last_event
  FROM security_events WHERE source = 'host' AND ` + secWindow + `
  GROUP BY host
),
p AS (` + pending + `)
SELECT h.host,
  COALESCE(e.ssh_failures, 0) AS ssh_failures,
  COALESCE(e.ssh_logins, 0) AS ssh_logins,
  COALESCE(e.sudo, 0) AS sudo,
  COALESCE(e.ports_opened, 0) AS ports_opened,
  COALESCE(e.ports_closed, 0) AS ports_closed,
  COALESCE(e.reboots, 0) AS reboots,
  p.pending AS pending_updates,
  e.last_event
FROM hosts AS h
LEFT JOIN e ON e.host = h.host
LEFT JOIN p ON p.host = h.host
ORDER BY COALESCE(e.ssh_failures, 0) DESC, h.host
LIMIT 200`,

		"ssh_failures": `
SELECT {{t}} AS t, host AS series, SUM(count) AS value
FROM security_events WHERE ` + secWindow + ` AND kind = 'ssh_failed'
GROUP BY t, host ORDER BY t`,

		// Host events worth reading one by one (failed SSH logins are counted
		// above instead).
		"host_events": `
SELECT id, created_at, host, kind, severity, actor, ip, target, count
FROM security_events
WHERE source = 'host' AND ` + secWindow + ` AND kind <> 'ssh_failed'
ORDER BY created_at DESC
LIMIT 100`,
	}
}
