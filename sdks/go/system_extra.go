package visionary

import (
	"regexp"
	"strings"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	gnet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
)

// counters holds the cumulative readings the extra metrics are deltas of.
type counters struct {
	cpu        *cpu.TimesStat
	diskRead   uint64
	diskWrite  uint64
	diskReads  uint64
	diskWrites uint64
	diskTime   uint64 // read + write time, ms
	diskBusy   map[string]uint64
	netRxPkts  uint64
	netTxPkts  uint64
	netErrs    uint64
	netDrops   uint64
	tcpRetrans int64
	ctxt       int64
	procRead   uint64
	procWrite  uint64
	ok         map[string]bool // which baselines were read
}

// partition matches partitions and virtual devices whose I/O is already
// counted in a whole disk, so totals are not doubled.
var partition = regexp.MustCompile(`^((sd|vd|xvd|hd)[a-z]+\d+|nvme\d+n\d+p\d+|mmcblk\d+p\d+|loop\d+|ram\d+|zram\d+|dm-\d+|sr\d+|fd\d+|md\d+)$`)

func readCounters(proc *process.Process) counters {
	c := counters{ok: map[string]bool{}, diskBusy: map[string]uint64{}}
	if t, err := cpu.Times(false); err == nil && len(t) > 0 {
		c.cpu = &t[0]
	}
	if io, err := disk.IOCounters(); err == nil && len(io) > 0 {
		for name, d := range io {
			if partition.MatchString(name) {
				continue
			}
			c.diskRead += d.ReadBytes
			c.diskWrite += d.WriteBytes
			c.diskReads += d.ReadCount
			c.diskWrites += d.WriteCount
			c.diskTime += d.ReadTime + d.WriteTime
			c.diskBusy[name] = d.IoTime
		}
		c.ok["disk"] = true
	}
	if io, err := gnet.IOCounters(false); err == nil && len(io) > 0 {
		c.netRxPkts, c.netTxPkts = io[0].PacketsRecv, io[0].PacketsSent
		c.netErrs = io[0].Errin + io[0].Errout
		c.netDrops = io[0].Dropin + io[0].Dropout
		c.ok["net"] = true
	}
	if p, err := gnet.ProtoCounters([]string{"tcp"}); err == nil && len(p) > 0 {
		c.tcpRetrans = p[0].Stats["RetransSegs"]
		c.ok["tcp"] = true
	}
	if m, err := load.Misc(); err == nil && m.Ctxt > 0 {
		c.ctxt = int64(m.Ctxt)
		c.ok["ctxt"] = true
	}
	if proc != nil {
		if io, err := proc.IOCounters(); err == nil {
			c.procRead, c.procWrite = io.ReadBytes, io.WriteBytes
			c.ok["proc_io"] = true
		}
	}
	return c
}

// sampleExtra fills the node-level columns. Sources the platform lacks
// (iowait and steal outside Linux, TCP counters on Windows) stay NULL.
func (r *recorder) sampleExtra(v map[string]any, secs float64) {
	if n, err := cpu.Counts(true); err == nil && n > 0 {
		v["cpu_count"] = int64(n)
	}
	if vm, err := mem.VirtualMemory(); err == nil {
		v["host_mem_available_mb"] = round(mb(vm.Available), 1)
		if vm.Cached > 0 {
			v["host_mem_cached_mb"] = round(mb(vm.Cached), 1)
		}
	}
	if sw, err := mem.SwapMemory(); err == nil {
		v["swap_used_mb"] = round(mb(sw.Used), 1)
		v["swap_total_mb"] = round(mb(sw.Total), 1)
	}
	if du, err := disk.Usage(r.diskDir); err == nil {
		v["disk_total_gb"] = round(float64(du.Total)/(1<<30), 2)
		if du.InodesTotal > 0 {
			v["disk_inodes_percent"] = round(du.InodesUsedPercent, 1)
		}
	}
	if m, err := load.Misc(); err == nil && m.ProcsTotal > 0 {
		v["host_procs"] = int64(m.ProcsTotal)
		v["procs_running"] = int64(m.ProcsRunning)
		v["procs_blocked"] = int64(m.ProcsBlocked)
	} else if pids, err := process.Pids(); err == nil {
		v["host_procs"] = int64(len(pids))
	}
	if r.proc != nil {
		if lim, err := r.proc.Rlimit(); err == nil {
			for _, l := range lim {
				if l.Resource == process.RLIMIT_NOFILE && l.Soft > 0 && l.Soft < 1<<40 {
					v["proc_max_fds"] = int64(l.Soft)
				}
			}
		}
	}

	now := readCounters(r.proc)
	prev := r.prev
	r.prev = now

	if now.cpu != nil && prev.cpu != nil {
		total := now.cpu.Total() - prev.cpu.Total()
		if total > 0 {
			pct := func(a, b float64) float64 { return round(100*(a-b)/total, 1) }
			v["cpu_user_percent"] = pct(now.cpu.User+now.cpu.Nice, prev.cpu.User+prev.cpu.Nice)
			v["cpu_system_percent"] = pct(now.cpu.System+now.cpu.Irq+now.cpu.Softirq, prev.cpu.System+prev.cpu.Irq+prev.cpu.Softirq)
			if isLinux() {
				v["cpu_iowait_percent"] = pct(now.cpu.Iowait, prev.cpu.Iowait)
				v["cpu_steal_percent"] = pct(now.cpu.Steal, prev.cpu.Steal)
			}
		}
	}
	both := func(k string) bool { return now.ok[k] && prev.ok[k] }
	if both("disk") && now.diskRead >= prev.diskRead && now.diskWrite >= prev.diskWrite {
		v["disk_read_kbps"] = round(float64(now.diskRead-prev.diskRead)/1024/secs, 1)
		v["disk_write_kbps"] = round(float64(now.diskWrite-prev.diskWrite)/1024/secs, 1)
		reads, writes := now.diskReads-prev.diskReads, now.diskWrites-prev.diskWrites
		v["disk_read_iops"] = round(float64(reads)/secs, 1)
		v["disk_write_iops"] = round(float64(writes)/secs, 1)
		if ops := reads + writes; ops > 0 && now.diskTime >= prev.diskTime {
			v["disk_await_ms"] = round(float64(now.diskTime-prev.diskTime)/float64(ops), 2)
		}
		// Busy is the busiest disk's share of the interval spent on I/O.
		var busiest uint64
		for name, t := range now.diskBusy {
			if p, ok := prev.diskBusy[name]; ok && t >= p && t-p > busiest {
				busiest = t - p
			}
		}
		if len(now.diskBusy) > 0 && isLinux() {
			v["disk_busy_percent"] = round(min(100, 100*float64(busiest)/(secs*1000)), 1)
		}
	}
	if both("net") && now.netRxPkts >= prev.netRxPkts && now.netTxPkts >= prev.netTxPkts {
		v["net_rx_pps"] = round(float64(now.netRxPkts-prev.netRxPkts)/secs, 1)
		v["net_tx_pps"] = round(float64(now.netTxPkts-prev.netTxPkts)/secs, 1)
		if now.netErrs >= prev.netErrs {
			v["net_errors"] = int64(now.netErrs - prev.netErrs)
		}
		if now.netDrops >= prev.netDrops {
			v["net_drops"] = int64(now.netDrops - prev.netDrops)
		}
	}
	if both("tcp") && now.tcpRetrans >= prev.tcpRetrans {
		v["tcp_retrans"] = now.tcpRetrans - prev.tcpRetrans
	}
	if now.ok["tcp"] {
		if p, err := gnet.ProtoCounters([]string{"tcp"}); err == nil && len(p) > 0 {
			v["tcp_established"] = p[0].Stats["CurrEstab"]
		}
	}
	if both("ctxt") && now.ctxt >= prev.ctxt {
		v["ctx_switches_ps"] = round(float64(now.ctxt-prev.ctxt)/secs, 1)
	}
	if both("proc_io") && now.procRead >= prev.procRead && now.procWrite >= prev.procWrite {
		v["proc_read_kbps"] = round(float64(now.procRead-prev.procRead)/1024/secs, 1)
		v["proc_write_kbps"] = round(float64(now.procWrite-prev.procWrite)/1024/secs, 1)
	}
}

func isLinux() bool { return strings.EqualFold(goos, "linux") }
