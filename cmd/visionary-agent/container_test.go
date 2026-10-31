package main

import (
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func withHostRoot(t *testing.T, root string) {
	t.Helper()
	old := hostRoot
	hostRoot = root
	t.Cleanup(func() { hostRoot = old })
}

func TestRootFrom(t *testing.T) {
	for in, want := range map[string]string{"": "/", "  ": "/", "/": "/", "/host/": filepath.Clean("/host")} {
		if got := rootFrom(in); got != want {
			t.Errorf("rootFrom(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHostPathOnHost(t *testing.T) {
	withHostRoot(t, "/")
	if got := hostPath("/etc/passwd"); got != "/etc/passwd" {
		t.Errorf("hostPath = %q", got)
	}
	if got := fromHost("/etc/passwd"); got != "/etc/passwd" {
		t.Errorf("fromHost = %q", got)
	}
	name, args := hostArgv("systemctl", []string{"list-units"})
	if name != "systemctl" || !reflect.DeepEqual(args, []string{"list-units"}) {
		t.Errorf("hostArgv = %q %q", name, args)
	}
	t.Setenv("HOST_PROC", "")
	if got := procPath("1", "net"); got != filepath.Join("/proc", "1", "net") {
		t.Errorf("procPath = %q", got)
	}
	n, _ := os.Hostname()
	if got := machineName(); got != n {
		t.Errorf("machineName = %q, want %q", got, n)
	}
}

func TestHostPathInContainer(t *testing.T) {
	root := t.TempDir()
	withHostRoot(t, root)
	p := hostPath("/etc/passwd")
	if p != filepath.Join(root, "etc", "passwd") {
		t.Errorf("hostPath = %q", p)
	}
	if got := fromHost(p); got != "/etc/passwd" {
		t.Errorf("fromHost = %q", got)
	}
	if got := fromHost(root); got != "/" {
		t.Errorf("fromHost(root) = %q", got)
	}
	name, args := hostArgv("journalctl", []string{"-f"})
	if name != "chroot" || !reflect.DeepEqual(args, []string{root, "journalctl", "-f"}) {
		t.Errorf("hostArgv = %q %q", name, args)
	}
	t.Setenv("HOST_PROC", filepath.Join(root, "proc"))
	if got := procPath("1"); got != filepath.Join(root, "proc", "1") {
		t.Errorf("procPath = %q", got)
	}
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc", "hostname"), []byte("db-1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := machineName(); got != "db-1" {
		t.Errorf("machineName = %q", got)
	}
}

func TestSocketCandidates(t *testing.T) {
	t.Setenv("DOCKER_HOST", "")
	withHostRoot(t, "/")
	if got := socketCandidates(""); !reflect.DeepEqual(got, dockerSockets) {
		t.Errorf("on the host: %q", got)
	}
	if got := socketCandidates("/x.sock"); !reflect.DeepEqual(got, []string{"/x.sock"}) {
		t.Errorf("configured: %q", got)
	}
	withHostRoot(t, "/host")
	got := socketCandidates("")
	if !reflect.DeepEqual(got[:len(dockerSockets)], dockerSockets) || got[len(dockerSockets)] != hostPath("/run/docker.sock") {
		t.Errorf("in a container: %q", got)
	}
}

func TestDockerSocketUnderFakeRoot(t *testing.T) {
	t.Setenv("DOCKER_HOST", "")
	root, err := os.MkdirTemp("", "vroot")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	withHostRoot(t, root)
	sock := hostPath("/run/docker.sock")
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Skip("no unix sockets here:", err)
	}
	defer l.Close()
	if fi, err := os.Stat(sock); err != nil || fi.Mode()&os.ModeSocket == 0 {
		t.Skip("this OS does not report unix sockets as such")
	}
	if _, err := os.Stat("/var/run/docker.sock"); err == nil {
		t.Skip("a real docker socket is mounted")
	}
	d := newDockerCollector(&agent{log: slog.New(slog.NewTextHandler(io.Discard, nil))}, "")
	if got := d.socket(); got != sock {
		t.Errorf("socket = %q, want %q", got, sock)
	}
}
