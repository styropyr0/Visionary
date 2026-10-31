package hoststats

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// GPU is one device's reading. Pointers are nil when the driver does not
// report the value
type GPU struct {
	Index       int      `json:"index"`
	UUID        string   `json:"uuid,omitempty"`
	Name        string   `json:"name"`
	Vendor      string   `json:"vendor"`
	UtilPercent *float64 `json:"util_percent,omitempty"`
	MemUtilPct  *float64 `json:"mem_util_percent,omitempty"`
	MemUsedMB   *float64 `json:"mem_used_mb,omitempty"`
	MemTotalMB  *float64 `json:"mem_total_mb,omitempty"`
	TempC       *float64 `json:"temp_c,omitempty"`
	PowerW      *float64 `json:"power_w,omitempty"`
	PowerLimitW *float64 `json:"power_limit_w,omitempty"`
	FanPercent  *float64 `json:"fan_percent,omitempty"`
	ClockMHz    *float64 `json:"clock_mhz,omitempty"`
	MaxClockMHz *float64 `json:"max_clock_mhz,omitempty"`
}

// MemPercent is memory in use as a share of the device's memory
func (g GPU) MemPercent() (float64, bool) {
	if g.MemUsedMB == nil || g.MemTotalMB == nil || *g.MemTotalMB <= 0 {
		return 0, false
	}
	return 100 * *g.MemUsedMB / *g.MemTotalMB, true
}

// GPUProcess is a process holding GPU memory (NVIDIA only)
type GPUProcess struct {
	GPUUUID   string  `json:"gpu_uuid"`
	GPUIndex  int     `json:"gpu_index"`
	PID       int64   `json:"pid"`
	Name      string  `json:"name"`
	UsedMemMB float64 `json:"used_mem_mb"`
}

// NvidiaQuery is the --query-gpu field list ParseNvidiaGPUs expects
const NvidiaQuery = "index,uuid,name,utilization.gpu,utilization.memory,memory.used,memory.total,temperature.gpu,power.draw,power.limit,fan.speed,clocks.sm,clocks.max.sm"

// ParseNvidiaGPUs reads nvidia-smi --query-gpu=NvidiaQuery
// --format=csv,noheader,nounits. "[N/A]" and "[Not Supported]" are nil
func ParseNvidiaGPUs(b []byte) []GPU {
	var out []GPU
	for len(b) > 0 {
		var line []byte
		line, b = nextLine(b)
		f := strings.Split(strings.TrimSpace(string(line)), ",")
		if len(f) < 13 {
			continue
		}
		// A comma in the name shifts the numeric fields right
		name := strings.Join(f[2:len(f)-10], ",")
		nums := f[len(f)-10:]
		idx, err := strconv.Atoi(strings.TrimSpace(f[0]))
		if err != nil {
			continue
		}
		g := GPU{Index: idx, UUID: strings.TrimSpace(f[1]), Name: strings.TrimSpace(name), Vendor: "nvidia"}
		ptrs := []**float64{&g.UtilPercent, &g.MemUtilPct, &g.MemUsedMB, &g.MemTotalMB, &g.TempC, &g.PowerW, &g.PowerLimitW, &g.FanPercent, &g.ClockMHz, &g.MaxClockMHz}
		for i, p := range ptrs {
			*p = smiNumber(nums[i])
		}
		out = append(out, g)
	}
	return out
}

// ParseNvidiaApps reads nvidia-smi
// --query-compute-apps=gpu_uuid,pid,process_name,used_memory
// --format=csv,noheader,nounits
func ParseNvidiaApps(b []byte) []GPUProcess {
	var out []GPUProcess
	for len(b) > 0 {
		var line []byte
		line, b = nextLine(b)
		f := strings.Split(strings.TrimSpace(string(line)), ",")
		if len(f) < 4 {
			continue
		}
		pid, err := strconv.ParseInt(strings.TrimSpace(f[1]), 10, 64)
		if err != nil {
			continue
		}
		p := GPUProcess{GPUUUID: strings.TrimSpace(f[0]), PID: pid, Name: strings.TrimSpace(strings.Join(f[2:len(f)-1], ","))}
		if v := smiNumber(f[len(f)-1]); v != nil {
			p.UsedMemMB = *v
		}
		out = append(out, p)
	}
	return out
}

func smiNumber(s string) *float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return nil
	}
	return &f
}

// nvidiaPaths are where the driver installs nvidia-smi when it is not on PATH
var nvidiaPaths = []string{
	"/usr/bin/nvidia-smi", "/usr/local/bin/nvidia-smi", "/usr/local/nvidia/bin/nvidia-smi", "/usr/lib/wsl/lib/nvidia-smi",
	`C:\Windows\System32\nvidia-smi.exe`, `C:\Program Files\NVIDIA Corporation\NVSMI\nvidia-smi.exe`,
}

// findNvidiaSMI is nvidia-smi's path, or "" without one
func findNvidiaSMI() string {
	if p, err := exec.LookPath("nvidia-smi"); err == nil {
		return p
	}
	for _, p := range nvidiaPaths {
		if filepath.IsAbs(p) && fileExists(p) {
			return p
		}
	}
	return ""
}

// gpuReader runs nvidia-smi once per sample and reads AMD cards from sysfs
type gpuReader struct {
	fs       FS
	smi      string
	smiFound time.Time
	failures int
	skipTill time.Time
	amd      []string
	last     []GPU
	run      func(ctx context.Context, name string, args ...string) ([]byte, error)
}

const smiTimeout = 5 * time.Second

func newGPUReader(fs FS) *gpuReader {
	g := &gpuReader{fs: fs, run: runCommand}
	if fs.linux {
		cards, _ := filepath.Glob(filepath.Join(fs.Sys, "class", "drm", "card*"))
		for _, c := range cards {
			if _, err := strconv.Atoi(strings.TrimPrefix(filepath.Base(c), "card")); err != nil {
				continue
			}
			dev := filepath.Join(c, "device")
			if v, _ := os.ReadFile(filepath.Join(dev, "vendor")); strings.TrimSpace(string(v)) == "0x1002" && fileExists(filepath.Join(dev, "gpu_busy_percent")) {
				g.amd = append(g.amd, dev)
			}
		}
	}
	return g
}

func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, smiTimeout)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Output()
}

// read takes one reading of every GPU
func (g *gpuReader) read(now time.Time) []GPU {
	var out []GPU
	if g.smi == "" && now.Sub(g.smiFound) > time.Hour && (runtime.GOOS == "linux" || runtime.GOOS == "windows") {
		g.smi, g.smiFound = findNvidiaSMI(), now
	}
	if g.smi != "" && now.After(g.skipTill) {
		b, err := g.run(context.Background(), g.smi, "--query-gpu="+NvidiaQuery, "--format=csv,noheader,nounits")
		if err != nil {
			g.failures++
			g.skipTill = now.Add(min(time.Duration(g.failures)*time.Minute, 10*time.Minute))
		} else {
			g.failures = 0
			out = append(out, ParseNvidiaGPUs(b)...)
		}
	}
	for i, dev := range g.amd {
		out = append(out, readAMD(dev, i))
	}
	g.last = out
	return out
}

// processes lists the processes holding GPU memory, with a second
// nvidia-smi call, which the agent makes once per report
func (g *gpuReader) processes(ctx context.Context) []GPUProcess {
	if g.smi == "" || time.Now().Before(g.skipTill) {
		return nil
	}
	b, err := g.run(ctx, g.smi, "--query-compute-apps=gpu_uuid,pid,process_name,used_memory", "--format=csv,noheader,nounits")
	if err != nil {
		return nil
	}
	procs := ParseNvidiaApps(b)
	idx := map[string]int{}
	for _, d := range g.last {
		idx[d.UUID] = d.Index
	}
	for i := range procs {
		procs[i].GPUIndex = idx[procs[i].GPUUUID]
	}
	return procs
}

// readAMD reads an amdgpu card's sysfs attributes and its hwmon
func readAMD(dev string, n int) GPU {
	g := GPU{Index: n, Vendor: "amd", Name: "AMD GPU"}
	if card := filepath.Base(filepath.Dir(dev)); card != "" {
		if i, err := strconv.Atoi(strings.TrimPrefix(card, "card")); err == nil {
			g.Index = i
		}
	}
	if b, err := os.ReadFile(filepath.Join(dev, "product_name")); err == nil && len(bytes.TrimSpace(b)) > 0 {
		g.Name = string(bytes.TrimSpace(b))
	} else if b, err := os.ReadFile(filepath.Join(dev, "device")); err == nil {
		g.Name = "AMD GPU " + strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile(filepath.Join(dev, "unique_id")); err == nil {
		g.UUID = strings.TrimSpace(string(b))
	}
	num := func(name string, scale float64) *float64 {
		b, err := os.ReadFile(filepath.Join(dev, name))
		if err != nil {
			return nil
		}
		v, ok := ParseInt(b)
		if !ok {
			return nil
		}
		f := float64(v) / scale
		return &f
	}
	g.UtilPercent = num("gpu_busy_percent", 1)
	g.MemUtilPct = num("mem_busy_percent", 1)
	g.MemUsedMB = num("mem_info_vram_used", 1<<20)
	g.MemTotalMB = num("mem_info_vram_total", 1<<20)
	if b, err := os.ReadFile(filepath.Join(dev, "pp_dpm_sclk")); err == nil {
		if cur, max, ok := ParseDPMClock(b); ok {
			g.ClockMHz, g.MaxClockMHz = &cur, &max
		}
	}
	mons, _ := filepath.Glob(filepath.Join(dev, "hwmon", "hwmon*"))
	for _, h := range mons {
		temps, _ := filepath.Glob(filepath.Join(h, "temp*_input"))
		for _, t := range temps {
			if b, err := os.ReadFile(t); err == nil {
				if v, ok := ParseInt(b); ok {
					c := float64(v) / 1000
					if g.TempC == nil || c > *g.TempC {
						g.TempC = &c
					}
				}
			}
		}
		read := func(names ...string) *float64 {
			for _, n := range names {
				if b, err := os.ReadFile(filepath.Join(h, n)); err == nil {
					if v, ok := ParseInt(b); ok {
						f := float64(v)
						return &f
					}
				}
			}
			return nil
		}
		if p := read("power1_average", "power1_input"); p != nil {
			w := *p / 1e6
			g.PowerW = &w
		}
		if p := read("power1_cap"); p != nil {
			w := *p / 1e6
			g.PowerLimitW = &w
		}
		if pwm, top := read("pwm1"), read("pwm1_max"); pwm != nil {
			m := 255.0
			if top != nil && *top > 0 {
				m = *top
			}
			pct := 100 * *pwm / m
			g.FanPercent = &pct
		}
	}
	return g
}
