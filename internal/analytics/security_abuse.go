package analytics

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// API abuse, per window:
//
//   - enumeration: one user or IP reading many different records through a
//     route with an ID (/orders/:id): scraping.
//   - idor_probe: many different records refused (403/404) through one such
//     route: trying records that belong to someone else.
//   - injection_attempt: SQL injection, script, template, command, JNDI or
//     traversal patterns in the path or query string. Only the categories
//     and the route are kept, never the query itself.
//   - route_rate_limit: a route over its own per-minute limit (Route limits).
//   - signup_abuse / otp_abuse: many sign-ups, or many codes sent, from one
//     IP (or codes sent to one account).

// routeLimit is one line of Route limits: "POST /api/export* 10".
type routeLimit struct {
	method string // upper case; "" for any
	glob   string // as written, for the finding
	like   string // the LIKE pattern
	perMin int
}

// parseRouteLimits reads the setting; bad lines are skipped.
func parseRouteLimits(v string) []routeLimit {
	var out []routeLimit
	for _, line := range strings.Split(v, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || strings.HasPrefix(f[0], "#") {
			continue
		}
		method := ""
		if len(f) >= 3 {
			method, f = strings.ToUpper(f[0]), f[1:]
		}
		n, err := strconv.Atoi(f[len(f)-1])
		glob := strings.ToLower(f[0])
		if err != nil || n <= 0 || strings.ContainsAny(glob, `'\"`) || len(glob) > 200 {
			continue
		}
		like := strings.NewReplacer("*", "%", "?", "_").Replace(likeEscaper.Replace(glob))
		out = append(out, routeLimit{method: method, glob: f[0], like: like, perMin: n})
		if len(out) >= 20 {
			break
		}
	}
	return out
}

// injectionPatterns are lower-case substrings of a path or query string that
// only attacks contain, raw and URL-encoded.
var injectionPatterns = map[string][]string{
	"sql": {"union select", "union+select", "union%20select", "' or '1'='1", "%27%20or%20", "%27+or+", "' or 1=1", "%20or%201=1",
		"sleep(", "pg_sleep", "benchmark(", "waitfor delay", "waitfor%20delay", "information_schema", "xp_cmdshell", "@@version", "'--", "%27--", "%27%3b"},
	"script":    {"<script", "%3cscript", "javascript:", "onerror=", "onload=", "%3csvg", "<svg", "document.cookie", "alert(", "alert%28"},
	"template":  {"{{7*7}}", "%7b%7b", "${7*7}", "#{7*7}", "<%="},
	"command":   {";cat ", ";cat%20", "|cat%20", "%7ccat", "$(", "%24%28", "`id`", ";id;", "/bin/sh", "/bin/bash", "wget%20http", "curl%20http", "nc%20-e"},
	"jndi":      {"${jndi:", "%24%7bjndi", "jndi:ldap", "jndi:rmi"},
	"traversal": {"../", "..%2f", "%2e%2e", "/etc/passwd", "win.ini", "boot.ini"},
}

// injectionOrder fixes the column order of the per-category counts.
var injectionOrder = []string{"sql", "script", "template", "command", "jndi", "traversal"}

// injectionCond matches requests with any of a category's patterns.
func injectionCond(q sqlDialect, category string) string {
	likes := make([]string, len(injectionPatterns[category]))
	for i, p := range injectionPatterns[category] {
		likes[i] = "%" + likeEscaper.Replace(p) + "%"
	}
	return "(" + likeAny(q, "LOWER(path)", likes) + " OR " + likeAny(q, "LOWER(COALESCE(query, ''))", likes) + ")"
}

// paramRoute is a route pattern with a parameter in it.
const paramRoute = "(route LIKE '%:%' OR route LIKE '%{%' OR route LIKE '%<%' OR route LIKE '%[%')"

func (s *Server) detectAbuse(ctx context.Context, from, to, cursor time.Time) ([]finding, error) {
	st := &s.security().st
	q := s.q
	cur := cursor.Format(tsLayout)
	span := []any{from.Format(tsLayout), to.Format(tsLayout)}
	win := fmtSpan(st.window)
	minutes := st.window.Minutes()
	var out []finding

	// Enumeration and probing, per (IP, account, route).
	acct := accountExpr(q)
	ids, err := s.db.Query(ctx, `SELECT ip, `+acct+` AS account, route, MAX(service) AS service,
  COUNT(DISTINCT CASE WHEN status < 400 THEN path END) AS read_ids, COUNT(DISTINCT CASE WHEN status IN (403, 404) THEN path END) AS denied_ids,
  COUNT(*) AS n, `+countIf("created_at >= ?")+` AS n_new
FROM api_logs WHERE created_at >= ? AND created_at < ? AND `+paramRoute+` AND method = 'GET'
GROUP BY ip, `+acct+`, route HAVING COUNT(DISTINCT path) >= ? LIMIT 2000`, append([]any{cur}, append(span, min(st.enumIDs, st.idorIDs))...)...)
	if err != nil {
		return nil, err
	}
	type idSum struct {
		ip, account, route, service string
		read, denied, n, nNew       int64
	}
	sums := map[string]*idSum{}
	for _, row := range ids.Rows {
		if len(row) != 8 || st.ignored(toString(row[0])) {
			continue
		}
		ip, account, route := toString(row[0]), toString(row[1]), toString(row[2])
		key := "ip:" + ip + "|" + route
		if account != "" {
			key = "user:" + account + "|" + route
		}
		sm := sums[key]
		if sm == nil {
			sm = &idSum{ip: ip, account: account, route: route, service: toString(row[3])}
			sums[key] = sm
		}
		sm.read += toInt(row[4])
		sm.denied += toInt(row[5])
		sm.n += toInt(row[6])
		sm.nNew += toInt(row[7])
	}
	for _, sm := range sums {
		who := fallback(sm.account, sm.ip)
		ip := sm.ip
		if sm.account != "" {
			ip = "" // keyed by the account; its addresses may vary
		}
		if sm.denied >= int64(st.idorIDs) {
			out = append(out, finding{kind: "idor_probe", severity: grade(sm.denied, st.idorIDs, false), ip: ip, actor: sm.account,
				target: sm.route, service: sm.service, n: sm.n, nNew: sm.nNew,
				detail: map[string]any{"denied_ids": sm.denied, "read_ids": sm.read, "window": win,
					"summary": fmt.Sprintf("%s was refused %d different records through %s in %s (and read %d): trying records that aren't theirs.", who, sm.denied, sm.route, win, sm.read)}})
		} else if sm.read >= int64(st.enumIDs) {
			out = append(out, finding{kind: "enumeration", severity: grade(sm.read, st.enumIDs, false), ip: ip, actor: sm.account,
				target: sm.route, service: sm.service, n: sm.n, nNew: sm.nNew,
				detail: map[string]any{"read_ids": sm.read, "denied_ids": sm.denied, "window": win,
					"summary": fmt.Sprintf("%s read %d different records through %s in %s: scraping, or walking through IDs.", who, sm.read, sm.route, win)}})
		}
	}

	// Injection patterns, per IP.
	cols := make([]string, 0, len(injectionOrder))
	var conds []string
	for _, c := range injectionOrder {
		cond := injectionCond(q, c)
		cols = append(cols, countIf(cond)+" AS "+c)
		conds = append(conds, cond)
	}
	anyCond := "(" + strings.Join(conds, " OR ") + ")"
	inj, err := s.db.Query(ctx, `SELECT ip, MAX(service) AS service, `+strings.Join(cols, ", ")+`,
  `+countIf("created_at >= ?")+` AS n_new, `+countIf("status >= 500")+` AS errors_5xx, `+countIf("status >= 200 AND status < 300")+` AS ok,
  MAX(COALESCE(route, path)) AS route, MAX(`+acct+`) AS account
FROM api_logs WHERE created_at >= ? AND created_at < ? AND ip IS NOT NULL AND ip <> '' AND `+anyCond+`
GROUP BY ip LIMIT 500`, append([]any{cur}, span...)...)
	if err != nil {
		return nil, err
	}
	for _, row := range inj.Rows {
		if len(row) != len(injectionOrder)+7 || st.ignored(toString(row[0])) {
			continue
		}
		ip := toString(row[0])
		counts := map[string]int64{}
		var total int64
		var kinds []string
		for i, c := range injectionOrder {
			if n := toInt(row[2+i]); n > 0 {
				counts[c] = n
				total += n
				kinds = append(kinds, c)
			}
		}
		base := 2 + len(injectionOrder)
		nNew, fivexx, ok := toInt(row[base]), toInt(row[base+1]), toInt(row[base+2])
		if total < int64(st.injectionHits) {
			continue
		}
		summary := fmt.Sprintf("%s sent %d request(s) with %s injection patterns in %s", ip, total, strings.Join(kinds, ", "), win)
		if fivexx > 0 {
			summary += fmt.Sprintf("; %d answered with a server error, which can leak how the query failed", fivexx)
		}
		out = append(out, finding{kind: "injection_attempt", severity: grade(total, st.injectionHits, fivexx > 0), ip: ip,
			actor: toString(row[base+4]), target: maskPath(toString(row[base+3])), service: toString(row[1]), n: total, nNew: nNew,
			detail: map[string]any{"categories": counts, "server_errors": fivexx, "answered_2xx": ok, "window": win, "summary": summary + "."}})
	}

	// Route limits, per IP or account.
	for _, rl := range st.routeLimits {
		cond := "(LOWER(COALESCE(route, '')) LIKE " + q.sqlString(rl.like) + " ESCAPE '!' OR LOWER(path) LIKE " + q.sqlString(rl.like) + " ESCAPE '!')"
		if rl.method != "" {
			cond += " AND method = " + q.sqlString(rl.method)
		}
		res, err := s.db.Query(ctx, `SELECT ip, `+acct+` AS account, MAX(service) AS service, COUNT(*) AS n, `+countIf("created_at >= ?")+` AS n_new
FROM api_logs WHERE created_at >= ? AND created_at < ? AND `+cond+`
GROUP BY ip, `+acct+` HAVING COUNT(*) >= ? LIMIT 500`, append([]any{cur}, append(span, int64(math.Ceil(float64(rl.perMin)*minutes)))...)...)
		if err != nil {
			return nil, err
		}
		for _, row := range res.Rows {
			if len(row) != 5 || st.ignored(toString(row[0])) {
				continue
			}
			ip, account := toString(row[0]), toString(row[1])
			n, nNew := toInt(row[3]), toInt(row[4])
			rpm := float64(n) / minutes
			who := fallback(account, ip)
			if account != "" {
				ip = ""
			}
			label := strings.TrimSpace(rl.method + " " + rl.glob)
			out = append(out, finding{kind: "route_rate_limit", severity: grade(int64(rpm), rl.perMin, false), ip: ip, actor: account,
				target: label, service: toString(row[2]), n: n, nNew: nNew,
				detail: map[string]any{"rpm": math.Round(rpm), "limit": rl.perMin, "requests": n, "window": win,
					"summary": fmt.Sprintf("%s called %s %s times a minute over %s; its limit is %d.", who, label, fnum(math.Round(rpm)), win, rl.perMin)}})
		}
	}

	// Sign-up and code-sending floods.
	for _, flood := range []struct {
		kind, what string
		likes      []string
		max        int
	}{
		{"signup_abuse", "sign-ups", st.signupLikes, st.signupMax},
		{"otp_abuse", "codes sent", st.otpLikes, st.otpMax},
	} {
		cond := "method = 'POST' AND " + routeCond(q, flood.likes)
		res, err := s.db.Query(ctx, `SELECT ip, MAX(service) AS service, COUNT(*) AS n, `+countIf("created_at >= ?")+` AS n_new,
  COUNT(DISTINCT `+loginAccountExpr(q)+`) AS accounts, MAX(COALESCE(route, path)) AS route
FROM api_logs WHERE created_at >= ? AND created_at < ? AND ip IS NOT NULL AND ip <> '' AND `+cond+`
GROUP BY ip HAVING COUNT(*) >= ? LIMIT 500`, append([]any{cur}, append(span, flood.max)...)...)
		if err != nil {
			return nil, err
		}
		for _, row := range res.Rows {
			if len(row) != 6 || st.ignored(toString(row[0])) {
				continue
			}
			ip, n, nNew := toString(row[0]), toInt(row[2]), toInt(row[3])
			out = append(out, finding{kind: flood.kind, severity: grade(n, flood.max, false), ip: ip, target: maskPath(toString(row[5])),
				service: toString(row[1]), n: n, nNew: nNew,
				detail: map[string]any{"requests": n, "accounts": toInt(row[4]), "window": win,
					"summary": fmt.Sprintf("%d %s from %s in %s.", n, flood.what, ip, win)}})
		}
		if flood.kind != "otp_abuse" {
			continue
		}
		// Codes sent to one account from many addresses: SMS pumping, or
		// someone flooding a victim's phone.
		la := loginAccountExpr(q)
		res, err = s.db.Query(ctx, `SELECT `+la+` AS account, MAX(service) AS service, COUNT(*) AS n, `+countIf("created_at >= ?")+` AS n_new, COUNT(DISTINCT ip) AS ips
FROM api_logs WHERE created_at >= ? AND created_at < ? AND `+hasAccount+` AND `+cond+`
GROUP BY `+la+` HAVING COUNT(*) >= ? LIMIT 200`, append([]any{cur}, append(span, flood.max)...)...)
		if err != nil {
			return nil, err
		}
		for _, row := range res.Rows {
			if len(row) != 5 || toString(row[0]) == "" {
				continue
			}
			who, n, nNew := toString(row[0]), toInt(row[2]), toInt(row[3])
			out = append(out, finding{kind: "otp_abuse", severity: grade(n, flood.max, false), actor: who, service: toString(row[1]), n: n, nNew: nNew,
				detail: map[string]any{"requests": n, "ips": toInt(row[4]), "window": win,
					"summary": fmt.Sprintf("%d codes were sent for %s from %d address(es) in %s.", n, who, toInt(row[4]), win)}})
		}
	}
	return out, nil
}
