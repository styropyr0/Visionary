using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using Xunit;

namespace Styropyr0.Visionary.Tests;

public sealed class HostStatsTests : IDisposable
{
    // The fixtures of sdks/go/hoststats/testdata/linux
    private const string ProcPressureCpu = "some avg10=12.50 avg60=8.25 avg300=3.10 total=123456789\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n";
    private const string ProcPressureMemory = "some avg10=4.20 avg60=2.10 avg300=0.80 total=9876543\nfull avg10=1.50 avg60=0.75 avg300=0.20 total=1234567\n";
    private const string ProcPressureIo = "some avg10=30.00 avg60=22.40 avg300=10.00 total=55555555\nfull avg10=18.75 avg60=12.00 avg300=5.00 total=44444444\n";
    private const string CgroupCpuPressure = "some avg10=55.10 avg60=40.00 avg300=20.00 total=1000\nfull avg10=50.00 avg60=35.00 avg300=18.00 total=900\n";
    private const string CgroupMemoryPressure = "some avg10=0.00 avg60=0.00 avg300=0.00 total=0\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n";
    private const string CgroupIoPressure = "some avg10=1.00 avg60=0.50 avg300=0.10 total=10\nfull avg10=0.90 avg60=0.40 avg300=0.10 total=9\n";

    private const string VmStat = """
        nr_free_pages 1234567
        nr_dirty 512
        pgpgin 104857600
        pgpgout 209715200
        pswpin 1000
        pswpout 2000
        pgfault 50000000
        pgmajfault 20000
        pgsteal_kswapd 30000
        pgsteal_direct 5000
        pgsteal_khugepaged 0
        pgsteal_anon 1000
        pgsteal_file 34000
        pgscan_kswapd 60000
        pgscan_direct 9000
        pgscan_khugepaged 0
        pgscan_direct_throttle 7
        pgscan_anon 2000
        pgscan_file 67000
        oom_kill 3

        """;

    private const string MemInfo = """
        MemTotal:       16303300 kB
        MemFree:         1048576 kB
        MemAvailable:    8388608 kB
        Buffers:          204800 kB
        Cached:          4194304 kB
        SwapCached:            0 kB
        Dirty:             10240 kB
        Writeback:          2048 kB
        Shmem:            524288 kB
        Slab:             819200 kB
        SReclaimable:     614400 kB
        CommitLimit:    12345678 kB
        Committed_AS:   20971520 kB
        HugePages_Total:       0

        """;

    private readonly string _root = Path.Combine(Path.GetTempPath(), $"visionary-hoststats-{Guid.NewGuid():N}");

    public HostStatsTests()
    {
        Put("proc/pressure/cpu", ProcPressureCpu);
        Put("proc/pressure/memory", ProcPressureMemory);
        Put("proc/pressure/io", ProcPressureIo);
        Put("proc/vmstat", VmStat);
        Put("proc/meminfo", MemInfo);
        Put("cgroup/cpu.pressure", CgroupCpuPressure);
        Put("cgroup/memory.pressure", CgroupMemoryPressure);
        Put("cgroup/io.pressure", CgroupIoPressure);
    }

    public void Dispose()
    {
        try
        {
            Directory.Delete(_root, true);
        }
        catch (IOException)
        {
        }
    }

    private string P(string rel) => Path.Combine(new[] { _root }.Concat(rel.Split('/')).ToArray());

    private void Put(string rel, string text)
    {
        Directory.CreateDirectory(Path.GetDirectoryName(P(rel))!);
        File.WriteAllText(P(rel), text);
    }

    private void Replace(string rel, string old, string now)
    {
        var text = File.ReadAllText(P(rel));
        Assert.Contains(old, text);
        File.WriteAllText(P(rel), text.Replace(old, now));
    }

    [Fact]
    public void Parsers()
    {
        var p = HostStats.ParsePressure(ProcPressureMemory);
        Assert.Equal(new HostStats.Pressure(4.2, 2.1, 1.5), p);
        Assert.Null(HostStats.ParsePressure("garbage\n"));

        Assert.Equal(new HostStats.VmStat(50000000, 20000, 1000, 2000, 69000, 35000, 3), HostStats.ParseVmStat(VmStat));
        Assert.Equal(new HostStats.MemInfo(10240, 2048, 819200, 524288, 20971520, 12345678), HostStats.ParseMemInfo(MemInfo));
        Assert.Null(HostStats.ParseMemInfo("Dirty: 1 kB\n"));

        Assert.Equal((900UL, 12UL), HostStats.ParseCgroupMemStat("anon 123\npgfault 900\npgmajfault 12\n"));
        Assert.Equal((800UL, 7UL), HostStats.ParseCgroupMemStat("cache 1\npgfault 5\ntotal_pgfault 800\ntotal_pgmajfault 7\n"));
        Assert.Equal((14UL, 3UL), HostStats.ParseCgroupMemEvents("low 0\nhigh 14\nmax 3\noom 1\noom_kill 1\n"));
        Assert.Null(HostStats.ParseCgroupMemEvents("low 0\nhigh 14\n"));
        Assert.Equal((1024UL, 256UL), HostStats.ParseCgroupIo("8:0 rbytes=1000 wbytes=200 rios=3 wios=4 dbytes=0 dios=0\n253:1 rbytes=24 wbytes=56 rios=1 wios=1 dbytes=0 dios=0\n"));
        Assert.Equal((4096UL, 512UL), HostStats.ParseCgroupIo("8:0 Read 4096\n8:0 Write 512\n8:0 Sync 0\n8:0 Total 4608\nTotal 4608\n"));
        Assert.Null(HostStats.ParseCgroupIo(""));
    }

    [Fact]
    public void HostColumns()
    {
        var s = new HostStats(true, P("proc"), false, null);
        s.Prime();
        Replace("proc/vmstat", "pgfault 50000000", "pgfault 50030000");
        Replace("proc/vmstat", "pgmajfault 20000", "pgmajfault 20150");
        Replace("proc/vmstat", "pswpin 1000", "pswpin 1300");
        Replace("proc/vmstat", "pgscan_kswapd 60000", "pgscan_kswapd 61500");
        Replace("proc/vmstat", "oom_kill 3", "oom_kill 4");

        var v = new Dictionary<string, object?>();
        s.Sample(v, 15);
        var want = new Dictionary<string, object>
        {
            ["psi_cpu_some_avg10"] = 12.5, ["psi_cpu_some_avg60"] = 8.25, ["psi_mem_some_avg10"] = 4.2, ["psi_mem_some_avg60"] = 2.1, ["psi_mem_full_avg10"] = 1.5,
            ["psi_io_some_avg10"] = 30.0, ["psi_io_some_avg60"] = 22.4, ["psi_io_full_avg10"] = 18.75,
            ["minor_faults_ps"] = 1990.0, ["major_faults_ps"] = 10.0, ["swap_in_ps"] = 20.0, ["swap_out_ps"] = 0.0, ["pages_scanned_ps"] = 100.0, ["pages_stolen_ps"] = 0.0,
            ["host_oom_kills"] = 1L,
            ["mem_dirty_mb"] = 10.0, ["mem_writeback_mb"] = 2.0, ["mem_slab_mb"] = 800.0, ["mem_shmem_mb"] = 512.0, ["mem_committed_mb"] = 20480.0, ["mem_commit_limit_mb"] = 12056.3,
        };
        foreach (var (k, w) in want)
        {
            Assert.True(v.TryGetValue(k, out var got), k);
            Assert.Equal(w, got);
        }
        Assert.Equal(want.Count, v.Count);
    }

    [Fact]
    public void ContainerPressureComesFromTheCgroup()
    {
        var v = new Dictionary<string, object?>();
        var s = new HostStats(true, P("proc"), true, P("cgroup"));
        s.Prime();
        s.Sample(v, 15);
        Assert.Equal(55.1, v["psi_cpu_some_avg10"]);
        Assert.Equal(0.0, v["psi_mem_some_avg10"]);
        Assert.Equal(0.9, v["psi_io_full_avg10"]);

        v.Clear();
        s = new HostStats(true, P("proc"), true, null);
        s.Prime();
        s.Sample(v, 15);
        Assert.False(v.ContainsKey("psi_cpu_some_avg10"));
    }

    [Fact]
    public void OffLinuxReadsNothing()
    {
        var v = new Dictionary<string, object?>();
        var s = new HostStats(false, P("proc"), false, null);
        s.Prime();
        s.Sample(v, 15);
        Assert.Empty(v);
    }

    [Fact]
    public void CgroupMemoryEventsAndFaults()
    {
        Put("proc/self/cgroup", "0::/app\n");
        Put("sys/fs/cgroup/app/cpu.stat", "usage_usec 1000\nnr_periods 0\nnr_throttled 0\n");
        Put("sys/fs/cgroup/app/cpu.max", "max 100000\n");
        Put("sys/fs/cgroup/app/memory.max", "max\n");
        Put("sys/fs/cgroup/app/memory.current", "1048576\n");
        Put("sys/fs/cgroup/app/memory.stat", "anon 123\npgfault 900\npgmajfault 12\n");
        Put("sys/fs/cgroup/app/memory.events", "low 0\nhigh 14\nmax 3\noom 1\noom_kill 1\n");
        Put("sys/fs/cgroup/app/pids.current", "12\n");
        Put("sys/fs/cgroup/app/pids.max", "max\n");
        Put("sys/fs/cgroup/app/io.stat", "8:0 rbytes=1000 wbytes=200 rios=3 wios=4\n");
        Put(".dockerenv", "");

        var c = CgroupReader.Open(_root)!;
        Assert.True(c.IsContainer);
        Assert.Equal(Path.GetFullPath(P("sys/fs/cgroup/app")), Path.GetFullPath(c.V2Dir!));
        c.Prime();
        Replace("sys/fs/cgroup/app/memory.stat", "pgfault 900", "pgfault 1900");
        Replace("sys/fs/cgroup/app/memory.stat", "pgmajfault 12", "pgmajfault 22");
        Replace("sys/fs/cgroup/app/memory.events", "high 14", "high 20");
        Replace("sys/fs/cgroup/app/io.stat", "rbytes=1000 wbytes=200", "rbytes=11240 wbytes=2248");
        System.Threading.Thread.Sleep(20);

        var v = new Dictionary<string, object?>();
        c.Sample(v);
        Assert.Equal(6L, v["container_mem_high_events"]);
        Assert.Equal(0L, v["container_mem_max_events"]);
        Assert.Equal(12L, v["container_pids"]);
        Assert.False(v.ContainsKey("container_pids_max"));
        Assert.True((double)v["container_read_kbps"]! > 0);
        Assert.True((double)v["container_write_kbps"]! > 0);
        Assert.True((double)v["container_major_faults_ps"]! > 0);
        Assert.True((double)v["container_minor_faults_ps"]! > (double)v["container_major_faults_ps"]!);
    }

    [Fact]
    public void D1RowsSplitPastOneHundredParams()
    {
        var row = Enumerable.Range(0, Schema.SystemColumns.Length).Select(i => (object?)i).ToArray();
        Assert.True(row.Length > SystemSampler.D1MaxParams);

        var plain = SystemSampler.RowStmts("INSERT", false, row).ToList();
        Assert.Single(plain);

        var d1 = SystemSampler.RowStmts("INSERT", true, row).ToList();
        Assert.Equal(2, d1.Count);
        Assert.Equal(SystemSampler.D1MaxParams, d1[0].Args.Length);
        Assert.Equal(SystemSampler.D1MaxParams, d1[0].Sql.Count(c => c == '?'));
        Assert.Equal(row.Length - SystemSampler.D1MaxParams, d1[1].Args.Length);
        Assert.Equal(d1[1].Args.Length, d1[1].Sql.Count(c => c == '?'));
        Assert.EndsWith("WHERE id = last_insert_rowid()", d1[1].Sql);
        Assert.Equal(SystemSampler.D1MaxParams, d1[1].Args[0]);
    }
}
