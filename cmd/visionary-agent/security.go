package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/styropyr0/visionary/internal/agentapi"
)

// Host security findings (Linux). Each collector is optional: one that
// cannot read its source logs that once and reports nothing.

// --- SSH logins and sudo, from the auth log or journald ---

// authCollector reads new auth log lines since the previous report and
// aggregates them per (kind, user, address, command) into one event with a
// count. It starts at the end: history from before the agent started is not
// reported.
type authCollector struct {
	a    *agent
	mode string // "file", "journald" or "" (off)
	path string

	// file mode
	info os.FileInfo
	off  int64
	// journald mode
	cursor string
	since  time.Time
}

const authMaxRead = 4 << 20 // bytes of log read per report

func newAuthCollector(a *agent, setting string) *authCollector {
	c := &authCollector{a: a, since: time.Now()}
	switch setting {
	case "off", "false", "none":
		return c
	case "journald":
		c.mode = "journald"
		return c
	case "":
		if runtime.GOOS != "linux" {
			return c
		}
		for _, p := range []string{"/var/log/auth.log", "/var/log/secure"} {
			if _, err := os.Stat(hostPath(p)); err == nil {
				c.mode, c.path = "file", hostPath(p)
				break
			}
		}
		if c.mode == "" {
			c.mode = "journald"
		}
	default:
		// An explicit file is read as given, not under HOST_ROOT.
		c.mode, c.path = "file", setting
	}
	if c.mode == "file" {
		// Baseline: start at the current end of the file.
		if st, err := os.Stat(c.path); err == nil {
			c.info, c.off = st, st.Size()
		}
	}
	return c
}

func (c *authCollector) collect(ctx context.Context) []agentapi.Event {
	agg := &authEvents{byKey: map[string]*agentapi.Event{}}
	switch c.mode {
	case "file":
		c.readFile(agg)
	case "journald":
		c.readJournal(ctx, agg)
	}
	return agg.list()
}

func (c *authCollector) readFile(agg *authEvents) {
	f, err := os.Open(c.path)
	if err != nil {
		c.a.once("auth-file", "auth log unreadable; skipping SSH and sudo", "path", c.path, "err", err)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return
	}
	if c.info == nil {
		// The file appeared after start: read it from the beginning.
		c.off = 0
	} else if !os.SameFile(c.info, st) || st.Size() < c.off {
		c.off = 0 // rotated or truncated
	}
	c.info = st
	if st.Size() == c.off {
		return
	}
	if _, err := f.Seek(c.off, io.SeekStart); err != nil {
		return
	}
	buf, err := io.ReadAll(io.LimitReader(f, authMaxRead))
	if err != nil {
		return
	}
	// Only whole lines; a half-written one is read next time.
	end := bytes.LastIndexByte(buf, '\n')
	if end < 0 {
		if len(buf) < authMaxRead {
			return
		}
		end = len(buf) - 1
	}
	c.off += int64(end + 1)
	for _, line := range strings.Split(string(buf[:end]), "\n") {
		prog, msg := syslogMessage(line)
		agg.parse(prog, msg)
	}
}

// syslogProg finds the program of a syslog line, in the classic
// ("Oct 19 10:00:00 host sshd[12]: ...") and the RFC 3339 format.
var syslogProg = regexp.MustCompile(`\s(sshd(?:-session)?|sudo)(?:\[\d+\])?:\s`)

func syslogMessage(line string) (prog, msg string) {
	loc := syslogProg.FindStringSubmatchIndex(line)
	if loc == nil {
		return "", ""
	}
	return line[loc[2]:loc[3]], line[loc[1]:]
}

func (c *authCollector) readJournal(ctx context.Context, agg *authEvents) {
	args := []string{"-o", "json", "--no-pager", "-q", "-t", "sshd", "-t", "sshd-session", "-t", "sudo"}
	if c.cursor != "" {
		args = append(args, "--after-cursor="+c.cursor)
	} else {
		args = append(args, "--since=@"+strconv.FormatInt(c.since.Unix(), 10))
	}
	out, err := hostCommand(ctx, 30*time.Second, "journalctl", args...)
	if err != nil {
		c.a.once("journald", "journald unreadable; skipping SSH and sudo", "err", err)
		return
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var e struct {
			Cursor string `json:"__CURSOR"`
			Ident  string `json:"SYSLOG_IDENTIFIER"`
			// MESSAGE is a byte array when it is not valid UTF-8; those
			// lines are skipped.
			Message any `json:"MESSAGE"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		if e.Cursor != "" {
			c.cursor = e.Cursor
		}
		if msg, ok := e.Message.(string); ok {
			agg.parse(e.Ident, msg)
		}
	}
	if c.cursor == "" {
		c.since = time.Now().Add(-time.Second)
	}
}

var (
	sshFailed   = regexp.MustCompile(`Failed (?:\S+) for (?:invalid user )?(\S*) from (\S+) port`)
	sshAccepted = regexp.MustCompile(`Accepted (\S+) for (\S+) from (\S+) port`)
	sudoCommand = regexp.MustCompile(`^\s*(\S+)\s*:\s.*?(?:USER=(\S+)\s*;\s*)?COMMAND=(.*)$`)
	repeated    = regexp.MustCompile(`^message repeated (\d+) times: \[\s*(.*?)\s*\]$`)
)

type authEvents struct {
	byKey map[string]*agentapi.Event
}

func (g *authEvents) parse(prog, msg string) {
	if prog == "" {
		return
	}
	times := 1
	if m := repeated.FindStringSubmatch(msg); m != nil {
		times, _ = strconv.Atoi(m[1])
		msg = m[2]
	}
	switch {
	case strings.HasPrefix(prog, "sshd"):
		if m := sshFailed.FindStringSubmatch(msg); m != nil {
			g.add(agentapi.Event{Kind: "ssh_failed", Actor: m[1], IP: m[2]}, times)
		} else if m := sshAccepted.FindStringSubmatch(msg); m != nil {
			g.add(agentapi.Event{Kind: "ssh_login", Actor: m[2], IP: m[3], Detail: m[1]}, times)
		}
	case prog == "sudo":
		if m := sudoCommand.FindStringSubmatch(msg); m != nil {
			as := m[2]
			if as == "" {
				as = "root"
			}
			g.add(agentapi.Event{Kind: "sudo", Actor: m[1], Target: clipRunes(redact(m[3]), 300), Detail: "as " + as}, times)
		}
	}
}

func (g *authEvents) add(e agentapi.Event, n int) {
	key := e.Kind + "\x00" + e.Actor + "\x00" + e.IP + "\x00" + e.Target
	if old, ok := g.byKey[key]; ok {
		old.Count += n
		return
	}
	e.Count = n
	g.byKey[key] = &e
}

func (g *authEvents) list() []agentapi.Event {
	out := make([]agentapi.Event, 0, len(g.byKey))
	for _, e := range g.byKey {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out
}

// --- listening ports ---

// portCollector compares the listening sockets with the previous report's.
// The first reading is the baseline and reports nothing.
type portCollector struct {
	a    *agent
	prev map[string]string // "tcp 0.0.0.0:5432" -> process name
}

func (c *portCollector) collect() []agentapi.Event {
	if runtime.GOOS != "linux" {
		return nil
	}
	cur, inodes, err := listening()
	if err != nil {
		c.a.once("ports", "listening ports unreadable; skipping them", "err", err)
		return nil
	}
	if c.prev == nil {
		names := socketOwners(inodes)
		c.prev = map[string]string{}
		for k, ino := range cur {
			c.prev[k] = names[ino]
		}
		return nil
	}
	var opened []string
	for k := range cur {
		if _, ok := c.prev[k]; !ok {
			opened = append(opened, k)
		}
	}
	var names map[string]string
	if len(opened) > 0 {
		names = socketOwners(inodes)
	}
	var out []agentapi.Event
	next := make(map[string]string, len(cur))
	for k, ino := range cur {
		if name, ok := c.prev[k]; ok {
			next[k] = name
			continue
		}
		next[k] = names[ino]
		out = append(out, agentapi.Event{Kind: "port_opened", Target: strings.TrimSpace(k + " " + names[ino]), Count: 1})
	}
	for k, name := range c.prev {
		if _, ok := cur[k]; !ok {
			out = append(out, agentapi.Event{Kind: "port_closed", Target: strings.TrimSpace(k + " " + name), Count: 1})
		}
	}
	c.prev = next
	sort.Slice(out, func(i, j int) bool { return out[i].Target < out[j].Target })
	return out
}

// listening reads the host's listening TCP sockets and bound UDP sockets
// outside the ephemeral port range, by "proto addr:port", with their socket
// inodes. PID 1's view is the host's network namespace when the agent shares
// the host's PID namespace.
func listening() (map[string]string, map[string]bool, error) {
	lo, hi := ephemeralRange()
	out := map[string]string{}
	inodes := map[string]bool{}
	read := 0
	for _, f := range []struct{ file, proto string }{{"tcp", "tcp"}, {"tcp6", "tcp"}, {"udp", "udp"}, {"udp6", "udp"}} {
		b, err := os.ReadFile(procPath("1", "net", f.file))
		if err != nil {
			if b, err = os.ReadFile(procPath("net", f.file)); err != nil {
				continue
			}
		}
		read++
		for _, line := range strings.Split(string(b), "\n")[1:] {
			fs := strings.Fields(line)
			if len(fs) < 10 {
				continue
			}
			ip, port, ok := hexAddr(fs[1])
			if !ok {
				continue
			}
			if f.proto == "tcp" && fs[3] != "0A" { // LISTEN
				continue
			}
			if f.proto == "udp" {
				_, rport, _ := hexAddr(fs[2])
				if fs[3] != "07" || rport != 0 || (port >= lo && port <= hi) {
					continue
				}
			}
			key := f.proto + " " + net.JoinHostPort(ip, strconv.Itoa(port))
			out[key] = fs[9]
			inodes[fs[9]] = true
		}
	}
	if read == 0 {
		return nil, nil, errors.New("no /proc/net/tcp")
	}
	return out, inodes, nil
}

// hexAddr decodes "0100007F:1F90" (or the 32-digit IPv6 form) from /proc/net.
func hexAddr(s string) (string, int, bool) {
	host, p, ok := strings.Cut(s, ":")
	if !ok {
		return "", 0, false
	}
	port, err := strconv.ParseUint(p, 16, 16)
	if err != nil {
		return "", 0, false
	}
	raw, err := hex.DecodeString(host)
	if err != nil || (len(raw) != 4 && len(raw) != 16) {
		return "", 0, false
	}
	// Each 32-bit word is in host (little-endian) order.
	for i := 0; i+4 <= len(raw); i += 4 {
		raw[i], raw[i+1], raw[i+2], raw[i+3] = raw[i+3], raw[i+2], raw[i+1], raw[i]
	}
	return net.IP(raw).String(), int(port), true
}

func ephemeralRange() (int, int) {
	b, err := os.ReadFile(procPath("sys", "net", "ipv4", "ip_local_port_range"))
	if err == nil {
		f := strings.Fields(string(b))
		if len(f) == 2 {
			lo, err1 := strconv.Atoi(f[0])
			hi, err2 := strconv.Atoi(f[1])
			if err1 == nil && err2 == nil {
				return lo, hi
			}
		}
	}
	return 32768, 60999
}

// socketOwners maps socket inodes to the name of a process holding them.
// Reading other processes' descriptors needs root; without it names stay
// empty.
func socketOwners(want map[string]bool) map[string]string {
	out := map[string]string{}
	dirs, _ := filepath.Glob(procPath("[0-9]*"))
	for _, dir := range dirs {
		fds, err := os.ReadDir(filepath.Join(dir, "fd"))
		if err != nil {
			continue
		}
		var name string
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(dir, "fd", fd.Name()))
			if err != nil || !strings.HasPrefix(link, "socket:[") {
				continue
			}
			ino := strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")
			if !want[ino] || out[ino] != "" {
				continue
			}
			if name == "" {
				b, _ := os.ReadFile(filepath.Join(dir, "comm"))
				name = strings.TrimSpace(string(b))
			}
			out[ino] = name
		}
		if len(out) == len(want) {
			break
		}
	}
	return out
}

// --- pending package updates ---

// updateChecker counts pending updates every 6 hours (the first check a
// minute after start) with whichever package manager the host has. It does
// not refresh the package lists; the host's own timers do that.
type updateChecker struct {
	a       *agent
	mu      sync.Mutex
	pending []agentapi.Event
}

const updateEvery = 6 * time.Hour

func (u *updateChecker) run(ctx context.Context) {
	if runtime.GOOS != "linux" {
		return
	}
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		total, security, err := pendingUpdates(ctx)
		if err != nil {
			u.a.once("updates", "pending updates unavailable; skipping them", "err", err)
		} else if total > 0 {
			u.mu.Lock()
			u.pending = []agentapi.Event{{Kind: "updates_pending", Count: total, Security: security,
				Detail: strconv.Itoa(security) + " security"}}
			u.mu.Unlock()
		}
		timer.Reset(updateEvery)
	}
}

// take hands the latest finding to one report.
func (u *updateChecker) take() []agentapi.Event {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := u.pending
	u.pending = nil
	return out
}

func pendingUpdates(ctx context.Context) (total, security int, err error) {
	has := func(p string) bool { _, err := os.Stat(hostPath(p)); return err == nil }
	const wait = 5 * time.Minute
	switch {
	case has("/usr/lib/update-notifier/apt-check"):
		// Prints "total;security" on stderr.
		out, err := hostCommand(ctx, wait, "/usr/lib/update-notifier/apt-check")
		if err != nil {
			return 0, 0, err
		}
		t, s, ok := strings.Cut(strings.TrimSpace(lastLine(out)), ";")
		if !ok {
			return 0, 0, errors.New("unexpected apt-check output")
		}
		total, _ = strconv.Atoi(t)
		security, _ = strconv.Atoi(s)
		return total, security, nil
	case has("/usr/bin/apt-get"):
		out, err := hostCommand(ctx, wait, "apt-get", "-s", "-o", "Debug::NoLocking=true", "upgrade")
		if err != nil {
			return 0, 0, err
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(line, "Inst ") {
				total++
				if strings.Contains(line, "-security") {
					security++
				}
			}
		}
		return total, security, nil
	case has("/usr/bin/dnf") || has("/usr/bin/yum"):
		pm := "dnf"
		if !has("/usr/bin/dnf") {
			pm = "yum"
		}
		// check-update exits 100 when updates are pending.
		out, err := hostCommand(ctx, wait, pm, "-q", "check-update")
		var ce *commandError
		if errors.As(err, &ce) && ce.code == 100 {
			out, err = ce.out, nil
		}
		if err != nil {
			return 0, 0, err
		}
		total = countPackages(out)
		// One line per advisory and package: "FEDORA-2024-... Important/Sec. openssl-..."
		if sec, err := hostCommand(ctx, wait, pm, "-q", "updateinfo", "list", "--security"); err == nil {
			for _, line := range strings.Split(string(sec), "\n") {
				if len(strings.Fields(line)) >= 3 {
					security++
				}
			}
		}
		return total, security, nil
	case has("/sbin/apk"):
		out, err := hostCommand(ctx, wait, "apk", "list", "-u")
		if err != nil {
			return 0, 0, err
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "upgradable from") {
				total++
			}
		}
		return total, 0, nil
	}
	return 0, 0, errors.New("no apt, dnf, yum or apk")
}

// countPackages counts the package lines of dnf/yum output, up to the
// obsoletes section.
func countPackages(out []byte) int {
	n := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "Obsoleting") {
			break
		}
		if f := strings.Fields(line); len(f) >= 3 && strings.Contains(f[0], ".") {
			n++
		}
	}
	return n
}

func lastLine(b []byte) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	return lines[len(lines)-1]
}
