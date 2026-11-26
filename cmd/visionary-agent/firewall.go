package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/styropyr0/visionary/internal/agentapi"
)

// IP blocking: the agent polls the server for addresses to block and keeps
// them in a table or ipsets of its own, dropped before conntrack and Docker

const (
	fwPollDefault = 15 * time.Second
	fwVerifyEvery = 5 * time.Minute
	fwTooOldWait  = 5 * time.Minute
	fwCmdTimeout  = 30 * time.Second
	fwSkippedMax  = 50
)

// cmdRunner runs the host's firewall tools, faked in tests
type cmdRunner interface {
	run(ctx context.Context, stdin, name string, args ...string) ([]byte, error)
	has(name string) bool
}

type fwBackend interface {
	name() string
	apply(ctx context.Context, v4, v6 []netip.Prefix) error
	present(ctx context.Context) bool
	clear(ctx context.Context) error
}

type hostRunner struct{}

var hostBinDirs = []string{"/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin"}

func (hostRunner) has(name string) bool {
	for _, d := range hostBinDirs {
		if st, err := os.Stat(hostPath(filepath.Join(d, name))); err == nil && !st.IsDir() {
			return true
		}
	}
	return false
}

func (hostRunner) run(ctx context.Context, stdin, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, fwCmdTimeout)
	defer cancel()
	cmd := hostExec(ctx, name, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out, &commandError{code: ee.ExitCode(), stderr: strings.TrimSpace(stderr.String()), out: out}
	}
	return out, err
}

// pickBackend returns nil with the reason when this host cannot block
func pickBackend(goos string, r cmdRunner) (fwBackend, string) {
	switch {
	case goos != "linux":
		return nil, "IP blocking needs Linux"
	case r.has("nft"):
		return &nftBackend{r: r}, ""
	case r.has("ipset") && r.has("iptables"):
		return &iptablesBackend{r: r}, ""
	}
	return nil, "install nftables (nft), or ipset and iptables, to block IPs"
}

type firewall struct {
	cfg     config
	log     *slog.Logger
	client  *http.Client
	backend fwBackend
	mode    string
	setup   string

	ok        bool
	version   string
	applied   int
	lastErr   string
	skipped   []string
	verified  time.Time
	logged    string
	tooOld    bool
	serverIPs []netip.Addr
}

func newFirewall(cfg config, logger *slog.Logger, r cmdRunner) *firewall {
	f := &firewall{cfg: cfg, log: logger, client: &http.Client{Timeout: 30 * time.Second}, mode: "off"}
	if !cfg.firewall {
		return f
	}
	f.backend, f.setup = pickBackend(runtime.GOOS, r)
	f.mode = "unsupported"
	if f.backend != nil {
		f.mode = f.backend.name()
	}
	return f
}

func (f *firewall) run(ctx context.Context) {
	if f.setup != "" {
		f.log.Info("IP blocking unavailable", "reason", f.setup)
	}
	var wait, backoff time.Duration
	retry := func() {
		backoff = min(max(backoffMin, backoff*2), backoffMax)
		wait = backoff
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		var resp agentapi.Blocks
		status, msg, err := postJSON(ctx, f.client, f.cfg, agentapi.BlocksPath, f.pollBody(), &resp)
		switch {
		case err != nil:
			f.log.Warn("IP blocks poll failed; will retry", "err", err)
			retry()
			continue
		case status == http.StatusNotFound:
			if !f.tooOld {
				f.log.Info("the server does not hand out IP blocks yet; asking again every 5 minutes")
				f.tooOld = true
			}
			wait = fwTooOldWait
			continue
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			f.log.Warn("the server refused the key for IP blocks", "status", status, "error", msg)
			retry()
			continue
		case status >= 300:
			f.log.Warn("IP blocks poll failed; will retry", "status", status, "error", msg)
			retry()
			continue
		}
		f.tooOld, backoff = false, 0
		wait = fwPollWait(resp.PollS)
		f.sync(ctx, resp)
	}
}

func fwPollWait(s int) time.Duration {
	if s <= 0 {
		return fwPollDefault
	}
	return time.Duration(min(max(s, 5), 300)) * time.Second
}

func (f *firewall) pollBody() agentapi.BlocksPoll {
	p := agentapi.BlocksPoll{Host: f.cfg.host, Version: f.version, Backend: f.mode, Applied: f.applied, Error: f.lastErr, Skipped: f.skipped}
	if f.mode == "unsupported" {
		p.Error = f.setup
	}
	return p
}

// sync applies the list when its version changed, the last try failed, or
// the rules went missing, as after a firewall reload
func (f *firewall) sync(ctx context.Context, b agentapi.Blocks) {
	if f.backend == nil {
		return
	}
	lost := false
	if time.Since(f.verified) >= fwVerifyEvery {
		f.verified = time.Now()
		lost = f.ok && !f.backend.present(ctx)
	}
	if f.ok && f.lastErr == "" && b.Version == f.version && !lost {
		return
	}
	if lost {
		f.log.Info("IP block rules are missing; applying them again", "backend", f.mode)
	}
	addrs := b.Addresses
	if len(addrs) > agentapi.MaxBlocks {
		addrs = addrs[:agentapi.MaxBlocks]
	}
	v4, v6, refused := filterBlocks(addrs, f.protected(ctx))
	f.skipped = f.skipped[:0]
	for _, r := range refused {
		if len(f.skipped) == fwSkippedMax {
			break
		}
		f.skipped = append(f.skipped, r.addr)
	}
	if len(refused) > 0 && f.logged != b.Version {
		f.log.Info("not blocking these addresses", "version", b.Version, "count", len(refused), "addresses", refusalText(refused, fwSkippedMax))
	}
	f.logged = b.Version
	if err := f.backend.apply(ctx, v4, v6); err != nil {
		msg := clipRunes(err.Error(), 300)
		if msg != f.lastErr {
			f.log.Warn("could not apply IP blocks", "backend", f.mode, "err", msg)
		}
		f.lastErr = msg
		return
	}
	if !f.ok || f.version != b.Version || f.lastErr != "" {
		f.log.Info("IP blocks applied", "backend", f.mode, "version", b.Version, "addresses", len(v4)+len(v6))
	}
	f.ok, f.version, f.applied, f.lastErr, f.verified = true, b.Version, len(v4)+len(v6), "", time.Now()
}

// protected are the addresses no block may cover: the server's and every
// live SSH session's
func (f *firewall) protected(ctx context.Context) []netip.Addr {
	if ips := lookupServer(ctx, f.cfg.url); len(ips) > 0 {
		f.serverIPs = ips
	}
	return append(append([]netip.Addr(nil), f.serverIPs...), sshPeers()...)
}

// clearFirewall removes what either backend left on the host
func clearFirewall(ctx context.Context, r cmdRunner, logger *slog.Logger) error {
	var errs []error
	if r.has("nft") {
		errs = append(errs, (&nftBackend{r: r}).clear(ctx))
	}
	if r.has("ipset") && r.has("iptables") {
		errs = append(errs, (&iptablesBackend{r: r}).clear(ctx))
	}
	err := errors.Join(errs...)
	if err == nil {
		logger.Info("IP block rules removed")
	}
	return err
}
