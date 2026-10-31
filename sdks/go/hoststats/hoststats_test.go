package hoststats

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParsers(t *testing.T) {
	p, ok := ParsePressure(fixture(t, "linux/proc/pressure/memory"))
	if !ok || p.SomeAvg10 != 4.2 || p.SomeAvg60 != 2.1 || !p.HasFull || p.FullAvg10 != 1.5 {
		t.Errorf("pressure: %+v %v", p, ok)
	}
	if _, ok := ParsePressure([]byte("garbage\n")); ok {
		t.Error("garbage pressure parsed")
	}

	vm, ok := ParseVMStat(fixture(t, "linux/proc/vmstat"))
	want := VMStat{PgFault: 50000000, PgMajFault: 20000, PswpIn: 1000, PswpOut: 2000, PgScan: 69000, PgSteal: 35000, OOMKill: 3, HasOOMKill: true}
	if !ok || vm != want {
		t.Errorf("vmstat: %+v", vm)
	}

	mi, ok := ParseMemInfo(fixture(t, "linux/proc/meminfo"))
	if !ok || mi.Dirty != 10240 || mi.Writeback != 2048 || mi.Slab != 819200 || mi.Shmem != 524288 || mi.Committed != 20971520 || mi.CommitLimit != 12345678 {
		t.Errorf("meminfo: %+v", mi)
	}

	open, max, ok := ParseFileNr(fixture(t, "linux/proc/sys/fs/file-nr"))
	if !ok || open != 9824 || max != 9223372036854775807 {
		t.Errorf("file-nr: %d %d %v", open, max, ok)
	}

	ss, ok := ParseSockStat(fixture(t, "linux/proc/net/sockstat"))
	if !ok || ss != (SockStat{Used: 812, TCPInUse: 120, TCPOrphan: 3, TCPTimeWait: 455}) {
		t.Errorf("sockstat: %+v", ss)
	}

	u, ok := ParseSNMP(fixture(t, "linux/proc/net/snmp"))
	if !ok || u != (UDPErrors{InErrors: 40, BufErrors: 30}) {
		t.Errorf("snmp: %+v", u)
	}
	u6, ok := ParseSNMP6(fixture(t, "linux/proc/net/snmp6"))
	if !ok || u6 != (UDPErrors{InErrors: 2, BufErrors: 1}) {
		t.Errorf("snmp6: %+v", u6)
	}

	if n := CountTCPState(fixture(t, "linux/proc/net/tcp"), "08"); n != 2 {
		t.Errorf("tcp close_wait: %d", n)
	}
	if n := CountTCPState(fixture(t, "linux/proc/net/tcp6"), "08"); n != 1 {
		t.Errorf("tcp6 close_wait: %d", n)
	}

	if mhz, ok := ParseCPUInfoMHz(fixture(t, "linux/proc/cpuinfo")); !ok || mhz != 2500 {
		t.Errorf("cpuinfo: %v", mhz)
	}
	if cur, max, ok := ParseDPMClock(fixture(t, "linux/sys/class/drm/card1/device/pp_dpm_sclk")); !ok || cur != 1800 || max != 2500 {
		t.Errorf("dpm: %v %v", cur, max)
	}

	f, m, ok := ParseCgroupMemStat([]byte("anon 123\npgfault 900\npgmajfault 12\n"))
	if !ok || f != 900 || m != 12 {
		t.Errorf("memory.stat v2: %d %d", f, m)
	}
	f, m, ok = ParseCgroupMemStat([]byte("cache 1\npgfault 5\ntotal_pgfault 800\ntotal_pgmajfault 7\n"))
	if !ok || f != 800 || m != 7 {
		t.Errorf("memory.stat v1: %d %d", f, m)
	}
	h, mx, ok := ParseCgroupMemEvents([]byte("low 0\nhigh 14\nmax 3\noom 1\noom_kill 1\n"))
	if !ok || h != 14 || mx != 3 {
		t.Errorf("memory.events: %d %d", h, mx)
	}
}

func TestNvidiaCSV(t *testing.T) {
	gpus := ParseNvidiaGPUs(fixture(t, "nvidia-gpus.csv"))
	if len(gpus) != 2 {
		t.Fatalf("gpus: %+v", gpus)
	}
	g := gpus[0]
	if g.Index != 0 || g.Name != "NVIDIA GeForce RTX 4090" || g.Vendor != "nvidia" || *g.UtilPercent != 97 || *g.MemUsedMB != 20480 ||
		*g.TempC != 74 || *g.PowerW != 412.35 || *g.PowerLimitW != 450 || *g.FanPercent != 68 || *g.ClockMHz != 2730 || *g.MaxClockMHz != 3105 {
		t.Errorf("gpu 0: %+v", g)
	}
	if pct, ok := g.MemPercent(); !ok || pct < 83 || pct > 84 {
		t.Errorf("mem percent: %v", pct)
	}
	if gpus[1].FanPercent != nil || gpus[1].UUID != "GPU-aaaabbbb-cccc-dddd-eeee-ffff00001111" {
		t.Errorf("gpu 1: %+v", gpus[1])
	}
	if got := ParseNvidiaGPUs([]byte("0, GPU-x, Weird, Name, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10\n")); len(got) != 1 || got[0].Name != "Weird, Name" || *got[0].UtilPercent != 1 {
		t.Errorf("comma in name: %+v", got)
	}
	apps := ParseNvidiaApps(fixture(t, "nvidia-apps.csv"))
	if len(apps) != 2 || apps[0].PID != 4242 || apps[0].Name != "/usr/bin/python3" || apps[0].UsedMemMB != 18000 || apps[1].Name != "ollama" {
		t.Errorf("apps: %+v", apps)
	}
}

// copyTree copies the fixture tree so a test can change counters between
// samples
func copyTree(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func replace(t *testing.T, path, old, new string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), old) {
		t.Fatalf("%s has no %q", path, old)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(b), old, new, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCollectorHost(t *testing.T) {
	root := copyTree(t, filepath.Join("testdata", "linux"))
	c := New(Options{FS: FixtureFS(root), GPU: true})
	c.gpu.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if strings.HasPrefix(args[0], "--query-gpu=") {
			return fixture(t, "nvidia-gpus.csv"), nil
		}
		return fixture(t, "nvidia-apps.csv"), nil
	}
	c.gpu.smi, c.gpu.smiFound = "nvidia-smi", time.Now()
	c.Prime()

	proc := filepath.Join(root, "proc")
	replace(t, filepath.Join(proc, "vmstat"), "pgfault 50000000", "pgfault 50030000")
	replace(t, filepath.Join(proc, "vmstat"), "pgmajfault 20000", "pgmajfault 20150")
	replace(t, filepath.Join(proc, "vmstat"), "pswpin 1000", "pswpin 1300")
	replace(t, filepath.Join(proc, "vmstat"), "pgscan_kswapd 60000", "pgscan_kswapd 61500")
	replace(t, filepath.Join(proc, "vmstat"), "oom_kill 3", "oom_kill 4")
	replace(t, filepath.Join(proc, "net", "snmp"), "Udp: 90000 12 40 88000 25 5", "Udp: 90000 12 52 88000 27 5")
	replace(t, filepath.Join(root, "sys", "devices", "system", "cpu", "cpu1", "thermal_throttle", "core_throttle_count"), "5", "9")

	v := map[string]any{}
	c.Sample(v, 15)
	want := map[string]any{
		"psi_cpu_some_avg10": 12.5, "psi_cpu_some_avg60": 8.25, "psi_mem_some_avg10": 4.2, "psi_mem_some_avg60": 2.1, "psi_mem_full_avg10": 1.5,
		"psi_io_some_avg10": 30.0, "psi_io_some_avg60": 22.4, "psi_io_full_avg10": 18.75,
		"minor_faults_ps": 1990.0, "major_faults_ps": 10.0, "swap_in_ps": 20.0, "swap_out_ps": 0.0, "pages_scanned_ps": 100.0, "pages_stolen_ps": 0.0,
		"host_oom_kills": int64(1),
		"mem_dirty_mb":   10.0, "mem_writeback_mb": 2.0, "mem_slab_mb": 800.0, "mem_shmem_mb": 512.0, "mem_committed_mb": 20480.0, "mem_commit_limit_mb": 12056.3,
		"files_open": int64(9824), "sockets_used": int64(812), "tcp_time_wait": int64(455), "tcp_orphans": int64(3), "tcp_close_wait": int64(3),
		"conntrack_count": int64(1234), "conntrack_max": int64(262144), "udp_in_errors": int64(12), "udp_buf_errors": int64(2),
		"cpu_temp_c": 63.0, "cpu_mhz": 2800.0, "cpu_max_mhz": 4500.0, "cpu_throttle_events": int64(4),
		"gpu_count": int64(3), "gpu_util_percent": 97.0, "gpu_mem_percent": 83.4, "gpu_temp_c": 88.0,
	}
	for k, w := range want {
		if v[k] != w {
			t.Errorf("%s = %v (%T), want %v", k, v[k], v[k], w)
		}
	}
	if _, ok := v["files_max"]; ok {
		t.Error("unlimited file-max was stored")
	}

	gpus := c.GPUs()
	if len(gpus) != 3 {
		t.Fatalf("gpus: %+v", gpus)
	}
	amd := gpus[2]
	if amd.Vendor != "amd" || amd.Index != 1 || *amd.UtilPercent != 87 || *amd.MemUtilPct != 40 || *amd.MemUsedMB != 4096 || *amd.TempC != 88 ||
		*amd.PowerW != 210 || *amd.PowerLimitW != 255 || *amd.ClockMHz != 1800 || *amd.MaxClockMHz != 2500 || *amd.FanPercent < 50 || *amd.FanPercent > 51 {
		t.Errorf("amd: %+v", amd)
	}
	procs := c.GPUProcesses(context.Background())
	if len(procs) != 2 || procs[0].GPUIndex != 0 {
		t.Errorf("processes: %+v", procs)
	}

	sensors := map[string]Sensor{}
	for _, s := range c.Sensors() {
		sensors[s.Name] = s
	}
	if s := sensors["coretemp Package id 0"]; s.Value != 61 || s.Unit != "C" || s.Kind != "temperature" {
		t.Errorf("coretemp: %+v (%+v)", s, c.Sensors())
	}
	if s := sensors["nct6775 fan1"]; s.Value != 1200 || s.Unit != "rpm" {
		t.Errorf("fan: %+v", s)
	}
	if s := sensors["acpitz"]; s.Value != 70 {
		t.Errorf("acpitz: %+v", s)
	}
	if len(sensors) != 6 {
		t.Errorf("sensors: %+v", c.Sensors())
	}
}

func TestCollectorContainer(t *testing.T) {
	root := copyTree(t, filepath.Join("testdata", "linux"))
	c := New(Options{FS: FixtureFS(root), Container: true, CgroupDir: filepath.Join(root, "cgroup")})
	c.Prime()
	v := map[string]any{}
	c.Sample(v, 15)
	if v["psi_cpu_some_avg10"] != 55.1 || v["psi_mem_some_avg10"] != 0.0 || v["psi_io_full_avg10"] != 0.9 {
		t.Errorf("cgroup pressure: %v %v %v", v["psi_cpu_some_avg10"], v["psi_mem_some_avg10"], v["psi_io_full_avg10"])
	}
	if _, ok := v["gpu_count"]; ok {
		t.Error("GPU read with GPU off")
	}

	c = New(Options{FS: FixtureFS(root), Container: true})
	c.Prime()
	v = map[string]any{}
	c.Sample(v, 15)
	if _, ok := v["psi_cpu_some_avg10"]; ok {
		t.Error("a container without a cgroup v2 directory read the host's pressure")
	}
}

func TestCollectorBusyHostSkipsTCPWalk(t *testing.T) {
	root := copyTree(t, filepath.Join("testdata", "linux"))
	c := New(Options{FS: FixtureFS(root), TCPStateLimit: 100})
	c.Prime()
	v := map[string]any{}
	c.Sample(v, 15)
	if _, ok := v["tcp_close_wait"]; ok {
		t.Error("CLOSE_WAIT counted past the limit")
	}
	if v["sockets_used"] != int64(812) {
		t.Errorf("sockets: %v", v["sockets_used"])
	}
}

func TestGPUBackoff(t *testing.T) {
	g := newGPUReader(FS{})
	calls := 0
	g.run = func(context.Context, string, ...string) ([]byte, error) {
		calls++
		return nil, errors.New("driver gone")
	}
	now := time.Now()
	g.smi, g.smiFound = "nvidia-smi", now
	g.read(now)
	g.read(now.Add(15 * time.Second))
	if calls != 1 {
		t.Errorf("nvidia-smi ran %d times during the back-off", calls)
	}
	g.read(now.Add(2 * time.Minute))
	if calls != 2 {
		t.Errorf("nvidia-smi was not retried: %d", calls)
	}
}

func TestOffLinuxReadsNothing(t *testing.T) {
	c := New(Options{FS: FS{Proc: "testdata/linux/proc", Sys: "testdata/linux/sys"}})
	c.Prime()
	v := map[string]any{}
	c.Sample(v, 15)
	if len(v) != 0 {
		t.Errorf("read off Linux: %v", v)
	}
}

func TestFindNvidiaSMIUnderHostRoot(t *testing.T) {
	if p, _ := findNvidiaSMI(""); p != "" {
		t.Skip("this machine has its own nvidia-smi")
	}
	root := t.TempDir()
	if p, _ := findNvidiaSMI(root); p != "" {
		t.Fatalf("found %q under an empty root", p)
	}
	smi := filepath.Join(root, "usr", "bin", "nvidia-smi")
	if err := os.MkdirAll(filepath.Dir(smi), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(smi, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	name, args := findNvidiaSMI(root)
	if name != "chroot" || len(args) != 2 || args[0] != root || args[1] != "/usr/bin/nvidia-smi" {
		t.Fatalf("got %q %q", name, args)
	}
	g := &gpuReader{smi: name, smiArgs: args}
	if got := g.args("-L"); len(got) != 3 || got[2] != "-L" {
		t.Fatalf("args %q", got)
	}
}
