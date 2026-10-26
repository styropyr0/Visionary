package analytics

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Account takeover signals, from successful sign-ins and signed-in traffic:
//
//   - new_login_location / new_login_device: a sign-in from a country (with a
//     location database) or a browser and system the account never used
//     before. An account's first sign-ins only build its history.
//   - impossible_travel: two sign-ins farther apart than anyone could travel
//     in the time between them (needs a city database).
//   - listed_ip_login: a sign-in from an address a blocklist names.
//   - session_spread: one account used from many addresses at once, in
//     different countries or on different devices: a stolen session.
//   - risky_account_change: a password, email, MFA or payout change soon
//     after any of the above.
//
// security_known remembers what each account signed in from.

// Impossible travel needs both a distance and a speed no flight reaches.
const (
	travelMinKm  = 500.0
	travelMaxKmh = 1000.0
)

// riskyKinds are findings after which an account's sensitive changes are
// flagged.
var riskyKinds = []string{"new_login_location", "impossible_travel", "listed_ip_login", "session_spread", "login_as_other", "account_targeted"}

func (s *Server) detectTakeover(ctx context.Context, from, to, cursor time.Time) ([]finding, error) {
	st := &s.security().st
	q := s.q
	login := st.loginCond(q)
	acct := loginAccountExpr(q)
	res, err := s.db.Query(ctx, `SELECT created_at, ip, COALESCE(user_agent, '') AS ua, `+acct+` AS account, service
FROM api_logs WHERE created_at >= ? AND created_at < ? AND `+login+` AND status < 400 AND `+hasAccount+` AND ip IS NOT NULL AND ip <> ''
ORDER BY created_at LIMIT 2000`, cursor.Format(tsLayout), to.Format(tsLayout))
	if err != nil {
		return nil, err
	}
	type signIn struct {
		at                         time.Time
		ip, ua, account, service string
	}
	var logins []signIn
	accounts := map[string]bool{}
	for _, row := range res.Rows {
		if len(row) != 5 || st.ignored(toString(row[1])) {
			continue
		}
		at, err := time.Parse(tsLayout, toString(row[0]))
		account := strings.ToLower(strings.TrimSpace(toString(row[3])))
		if err != nil || account == "" {
			continue
		}
		logins = append(logins, signIn{at, toString(row[1]), toString(row[2]), account, toString(row[4])})
		accounts[account] = true
	}
	var out []finding
	if len(logins) > 0 {
		known, err := s.knownFor(ctx, accounts)
		if err != nil {
			return nil, err
		}
		for _, l := range logins {
			k := known[l.account]
			if k == nil {
				k = &knownAccount{values: map[string]map[string]bool{}}
				known[l.account] = k
			}
			g := s.lookupIP(l.ip)
			device := deviceOf(l.ua)
			if g.Country != "" {
				if k.has("country") && !k.values["country"][g.Country] {
					var before []string
					for c := range k.values["country"] {
						before = append(before, c)
					}
					out = append(out, finding{kind: "new_login_location", severity: "warning", ip: l.ip, actor: l.account,
						target: g.place(), service: l.service, n: 1, nNew: 1,
						detail: map[string]any{"place": g.place(), "country": g.Country, "seen_before": head(before, 10),
							"summary": fmt.Sprintf("%s signed in from %s for the first time (from %s).", l.account, g.place(), l.ip)}})
				}
				if err := s.remember(ctx, k, l.account, "country", g.Country, l.at); err != nil {
					return nil, err
				}
			}
			if device != "" {
				if k.has("device") && !k.values["device"][device] {
					out = append(out, finding{kind: "new_login_device", severity: "info", ip: l.ip, actor: l.account,
						target: device, service: l.service, n: 1, nNew: 1,
						detail: map[string]any{"device": device, "user_agent": truncate(l.ua, 160),
							"summary": fmt.Sprintf("%s signed in with %s for the first time, from %s.", l.account, device, l.ip)}})
				}
				if err := s.remember(ctx, k, l.account, "device", device, l.at); err != nil {
					return nil, err
				}
			}
			if g.HasLoc {
				if prev, ok := k.last(); ok {
					km := distanceKm(prev.lat, prev.lon, g.Lat, g.Lon)
					hours := l.at.Sub(prev.at).Hours()
					if km >= travelMinKm && (hours <= 0 || km/hours > travelMaxKmh) {
						kmh := "instantly"
						if hours > 0 {
							kmh = fmt.Sprintf("at %s km/h", fnum(math.Round(km/hours)))
						}
						out = append(out, finding{kind: "impossible_travel", severity: "critical", ip: l.ip, actor: l.account,
							target: prev.place + " → " + g.place(), service: l.service, n: 1, nNew: 1,
							detail: map[string]any{"from": prev.place, "to": g.place(), "km": math.Round(km), "minutes": math.Round(hours * 60),
								"summary": fmt.Sprintf("%s signed in from %s and then %s, %s km apart in %s: %s.", l.account, prev.place, g.place(),
									fnum(math.Round(km)), fmtSpan(max(l.at.Sub(prev.at).Round(time.Second), time.Second)), kmh)}})
					}
				}
				if err := s.rememberLast(ctx, k, l.account, g, l.at); err != nil {
					return nil, err
				}
			}
			if lists := s.listedIn(l.ip); len(lists) > 0 {
				// prepareFindings raises it one level for being listed.
				out = append(out, finding{kind: "listed_ip_login", severity: "info", ip: l.ip, actor: l.account, service: l.service,
					n: 1, nNew: 1, detail: map[string]any{"summary": fmt.Sprintf("%s signed in from %s, which %s lists.", l.account, l.ip, strings.Join(lists, " and "))}})
			}
		}
	}

	spread, err := s.sessionSpread(ctx, from, to, cursor)
	if err != nil {
		return nil, err
	}
	out = append(out, spread...)
	changes, err := s.riskyChanges(ctx, to, cursor, out)
	if err != nil {
		return nil, err
	}
	return append(out, changes...), nil
}

// knownAccount is what one account signed in from before.
type knownAccount struct {
	values  map[string]map[string]bool // kind -> values
	lastLoc string                     // "lat,lon|place" of the latest sign-in
	lastAt  time.Time
}

func (k *knownAccount) has(kind string) bool { return len(k.values[kind]) > 0 }

type lastPlace struct {
	lat, lon float64
	place    string
	at       time.Time
}

func (k *knownAccount) last() (lastPlace, bool) {
	coords, place, ok := strings.Cut(k.lastLoc, "|")
	if !ok {
		return lastPlace{}, false
	}
	a, b, ok := strings.Cut(coords, ",")
	lat, err1 := strconv.ParseFloat(a, 64)
	lon, err2 := strconv.ParseFloat(b, 64)
	if !ok || err1 != nil || err2 != nil {
		return lastPlace{}, false
	}
	return lastPlace{lat, lon, place, k.lastAt}, true
}

// knownFor loads the history of each account.
func (s *Server) knownFor(ctx context.Context, accounts map[string]bool) (map[string]*knownAccount, error) {
	out := map[string]*knownAccount{}
	var list []any
	for a := range accounts {
		list = append(list, a)
	}
	for len(list) > 0 {
		batch := list[:min(len(list), 200)]
		list = list[len(batch):]
		res, err := s.db.Query(ctx, `SELECT account, kind, value, last_seen FROM security_known WHERE account IN (`+placeholders(len(batch))+`)`, batch...)
		if err != nil {
			return nil, err
		}
		for _, row := range res.Rows {
			if len(row) != 4 {
				continue
			}
			a, kind, v := toString(row[0]), toString(row[1]), toString(row[2])
			k := out[a]
			if k == nil {
				k = &knownAccount{values: map[string]map[string]bool{}}
				out[a] = k
			}
			if kind == "last" {
				k.lastLoc = v
				k.lastAt, _ = time.Parse(tsLayout, toString(row[3]))
				continue
			}
			if k.values[kind] == nil {
				k.values[kind] = map[string]bool{}
			}
			k.values[kind][v] = true
		}
	}
	return out, nil
}

// remember records that account signed in with value.
func (s *Server) remember(ctx context.Context, k *knownAccount, account, kind, value string, at time.Time) error {
	value = clip(value, 191)
	ts := at.UTC().Format(tsLayout)
	if k.values[kind][value] {
		return s.db.Exec(ctx, `UPDATE security_known SET last_seen = ?, hits = hits + 1 WHERE account = ? AND kind = ? AND value = ?`, ts, account, kind, value)
	}
	if k.values[kind] == nil {
		k.values[kind] = map[string]bool{}
	}
	k.values[kind][value] = true
	err := s.db.Exec(ctx, `INSERT INTO security_known (account, kind, value, first_seen, last_seen, hits) VALUES (?, ?, ?, ?, ?, 1)`, clip(account, 191), kind, value, ts, ts)
	if err != nil && isDuplicateKey(err) {
		return nil // another instance recorded it first
	}
	return err
}

// rememberLast replaces the account's latest sign-in place.
func (s *Server) rememberLast(ctx context.Context, k *knownAccount, account string, g geoInfo, at time.Time) error {
	v := clip(strconv.FormatFloat(g.Lat, 'f', 3, 64)+","+strconv.FormatFloat(g.Lon, 'f', 3, 64)+"|"+g.place(), 191)
	if err := s.db.Exec(ctx, `DELETE FROM security_known WHERE account = ? AND kind = 'last'`, account); err != nil {
		return err
	}
	ts := at.UTC().Format(tsLayout)
	k.lastLoc, k.lastAt = v, at
	err := s.db.Exec(ctx, `INSERT INTO security_known (account, kind, value, first_seen, last_seen, hits) VALUES (?, 'last', ?, ?, ?, 1)`, clip(account, 191), v, ts, ts)
	if err != nil && isDuplicateKey(err) {
		return nil
	}
	return err
}

// isDuplicateKey is an insert that lost a race on a primary key.
func isDuplicateKey(err error) bool {
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "unique") || strings.Contains(m, "duplicate") || strings.Contains(m, "1062")
}

// sessionSpread finds accounts used from many addresses in one window that
// also span countries or devices.
func (s *Server) sessionSpread(ctx context.Context, from, to, cursor time.Time) ([]finding, error) {
	st := &s.security().st
	acct := accountExpr(s.q)
	signedIn := "(user_id IS NOT NULL OR (user_handle IS NOT NULL AND user_handle <> ''))"
	res, err := s.db.Query(ctx, `SELECT `+acct+` AS account FROM api_logs
WHERE created_at >= ? AND created_at < ? AND `+signedIn+` AND ip IS NOT NULL AND ip <> ''
GROUP BY `+acct+` HAVING COUNT(DISTINCT ip) >= ? LIMIT 100`, from.Format(tsLayout), to.Format(tsLayout), st.userIPs)
	if err != nil || len(res.Rows) == 0 {
		return nil, err
	}
	args := []any{cursor.Format(tsLayout), from.Format(tsLayout), to.Format(tsLayout)}
	for _, row := range res.Rows {
		args = append(args, toString(row[0]))
	}
	pairs, err := s.db.Query(ctx, `SELECT `+acct+` AS account, ip, COALESCE(user_agent, '') AS ua, COUNT(*) AS n, `+countIf("created_at >= ?")+` AS n_new, MAX(service) AS service
FROM api_logs WHERE created_at >= ? AND created_at < ? AND `+signedIn+` AND `+acct+` IN (`+placeholders(len(res.Rows))+`)
GROUP BY `+acct+`, ip, COALESCE(user_agent, '') LIMIT 5000`, args...)
	if err != nil {
		return nil, err
	}
	type spreadSum struct {
		ips, countries, devices map[string]bool
		n, nNew                 int64
		service                 string
	}
	sums := map[string]*spreadSum{}
	for _, row := range pairs.Rows {
		if len(row) != 6 || st.ignored(toString(row[1])) {
			continue
		}
		a := toString(row[0])
		sm := sums[a]
		if sm == nil {
			sm = &spreadSum{ips: map[string]bool{}, countries: map[string]bool{}, devices: map[string]bool{}, service: toString(row[5])}
			sums[a] = sm
		}
		ip := toString(row[1])
		sm.ips[ip] = true
		if c := s.lookupIP(ip).Country; c != "" {
			sm.countries[c] = true
		}
		if d := deviceOf(toString(row[2])); d != "" {
			sm.devices[d] = true
		}
		sm.n += toInt(row[3])
		sm.nNew += toInt(row[4])
	}
	win := fmtSpan(st.window)
	var out []finding
	for a, sm := range sums {
		if len(sm.ips) < st.userIPs || (len(sm.countries) < 2 && len(sm.devices) < 2) {
			continue
		}
		sev := "warning"
		if len(sm.countries) >= 3 {
			sev = "critical"
		}
		var where []string
		for c := range sm.countries {
			where = append(where, c)
		}
		summary := fmt.Sprintf("%s was used from %d addresses in %s", a, len(sm.ips), win)
		if len(where) > 1 {
			summary += fmt.Sprintf(" in %d countries (%s)", len(where), strings.Join(head(where, 5), ", "))
		}
		if len(sm.devices) > 1 {
			summary += fmt.Sprintf(" on %d kinds of device", len(sm.devices))
		}
		out = append(out, finding{kind: "session_spread", severity: sev, actor: a, service: sm.service, n: sm.n, nNew: sm.nNew,
			detail: map[string]any{"ips": len(sm.ips), "countries": where, "devices": len(sm.devices), "window": win,
				"summary": summary + ": one session in several hands looks like a stolen token."}})
	}
	return out, nil
}

// riskyChanges flags sensitive changes (password, email, MFA, payout) by
// accounts with a risky finding in the last security_risky_window, this
// run's included.
func (s *Server) riskyChanges(ctx context.Context, to, cursor time.Time, fresh []finding) ([]finding, error) {
	st := &s.security().st
	risky := map[string]string{} // account -> the finding kind that made it risky
	for _, f := range fresh {
		if f.actor != "" && f.kind != "new_login_device" {
			risky[strings.ToLower(f.actor)] = f.kind
		}
	}
	args := []any{to.Add(-st.riskyWindow).Format(tsLayout)}
	for _, k := range riskyKinds {
		args = append(args, k)
	}
	res, err := s.db.Query(ctx, `SELECT DISTINCT kind, actor, target, severity FROM security_events
WHERE created_at >= ? AND kind IN (`+placeholders(len(riskyKinds))+`) AND actor IS NOT NULL LIMIT 500`, args...)
	if err != nil {
		return nil, err
	}
	for _, row := range res.Rows {
		if len(row) != 4 {
			continue
		}
		kind, actor := toString(row[0]), toString(row[1])
		switch kind {
		case "account_targeted":
			if toString(row[3]) != "critical" {
				continue
			}
		case "login_as_other":
			actor = toString(row[2]) // the account that was signed in to
		}
		risky[strings.ToLower(actor)] = kind
	}
	if len(risky) == 0 {
		return nil, nil
	}
	// A sign-in name (an email) and the user id the app sets afterwards are
	// the same person: watch both.
	var names []string
	for a := range risky {
		names = append(names, a)
	}
	links, err := s.identityLinks(ctx, names)
	if err != nil {
		return nil, err
	}
	for _, a := range names {
		for _, alias := range links[a] {
			if _, ok := risky[alias]; !ok {
				risky[alias] = risky[a]
			}
		}
	}
	acct := accountExpr(s.q)
	args = []any{cursor.Format(tsLayout), to.Format(tsLayout)}
	for a := range risky {
		args = append(args, a)
	}
	changes, err := s.db.Query(ctx, `SELECT LOWER(`+acct+`) AS account, COALESCE(route, path) AS route, method, MAX(ip) AS ip, MAX(service) AS service, COUNT(*) AS n
FROM api_logs WHERE created_at >= ? AND created_at < ? AND LOWER(`+acct+`) IN (`+placeholders(len(risky))+`)
  AND method IN ('POST', 'PUT', 'PATCH', 'DELETE') AND status < 400 AND `+routeCond(s.q, st.sensitiveLikes)+`
GROUP BY LOWER(`+acct+`), COALESCE(route, path), method LIMIT 200`, args...)
	if err != nil {
		return nil, err
	}
	var out []finding
	for _, row := range changes.Rows {
		if len(row) != 6 {
			continue
		}
		a, route, method := toString(row[0]), maskPath(toString(row[1])), toString(row[2])
		why := strings.ReplaceAll(risky[a], "_", " ")
		out = append(out, finding{kind: "risky_account_change", severity: "critical", ip: toString(row[3]), actor: a,
			target: method + " " + route, service: toString(row[4]), n: toInt(row[5]), nNew: toInt(row[5]),
			detail: map[string]any{"after": risky[a], "route": route, "method": method,
				"summary": fmt.Sprintf("%s made a sensitive change (%s %s) within %s of a risky sign-in (%s).", a, method, route, fmtSpan(st.riskyWindow), why)}})
	}
	return out, nil
}

// deviceOf names the browser or client and the system from a user agent,
// such as "Chrome on Windows"; "" when there is none.
func deviceOf(ua string) string {
	if strings.TrimSpace(ua) == "" {
		return ""
	}
	l := strings.ToLower(ua)
	client := ""
	for _, c := range []struct{ match, name string }{
		{"edg/", "Edge"}, {"opr/", "Opera"}, {"samsungbrowser", "Samsung Internet"}, {"firefox/", "Firefox"},
		{"chrome/", "Chrome"}, {"crios/", "Chrome"}, {"fxios/", "Firefox"}, {"safari/", "Safari"},
		{"okhttp", "okhttp"}, {"dart:io", "Dart"}, {"cfnetwork", "iOS app"}, {"curl/", "curl"}, {"wget/", "Wget"},
		{"python-requests", "Python requests"}, {"python-urllib", "Python"}, {"go-http-client", "Go"},
		{"postmanruntime", "Postman"}, {"insomnia", "Insomnia"}, {"axios", "axios"}, {"node-fetch", "Node"}, {"java/", "Java"},
	} {
		if strings.Contains(l, c.match) {
			client = c.name
			break
		}
	}
	if client == "" {
		client, _, _ = strings.Cut(ua, "/")
		client = clip(strings.TrimSpace(client), 40)
	}
	system := ""
	for _, o := range []struct{ match, name string }{
		{"windows", "Windows"}, {"android", "Android"}, {"iphone", "iOS"}, {"ipad", "iPadOS"}, {"cros", "ChromeOS"},
		{"mac os x", "macOS"}, {"macintosh", "macOS"}, {"linux", "Linux"},
	} {
		if strings.Contains(l, o.match) {
			system = o.name
			break
		}
	}
	if system == "" {
		return client
	}
	return client + " on " + system
}
