package uptimecore

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const maxHeaders = 20

var (
	methods      = map[string]bool{"GET": true, "HEAD": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "OPTIONS": true}
	headerNameRe = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+.^_|~-]{1,64}$`)
	// Underscores are allowed for DNS names
	HostNameRe    = regexp.MustCompile(`(?i)^[a-z0-9_]([a-z0-9_-]{0,62})?(\.[a-z0-9_]([a-z0-9_-]{0,62})?)*\.?$`)
	TemplateRefRe = regexp.MustCompile(`\{\{\s*(var|secret)\.([A-Za-z0-9_]+)\s*\}\}`)
)

func ProbeKind(ctx context.Context, c Check, allowPrivate func() bool) Result {
	switch c.Kind {
	case "api":
		if c.API != nil {
			return probeAPI(ctx, c, allowPrivate)
		}
	case "tcp":
		if c.TCP != nil {
			return probeTCP(ctx, c, allowPrivate)
		}
	case "dns":
		if c.DNS != nil {
			return probeDNS(ctx, c, allowPrivate)
		}
	case "ping":
		if c.Ping != nil {
			return probePing(ctx, c, allowPrivate)
		}
	case "ssl":
		if c.SSL != nil {
			return probeSSL(ctx, c, allowPrivate)
		}
	case "", "http":
		return Probe(ctx, c, allowPrivate)
	}
	return Result{CreatedAt: now(), Error: "this runner cannot run " + c.Kind + " checks"}
}

// A ping check waits for each packet in turn, after looking up the host
func (c Check) AttemptTimeout() time.Duration {
	if c.Kind == "ping" && c.Ping != nil {
		return time.Duration(c.Ping.Packets)*(time.Duration(c.TimeoutMs+PingGapMs)*time.Millisecond) + 10*time.Second
	}
	return time.Duration(c.TimeoutMs) * time.Millisecond
}

func headerValue(h map[string]string, name string) (string, bool) {
	for k, v := range h {
		if strings.EqualFold(k, name) {
			return v, true
		}
	}
	return "", false
}

func BracketHost(h string) string {
	if strings.Contains(h, ":") {
		return "[" + h + "]"
	}
	return h
}

func NormalizeHost(h string) (string, error) {
	h = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(h), "["), "]")
	if h == "" {
		return "", errors.New("host is required")
	}
	if a, err := netip.ParseAddr(h); err == nil {
		return a.String(), nil
	}
	if len(h) > 253 || !HostNameRe.MatchString(h) {
		return "", fmt.Errorf("host %q is not a valid name or IP address", h)
	}
	return strings.ToLower(h), nil
}

func MaskValues(vals []string) func(string) string {
	var keep []string
	for _, v := range vals {
		if len(v) >= 3 {
			keep = append(keep, v)
		}
	}
	return func(s string) string {
		for _, v := range keep {
			s = strings.ReplaceAll(s, v, Mask)
			if e := url.QueryEscape(v); e != v {
				s = strings.ReplaceAll(s, e, Mask)
			}
		}
		return s
	}
}

func msSince(t time.Time) float64 { return float64(time.Since(t).Microseconds()) / 1000 }

func msPtr(v float64) *float64 { return &v }

func kindDialer(allowPrivate func() bool, timeout time.Duration) *net.Dialer {
	d := &net.Dialer{Timeout: timeout}
	if !allowPrivate() {
		d.Control = DialGuard
	}
	return d
}

func precheckHost(allowPrivate func() bool, host string) error {
	if allowPrivate() {
		return nil
	}
	return PrecheckURL(&url.URL{Scheme: "http", Host: BracketHost(host)})
}

func resolveGuarded(ctx context.Context, allowPrivate func() bool, host string) (netip.Addr, error) {
	if err := precheckHost(allowPrivate, host); err != nil {
		return netip.Addr{}, err
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return netip.Addr{}, err
	}
	if len(addrs) == 0 {
		return netip.Addr{}, fmt.Errorf("%s has no address", host)
	}
	pick := addrs[0]
	for _, a := range addrs {
		if a.Unmap().Is4() {
			pick = a.Unmap()
			break
		}
	}
	if !allowPrivate() && BlockedAddr(pick) {
		return netip.Addr{}, fmt.Errorf("%w: %s is a private, loopback or link-local address"+GuardHint, ErrBlockedAddress, pick)
	}
	return pick, nil
}
