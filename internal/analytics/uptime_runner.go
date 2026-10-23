package analytics

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"time"
)

// The runner keeps a schedule of enabled checks, reloaded every 30s and right
// after any change through the API, and feeds due checks to a fixed pool of
// workers. A check never runs twice at once; when every worker is busy, due
// checks wait for the next tick instead of piling up.

const (
	uptimeTick       = time.Second
	uptimeReload     = 30 * time.Second
	uptimeRetryDelay = 2 * time.Second
	uptimeMaxBody    = 1 << 20
	uptimeUserAgent  = "Visionary-Uptime/1.0"
	uptimeRedirects  = 5
)

// uptimeRunners holds each server's runner, so handlers can reach it without
// a field on Server.
var uptimeRunners sync.Map // *Server -> *uptimeRunner

type uptimeRunner struct {
	s    *Server
	set  uptimeSettings
	wake chan struct{}

	mu      sync.Mutex
	running map[int64]bool
}

func newUptimeRunner(s *Server, set uptimeSettings) *uptimeRunner {
	return &uptimeRunner{s: s, set: set, wake: make(chan struct{}, 1), running: map[int64]bool{}}
}

func runnerFor(s *Server) *uptimeRunner {
	if v, ok := uptimeRunners.Load(s); ok {
		return v.(*uptimeRunner)
	}
	return newUptimeRunner(s, s.uptimeSettings())
}

// wakeUptime makes the runner reload checks now instead of within 30s.
func wakeUptime(s *Server) {
	if v, ok := uptimeRunners.Load(s); ok {
		select {
		case v.(*uptimeRunner).wake <- struct{}{}:
		default:
		}
	}
}

type scheduled struct {
	check UptimeCheck
	next  time.Time
}

func (u *uptimeRunner) run(ctx context.Context) {
	jobs := make(chan UptimeCheck)
	var wg sync.WaitGroup
	for range u.set.workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range jobs {
				u.execute(ctx, c)
			}
		}()
	}
	defer func() {
		close(jobs)
		wg.Wait()
	}()

	schedule := map[int64]*scheduled{}
	tick := time.NewTicker(uptimeTick)
	defer tick.Stop()
	var lastReload time.Time
	for {
		now := time.Now()
		if now.Sub(lastReload) >= uptimeReload {
			u.reload(ctx, schedule)
			lastReload = now
		}
		for _, e := range schedule {
			if now.Before(e.next) || u.isRunning(e.check.ID) {
				continue
			}
			select {
			case jobs <- e.check:
				u.setRunning(e.check.ID, true)
				e.next = now.Add(jittered(e.check.IntervalS))
			default:
				// All workers busy: try again next tick.
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-u.wake:
			lastReload = time.Time{}
		case <-tick.C:
		}
	}
}

// jittered is the interval ±5% (at most ±3s), so checks with the same
// interval drift apart instead of firing together forever.
func jittered(intervalS int) time.Duration {
	d := time.Duration(intervalS) * time.Second
	j := min(d/20, 3*time.Second)
	return d - j + time.Duration(rand.Int64N(int64(2*j)+1))
}

func (u *uptimeRunner) reload(ctx context.Context, schedule map[int64]*scheduled) {
	checks, err := u.s.listUptimeChecks(ctx)
	if err != nil {
		if ctx.Err() == nil {
			u.s.logger.Warn("uptime checks not loaded", "err", err)
		}
		return
	}
	seen := map[int64]bool{}
	for _, c := range checks {
		if !c.Enabled {
			continue
		}
		seen[c.ID] = true
		if e, ok := schedule[c.ID]; ok {
			if e.check.IntervalS != c.IntervalS {
				e.next = time.Now().Add(time.Duration(rand.Int64N(int64(5 * time.Second))))
			}
			e.check = c
			continue
		}
		// New (or just started): first run soon, spread over a few seconds.
		spread := min(time.Duration(c.IntervalS)*time.Second/2, 15*time.Second)
		schedule[c.ID] = &scheduled{check: c, next: time.Now().Add(time.Duration(rand.Int64N(int64(spread))))}
	}
	for id := range schedule {
		if !seen[id] {
			delete(schedule, id)
		}
	}
}

func (u *uptimeRunner) isRunning(id int64) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.running[id]
}

func (u *uptimeRunner) setRunning(id int64, v bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if v {
		u.running[id] = true
	} else {
		delete(u.running, id)
	}
}

// execute runs one scheduled check; nothing a check does may stop the loop.
func (u *uptimeRunner) execute(ctx context.Context, c UptimeCheck) {
	defer u.setRunning(c.ID, false)
	defer func() {
		if p := recover(); p != nil {
			u.s.logger.Error("uptime check panicked", "check_id", c.ID, "panic", fmt.Sprint(p))
		}
	}()
	res := u.probeWithRetry(ctx, c)
	if ctx.Err() != nil {
		return
	}
	if err := u.record(ctx, c, res); err != nil && ctx.Err() == nil {
		u.s.logger.Warn("uptime result not saved", "check_id", c.ID, "err", err)
	}
}

// uptimeResult is one recorded run.
type uptimeResult struct {
	CreatedAt     string   `json:"created_at"`
	OK            bool     `json:"ok"`
	Status        int      `json:"status"`
	DurationMs    float64  `json:"duration_ms"`
	Error         string   `json:"error,omitempty"`
	Degraded      bool     `json:"degraded"`
	Retried       bool     `json:"retried"`
	DNSMs         *float64 `json:"dns_ms"`
	ConnectMs     *float64 `json:"connect_ms"`
	TLSMs         *float64 `json:"tls_ms"`
	TTFBMs        *float64 `json:"ttfb_ms"`
	CertExpiresAt string   `json:"cert_expires_at,omitempty"`

	blocked bool // refused by the address guard; retrying cannot help
}

// probeWithRetry tries again once, shortly, before calling a check failed,
// so one dropped packet does not open an incident.
func (u *uptimeRunner) probeWithRetry(ctx context.Context, c UptimeCheck) uptimeResult {
	res := u.probe(ctx, c)
	if res.OK || ctx.Err() != nil || res.blocked {
		return res
	}
	select {
	case <-ctx.Done():
		return res
	case <-time.After(uptimeRetryDelay):
	}
	res = u.probe(ctx, c)
	res.Retried = true
	return res
}

// phases collects httptrace timings of the latest request in a redirect
// chain. Hooks can fire on other goroutines, hence the lock.
type phases struct {
	mu sync.Mutex
	t  phaseTimes
}

type phaseTimes struct {
	dnsStart, dnsDone, connStart, connDone time.Time
	tlsStart, tlsDone, wrote, firstByte    time.Time
}

func (p *phases) set(f func()) {
	p.mu.Lock()
	f()
	p.mu.Unlock()
}

func (p *phases) trace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		GetConn:      func(string) { p.set(func() { p.t = phaseTimes{} }) },
		DNSStart:     func(httptrace.DNSStartInfo) { p.set(func() { p.t.dnsStart = time.Now() }) },
		DNSDone:      func(httptrace.DNSDoneInfo) { p.set(func() { p.t.dnsDone = time.Now() }) },
		ConnectStart: func(string, string) { p.set(func() { p.t.connStart = firstTime(p.t.connStart) }) },
		ConnectDone: func(_, _ string, err error) {
			if err == nil {
				p.set(func() { p.t.connDone = time.Now() })
			}
		},
		TLSHandshakeStart:    func() { p.set(func() { p.t.tlsStart = time.Now() }) },
		TLSHandshakeDone:     func(tls.ConnectionState, error) { p.set(func() { p.t.tlsDone = time.Now() }) },
		WroteRequest:         func(httptrace.WroteRequestInfo) { p.set(func() { p.t.wrote = time.Now() }) },
		GotFirstResponseByte: func() { p.set(func() { p.t.firstByte = time.Now() }) },
	}
}

func firstTime(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now()
	}
	return t
}

func span(a, b time.Time) *float64 {
	if a.IsZero() || b.IsZero() || b.Before(a) {
		return nil
	}
	ms := float64(b.Sub(a).Microseconds()) / 1000
	return &ms
}

func (u *uptimeRunner) client(c UptimeCheck) *http.Client {
	timeout := time.Duration(c.TimeoutMs) * time.Millisecond
	dialer := &net.Dialer{Timeout: timeout}
	if !u.set.allowPrivate {
		dialer.Control = dialGuard
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:                  nil, // a proxy would hide the real destination from the guard
			DialContext:            dialer.DialContext,
			TLSHandshakeTimeout:    timeout,
			DisableKeepAlives:      true,
			ForceAttemptHTTP2:      true,
			MaxResponseHeaderBytes: 64 << 10,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if !c.FollowRedirects {
				return http.ErrUseLastResponse
			}
			if len(via) >= uptimeRedirects {
				return fmt.Errorf("stopped after %d redirects", uptimeRedirects)
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("redirect to unsupported scheme %q", req.URL.Scheme)
			}
			if !u.set.allowPrivate {
				return precheckURL(req.URL)
			}
			return nil
		},
	}
}

func (u *uptimeRunner) probe(ctx context.Context, c UptimeCheck) (res uptimeResult) {
	start := time.Now()
	defer func() {
		res.CreatedAt = start.UTC().Format(tsLayout)
		res.DurationMs = float64(time.Since(start).Microseconds()) / 1000
		res.Degraded = res.OK && c.DegradedMs > 0 && res.DurationMs > float64(c.DegradedMs)
	}()
	fail := func(err error) uptimeResult {
		res.OK, res.Error, res.blocked = false, probeError(err, c.TimeoutMs), errors.Is(err, errBlockedAddress)
		return res
	}

	target, err := url.Parse(c.URL)
	if err != nil {
		return fail(errors.New("invalid URL"))
	}
	if !u.set.allowPrivate {
		if err := precheckURL(target); err != nil {
			return fail(err)
		}
	}
	expect, err := parseExpectStatus(c.ExpectStatus)
	if err != nil {
		return fail(err)
	}
	ph := &phases{}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, ph.trace()), c.Method, c.URL, nil)
	if err != nil {
		return fail(errors.New("invalid request"))
	}
	req.Header.Set("User-Agent", uptimeUserAgent)
	for k, v := range c.Headers {
		if strings.EqualFold(k, "Host") {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}

	resp, err := u.client(c).Do(req)
	if err != nil {
		return fail(err)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, uptimeMaxBody))

	ph.mu.Lock()
	res.DNSMs = span(ph.t.dnsStart, ph.t.dnsDone)
	res.ConnectMs = span(ph.t.connStart, ph.t.connDone)
	res.TLSMs = span(ph.t.tlsStart, ph.t.tlsDone)
	res.TTFBMs = span(ph.t.wrote, ph.t.firstByte)
	ph.mu.Unlock()
	res.Status = resp.StatusCode
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		res.CertExpiresAt = resp.TLS.PeerCertificates[0].NotAfter.UTC().Format(tsLayout)
	}

	switch {
	case readErr != nil:
		return fail(readErr)
	case !statusAccepted(expect, resp.StatusCode):
		return fail(fmt.Errorf("unexpected status %d (expected %s)", resp.StatusCode, c.ExpectStatus))
	case c.BodyContains != "" && !strings.Contains(string(body), c.BodyContains):
		return fail(errors.New("expected text not found in the response body"))
	}
	res.OK = true
	return res
}

// probeError turns a client error into a short reason. url.Error repeats the
// full URL, which can carry credentials, so only its cause is kept.
func probeError(err error, timeoutMs int) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		if ue.Timeout() {
			return fmt.Sprintf("timed out after %s", time.Duration(timeoutMs)*time.Millisecond)
		}
		err = ue.Err
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return fmt.Sprintf("timed out after %s", time.Duration(timeoutMs)*time.Millisecond)
	}
	msg := err.Error()
	if errors.Is(err, errBlockedAddress) {
		// Keep the guard's explanation, not the dialer's wrapping.
		if i := strings.Index(msg, errBlockedAddress.Error()); i > 0 {
			msg = msg[i:]
		}
	}
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return msg
}

func optional(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func (u *uptimeRunner) record(ctx context.Context, c UptimeCheck, r uptimeResult) error {
	var errText, cert any
	if r.Error != "" {
		errText = r.Error
	}
	if r.CertExpiresAt != "" {
		cert = r.CertExpiresAt
	}
	cols := []string{"created_at", "check_id", "check_name", "url", "ok", "status", "duration_ms", "error", "degraded", "retried",
		"dns_ms", "connect_ms", "tls_ms", "ttfb_ms", "cert_expires_at"}
	vals := []any{r.CreatedAt, c.ID, c.Name, redactURL(c.URL), boolInt(r.OK), r.Status, r.DurationMs, errText, boolInt(r.Degraded),
		boolInt(r.Retried), optional(r.DNSMs), optional(r.ConnectMs), optional(r.TLSMs), optional(r.TTFBMs), cert}
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
	return u.s.db.Exec(ctx, "INSERT INTO uptime_results ("+strings.Join(cols, ", ")+") VALUES ("+strings.Join(marks, ", ")+")", args...)
}

// redactURL drops credentials and secret-looking query values, for tables
// the analyst and the audit log can read.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	u.User = nil
	if u.RawQuery != "" {
		q := u.Query()
		changed := false
		for k := range q {
			if secretHeaderRe.MatchString(k) || strings.EqualFold(k, "sig") {
				q[k] = []string{"REDACTED"}
				changed = true
			}
		}
		if changed {
			u.RawQuery = q.Encode()
		}
	}
	return u.String()
}
