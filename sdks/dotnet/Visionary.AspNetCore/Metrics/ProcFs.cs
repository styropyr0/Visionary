using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Linq;
using System.Text.RegularExpressions;

namespace Styropyr0.Visionary;

/// <summary>
/// Linux /proc readers for what the runtime doesn't expose. Every reader returns null when the
/// file is missing, unreadable or not in the expected shape; nothing here throws into the sampler.
/// </summary>
internal static class ProcFs
{
    /// <summary>
    /// Partitions and virtual devices whose I/O is already counted in a whole disk, so totals are
    /// not doubled (SPEC.md partition rule).
    /// </summary>
    public static readonly Regex Partition = new(
        @"^((sd|vd|xvd|hd)[a-z]+\d+|nvme\d+n\d+p\d+|mmcblk\d+p\d+|loop\d+|ram\d+|zram\d+|dm-\d+|sr\d+|fd\d+|md\d+)$",
        RegexOptions.Compiled | RegexOptions.CultureInvariant);

    private const long SectorBytes = 512;

    public static readonly bool Available = OperatingSystem.IsLinux() && File.Exists("/proc/stat");

    /// <summary>Aggregate CPU jiffies from the first line of /proc/stat.</summary>
    public sealed record Cpu(long User, long Nice, long System, long Idle, long Iowait, long Irq, long Softirq, long Steal)
    {
        public long Total => User + Nice + System + Idle + Iowait + Irq + Softirq + Steal;
    }

    public sealed record Stat(Cpu? Cpu, long? Ctxt, long? ProcsRunning, long? ProcsBlocked);

    /// <summary>Sums over whole disks; <see cref="BusyMs"/> is the I/O time per disk.</summary>
    public sealed record Disks(long ReadBytes, long WriteBytes, long Reads, long Writes, long IoTimeMs, Dictionary<string, long> BusyMs);

    /// <summary>Sums over all interfaces.</summary>
    public sealed record Net(long RxBytes, long TxBytes, long RxPackets, long TxPackets, long Errors, long Drops);

    public sealed record Tcp(long CurrEstab, long RetransSegs);

    /// <summary>/proc/meminfo values in kB.</summary>
    public sealed record Memory(long? TotalKb, long? AvailableKb, long? CachedKb, long? SwapTotalKb, long? SwapFreeKb);

    public sealed record ProcessIo(long ReadBytes, long WriteBytes);

    private static string[] Fields(string line) => line.Split((char[]?)null, StringSplitOptions.RemoveEmptyEntries);

    private static long L(string s) => long.Parse(s, NumberStyles.Integer, CultureInfo.InvariantCulture);

    private static T? Read<T>(string file, Func<string[], T?> parse) where T : class
    {
        if (!Available)
        {
            return null;
        }
        try
        {
            return parse(File.ReadAllLines(file));
        }
        catch (Exception)
        {
            return null;
        }
    }

    public static Stat? ReadStat() => Read("/proc/stat", lines =>
    {
        Cpu? cpu = null;
        long? ctxt = null, running = null, blocked = null;
        foreach (var line in lines)
        {
            var f = Fields(line);
            if (f.Length < 2)
            {
                continue;
            }
            switch (f[0])
            {
                case "cpu":
                    var v = new long[8];
                    for (var i = 0; i < v.Length && i + 1 < f.Length; i++)
                    {
                        v[i] = L(f[i + 1]);
                    }
                    cpu = new Cpu(v[0], v[1], v[2], v[3], v[4], v[5], v[6], v[7]);
                    break;
                case "ctxt":
                    ctxt = L(f[1]);
                    break;
                case "procs_running":
                    running = L(f[1]);
                    break;
                case "procs_blocked":
                    blocked = L(f[1]);
                    break;
            }
        }
        return new Stat(cpu, ctxt, running, blocked);
    });

    public static Memory? ReadMemory() => Read("/proc/meminfo", lines =>
    {
        var kb = new Dictionary<string, long>();
        foreach (var line in lines)
        {
            var colon = line.IndexOf(':');
            if (colon > 0)
            {
                var f = Fields(line[(colon + 1)..]);
                if (f.Length > 0)
                {
                    kb[line[..colon]] = L(f[0]);
                }
            }
        }
        long? Get(string k) => kb.TryGetValue(k, out var v) ? v : null;
        // Match `free` and gopsutil: reclaimable slab counts as cache.
        var cached = Get("Cached");
        if (cached is not null && Get("SReclaimable") is { } slab)
        {
            cached += slab;
        }
        return new Memory(Get("MemTotal"), Get("MemAvailable"), cached, Get("SwapTotal"), Get("SwapFree"));
    });

    public static Disks? ReadDisks() => Read("/proc/diskstats", lines =>
    {
        long readBytes = 0, writeBytes = 0, reads = 0, writes = 0, ioTime = 0;
        var busy = new Dictionary<string, long>();
        foreach (var line in lines)
        {
            var f = Fields(line);
            if (f.Length < 14 || Partition.IsMatch(f[2]))
            {
                continue;
            }
            reads += L(f[3]);
            readBytes += L(f[5]) * SectorBytes;
            ioTime += L(f[6]);
            writes += L(f[7]);
            writeBytes += L(f[9]) * SectorBytes;
            ioTime += L(f[10]);
            busy[f[2]] = L(f[12]);
        }
        return new Disks(readBytes, writeBytes, reads, writes, ioTime, busy);
    });

    public static Net? ReadNet() => Read("/proc/net/dev", lines =>
    {
        var sum = new long[6];
        var any = false;
        foreach (var line in lines)
        {
            var colon = line.IndexOf(':');
            if (colon < 0)
            {
                continue;
            }
            var f = Fields(line[(colon + 1)..]);
            if (f.Length < 12)
            {
                continue;
            }
            sum[0] += L(f[0]);
            sum[1] += L(f[8]);
            sum[2] += L(f[1]);
            sum[3] += L(f[9]);
            sum[4] += L(f[2]) + L(f[10]);
            sum[5] += L(f[3]) + L(f[11]);
            any = true;
        }
        return any ? new Net(sum[0], sum[1], sum[2], sum[3], sum[4], sum[5]) : null;
    });

    /// <summary>The Tcp header and value lines of /proc/net/snmp.</summary>
    public static Tcp? ReadTcp() => Read("/proc/net/snmp", lines =>
    {
        var tcp = lines.Where(l => l.StartsWith("Tcp:", StringComparison.Ordinal)).Select(Fields).ToList();
        if (tcp.Count < 2)
        {
            return null;
        }
        var header = tcp[0];
        var values = tcp[1];
        var estab = Array.IndexOf(header, "CurrEstab");
        var retrans = Array.IndexOf(header, "RetransSegs");
        if (estab < 1 || retrans < 1 || estab >= values.Length || retrans >= values.Length)
        {
            return null;
        }
        return new Tcp(L(values[estab]), L(values[retrans]));
    });

    public static ProcessIo? ReadProcessIo() => Read("/proc/self/io", lines =>
    {
        long? read = null, write = null;
        foreach (var line in lines)
        {
            if (line.StartsWith("read_bytes:", StringComparison.Ordinal))
            {
                read = L(line[11..].Trim());
            }
            else if (line.StartsWith("write_bytes:", StringComparison.Ordinal))
            {
                write = L(line[12..].Trim());
            }
        }
        return read is null || write is null ? null : new ProcessIo(read.Value, write.Value);
    });

    /// <summary>The 1, 5 and 15 minute load averages.</summary>
    public static double[]? ReadLoad() => Read("/proc/loadavg", lines =>
    {
        var f = Fields(lines[0]);
        return f.Take(3).Select(s => double.Parse(s, CultureInfo.InvariantCulture)).ToArray();
    });

    /// <summary>The soft limit on open files, from /proc/self/limits; null when unlimited.</summary>
    public static long? MaxOpenFiles()
    {
        var limit = Read("/proc/self/limits", lines =>
        {
            var line = lines.FirstOrDefault(l => l.StartsWith("Max open files", StringComparison.Ordinal));
            var f = line is null ? null : Fields(line["Max open files".Length..]);
            return f is { Length: > 0 } && long.TryParse(f[0], NumberStyles.Integer, CultureInfo.InvariantCulture, out var n) ? (object)n : null;
        });
        return limit is long n && n > 0 && n < 1L << 40 ? n : null;
    }

    public static long? OpenFds()
    {
        if (!Available)
        {
            return null;
        }
        try
        {
            return Directory.EnumerateFileSystemEntries("/proc/self/fd").LongCount();
        }
        catch (Exception)
        {
            return null;
        }
    }

    /// <summary>Number of processes: the numeric entries of /proc.</summary>
    public static long? ProcessCount()
    {
        if (!Available)
        {
            return null;
        }
        try
        {
            return Directory.EnumerateDirectories("/proc").LongCount(d => Path.GetFileName(d).All(char.IsAsciiDigit));
        }
        catch (Exception)
        {
            return null;
        }
    }
}
