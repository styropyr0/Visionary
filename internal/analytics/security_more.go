package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Detectors beyond the per-window IP and account passes in security_detect.go:
//
//   - account_targeted escalates when an IP that failed on the account then
//     signs in to it (targetedSucceeded).
//   - login_as_other: a browser signed in as one user that tries to sign in
//     as someone else (detectOtherAccount). Needs LoginAttempt in the app.
//   - portal_*: Visionary's own audit log, read in place: failed sign-ins to
//     this dashboard, and changes worth an alert such as a secret shown or a
//     role changed (detectPortal). Their rows have source 'audit'.
//   - *_slow and distributed_login_attack: the same failures over the much
//     longer security_slow_window, run every slowEvery (detectSlow).
//
// The account-takeover, abuse and threat-intelligence detectors live in
// security_takeover.go, security_abuse.go and security_intel.go.

// slowEvery is how often the slow pass reads its long window.
const slowEvery = 15 * time.Minute

// otherAccountLookback is how far back a browser counts as signed in as a
// user when it then tries another account.
const otherAccountLookback = time.Hour

// slowKinds repeat once per slow window rather than every dedupe period.
var slowKinds = map[string]bool{"auth_bruteforce_slow": true, "account_targeted_slow": true, "distributed_login_attack": true}

// dedupeFor is how long one row stands for a kind's ongoing activity.
func (st *securitySettings) dedupeFor(kind string) time.Duration {
	if slowKinds[kind] {
		return max(st.dedupe, st.slowWindow)
	}
	return st.dedupe
}

// detectMore runs every detector after the first two passes. Each one reads
// only what it needs; a run where none applies costs a few small queries.
func (s *Server) detectMore(ctx context.Context, from, to, cursor, now time.Time) ([]finding, error) {
	var out []finding
	if err := s.linkIdentities(ctx, to); err != nil {
		return nil, err
	}
	steps := []func(context.Context, time.Time, time.Time, time.Time) ([]finding, error){
		s.detectOtherAccount,
		s.detectPortal,
		s.detectTakeover,
		s.detectAbuse,
		s.detectSSH,
	}
	for _, step := range steps {
		found, err := step(ctx, from, to, cursor)
		if err != nil {
			return nil, err
		}
		out = append(out, found...)
	}
	sec := s.security()
	sec.mu.Lock()
	due := now.Sub(sec.slowAt) >= slowEvery
	prev := sec.slowAt
	sec.mu.Unlock()
	if due {
		found, err := s.detectSlow(ctx, to, prev)
		if err != nil {
			return nil, err
		}
		out = append(out, found...)
		sec.mu.Lock()
		sec.slowAt = now
		sec.mu.Unlock()
	}
	return out, nil
}

// targetedSucceeded marks account_targeted findings critical when one of the
// IPs that failed on the account then signed in to it: the guessing worked.
// A success from an IP that never failed is noted but is likely the owner.
func (s *Server) targetedSucceeded(ctx context.Context, out []finding, at []int, span []any, login, failed string) error {
	if len(at) == 0 {
		return nil
	}
	at = head(at, 50)
	args := append([]any{}, span...)
	byAccount := map[string]int{}
	for _, i := range at {
		byAccount[out[i].actor] = i
		args = append(args, out[i].actor)
	}
	acct := loginAccountExpr(s.q)
	res, err := s.db.Query(ctx, `SELECT `+acct+` AS account, ip, `+countIf(failed)+` AS fails, `+countIf(login+" AND status < 400")+` AS oks
FROM api_logs WHERE created_at >= ? AND created_at < ? AND `+acct+` IN (`+placeholders(len(at))+`) AND `+login+`
GROUP BY `+acct+`, ip`, args...)
	if err != nil {
		return err
	}
	type tally struct {
		worked, owner []string
	}
	t := map[string]*tally{}
	for _, row := range res.Rows {
		if len(row) != 4 || toInt(row[3]) == 0 {
			continue
		}
		acc, ip := toString(row[0]), toString(row[1])
		if t[acc] == nil {
			t[acc] = &tally{}
		}
		if toInt(row[2]) > 0 {
			t[acc].worked = append(t[acc].worked, ip)
		} else {
			t[acc].owner = append(t[acc].owner, ip)
		}
	}
	for acc, v := range t {
		i, ok := byAccount[acc]
		if !ok {
			continue
		}
		f := &out[i]
		if len(v.worked) > 0 {
			f.severity = "critical"
			f.detail["succeeded_from"] = head(v.worked, 5)
			f.detail["summary"] = strings.TrimSuffix(f.detail["summary"].(string), ".") +
				fmt.Sprintf("; %s then signed in to it, so the guessing may have worked.", strings.Join(head(v.worked, 3), ", "))
		} else if len(v.owner) > 0 {
			f.detail["signed_in_from"] = head(v.owner, 5)
		}
	}
	return nil
}

// detectOtherAccount finds a browser (IP and user agent) that was signed in
// as one user and then tried to sign in as a different account: someone
// trying a colleague's or a leaked password from their own session. Only
// sign-ins that recorded the account tried (LoginAttempt) are considered.
func (s *Server) detectOtherAccount(ctx context.Context, from, to, cursor time.Time) ([]finding, error) {
	st := &s.security().st
	q := s.q
	login, fail := st.loginCond(q), authFailCond(q)
	attempts, err := s.db.Query(ctx, `SELECT ip, COALESCE(user_agent, '') AS ua, login_account, MAX(service) AS service,
  `+countIf(fail)+` AS fails, `+countIf(fail+" AND created_at >= ?")+` AS fails_new, `+countIf("status < 400")+` AS oks
FROM api_logs WHERE created_at >= ? AND created_at < ? AND login_account IS NOT NULL AND login_account <> '' AND ip IS NOT NULL AND `+login+`
GROUP BY ip, COALESCE(user_agent, ''), login_account
LIMIT 2000`, cursor.Format(tsLayout), from.Format(tsLayout), to.Format(tsLayout))
	if err != nil || len(attempts.Rows) == 0 {
		return nil, err
	}
	type attempt struct {
		account, service           string
		fails, failsNew, oks       int64
	}
	byDevice := map[[2]string][]attempt{}
	var ips []string
	seenIP := map[string]bool{}
	for _, row := range attempts.Rows {
		if len(row) != 7 || st.ignored(toString(row[0])) {
			continue
		}
		k := [2]string{toString(row[0]), toString(row[1])}
		byDevice[k] = append(byDevice[k], attempt{toString(row[2]), toString(row[3]), toInt(row[4]), toInt(row[5]), toInt(row[6])})
		if !seenIP[k[0]] {
			seenIP[k[0]] = true
			ips = append(ips, k[0])
		}
	}
	ips = head(ips, 200)
	if len(ips) == 0 {
		return nil, nil
	}
	// Who each browser was signed in as: requests that carried a user and
	// were not sign-ins themselves.
	args := []any{to.Add(-otherAccountLookback).Format(tsLayout), to.Format(tsLayout)}
	for _, ip := range ips {
		args = append(args, ip)
	}
	acct := accountExpr(q)
	signed, err := s.db.Query(ctx, `SELECT ip, COALESCE(user_agent, '') AS ua, `+acct+` AS account
FROM api_logs WHERE created_at >= ? AND created_at < ? AND ip IN (`+placeholders(len(ips))+`)
  AND (user_id IS NOT NULL OR (user_handle IS NOT NULL AND user_handle <> '')) AND status < 400 AND NOT `+login+`
GROUP BY ip, COALESCE(user_agent, ''), `+acct+`
LIMIT 5000`, args...)
	if err != nil {
		return nil, err
	}
	as := map[[2]string]map[string]bool{}
	add := func(k [2]string, who string) {
		if as[k] == nil {
			as[k] = map[string]bool{}
		}
		as[k][strings.ToLower(who)] = true
	}
	// signedAs is who each browser was signed in as, in a fixed order, so a
	// finding keeps the same actor from one run to the next.
	signedAs := map[[2]string][]string{}
	var users []string
	for _, row := range signed.Rows {
		if len(row) != 3 {
			continue
		}
		k, u := [2]string{toString(row[0]), toString(row[1])}, strings.ToLower(toString(row[2]))
		add(k, u)
		signedAs[k] = append(signedAs[k], u)
		users = append(users, u)
	}
	// The accounts a browser signed in to itself are its own, and so are the
	// sign-in names linked to the users it was signed in as.
	own, err := s.db.Query(ctx, `SELECT ip, COALESCE(user_agent, ''), LOWER(login_account) FROM api_logs
WHERE created_at >= ? AND created_at < ? AND ip IN (`+placeholders(len(ips))+`) AND login_account IS NOT NULL AND login_account <> '' AND status < 400 AND `+login+`
GROUP BY ip, COALESCE(user_agent, ''), LOWER(login_account) LIMIT 5000`, args...)
	if err != nil {
		return nil, err
	}
	for _, row := range own.Rows {
		if len(row) == 3 {
			add([2]string{toString(row[0]), toString(row[1])}, toString(row[2]))
		}
	}
	links, err := s.identityLinks(ctx, users)
	if err != nil {
		return nil, err
	}
	for _, set := range as {
		for u := range set {
			for _, alias := range links[u] {
				set[alias] = true
			}
		}
	}
	win := fmtSpan(st.window)
	var out []finding
	for k, list := range byDevice {
		users := as[k]
		if len(users) == 0 {
			continue
		}
		who := signedAs[k]
		if len(who) == 0 {
			continue
		}
		sort.Strings(who)
		// Name the user by their sign-in name when one is linked: an email
		// reads better than an id.
		actor := who[0]
		if names := append([]string(nil), links[actor]...); len(names) > 0 {
			sort.Strings(names)
			actor = names[0]
		}
		for _, a := range list {
			if users[strings.ToLower(a.account)] {
				continue
			}
			worked := a.oks > 0 && a.fails > 0
			if a.fails < int64(st.otherAccountFails) && !worked {
				continue
			}
			sev := "warning"
			if worked || a.fails >= int64(severeFactor*st.otherAccountFails) {
				sev = "critical"
			}
			summary := fmt.Sprintf("A browser signed in as %s failed %d time(s) to sign in as %s from %s in %s", strings.Join(head(who, 3), ", "), a.fails, a.account, k[0], win)
			if a.oks > 0 {
				summary += fmt.Sprintf(", then signed in as %s", a.account)
			}
			out = append(out, finding{kind: "login_as_other", severity: sev, ip: k[0], actor: actor, target: a.account,
				service: a.service, n: a.fails + a.oks, nNew: a.failsNew,
				detail: map[string]any{"signed_in_as": head(who, 5), "tried": a.account, "failures": a.fails, "successes": a.oks,
					"user_agent": truncate(k[1], 160), "window": win, "summary": summary + "."}})
		}
	}
	return out, nil
}

// portalChanges are audit actions that become findings, so alerts and the
// Security page see changes to Visionary itself.
var portalChanges = map[string]struct{ kind, severity, what string }{
	"settings.secret_viewed": {"portal_secret_viewed", "info", "showed the stored secret %s"},
	"team.account_created":   {"portal_account_created", "info", "added the account %s"},
	"team.account_updated":   {"portal_account_changed", "info", "changed the account %s"},
	"team.account_deleted":   {"portal_account_deleted", "info", "deleted the account %s"},
	"team.role_mapping":      {"portal_roles_changed", "warning", "changed who gets which role%s"},
	"settings.sign_in":       {"portal_signin_changed", "warning", "changed how people sign in%s"},
	"api_key.create":         {"portal_api_key_created", "info", "created the API key %s"},
	"data.delete":            {"portal_data_deleted", "warning", "deleted stored data (%s)"},
	"auth.password_changed":  {"portal_password_changed", "info", "changed the password of %s"},
	"auth.ip_blocked":        {"portal_ip_blocked", "warning", "was refused at %s, which is not on the allow list"},
	"auth.two_step_reset":    {"portal_two_step_reset", "warning", "turned off two-step sign-in for %s"},
}

// detectPortal reads Visionary's own audit log: brute force and targeted
// accounts on this dashboard's sign-in, and the changes in portalChanges.
func (s *Server) detectPortal(ctx context.Context, from, to, cursor time.Time) ([]finding, error) {
	st := &s.security().st
	win := fmtSpan(st.window)
	span := []any{from.Format(tsLayout), to.Format(tsLayout)}
	cur := cursor.Format(tsLayout)
	var out []finding

	byIP, err := s.db.Query(ctx, `SELECT ip, `+countIf("action = 'auth.sign_in_failed'")+` AS fails,
  `+countIf("action = 'auth.sign_in_failed' AND created_at >= ?")+` AS fails_new,
  `+countIf("action = 'auth.sign_in'")+` AS oks,
  COUNT(DISTINCT CASE WHEN action = 'auth.sign_in_failed' THEN target END) AS accounts,
  MAX(CASE WHEN action = 'auth.sign_in' THEN user_name END) AS who
FROM audit_log WHERE created_at >= ? AND created_at < ? AND action IN ('auth.sign_in_failed', 'auth.sign_in') AND ip <> ''
GROUP BY ip HAVING `+countIf("action = 'auth.sign_in_failed'")+` >= ?`, append([]any{cur}, append(span, st.portalFailures)...)...)
	if err != nil {
		return nil, err
	}
	for _, row := range byIP.Rows {
		if len(row) != 6 || st.ignored(toString(row[0])) {
			continue
		}
		ip, fails, failsNew, oks, accounts := toString(row[0]), toInt(row[1]), toInt(row[2]), toInt(row[3]), toInt(row[4])
		summary := fmt.Sprintf("%d failed sign-ins to Visionary from %s in %s", fails, ip, win)
		if accounts > 1 {
			summary += fmt.Sprintf(", for %d accounts", accounts)
		}
		if oks > 0 {
			summary += fmt.Sprintf("; then %s signed in from it", fallback(toString(row[5]), "someone"))
		}
		out = append(out, finding{source: "audit", kind: "portal_bruteforce", severity: grade(fails, st.portalFailures, oks > 0),
			ip: ip, target: "Visionary sign-in", n: fails, nNew: failsNew,
			detail: map[string]any{"failures": fails, "accounts": accounts, "successes": oks, "window": win, "summary": summary + "."}})
	}

	byAccount, err := s.db.Query(ctx, `SELECT target, COUNT(*) AS fails, `+countIf("created_at >= ?")+` AS fails_new, COUNT(DISTINCT ip) AS ips
FROM audit_log WHERE created_at >= ? AND created_at < ? AND action = 'auth.sign_in_failed' AND target <> ''
GROUP BY target HAVING COUNT(DISTINCT ip) >= ?`, append([]any{cur}, append(span, st.targetedIPs)...)...)
	if err != nil {
		return nil, err
	}
	for _, row := range byAccount.Rows {
		if len(row) != 4 {
			continue
		}
		who, fails, failsNew, ips := toString(row[0]), toInt(row[1]), toInt(row[2]), toInt(row[3])
		out = append(out, finding{source: "audit", kind: "portal_account_targeted", severity: grade(ips, st.targetedIPs, false),
			actor: who, target: "Visionary sign-in", n: fails, nNew: failsNew,
			detail: map[string]any{"failures": fails, "ips": ips, "window": win,
				"summary": fmt.Sprintf("%d IPs failed to sign in to Visionary as %s (%d attempts) in %s.", ips, who, fails, win)}})
	}

	actions := make([]any, 0, len(portalChanges)+1)
	actions = append(actions, cur, to.Format(tsLayout))
	for a := range portalChanges {
		actions = append(actions, a)
	}
	changes, err := s.db.Query(ctx, `SELECT id, user_name, action, target, detail, ip FROM audit_log
WHERE created_at >= ? AND created_at < ? AND action IN (`+placeholders(len(portalChanges))+`)
ORDER BY id LIMIT 200`, actions...)
	if err != nil {
		return nil, err
	}
	for _, row := range changes.Rows {
		if len(row) != 6 {
			continue
		}
		who, action, target, ip := toString(row[1]), toString(row[2]), toString(row[3]), toString(row[5])
		c := portalChanges[action]
		sev := c.severity
		var d map[string]any
		_ = json.Unmarshal([]byte(toString(row[4])), &d)
		if action == "team.account_updated" && d["role"] == "admin" {
			sev = "warning"
		}
		arg := target
		switch action {
		case "team.role_mapping", "settings.sign_in":
			arg = ""
		case "auth.ip_blocked":
			arg = ip
		}
		summary := fmt.Sprintf("%s "+c.what+".", fallback(who, "Someone"), arg)
		out = append(out, finding{source: "audit", kind: c.kind, severity: sev, ip: ip, actor: who, target: target,
			n: 1, nNew: 1, each: true, detail: map[string]any{"action": action, "audit_id": toInt(row[0]), "change": d, "summary": summary}})
	}
	return out, nil
}

// detectSlow repeats the failed-login checks over the long window, for
// attacks paced to stay under the per-window limits, and looks for many
// addresses each failing a little: a botnet spreading its guesses.
func (s *Server) detectSlow(ctx context.Context, to, prev time.Time) ([]finding, error) {
	st := &s.security().st
	q := s.q
	login, fail := st.loginCond(q), authFailCond(q)
	failed := login + " AND " + fail
	from := to.Add(-st.slowWindow)
	if prev.IsZero() || prev.Before(from) {
		prev = from
	}
	cur := prev.Format(tsLayout)
	span := []any{from.Format(tsLayout), to.Format(tsLayout)}
	win := fmtSpan(st.slowWindow)
	var out []finding

	// Per IP. Rows here already over the fast limit were reported by it.
	byIP, err := s.db.Query(ctx, `SELECT ip, MAX(service) AS service, COUNT(*) AS fails, `+countIf("created_at >= ?")+` AS fails_new,
  COUNT(DISTINCT `+loginAccountExpr(q)+`) AS accounts, MAX(COALESCE(route, path)) AS route
FROM api_logs WHERE created_at >= ? AND created_at < ? AND ip IS NOT NULL AND ip <> '' AND `+failed+`
GROUP BY ip`, append([]any{cur}, span...)...)
	if err != nil {
		return nil, err
	}
	var spread int64
	var spreadIPs, spreadNew int64
	route, service := "", ""
	for _, row := range byIP.Rows {
		if len(row) != 6 {
			continue
		}
		ip := toString(row[0])
		if st.ignored(ip) {
			continue
		}
		fails, failsNew, accounts := toInt(row[2]), toInt(row[3]), toInt(row[4])
		if fails < int64(st.bruteforce) {
			spread += fails
			spreadNew += failsNew
			spreadIPs++
			route, service = fallback(route, maskPath(toString(row[5]))), fallback(service, toString(row[1]))
		}
		if fails >= int64(st.slowFailures) {
			out = append(out, finding{kind: "auth_bruteforce_slow", severity: grade(fails, st.slowFailures, false),
				ip: ip, target: maskPath(toString(row[5])), service: toString(row[1]), n: fails, nNew: failsNew,
				detail: map[string]any{"failures": fails, "accounts": accounts, "window": win,
					"summary": fmt.Sprintf("%d failed sign-ins from %s over %s, for %d account(s): paced to stay under the per-%s limit.", fails, ip, win, accounts, fmtSpan(st.window))}})
		}
	}
	if spreadIPs >= int64(st.distributedIPs) {
		out = append(out, finding{kind: "distributed_login_attack", severity: grade(spreadIPs, st.distributedIPs, false),
			target: route, service: service, n: spread, nNew: spreadNew,
			detail: map[string]any{"failures": spread, "ips": spreadIPs, "window": win,
				"summary": fmt.Sprintf("%d addresses each failed a few sign-ins (%d in all) over %s: guesses spread across many IPs, as a botnet does.", spreadIPs, spread, win)}})
	}

	acct := loginAccountExpr(q)
	byAccount, err := s.db.Query(ctx, `SELECT `+acct+` AS account, MAX(service) AS service, COUNT(*) AS fails, `+countIf("created_at >= ?")+` AS fails_new, COUNT(DISTINCT ip) AS ips
FROM api_logs WHERE created_at >= ? AND created_at < ? AND `+hasAccount+` AND `+failed+`
GROUP BY `+acct+` HAVING COUNT(DISTINCT ip) >= ?`, append([]any{cur}, append(span, st.slowIPs)...)...)
	if err != nil {
		return nil, err
	}
	for _, row := range byAccount.Rows {
		if len(row) != 5 || toString(row[0]) == "" {
			continue
		}
		who, fails, failsNew, ips := toString(row[0]), toInt(row[2]), toInt(row[3]), toInt(row[4])
		out = append(out, finding{kind: "account_targeted_slow", severity: grade(ips, st.slowIPs, false),
			actor: who, service: toString(row[1]), n: fails, nNew: failsNew,
			detail: map[string]any{"failures": fails, "ips": ips, "window": win,
				"summary": fmt.Sprintf("%d IPs failed to sign in as %s over %s (%d attempts).", ips, who, win, fails)}})
	}
	return out, nil
}

// placeholders is n comma-separated question marks.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

// detectSSH reads the host agent's failed SSH logins: an address failing on
// a host at least bruteforce times in the window, critical when a login from
// it then succeeded. The agent reports raw events; this grades them together.
func (s *Server) detectSSH(ctx context.Context, from, to, cursor time.Time) ([]finding, error) {
	st := &s.security().st
	res, err := s.db.Query(ctx, `SELECT host, ip, `+countIf("kind = 'ssh_failed'")+` AS rows_failed,
  SUM(CASE WHEN kind = 'ssh_failed' THEN count ELSE 0 END) AS fails,
  SUM(CASE WHEN kind = 'ssh_failed' AND created_at >= ? THEN count ELSE 0 END) AS fails_new,
  SUM(CASE WHEN kind = 'ssh_login' THEN count ELSE 0 END) AS logins,
  COUNT(DISTINCT CASE WHEN kind = 'ssh_failed' THEN actor END) AS users,
  MAX(CASE WHEN kind = 'ssh_login' THEN actor END) AS logged_in_as
FROM security_events WHERE source = 'host' AND created_at >= ? AND created_at < ? AND kind IN ('ssh_failed', 'ssh_login') AND ip IS NOT NULL
GROUP BY host, ip HAVING SUM(CASE WHEN kind = 'ssh_failed' THEN count ELSE 0 END) >= ?`,
		cursor.Format(tsLayout), from.Format(tsLayout), to.Format(tsLayout), st.bruteforce)
	if err != nil {
		return nil, err
	}
	win := fmtSpan(st.window)
	var out []finding
	for _, row := range res.Rows {
		if len(row) != 8 || st.ignored(toString(row[1])) {
			continue
		}
		host, ip := toString(row[0]), toString(row[1])
		fails, failsNew, logins, users := toInt(row[3]), toInt(row[4]), toInt(row[5]), toInt(row[6])
		summary := fmt.Sprintf("%d failed SSH logins on %s from %s for %d user name(s) in %s", fails, host, ip, users, win)
		if logins > 0 {
			summary += fmt.Sprintf("; then %s logged in from it", fallback(toString(row[7]), "someone"))
		}
		out = append(out, finding{source: "host", host: host, kind: "ssh_bruteforce", severity: grade(fails, st.bruteforce, logins > 0),
			ip: ip, actor: host, target: "ssh", n: fails, nNew: failsNew,
			detail: map[string]any{"failures": fails, "users": users, "logins": logins, "window": win, "summary": summary + "."}})
	}
	return out, nil
}

// identityLinkWindow is how soon after a sign-in its browser's first
// signed-in request must come to tie the sign-in name to the user.
const identityLinkWindow = 10 * time.Minute

// linkIdentities ties the account a sign-in tried (LoginAttempt, often an
// email) to the user the app set afterwards (SetUser, often an id): after a
// successful sign-in, the first signed-in request from the same IP and user
// agent names the user. Links are kept in security_known (kind 'user'), so a
// browser's own sign-in name isn't mistaken for someone else's and a risky
// sign-in matches the changes its user makes.
func (s *Server) linkIdentities(ctx context.Context, to time.Time) error {
	st := &s.security().st
	q := s.q
	login := st.loginCond(q)
	from := to.Add(-otherAccountLookback).Format(tsLayout)
	res, err := s.db.Query(ctx, `SELECT ip, COALESCE(user_agent, ''), LOWER(login_account), MIN(created_at) FROM api_logs
WHERE created_at >= ? AND created_at < ? AND login_account IS NOT NULL AND login_account <> '' AND status < 400 AND ip IS NOT NULL AND `+login+`
GROUP BY ip, COALESCE(user_agent, ''), LOWER(login_account) LIMIT 1000`, from, to.Format(tsLayout))
	if err != nil || len(res.Rows) == 0 {
		return err
	}
	type signIn struct {
		account string
		at      string
	}
	byDevice := map[[2]string][]signIn{}
	var ips []any
	seen := map[string]bool{}
	for _, row := range res.Rows {
		if len(row) != 4 {
			continue
		}
		k := [2]string{toString(row[0]), toString(row[1])}
		byDevice[k] = append(byDevice[k], signIn{toString(row[2]), toString(row[3])})
		if !seen[k[0]] && len(ips) < 200 {
			seen[k[0]] = true
			ips = append(ips, k[0])
		}
	}
	acct := accountExpr(q)
	users, err := s.db.Query(ctx, `SELECT ip, COALESCE(user_agent, ''), LOWER(`+acct+`), MIN(created_at) FROM api_logs
WHERE created_at >= ? AND created_at < ? AND ip IN (`+placeholders(len(ips))+`) AND (user_id IS NOT NULL OR (user_handle IS NOT NULL AND user_handle <> ''))
GROUP BY ip, COALESCE(user_agent, ''), LOWER(`+acct+`) LIMIT 5000`, append([]any{from, to.Format(tsLayout)}, ips...)...)
	if err != nil {
		return err
	}
	type firstUse struct {
		user string
		at   string
	}
	used := map[[2]string][]firstUse{}
	for _, row := range users.Rows {
		if len(row) == 4 {
			k := [2]string{toString(row[0]), toString(row[1])}
			used[k] = append(used[k], firstUse{toString(row[2]), toString(row[3])})
		}
	}
	for k, list := range byDevice {
		for _, si := range list {
			at, err := time.Parse(tsLayout, si.at)
			if err != nil {
				continue
			}
			limit := at.Add(identityLinkWindow).Format(tsLayout)
			best := firstUse{}
			for _, u := range used[k] {
				if u.at >= si.at && u.at < limit && (best.at == "" || u.at < best.at) {
					best = u
				}
			}
			if best.user == "" || best.user == si.account {
				continue
			}
			err = s.db.Exec(ctx, `INSERT INTO security_known (account, kind, value, first_seen, last_seen, hits) VALUES (?, 'user', ?, ?, ?, 1)`,
				clip(si.account, 191), clip(best.user, 191), si.at, si.at)
			if err != nil && !isDuplicateKey(err) {
				return err
			}
		}
	}
	return nil
}

// identityLinks returns, for each of names (lower case), the names linked
// to it either way.
func (s *Server) identityLinks(ctx context.Context, names []string) (map[string][]string, error) {
	out := map[string][]string{}
	if len(names) == 0 {
		return out, nil
	}
	names = head(names, 500)
	args := make([]any, 0, 2*len(names))
	for _, n := range names {
		args = append(args, n)
	}
	for _, n := range names {
		args = append(args, n)
	}
	res, err := s.db.Query(ctx, `SELECT account, value FROM security_known WHERE kind = 'user' AND (account IN (`+placeholders(len(names))+`) OR value IN (`+placeholders(len(names))+`))`, args...)
	if err != nil {
		return nil, err
	}
	for _, row := range res.Rows {
		if len(row) == 2 {
			a, v := toString(row[0]), toString(row[1])
			out[a] = append(out[a], v)
			out[v] = append(out[v], a)
		}
	}
	return out, nil
}
