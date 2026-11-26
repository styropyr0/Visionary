package main

import (
	"context"
	"net/netip"
	"slices"
	"strings"
)

// iptablesBackend fills the ipsets visionary4 and visionary6, swapped in
// whole, and drops their sources in the raw table's PREROUTING
type iptablesBackend struct {
	r cmdRunner
}

var ipsetFamilies = []struct{ set, family, tool string }{
	{"visionary4", "inet", "iptables"},
	{"visionary6", "inet6", "ip6tables"},
}

func (t *iptablesBackend) name() string { return "iptables" }

func (t *iptablesBackend) apply(ctx context.Context, v4, v6 []netip.Prefix) error {
	if _, err := t.r.run(ctx, ipsetRestore(v4, v6), "ipset", "-exist", "restore"); err != nil {
		return err
	}
	for _, f := range ipsetFamilies {
		if !t.r.has(f.tool) {
			continue
		}
		if _, err := t.r.run(ctx, "", f.tool, ipsetRule("-C", f.set)...); err == nil {
			continue
		}
		if _, err := t.r.run(ctx, "", f.tool, ipsetRule("-I", f.set)...); err != nil {
			return err
		}
	}
	return nil
}

func (t *iptablesBackend) present(ctx context.Context) bool {
	out, err := t.r.run(ctx, "", "ipset", "list", "-n")
	if err != nil {
		return false
	}
	names := strings.Fields(string(out))
	for _, f := range ipsetFamilies {
		if !t.r.has(f.tool) {
			continue
		}
		if !slices.Contains(names, f.set) {
			return false
		}
		if _, err := t.r.run(ctx, "", f.tool, ipsetRule("-C", f.set)...); err != nil {
			return false
		}
	}
	return true
}

func (t *iptablesBackend) clear(ctx context.Context) error {
	for _, f := range ipsetFamilies {
		if !t.r.has(f.tool) {
			continue
		}
		for range 10 {
			if _, err := t.r.run(ctx, "", f.tool, ipsetRule("-D", f.set)...); err != nil {
				break
			}
		}
	}
	out, err := t.r.run(ctx, "", "ipset", "list", "-n")
	if err != nil {
		return err
	}
	names := strings.Fields(string(out))
	for _, f := range ipsetFamilies {
		for _, s := range []string{f.set, f.set + "-tmp"} {
			if !slices.Contains(names, s) {
				continue
			}
			if _, err := t.r.run(ctx, "", "ipset", "destroy", s); err != nil {
				return err
			}
		}
	}
	return nil
}

func ipsetRule(op, set string) []string {
	args := []string{"-w", "-t", "raw", op, "PREROUTING"}
	if op == "-I" {
		args = append(args, "1")
	}
	return append(args, "-m", "set", "--match-set", set, "src", "-j", "DROP")
}

// ipsetRestore fills a temporary set per family and swaps it in
func ipsetRestore(v4, v6 []netip.Prefix) string {
	var b strings.Builder
	for i, ps := range [][]netip.Prefix{v4, v6} {
		f := ipsetFamilies[i]
		tmp := f.set + "-tmp"
		opts := " hash:net family " + f.family + " maxelem 65536\n"
		b.WriteString("create " + f.set + opts)
		b.WriteString("create " + tmp + opts)
		b.WriteString("flush " + tmp + "\n")
		for _, p := range ps {
			b.WriteString("add " + tmp + " " + prefixText(p) + "\n")
		}
		b.WriteString("swap " + tmp + " " + f.set + "\n")
		b.WriteString("destroy " + tmp + "\n")
	}
	return b.String()
}
