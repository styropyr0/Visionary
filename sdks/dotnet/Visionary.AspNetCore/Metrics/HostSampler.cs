using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Linq;
using System.Net.NetworkInformation;

namespace Styropyr0.Visionary;

/// <summary>
/// Host and process columns: /proc on Linux, Win32 on Windows, managed APIs everywhere else.
/// Counters become per-sample deltas or per-second rates against the previous reading; whatever
/// the platform can't report stays NULL.
/// </summary>
internal sealed class HostSampler
{
    public const double Mb = 1024.0 * 1024.0;
    private const double Gb = Mb * 1024.0;

    private readonly Process _process = Process.GetCurrentProcess();
    private readonly string _diskPath;

    private DateTime _lastSample;
    private TimeSpan _procCpu;
    private ProcFs.Stat? _stat;
    private Native.CpuTimes? _winCpu;
    private ProcFs.Disks? _disks;
    private ProcFs.Net? _net;
    private ProcFs.Tcp? _tcp;
    private (ulong Read, ulong Write)? _procIo;

    public HostSampler(string? diskPath)
    {
        _diskPath = string.IsNullOrWhiteSpace(diskPath) ? Directory.GetCurrentDirectory() : diskPath;
    }

    public void Prime()
    {
        _lastSample = DateTime.UtcNow;
        _procCpu = ProcessCpu() ?? TimeSpan.Zero;
        _stat = ProcFs.ReadStat();
        _winCpu = Native.SystemTimes();
        _disks = ProcFs.ReadDisks();
        _net = ReadNet();
        _tcp = ProcFs.ReadTcp();
        _procIo = ProcessIo();
    }

    public void Sample(Dictionary<string, object?> v, double secs)
    {
        var now = DateTime.UtcNow;
        var elapsed = (now - _lastSample).TotalSeconds;
        _lastSample = now;
        if (elapsed <= 0)
        {
            elapsed = secs;
        }
        v["cpu_count"] = (long)Environment.ProcessorCount;
        SampleProcess(v, elapsed);
        SampleCpu(v, secs);
        SampleMemory(v);
        SampleDisk(v, secs);
        SampleNet(v, secs);

        if (ProcFs.ReadLoad() is { Length: 3 } load)
        {
            v["load_1"] = Math.Round(load[0], 2);
            v["load_5"] = Math.Round(load[1], 2);
            v["load_15"] = Math.Round(load[2], 2);
        }
        var tcp = ProcFs.ReadTcp();
        if (tcp is not null)
        {
            v["tcp_established"] = tcp.CurrEstab;
            if (_tcp is not null && tcp.RetransSegs >= _tcp.RetransSegs)
            {
                v["tcp_retrans"] = tcp.RetransSegs - _tcp.RetransSegs;
            }
        }
        _tcp = tcp;
        v["host_procs"] = ProcFs.ProcessCount() ?? CountProcesses();
    }

    private void SampleProcess(Dictionary<string, object?> v, double elapsed)
    {
        _process.Refresh();
        var cpu = ProcessCpu();
        if (cpu is not null)
        {
            v["proc_cpu_percent"] = Math.Round((cpu.Value - _procCpu).TotalSeconds / elapsed * 100, 1);
            _procCpu = cpu.Value;
        }
        Try(() => v["proc_rss_mb"] = Math.Round(_process.WorkingSet64 / Mb, 1));
        Try(() => v["proc_threads"] = (long)_process.Threads.Count);
        if (ProcFs.Available)
        {
            v["proc_open_fds"] = ProcFs.OpenFds();
            v["proc_max_fds"] = ProcFs.MaxOpenFiles();
        }
        else if (OperatingSystem.IsWindows())
        {
            Try(() => v["proc_open_fds"] = (long)_process.HandleCount);
        }

        var io = ProcessIo();
        if (io is not null && _procIo is not null && io.Value.Read >= _procIo.Value.Read && io.Value.Write >= _procIo.Value.Write)
        {
            v["proc_read_kbps"] = Kbps((long)(io.Value.Read - _procIo.Value.Read), elapsed);
            v["proc_write_kbps"] = Kbps((long)(io.Value.Write - _procIo.Value.Write), elapsed);
        }
        _procIo = io;
    }

    private void SampleCpu(Dictionary<string, object?> v, double secs)
    {
        var stat = ProcFs.ReadStat();
        if (stat is not null)
        {
            v["procs_running"] = stat.ProcsRunning;
            v["procs_blocked"] = stat.ProcsBlocked;
            var prev = _stat;
            _stat = stat;
            if (prev is null)
            {
                return;
            }
            if (stat.Ctxt is { } c && prev.Ctxt is { } pc && c >= pc)
            {
                v["ctx_switches_ps"] = Math.Round((c - pc) / secs, 1);
            }
            if (stat.Cpu is { } b && prev.Cpu is { } a && b.Total - a.Total > 0)
            {
                double total = b.Total - a.Total;
                double Share(long delta) => Math.Round(100.0 * delta / total, 1);
                // gopsutil's busy time: everything but idle and iowait.
                v["host_cpu_percent"] = Share(b.Total - b.Idle - b.Iowait - (a.Total - a.Idle - a.Iowait));
                v["cpu_user_percent"] = Share(b.User + b.Nice - a.User - a.Nice);
                v["cpu_system_percent"] = Share(b.System + b.Irq + b.Softirq - a.System - a.Irq - a.Softirq);
                v["cpu_iowait_percent"] = Share(b.Iowait - a.Iowait);
                v["cpu_steal_percent"] = Share(b.Steal - a.Steal);
            }
            return;
        }
        var win = Native.SystemTimes();
        if (win is { } w && _winCpu is { } p)
        {
            double idle = w.Idle - p.Idle, kernel = w.Kernel - p.Kernel, user = w.User - p.User;
            var total = kernel + user;
            if (total > 0)
            {
                v["host_cpu_percent"] = Math.Round(100 * (total - idle) / total, 1);
                v["cpu_user_percent"] = Math.Round(100 * user / total, 1);
                v["cpu_system_percent"] = Math.Round(100 * (kernel - idle) / total, 1);
            }
        }
        _winCpu = win;
    }

    private void SampleMemory(Dictionary<string, object?> v)
    {
        var mem = ProcFs.ReadMemory();
        if (mem is { TotalKb: { } totalKb, AvailableKb: { } availKb } && totalKb > 0)
        {
            var used = totalKb - availKb;
            v["host_mem_total_mb"] = Math.Round(totalKb / 1024.0, 1);
            v["host_mem_used_mb"] = Math.Round(used / 1024.0, 1);
            v["host_mem_percent"] = Math.Round(100.0 * used / totalKb, 1);
            v["host_mem_available_mb"] = Math.Round(availKb / 1024.0, 1);
            if (mem.CachedKb is > 0)
            {
                v["host_mem_cached_mb"] = Math.Round(mem.CachedKb.Value / 1024.0, 1);
            }
            if (mem is { SwapTotalKb: { } swapTotal, SwapFreeKb: { } swapFree })
            {
                v["swap_total_mb"] = Math.Round(swapTotal / 1024.0, 1);
                v["swap_used_mb"] = Math.Round(Math.Max(0, swapTotal - swapFree) / 1024.0, 1);
            }
            return;
        }
        if (Native.Memory() is { TotalPhys: > 0 } m)
        {
            var used = m.TotalPhys - m.AvailPhys;
            v["host_mem_total_mb"] = Math.Round(m.TotalPhys / Mb, 1);
            v["host_mem_used_mb"] = Math.Round(used / Mb, 1);
            v["host_mem_percent"] = Math.Round(100.0 * used / m.TotalPhys, 1);
            v["host_mem_available_mb"] = Math.Round(m.AvailPhys / Mb, 1);
            // The page file is RAM plus swap; what is committed beyond RAM is swap in use.
            var swapTotal = m.TotalPageFile > m.TotalPhys ? m.TotalPageFile - m.TotalPhys : 0;
            var committed = m.TotalPageFile - m.AvailPageFile;
            var swapUsed = committed > used ? committed - used : 0;
            v["swap_total_mb"] = Math.Round(swapTotal / Mb, 1);
            v["swap_used_mb"] = Math.Round(Math.Min(swapUsed, swapTotal) / Mb, 1);
            return;
        }
        // Elsewhere the GC knows the memory the process may use (the host's, or a container limit).
        var gc = GC.GetGCMemoryInfo();
        if (gc.TotalAvailableMemoryBytes > 0)
        {
            v["host_mem_total_mb"] = Math.Round(gc.TotalAvailableMemoryBytes / Mb, 1);
        }
    }

    private void SampleDisk(Dictionary<string, object?> v, double secs)
    {
        var drive = Drive();
        if (drive is not null)
        {
            Try(() =>
            {
                long total = drive.TotalSize, free = drive.TotalFreeSpace, avail = drive.AvailableFreeSpace;
                if (total <= 0)
                {
                    return;
                }
                // Same definitions as gopsutil: used / (used + available to unprivileged users).
                var used = total - free;
                if (used + avail > 0)
                {
                    v["disk_used_percent"] = Math.Round(100.0 * used / (used + avail), 1);
                }
                v["disk_free_gb"] = Math.Round(avail / Gb, 2);
                v["disk_total_gb"] = Math.Round(total / Gb, 2);
            });
        }
        if (Native.InodesUsedPercent(_diskPath) is { } inodes)
        {
            v["disk_inodes_percent"] = Math.Round(inodes, 1);
        }

        var disks = ProcFs.ReadDisks();
        var prev = _disks;
        _disks = disks;
        if (disks is null || prev is null || disks.ReadBytes < prev.ReadBytes || disks.WriteBytes < prev.WriteBytes)
        {
            return;
        }
        long reads = disks.Reads - prev.Reads, writes = disks.Writes - prev.Writes;
        v["disk_read_kbps"] = Kbps(disks.ReadBytes - prev.ReadBytes, secs);
        v["disk_write_kbps"] = Kbps(disks.WriteBytes - prev.WriteBytes, secs);
        v["disk_read_iops"] = Math.Round(reads / secs, 1);
        v["disk_write_iops"] = Math.Round(writes / secs, 1);
        if (reads + writes > 0 && disks.IoTimeMs >= prev.IoTimeMs)
        {
            v["disk_await_ms"] = Math.Round((double)(disks.IoTimeMs - prev.IoTimeMs) / (reads + writes), 2);
        }
        if (disks.BusyMs.Count > 0)
        {
            long busiest = 0;
            foreach (var (name, ms) in disks.BusyMs)
            {
                if (prev.BusyMs.TryGetValue(name, out var before) && ms >= before)
                {
                    busiest = Math.Max(busiest, ms - before);
                }
            }
            v["disk_busy_percent"] = Math.Round(Math.Min(100, 100.0 * busiest / (secs * 1000)), 1);
        }
    }

    private void SampleNet(Dictionary<string, object?> v, double secs)
    {
        var net = ReadNet();
        var prev = _net;
        _net = net;
        if (net is null || prev is null)
        {
            return;
        }
        if (net.RxBytes >= prev.RxBytes && net.TxBytes >= prev.TxBytes)
        {
            v["net_rx_kbps"] = Kbps(net.RxBytes - prev.RxBytes, secs);
            v["net_tx_kbps"] = Kbps(net.TxBytes - prev.TxBytes, secs);
        }
        if (net.RxPackets >= prev.RxPackets && net.TxPackets >= prev.TxPackets)
        {
            v["net_rx_pps"] = Math.Round((net.RxPackets - prev.RxPackets) / secs, 1);
            v["net_tx_pps"] = Math.Round((net.TxPackets - prev.TxPackets) / secs, 1);
        }
        if (net.Errors >= prev.Errors)
        {
            v["net_errors"] = net.Errors - prev.Errors;
        }
        if (net.Drops >= prev.Drops)
        {
            v["net_drops"] = net.Drops - prev.Drops;
        }
    }

    /// <summary>/proc/net/dev on Linux; the interface statistics on Windows.</summary>
    private static ProcFs.Net? ReadNet()
    {
        if (ProcFs.Available)
        {
            return ProcFs.ReadNet();
        }
        if (!OperatingSystem.IsWindows())
        {
            return null;
        }
        try
        {
            long rx = 0, tx = 0, rxp = 0, txp = 0, errs = 0, drops = 0;
            var any = false;
            foreach (var nic in NetworkInterface.GetAllNetworkInterfaces())
            {
                try
                {
                    var s = nic.GetIPStatistics();
                    rx += s.BytesReceived;
                    tx += s.BytesSent;
                    rxp += s.UnicastPacketsReceived + s.NonUnicastPacketsReceived;
                    txp += s.UnicastPacketsSent + s.NonUnicastPacketsSent;
                    errs += s.IncomingPacketsWithErrors + s.OutgoingPacketsWithErrors;
                    drops += s.IncomingPacketsDiscarded + s.OutgoingPacketsDiscarded;
                    any = true;
                }
                catch (Exception)
                {
                    // an interface without statistics on this platform
                }
            }
            return any ? new ProcFs.Net(rx, tx, rxp, txp, errs, drops) : null;
        }
        catch (Exception)
        {
            return null;
        }
    }

    /// <summary>The mounted filesystem holding the disk path: the longest matching mount point.</summary>
    private DriveInfo? Drive()
    {
        try
        {
            var full = Path.GetFullPath(_diskPath);
            var comparison = OperatingSystem.IsWindows() ? StringComparison.OrdinalIgnoreCase : StringComparison.Ordinal;
            return DriveInfo.GetDrives()
                .Where(d => full.StartsWith(d.RootDirectory.FullName, comparison) && IsReady(d))
                .OrderByDescending(d => d.RootDirectory.FullName.Length)
                .FirstOrDefault();
        }
        catch (Exception)
        {
            return null;
        }
    }

    private static bool IsReady(DriveInfo d)
    {
        try
        {
            return d.IsReady && d.TotalSize > 0;
        }
        catch (Exception)
        {
            return false;
        }
    }

    private TimeSpan? ProcessCpu()
    {
        try
        {
            return _process.TotalProcessorTime;
        }
        catch (Exception)
        {
            return null;
        }
    }

    private (ulong Read, ulong Write)? ProcessIo()
    {
        if (ProcFs.ReadProcessIo() is { } io)
        {
            return ((ulong)io.ReadBytes, (ulong)io.WriteBytes);
        }
        try
        {
            return Native.ProcessIo(_process.Handle);
        }
        catch (Exception)
        {
            return null;
        }
    }

    private static long? CountProcesses()
    {
        try
        {
            var all = Process.GetProcesses();
            foreach (var p in all)
            {
                p.Dispose();
            }
            return all.Length;
        }
        catch (Exception)
        {
            return null;
        }
    }

    private static double Kbps(long bytes, double secs) => Math.Round(bytes / 1024.0 / secs, 1);

    private static void Try(Action read)
    {
        try
        {
            read();
        }
        catch (Exception)
        {
            // this source doesn't exist on this runtime or OS: the column stays NULL
        }
    }
}
