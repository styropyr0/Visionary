package analytics

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	sdk "github.com/styropyr0/visionary/sdks/go"

	"github.com/styropyr0/visionary/internal/uptimecore"
)

// Posture is what is weak right now rather than what happened: open until
// fixed. Items from the data are read live; header, TLS and package checks
// run in the background and are kept in security_posture_scans

const (
	postureLookback = 7 * 24 * time.Hour
	osvEvery        = 6 * time.Hour
	osvURL          = "https://api.osv.dev/v1"
)

type postureItem struct {
	Key      string         `json:"key"`
	Kind     string         `json:"kind"`
	Severity string         `json:"severity"`
	Title    string         `json:"title"`
	Target   string         `json:"target"`
	Fix      string         `json:"fix"`
	Since    string         `json:"since,omitempty"`
	Last     string         `json:"last,omitempty"`
	Detail   map[string]any `json:"detail,omitempty"`
}

func (s *Server) securityPostureSchema() []string {
	key, ts, tail := "TEXT", "TEXT", ""
	if s.q.d == sdk.MySQL {
		key, ts, tail = "VARCHAR(191)", "VARCHAR(32)", " CHARACTER SET utf8mb4"
	}
	return []string{strings.NewReplacer("{key}", key, "{ts}", ts, "{tail}", tail).Replace(`CREATE TABLE IF NOT EXISTS security_posture_scans (
    item_key   {key} NOT NULL PRIMARY KEY,
    kind       {key} NOT NULL,
    severity   TEXT NOT NULL,
    title      TEXT NOT NULL,
    target     TEXT NOT NULL,
    fix        TEXT NOT NULL,
    detail     TEXT,
    first_seen {ts} NOT NULL,
    checked_at {ts} NOT NULL
){tail}`)}
}

func (s *Server) handleSecurityPosture(w http.ResponseWriter, r *http.Request) {
	items, err := s.posture(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	sev := map[string]int{}
	for _, it := range items {
		sev[it.Severity]++
	}
	osv, _ := s.readSetting(r.Context(), "security.osv_checked")
	headers, _ := s.readSetting(r.Context(), "security.headers_checked")
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "counts": sev, "osv_enabled": s.confBool("security_osv"),
		"osv_checked": osv, "headers_checked": headers})
}

func (s *Server) readSetting(ctx context.Context, name string) (string, error) {
	res, err := s.db.Query(ctx, `SELECT value FROM visionary_settings WHERE name = ?`, name)
	if err != nil || len(res.Rows) == 0 {
		return "", err
	}
	return toString(res.Rows[0][0]), nil
}

func (s *Server) posture(ctx context.Context) ([]postureItem, error) {
	since := time.Now().UTC().Add(-postureLookback).Format(tsLayout)
	var items []postureItem
	parts := []func(context.Context, string) ([]postureItem, error){s.postureExposed, s.postureDebug, s.postureAnonymous, s.postureHosts}
	if scopeFrom(ctx) == nil {
		// scans aren't split by service, so people limited to some services don't see them
		parts = append(parts, s.postureScans)
	}
	for _, part := range parts {
		got, err := part(ctx, since)
		if err != nil {
			return nil, err
		}
		items = append(items, got...)
	}
	slices.SortStableFunc(items, func(a, b postureItem) int {
		if d := severityRank[b.Severity] - severityRank[a.Severity]; d != 0 {
			return d
		}
		return strings.Compare(a.Title, b.Title)
	})
	if items == nil {
		items = []postureItem{}
	}
	return items, nil
}

func (s *Server) postureExposed(ctx context.Context, since string) ([]postureItem, error) {
	res, err := s.db.Query(ctx, `SELECT path, COUNT(*), MIN(created_at), MAX(created_at), MAX(status), COUNT(DISTINCT ip)
FROM api_logs WHERE created_at >= ? AND `+probeCond(s.q)+` AND status >= 200 AND status < 300
GROUP BY path ORDER BY COUNT(*) DESC LIMIT 25`, since)
	if err != nil {
		return nil, dbErr(err)
	}
	var out []postureItem
	for _, row := range res.Rows {
		path := toString(row[0])
		out = append(out, postureItem{Key: "exposed:" + path, Kind: "exposed_path", Severity: "critical", Target: path,
			Title: path + " answers scanners with " + fmt.Sprint(toInt(row[4])),
			Fix:   "Stop serving it (deny dotfiles and admin paths at the proxy or router), then rotate any secret it held.",
			Since: toString(row[2]), Last: toString(row[3]), Detail: map[string]any{"hits": toInt(row[1]), "ips": toInt(row[5])}})
	}
	return out, nil
}

var debugPaths = []string{"%/debug/pprof%", "%/debug/vars%", "%/actuator/%", "/metrics", "%/h2-console%", "%/_profiler%", "%/graphiql%", "%/__debug__%", "%/swagger-ui%", "%/api-docs%"}

func (s *Server) postureDebug(ctx context.Context, since string) ([]postureItem, error) {
	res, err := s.db.Query(ctx, `SELECT COALESCE(route, path), service, COUNT(*), MIN(created_at), MAX(created_at), COUNT(DISTINCT ip)
FROM api_logs WHERE created_at >= ? AND `+likeAny(s.q, "LOWER(path)", debugPaths)+` AND status >= 200 AND status < 300
  AND ip IS NOT NULL AND ip NOT IN ('127.0.0.1', '::1') AND ip NOT LIKE '10.%' AND ip NOT LIKE '192.168.%' AND ip NOT LIKE '172.%'
GROUP BY COALESCE(route, path), service ORDER BY COUNT(*) DESC LIMIT 25`, since)
	if err != nil {
		return nil, dbErr(err)
	}
	var out []postureItem
	for _, row := range res.Rows {
		route, svc := toString(row[0]), toString(row[1])
		out = append(out, postureItem{Key: "debug:" + svc + ":" + route, Kind: "debug_endpoint", Severity: "warning", Target: route,
			Title: route + " is reachable from outside" + inService(svc),
			Fix:   "Serve debug, metrics and API docs on an internal port or behind sign-in.",
			Since: toString(row[3]), Last: toString(row[4]), Detail: map[string]any{"requests": toInt(row[2]), "ips": toInt(row[5]), "service": svc}})
	}
	return out, nil
}

func inService(svc string) string {
	if svc == "" {
		return ""
	}
	return " in " + svc
}

// postureAnonymous finds changes to sensitive or admin routes that went
// through with no signed-in user, in services that do name their users
func (s *Server) postureAnonymous(ctx context.Context, since string) ([]postureItem, error) {
	st := &s.security().st
	likes := append(slices.Clone(st.sensitiveLikes), "%/admin%")
	res, err := s.db.Query(ctx, `SELECT a.method, COALESCE(a.route, a.path), COALESCE(a.service, ''), COUNT(*), MIN(a.created_at), MAX(a.created_at), COUNT(DISTINCT a.ip)
FROM api_logs AS a
WHERE a.created_at >= ? AND a.method IN ('POST', 'PUT', 'PATCH', 'DELETE') AND a.status >= 200 AND a.status < 300
  AND (a.user_id IS NULL OR `+s.q.text("a.user_id")+` = '') AND (a.user_handle IS NULL OR a.user_handle = '')
  AND `+strings.ReplaceAll(routeCond(s.q, likes), "COALESCE(route", "COALESCE(a.route")+`
  AND NOT `+st.loginCond(s.q)+`
  AND EXISTS (SELECT 1 FROM api_logs AS u WHERE u.created_at >= ? AND COALESCE(u.service, '') = COALESCE(a.service, '') AND u.user_id IS NOT NULL)
GROUP BY a.method, COALESCE(a.route, a.path), COALESCE(a.service, '')
HAVING COUNT(*) >= 3 ORDER BY COUNT(*) DESC LIMIT 25`, since, since)
	if err != nil {
		return nil, dbErr(err)
	}
	var out []postureItem
	for _, row := range res.Rows {
		method, route, svc := toString(row[0]), toString(row[1]), toString(row[2])
		out = append(out, postureItem{Key: "anon:" + svc + ":" + method + " " + route, Kind: "anonymous_change", Severity: "warning", Target: method + " " + route,
			Title: method + " " + route + " changed data with no signed-in user" + inService(svc),
			Fix:   "Check that the route requires sign-in. If it does, call set_user before the handler so Visionary can see who it was.",
			Since: toString(row[4]), Last: toString(row[5]), Detail: map[string]any{"requests": toInt(row[3]), "ips": toInt(row[6]), "service": svc}})
	}
	return out, nil
}

func (s *Server) postureHosts(ctx context.Context, since string) ([]postureItem, error) {
	var out []postureItem
	pending, err := s.db.Query(ctx, `SELECT e.host, MAX(e.count), MAX(e.created_at) FROM security_events AS e
JOIN (SELECT host, MAX(created_at) AS last_at FROM security_events WHERE kind = 'updates_pending' AND created_at >= ? GROUP BY host) AS l
  ON l.host = e.host AND l.last_at = e.created_at
WHERE e.kind = 'updates_pending' GROUP BY e.host`, since)
	if err != nil {
		return nil, dbErr(err)
	}
	for _, row := range pending.Rows {
		if n := toInt(row[1]); n > 0 {
			host := toString(row[0])
			out = append(out, postureItem{Key: "updates:" + host, Kind: "updates_pending", Severity: "warning", Target: host,
				Title: fmt.Sprintf("%d security update(s) waiting on %s", n, host), Fix: "Install them (apt upgrade, dnf upgrade) and reboot if the kernel changed.",
				Last: toString(row[2]), Detail: map[string]any{"updates": n, "host": host}})
		}
	}
	recent := time.Now().UTC().Add(-48 * time.Hour).Format(tsLayout)
	risky, err := s.db.Query(ctx, `SELECT kind, host, target, MIN(created_at), MAX(created_at) FROM security_events
WHERE source = 'host' AND kind IN ('container_privileged', 'container_root') AND created_at >= ?
  AND COALESCE(detail, '') NOT LIKE '%visionary-agent%' 
GROUP BY kind, host, target ORDER BY kind, host LIMIT 50`, recent)
	if err != nil {
		return nil, dbErr(err)
	}
	for _, row := range risky.Rows {
		kind, host, target := toString(row[0]), toString(row[1]), toString(row[2])
		it := postureItem{Key: kind + ":" + host + ":" + target, Kind: kind, Target: target, Since: toString(row[3]), Last: toString(row[4]), Detail: map[string]any{"host": host}}
		if kind == "container_privileged" {
			it.Severity, it.Title, it.Fix = "critical", target+" on "+host+" can take over the host", "Drop privileged mode, host networking, the Docker socket and extra capabilities."
		} else {
			it.Severity, it.Title, it.Fix = "info", target+" on "+host+" runs as root", "Set a non-root USER in the image or run it with --user."
		}
		out = append(out, it)
	}
	ports, err := s.db.Query(ctx, `SELECT o.host, o.target, MAX(o.created_at) FROM security_events AS o
WHERE o.kind = 'port_opened' AND o.created_at >= ?
  AND NOT EXISTS (SELECT 1 FROM security_events AS c WHERE c.kind = 'port_closed' AND c.host = o.host AND c.target = o.target AND c.created_at > o.created_at)
GROUP BY o.host, o.target ORDER BY MAX(o.created_at) DESC LIMIT 25`, since)
	if err != nil {
		return nil, dbErr(err)
	}
	for _, row := range ports.Rows {
		host, target := toString(row[0]), toString(row[1])
		out = append(out, postureItem{Key: "port:" + host + ":" + target, Kind: "port_opened", Severity: "info", Target: target,
			Title: "New listening port " + target + " on " + host, Fix: "If it shouldn't be public, bind it to 127.0.0.1 or close it in the firewall.",
			Last: toString(row[2]), Detail: map[string]any{"host": host}})
	}
	return out, nil
}

func (s *Server) postureScans(ctx context.Context, _ string) ([]postureItem, error) {
	res, err := s.db.Query(ctx, `SELECT item_key, kind, severity, title, target, fix, detail, first_seen, checked_at FROM security_posture_scans ORDER BY item_key`)
	if err != nil {
		return nil, dbErr(err)
	}
	var out []postureItem
	for _, row := range res.Rows {
		it := postureItem{Key: toString(row[0]), Kind: toString(row[1]), Severity: toString(row[2]), Title: toString(row[3]), Target: toString(row[4]),
			Fix: toString(row[5]), Since: toString(row[7]), Last: toString(row[8])}
		if d := toString(row[6]); d != "" {
			_ = json.Unmarshal([]byte(d), &it.Detail)
		}
		out = append(out, it)
	}
	return out, nil
}

// replaceScans swaps one kind's scanned items, keeping first_seen of the ones
// still there
func (s *Server) replaceScans(ctx context.Context, kinds []string, items []postureItem) error {
	ph := strings.TrimSuffix(strings.Repeat("?, ", len(kinds)), ", ")
	args := make([]any, len(kinds))
	for i, k := range kinds {
		args[i] = k
	}
	res, err := s.db.Query(ctx, `SELECT item_key, first_seen FROM security_posture_scans WHERE kind IN (`+ph+`)`, args...)
	if err != nil {
		return dbErr(err)
	}
	first := map[string]string{}
	for _, row := range res.Rows {
		first[toString(row[0])] = toString(row[1])
	}
	now := nowTS()
	if err := s.db.Exec(ctx, `DELETE FROM security_posture_scans WHERE kind IN (`+ph+`)`, args...); err != nil {
		return dbErr(err)
	}
	seen := map[string]bool{}
	for _, it := range items {
		if seen[it.Key] {
			continue
		}
		seen[it.Key] = true
		detail, _ := json.Marshal(it.Detail)
		if err := s.db.Exec(ctx, `INSERT INTO security_posture_scans (item_key, kind, severity, title, target, fix, detail, first_seen, checked_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			clip(it.Key, 191), it.Kind, it.Severity, it.Title, it.Target, it.Fix, string(detail), fallback(first[it.Key], now), now); err != nil {
			return dbErr(err)
		}
	}
	return nil
}

// postureLoop checks headers and TLS hourly and packages every osvEvery
func (s *Server) postureLoop(ctx context.Context) {
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		if err := s.scanHeaders(ctx); err != nil && ctx.Err() == nil {
			s.logger.Warn("security header check", "err", err)
		}
		last, _ := s.readSetting(ctx, "security.osv_checked")
		if t, err := time.Parse(tsLayout, last); s.confBool("security_osv") && (err != nil || time.Since(t) >= osvEvery) {
			if err := s.scanPackages(ctx); err != nil && ctx.Err() == nil {
				s.logger.Warn("vulnerable package check", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func guardedClient(timeout time.Duration) *http.Client {
	d := &net.Dialer{Timeout: 10 * time.Second, Control: uptimecore.DialGuard}
	return &http.Client{Timeout: timeout, Transport: &http.Transport{DialContext: d.DialContext, TLSHandshakeTimeout: 10 * time.Second},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// scanHeaders reads the security headers and certificate of every URL an
// uptime check watches
func (s *Server) scanHeaders(ctx context.Context) error {
	res, err := s.db.Query(ctx, `SELECT DISTINCT url FROM uptime_checks WHERE enabled = 1 AND (url LIKE 'https://%' OR url LIKE 'http://%') LIMIT 100`)
	if err != nil {
		return dbErr(err)
	}
	client := guardedClient(15 * time.Second)
	var items []postureItem
	origins := map[string]bool{}
	for _, row := range res.Rows {
		u, err := url.Parse(toString(row[0]))
		if err != nil || u.Host == "" || origins[u.Scheme+"://"+u.Host] {
			continue
		}
		origin := u.Scheme + "://" + u.Host
		origins[origin] = true
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/", nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "Visionary security check")
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		items = append(items, headerItems(origin, resp)...)
	}
	if err := s.replaceScans(ctx, []string{"missing_headers", "tls_expiry", "no_tls"}, items); err != nil {
		return err
	}
	return s.writeSettings(ctx, map[string]string{"security.headers_checked": nowTS()})
}

func headerItems(origin string, resp *http.Response) []postureItem {
	var out []postureItem
	h := resp.Header
	https := strings.HasPrefix(origin, "https://")
	if !https {
		return []postureItem{{Key: "no_tls:" + origin, Kind: "no_tls", Severity: "warning", Target: origin, Title: origin + " is served without TLS",
			Fix: "Serve it over HTTPS and redirect plain HTTP to it."}}
	}
	var missing []string
	if h.Get("Strict-Transport-Security") == "" {
		missing = append(missing, "Strict-Transport-Security")
	}
	csp := h.Get("Content-Security-Policy")
	if csp == "" && strings.Contains(h.Get("Content-Type"), "html") {
		missing = append(missing, "Content-Security-Policy")
	}
	if !strings.EqualFold(h.Get("X-Content-Type-Options"), "nosniff") {
		missing = append(missing, "X-Content-Type-Options")
	}
	if h.Get("X-Frame-Options") == "" && !strings.Contains(csp, "frame-ancestors") && strings.Contains(h.Get("Content-Type"), "html") {
		missing = append(missing, "X-Frame-Options")
	}
	if len(missing) > 0 {
		sev := "info"
		if slices.Contains(missing, "Strict-Transport-Security") {
			sev = "warning"
		}
		out = append(out, postureItem{Key: "headers:" + origin, Kind: "missing_headers", Severity: sev, Target: origin,
			Title: origin + " is missing " + strings.Join(missing, ", "),
			Fix:   "Add them at the proxy or in your app's middleware.", Detail: map[string]any{"missing": missing}})
	}
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		left := time.Until(resp.TLS.PeerCertificates[0].NotAfter)
		if left < 21*24*time.Hour {
			sev := "warning"
			if left < 7*24*time.Hour {
				sev = "critical"
			}
			out = append(out, postureItem{Key: "tls:" + origin, Kind: "tls_expiry", Severity: sev, Target: origin,
				Title: fmt.Sprintf("The certificate for %s expires in %d day(s)", origin, max(0, int(left.Hours()/24))),
				Fix:   "Renew it, and check why automatic renewal didn't.", Detail: map[string]any{"expires": resp.TLS.PeerCertificates[0].NotAfter.UTC().Format(time.RFC3339)}})
		}
	}
	return out
}

type osvPackage struct{ Ecosystem, Name, Version string }

// scanPackages looks up the packages each service reported last
func (s *Server) scanPackages(ctx context.Context) error {
	since := time.Now().UTC().Add(-30 * 24 * time.Hour).Format(tsLayout)
	res, err := s.db.Query(ctx, `SELECT p.ecosystem, p.name, p.pkg_version, COALESCE(p.service, '') FROM app_packages AS p
JOIN (SELECT COALESCE(service, '') AS svc, MAX(created_at) AS last_at FROM app_packages WHERE created_at >= ? GROUP BY COALESCE(service, '')) AS l
  ON COALESCE(p.service, '') = l.svc AND p.created_at = l.last_at
LIMIT 20000`, since)
	if err != nil {
		return dbErr(err)
	}
	services := map[osvPackage][]string{}
	var pkgs []osvPackage
	for _, row := range res.Rows {
		p := osvPackage{toString(row[0]), toString(row[1]), toString(row[2])}
		if _, ok := services[p]; !ok {
			pkgs = append(pkgs, p)
		}
		if svc := toString(row[3]); !slices.Contains(services[p], svc) {
			services[p] = append(services[p], svc)
		}
	}
	items, err := osvLookup(ctx, &http.Client{Timeout: 60 * time.Second}, osvURL, pkgs, services)
	if err != nil {
		return err
	}
	if err := s.replaceScans(ctx, []string{"vulnerable_package"}, items); err != nil {
		return err
	}
	return s.writeSettings(ctx, map[string]string{"security.osv_checked": nowTS()})
}

type osvVuln struct {
	ID       string   `json:"id"`
	Summary  string   `json:"summary"`
	Aliases  []string `json:"aliases"`
	Affected []struct {
		Package struct {
			Ecosystem string `json:"ecosystem"`
			Name      string `json:"name"`
		} `json:"package"`
		Ranges []struct {
			Events []map[string]string `json:"events"`
		} `json:"ranges"`
	} `json:"affected"`
	DatabaseSpecific struct {
		Severity string `json:"severity"`
	} `json:"database_specific"`
}

func osvLookup(ctx context.Context, client *http.Client, base string, pkgs []osvPackage, services map[osvPackage][]string) ([]postureItem, error) {
	type query struct {
		Package struct {
			Ecosystem string `json:"ecosystem"`
			Name      string `json:"name"`
		} `json:"package"`
		Version string `json:"version"`
	}
	hits := map[osvPackage][]string{}
	for i := 0; i < len(pkgs); i += 1000 {
		chunk := pkgs[i:min(i+1000, len(pkgs))]
		qs := make([]query, len(chunk))
		for j, p := range chunk {
			qs[j].Package.Ecosystem, qs[j].Package.Name, qs[j].Version = p.Ecosystem, p.Name, p.Version
		}
		var out struct {
			Results []struct {
				Vulns []struct {
					ID string `json:"id"`
				} `json:"vulns"`
			} `json:"results"`
		}
		if err := osvCall(ctx, client, http.MethodPost, base+"/querybatch", map[string]any{"queries": qs}, &out); err != nil {
			return nil, err
		}
		for j, r := range out.Results {
			if j >= len(chunk) {
				break
			}
			for _, v := range r.Vulns {
				hits[chunk[j]] = append(hits[chunk[j]], v.ID)
			}
		}
	}
	details := map[string]*osvVuln{}
	var items []postureItem
	fetched := 0
	for _, p := range pkgs {
		ids := hits[p]
		if len(ids) == 0 {
			continue
		}
		sev, fixed := "info", ""
		var names []string
		for _, id := range ids {
			v := details[id]
			if v == nil && fetched < 300 {
				fetched++
				v = &osvVuln{}
				if err := osvCall(ctx, client, http.MethodGet, base+"/vulns/"+url.PathEscape(id), nil, v); err != nil {
					v = &osvVuln{ID: id}
				}
				details[id] = v
			}
			if v == nil {
				v = &osvVuln{ID: id}
			}
			if s := osvSeverity(v.DatabaseSpecific.Severity); severityRank[s] > severityRank[sev] {
				sev = s
			}
			if f := osvFixed(v, p); f != "" && (fixed == "" || versionLess(fixed, f)) {
				fixed = f
			}
			names = append(names, fallback(firstCVE(v.Aliases), id)+": "+fallback(v.Summary, "known vulnerability"))
		}
		if sev == "info" {
			sev = "warning"
		}
		fix := "Upgrade " + p.Name
		if fixed != "" {
			fix += " to " + fixed + " or later"
		}
		sum := sha256.Sum256([]byte(p.Ecosystem + "/" + p.Name + "@" + p.Version))
		items = append(items, postureItem{Key: "vuln:" + hex.EncodeToString(sum[:12]), Kind: "vulnerable_package", Severity: sev,
			Target: p.Name + "@" + p.Version, Title: fmt.Sprintf("%s %s has %d known vulnerabilit%s", p.Name, p.Version, len(ids), map[bool]string{true: "y", false: "ies"}[len(ids) == 1]),
			Fix: fix + ".", Detail: map[string]any{"ecosystem": p.Ecosystem, "ids": ids, "vulns": head(names, 10), "services": services[p], "fixed": fixed}})
	}
	return items, nil
}

func osvCall(ctx context.Context, client *http.Client, method, u string, body, out any) error {
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Visionary")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("osv.dev answered %s", resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(out)
}

func osvSeverity(v string) string {
	switch strings.ToUpper(v) {
	case "CRITICAL", "HIGH":
		return "critical"
	case "MODERATE", "MEDIUM":
		return "warning"
	}
	return "info"
}

func osvFixed(v *osvVuln, p osvPackage) string {
	best := ""
	for _, a := range v.Affected {
		if a.Package.Name != p.Name {
			continue
		}
		for _, r := range a.Ranges {
			for _, e := range r.Events {
				if f := e["fixed"]; f != "" && versionLess(p.Version, f) && (best == "" || versionLess(f, best)) {
					best = f
				}
			}
		}
	}
	return best
}

func firstCVE(aliases []string) string {
	for _, a := range aliases {
		if strings.HasPrefix(a, "CVE-") {
			return a
		}
	}
	return ""
}

// versionLess compares dotted versions numerically where it can, enough to
// pick the nearest fixed release
func versionLess(a, b string) bool {
	pa, pb := strings.FieldsFunc(strings.TrimPrefix(a, "v"), versionSep), strings.FieldsFunc(strings.TrimPrefix(b, "v"), versionSep)
	for i := 0; i < max(len(pa), len(pb)); i++ {
		var x, y string
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x == y {
			continue
		}
		var nx, ny int
		_, ex := fmt.Sscan(x, &nx)
		_, ey := fmt.Sscan(y, &ny)
		if ex == nil && ey == nil {
			return nx < ny
		}
		return x < y
	}
	return false
}

func versionSep(r rune) bool { return r == '.' || r == '-' || r == '+' }
