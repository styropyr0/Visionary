package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"log/slog"
	"math"
	"net"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/process"

	"github.com/styropyr0/visionary/internal/agentapi"
)

// agent builds reports. Only the main loop calls report, so the collectors'
// state needs no lock (the update checker, which runs on its own, has one).
type agent struct {
	cfg    config
	log    *slog.Logger
	logged sync.Map // messages logged once

	procs   map[int32]procTime // CPU time per process at the last report
	procsAt time.Time
	auth    *authCollector
	ports   *portCollector
	updates *updateChecker
	keys    *sshKeyCollector // nil when off, like files and suspect
	files   *fileCollector
	suspect *procCollector
	docker  *dockerCollector // nil when VISIONARY_AGENT_DOCKER is off
}

type procTime struct {
	cpu     float64 // user + system seconds
	created int64
}

const topProcesses = 15

func newAgent(cfg config, logger *slog.Logger) *agent {
	a := &agent{cfg: cfg, log: logger}
	if cfg.docker != "off" {
		a.docker = newDockerCollector(a, cfg.docker)
	}
	if cfg.security {
		a.auth = newAuthCollector(a, cfg.authLog)
		a.ports = &portCollector{a: a}
		a.updates = &updateChecker{a: a}
		if cfg.sshKeys {
			a.keys = &sshKeyCollector{a: a}
		}
		if cfg.fileIntegrity {
			a.files = &fileCollector{a: a}
		}
		if cfg.processChecks {
			a.suspect = &procCollector{a: a}
		}
		if cfg.containerRisks && a.docker != nil {
			a.docker.risks = newRiskTracker()
		}
	}
	return a
}

// once logs a message the first time key is seen, for sources that stay
// unreadable on this host.
func (a *agent) once(key, msg string, args ...any) {
	if _, seen := a.logged.LoadOrStore(key, true); !seen {
		a.log.Info(msg, args...)
	}
}

func (a *agent) report(ctx context.Context, samples []agentapi.Sample) *agentapi.Report {
	now := time.Now().UTC()
	inv, runtimeDesc := a.inventory()
	rep := &agentapi.Report{
		Host:      a.cfg.host,
		Service:   a.cfg.service,
		At:        now.Format(time.RFC3339Nano),
		ReportS:   a.cfg.report.Seconds(),
		Runtime:   runtimeDesc,
		Inventory: inv,
		Samples:   samples,
		Processes: a.processes(now),
		Mounts:    a.mounts(),
		Units:     a.units(ctx),
	}
	if a.docker != nil {
		rep.Containers, rep.ContainerEvents = a.docker.report(ctx)
		rep.ContainerSamples = a.docker.takeSamples()
	}
	if a.cfg.security {
		rep.Events = append(rep.Events, a.auth.collect(ctx)...)
		rep.Events = append(rep.Events, a.ports.collect()...)
		rep.Events = append(rep.Events, a.updates.take()...)
		if a.keys != nil {
			rep.Events = append(rep.Events, a.keys.collect()...)
		}
		if a.files != nil {
			rep.Events = append(rep.Events, a.files.collect()...)
		}
		if a.suspect != nil {
			rep.Events = append(rep.Events, a.suspect.collect()...)
		}
		if a.docker != nil && a.docker.risks != nil {
			rep.Events = append(rep.Events, a.docker.risks.take()...)
		}
	}
	return rep
}

// inventory describes the machine, and returns the OS description that goes
// to system_metrics.runtime, e.g. "Ubuntu 24.04 (linux 6.8.0-45-generic)".
func (a *agent) inventory() (agentapi.Inventory, string) {
	inv := agentapi.Inventory{AgentVersion: version, Arch: runtime.GOARCH, IP: a.primaryIP()}
	goos := runtime.GOOS
	if info, err := host.Info(); err == nil {
		inv.OS = osLabel(info)
		inv.Kernel = info.KernelVersion
		if info.KernelArch != "" {
			inv.Arch = info.KernelArch
		}
		if info.BootTime > 0 {
			inv.BootTime = time.Unix(int64(info.BootTime), 0).UTC().Format(time.RFC3339)
		}
		if info.OS != "" {
			goos = info.OS
		}
	} else {
		a.once("host-info", "host info unavailable", "err", err)
	}
	if n, err := cpu.Counts(true); err == nil {
		inv.CPUCount = n
	}
	if vm, err := mem.VirtualMemory(); err == nil {
		inv.MemTotalMB = float64(vm.Total) / (1 << 20)
	}
	desc := strings.TrimSpace(inv.OS + " (" + strings.TrimSpace(goos+" "+inv.Kernel) + ")")
	return inv, desc
}

// osLabel is "Ubuntu 24.04", "Debian 12.5", "Windows 11 Pro" and the like.
func osLabel(info *host.InfoStat) string {
	p := info.Platform
	if p == "" {
		p = info.OS
	}
	if p != "" && strings.ToLower(p) == p {
		p = strings.ToUpper(p[:1]) + p[1:]
	}
	if info.PlatformVersion != "" && !strings.Contains(p, info.PlatformVersion) {
		p += " " + info.PlatformVersion
	}
	return p
}

// primaryIP is the local address of the route to the server (no packet is
// sent), or the first non-loopback address when that is a loopback.
func (a *agent) primaryIP() string {
	if u, err := url.Parse(a.cfg.url); err == nil {
		port := u.Port()
		if port == "" {
			port = "443"
		}
		if c, err := net.Dial("udp", net.JoinHostPort(u.Hostname(), port)); err == nil {
			ip := c.LocalAddr().(*net.UDPAddr).IP
			c.Close()
			if !ip.IsLoopback() {
				return ip.String()
			}
		}
	}
	addrs, _ := net.InterfaceAddrs()
	for _, ad := range addrs {
		if n, ok := ad.(*net.IPNet); ok && !n.IP.IsLoopback() && !n.IP.IsLinkLocalUnicast() && n.IP.To4() != nil {
			return n.IP.String()
		}
	}
	return ""
}

// processes are the top processes by CPU since the last report plus the top
// by resident memory. The first report, which has nothing to compare with,
// uses each process's average since it started.
func (a *agent) processes(now time.Time) []agentapi.Process {
	list, err := process.Processes()
	if err != nil {
		a.once("processes", "process list unavailable", "err", err)
		return nil
	}
	type row struct {
		p   *process.Process
		cpu float64
		rss uint64
	}
	wall := now.Sub(a.procsAt).Seconds()
	seen := make(map[int32]procTime, len(list))
	rows := make([]row, 0, len(list))
	for _, p := range list {
		t, err := p.Times()
		if err != nil {
			continue
		}
		created, _ := p.CreateTime()
		total := t.User + t.System
		seen[p.Pid] = procTime{cpu: total, created: created}
		r := row{p: p}
		if prev, ok := a.procs[p.Pid]; ok && prev.created == created && wall > 0 && total >= prev.cpu {
			r.cpu = 100 * (total - prev.cpu) / wall
		} else if a.procs == nil {
			r.cpu, _ = p.CPUPercent()
		} else if created > 0 {
			// Started since the last report.
			if age := now.Sub(time.UnixMilli(created)).Seconds(); age > 0 {
				r.cpu = 100 * total / age
			}
		}
		if m, err := p.MemoryInfo(); err == nil {
			r.rss = m.RSS
		}
		rows = append(rows, r)
	}
	a.procs, a.procsAt = seen, now

	pick := map[int32]row{}
	sort.Slice(rows, func(i, j int) bool { return rows[i].cpu > rows[j].cpu })
	for _, r := range rows[:min(topProcesses, len(rows))] {
		pick[r.p.Pid] = r
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].rss > rows[j].rss })
	for _, r := range rows[:min(topProcesses, len(rows))] {
		pick[r.p.Pid] = r
	}
	users := passwdNames()
	out := make([]agentapi.Process, 0, len(pick))
	for _, r := range pick {
		name, _ := r.p.Name()
		if name == "" {
			continue
		}
		user := ""
		if uids, err := r.p.Uids(); err == nil && len(uids) > 0 {
			user = users[uids[0]]
			if user == "" {
				user = strconv.FormatUint(uint64(uids[0]), 10)
			}
		} else if u, err := r.p.Username(); err == nil {
			user = u // Windows and macOS
		}
		cmd, _ := r.p.Cmdline()
		out = append(out, agentapi.Process{
			PID: r.p.Pid, Name: name, Username: user,
			CPUPercent: round(r.cpu, 1), RSSMB: round(float64(r.rss)/(1<<20), 1),
			Cmdline: clipRunes(redact(cmd), 300),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CPUPercent > out[j].CPUPercent })
	return out
}

// passwdNames maps uids to user names from /etc/passwd.
func passwdNames() map[uint32]string {
	out := map[uint32]string{}
	b, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(line, ":")
		if len(f) < 3 {
			continue
		}
		if uid, err := strconv.ParseUint(f[2], 10, 32); err == nil {
			out[uint32(uid)] = f[0]
		}
	}
	return out
}

// pseudoFS are filesystems that hold no disk space worth watching.
var pseudoFS = map[string]bool{
	"proc": true, "sysfs": true, "tmpfs": true, "devtmpfs": true, "devpts": true, "cgroup": true, "cgroup2": true,
	"overlay": true, "squashfs": true, "securityfs": true, "pstore": true, "bpf": true, "tracefs": true, "debugfs": true,
	"configfs": true, "fusectl": true, "mqueue": true, "hugetlbfs": true, "autofs": true, "binfmt_misc": true, "nsfs": true,
	"ramfs": true, "rpc_pipefs": true, "efivarfs": true, "selinuxfs": true, "nfsd": true, "fuse.lxcfs": true,
	"fuse.gvfsd-fuse": true, "fuse.portal": true, "shm": true, "iso9660": true, "udf": true,
}

// pseudoMounts are mount points of the system and of container runtimes.
var pseudoMounts = []string{"/proc", "/sys", "/dev", "/run", "/snap", "/var/lib/docker", "/var/lib/containers", "/var/lib/kubelet", "/var/snap"}

// mounts reports every real filesystem once (bind mounts of the same device
// count once, under the shortest mount point), or only VISIONARY_DISK_PATHS.
func (a *agent) mounts() []agentapi.Mount {
	parts, err := disk.Partitions(false)
	if err != nil {
		a.once("mounts", "mount list unavailable", "err", err)
		return nil
	}
	want := map[string]bool{}
	for _, p := range a.cfg.disks {
		want[p] = true
	}
	sort.Slice(parts, func(i, j int) bool { return len(parts[i].Mountpoint) < len(parts[j].Mountpoint) })
	devices := map[string]bool{}
	var out []agentapi.Mount
	for _, p := range parts {
		mp := p.Mountpoint
		if len(want) > 0 {
			if !want[mp] {
				continue
			}
		} else if pseudoFS[p.Fstype] || underAny(mp, pseudoMounts) || devices[p.Device] {
			continue
		}
		u, err := disk.Usage(mp)
		if err != nil || u.Total == 0 {
			continue
		}
		devices[p.Device] = true
		m := agentapi.Mount{
			Mount: mp, Device: p.Device, FSType: p.Fstype,
			UsedPercent: round(u.UsedPercent, 1),
			FreeGB:      round(float64(u.Free)/(1<<30), 2),
			TotalGB:     round(float64(u.Total)/(1<<30), 2),
		}
		if u.InodesTotal > 0 {
			v := round(u.InodesUsedPercent, 1)
			m.InodesPercent = &v
		}
		out = append(out, m)
		if len(out) >= 100 {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mount < out[j].Mount })
	return out
}

func underAny(p string, roots []string) bool {
	for _, r := range roots {
		if p == r || strings.HasPrefix(p, r+"/") {
			return true
		}
	}
	return false
}

// units are the failed systemd services plus the watched ones, from
// systemctl. Nil when systemctl is not there or cannot reach systemd.
func (a *agent) units(ctx context.Context) []agentapi.Unit {
	if runtime.GOOS != "linux" {
		return nil
	}
	out, err := hostCommand(ctx, 20*time.Second, "systemctl", "list-units", "--type=service", "--all", "--no-legend", "--plain", "--no-pager")
	if err != nil {
		a.once("units", "systemd units unavailable; skipping them", "err", err)
		return nil
	}
	all := map[string]agentapi.Unit{}
	var failed []agentapi.Unit
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(strings.TrimLeft(sc.Text(), "●* "))
		if len(f) < 4 {
			continue
		}
		u := agentapi.Unit{Unit: f[0], State: f[2], Sub: f[3]}
		all[u.Unit] = u
		if u.State == "failed" {
			failed = append(failed, u)
		}
	}
	units := failed
	for _, w := range a.cfg.units {
		name := w
		if !strings.Contains(name, ".") {
			name += ".service"
		}
		u, ok := all[name]
		if !ok {
			u = agentapi.Unit{Unit: name, State: "not-found"}
		}
		if u.State != "failed" {
			units = append(units, u)
		}
	}
	if units == nil {
		units = []agentapi.Unit{} // systemd answered: nothing failed
	}
	return units
}

// hostCommand runs a program of the host with a fixed environment.
func hostCommand(ctx context.Context, timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = []string{"LC_ALL=C", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "SYSTEMD_COLORS=0"}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return out, &commandError{code: ee.ExitCode(), stderr: strings.TrimSpace(stderr.String()), out: out}
		}
		return out, err
	}
	return append(out, stderr.Bytes()...), nil
}

// commandError is a non-zero exit, with what the command printed.
type commandError struct {
	code   int
	stderr string
	out    []byte
}

func (e *commandError) Error() string {
	msg := "exit status " + strconv.Itoa(e.code)
	if e.stderr != "" {
		msg += ": " + clipRunes(e.stderr, 200)
	}
	return msg
}

var (
	secretFlag = regexp.MustCompile(`(?i)(--?[a-z0-9._-]*(?:password|passwd|pwd|secret|token|api[-_]?key|apikey|access[-_]?key|private[-_]?key|credential)[a-z0-9._-]*(?:=|\s+))("[^"]*"|'[^']*'|\S+)`)
	secretEnv  = regexp.MustCompile(`(?i)\b([a-z0-9_]*(?:password|passwd|secret|token|api_?key|access_key|private_key)[a-z0-9_]*=)\S+`)
	urlCreds   = regexp.MustCompile(`(://[^/\s:@]+:)[^@\s/]+@`)
	bearer     = regexp.MustCompile(`(?i)\b(bearer\s+)\S+`)
	mysqlPass  = regexp.MustCompile(`(\s-p)\S+`)
)

// redact masks the obvious secrets of a command line: --password=..., token
// flags and variables, credentials in URLs, bearer tokens, mysql -p....
func redact(cmd string) string {
	cmd = secretFlag.ReplaceAllString(cmd, "${1}***")
	cmd = secretEnv.ReplaceAllString(cmd, "${1}***")
	cmd = urlCreds.ReplaceAllString(cmd, "${1}***@")
	cmd = bearer.ReplaceAllString(cmd, "${1}***")
	if strings.Contains(cmd, "mysql") || strings.Contains(cmd, "mariadb") {
		cmd = mysqlPass.ReplaceAllString(cmd, "${1}***")
	}
	return cmd
}

func clipRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func round(v float64, prec int) float64 {
	p := math.Pow(10, float64(prec))
	return math.Round(v*p) / p
}
