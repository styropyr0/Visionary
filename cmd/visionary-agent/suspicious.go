package main

import (
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/styropyr0/visionary/internal/agentapi"
)

// --- suspicious processes ---

// procCollector looks for processes that are bad wherever they run: started
// from /tmp or a deleted file, known miners, shells wired to a network
// socket, netcat running a program. There is no baseline; each (process,
// reason) is reported once, and forgotten when the process exits.
type procCollector struct {
	a        *agent
	reported map[string]bool // "pid\x00reason"
}

// procInfo is what the classifier looks at.
type procInfo struct {
	exe     string   // readlink /proc/PID/exe; "" when unreadable
	name    string   // /proc/PID/comm
	cmdline []string // the arguments
	fds     []string // readlink of fd 0 and 1
	// netSockets are the inodes of the process's TCP and UDP sockets; nil
	// when unknown, and then any socket counts.
	netSockets map[string]bool
	// exeOnDisk: a " (deleted)" executable's path exists again, so it was
	// replaced (a package upgrade) rather than removed.
	exeOnDisk bool
}

// knownMiners are names of miners and of the malware that drops them,
// matched against the process name and the executable's file name.
var knownMiners = map[string]bool{
	"xmrig": true, "xmr-stak": true, "xmr-stak-cpu": true, "minerd": true, "cpuminer": true,
	"cpuminer-multi": true, "kdevtmpfsi": true, "kinsing": true, "ethminer": true, "nbminer": true,
	"t-rex": true, "lolminer": true, "phoenixminer": true, "nanominer": true, "srbminer": true,
	"srbminer-multi": true, "teamredminer": true, "gminer": true, "ccminer": true,
}

var (
	shellNames  = map[string]bool{"sh": true, "bash": true, "dash": true, "zsh": true, "ksh": true, "ash": true, "busybox": true}
	netcatNames = map[string]bool{"nc": true, "ncat": true, "netcat": true, "socat": true, "nc.traditional": true, "nc.openbsd": true}
	tempDirs    = []string{"/tmp/", "/var/tmp/", "/dev/shm/"}
)

// classifyProcess returns why a process looks malicious, or nothing.
func classifyProcess(p procInfo) []string {
	var out []string
	exe, deleted := strings.CutSuffix(p.exe, " (deleted)")
	for _, d := range tempDirs {
		if strings.HasPrefix(exe, d) {
			out = append(out, "runs from "+strings.TrimSuffix(d, "/"))
			break
		}
	}
	if deleted && !p.exeOnDisk {
		out = append(out, "deleted executable")
	}
	name := strings.ToLower(p.name)
	base := ""
	if exe != "" {
		base = strings.ToLower(path.Base(exe))
	}
	switch {
	case knownMiners[name] || strings.HasPrefix(name, "xmrig"):
		out = append(out, "known miner "+name)
	case knownMiners[base] || strings.HasPrefix(base, "xmrig"):
		out = append(out, "known miner "+base)
	}
	if shellNames[name] || shellNames[base] {
		for _, fd := range p.fds {
			ino, ok := strings.CutPrefix(fd, "socket:[")
			if !ok {
				continue
			}
			if p.netSockets == nil || p.netSockets[strings.TrimSuffix(ino, "]")] {
				out = append(out, "shell attached to a network socket")
				break
			}
		}
	}
	if netcatNames[name] || netcatNames[base] {
		for _, arg := range p.cmdline[min(1, len(p.cmdline)):] {
			a := strings.ToLower(arg)
			if a == "-e" || (a == "-c" && name == "ncat") || a == "--exec" || a == "--sh-exec" ||
				strings.HasPrefix(a, "--exec=") || strings.HasPrefix(a, "--sh-exec=") ||
				strings.HasPrefix(a, "exec:") || strings.HasPrefix(a, "system:") {
				out = append(out, "netcat with exec")
				break
			}
		}
	}
	return out
}

func (c *procCollector) collect() []agentapi.Event {
	if runtime.GOOS != "linux" {
		return nil
	}
	dirs, err := filepath.Glob(procPath("[0-9]*"))
	if err != nil || len(dirs) == 0 {
		c.a.once("suspicious", "/proc unreadable; skipping process checks")
		return nil
	}
	if c.reported == nil {
		c.reported = map[string]bool{}
	}
	self := strconv.Itoa(os.Getpid())
	alive := map[string]bool{}
	var users map[uint32]string
	var out []agentapi.Event
	for _, dir := range dirs {
		pid := filepath.Base(dir)
		if pid == self {
			continue
		}
		alive[pid] = true
		p, ok := readProc(dir)
		if !ok {
			continue
		}
		for _, reason := range classifyProcess(p) {
			key := pid + "\x00" + reason
			if c.reported[key] {
				continue
			}
			c.reported[key] = true
			if users == nil {
				users = passwdNames()
			}
			exe := strings.TrimSuffix(p.exe, " (deleted)")
			if exe == "" {
				exe = p.name
			}
			out = append(out, agentapi.Event{Kind: "suspicious_process", Actor: procUser(dir, users), Detail: reason, Count: 1,
				Target: clipRunes(redact("pid "+pid+" "+exe+" "+strings.Join(p.cmdline, " ")), 300)})
		}
	}
	for key := range c.reported {
		pid, _, _ := strings.Cut(key, "\x00")
		if !alive[pid] {
			delete(c.reported, key)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Target < out[j].Target })
	return out
}

// readProc reads what the classifier needs of one process. Without root
// the executable and descriptors of other users' processes stay unknown.
func readProc(dir string) (procInfo, bool) {
	comm, err := os.ReadFile(filepath.Join(dir, "comm"))
	if err != nil {
		return procInfo{}, false // exited
	}
	p := procInfo{name: strings.TrimSpace(string(comm))}
	p.exe, _ = os.Readlink(filepath.Join(dir, "exe"))
	if raw, err := os.ReadFile(filepath.Join(dir, "cmdline")); err == nil {
		p.cmdline = strings.Fields(strings.ReplaceAll(string(raw), "\x00", " "))
	}
	if exe, deleted := strings.CutSuffix(p.exe, " (deleted)"); deleted && strings.HasPrefix(exe, "/") {
		// Look in the process's own root: it may run in a container.
		if _, err := os.Stat(filepath.Join(dir, "root", exe)); err == nil {
			p.exeOnDisk = true
		} else if !os.IsNotExist(err) {
			p.exeOnDisk = true // unknown: do not guess
		}
	}
	sockets := false
	for _, fd := range []string{"0", "1"} {
		if l, err := os.Readlink(filepath.Join(dir, "fd", fd)); err == nil {
			p.fds = append(p.fds, l)
			sockets = sockets || strings.HasPrefix(l, "socket:[")
		}
	}
	base := strings.ToLower(path.Base(strings.TrimSuffix(p.exe, " (deleted)")))
	if sockets && (shellNames[strings.ToLower(p.name)] || shellNames[base]) {
		p.netSockets = netSocketInodes(dir)
	}
	return p, true
}

// netSocketInodes are the inodes of the TCP and UDP sockets in a process's
// network namespace; nil when they cannot be read.
func netSocketInodes(dir string) map[string]bool {
	out := map[string]bool{}
	read := 0
	for _, f := range []string{"tcp", "tcp6", "udp", "udp6"} {
		b, err := os.ReadFile(filepath.Join(dir, "net", f))
		if err != nil {
			continue
		}
		read++
		for _, line := range strings.Split(string(b), "\n")[1:] {
			if fs := strings.Fields(line); len(fs) >= 10 {
				out[fs[9]] = true
			}
		}
	}
	if read == 0 {
		return nil
	}
	return out
}

// procUser is the name of a process's real user, from its status.
func procUser(dir string, users map[uint32]string) string {
	b, err := os.ReadFile(filepath.Join(dir, "status"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "Uid:"); ok {
			f := strings.Fields(rest)
			if len(f) == 0 {
				return ""
			}
			uid, err := strconv.ParseUint(f[0], 10, 32)
			if err != nil {
				return f[0]
			}
			if n := users[uint32(uid)]; n != "" {
				return n
			}
			return f[0]
		}
	}
	return ""
}
