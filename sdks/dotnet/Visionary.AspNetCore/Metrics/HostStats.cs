using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;

namespace Styropyr0.Visionary;

/// <summary>
/// Pressure stall information and the /proc/vmstat and /proc/meminfo columns on Linux, with
/// sdks/go/hoststats as the reference
/// </summary>
internal sealed class HostStats
{
    public sealed record Pressure(double SomeAvg10, double SomeAvg60, double? FullAvg10);

    public sealed record VmStat(ulong PgFault, ulong PgMajFault, ulong PswpIn, ulong PswpOut, ulong PgScan, ulong PgSteal, ulong? OomKill);

    public sealed record MemInfo(ulong Dirty, ulong Writeback, ulong Slab, ulong Shmem, ulong Committed, ulong CommitLimit);

    private readonly bool _linux;
    private readonly string _proc;
    private readonly bool _container;
    private readonly string? _cgroupDir;
    private VmStat? _vm;

    /// <summary>
    /// In a container pressure comes from the cgroup v2 directory, since /proc/pressure is the whole
    /// machine's, and is not read at all without one
    /// </summary>
    public HostStats(bool linux, string proc, bool container, string? cgroupDir)
    {
        _linux = linux;
        _proc = proc;
        _container = container;
        _cgroupDir = cgroupDir;
    }

    public static HostStats ForThisProcess(CgroupReader? cgroup) =>
        new(OperatingSystem.IsLinux(), "/proc", cgroup?.IsContainer ?? false, cgroup?.V2Dir);

    public void Prime()
    {
        if (_linux)
        {
            _vm = ParseVmStat(Read(Path.Combine(_proc, "vmstat")));
        }
    }

    public void Sample(Dictionary<string, object?> v, double secs)
    {
        if (!_linux || secs <= 0)
        {
            return;
        }
        SamplePressure(v);
        SampleVm(v, secs);
    }

    private void SamplePressure(Dictionary<string, object?> v)
    {
        var dir = Path.Combine(_proc, "pressure");
        string[] names = { "cpu", "memory", "io" };
        if (_container)
        {
            if (_cgroupDir is null)
            {
                return;
            }
            dir = _cgroupDir;
            names = new[] { "cpu.pressure", "memory.pressure", "io.pressure" };
        }
        if (ParsePressure(Read(Path.Combine(dir, names[0]))) is { } cpu)
        {
            (v["psi_cpu_some_avg10"], v["psi_cpu_some_avg60"]) = (cpu.SomeAvg10, cpu.SomeAvg60);
        }
        if (ParsePressure(Read(Path.Combine(dir, names[1]))) is { } mem)
        {
            (v["psi_mem_some_avg10"], v["psi_mem_some_avg60"]) = (mem.SomeAvg10, mem.SomeAvg60);
            if (mem.FullAvg10 is { } full)
            {
                v["psi_mem_full_avg10"] = full;
            }
        }
        if (ParsePressure(Read(Path.Combine(dir, names[2]))) is { } io)
        {
            (v["psi_io_some_avg10"], v["psi_io_some_avg60"]) = (io.SomeAvg10, io.SomeAvg60);
            if (io.FullAvg10 is { } full)
            {
                v["psi_io_full_avg10"] = full;
            }
        }
    }

    private void SampleVm(Dictionary<string, object?> v, double secs)
    {
        var now = ParseVmStat(Read(Path.Combine(_proc, "vmstat")));
        if (now is not null && _vm is { } p)
        {
            void Rate(string col, ulong a, ulong b)
            {
                if (a >= b)
                {
                    v[col] = Math.Round((a - b) / secs, 1);
                }
            }
            if (now.PgFault >= now.PgMajFault && p.PgFault >= p.PgMajFault)
            {
                Rate("minor_faults_ps", now.PgFault - now.PgMajFault, p.PgFault - p.PgMajFault);
            }
            Rate("major_faults_ps", now.PgMajFault, p.PgMajFault);
            Rate("swap_in_ps", now.PswpIn, p.PswpIn);
            Rate("swap_out_ps", now.PswpOut, p.PswpOut);
            Rate("pages_scanned_ps", now.PgScan, p.PgScan);
            Rate("pages_stolen_ps", now.PgSteal, p.PgSteal);
            if (now.OomKill is { } k && p.OomKill is { } pk && k >= pk)
            {
                v["host_oom_kills"] = (long)(k - pk);
            }
        }
        _vm = now;
        if (ParseMemInfo(Read(Path.Combine(_proc, "meminfo"))) is { } m)
        {
            static double Mb(ulong kb) => Math.Round(kb / 1024.0, 1);
            (v["mem_dirty_mb"], v["mem_writeback_mb"]) = (Mb(m.Dirty), Mb(m.Writeback));
            (v["mem_slab_mb"], v["mem_shmem_mb"]) = (Mb(m.Slab), Mb(m.Shmem));
            (v["mem_committed_mb"], v["mem_commit_limit_mb"]) = (Mb(m.Committed), Mb(m.CommitLimit));
        }
    }

    /// <summary>Reads "some avg10=0.12 avg60=..." and the optional "full" line</summary>
    public static Pressure? ParsePressure(string? text)
    {
        Pressure? p = null;
        double? full = null;
        foreach (var line in Lines(text))
        {
            var space = line.IndexOf(' ');
            if (space < 0 || !Field(line, "avg10=", out var a10) || !Field(line, "avg60=", out var a60))
            {
                continue;
            }
            switch (line[..space])
            {
                case "some":
                    p = new Pressure(a10, a60, null);
                    break;
                case "full":
                    full = a10;
                    break;
            }
        }
        return p is null ? null : p with { FullAvg10 = full };
    }

    /// <summary>
    /// Sums reclaim over kswapd, direct and khugepaged scans and the per-zone names older kernels
    /// use, but not pgscan_anon or pgscan_file, which split the same pages by type
    /// </summary>
    public static VmStat? ParseVmStat(string? text)
    {
        ulong fault = 0, major = 0, swpIn = 0, swpOut = 0, scan = 0, steal = 0;
        ulong? oom = null;
        var ok = false;
        foreach (var (k, n) in Kv(text, ' '))
        {
            switch (k)
            {
                case "pgfault":
                    (fault, ok) = (n, true);
                    break;
                case "pgmajfault":
                    major = n;
                    break;
                case "pswpin":
                    swpIn = n;
                    break;
                case "pswpout":
                    swpOut = n;
                    break;
                case "oom_kill":
                    oom = n;
                    break;
                case "pgscan_direct_throttle":
                    break;
                default:
                    if (HasAnyPrefix(k, "pgscan_kswapd", "pgscan_direct", "pgscan_khugepaged"))
                    {
                        scan += n;
                    }
                    else if (HasAnyPrefix(k, "pgsteal_kswapd", "pgsteal_direct", "pgsteal_khugepaged"))
                    {
                        steal += n;
                    }
                    break;
            }
        }
        return ok ? new VmStat(fault, major, swpIn, swpOut, scan, steal, oom) : null;
    }

    /// <summary>Reads "Dirty:   1234 kB" lines, null without Dirty, Committed_AS or CommitLimit</summary>
    public static MemInfo? ParseMemInfo(string? text)
    {
        var kb = new Dictionary<string, ulong>();
        foreach (var (k, n) in Kv(text, ':'))
        {
            kb[k] = n;
        }
        if (!kb.TryGetValue("Dirty", out var dirty) || !kb.TryGetValue("Committed_AS", out var committed) || !kb.TryGetValue("CommitLimit", out var limit))
        {
            return null;
        }
        return new MemInfo(dirty, kb.GetValueOrDefault("Writeback"), kb.GetValueOrDefault("Slab"), kb.GetValueOrDefault("Shmem"), committed, limit);
    }

    /// <summary>The fault counters of a cgroup's memory.stat, v2 (pgfault) or v1 (total_pgfault)</summary>
    public static (ulong Faults, ulong Major)? ParseCgroupMemStat(string? text)
    {
        ulong faults = 0, major = 0;
        var ok = false;
        foreach (var (k, n) in Kv(text, ' '))
        {
            switch (k)
            {
                case "pgfault" or "total_pgfault":
                    (faults, ok) = (n, true);
                    break;
                case "pgmajfault" or "total_pgmajfault":
                    major = n;
                    break;
            }
        }
        return ok ? (faults, major) : null;
    }

    /// <summary>high and max from a v2 memory.events</summary>
    public static (ulong High, ulong Max)? ParseCgroupMemEvents(string? text)
    {
        ulong? high = null, max = null;
        foreach (var (k, n) in Kv(text, ' '))
        {
            switch (k)
            {
                case "high":
                    high = n;
                    break;
                case "max":
                    max = n;
                    break;
            }
        }
        return high is { } h && max is { } m ? (h, m) : null;
    }

    internal static string? Read(string file)
    {
        try
        {
            return File.ReadAllText(file);
        }
        catch (Exception)
        {
            return null;
        }
    }

    private static IEnumerable<string> Lines(string? text) =>
        (text ?? "").Split('\n', StringSplitOptions.RemoveEmptyEntries | StringSplitOptions.TrimEntries);

    private static bool Field(string line, string key, out double value)
    {
        value = 0;
        var i = line.IndexOf(key, StringComparison.Ordinal);
        if (i < 0)
        {
            return false;
        }
        var s = line[(i + key.Length)..];
        var end = s.IndexOf(' ');
        return double.TryParse(end < 0 ? s : s[..end], NumberStyles.Float, CultureInfo.InvariantCulture, out value);
    }

    /// <summary>"key&lt;sep&gt; value [unit]" lines with an unsigned value</summary>
    private static IEnumerable<(string Key, ulong Value)> Kv(string? text, char sep)
    {
        foreach (var line in Lines(text))
        {
            var i = line.IndexOf(sep);
            if (i <= 0)
            {
                continue;
            }
            var s = line[(i + 1)..].Trim();
            var end = s.IndexOf(' ');
            if (ulong.TryParse(end < 0 ? s : s[..end], NumberStyles.None, CultureInfo.InvariantCulture, out var n))
            {
                yield return (line[..i], n);
            }
        }
    }

    private static bool HasAnyPrefix(string s, params string[] prefixes)
    {
        foreach (var p in prefixes)
        {
            if (s.StartsWith(p, StringComparison.Ordinal))
            {
                return true;
            }
        }
        return false;
    }
}
