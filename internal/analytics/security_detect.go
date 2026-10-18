package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"runtime/debug"
	"sort"
	"strings"
	"time"
)

// The detectors run every VISIONARY_SECURITY_INTERVAL over the last
// VISIONARY_SECURITY_WINDOW of api_logs (ending securityLag ago), in two
// grouped queries that each read the window once through the created_at
// index: one row per IP, one per signed-in account. Only keys worth a look
// come back (HAVING), so the work in Go stays small.
//
// Deduplication: a finding is one row per (kind, ip, actor) per
// VISIONARY_SECURITY_DEDUPE (10m), written by the run that first sees it with
// the window's count. While the activity goes on, later runs add only the
// requests that arrived since the previous run, refresh detail and raise
// (never lower) the severity. Once the period is over, the next row of the
// same key counts only new requests, so SUM(count) over rows stays the number
// of occurrences.
//
// Severity: warning at the threshold, critical at 5× the threshold or when
// the attack may have worked: an IP that failed to sign in many times and then
// succeeded, or a probe path answered with 2xx. rate_abuse and
// forbidden_spike compare a key with its own recent windows (the median of
// the last hour, kept in memory), so they wait for warmupRuns runs after a
// start.

const (
	warmupRuns      = 10
	baselineSamples = 60
	severeFactor    = 5
	maxDetailBytes  = 4 << 10
)

type finding struct {
	kind, severity    string
	ip, actor, target string
	service           string
	n, nNew           int64 // occurrences in the window, and since the previous run
	detail            map[string]any
}

func (f finding) key() string { return f.kind + "|" + f.ip + "|" + f.actor }

var severityRank = map[string]int{"info": 1, "warning": 2, "critical": 3}

func maxSeverity(a, b string) string {
	if severityRank[b] > severityRank[a] {
		return b
	}
	return a
}

// grade is warning at the threshold and critical at severeFactor times it.
func grade(v int64, threshold int, worked bool) string {
	if worked || v >= int64(severeFactor*threshold) {
		return "critical"
	}
	return "warning"
}

func (s *Server) runDetectors(parent context.Context, now time.Time) {
	defer func() {
		if p := recover(); p != nil {
			s.logger.Error("security: detectors panicked", "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
		}
	}()
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	sec := s.security()
	sec.mu.Lock()
	cursor := sec.cursor
	sec.mu.Unlock()
	to := now.Add(-securityLag)
	from := to.Add(-sec.st.window)
	if cursor.Before(from) {
		cursor = from
	}
	found, err := s.detect(ctx, from, to, cursor)
	if err == nil {
		err = s.writeFindings(ctx, found, now)
	}
	sec.mu.Lock()
	sec.lastRun, sec.lastErr = now, ""
	if err != nil {
		sec.lastErr = userMessage(err)
	} else {
		sec.cursor = to
		sec.runs++
	}
	sec.mu.Unlock()
	if err != nil && ctx.Err() == nil && !missingTable(err, "api_logs") {
		s.logger.Warn("security: detectors failed", "err", err)
	}
}

// detect reads the window and returns what crossed a threshold.
func (s *Server) detect(ctx context.Context, from, to, cursor time.Time) ([]finding, error) {
	sec := s.security()
	st := &sec.st
	q := s.q
	login, fail, probe, agent := st.loginCond(q), authFailCond(q), probeCond(q), scannerAgentCond(q)
	failed := login + " AND " + fail
	isNew := "created_at >= ?"
	scan := "(" + probe + " OR " + agent + ")"
	cur := cursor.Format(tsLayout)
	span := []any{from.Format(tsLayout), to.Format(tsLayout)}
	minutes := st.window.Minutes()
	track := max(int64(float64(st.rateMin)*minutes)/4, 1)
	win := fmtSpan(st.window)

	sec.mu.Lock()
	warm := sec.runs >= warmupRuns
	sec.mu.Unlock()
	samples := map[string]float64{}
	flagged := map[string]bool{}
	var out []finding

	byIP, err := s.db.Query(ctx, `SELECT ip, MAX(service) AS service, COUNT(*) AS n, `+countIf(isNew)+` AS n_new,
  `+countIf(failed)+` AS fails, `+countIf(failed+" AND "+isNew)+` AS fails_new,
  `+countIf(login+" AND status < 400")+` AS login_ok,
  COUNT(DISTINCT CASE WHEN `+failed+` THEN COALESCE(`+accountExpr(q)+`, path) END) AS accounts,
  MAX(CASE WHEN `+failed+` THEN COALESCE(route, path) END) AS login_route,
  `+countIf(probe)+` AS probes, `+countIf(probe+" AND status >= 200 AND status < 300")+` AS probes_ok,
  `+countIf(agent)+` AS agent_hits, MAX(CASE WHEN `+agent+` THEN user_agent END) AS agent,
  `+countIf(scan)+` AS scans, `+countIf(scan+" AND "+isNew)+` AS scans_new,
  COUNT(DISTINCT COALESCE(route, path)) AS paths
FROM api_logs WHERE created_at >= ? AND created_at < ? AND ip IS NOT NULL AND ip <> ''
GROUP BY ip
HAVING COUNT(*) >= ? OR `+countIf(failed)+` >= ? OR `+countIf(scan)+` > 0
ORDER BY n DESC
LIMIT 500`, append([]any{cur, cur, cur}, append(span, track, min(st.bruteforce, st.stuffingAccounts))...)...)
	if err != nil {
		return nil, err
	}
	var scanners []string
	scanAt := map[string]int{}
	for _, row := range byIP.Rows {
		if len(row) != 16 {
			continue
		}
		ip := toString(row[0])
		if st.ignored(ip) {
			continue
		}
		service, n, nNew := toString(row[1]), toInt(row[2]), toInt(row[3])
		fails, failsNew, loginOK, accounts := toInt(row[4]), toInt(row[5]), toInt(row[6]), toInt(row[7])
		route := maskPath(toString(row[8]))
		probes, probesOK, agentHits, ua := toInt(row[9]), toInt(row[10]), toInt(row[11]), toString(row[12])
		scans, scansNew, paths := toInt(row[13]), toInt(row[14]), toInt(row[15])

		switch {
		case accounts >= int64(st.stuffingAccounts) && fails >= int64(st.stuffingAccounts):
			out = append(out, finding{kind: "credential_stuffing", severity: grade(accounts, st.stuffingAccounts, loginOK > 0),
				ip: ip, target: route, service: service, n: fails, nNew: failsNew,
				detail: map[string]any{"failures": fails, "accounts": accounts, "successes": loginOK, "window": win,
					"summary": fmt.Sprintf("%d failed sign-ins for %d different accounts from %s in %s%s.", fails, accounts, ip, win, succeeded(loginOK))}})
		case fails >= int64(st.bruteforce):
			out = append(out, finding{kind: "auth_bruteforce", severity: grade(fails, st.bruteforce, loginOK > 0),
				ip: ip, target: route, service: service, n: fails, nNew: failsNew,
				detail: map[string]any{"failures": fails, "successes": loginOK, "window": win,
					"summary": fmt.Sprintf("%d failed sign-ins from %s on %s in %s%s.", fails, ip, fallback(route, "login routes"), win, succeeded(loginOK))}})
		}

		if probes >= int64(st.scannerHits) || agentHits > 0 {
			sev := "warning"
			if probesOK > 0 {
				sev = "critical"
			}
			d := map[string]any{"probes": probes, "answered_2xx": probesOK, "scanner_agent_requests": agentHits, "window": win}
			summary := fmt.Sprintf("%s requested %d probe path(s) in %s", ip, probes, win)
			if ua != "" {
				d["user_agent"] = truncate(ua, 160)
				summary += fmt.Sprintf(" with the scanner user agent %q", truncate(ua, 60))
			}
			if probesOK > 0 {
				summary += fmt.Sprintf("; %d answered 2xx, check what they returned", probesOK)
			}
			d["summary"] = summary + "."
			scanAt[ip] = len(out)
			scanners = append(scanners, ip)
			out = append(out, finding{kind: "scanner", severity: sev, ip: ip, service: service, n: scans, nNew: scansNew, detail: d})
		}

		key := "ip:" + ip
		samples[key] = float64(n)
		if f, ok := s.rateFinding(sec, warm, key, ip, n, nNew, minutes); ok {
			f.ip, f.service = ip, service
			f.detail["paths"] = paths
			flagged[key] = true
			out = append(out, f)
		}
	}
	if err := s.scannerPaths(ctx, out, scanners, scanAt, span, probe); err != nil {
		return nil, err
	}

	acct := accountExpr(q)
	byUser, err := s.db.Query(ctx, `SELECT `+acct+` AS account, MAX(user_role) AS role, MAX(service) AS service,
  COUNT(*) AS n, `+countIf(isNew)+` AS n_new,
  `+countIf("status = 403")+` AS forbidden, `+countIf("status = 403 AND "+isNew)+` AS forbidden_new,
  COUNT(DISTINCT CASE WHEN status = 403 THEN COALESCE(route, path) END) AS forbidden_routes,
  `+countIf(failed)+` AS fails, `+countIf(failed+" AND "+isNew)+` AS fails_new,
  COUNT(DISTINCT CASE WHEN `+failed+` THEN ip END) AS fail_ips
FROM api_logs WHERE created_at >= ? AND created_at < ? AND (user_id IS NOT NULL OR (user_handle IS NOT NULL AND user_handle <> ''))
GROUP BY `+acct+`
HAVING COUNT(*) >= ? OR `+countIf("status = 403")+` > 0 OR COUNT(DISTINCT CASE WHEN `+failed+` THEN ip END) >= ?
ORDER BY n DESC
LIMIT 500`, append([]any{cur, cur, cur}, append(span, track, st.targetedIPs)...)...)
	if err != nil {
		return nil, err
	}
	type roleSum struct {
		forbidden, forbiddenNew int64
		users                   int
		service                 string
	}
	roles := map[string]*roleSum{}
	for _, row := range byUser.Rows {
		if len(row) != 11 {
			continue
		}
		who, role, service := toString(row[0]), toString(row[1]), toString(row[2])
		n, nNew := toInt(row[3]), toInt(row[4])
		forb, forbNew, forbRoutes := toInt(row[5]), toInt(row[6]), toInt(row[7])
		fails, failsNew, failIPs := toInt(row[8]), toInt(row[9]), toInt(row[10])
		if who == "" {
			continue
		}
		if failIPs >= int64(st.targetedIPs) {
			out = append(out, finding{kind: "account_targeted", severity: grade(failIPs, st.targetedIPs, false),
				actor: who, service: service, n: fails, nNew: failsNew,
				detail: map[string]any{"failures": fails, "ips": failIPs, "window": win,
					"summary": fmt.Sprintf("%d IPs failed to sign in as %s (%d attempts) in %s.", failIPs, who, fails, win)}})
		}
		key := "user:" + who
		samples[key] = float64(n)
		if f, ok := s.rateFinding(sec, warm, key, who, n, nNew, minutes); ok {
			f.actor, f.service = who, service
			flagged[key] = true
			out = append(out, f)
		}
		fkey := "forbidden:user:" + who
		samples[fkey] = float64(forb)
		if f, ok := s.forbiddenFinding(sec, warm, fkey, fmt.Sprintf("%s got %d forbidden (403) responses on %d route(s)", who, forb, forbRoutes), forb, forbNew); ok {
			f.actor, f.service = who, service
			f.detail["routes"] = forbRoutes
			f.detail["role"] = role
			flagged[fkey] = true
			out = append(out, f)
		}
		if role != "" && forb > 0 {
			r := roles[role]
			if r == nil {
				r = &roleSum{service: service}
				roles[role] = r
			}
			r.forbidden += forb
			r.forbiddenNew += forbNew
			r.users++
		}
	}
	for role, r := range roles {
		key := "forbidden:role:" + role
		samples[key] = float64(r.forbidden)
		// One user's spike is already reported for that user.
		if r.users < 2 {
			continue
		}
		if f, ok := s.forbiddenFinding(sec, warm, key, fmt.Sprintf("%d users with the role %s got %d forbidden (403) responses", r.users, role, r.forbidden), r.forbidden, r.forbiddenNew); ok {
			f.actor, f.service = "role:"+role, r.service
			f.detail["users"] = r.users
			flagged[key] = true
			out = append(out, f)
		}
	}
	sec.record(samples, flagged)
	return out, nil
}

func succeeded(n int64) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("; %d sign-in(s) from it succeeded", n)
}

// rateFinding flags a key far above its own baseline and the absolute floor.
func (s *Server) rateFinding(sec *securityState, warm bool, key, who string, n, nNew int64, minutes float64) (finding, bool) {
	st := &sec.st
	rpm := float64(n) / minutes
	if !warm || rpm < float64(st.rateMin) {
		return finding{}, false
	}
	base := sec.baselineOf(key)
	if float64(n) < st.rateFactor*base {
		return finding{}, false
	}
	usual := "with no traffic in its recent windows"
	if base > 0 {
		usual = fmt.Sprintf("%.0f× its usual %s/min", float64(n)/base, fnum(math.Round(base/minutes)))
	}
	win := fmtSpan(st.window)
	return finding{kind: "rate_abuse", severity: grade(int64(rpm), st.rateMin, false), n: n, nNew: nNew,
		detail: map[string]any{"requests": n, "rpm": math.Round(rpm), "baseline_rpm": math.Round(base / minutes), "window": win,
			"summary": fmt.Sprintf("%s made %s requests/min over %s, %s.", who, fnum(math.Round(rpm)), win, usual)}}, true
}

// forbiddenFinding flags a jump in 403s against the key's own baseline.
func (s *Server) forbiddenFinding(sec *securityState, warm bool, key, what string, forb, forbNew int64) (finding, bool) {
	st := &sec.st
	if !warm || forb < int64(st.forbidden) {
		return finding{}, false
	}
	base := sec.baselineOf(key)
	if float64(forb) < st.rateFactor*base {
		return finding{}, false
	}
	win := fmtSpan(st.window)
	return finding{kind: "forbidden_spike", severity: grade(forb, st.forbidden, false), n: forb, nNew: forbNew,
		detail: map[string]any{"forbidden": forb, "baseline": math.Round(base), "window": win,
			"summary": fmt.Sprintf("%s in %s; usually %s.", what, win, fnum(math.Round(base)))}}, true
}

// scannerPaths adds the most requested probe paths to each scanner finding.
func (s *Server) scannerPaths(ctx context.Context, out []finding, ips []string, at map[string]int, span []any, probe string) error {
	ips = head(ips, 20)
	if len(ips) == 0 {
		return nil
	}
	args := append([]any{}, span...)
	for _, ip := range ips {
		args = append(args, ip)
	}
	res, err := s.db.Query(ctx, `SELECT ip, path, COUNT(*) AS hits FROM api_logs
WHERE created_at >= ? AND created_at < ? AND ip IN (`+strings.TrimSuffix(strings.Repeat("?, ", len(ips)), ", ")+`) AND `+probe+`
GROUP BY ip, path ORDER BY hits DESC LIMIT 200`, args...)
	if err != nil {
		return err
	}
	for _, row := range res.Rows {
		if len(row) != 3 {
			continue
		}
		i, ok := at[toString(row[0])]
		if !ok {
			continue
		}
		d := out[i].detail
		list, _ := d["paths"].([]string)
		if len(list) >= 5 {
			continue
		}
		p := truncate(maskPath(toString(row[1])), 120)
		d["paths"] = append(list, p)
		if out[i].target == "" {
			out[i].target = p
		}
	}
	return nil
}

// tokenSegment looks like an id, code or token rather than a route word.
var tokenSegment = regexp.MustCompile(`^[A-Za-z0-9_\-.~%=]{20,}$`)

// maskPath hides path segments that look like tokens, so a detail never
// carries a reset link or magic-link code.
func maskPath(p string) string {
	if p == "" {
		return ""
	}
	parts := strings.Split(p, "/")
	for i, seg := range parts {
		if tokenSegment.MatchString(seg) {
			parts[i] = ":token"
		}
	}
	return strings.Join(parts, "/")
}

// baselineOf is the median of a key's recent windows (0 for a new key).
func (sec *securityState) baselineOf(key string) float64 {
	sec.mu.Lock()
	defer sec.mu.Unlock()
	h := sec.baseline[key]
	if len(h) == 0 {
		return 0
	}
	v := append([]float64(nil), h...)
	sort.Float64s(v)
	return v[len(v)/2]
}

// record appends this run's value of every tracked key (0 when it did not
// come back). Flagged keys are not recorded, so an attack that goes on does
// not become its own baseline. Keys with only zeros are dropped.
func (sec *securityState) record(samples map[string]float64, flagged map[string]bool) {
	sec.mu.Lock()
	defer sec.mu.Unlock()
	for k := range samples {
		if _, ok := sec.baseline[k]; !ok && !flagged[k] {
			sec.baseline[k] = nil
		}
	}
	for k, h := range sec.baseline {
		if flagged[k] {
			continue
		}
		h = append(h, samples[k])
		if len(h) > baselineSamples {
			h = h[len(h)-baselineSamples:]
		}
		zero := true
		for _, v := range h {
			if v != 0 {
				zero = false
				break
			}
		}
		if zero {
			delete(sec.baseline, k)
			continue
		}
		sec.baseline[k] = h
	}
}

// writeFindings inserts new findings and extends the ones still in their
// dedupe period.
func (s *Server) writeFindings(ctx context.Context, found []finding, now time.Time) error {
	sec := s.security()
	res, err := s.db.Query(ctx, `SELECT id, kind, COALESCE(ip, ''), COALESCE(actor, ''), severity FROM security_events
WHERE source = 'api' AND created_at >= ? ORDER BY id`, now.Add(-sec.st.dedupe).Format(tsLayout))
	if err != nil {
		return err
	}
	type openRow struct {
		id       int64
		severity string
	}
	open := map[string]openRow{}
	for _, row := range res.Rows {
		if len(row) == 5 {
			open[toString(row[1])+"|"+toString(row[2])+"|"+toString(row[3])] = openRow{toInt(row[0]), toString(row[4])}
		}
	}
	sec.mu.Lock()
	prev := sec.active
	sec.mu.Unlock()
	active := map[string]bool{}
	for _, f := range found {
		k := f.key()
		active[k] = true
		detail := "{}"
		if b, err := json.Marshal(f.detail); err == nil && len(b) <= maxDetailBytes {
			detail = string(b)
		}
		if o, ok := open[k]; ok {
			sev := maxSeverity(o.severity, f.severity)
			if f.nNew <= 0 && sev == o.severity {
				continue
			}
			if err := s.db.Exec(ctx, `UPDATE security_events SET count = count + ?, severity = ?, detail = ? WHERE id = ?`,
				max(f.nNew, 0), sev, detail, o.id); err != nil {
				return err
			}
			continue
		}
		count := f.n
		if prev[k] {
			// The same activity went on past its row's period: count only
			// what is new, the rest is in the previous row.
			count = f.nNew
		}
		if count <= 0 {
			continue
		}
		if err := s.db.Exec(ctx, `INSERT INTO security_events (created_at, source, kind, severity, host, service, actor, ip, target, detail, count)
VALUES (?, 'api', ?, ?, NULL, ?, ?, ?, ?, ?, ?)`,
			now.Format(tsLayout), f.kind, f.severity, nullable(f.service), nullable(clip(f.actor, 255)), nullable(f.ip),
			nullable(clip(f.target, 255)), detail, count); err != nil {
			return err
		}
		s.logger.Info("security finding", "kind", f.kind, "severity", f.severity, "ip", f.ip, "actor", f.actor, "count", count)
	}
	sec.mu.Lock()
	sec.active = active
	sec.mu.Unlock()
	return nil
}
