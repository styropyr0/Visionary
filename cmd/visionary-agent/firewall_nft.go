package main

import (
	"context"
	"net/netip"
	"strings"
)

// nftBackend keeps the table inet visionary: two interval sets and a chain at
// prerouting, priority raw, which sees Docker's published ports too
type nftBackend struct {
	r cmdRunner
}

const nftChunk = 500

func (n *nftBackend) name() string { return "nftables" }

func (n *nftBackend) apply(ctx context.Context, v4, v6 []netip.Prefix) error {
	_, err := n.r.run(ctx, nftScript(v4, v6), "nft", "-f", "-")
	return err
}

// The chain's rules name both sets, so a listing that shows them means the
// sets are there too
func (n *nftBackend) present(ctx context.Context) bool {
	out, err := n.r.run(ctx, "", "nft", "list", "chain", "inet", "visionary", "prerouting")
	return err == nil && strings.Contains(string(out), "@blocked4") && strings.Contains(string(out), "@blocked6")
}

func (n *nftBackend) clear(ctx context.Context) error {
	if _, err := n.r.run(ctx, "", "nft", "list", "table", "inet", "visionary"); err != nil {
		return nil
	}
	_, err := n.r.run(ctx, "", "nft", "delete", "table", "inet", "visionary")
	return err
}

// nftScript replaces the rules and set contents in one transaction
func nftScript(v4, v6 []netip.Prefix) string {
	var b strings.Builder
	b.WriteString(`add table inet visionary
add set inet visionary blocked4 { type ipv4_addr; flags interval; }
add set inet visionary blocked6 { type ipv6_addr; flags interval; }
add chain inet visionary prerouting { type filter hook prerouting priority -300; policy accept; }
flush chain inet visionary prerouting
flush set inet visionary blocked4
flush set inet visionary blocked6
add rule inet visionary prerouting ip saddr @blocked4 drop
add rule inet visionary prerouting ip6 saddr @blocked6 drop
`)
	nftElements(&b, "blocked4", v4)
	nftElements(&b, "blocked6", v6)
	return b.String()
}

func nftElements(b *strings.Builder, set string, ps []netip.Prefix) {
	for i := 0; i < len(ps); i += nftChunk {
		b.WriteString("add element inet visionary " + set + " { ")
		for j, p := range ps[i:min(i+nftChunk, len(ps))] {
			if j > 0 {
				b.WriteString(", ")
			}
			b.WriteString(prefixText(p))
		}
		b.WriteString(" }\n")
	}
}

// prefixText writes a single address without its /32 or /128
func prefixText(p netip.Prefix) string {
	if p.IsSingleIP() {
		return p.Addr().String()
	}
	return p.String()
}
