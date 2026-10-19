using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Threading;
using System.Threading.Tasks;
using Microsoft.Extensions.Hosting;
using Microsoft.Extensions.Logging;

namespace Styropyr0.Visionary;

/// <summary>
/// Samples host, process and runtime metrics every interval into system_metrics, four rows per
/// write. Failed writes keep their rows (at most an hour's worth) for the next attempt.
/// </summary>
internal sealed class SystemSampler : BackgroundService
{
    private const int SamplesPerWrite = 4;

    private readonly VisionaryAgent _agent;
    private readonly string _insert;
    private readonly List<object?[]> _buffer = new();
    private readonly long _started = Stopwatch.GetTimestamp();
    private HostSampler? _host;
    private CgroupReader? _cgroup; // null off Linux and outside a cgroup
    private bool _schemaReady;

    // Previous readings, touched only on the sampler loop.
    private long _last;
    private long _requests;
    private long _errors;
    private int _gcCount;
    private TimeSpan _gcPause;
    private VisionaryDbStats? _db;

    public SystemSampler(VisionaryAgent agent)
    {
        _agent = agent;
        _insert = Schema.InsertSql("system_metrics", Schema.SystemColumns, agent.Store?.StringParams ?? false);
    }

    private TimeSpan Interval => _agent.Options.SystemInterval;

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        if (!_agent.Enabled || !_agent.Options.SystemMetrics || Interval <= TimeSpan.Zero)
        {
            return;
        }
        Suppress.Enter();
        await Task.Yield();
        _host = new HostSampler(_agent.Options.DiskPath);
        Prime();
        _agent.Logger.LogInformation("visionary: recording system metrics every {Interval} as {Instance}", Interval, _agent.Instance);
        await EnsureSchemaAsync(stoppingToken).ConfigureAwait(false);
        var retention = Retention.RunAsync(_agent, _agent.Options.SystemRetentionDays > 0
            ? new() { ("system_metrics", _agent.Options.SystemRetentionDays) }
            : new(), stoppingToken);

        using var timer = new PeriodicTimer(Interval);
        try
        {
            while (await timer.WaitForNextTickAsync(stoppingToken).ConfigureAwait(false))
            {
                try
                {
                    _buffer.Add(Sample());
                }
                catch (Exception e)
                {
                    _agent.Logger.LogDebug(e, "visionary: system sample failed");
                }
                if (_buffer.Count >= SamplesPerWrite && _buffer.Count % SamplesPerWrite == 0)
                {
                    await FlushAsync(TimeSpan.FromSeconds(20)).ConfigureAwait(false);
                }
            }
        }
        catch (OperationCanceledException)
        {
            // stopping
        }
        await FlushAsync(TimeSpan.FromSeconds(10)).ConfigureAwait(false);
        try
        {
            await retention.ConfigureAwait(false);
        }
        catch (OperationCanceledException)
        {
            // stopped with the host
        }
    }

    private async Task EnsureSchemaAsync(CancellationToken ct)
    {
        if (_schemaReady)
        {
            return;
        }
        using var timeout = CancellationTokenSource.CreateLinkedTokenSource(ct);
        timeout.CancelAfter(TimeSpan.FromSeconds(20));
        try
        {
            await _agent.Store!.CreateAsync("system", "system_metrics", Schema.AddedSystemColumns, timeout.Token).ConfigureAwait(false);
            _schemaReady = true;
        }
        catch (Exception e) when (!ct.IsCancellationRequested)
        {
            _agent.Logger.LogError("visionary: create system_metrics failed (retrying on the next write): {Error}", e.Message);
        }
    }

    private async Task FlushAsync(TimeSpan limit)
    {
        if (_buffer.Count == 0)
        {
            return;
        }
        using var timeout = new CancellationTokenSource(limit);
        try
        {
            await EnsureSchemaAsync(timeout.Token).ConfigureAwait(false);
            var stmts = _buffer.ConvertAll(row => new Stmt(_insert, row));
            await _agent.Store!.BatchAsync(stmts, timeout.Token).ConfigureAwait(false);
            _buffer.Clear();
        }
        catch (Exception e)
        {
            _agent.Logger.LogWarning("visionary: system metrics write failed ({Rows} rows kept): {Error}", _buffer.Count, e.Message);
            // Keep at most an hour of samples while the database is unreachable.
            var keep = (int)Math.Max(1, TimeSpan.FromHours(1) / Interval);
            if (_buffer.Count > keep)
            {
                _buffer.RemoveRange(0, _buffer.Count - keep);
            }
        }
    }

    /// <summary>Baseline readings so the first row has real deltas.</summary>
    private void Prime()
    {
        _last = Stopwatch.GetTimestamp();
        _requests = Interlocked.Read(ref _agent.Http.Requests);
        _errors = Interlocked.Read(ref _agent.Http.Errors);
        _gcCount = GC.CollectionCount(0);
        _gcPause = GC.GetTotalPauseDuration();
        _db = ReadDb();
        _host!.Prime();
        _cgroup = CgroupReader.Open();
        _cgroup?.Prime();
    }

    private object?[] Sample()
    {
        var now = Stopwatch.GetTimestamp();
        var secs = Stopwatch.GetElapsedTime(_last, now).TotalSeconds;
        _last = now;
        if (secs <= 0)
        {
            secs = Interval.TotalSeconds;
        }
        var v = new Dictionary<string, object?>(Schema.SystemColumns.Length)
        {
            ["created_at"] = RowBuilder.Timestamp(DateTime.UtcNow),
            ["instance"] = _agent.Instance,
            ["interval_s"] = Math.Round(secs, 2),
            ["service"] = _agent.Service,
            ["version"] = _agent.Version,
            ["sdk"] = VisionaryAgent.Sdk,
            ["runtime"] = _agent.Runtime,
        };

        // .NET's concurrency unit is the thread pool worker; OS threads are proc_threads.
        v["goroutines"] = (long)ThreadPool.ThreadCount;
        var gc = GC.GetGCMemoryInfo();
        v["heap_alloc_mb"] = Math.Round(GC.GetTotalMemory(false) / HostSampler.Mb, 1);
        if (gc.TotalCommittedBytes > 0)
        {
            // Known once the first collection has run.
            v["heap_inuse_mb"] = Math.Round(gc.TotalCommittedBytes / HostSampler.Mb, 1);
        }
        var gcCount = GC.CollectionCount(0);
        var gcPause = GC.GetTotalPauseDuration();
        var pauseMs = (gcPause - _gcPause).TotalMilliseconds;
        v["gc_count"] = (long)(gcCount - _gcCount);
        v["gc_pause_ms"] = Math.Round(pauseMs, 2);
        v["gc_cpu_percent"] = Math.Round(pauseMs / (secs * 1000) * 100, 2);
        _gcCount = gcCount;
        _gcPause = gcPause;

        var db = ReadDb();
        if (db is not null)
        {
            v["db_total_conns"] = db.Total;
            v["db_idle_conns"] = db.Idle;
            v["db_acquired_conns"] = db.InUse;
            v["db_max_conns"] = db.Max;
            if (_db is not null)
            {
                v["db_acquires"] = db.Acquires - _db.Acquires;
                v["db_empty_acquires"] = db.EmptyAcquires - _db.EmptyAcquires;
                v["db_acquire_wait_ms"] = Math.Round(db.AcquireWaitMs - _db.AcquireWaitMs, 1);
            }
        }
        _db = db;

        var reqs = Interlocked.Read(ref _agent.Http.Requests);
        var errs = Interlocked.Read(ref _agent.Http.Errors);
        v["http_requests"] = reqs - _requests;
        v["http_errors"] = errs - _errors;
        v["http_rps"] = Math.Round((reqs - _requests) / secs, 2);
        v["http_in_flight"] = Math.Max(0, Interlocked.Read(ref _agent.Http.InFlight));
        _requests = reqs;
        _errors = errs;
        v["uptime_s"] = (long)Stopwatch.GetElapsedTime(_started).TotalSeconds;

        try
        {
            _host!.Sample(v, secs);
        }
        catch (Exception e)
        {
            // The host columns are best effort; the rest of the row still counts.
            _agent.Logger.LogDebug(e, "visionary: host metrics failed");
        }
        try
        {
            _cgroup?.Sample(v);
        }
        catch (Exception e)
        {
            _agent.Logger.LogDebug(e, "visionary: container metrics failed");
        }

        var row = new object?[Schema.SystemColumns.Length];
        for (var i = 0; i < row.Length; i++)
        {
            row[i] = v.TryGetValue(Schema.SystemColumns[i], out var value) ? value : null;
        }
        return row;
    }

    private VisionaryDbStats? ReadDb()
    {
        try
        {
            return _agent.Options.DbStats?.Invoke();
        }
        catch (Exception)
        {
            return null;
        }
    }
}
