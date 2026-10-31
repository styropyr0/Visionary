package analytics

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/styropyr0/visionary/internal/uptimecore"
)

// The runner runs the server's own checks with internal/uptimecore, shared with
// the host agent's location runner. Here: loading checks and storing results.

// uptimeRunners holds each server's runner, so handlers can reach it without
// a field on Server.
var uptimeRunners sync.Map // *Server -> *uptimeRunner

type uptimeRunner struct {
	s       *Server
	set     uptimeSettings
	sched   *uptimecore.Scheduler
	browser *uptimecore.BrowserPool

	mu     sync.Mutex
	checks map[int64]UptimeCheck
}

func newUptimeRunner(s *Server, set uptimeSettings) *uptimeRunner {
	u := &uptimeRunner{s: s, set: set, checks: map[int64]UptimeCheck{},
		browser: uptimecore.NewBrowserPool(set.browserWorkers, func() string { return s.conf("uptime_chrome_path") }, browserHelp, s.logger)}
	u.sched = uptimecore.NewScheduler()
	u.sched.Load, u.sched.Execute = u.load, u.executeCore
	u.sched.Workers, u.sched.BrowserWorkers = set.workers, set.browserWorkers
	u.sched.BrowserReady, u.sched.Logger = u.browser.Available, s.logger
	return u
}

// allowPrivate reads the setting on every check, so allowing private
// addresses applies without a restart.
func (u *uptimeRunner) allowPrivate() bool {
	return u.set.allowPrivate || u.s.confBool("uptime_allow_private")
}

func runnerFor(s *Server) *uptimeRunner {
	if v, ok := uptimeRunners.Load(s); ok {
		return v.(*uptimeRunner)
	}
	return newUptimeRunner(s, s.uptimeSettings())
}

// wakeUptime makes the runner reload checks now instead of within 30s,
// on whichever replica runs it.
func wakeUptime(s *Server) {
	s.announce("uptime")
	wakeUptimeHere(s)
}

func wakeUptimeHere(s *Server) {
	if v, ok := uptimeRunners.Load(s); ok {
		v.(*uptimeRunner).sched.Wake()
	}
}

func (u *uptimeRunner) run(ctx context.Context) {
	defer u.browser.Close()
	u.sched.Run(ctx)
}

func (u *uptimeRunner) load(ctx context.Context) ([]uptimecore.Check, error) {
	checks, err := u.s.listUptimeChecks(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]UptimeCheck, len(checks))
	out := make([]uptimecore.Check, 0, len(checks))
	for _, c := range checks {
		if !c.Enabled || c.Kind == "playwright" || !c.runsAt(serverLocation) {
			continue
		}
		byID[c.ID] = c
		out = append(out, c.core())
	}
	u.mu.Lock()
	u.checks = byID
	u.mu.Unlock()
	return out, nil
}

func (u *uptimeRunner) executeCore(ctx context.Context, cc uptimecore.Check) {
	u.mu.Lock()
	c, ok := u.checks[cc.ID]
	u.mu.Unlock()
	if ok {
		u.execute(ctx, c)
	}
}

func (u *uptimeRunner) execute(ctx context.Context, c UptimeCheck) {
	if c.Kind == "browser" {
		u.executeBrowser(ctx, c)
		return
	}
	res := u.probeWithRetry(ctx, c)
	if ctx.Err() != nil || res.Skip != "" {
		return
	}
	if err := u.record(ctx, c, res); err != nil && ctx.Err() == nil {
		u.s.logger.Warn("uptime result not saved", "check_id", c.ID, "err", err)
	}
}

func (u *uptimeRunner) executeBrowser(ctx context.Context, c UptimeCheck) {
	res, run, err := u.runBrowser(ctx, c)
	if err != nil || ctx.Err() != nil {
		// No browser (or it would not start): nothing ran, so nothing is
		// recorded and no incident opens.
		return
	}
	if err := u.record(ctx, c, res); err != nil {
		if ctx.Err() == nil {
			u.s.logger.Warn("uptime result not saved", "check_id", c.ID, "err", err)
		}
		return
	}
	if err := u.recordBrowser(ctx, c, res, run); err != nil && ctx.Err() == nil {
		u.s.logger.Warn("browser run not saved", "check_id", c.ID, "err", err)
	}
}

// probeWithRetry runs a check of any kind but browser, retrying once on
// failure. Heartbeats are looked at here, not probed.
func (u *uptimeRunner) probeWithRetry(ctx context.Context, c UptimeCheck) uptimeResult {
	if c.Kind == "heartbeat" {
		return u.s.heartbeatResult(ctx, c, time.Now())
	}
	return uptimecore.ProbeWithRetry(ctx, c.core(), u.allowPrivate)
}

// runBrowser plays a browser check, retrying once on failure. An error means
// it could not run at all.
func (u *uptimeRunner) runBrowser(ctx context.Context, c UptimeCheck) (uptimeResult, browserRun, error) {
	return u.browser.Run(ctx, c.core(), u.allowPrivate())
}

func optional(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func (u *uptimeRunner) record(ctx context.Context, c UptimeCheck, r uptimeResult) error {
	return u.s.recordResult(ctx, c, r, serverLocation)
}

// recordResult stores a run from location. ok is the check's verdict across
// its locations; when it is down, error names the failing locations.
func (s *Server) recordResult(ctx context.Context, c UptimeCheck, r uptimeResult, location string) error {
	ok, errMsg := r.OK, r.Error
	if len(c.Locations) > 1 {
		var failing []string
		var err error
		if ok, failing, err = s.locationVerdict(ctx, c, location, r.OK); err != nil {
			return err
		}
		if !ok {
			errMsg = "down in " + strings.Join(failing, ", ")
			if r.Error != "" {
				errMsg += ": " + r.Error
			}
		}
	}
	var errText, cert any
	if errMsg != "" {
		errText = errMsg
	}
	if r.CertExpiresAt != "" {
		cert = r.CertExpiresAt
	}
	cols := []string{"created_at", "check_id", "check_name", "url", "ok", "status", "duration_ms", "error", "degraded", "retried",
		"dns_ms", "connect_ms", "tls_ms", "ttfb_ms", "cert_expires_at", "location", "location_ok"}
	vals := []any{r.CreatedAt, c.ID, c.Name, redactURL(c.URL), boolInt(ok), r.Status, r.DurationMs, errText, boolInt(r.Degraded),
		boolInt(r.Retried), optional(r.DNSMs), optional(r.ConnectMs), optional(r.TLSMs), optional(r.TTFBMs), cert, location, boolInt(r.OK)}
	if c.Kind == "browser" {
		cols = append(cols, "lcp_ms", "cls")
		vals = append(vals, optional(r.LCPMs), optional(r.CLS))
	}
	// NULLs are written as literals: D1 sends parameters as strings, which
	// would turn nil into ''.
	marks := make([]string, len(vals))
	args := make([]any, 0, len(vals))
	for i, v := range vals {
		if v == nil {
			marks[i] = "NULL"
			continue
		}
		marks[i] = "?"
		args = append(args, v)
	}
	if err := s.db.Exec(ctx, "INSERT INTO uptime_results ("+strings.Join(cols, ", ")+") VALUES ("+strings.Join(marks, ", ")+")", args...); err != nil {
		return err
	}
	return s.recordDetail(ctx, c, r, location)
}

// redactURL drops credentials and secret-looking query values, for tables
// the analyst and the audit log can read.
func redactURL(raw string) string { return uptimecore.RedactURL(raw) }
