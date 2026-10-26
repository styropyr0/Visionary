package main

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/styropyr0/visionary/internal/agentapi"
)

// --- SSH authorized keys ---

// sshKeyCollector compares the keys in every authorized_keys file with the
// previous report's. The first reading is the baseline and reports nothing.
// Only the key type, fingerprint and comment leave the host, never the key.
type sshKeyCollector struct {
	a    *agent
	prev map[string]map[string]sshKey // by file, by fingerprint
}

type sshKey struct {
	typ, fp, comment string
}

const sshKeyFileMax = 1 << 20

func (c *sshKeyCollector) collect() []agentapi.Event {
	if runtime.GOOS != "linux" {
		return nil
	}
	cur := map[string]map[string]sshKey{}
	owners := map[string]string{}
	names := passwdNames()
	for _, path := range authorizedKeyFiles() {
		f, err := os.Open(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			// Unreadable (the agent runs unprivileged): keep what was there.
			c.a.once("ssh-keys", "an authorized_keys file is unreadable; skipping it", "path", path, "err", err)
			if old, ok := c.prev[path]; ok {
				cur[path] = old
			}
			continue
		}
		st, err := f.Stat()
		if err != nil || !st.Mode().IsRegular() || st.Size() > sshKeyFileMax {
			f.Close()
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(f, sshKeyFileMax))
		f.Close()
		keys := map[string]sshKey{}
		for _, line := range strings.Split(string(b), "\n") {
			if k, ok := parseAuthorizedKey(line); ok {
				keys[k.fp] = k
			}
		}
		cur[path] = keys
		uid, _, _ := statOwner(st)
		owners[path] = names[uid]
		if owners[path] == "" {
			owners[path] = strconv.FormatUint(uint64(uid), 10)
		}
	}
	if c.prev == nil {
		c.prev = cur
		return nil
	}
	var out []agentapi.Event
	ev := func(kind, path string, k sshKey) {
		owner := owners[path]
		if owner == "" {
			owner = homeOwner(path)
		}
		out = append(out, agentapi.Event{Kind: kind, Actor: owner, Detail: path, Count: 1,
			Target: clipRunes(strings.TrimSpace(k.typ+" "+k.fp+" "+k.comment), 300)})
	}
	for path, keys := range cur {
		for fp, k := range keys {
			if _, ok := c.prev[path][fp]; !ok {
				ev("ssh_key_added", path, k)
			}
		}
	}
	for path, keys := range c.prev {
		for fp, k := range keys {
			if _, ok := cur[path][fp]; !ok {
				ev("ssh_key_removed", path, k)
			}
		}
	}
	c.prev = cur
	sort.Slice(out, func(i, j int) bool { return out[i].Detail+out[i].Target < out[j].Detail+out[j].Target })
	return out
}

// homeOwner guesses the account of a deleted key file from its path.
func homeOwner(path string) string {
	if strings.HasPrefix(path, "/root/") {
		return "root"
	}
	if rest, ok := strings.CutPrefix(path, "/home/"); ok {
		user, _, _ := strings.Cut(rest, "/")
		return user
	}
	return ""
}

// authorizedKeyFiles are root's and every user's authorized_keys and
// authorized_keys2: the homes under /home, and those in /etc/passwd of root
// and of accounts with a UID of 1000 or more.
func authorizedKeyFiles() []string {
	homes := map[string]bool{"/root": true}
	if dirs, err := filepath.Glob("/home/*"); err == nil {
		for _, d := range dirs {
			homes[d] = true
		}
	}
	if b, err := os.ReadFile("/etc/passwd"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Split(line, ":")
			if len(f) < 6 || !strings.HasPrefix(f[5], "/") || f[5] == "/" {
				continue
			}
			if uid, err := strconv.ParseUint(f[2], 10, 32); err == nil && (uid == 0 || uid >= 1000) && uid != 65534 {
				homes[filepath.Clean(f[5])] = true
			}
		}
	}
	var out []string
	for h := range homes {
		for _, name := range []string{"authorized_keys", "authorized_keys2"} {
			out = append(out, h+"/.ssh/"+name)
		}
	}
	sort.Strings(out)
	return out
}

// parseAuthorizedKey reads one authorized_keys line: optional options
// (`command="..."`, `from="..."`, `no-pty`, ...), the key type, the base64
// key and an optional comment. Comments and blank lines are not keys.
func parseAuthorizedKey(line string) (sshKey, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return sshKey{}, false
	}
	if !isSSHKeyType(firstField(line)) {
		line = strings.TrimLeft(skipKeyOptions(line), " \t")
	}
	f := strings.Fields(line)
	if len(f) < 2 || !isSSHKeyType(f[0]) {
		return sshKey{}, false
	}
	blob, err := base64.StdEncoding.DecodeString(f[1])
	if err != nil || len(blob) == 0 {
		return sshKey{}, false
	}
	k := sshKey{typ: f[0], fp: sshFingerprint(blob)}
	if len(f) > 2 {
		k.comment = strings.Join(f[2:], " ")
	}
	return k, true
}

// sshFingerprint is the fingerprint ssh-keygen -l prints: SHA256: and the
// unpadded base64 of the SHA-256 of the key blob.
func sshFingerprint(blob []byte) string {
	sum := sha256.Sum256(blob)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

func isSSHKeyType(s string) bool {
	switch {
	case s == "ssh-rsa", s == "ssh-dss", s == "ssh-ed25519", s == "ssh-ed448":
		return true
	case strings.HasPrefix(s, "ecdsa-sha2-"), strings.HasPrefix(s, "sk-ssh-"), strings.HasPrefix(s, "sk-ecdsa-sha2-"):
		return true
	case strings.HasSuffix(s, "-cert-v01@openssh.com"):
		return true
	}
	return false
}

func firstField(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return ""
}

// skipKeyOptions drops the options before the key type: everything up to
// the first space outside double quotes (a backslash escapes a quote).
func skipKeyOptions(s string) string {
	quoted := false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\\' && quoted && i+1 < len(s):
			i++
		case c == '"':
			quoted = !quoted
		case (c == ' ' || c == '\t') && !quoted:
			return s[i:]
		}
	}
	return ""
}
