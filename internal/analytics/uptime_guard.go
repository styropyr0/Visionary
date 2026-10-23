package analytics

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
)

// Uptime checks make the server send requests anywhere a user points them,
// so by default they may not reach the server's own network: loopback,
// private, link-local, CGNAT and other non-public ranges are refused. The
// check runs on the dialer, after DNS resolution, so it also covers DNS
// rebinding and every redirect hop. VISIONARY_UPTIME_ALLOW_PRIVATE=true turns
// it off for monitoring internal services.

var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), // benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"), // NAT64 can reach IPv4 private space
	netip.MustParsePrefix("2002::/16"),    // 6to4 likewise
}

var errBlockedAddress = errors.New("blocked address")

func blockedAddr(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsInterfaceLocalMulticast() || a.IsMulticast() || a.IsUnspecified() {
		return true
	}
	for _, p := range blockedPrefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// dialGuard is a net.Dialer Control function: it sees the resolved address
// of every connection, just before connecting.
func dialGuard(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("%w: %s", errBlockedAddress, host)
	}
	if blockedAddr(a) {
		return fmt.Errorf("%w: %s is a private, loopback or link-local address (an admin can allow it in Settings → Server → Other)", errBlockedAddress, a)
	}
	return nil
}

// precheckURL refuses obvious targets before any request: a literal private
// IP or localhost. Names that resolve to private addresses are caught by
// dialGuard.
func precheckURL(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return fmt.Errorf("%w: %s is a loopback name (an admin can allow it in Settings → Server → Other)", errBlockedAddress, host)
	}
	if a, err := netip.ParseAddr(host); err == nil && blockedAddr(a) {
		return fmt.Errorf("%w: %s is a private, loopback or link-local address (an admin can allow it in Settings → Server → Other)", errBlockedAddress, a)
	}
	return nil
}
