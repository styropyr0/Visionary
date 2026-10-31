package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/publicsuffix"
)

const runParallel = 4

// executeBatch runs each check of a batch once, a few at a time, and records
// how each did. It outlives the request that started it.
func (s *Server) executeBatch(b runBatch, items []runItem, checks []UptimeCheck, override string) {
	ctx, cancel := context.WithTimeout(context.Background(), runBatchTTL)
	defer cancel()
	byID := make(map[int64]UptimeCheck, len(checks))
	for _, c := range checks {
		byID[c.ID] = c
	}
	run := runnerFor(s)
	sem := make(chan struct{}, runParallel)
	var wg sync.WaitGroup
	for _, it := range items {
		c, ok := byID[it.CheckID]
		if !ok {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			defer func() {
				if p := recover(); p != nil {
					s.logger.Error("uptime run panicked", "check_id", c.ID, "panic", fmt.Sprint(p))
					s.saveItem(ctx, it.id, runItem{Status: "error", Error: "the run crashed"})
				}
			}()
			_ = s.db.Exec(ctx, `UPDATE uptime_run_items SET status = 'running', started_at = ? WHERE id = ?`, uptimeNow(), it.id)
			s.saveItem(ctx, it.id, s.runOne(ctx, run, c, override))
		}()
	}
	wg.Wait()
	if err := s.finishBatch(context.WithoutCancel(ctx), b.ID); err != nil {
		s.logger.Warn("uptime run not closed", "batch", b.ID, "err", err)
	}
}

// runOne runs one check, always from the server location. Against its own
// URL, on a check that runs from the server, the result is recorded like a
// scheduled one; otherwise it is kept in the batch only, so a check that
// runs elsewhere never gets results from a location it doesn't use.
func (s *Server) runOne(ctx context.Context, run *uptimeRunner, c UptimeCheck, override string) runItem {
	if c.Kind == "playwright" {
		return runItem{Status: "skipped", Error: "Playwright checks report from your test runs; they can't be started here"}
	}
	if c.Kind == "heartbeat" {
		return runItem{Status: "skipped", Error: "Heartbeat checks wait for pings from your job; they can't be started here"}
	}
	// TCP, DNS, ping and certificate checks have a host, not a URL, so they
	// run against their own target whatever the override.
	if hostKinds[c.Kind] {
		override = ""
	}
	recordIt := override == "" && c.runsAt(serverLocation)
	if override == "" && !recordIt && !probeKinds[c.Kind] && c.Kind != "browser" {
		return runItem{Status: "skipped", Error: c.Kind + " checks that don't run from the server can't be run from CI yet"}
	}
	if override != "" {
		var err error
		if c, err = withOrigin(c, override); err != nil {
			return runItem{Status: "error", Error: err.Error()}
		}
	}
	var res uptimeResult
	var detail any
	switch {
	case probeKinds[c.Kind]:
		cctx, cancel := context.WithTimeout(ctx, 2*c.core().AttemptTimeout()+5*time.Second)
		defer cancel()
		res = run.probeWithRetry(cctx, c)
		if res.Skip != "" {
			return runItem{Status: "error", Error: res.Skip}
		}
		detail = res.Detail
		if recordIt {
			if err := run.record(cctx, c, res); err != nil {
				return runItem{Status: "error", Error: "result not saved: " + userMessage(err)}
			}
		}
	case c.Kind == "browser":
		if !run.browser.Available() {
			st := run.browser.Status()
			return runItem{Status: "error", Error: strings.TrimSpace(st.Error + ". " + st.Help)}
		}
		cctx, cancel := context.WithTimeout(ctx, 2*time.Duration(c.TimeoutMs)*time.Millisecond+2*time.Minute)
		defer cancel()
		r, b, err := run.runBrowser(cctx, c)
		if err != nil {
			return runItem{Status: "error", Error: "the browser did not start: " + userMessage(err)}
		}
		res, detail = r, map[string]any{"steps": b.Steps, "failed_step": b.FailedStep, "trace_id": b.TraceID, "warning": b.Warning}
		if recordIt {
			if err := run.record(cctx, c, res); err == nil {
				_ = run.recordBrowser(cctx, c, res, b)
			}
		}
	default:
		if !recordIt {
			return runItem{Status: "skipped", Error: c.Kind + " checks can't run against another URL yet"}
		}
		return s.runRecorded(ctx, run, c)
	}
	return itemOf(res, detail)
}

var (
	// probeKinds run as one probe, with uptimecore.
	probeKinds = map[string]bool{"http": true, "api": true, "tcp": true, "dns": true, "ping": true, "ssl": true}
	// hostKinds point at a host rather than a URL.
	hostKinds = map[string]bool{"tcp": true, "dns": true, "ping": true, "ssl": true, "heartbeat": true}
)

// runRecorded runs a check of a kind this file doesn't know through the
// scheduler's own path, then reads back the result it recorded.
func (s *Server) runRecorded(ctx context.Context, run *uptimeRunner, c UptimeCheck) runItem {
	start := uptimeNow()
	run.execute(ctx, c)
	rows, err := s.db.Query(ctx, `SELECT created_at, ok, status, duration_ms, error FROM uptime_results
WHERE check_id = ? AND created_at >= ? ORDER BY created_at DESC LIMIT 1`, c.ID, start)
	if err != nil || len(rows.Rows) == 0 || len(rows.Rows[0]) != 5 {
		return runItem{Status: "error", Error: "the check did not record a result"}
	}
	row := rows.Rows[0]
	ms, _ := asFloat(row[3])
	return itemOf(uptimeResult{OK: toInt(row[1]) == 1, Status: int(toInt(row[2])), DurationMs: ms, Error: toString(row[4])}, nil)
}

func itemOf(res uptimeResult, detail any) runItem {
	it := runItem{Status: "failed", HTTPStatus: int64(res.Status), Error: res.Error}
	if res.OK {
		it.Status = "passed"
	}
	ms := res.DurationMs
	it.DurationMs = &ms
	if detail != nil {
		it.Detail, _ = json.Marshal(detail)
	}
	return it
}

func (s *Server) saveItem(ctx context.Context, id int64, it runItem) {
	ctx = context.WithoutCancel(ctx)
	ms := 0.0
	if it.DurationMs != nil {
		ms = *it.DurationMs
	}
	detail := string(it.Detail)
	if detail == "" {
		detail = "{}"
	}
	if err := s.db.Exec(ctx, `UPDATE uptime_run_items SET status = ?, finished_at = ?, duration_ms = ?, http_status = ?, error = ?, detail = ? WHERE id = ?`,
		it.Status, uptimeNow(), ms, it.HTTPStatus, ciClip(it.Error, 1000), detail, id); err != nil {
		s.logger.Warn("uptime run result not saved", "item", id, "err", err)
	}
}

// --- base URL override ---

// parseOverride checks an override origin against every picked check: it
// must be on the same site as the check's own URL, or match the hosts an
// admin allows in uptime_override_hosts. Otherwise a CI key could send a
// check's secret headers to any server.
func (s *Server) parseOverride(raw string, checks []UptimeCheck) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return "", errors.New("environment_url_override must be an http:// or https:// origin, like https://pr-12.example.com")
	}
	if strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("environment_url_override takes an origin only (scheme, host and port), without a path")
	}
	origin := u.Scheme + "://" + u.Host
	allowed := splitList(s.conf("uptime_override_hosts"))
	for _, c := range checks {
		if c.Kind == "playwright" || hostKinds[c.Kind] {
			continue
		}
		cu, err := url.Parse(c.URL)
		if err != nil {
			continue
		}
		if siteOf(cu.Hostname()) != siteOf(u.Hostname()) && !hostAllowed(u.Hostname(), allowed) {
			return "", fmt.Errorf("the override host %s is not on the same site as check %q (%s); an admin can allow it in Settings → Server → Other → Hosts runs from CI may target",
				u.Hostname(), c.Name, cu.Hostname())
		}
	}
	return origin, nil
}

// siteOf is a host's registrable domain (example.co.uk for a.b.example.co.uk),
// or the host itself for IPs and single labels.
func siteOf(host string) string {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if site, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil {
		return site
	}
	return host
}

func hostAllowed(host string, patterns []string) bool {
	host = strings.ToLower(host)
	for _, p := range patterns {
		if ok, _ := path.Match(strings.ToLower(p), host); ok {
			return true
		}
	}
	return false
}

// withOrigin points a check at another origin: its URL and every URL in it
// (browser steps, request steps) on the same site as its URL, keeping paths
// and queries. Other sites a check visits, such as a sign-in provider, stay.
func withOrigin(c UptimeCheck, origin string) (UptimeCheck, error) {
	base, err := url.Parse(c.URL)
	if err != nil {
		return c, errors.New("the check's URL is not valid")
	}
	site := siteOf(base.Hostname())
	raw, err := json.Marshal(c)
	if err != nil {
		return c, err
	}
	out := originRe.ReplaceAllStringFunc(string(raw), func(m string) string {
		sub := originRe.FindStringSubmatch(m)
		u, err := url.Parse(sub[1])
		if err != nil || siteOf(u.Hostname()) != site {
			return m
		}
		return origin + sub[2]
	})
	var next UptimeCheck
	if err := json.Unmarshal([]byte(out), &next); err != nil {
		return c, err
	}
	// Cookie domains and wildcard trace origins carry no scheme, so the
	// rewrite above misses them.
	o, _ := url.Parse(origin)
	for i, st := range next.Steps {
		if st.Action == "set_cookie" && st.Scope != "" && !strings.Contains(st.Scope, "://") && siteOf(strings.TrimPrefix(st.Scope, ".")) == site {
			next.Steps[i].Scope = o.Hostname()
		}
	}
	for i, t := range next.TraceOrigins {
		if h, ok := strings.CutPrefix(t, base.Scheme+"://*."); ok && siteOf(h) == site {
			next.TraceOrigins[i] = origin
		}
	}
	return next, nil
}

// originRe finds origins inside JSON strings, with the character after them.
var originRe = regexp.MustCompile(`(https?://[A-Za-z0-9.\-\[\]:]+)([/?#"\\])`)

// overrideURL is u with its origin replaced, for showing where a run went.
func overrideURL(u, origin string) string {
	if origin == "" {
		return u
	}
	p, err := url.Parse(u)
	if err != nil {
		return u
	}
	o, _ := url.Parse(origin)
	p.Scheme, p.Host = o.Scheme, o.Host
	return p.String()
}
