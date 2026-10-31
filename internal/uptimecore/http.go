package uptimecore

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// RetryDelay is the pause before a failed run is tried again.
	RetryDelay = 2 * time.Second
	maxBody    = 1 << 20
	userAgent  = "Visionary-Uptime/1.0"
	redirects  = 5
)

// ProbeWithRetry tries again once, shortly, before calling a check failed,
// so one dropped packet does not open an incident. allowPrivate is read on
// every request, so changing it applies at once.
func ProbeWithRetry(ctx context.Context, c Check, allowPrivate func() bool) Result {
	res := ProbeKind(ctx, c, allowPrivate)
	if res.OK || ctx.Err() != nil || res.blocked || res.Skip != "" {
		return res
	}
	select {
	case <-ctx.Done():
		return res
	case <-time.After(RetryDelay):
	}
	res = ProbeKind(ctx, c, allowPrivate)
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

func client(c Check, allowPrivate func() bool) *http.Client {
	timeout := time.Duration(c.TimeoutMs) * time.Millisecond
	dialer := &net.Dialer{Timeout: timeout}
	if !allowPrivate() {
		dialer.Control = DialGuard
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
			if len(via) >= redirects {
				return fmt.Errorf("stopped after %d redirects", redirects)
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("redirect to unsupported scheme %q", req.URL.Scheme)
			}
			if !allowPrivate() {
				return PrecheckURL(req.URL)
			}
			return nil
		},
	}
}

// Probe makes one HTTP request for c.
func Probe(ctx context.Context, c Check, allowPrivate func() bool) (res Result) {
	start := time.Now()
	defer func() {
		res.CreatedAt = start.UTC().Format(TimeLayout)
		res.DurationMs = float64(time.Since(start).Microseconds()) / 1000
		res.Degraded = res.OK && c.DegradedMs > 0 && res.DurationMs > float64(c.DegradedMs)
	}()
	fail := func(err error) Result {
		res.OK, res.Error, res.blocked = false, ProbeError(err, c.TimeoutMs), errors.Is(err, ErrBlockedAddress)
		return res
	}

	target, err := url.Parse(c.URL)
	if err != nil {
		return fail(errors.New("invalid URL"))
	}
	if !allowPrivate() {
		if err := PrecheckURL(target); err != nil {
			return fail(err)
		}
	}
	expect, err := ParseExpectStatus(c.ExpectStatus)
	if err != nil {
		return fail(err)
	}
	ph := &phases{}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, ph.trace()), c.Method, c.URL, nil)
	if err != nil {
		return fail(errors.New("invalid request"))
	}
	req.Header.Set("User-Agent", userAgent)
	for k, v := range c.Headers {
		if strings.EqualFold(k, "Host") {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}

	resp, err := client(c, allowPrivate).Do(req)
	if err != nil {
		return fail(err)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBody))

	ph.mu.Lock()
	res.DNSMs = span(ph.t.dnsStart, ph.t.dnsDone)
	res.ConnectMs = span(ph.t.connStart, ph.t.connDone)
	res.TLSMs = span(ph.t.tlsStart, ph.t.tlsDone)
	res.TTFBMs = span(ph.t.wrote, ph.t.firstByte)
	ph.mu.Unlock()
	res.Status = resp.StatusCode
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		res.CertExpiresAt = resp.TLS.PeerCertificates[0].NotAfter.UTC().Format(TimeLayout)
	}

	switch {
	case readErr != nil:
		return fail(readErr)
	case !StatusAccepted(expect, resp.StatusCode):
		return fail(fmt.Errorf("unexpected status %d (expected %s)", resp.StatusCode, c.ExpectStatus))
	case c.BodyContains != "" && !strings.Contains(string(body), c.BodyContains):
		return fail(errors.New("expected text not found in the response body"))
	}
	res.OK = true
	return res
}

// ProbeError turns a client error into a short reason. url.Error repeats the
// full URL, which can carry credentials, so only its cause is kept.
func ProbeError(err error, timeoutMs int) string {
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
	if errors.Is(err, ErrBlockedAddress) {
		// Keep the guard's explanation, not the dialer's wrapping.
		if i := strings.Index(msg, ErrBlockedAddress.Error()); i > 0 {
			msg = msg[i:]
		}
	}
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return msg
}

// StatusRange is an inclusive range of accepted HTTP statuses.
type StatusRange struct{ lo, hi int }

var expectStatusPart = regexp.MustCompile(`^([1-5])xx$|^(\d{3})(?:-(\d{3}))?$`)

// ParseExpectStatus reads "200-299", "2xx", "200,204,301" or a mix.
func ParseExpectStatus(spec string) ([]StatusRange, error) {
	var out []StatusRange
	for _, part := range strings.Split(spec, ",") {
		m := expectStatusPart.FindStringSubmatch(strings.ToLower(part))
		if m == nil {
			return nil, fmt.Errorf("expected status %q: use a code (200), a range (200-299) or a class (2xx), comma separated", part)
		}
		var r StatusRange
		switch {
		case m[1] != "":
			d, _ := strconv.Atoi(m[1])
			r = StatusRange{d * 100, d*100 + 99}
		default:
			r.lo, _ = strconv.Atoi(m[2])
			r.hi = r.lo
			if m[3] != "" {
				r.hi, _ = strconv.Atoi(m[3])
			}
		}
		if r.lo < 100 || r.hi > 599 || r.hi < r.lo {
			return nil, fmt.Errorf("expected status %q is out of range", part)
		}
		out = append(out, r)
	}
	return out, nil
}

// StatusAccepted is whether code falls in one of rs.
func StatusAccepted(rs []StatusRange, code int) bool {
	for _, r := range rs {
		if code >= r.lo && code <= r.hi {
			return true
		}
	}
	return false
}
