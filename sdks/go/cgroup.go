package visionary

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// cgroupReader reads the CPU and memory limits and use of the process's own
// cgroup (Linux, v2 or v1): in a container these are the numbers that
// matter, since the host columns describe the whole machine underneath. The
// container_* columns are filled only in a container, or when the cgroup sets
// a CPU or memory limit (a systemd unit with MemoryMax=, say).
type cgroupReader struct {
	v2       bool
	cpuDir   string // v2: the cgroup; v1: the cpu controller's directory
	cpuAcct  string // v1: the cpuacct controller's directory
	memDir   string
	inCtr    bool
	last     time.Time
	usage    uint64 // CPU time, ns
	periods  uint64
	throttle uint64
	ooms     uint64
	oomsOK   bool // the kernel reports OOM kills (v1 only on newer kernels)
	primed   bool
}

// newCgroupReader finds the process's cgroup; nil when there is none to read.
func newCgroupReader() *cgroupReader {
	if !isLinux() {
		return nil
	}
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return nil
	}
	c := &cgroupReader{inCtr: inContainer(string(b))}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		f := strings.SplitN(line, ":", 3)
		if len(f) != 3 {
			continue
		}
		ctrls, path := f[1], f[2]
		switch {
		case f[0] == "0" && ctrls == "":
			if dir := cgroupDir("/sys/fs/cgroup", path); exists(filepath.Join(dir, "cpu.stat")) {
				c.v2, c.cpuDir, c.memDir = true, dir, dir
			}
		case hasController(ctrls, "memory"):
			c.memDir = cgroupDir("/sys/fs/cgroup/memory", path)
		case hasController(ctrls, "cpu") || hasController(ctrls, "cpuacct"):
			dir := cgroupDir("/sys/fs/cgroup/"+ctrls, path)
			if hasController(ctrls, "cpu") {
				c.cpuDir = dir
			}
			if hasController(ctrls, "cpuacct") {
				c.cpuAcct = dir
			}
		}
		if c.v2 {
			break
		}
	}
	if c.cpuDir == "" && c.memDir == "" {
		return nil
	}
	return c
}

// cgroupDir is the cgroup's directory under a controller's mount. Inside a
// container's cgroup namespace the path is "/", and with a private mount the
// host path does not exist below it, so the mount itself is the cgroup.
func cgroupDir(mount, path string) string {
	if dir := filepath.Join(mount, path); exists(dir) {
		return dir
	}
	return mount
}

func hasController(list, name string) bool {
	for _, c := range strings.Split(list, ",") {
		if c == name {
			return true
		}
	}
	return false
}

// containerEnv are variables that container platforms set in every container:
// Kubernetes, Railway, Render, Heroku, Cloud Run, ECS/Fargate and Azure
// Container Apps.
var containerEnv = []string{"KUBERNETES_SERVICE_HOST", "RAILWAY_SERVICE_ID", "RENDER_SERVICE_ID", "DYNO", "K_SERVICE", "ECS_CONTAINER_METADATA_URI_V4", "CONTAINER_APP_NAME"}

// inContainer tells a container from a process on the host by the
// runtimes' marker files, the platforms' variables and the cgroup path.
func inContainer(cgroups string) bool {
	if exists("/.dockerenv") || exists("/run/.containerenv") {
		return true
	}
	for _, k := range containerEnv {
		if os.Getenv(k) != "" {
			return true
		}
	}
	for _, k := range []string{"docker", "kubepods", "containerd", "libpod", "lxc"} {
		if strings.Contains(cgroups, k) {
			return true
		}
	}
	return false
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func readUint(p string) (uint64, bool) {
	b, err := os.ReadFile(p)
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	return n, err == nil
}

// readKV reads "key value" lines (cpu.stat, memory.stat, memory.events).
func readKV(p string) map[string]uint64 {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	out := map[string]uint64{}
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) == 2 {
			if n, err := strconv.ParseUint(f[1], 10, 64); err == nil {
				out[f[0]] = n
			}
		}
	}
	return out
}

// limits is the CPU limit in cores and the memory limit in bytes, 0 when
// unlimited.
func (c *cgroupReader) limits() (cpus float64, mem uint64) {
	if c.v2 {
		if b, err := os.ReadFile(filepath.Join(c.cpuDir, "cpu.max")); err == nil {
			if f := strings.Fields(string(b)); len(f) == 2 && f[0] != "max" {
				q, e1 := strconv.ParseFloat(f[0], 64)
				p, e2 := strconv.ParseFloat(f[1], 64)
				if e1 == nil && e2 == nil && p > 0 {
					cpus = q / p
				}
			}
		}
		mem, _ = readUint(filepath.Join(c.memDir, "memory.max")) // "max" fails to parse: 0
		return cpus, mem
	}
	if c.cpuDir != "" {
		b, err := os.ReadFile(filepath.Join(c.cpuDir, "cpu.cfs_quota_us"))
		q, e1 := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
		p, ok := readUint(filepath.Join(c.cpuDir, "cpu.cfs_period_us"))
		if err == nil && e1 == nil && q > 0 && ok && p > 0 {
			cpus = q / float64(p)
		}
	}
	if c.memDir != "" {
		// v1 writes a page-rounded maximum int64 for no limit.
		if m, ok := readUint(filepath.Join(c.memDir, "memory.limit_in_bytes")); ok && m < 1<<62 {
			mem = m
		}
	}
	return cpus, mem
}

// counters reads the cumulative CPU time (ns), CFS periods, throttled
// periods and OOM kills, and whether OOM kills are reported at all.
func (c *cgroupReader) counters() (usage, periods, throttled, ooms uint64, oomsOK bool) {
	if c.v2 {
		st := readKV(filepath.Join(c.cpuDir, "cpu.stat"))
		ooms, oomsOK = readKV(filepath.Join(c.memDir, "memory.events"))["oom_kill"]
		return st["usage_usec"] * 1000, st["nr_periods"], st["nr_throttled"], ooms, oomsOK
	}
	if c.cpuAcct != "" {
		usage, _ = readUint(filepath.Join(c.cpuAcct, "cpuacct.usage"))
	}
	if c.cpuDir != "" {
		st := readKV(filepath.Join(c.cpuDir, "cpu.stat"))
		periods, throttled = st["nr_periods"], st["nr_throttled"]
	}
	if c.memDir != "" {
		ooms, oomsOK = readKV(filepath.Join(c.memDir, "memory.oom_control"))["oom_kill"]
	}
	return usage, periods, throttled, ooms, oomsOK
}

// memUsed is memory in use without the inactive page cache, as docker stats
// counts it.
func (c *cgroupReader) memUsed() (uint64, bool) {
	file, statKey := "memory.current", "inactive_file"
	if !c.v2 {
		file, statKey = "memory.usage_in_bytes", "total_inactive_file"
	}
	used, ok := readUint(filepath.Join(c.memDir, file))
	if !ok {
		return 0, false
	}
	if inactive, ok := readKV(filepath.Join(c.memDir, "memory.stat"))[statKey]; ok && inactive < used {
		used -= inactive
	}
	return used, true
}

// prime takes the baseline the first sample's deltas start from.
func (c *cgroupReader) prime() {
	c.usage, c.periods, c.throttle, c.ooms, c.oomsOK = c.counters()
	c.last, c.primed = time.Now(), true
}

// sample fills the container_* columns.
func (c *cgroupReader) sample(v map[string]any) {
	cpus, memLimit := c.limits()
	if !c.inCtr && cpus == 0 && memLimit == 0 {
		return
	}
	now := time.Now()
	usage, periods, throttled, ooms, oomsOK := c.counters()
	if secs := now.Sub(c.last).Seconds(); c.primed && secs > 0 && usage >= c.usage {
		v["container_cpu_cores"] = round(float64(usage-c.usage)/1e9/secs, 3)
		if periods > c.periods && throttled >= c.throttle {
			v["container_cpu_throttled_percent"] = round(100*float64(throttled-c.throttle)/float64(periods-c.periods), 1)
		} else if cpus > 0 {
			v["container_cpu_throttled_percent"] = 0.0
		}
		if oomsOK && c.oomsOK && ooms >= c.ooms {
			v["container_oom_kills"] = int64(ooms - c.ooms)
		}
	}
	c.usage, c.periods, c.throttle, c.ooms, c.oomsOK, c.last, c.primed = usage, periods, throttled, ooms, oomsOK, now, true
	if cpus > 0 {
		v["container_cpu_limit"] = round(cpus, 2)
	}
	if used, ok := c.memUsed(); ok {
		v["container_mem_used_mb"] = round(mb(used), 1)
	}
	if memLimit > 0 {
		v["container_mem_limit_mb"] = round(mb(memLimit), 1)
	}
}
