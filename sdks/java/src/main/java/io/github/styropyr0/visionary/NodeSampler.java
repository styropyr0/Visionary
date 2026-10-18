package io.github.styropyr0.visionary;

import java.io.File;
import java.lang.management.ManagementFactory;
import java.lang.management.OperatingSystemMXBean;
import java.util.Map;

/**
 * The node-exporter style columns (from {@code cpu_count} on, plus host network
 * throughput, and host CPU and memory on Linux): MXBeans where the JVM has them,
 * {@code /proc} on Linux for the rest. Counters are turned into per-sample deltas or per-second rates against
 * the previous reading; the first reading is taken at start. Whatever the
 * platform can't report stays NULL.
 */
final class NodeSampler {

    private static final double MB = 1024.0 * 1024.0;
    private static final double GB = MB * 1024.0;
    private static final long UNLIMITED_FDS = 1L << 40;

    private final ProcFs proc = new ProcFs();
    private final OperatingSystemMXBean os = ManagementFactory.getOperatingSystemMXBean();
    private final File disk;

    private ProcFs.Stat stat;
    private ProcFs.Disks disks;
    private ProcFs.Net net;
    private ProcFs.Tcp tcp;
    private ProcFs.ProcessIo processIo;

    NodeSampler(File disk) {
        this.disk = disk;
    }

    void prime() {
        stat = proc.stat();
        disks = proc.disks();
        net = proc.net();
        tcp = proc.tcp();
        processIo = proc.processIo();
    }

    /** Resident set size in kB on Linux, else null. */
    Long residentKb() {
        return proc.residentKb();
    }

    void sample(Object[] row, double secs) {
        set(row, "cpu_count", (long) Runtime.getRuntime().availableProcessors());
        sampleMemory(row);
        long total = disk.getTotalSpace();
        if (total > 0) {
            set(row, "disk_total_gb", Values.round(total / GB, 2));
        }
        if (os instanceof com.sun.management.UnixOperatingSystemMXBean unix) {
            long max = unix.getMaxFileDescriptorCount();
            if (max > 0 && max < UNLIMITED_FDS) {
                set(row, "proc_max_fds", max);
            }
        }
        set(row, "host_procs", proc.processCount());

        ProcFs.Stat nowStat = proc.stat();
        sampleCpu(row, stat, nowStat, secs);
        stat = nowStat;

        ProcFs.Disks nowDisks = proc.disks();
        sampleDisks(row, disks, nowDisks, secs);
        disks = nowDisks;

        ProcFs.Net nowNet = proc.net();
        sampleNet(row, net, nowNet, secs);
        net = nowNet;

        ProcFs.Tcp nowTcp = proc.tcp();
        if (nowTcp != null) {
            set(row, "tcp_established", nowTcp.currEstab());
            if (tcp != null && nowTcp.retransSegs() >= tcp.retransSegs()) {
                set(row, "tcp_retrans", nowTcp.retransSegs() - tcp.retransSegs());
            }
        }
        tcp = nowTcp;

        ProcFs.ProcessIo nowIo = proc.processIo();
        if (nowIo != null && processIo != null && nowIo.readBytes() >= processIo.readBytes()
                && nowIo.writeBytes() >= processIo.writeBytes()) {
            set(row, "proc_read_kbps", kbps(nowIo.readBytes() - processIo.readBytes(), secs));
            set(row, "proc_write_kbps", kbps(nowIo.writeBytes() - processIo.writeBytes(), secs));
        }
        processIo = nowIo;
    }

    private void sampleMemory(Object[] row) {
        ProcFs.Memory mem = proc.memory();
        // Replaces the MXBean figures on Linux: those count page cache as used, and inside a
        // container they are the cgroup's limit and usage rather than the host's memory.
        if (mem != null && mem.totalKb() != null && mem.totalKb() > 0 && mem.usedKb() != null) {
            long total = mem.totalKb();
            long notFree = mem.availableKb() != null ? total - mem.availableKb() : mem.usedKb();
            set(row, "host_mem_total_mb", Values.round(total / 1024.0, 1));
            set(row, "host_mem_used_mb", Values.round(mem.usedKb() / 1024.0, 1));
            set(row, "host_mem_percent", Values.round(notFree * 100.0 / total, 1));
        }
        if (mem != null && mem.availableKb() != null) {
            set(row, "host_mem_available_mb", Values.round(mem.availableKb() / 1024.0, 1));
        } else if (os instanceof com.sun.management.OperatingSystemMXBean sun && sun.getFreeMemorySize() >= 0) {
            set(row, "host_mem_available_mb", Values.round(sun.getFreeMemorySize() / MB, 1));
        }
        if (mem != null && mem.cachedKb() != null && mem.cachedKb() > 0) {
            set(row, "host_mem_cached_mb", Values.round(mem.cachedKb() / 1024.0, 1));
        }
        if (os instanceof com.sun.management.OperatingSystemMXBean sun) {
            long swapTotal = sun.getTotalSwapSpaceSize();
            long swapFree = sun.getFreeSwapSpaceSize();
            if (swapTotal >= 0 && swapFree >= 0) {
                set(row, "swap_used_mb", Values.round(Math.max(0, swapTotal - swapFree) / MB, 1));
                set(row, "swap_total_mb", Values.round(swapTotal / MB, 1));
            }
        }
    }

    private static void sampleCpu(Object[] row, ProcFs.Stat prev, ProcFs.Stat now, double secs) {
        if (now == null) {
            return;
        }
        set(row, "procs_running", now.procsRunning());
        set(row, "procs_blocked", now.procsBlocked());
        if (prev == null) {
            return;
        }
        if (now.ctxt() != null && prev.ctxt() != null && now.ctxt() >= prev.ctxt()) {
            set(row, "ctx_switches_ps", Values.round((now.ctxt() - prev.ctxt()) / secs, 1));
        }
        ProcFs.Cpu a = prev.cpu();
        ProcFs.Cpu b = now.cpu();
        if (a == null || b == null) {
            return;
        }
        double total = b.total() - a.total();
        if (total <= 0) {
            return;
        }
        // Replaces getCpuLoad() on Linux, which inside a container with a CPU limit reports the
        // container's share of that limit rather than the whole machine.
        long busy = b.total() - b.idle() - b.iowait() - (a.total() - a.idle() - a.iowait());
        set(row, "host_cpu_percent", share(busy, total));
        set(row, "cpu_user_percent", share(b.user() + b.nice() - a.user() - a.nice(), total));
        set(row, "cpu_system_percent",
                share(b.system() + b.irq() + b.softirq() - a.system() - a.irq() - a.softirq(), total));
        set(row, "cpu_iowait_percent", share(b.iowait() - a.iowait(), total));
        set(row, "cpu_steal_percent", share(b.steal() - a.steal(), total));
    }

    private static void sampleDisks(Object[] row, ProcFs.Disks prev, ProcFs.Disks now, double secs) {
        if (prev == null || now == null || now.readBytes() < prev.readBytes() || now.writeBytes() < prev.writeBytes()) {
            return;
        }
        long reads = now.reads() - prev.reads();
        long writes = now.writes() - prev.writes();
        set(row, "disk_read_kbps", kbps(now.readBytes() - prev.readBytes(), secs));
        set(row, "disk_write_kbps", kbps(now.writeBytes() - prev.writeBytes(), secs));
        set(row, "disk_read_iops", Values.round(reads / secs, 1));
        set(row, "disk_write_iops", Values.round(writes / secs, 1));
        if (reads + writes > 0 && now.ioTimeMs() >= prev.ioTimeMs()) {
            set(row, "disk_await_ms", Values.round((double) (now.ioTimeMs() - prev.ioTimeMs()) / (reads + writes), 2));
        }
        long busiest = 0;
        for (Map.Entry<String, Long> e : now.busyMs().entrySet()) {
            Long before = prev.busyMs().get(e.getKey());
            if (before != null && e.getValue() >= before) {
                busiest = Math.max(busiest, e.getValue() - before);
            }
        }
        if (!now.busyMs().isEmpty()) {
            set(row, "disk_busy_percent", Values.round(Math.min(100, 100.0 * busiest / (secs * 1000)), 1));
        }
    }

    private static void sampleNet(Object[] row, ProcFs.Net prev, ProcFs.Net now, double secs) {
        if (prev == null || now == null || now.rxPackets() < prev.rxPackets() || now.txPackets() < prev.txPackets()) {
            return;
        }
        if (now.rxBytes() >= prev.rxBytes() && now.txBytes() >= prev.txBytes()) {
            set(row, "net_rx_kbps", kbps(now.rxBytes() - prev.rxBytes(), secs));
            set(row, "net_tx_kbps", kbps(now.txBytes() - prev.txBytes(), secs));
        }
        set(row, "net_rx_pps", Values.round((now.rxPackets() - prev.rxPackets()) / secs, 1));
        set(row, "net_tx_pps", Values.round((now.txPackets() - prev.txPackets()) / secs, 1));
        if (now.errors() >= prev.errors()) {
            set(row, "net_errors", now.errors() - prev.errors());
        }
        if (now.drops() >= prev.drops()) {
            set(row, "net_drops", now.drops() - prev.drops());
        }
    }

    private static Double share(long delta, double total) {
        return Values.round(100.0 * delta / total, 1);
    }

    private static Double kbps(long bytes, double secs) {
        return Values.round(bytes / 1024.0 / secs, 1);
    }

    private static void set(Object[] row, String column, Object value) {
        row[Table.SYSTEM_METRICS.indexOf(column)] = value;
    }
}
