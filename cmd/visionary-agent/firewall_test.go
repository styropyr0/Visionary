package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/styropyr0/visionary/internal/agentapi"
)

type fakeRunner struct {
	tools map[string]bool
	calls []string
	stdin []string
	fail  func(call string) bool
	out   map[string]string
}

func (r *fakeRunner) has(name string) bool { return r.tools[name] }

func (r *fakeRunner) run(_ context.Context, stdin, name string, args ...string) ([]byte, error) {
	call := strings.Join(append([]string{name}, args...), " ")
	r.calls = append(r.calls, call)
	r.stdin = append(r.stdin, stdin)
	if r.fail != nil && r.fail(call) {
		return nil, &commandError{code: 1}
	}
	return []byte(r.out[call]), nil
}

func prefixes(ss ...string) []netip.Prefix {
	var out []netip.Prefix
	for _, s := range ss {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}

func TestFilterBlocks(t *testing.T) {
	in := []string{
		"203.0.113.7", " 198.51.100.0/24 ", "198.51.100.9", "198.51.100.12/30",
		"2001:db8::1", "2001:db8:1::/48", "::ffff:192.0.2.4",
		"nonsense", "1.2.3.4/33", "fe80::1%eth0",
		"10.1.2.3", "100.64.0.1", "127.0.0.1", "169.254.1.1", "224.0.0.1", "0.0.0.0",
		"fd00::1", "::1", "ff02::1", "fe80::1",
		"4.0.0.0/7", "2001::/31", "100.0.0.0/8",
		"192.0.2.10", "203.0.112.0/23",
	}
	protected := []netip.Addr{netip.MustParseAddr("192.0.2.10"), netip.MustParseAddr("203.0.112.200")}
	v4, v6, refused := filterBlocks(in, protected)
	if want := prefixes("192.0.2.4/32", "198.51.100.0/24", "203.0.113.7/32"); !reflect.DeepEqual(v4, want) {
		t.Fatalf("v4 %v, want %v", v4, want)
	}
	if want := prefixes("2001:db8::1/128", "2001:db8:1::/48"); !reflect.DeepEqual(v6, want) {
		t.Fatalf("v6 %v, want %v", v6, want)
	}
	got := map[string]string{}
	for _, r := range refused {
		got[r.addr] = r.why
	}
	want := map[string]string{
		"nonsense": "invalid", "1.2.3.4/33": "invalid", "fe80::1%eth0": "invalid",
		"10.1.2.3": "private", "100.64.0.1": "private", "127.0.0.1": "loopback", "169.254.1.1": "link-local",
		"224.0.0.1": "multicast", "0.0.0.0": "unspecified", "fd00::1": "private", "::1": "loopback",
		"ff02::1": "multicast", "fe80::1": "link-local", "4.0.0.0/7": "too wide", "2001::/31": "too wide",
		"100.0.0.0/8": "private", "192.0.2.10": "server or SSH session", "203.0.112.0/23": "server or SSH session",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("refused %v, want %v", got, want)
	}
}

func TestNftScript(t *testing.T) {
	got := nftScript(prefixes("192.0.2.4/32", "198.51.100.0/24"), prefixes("2001:db8::/32"))
	want := `add table inet visionary
add set inet visionary blocked4 { type ipv4_addr; flags interval; }
add set inet visionary blocked6 { type ipv6_addr; flags interval; }
add chain inet visionary prerouting { type filter hook prerouting priority -300; policy accept; }
flush chain inet visionary prerouting
flush set inet visionary blocked4
flush set inet visionary blocked6
add rule inet visionary prerouting ip saddr @blocked4 drop
add rule inet visionary prerouting ip6 saddr @blocked6 drop
add element inet visionary blocked4 { 192.0.2.4, 198.51.100.0/24 }
add element inet visionary blocked6 { 2001:db8::/32 }
`
	if got != want {
		t.Fatalf("script:\n%s", got)
	}
	var many []netip.Prefix
	for i := range 1200 {
		many = append(many, netip.PrefixFrom(netip.AddrFrom4([4]byte{198, 18, byte(i >> 8), byte(i)}), 32))
	}
	if n := strings.Count(nftScript(many, nil), "add element inet visionary blocked4"); n != 3 {
		t.Fatalf("%d element lines, want 3", n)
	}
}

func TestIpsetRestore(t *testing.T) {
	got := ipsetRestore(prefixes("192.0.2.4/32", "198.51.100.0/24"), nil)
	want := `create visionary4 hash:net family inet maxelem 65536
create visionary4-tmp hash:net family inet maxelem 65536
flush visionary4-tmp
add visionary4-tmp 192.0.2.4
add visionary4-tmp 198.51.100.0/24
swap visionary4-tmp visionary4
destroy visionary4-tmp
create visionary6 hash:net family inet6 maxelem 65536
create visionary6-tmp hash:net family inet6 maxelem 65536
flush visionary6-tmp
swap visionary6-tmp visionary6
destroy visionary6-tmp
`
	if got != want {
		t.Fatalf("restore:\n%s", got)
	}
}

const procTCP = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1001 1 0 100 0 0 10 0
   1: 0A00000F:0016 077100CB:D431 01 00000000:00000000 02:000A7B1B 00000000     0        0 1002 4 0 20 4 30 10 -1
   2: 0A00000F:0050 026433C6:C350 01 00000000:00000000 00:00000000 00000000    33        0 1003 1 0 20 4 30 10 -1
   3: 0A00000F:08AE 096433C6:E001 01 00000000:00000000 00:00000000 00000000     0        0 1004 1 0 20 4 30 10 -1
   4: 0A00000F:0016 0A6433C6:E002 06 00000000:00000000 00:00000000 00000000     0        0 0 3 0 0 0 0 0 0
`

const procTCP6 = `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000000000000:0016 B80D0120000000000000000005000000:D000 01 00000000:00000000 00:00000000 00000000     0        0 2001 1 0 20 4 30 10 -1
   1: 0000000000000000FFFF00000A00000F:0016 0000000000000000FFFF0000096433C6:D001 01 00000000:00000000 00:00000000 00000000     0        0 2002 1 0 20 4 30 10 -1
`

func TestEstablishedPeers(t *testing.T) {
	ports := map[int]bool{22: true, 2222: true}
	got := append(establishedPeers(procTCP, ports), establishedPeers(procTCP6, ports)...)
	want := []netip.Addr{
		netip.MustParseAddr("203.0.113.7"), netip.MustParseAddr("198.51.100.9"),
		netip.MustParseAddr("2001:db8::5"), netip.MustParseAddr("198.51.100.9"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("peers %v, want %v", got, want)
	}
}

func TestPickBackend(t *testing.T) {
	cases := []struct {
		goos  string
		tools []string
		want  string
	}{
		{"linux", []string{"nft", "ipset", "iptables"}, "nftables"},
		{"linux", []string{"ipset", "iptables"}, "iptables"},
		{"linux", []string{"iptables"}, ""},
		{"windows", []string{"nft"}, ""},
	}
	for _, c := range cases {
		r := &fakeRunner{tools: map[string]bool{}}
		for _, tool := range c.tools {
			r.tools[tool] = true
		}
		b, why := pickBackend(c.goos, r)
		if c.want == "" {
			if b != nil || why == "" {
				t.Fatalf("%s %v: got %v %q", c.goos, c.tools, b, why)
			}
			continue
		}
		if b == nil || b.name() != c.want {
			t.Fatalf("%s %v: got %v, want %s", c.goos, c.tools, b, c.want)
		}
	}
}

func TestNftBackend(t *testing.T) {
	r := &fakeRunner{tools: map[string]bool{"nft": true}, out: map[string]string{
		"nft list chain inet visionary prerouting": "ip saddr @blocked4 drop\nip6 saddr @blocked6 drop",
	}}
	n := &nftBackend{r: r}
	ctx := context.Background()
	if err := n.apply(ctx, prefixes("192.0.2.4/32"), nil); err != nil {
		t.Fatal(err)
	}
	if r.calls[0] != "nft -f -" || !strings.Contains(r.stdin[0], "add element inet visionary blocked4 { 192.0.2.4 }") {
		t.Fatalf("apply ran %q with %q", r.calls[0], r.stdin[0])
	}
	if !n.present(ctx) {
		t.Fatal("present")
	}
	r.fail = func(string) bool { return true }
	if n.present(ctx) {
		t.Fatal("present with the table gone")
	}
	r.calls, r.fail = nil, nil
	if err := n.clear(ctx); err != nil {
		t.Fatal(err)
	}
	if want := []string{"nft list table inet visionary", "nft delete table inet visionary"}; !reflect.DeepEqual(r.calls, want) {
		t.Fatalf("clear ran %v", r.calls)
	}
}

func TestIptablesBackend(t *testing.T) {
	r := &fakeRunner{tools: map[string]bool{"ipset": true, "iptables": true, "ip6tables": true}}
	r.fail = func(call string) bool { return strings.HasPrefix(call, "ip6tables") && strings.Contains(call, " -C ") }
	b := &iptablesBackend{r: r}
	if err := b.apply(context.Background(), prefixes("192.0.2.4/32"), nil); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"ipset -exist restore",
		"iptables -w -t raw -C PREROUTING -m set --match-set visionary4 src -j DROP",
		"ip6tables -w -t raw -C PREROUTING -m set --match-set visionary6 src -j DROP",
		"ip6tables -w -t raw -I PREROUTING 1 -m set --match-set visionary6 src -j DROP",
	}
	if !reflect.DeepEqual(r.calls, want) {
		t.Fatalf("apply ran %v", r.calls)
	}

	r.calls, r.out = nil, map[string]string{"ipset list -n": "visionary4\nvisionary6\nother\n"}
	deleted := map[string]bool{}
	r.fail = func(call string) bool {
		if strings.Contains(call, " -D ") {
			if deleted[call] {
				return true
			}
			deleted[call] = true
		}
		return false
	}
	if err := b.clear(context.Background()); err != nil {
		t.Fatal(err)
	}
	want = []string{
		"iptables -w -t raw -D PREROUTING -m set --match-set visionary4 src -j DROP",
		"iptables -w -t raw -D PREROUTING -m set --match-set visionary4 src -j DROP",
		"ip6tables -w -t raw -D PREROUTING -m set --match-set visionary6 src -j DROP",
		"ip6tables -w -t raw -D PREROUTING -m set --match-set visionary6 src -j DROP",
		"ipset list -n",
		"ipset destroy visionary4",
		"ipset destroy visionary6",
	}
	if !reflect.DeepEqual(r.calls, want) {
		t.Fatalf("clear ran %v", r.calls)
	}
}

type fakeBackend struct {
	applied [][]netip.Prefix
	gone    bool
	err     error
}

func (b *fakeBackend) name() string                 { return "fake" }
func (b *fakeBackend) present(context.Context) bool { return !b.gone }
func (b *fakeBackend) clear(context.Context) error  { return nil }
func (b *fakeBackend) apply(_ context.Context, v4, v6 []netip.Prefix) error {
	if b.err != nil {
		return b.err
	}
	b.applied = append(b.applied, append(append([]netip.Prefix(nil), v4...), v6...))
	b.gone = false
	return nil
}

func TestFirewallSync(t *testing.T) {
	be := &fakeBackend{}
	f := &firewall{cfg: config{url: "https://192.0.2.10"}, log: slog.New(slog.NewTextHandler(io.Discard, nil)), backend: be, mode: "fake"}
	ctx := context.Background()
	list := agentapi.Blocks{Version: "v1", Addresses: []string{"203.0.113.7", "192.0.2.10", "10.0.0.1"}}

	f.sync(ctx, list)
	if len(be.applied) != 1 || f.applied != 1 || f.version != "v1" {
		t.Fatalf("first sync: %v applied %d version %q", be.applied, f.applied, f.version)
	}
	if want := []string{"192.0.2.10", "10.0.0.1"}; !reflect.DeepEqual(f.skipped, want) {
		t.Fatalf("skipped %v", f.skipped)
	}
	f.sync(ctx, list)
	if len(be.applied) != 1 {
		t.Fatal("same version applied again")
	}
	be.gone, f.verified = true, time.Now().Add(-fwVerifyEvery)
	f.sync(ctx, list)
	if len(be.applied) != 2 {
		t.Fatal("lost rules not applied again")
	}
	be.err = errors.New("nft: no such file")
	f.sync(ctx, agentapi.Blocks{Version: "v2", Addresses: []string{"203.0.113.8"}})
	if f.lastErr == "" || f.version != "v1" {
		t.Fatalf("failed apply: err %q version %q", f.lastErr, f.version)
	}
	be.err = nil
	f.sync(ctx, agentapi.Blocks{Version: "v2", Addresses: []string{"203.0.113.8"}})
	if f.lastErr != "" || f.version != "v2" || len(be.applied) != 3 {
		t.Fatalf("retry: err %q version %q", f.lastErr, f.version)
	}
	if p := f.pollBody(); p.Version != "v2" || p.Backend != "fake" || p.Applied != 1 || len(p.Skipped) != 0 {
		t.Fatalf("poll %+v", p)
	}
}

func TestFwPollWait(t *testing.T) {
	for in, want := range map[int]time.Duration{0: 15 * time.Second, 1: 5 * time.Second, 60: time.Minute, 9999: 5 * time.Minute} {
		if got := fwPollWait(in); got != want {
			t.Fatalf("%d: %v", in, got)
		}
	}
}
