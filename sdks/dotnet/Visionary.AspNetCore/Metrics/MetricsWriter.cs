using System;
using System.Collections.Generic;
using System.Threading;
using System.Threading.Tasks;
using Microsoft.Extensions.Hosting;
using Microsoft.Extensions.Logging;

namespace Styropyr0.Visionary;

/// <summary>
/// Writes custom metrics every interval: one row per series that has something to say, in
/// batches (see <see cref="Batches"/>) with two retries, apart from the request writer. The table is created at the
/// first interval with rows, so services without custom metrics never get it. On shutdown the
/// last interval is written.
/// </summary>
internal sealed class MetricsWriter : BackgroundService
{
    private const int QueueSize = 20_000;

    private readonly VisionaryAgent _agent;
    private readonly RowBuilder _rows;
    private bool _schemaReady;
    private MeterBridge? _meters;

    internal MetricRegistry Registry { get; init; } = MetricRegistry.Shared;

    public MetricsWriter(VisionaryAgent agent)
    {
        _agent = agent;
        _rows = new RowBuilder(agent);
        MetricRegistry.Shared.MaxSeries = agent.Options.MetricsMaxSeries;
        MetricRegistry.Shared.Off = !agent.Options.Metrics;
    }

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        if (!_agent.Enabled || !_agent.Options.Metrics)
        {
            return;
        }
        Suppress.Enter();
        await Task.Yield();
        var tables = new List<(string Table, int Days)>();
        if (_agent.Options.LogRetentionDays > 0)
        {
            tables.Add(("metrics", _agent.Options.LogRetentionDays));
        }
        var retention = Retention.RunAsync(_agent, tables, stoppingToken);
        using var meters = _agent.Options.MetricsMeters.Count > 0 ? new MeterBridge(_agent.Options.MetricsMeters) : null;
        _meters = meters;
        var interval = _agent.Options.MetricsInterval > TimeSpan.Zero ? _agent.Options.MetricsInterval : TimeSpan.FromSeconds(15);
        using var timer = new PeriodicTimer(interval);
        try
        {
            while (await timer.WaitForNextTickAsync(stoppingToken).ConfigureAwait(false))
            {
                await FlushAsync(DateTime.UtcNow, CancellationToken.None).ConfigureAwait(false);
            }
        }
        catch (OperationCanceledException)
        {
            // stopping
        }
        using var drain = new CancellationTokenSource(TimeSpan.FromSeconds(10));
        await FlushAsync(DateTime.UtcNow, drain.Token).ConfigureAwait(false);
        try
        {
            await retention.ConfigureAwait(false);
        }
        catch (OperationCanceledException)
        {
            // stopped with the host
        }
    }

    /// <summary>Takes the interval that ends at <paramref name="now"/> and writes it.</summary>
    internal async Task FlushAsync(DateTime now, CancellationToken ct)
    {
        _meters?.Observe();
        var (rows, dropped) = Registry.Collect(now);
        foreach (var (name, n) in dropped)
        {
            _agent.Logger.LogWarning("visionary: metric series dropped over the limit (metric {Metric}, dropped {Dropped}, limit {Limit})",
                name, n, Registry.MaxSeries);
        }
        if (rows.Count == 0)
        {
            return;
        }
        if (rows.Count > QueueSize)
        {
            _agent.Logger.LogWarning("visionary: metrics queue full; {Dropped} rows dropped", rows.Count - QueueSize);
            _agent.Writes.Lost(rows.Count - QueueSize);
            rows.RemoveRange(QueueSize, rows.Count - QueueSize);
        }
        try
        {
            if (!_schemaReady)
            {
                using var timeout = CancellationTokenSource.CreateLinkedTokenSource(ct);
                timeout.CancelAfter(TimeSpan.FromSeconds(20));
                await _agent.Store!.CreateAsync("metrics", null, null, timeout.Token).ConfigureAwait(false);
                _schemaReady = true;
            }
        }
        catch (Exception e) when (!ct.IsCancellationRequested)
        {
            _agent.Logger.LogError("visionary: create metrics failed; {Count} rows dropped: {Error}", rows.Count, e.Message);
            _agent.Writes.Lost(rows.Count);
            return;
        }
        var size = Batches.Size(_agent.Store, _agent.Options.BatchSize);
        for (var i = 0; i < rows.Count; i += size)
        {
            var stmts = new List<Stmt>(size);
            for (var j = i; j < Math.Min(i + size, rows.Count); j++)
            {
                stmts.Add(_rows.Metric(rows[j]));
            }
            await WriteAsync(Batches.Combine(_agent.Store, stmts), ct).ConfigureAwait(false);
        }
    }

    private async Task WriteAsync(List<Stmt> stmts, CancellationToken ct)
    {
        Exception? last = null;
        for (var attempt = 1; attempt <= 3; attempt++)
        {
            using var timeout = CancellationTokenSource.CreateLinkedTokenSource(ct);
            timeout.CancelAfter(TimeSpan.FromSeconds(30));
            try
            {
                await _agent.Store!.BatchAsync(stmts, timeout.Token).ConfigureAwait(false);
                _agent.Writes.Wrote(Batches.RowCount(stmts));
                return;
            }
            catch (Exception e)
            {
                last = e;
                if (ct.IsCancellationRequested)
                {
                    break;
                }
            }
            if (attempt < 3)
            {
                try
                {
                    await Task.Delay(TimeSpan.FromSeconds(attempt), ct).ConfigureAwait(false);
                }
                catch (OperationCanceledException)
                {
                    break;
                }
            }
        }
        var lost = Batches.RowCount(stmts);
        _agent.Logger.LogError("visionary: metrics write failed; {Count} rows dropped: {Error}", lost, last?.Message);
        _agent.Writes.Lost(lost);
    }
}
