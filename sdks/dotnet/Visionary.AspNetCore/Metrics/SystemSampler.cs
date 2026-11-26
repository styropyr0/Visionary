using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Linq;
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

    /// <summary>D1's limit of bound params per statement</summary>
    internal const int D1MaxParams = 100;

    /// <summary>The visionary_settings row Settings → Server keeps the switch for SDK system metrics in</summary>
    internal const string Setting = "config.sdk_system_metrics";

    private const string SettingQuery = "SELECT value FROM visionary_settings WHERE name = '" + Setting + "'";
    private static readonly TimeSpan SettingsPoll = TimeSpan.FromSeconds(30);

    private readonly VisionaryAgent _agent;
    private readonly string _insert;
    private readonly bool _d1;
    private readonly List<object?[]> _buffer = new();
    private readonly long _started = Stopwatch.GetTimestamp();
    private HostSampler? _host;
    private CgroupReader? _cgroup; // null off Linux and outside a cgroup
    private HostStats? _stats;
    private bool _schemaReady;
    private bool _off;

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
        _d1 = agent.Store?.StringParams ?? false;
        _insert = Schema.InsertSql("system_metrics", Schema.SystemColumns, _d1);
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
        Prime();
        _agent.Logger.LogInformation("visionary: recording system metrics every {Interval} as {Instance}", Interval, _agent.Instance);
        await EnsureSchemaAsync(stoppingToken).ConfigureAwait(false);
        var retention = Retention.RunAsync(_agent, _agent.Options.SystemRetentionDays > 0
            ? new() { ("system_metrics", _agent.Options.SystemRetentionDays) }
            : new(), stoppingToken);

        using var timer = new PeriodicTimer(Interval);
        using var poll = new PeriodicTimer(SettingsPoll);
        try
        {
            await ReadSwitchAsync(stoppingToken).ConfigureAwait(false);
            var tick = timer.WaitForNextTickAsync(stoppingToken).AsTask();
            var check = poll.WaitForNextTickAsync(stoppingToken).AsTask();
            while (true)
            {
                var done = await Task.WhenAny(tick, check).ConfigureAwait(false);
                if (!await done.ConfigureAwait(false))
                {
                    break;
                }
                if (done == check)
                {
                    await ReadSwitchAsync(stoppingToken).ConfigureAwait(false);
                    check = poll.WaitForNextTickAsync(stoppingToken).AsTask();
                    continue;
                }
                await TickAsync().ConfigureAwait(false);
                tick = timer.WaitForNextTickAsync(stoppingToken).AsTask();
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

    internal bool Off => _off;

    internal int Buffered => _buffer.Count;

    internal async Task TickAsync()
    {
        if (_off)
        {
            return;
        }
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

    /// <summary>
    /// Follows Settings → Server, and a failed read keeps the state
    /// </summary>
    internal async Task ReadSwitchAsync(CancellationToken ct)
    {
        List<string?[]> rows;
        try
        {
            using var timeout = CancellationTokenSource.CreateLinkedTokenSource(ct);
            timeout.CancelAfter(TimeSpan.FromSeconds(10));
            rows = await _agent.Store!.RowsAsync(SettingQuery, timeout.Token).ConfigureAwait(false);
        }
        catch (Exception e) when (!ct.IsCancellationRequested)
        {
            _agent.Logger.LogDebug("visionary: system metrics switch unavailable; keeping the current state: {Error}", e.Message);
            return;
        }
        var off = rows.Count > 0 && rows[0].Length > 0
            && string.Equals(rows[0][0]?.Trim(), "false", StringComparison.OrdinalIgnoreCase);
        if (off == _off)
        {
            return;
        }
        _off = off;
        if (off)
        {
            _buffer.Clear();
            _agent.Logger.LogInformation("visionary: system metrics turned off in Settings → Server");
            return;
        }
        // A fresh baseline, so the next row doesn't average over the pause
        try
        {
            Sample();
        }
        catch (Exception e)
        {
            _agent.Logger.LogDebug(e, "visionary: system sample failed");
        }
        _agent.Logger.LogInformation("visionary: system metrics turned back on in Settings → Server");
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
            var stmts = _buffer.SelectMany(row => RowStmts(_insert, _d1, row)).ToList();
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

    /// <summary>
    /// The row's INSERT. D1 binds at most 100 params, so there the columns past the first 100
    /// follow in an UPDATE of the row just inserted, in the same batch
    /// </summary>
    internal static IEnumerable<Stmt> RowStmts(string insert, bool d1, object?[] row)
    {
        if (!d1 || row.Length <= D1MaxParams)
        {
            return new[] { new Stmt(insert, row) };
        }
        return new[] { new Stmt(D1Insert, row[..D1MaxParams]), new Stmt(D1Update, row[D1MaxParams..]) };
    }

    private static readonly string D1Insert = Schema.InsertSql("system_metrics", Schema.SystemColumns[..Math.Min(D1MaxParams, Schema.SystemColumns.Length)], true);

    private static readonly string D1Update = "UPDATE system_metrics SET "
        + string.Join(", ", Schema.SystemColumns.Skip(D1MaxParams).Select(c => c + " = NULLIF(?, '')"))
        + " WHERE id = last_insert_rowid()";

    /// <summary>Baseline readings so the first row has real deltas.</summary>
    internal void Prime()
    {
        _host = new HostSampler(_agent.Options.DiskPath);
        _last = Stopwatch.GetTimestamp();
        _requests = Interlocked.Read(ref _agent.Http.Requests);
        _errors = Interlocked.Read(ref _agent.Http.Errors);
        _gcCount = GC.CollectionCount(0);
        _gcPause = GC.GetTotalPauseDuration();
        _db = ReadDb();
        _host!.Prime();
        _cgroup = CgroupReader.Open();
        _cgroup?.Prime();
        _stats = HostStats.ForThisProcess(_cgroup);
        _stats.Prime();
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
        try
        {
            _stats?.Sample(v, secs);
        }
        catch (Exception e)
        {
            _agent.Logger.LogDebug(e, "visionary: pressure and memory stats failed");
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
