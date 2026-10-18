package visionary

import (
	"os"
	"path/filepath"
	"time"

	"github.com/shirou/gopsutil/v4/host"
)

// HostColumns are the system_metrics columns a HostSampler fills: the
// machine's CPU, memory, swap, disk, network and process counts, plus
// interval_s and uptime_s (the host's uptime). Process, runtime, database
// and HTTP columns are not among them.
var HostColumns = []string{
	"interval_s",
	"host_cpu_percent", "cpu_count", "cpu_user_percent", "cpu_system_percent", "cpu_iowait_percent", "cpu_steal_percent",
	"load_1", "load_5", "load_15",
	"host_mem_used_mb", "host_mem_total_mb", "host_mem_percent", "host_mem_available_mb", "host_mem_cached_mb",
	"swap_used_mb", "swap_total_mb",
	"disk_used_percent", "disk_free_gb", "disk_total_gb", "disk_inodes_percent",
	"disk_read_kbps", "disk_write_kbps", "disk_read_iops", "disk_write_iops", "disk_busy_percent", "disk_await_ms",
	"net_rx_kbps", "net_tx_kbps", "net_rx_pps", "net_tx_pps", "net_errors", "net_drops",
	"tcp_established", "tcp_retrans",
	"host_procs", "procs_running", "procs_blocked", "ctx_switches_ps",
	"uptime_s",
}

// HostSampler reads the host-level system_metrics columns the same way the
// recorder does, without a process, language runtime or database to watch.
// The standalone host agent (cmd/visionary-agent) uses it. gopsutil reads
// HOST_PROC, HOST_SYS and HOST_ROOT, so it can watch a host from a container.
//
// Rates and deltas are measured since the previous Sample, or since
// NewHostSampler for the first one. A HostSampler is not safe for concurrent
// use.
type HostSampler struct{ r *recorder }

// NewHostSampler takes baseline readings. diskPath is the filesystem the disk
// space columns describe; empty means the root of the working directory's
// volume.
func NewHostSampler(diskPath string, interval time.Duration) *HostSampler {
	if diskPath == "" {
		wd, _ := os.Getwd()
		diskPath = filepath.VolumeName(wd) + string(filepath.Separator)
	}
	r := &recorder{
		cfg:      SystemConfig{Interval: interval, DiskPath: diskPath},
		diskDir:  diskPath,
		started:  time.Now(),
		hostOnly: true,
	}
	r.prime()
	return &HostSampler{r: r}
}

// Sample returns the HostColumns read this time, by name. Sources the
// platform or permissions do not allow are left out.
func (h *HostSampler) Sample() map[string]any {
	v := h.r.values()
	if up, err := host.Uptime(); err == nil {
		v["uptime_s"] = int64(up)
	}
	out := make(map[string]any, len(HostColumns))
	for _, c := range HostColumns {
		if x, ok := v[c]; ok && x != nil {
			out[c] = x
		}
	}
	return out
}
