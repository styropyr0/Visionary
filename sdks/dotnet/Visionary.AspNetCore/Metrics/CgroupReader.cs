using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Globalization;
using System.IO;
using System.Linq;

namespace Styropyr0.Visionary;

/// <summary>
/// The CPU and memory limits and use of the process's own cgroup (Linux, v2 or v1): in a
/// container these are the numbers that matter, since the host columns describe the whole machine
/// underneath. The container_* columns are filled only in a container, or when the cgroup sets a
/// CPU or memory limit (a systemd unit with MemoryMax=, say). sdks/go/cgroup.go is the reference.
/// </summary>
internal sealed class CgroupReader
{
    private static readonly string[] ContainerMarkers = { "docker", "kubepods", "containerd", "libpod", "lxc" };

    private readonly bool _v2;
    private readonly string? _cpuDir; // v2: the cgroup; v1: the cpu controller's directory
    private readonly string? _cpuAcct; // v1: the cpuacct controller's directory
    private readonly string? _memDir;
    private readonly bool _inContainer;

    // Previous readings, touched only on the sampler loop.
    private long _last;
    private ulong _usage; // CPU time, ns
    private ulong _periods;
    private ulong _throttled;
    private ulong _ooms;
    private bool _primed;

    private CgroupReader(bool v2, string? cpuDir, string? cpuAcct, string? memDir, bool inContainer)
    {
        _v2 = v2;
        _cpuDir = cpuDir;
        _cpuAcct = cpuAcct;
        _memDir = memDir;
        _inContainer = inContainer;
    }

    /// <summary>The process's cgroup; null off Linux or when there is none to read.</summary>
    public static CgroupReader? Open() => OperatingSystem.IsLinux() ? Open("/") : null;

    /// <summary>
    /// Reads proc/self/cgroup and sys/fs/cgroup under <paramref name="root"/>, which is "/" except
    /// in tests.
    /// </summary>
    internal static CgroupReader? Open(string root)
    {
        string text;
        try
        {
            text = File.ReadAllText(Join(root, "proc/self/cgroup"));
        }
        catch (Exception)
        {
            return null;
        }
        bool v2 = false;
        string? cpuDir = null, cpuAcct = null, memDir = null;
        foreach (var line in text.Trim().Split('\n'))
        {
            var f = line.TrimEnd('\r').Split(':', 3);
            if (f.Length != 3)
            {
                continue;
            }
            var (ctrls, path) = (f[1], f[2]);
            if (f[0] == "0" && ctrls.Length == 0)
            {
                var dir = CgroupDir(Join(root, "sys/fs/cgroup"), path);
                if (File.Exists(Join(dir, "cpu.stat")))
                {
                    (v2, cpuDir, memDir) = (true, dir, dir);
                    break;
                }
            }
            else if (HasController(ctrls, "memory"))
            {
                memDir = CgroupDir(Join(root, "sys/fs/cgroup/memory"), path);
            }
            else if (HasController(ctrls, "cpu") || HasController(ctrls, "cpuacct"))
            {
                var dir = CgroupDir(Join(root, "sys/fs/cgroup", ctrls), path);
                if (HasController(ctrls, "cpu"))
                {
                    cpuDir = dir;
                }
                if (HasController(ctrls, "cpuacct"))
                {
                    cpuAcct = dir;
                }
            }
        }
        if (cpuDir is null && memDir is null)
        {
            return null;
        }
        return new CgroupReader(v2, cpuDir, cpuAcct, memDir, InContainer(root, text));
    }

    /// <summary>Takes the baseline the first sample's deltas start from.</summary>
    public void Prime()
    {
        (_usage, _periods, _throttled, _ooms) = Counters();
        _last = Stopwatch.GetTimestamp();
        _primed = true;
    }

    /// <summary>Fills the container_* columns; all six stay NULL outside a container without limits.</summary>
    public void Sample(Dictionary<string, object?> v)
    {
        var (cpus, memLimit) = Limits();
        if (!_inContainer && cpus == 0 && memLimit == 0)
        {
            return;
        }
        var now = Stopwatch.GetTimestamp();
        var (usage, periods, throttled, ooms) = Counters();
        var secs = Stopwatch.GetElapsedTime(_last, now).TotalSeconds;
        if (_primed && secs > 0 && usage >= _usage)
        {
            v["container_cpu_cores"] = Math.Round((usage - _usage) / 1e9 / secs, 3);
            if (periods > _periods && throttled >= _throttled)
            {
                v["container_cpu_throttled_percent"] = Math.Round(100.0 * (throttled - _throttled) / (periods - _periods), 1);
            }
            else if (cpus > 0)
            {
                v["container_cpu_throttled_percent"] = 0.0;
            }
            if (ooms >= _ooms)
            {
                v["container_oom_kills"] = (long)(ooms - _ooms);
            }
        }
        (_usage, _periods, _throttled, _ooms, _last, _primed) = (usage, periods, throttled, ooms, now, true);
        if (cpus > 0)
        {
            v["container_cpu_limit"] = Math.Round(cpus, 2);
        }
        if (MemUsed() is { } used)
        {
            v["container_mem_used_mb"] = Math.Round(used / HostSampler.Mb, 1);
        }
        if (memLimit > 0)
        {
            v["container_mem_limit_mb"] = Math.Round(memLimit / HostSampler.Mb, 1);
        }
    }

    /// <summary>The CPU limit in cores and the memory limit in bytes, 0 when unlimited.</summary>
    private (double Cpus, ulong Mem) Limits()
    {
        double cpus = 0;
        ulong mem = 0;
        if (_v2)
        {
            var f = Fields(ReadText(Join(_cpuDir!, "cpu.max")));
            if (f.Length == 2 && f[0] != "max" && TryDouble(f[0], out var q) && TryDouble(f[1], out var p) && p > 0)
            {
                cpus = q / p;
            }
            // "max" doesn't parse: 0.
            return (cpus, ReadUInt(Join(_memDir!, "memory.max")) ?? 0);
        }
        if (_cpuDir is not null
            && TryDouble(ReadText(Join(_cpuDir, "cpu.cfs_quota_us"))?.Trim(), out var quota) && quota > 0
            && ReadUInt(Join(_cpuDir, "cpu.cfs_period_us")) is { } period && period > 0)
        {
            cpus = quota / period;
        }
        // v1 writes a page-rounded maximum int64 for no limit.
        if (_memDir is not null && ReadUInt(Join(_memDir, "memory.limit_in_bytes")) is { } m && m < 1UL << 62)
        {
            mem = m;
        }
        return (cpus, mem);
    }

    /// <summary>The cumulative CPU time (ns), CFS periods, throttled periods and OOM kills.</summary>
    private (ulong Usage, ulong Periods, ulong Throttled, ulong Ooms) Counters()
    {
        if (_v2)
        {
            var st = ReadKv(Join(_cpuDir!, "cpu.stat"));
            var ev = ReadKv(Join(_memDir!, "memory.events"));
            return (st.GetValueOrDefault("usage_usec") * 1000, st.GetValueOrDefault("nr_periods"),
                st.GetValueOrDefault("nr_throttled"), ev.GetValueOrDefault("oom_kill"));
        }
        ulong usage = 0, periods = 0, throttled = 0, ooms = 0;
        if (_cpuAcct is not null)
        {
            usage = ReadUInt(Join(_cpuAcct, "cpuacct.usage")) ?? 0;
        }
        if (_cpuDir is not null)
        {
            var st = ReadKv(Join(_cpuDir, "cpu.stat"));
            (periods, throttled) = (st.GetValueOrDefault("nr_periods"), st.GetValueOrDefault("nr_throttled"));
        }
        if (_memDir is not null)
        {
            ooms = ReadKv(Join(_memDir, "memory.oom_control")).GetValueOrDefault("oom_kill");
        }
        return (usage, periods, throttled, ooms);
    }

    /// <summary>Memory in use without the inactive page cache, as docker stats counts it.</summary>
    private ulong? MemUsed()
    {
        if (_memDir is null)
        {
            return null;
        }
        var (file, statKey) = _v2 ? ("memory.current", "inactive_file") : ("memory.usage_in_bytes", "total_inactive_file");
        if (ReadUInt(Join(_memDir, file)) is not { } used)
        {
            return null;
        }
        if (ReadKv(Join(_memDir, "memory.stat")).TryGetValue(statKey, out var inactive) && inactive < used)
        {
            used -= inactive;
        }
        return used;
    }

    /// <summary>
    /// The cgroup's directory under a controller's mount. Inside a container's cgroup namespace the
    /// path is "/", and with a private mount the host path does not exist below it, so the mount
    /// itself is the cgroup.
    /// </summary>
    private static string CgroupDir(string mount, string path)
    {
        var dir = Join(mount, path);
        return Directory.Exists(dir) ? dir : mount;
    }

    private static bool HasController(string list, string name) => list.Split(',').Contains(name);

    /// <summary>
    /// Tells a container from a process on the host by the runtimes' marker files, the cgroup path
    /// and Kubernetes' environment.
    /// </summary>
    private static bool InContainer(string root, string cgroups) =>
        File.Exists(Join(root, ".dockerenv"))
        || File.Exists(Join(root, "run/.containerenv"))
        || !string.IsNullOrEmpty(Environment.GetEnvironmentVariable("KUBERNETES_SERVICE_HOST"))
        || ContainerMarkers.Any(k => cgroups.Contains(k, StringComparison.Ordinal));

    /// <summary>Joins cgroup paths, which start with "/", under a directory.</summary>
    private static string Join(string dir, params string[] parts) =>
        Path.Combine(new[] { dir }.Concat(parts.Select(p => p.Trim('/')).Where(p => p.Length > 0)).ToArray());

    private static string? ReadText(string file)
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

    private static string[] Fields(string? s) => s?.Split((char[]?)null, StringSplitOptions.RemoveEmptyEntries) ?? Array.Empty<string>();

    private static bool TryDouble(string? s, out double d) =>
        double.TryParse(s, NumberStyles.Float, CultureInfo.InvariantCulture, out d);

    private static ulong? ReadUInt(string file) =>
        ulong.TryParse(ReadText(file)?.Trim(), NumberStyles.None, CultureInfo.InvariantCulture, out var n) ? n : null;

    /// <summary>Reads "key value" lines (cpu.stat, memory.stat, memory.events).</summary>
    private static Dictionary<string, ulong> ReadKv(string file)
    {
        var kv = new Dictionary<string, ulong>();
        foreach (var line in (ReadText(file) ?? "").Split('\n'))
        {
            var f = Fields(line);
            if (f.Length == 2 && ulong.TryParse(f[1], NumberStyles.None, CultureInfo.InvariantCulture, out var n))
            {
                kv[f[0]] = n;
            }
        }
        return kv;
    }
}
