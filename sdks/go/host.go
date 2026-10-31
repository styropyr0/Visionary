package visionary

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/shirou/gopsutil/v4/host"

	"github.com/styropyr0/visionary/sdks/go/hoststats"
)

// HostColumns are the system_metrics columns a HostSampler fills: the
// machine's CPU, memory, swap, disk, network and process counts, its
// pressure, virtual memory, kernel limits, temperatures and GPUs, plus
// interval_s and uptime_s (the host's uptime). Process, runtime, database,
// HTTP and container columns are not among them
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
	"psi_cpu_some_avg10", "psi_cpu_some_avg60", "psi_mem_some_avg10", "psi_mem_some_avg60", "psi_mem_full_avg10",
	"psi_io_some_avg10", "psi_io_some_avg60", "psi_io_full_avg10",
	"minor_faults_ps", "major_faults_ps", "swap_in_ps", "swap_out_ps", "pages_scanned_ps", "pages_stolen_ps", "host_oom_kills",
	"mem_dirty_mb", "mem_writeback_mb", "mem_slab_mb", "mem_shmem_mb", "mem_committed_mb", "mem_commit_limit_mb",
	"cpu_temp_c", "cpu_mhz", "cpu_max_mhz", "cpu_throttle_events",
	"files_open", "files_max", "sockets_used", "tcp_time_wait", "tcp_orphans", "tcp_close_wait",
	"conntrack_count", "conntrack_max", "udp_in_errors", "udp_buf_errors",
	"gpu_count", "gpu_util_percent", "gpu_mem_percent", "gpu_temp_c",
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

// HostOption changes what a HostSampler reads
type HostOption func(*recorder)

// WithoutGPU leaves out the GPU columns, so nvidia-smi never runs
func WithoutGPU() HostOption { return func(r *recorder) { r.gpu = false } }

// NewHostSampler takes baseline readings. diskPath is the filesystem the disk
// space columns describe; empty means the root of the working directory's
// volume.
func NewHostSampler(diskPath string, interval time.Duration, opts ...HostOption) *HostSampler {
	if diskPath == "" {
		wd, _ := os.Getwd()
		diskPath = filepath.VolumeName(wd) + string(filepath.Separator)
	}
	r := &recorder{
		cfg:      SystemConfig{Interval: interval, DiskPath: diskPath},
		diskDir:  diskPath,
		started:  time.Now(),
		hostOnly: true,
		gpu:      true,
	}
	for _, o := range opts {
		o(r)
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

// GPUs is each GPU as the latest Sample read it
func (h *HostSampler) GPUs() []hoststats.GPU { return h.r.stats.GPUs() }

// Sensors is every temperature and fan reading of the latest Sample
func (h *HostSampler) Sensors() []hoststats.Sensor { return h.r.stats.Sensors() }

// GPUProcesses lists the processes holding GPU memory (NVIDIA only)
func (h *HostSampler) GPUProcesses(ctx context.Context) []hoststats.GPUProcess {
	return h.r.stats.GPUProcesses(ctx)
}
