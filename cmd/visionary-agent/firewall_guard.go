package main

import (
	"context"
	"net"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

type refusal struct {
	addr, why string
}

var reservedBlocks = []struct {
	p   netip.Prefix
	why string
}{
	{netip.MustParsePrefix("0.0.0.0/8"), "unspecified"},
	{netip.MustParsePrefix("10.0.0.0/8"), "private"},
	{netip.MustParsePrefix("100.64.0.0/10"), "private"},
	{netip.MustParsePrefix("127.0.0.0/8"), "loopback"},
	{netip.MustParsePrefix("169.254.0.0/16"), "link-local"},
	{netip.MustParsePrefix("172.16.0.0/12"), "private"},
	{netip.MustParsePrefix("192.168.0.0/16"), "private"},
	{netip.MustParsePrefix("224.0.0.0/4"), "multicast"},
	{netip.MustParsePrefix("240.0.0.0/4"), "reserved"},
	{netip.MustParsePrefix("::/128"), "unspecified"},
	{netip.MustParsePrefix("::1/128"), "loopback"},
	{netip.MustParsePrefix("::ffff:0:0/96"), "mapped"},
	{netip.MustParsePrefix("fc00::/7"), "private"},
	{netip.MustParsePrefix("fe80::/10"), "link-local"},
	{netip.MustParsePrefix("ff00::/8"), "multicast"},
}

// filterBlocks parses the server's list and drops what must never be
// blocked, returning the rest per family with nested ranges folded away
func filterBlocks(addrs []string, protected []netip.Addr) (v4, v6 []netip.Prefix, refused []refusal) {
	for _, s := range addrs {
		s = strings.TrimSpace(s)
		p, ok := parseBlock(s)
		if !ok {
			refused = append(refused, refusal{s, "invalid"})
			continue
		}
		if why := blockRefusal(p, protected); why != "" {
			refused = append(refused, refusal{s, why})
			continue
		}
		if p.Addr().Is4() {
			v4 = append(v4, p)
		} else {
			v6 = append(v6, p)
		}
	}
	return foldPrefixes(v4), foldPrefixes(v6), refused
}

func parseBlock(s string) (netip.Prefix, bool) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, false
		}
		if p.Addr().Is4In6() && p.Bits() >= 96 {
			p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
		}
		return p.Masked(), true
	}
	a, err := netip.ParseAddr(s)
	if err != nil || a.Zone() != "" {
		return netip.Prefix{}, false
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), true
}

func blockRefusal(p netip.Prefix, protected []netip.Addr) string {
	if (p.Addr().Is4() && p.Bits() < 8) || (p.Addr().Is6() && p.Bits() < 32) {
		return "too wide"
	}
	for _, r := range reservedBlocks {
		if p.Overlaps(r.p) {
			return r.why
		}
	}
	for _, a := range protected {
		if p.Contains(a) {
			return "server or SSH session"
		}
	}
	return ""
}

// foldPrefixes sorts and drops ranges inside another, which nftables'
// interval sets refuse as overlapping
func foldPrefixes(ps []netip.Prefix) []netip.Prefix {
	slices.SortFunc(ps, func(a, b netip.Prefix) int {
		if c := a.Addr().Compare(b.Addr()); c != 0 {
			return c
		}
		return a.Bits() - b.Bits()
	})
	out := ps[:0]
	for _, p := range ps {
		if n := len(out); n > 0 && out[n-1].Contains(p.Addr()) {
			continue
		}
		out = append(out, p)
	}
	return out
}

func refusalText(rs []refusal, n int) string {
	var parts []string
	for i, r := range rs {
		if i == n {
			parts = append(parts, "...")
			break
		}
		parts = append(parts, r.addr+" ("+r.why+")")
	}
	return strings.Join(parts, ", ")
}

func lookupServer(ctx context.Context, raw string) []netip.Addr {
	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	host := u.Hostname()
	if a, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{a.Unmap()}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil
	}
	for i := range ips {
		ips[i] = ips[i].Unmap()
	}
	return ips
}

// sshPeers are the remote addresses of established connections to sshd
func sshPeers() []netip.Addr {
	ports := map[int]bool{22: true}
	if cur, inodes, err := listening(); err == nil {
		names := socketOwners(inodes)
		for k, ino := range cur {
			if !strings.HasPrefix(k, "tcp ") || !strings.HasPrefix(names[ino], "sshd") {
				continue
			}
			if _, p, err := net.SplitHostPort(strings.TrimPrefix(k, "tcp ")); err == nil {
				if n, err := strconv.Atoi(p); err == nil {
					ports[n] = true
				}
			}
		}
	}
	var out []netip.Addr
	for _, f := range []string{"tcp", "tcp6"} {
		b, err := os.ReadFile(procPath("1", "net", f))
		if err != nil {
			if b, err = os.ReadFile(procPath("net", f)); err != nil {
				continue
			}
		}
		out = append(out, establishedPeers(string(b), ports)...)
	}
	return out
}

// establishedPeers reads a /proc/net/tcp table for ESTABLISHED sockets on
// one of ports and returns their remote addresses
func establishedPeers(table string, ports map[int]bool) []netip.Addr {
	var out []netip.Addr
	for _, line := range strings.Split(table, "\n") {
		fs := strings.Fields(line)
		if len(fs) < 4 || fs[3] != "01" {
			continue
		}
		_, lport, ok := hexAddr(fs[1])
		if !ok || !ports[lport] {
			continue
		}
		rip, _, ok := hexAddr(fs[2])
		if !ok {
			continue
		}
		if a, err := netip.ParseAddr(rip); err == nil {
			out = append(out, a.Unmap())
		}
	}
	return out
}
