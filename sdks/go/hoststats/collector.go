package hoststats

import (
	"context"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// FS is where /proc and /sys are. HOST_PROC and HOST_SYS move them, as for
// gopsutil, so the agent can watch the host from a container
type FS struct {
	Proc, Sys string
	linux     bool
}

// DefaultFS is the running system's /proc and /sys, read only on Linux
func DefaultFS() FS {
	return FS{Proc: envOr("HOST_PROC", "/proc"), Sys: envOr("HOST_SYS", "/sys"), linux: runtime.GOOS == "linux"}
}

// FixtureFS reads a Linux tree copied under root on any OS, for tests
func FixtureFS(root string) FS {
	return FS{Proc: filepath.Join(root, "proc"), Sys: filepath.Join(root, "sys"), linux: true}
}

// Options choose what a Collector reads
type Options struct {
	FS FS
	// Container means the process runs in a container. Pressure then comes
	// from CgroupDir, the process's cgroup v2 directory, since /proc/pressure
	// is the whole machine's, and with no CgroupDir it is not read at all
	Container bool
	CgroupDir string
	GPU       bool
	// TCPStateLimit skips counting CLOSE_WAIT when more sockets than this are
	// in use, since that walks /proc/net/tcp. 0 means 20000
	TCPStateLimit uint64
}

// Sensor is one temperature or fan reading
type Sensor struct {
	Name  string  `json:"name"`
	Kind  string  `json:"kind"`
	Value float64 `json:"value"`
	Unit  string  `json:"unit"`
}

type sensorFile struct {
	name, kind, unit, path string
	scale                  float64
	cpu                    bool
}

// Collector keeps the counters the per-second columns are deltas of. It is
// not safe for concurrent use
type Collector struct {
	opt   Options
	fs    FS
	buf   []byte
	gpu   *gpuReader
	files []sensorFile

	freqFiles     []string
	maxMHz        float64
	throttleFiles []string

	vm        VMStat
	vmOK      bool
	udp       UDPErrors
	udpOK     bool
	throttle  uint64
	throttleN int

	sensors []Sensor
	gpus    []GPU
}

// New finds the sensors, clock and throttle files and GPUs once
func New(opt Options) *Collector {
	if opt.FS.Proc == "" {
		opt.FS = DefaultFS()
	}
	if opt.TCPStateLimit == 0 {
		opt.TCPStateLimit = 20000
	}
	c := &Collector{opt: opt, fs: opt.FS, buf: make([]byte, 64<<10)}
	if opt.GPU {
		c.gpu = newGPUReader(opt.FS)
	}
	if !c.fs.linux {
		return c
	}
	c.findSensors()
	cpus, _ := filepath.Glob(filepath.Join(c.fs.Sys, "devices", "system", "cpu", "cpu[0-9]*"))
	sort.Strings(cpus)
	for _, cpu := range cpus {
		if f := filepath.Join(cpu, "cpufreq", "scaling_cur_freq"); fileExists(f) {
			c.freqFiles = append(c.freqFiles, f)
		}
		if b, err := os.ReadFile(filepath.Join(cpu, "cpufreq", "cpuinfo_max_freq")); err == nil {
			if v, ok := ParseInt(b); ok {
				c.maxMHz = math.Max(c.maxMHz, float64(v)/1000)
			}
		}
		if f := filepath.Join(cpu, "thermal_throttle", "core_throttle_count"); fileExists(f) {
			c.throttleFiles = append(c.throttleFiles, f)
		}
	}
	return c
}

// cpuSensors are the hwmon drivers and thermal zone types that measure the
// CPU package or cores
var cpuSensors = []string{"coretemp", "k10temp", "zenpower", "cpu_thermal", "cpu-thermal", "x86_pkg_temp", "soc_thermal", "cpu"}

func isCPUSensor(name string) bool {
	name = strings.ToLower(name)
	for _, s := range cpuSensors {
		if name == s || strings.HasPrefix(name, s) {
			return true
		}
	}
	return false
}

func (c *Collector) findSensors() {
	mons, _ := filepath.Glob(filepath.Join(c.fs.Sys, "class", "hwmon", "hwmon*"))
	sort.Strings(mons)
	for _, h := range mons {
		chip := strings.TrimSpace(readString(filepath.Join(h, "name")))
		if chip == "" || chip == "amdgpu" || chip == "nouveau" {
			continue
		}
		inputs, _ := filepath.Glob(filepath.Join(h, "*_input"))
		sort.Strings(inputs)
		for _, in := range inputs {
			base := strings.TrimSuffix(filepath.Base(in), "_input")
			kind, unit, scale := "", "", 1.0
			switch {
			case strings.HasPrefix(base, "temp"):
				kind, unit, scale = "temperature", "C", 1000
			case strings.HasPrefix(base, "fan"):
				kind, unit = "fan", "rpm"
			default:
				continue
			}
			label := strings.TrimSpace(readString(filepath.Join(h, base+"_label")))
			if label == "" {
				label = base
			}
			c.files = append(c.files, sensorFile{name: chip + " " + label, kind: kind, unit: unit, path: in, scale: scale, cpu: kind == "temperature" && isCPUSensor(chip)})
		}
	}
	zones, _ := filepath.Glob(filepath.Join(c.fs.Sys, "class", "thermal", "thermal_zone*"))
	sort.Strings(zones)
	for _, z := range zones {
		typ := strings.TrimSpace(readString(filepath.Join(z, "type")))
		if typ == "" {
			continue
		}
		c.files = append(c.files, sensorFile{name: typ, kind: "temperature", unit: "C", path: filepath.Join(z, "temp"), scale: 1000, cpu: isCPUSensor(typ)})
	}
}

// Prime takes the baseline the first sample's rates start from
func (c *Collector) Prime() {
	if !c.fs.linux {
		return
	}
	c.vm, c.vmOK = c.readVMStat()
	c.udp, c.udpOK = c.readUDP()
	c.throttle, c.throttleN = c.readThrottle()
}

// Sample adds the columns it could read to v. secs is the time since the
// previous sample (or Prime), for rates
func (c *Collector) Sample(v map[string]any, secs float64) {
	if c.gpu != nil {
		c.gpus = c.gpu.read(time.Now())
		putGPUSummary(v, c.gpus)
	}
	if !c.fs.linux || secs <= 0 {
		return
	}
	c.samplePressure(v)
	c.sampleVM(v, secs)
	c.sampleLimits(v)
	c.sampleThermal(v)
}

// GPUs is the latest GPU reading
func (c *Collector) GPUs() []GPU { return c.gpus }

// Sensors is the latest temperature and fan reading
func (c *Collector) Sensors() []Sensor { return c.sensors }

// GPUProcesses asks nvidia-smi which processes hold GPU memory
func (c *Collector) GPUProcesses(ctx context.Context) []GPUProcess {
	if c.gpu == nil {
		return nil
	}
	return c.gpu.processes(ctx)
}

func (c *Collector) samplePressure(v map[string]any) {
	dir := filepath.Join(c.fs.Proc, "pressure")
	names := [3]string{"cpu", "memory", "io"}
	if c.opt.Container {
		if c.opt.CgroupDir == "" {
			return
		}
		dir = c.opt.CgroupDir
		names = [3]string{"cpu.pressure", "memory.pressure", "io.pressure"}
	}
	if p, ok := c.pressure(filepath.Join(dir, names[0])); ok {
		v["psi_cpu_some_avg10"], v["psi_cpu_some_avg60"] = p.SomeAvg10, p.SomeAvg60
	}
	if p, ok := c.pressure(filepath.Join(dir, names[1])); ok {
		v["psi_mem_some_avg10"], v["psi_mem_some_avg60"] = p.SomeAvg10, p.SomeAvg60
		if p.HasFull {
			v["psi_mem_full_avg10"] = p.FullAvg10
		}
	}
	if p, ok := c.pressure(filepath.Join(dir, names[2])); ok {
		v["psi_io_some_avg10"], v["psi_io_some_avg60"] = p.SomeAvg10, p.SomeAvg60
		if p.HasFull {
			v["psi_io_full_avg10"] = p.FullAvg10
		}
	}
}

func (c *Collector) pressure(path string) (Pressure, bool) {
	b, ok := c.read(path)
	if !ok {
		return Pressure{}, false
	}
	return ParsePressure(b)
}

func (c *Collector) readVMStat() (VMStat, bool) {
	b, ok := c.read(filepath.Join(c.fs.Proc, "vmstat"))
	if !ok {
		return VMStat{}, false
	}
	return ParseVMStat(b)
}

func (c *Collector) sampleVM(v map[string]any, secs float64) {
	now, ok := c.readVMStat()
	if ok && c.vmOK {
		p := c.vm
		rate := func(col string, a, b uint64) {
			if a >= b {
				v[col] = round(float64(a-b)/secs, 1)
			}
		}
		if now.PgFault >= now.PgMajFault && p.PgFault >= p.PgMajFault {
			rate("minor_faults_ps", now.PgFault-now.PgMajFault, p.PgFault-p.PgMajFault)
		}
		rate("major_faults_ps", now.PgMajFault, p.PgMajFault)
		rate("swap_in_ps", now.PswpIn, p.PswpIn)
		rate("swap_out_ps", now.PswpOut, p.PswpOut)
		rate("pages_scanned_ps", now.PgScan, p.PgScan)
		rate("pages_stolen_ps", now.PgSteal, p.PgSteal)
		if now.HasOOMKill && p.HasOOMKill && now.OOMKill >= p.OOMKill {
			v["host_oom_kills"] = int64(now.OOMKill - p.OOMKill)
		}
	}
	c.vm, c.vmOK = now, ok
	if b, ok := c.read(filepath.Join(c.fs.Proc, "meminfo")); ok {
		if m, ok := ParseMemInfo(b); ok {
			mb := func(kb uint64) float64 { return round(float64(kb)/1024, 1) }
			v["mem_dirty_mb"], v["mem_writeback_mb"] = mb(m.Dirty), mb(m.Writeback)
			v["mem_slab_mb"], v["mem_shmem_mb"] = mb(m.Slab), mb(m.Shmem)
			v["mem_committed_mb"], v["mem_commit_limit_mb"] = mb(m.Committed), mb(m.CommitLimit)
		}
	}
}

func (c *Collector) readUDP() (UDPErrors, bool) {
	var u UDPErrors
	ok := false
	if b, found := c.read(filepath.Join(c.fs.Proc, "net", "snmp")); found {
		u, ok = ParseSNMP(b)
	}
	if b, found := c.read(filepath.Join(c.fs.Proc, "net", "snmp6")); found && ok {
		if u6, ok6 := ParseSNMP6(b); ok6 {
			u.InErrors += u6.InErrors
			u.BufErrors += u6.BufErrors
		}
	}
	return u, ok
}

// unlimitedFiles is file-max on kernels that no longer cap file handles
const unlimitedFiles = 1 << 62

func (c *Collector) sampleLimits(v map[string]any) {
	if b, ok := c.read(filepath.Join(c.fs.Proc, "sys", "fs", "file-nr")); ok {
		if open, max, ok := ParseFileNr(b); ok {
			v["files_open"] = int64(open)
			if max > 0 && max < unlimitedFiles {
				v["files_max"] = int64(max)
			}
		}
	}
	if b, ok := c.read(filepath.Join(c.fs.Proc, "net", "sockstat")); ok {
		if s, ok := ParseSockStat(b); ok {
			v["sockets_used"], v["tcp_time_wait"], v["tcp_orphans"] = int64(s.Used), int64(s.TCPTimeWait), int64(s.TCPOrphan)
			if s.Used <= c.opt.TCPStateLimit {
				n, found := 0, false
				for _, f := range []string{"tcp", "tcp6"} {
					if b, ok := c.read(filepath.Join(c.fs.Proc, "net", f)); ok {
						n += CountTCPState(b, "08")
						found = true
					}
				}
				if found {
					v["tcp_close_wait"] = int64(n)
				}
			}
		}
	}
	nf := filepath.Join(c.fs.Proc, "sys", "net", "netfilter")
	if n, ok := c.readInt(filepath.Join(nf, "nf_conntrack_count")); ok {
		v["conntrack_count"] = n
		if m, ok := c.readInt(filepath.Join(nf, "nf_conntrack_max")); ok && m > 0 {
			v["conntrack_max"] = m
		}
	}
	now, ok := c.readUDP()
	if ok && c.udpOK && now.InErrors >= c.udp.InErrors && now.BufErrors >= c.udp.BufErrors {
		v["udp_in_errors"] = int64(now.InErrors - c.udp.InErrors)
		v["udp_buf_errors"] = int64(now.BufErrors - c.udp.BufErrors)
	}
	c.udp, c.udpOK = now, ok
}

func (c *Collector) readThrottle() (uint64, int) {
	var sum uint64
	n := 0
	for _, f := range c.throttleFiles {
		if x, ok := c.readInt(f); ok && x >= 0 {
			sum += uint64(x)
			n++
		}
	}
	return sum, n
}

func (c *Collector) sampleThermal(v map[string]any) {
	c.sensors = c.sensors[:0]
	cpuTemp, hasCPU := 0.0, false
	for _, s := range c.files {
		x, ok := c.readInt(s.path)
		if !ok {
			continue
		}
		val := float64(x) / s.scale
		if s.kind == "temperature" && (val <= -50 || val >= 200) {
			continue
		}
		c.sensors = append(c.sensors, Sensor{Name: s.name, Kind: s.kind, Value: round(val, 1), Unit: s.unit})
		if s.cpu && (!hasCPU || val > cpuTemp) {
			cpuTemp, hasCPU = val, true
		}
	}
	if hasCPU {
		v["cpu_temp_c"] = round(cpuTemp, 1)
	}
	if len(c.freqFiles) > 0 {
		sum, n := 0.0, 0
		for _, f := range c.freqFiles {
			if x, ok := c.readInt(f); ok && x > 0 {
				sum += float64(x) / 1000
				n++
			}
		}
		if n > 0 {
			v["cpu_mhz"] = round(sum/float64(n), 0)
		}
	} else if b, ok := c.read(filepath.Join(c.fs.Proc, "cpuinfo")); ok {
		if mhz, ok := ParseCPUInfoMHz(b); ok {
			v["cpu_mhz"] = round(mhz, 0)
		}
	}
	if c.maxMHz > 0 {
		v["cpu_max_mhz"] = round(c.maxMHz, 0)
	}
	if len(c.throttleFiles) > 0 {
		sum, n := c.readThrottle()
		if n == c.throttleN && n > 0 && sum >= c.throttle {
			v["cpu_throttle_events"] = int64(sum - c.throttle)
		}
		c.throttle, c.throttleN = sum, n
	}
}

func putGPUSummary(v map[string]any, gpus []GPU) {
	if len(gpus) == 0 {
		return
	}
	v["gpu_count"] = int64(len(gpus))
	var util, mem, temp float64
	var hasUtil, hasMem, hasTemp bool
	for _, g := range gpus {
		if g.UtilPercent != nil && (!hasUtil || *g.UtilPercent > util) {
			util, hasUtil = *g.UtilPercent, true
		}
		if m, ok := g.MemPercent(); ok && (!hasMem || m > mem) {
			mem, hasMem = m, true
		}
		if g.TempC != nil && (!hasTemp || *g.TempC > temp) {
			temp, hasTemp = *g.TempC, true
		}
	}
	if hasUtil {
		v["gpu_util_percent"] = round(util, 1)
	}
	if hasMem {
		v["gpu_mem_percent"] = round(mem, 1)
	}
	if hasTemp {
		v["gpu_temp_c"] = round(temp, 1)
	}
}

// read returns a file's contents in the collector's buffer, valid until the
// next read. Files under /proc report no size, so it reads to EOF and grows
// the buffer when a file fills it
func (c *Collector) read(path string) ([]byte, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	n := 0
	for {
		m, err := f.Read(c.buf[n:])
		n += m
		if err == io.EOF || (err == nil && m == 0) {
			return c.buf[:n], true
		}
		if err != nil {
			return nil, false
		}
		if n == len(c.buf) {
			if len(c.buf) >= 64<<20 {
				return nil, false
			}
			c.buf = append(c.buf, make([]byte, len(c.buf))...)
		}
	}
}

func (c *Collector) readInt(path string) (int64, bool) {
	b, ok := c.read(path)
	if !ok {
		return 0, false
	}
	return ParseInt(b)
}

func readString(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func round(v float64, prec int) float64 {
	p := math.Pow(10, float64(prec))
	return math.Round(v*p) / p
}
