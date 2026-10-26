package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/styropyr0/visionary/internal/agentapi"
)

// --- system file integrity ---

// fileCollector watches the files an intruder changes to keep access:
// accounts, sudo, cron, PAM, sshd, systemd units, ld.so.preload. Each report
// it stats them and hashes only those whose size or times changed. The first
// reading is the baseline and reports nothing. Contents never leave the
// host; for /etc/passwd a new UID 0 account is named.
type fileCollector struct {
	a    *agent
	prev map[string]fileState // nil until the baseline
}

type fileState struct {
	size, mtime, ctime int64
	mode               os.FileMode
	uid, gid           uint32
	hash               string
	roots              []string // /etc/passwd: the UID 0 accounts
}

const (
	fileHashMax  = 1 << 20 // bigger files are compared by size and mtime
	fileWatchMax = 2000
)

// watchedFiles are the single files; watchedDirs the directories whose
// files are watched (spool dirs one level down too).
var (
	watchedFiles = []string{"/etc/passwd", "/etc/shadow", "/etc/group", "/etc/sudoers", "/etc/crontab",
		"/etc/ld.so.preload", "/etc/ssh/sshd_config", "/etc/rc.local"}
	watchedDirs = []string{"/etc/sudoers.d", "/etc/cron.d", "/etc/cron.hourly", "/etc/cron.daily",
		"/etc/cron.weekly", "/etc/cron.monthly", "/etc/pam.d"}
	watchedSpools = []string{"/var/spool/cron"}
)

func (c *fileCollector) collect() []agentapi.Event {
	if runtime.GOOS != "linux" {
		return nil
	}
	cur := make(map[string]fileState, len(c.prev))
	for _, p := range fileWatchList() {
		was, had := c.prev[p]
		if st, ok := c.stat(p, was, had); ok {
			cur[p] = st
		}
	}
	if c.prev == nil {
		c.prev = cur
		return nil
	}
	var out []agentapi.Event
	for p, now := range cur {
		was, had := c.prev[p]
		var d string
		if had {
			d = fileChangeDetail(&was, &now)
		} else {
			d = fileChangeDetail(nil, &now)
		}
		if d != "" {
			out = append(out, agentapi.Event{Kind: "file_changed", Target: p, Detail: d, Count: 1})
		}
	}
	for p, was := range c.prev {
		if _, ok := cur[p]; !ok {
			out = append(out, agentapi.Event{Kind: "file_changed", Target: p, Detail: fileChangeDetail(&was, nil), Count: 1})
		}
	}
	c.prev = cur
	sort.Slice(out, func(i, j int) bool { return out[i].Target < out[j].Target })
	return out
}

// stat reads one file's state, reusing the previous hash while its size and
// times are unchanged. A symlink is compared by its target. False when the
// file is not there (or is not a file).
func (c *fileCollector) stat(p string, was fileState, had bool) (fileState, bool) {
	fi, err := os.Lstat(p)
	if err != nil || !(fi.Mode().IsRegular() || fi.Mode()&os.ModeSymlink != 0) {
		return fileState{}, false
	}
	uid, gid, ctime := statOwner(fi)
	st := fileState{size: fi.Size(), mtime: fi.ModTime().UnixNano(), ctime: ctime, mode: fi.Mode(), uid: uid, gid: gid}
	if had && was.size == st.size && was.mtime == st.mtime && was.ctime == st.ctime && was.mode.Type() == st.mode.Type() {
		st.hash, st.roots = was.hash, was.roots
		return st, true
	}
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		target, _ := os.Readlink(p)
		st.hash = "link:" + target
	case fi.Size() > fileHashMax:
		st.hash = "size:" + strconv.FormatInt(st.size, 10) + "@" + strconv.FormatInt(st.mtime, 10)
	default:
		f, err := os.Open(p)
		if err != nil {
			// Unreadable without root (/etc/shadow): compare size and mtime.
			c.a.once("files-unreadable", "a watched file is unreadable; comparing its size and time only", "path", p, "err", err)
			st.hash = "size:" + strconv.FormatInt(st.size, 10) + "@" + strconv.FormatInt(st.mtime, 10)
			break
		}
		b, _ := io.ReadAll(io.LimitReader(f, fileHashMax))
		f.Close()
		sum := sha256.Sum256(b)
		st.hash = hex.EncodeToString(sum[:])
		if p == "/etc/passwd" {
			st.roots = uid0Accounts(string(b))
		}
	}
	return st, true
}

// fileWatchList is the watched files that exist now.
func fileWatchList() []string {
	out := append([]string(nil), watchedFiles...)
	add := func(dir string, depth int) {
		var walk func(string, int)
		walk = func(dir string, depth int) {
			entries, err := os.ReadDir(dir)
			if err != nil {
				return
			}
			for _, e := range entries {
				p := filepath.Join(dir, e.Name())
				if e.IsDir() {
					if depth > 0 {
						walk(p, depth-1)
					}
					continue
				}
				out = append(out, p)
			}
		}
		walk(dir, depth)
	}
	for _, d := range watchedDirs {
		add(d, 0)
	}
	for _, d := range watchedSpools {
		add(d, 1)
	}
	for _, pat := range []string{"/etc/systemd/system/*.service", "/etc/systemd/system/*.timer"} {
		m, _ := filepath.Glob(pat)
		out = append(out, m...)
	}
	if len(out) > fileWatchMax {
		out = out[:fileWatchMax]
	}
	return out
}

// fileChangeDetail says how a file changed: created, deleted, modified,
// permissions changed (mode or owner), or "" when it did not. A new UID 0
// account in /etc/passwd is named.
func fileChangeDetail(was, now *fileState) string {
	switch {
	case was == nil && now == nil:
		return ""
	case was == nil:
		return "created"
	case now == nil:
		return "deleted"
	}
	var parts []string
	if was.hash != now.hash {
		parts = append(parts, "modified")
	}
	if was.mode != now.mode || was.uid != now.uid || was.gid != now.gid {
		parts = append(parts, "permissions changed")
	}
	if len(parts) == 0 {
		return ""
	}
	d := strings.Join(parts, ", ")
	for _, name := range newNames(was.roots, now.roots) {
		d += "; new UID 0 account " + name
	}
	return d
}

// uid0Accounts are the accounts of a passwd file with UID 0.
func uid0Accounts(passwd string) []string {
	var out []string
	for _, line := range strings.Split(passwd, "\n") {
		f := strings.Split(strings.TrimSpace(line), ":")
		if len(f) < 3 || f[0] == "" || strings.HasPrefix(f[0], "#") {
			continue
		}
		if uid, err := strconv.ParseUint(f[2], 10, 32); err == nil && uid == 0 {
			out = append(out, f[0])
		}
	}
	return out
}

// newNames are the names in now that are not in was.
func newNames(was, now []string) []string {
	old := map[string]bool{}
	for _, n := range was {
		old[n] = true
	}
	var out []string
	for _, n := range now {
		if !old[n] {
			out = append(out, n)
			old[n] = true
		}
	}
	return out
}
