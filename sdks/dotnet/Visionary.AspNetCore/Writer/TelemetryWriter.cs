using System;
using System.Collections.Generic;
using System.Threading;
using System.Threading.Tasks;
using Microsoft.Extensions.Hosting;
using Microsoft.Extensions.Logging;

namespace Styropyr0.Visionary;

/// <summary>
/// The single background writer for api_logs, spans, errors and releases: flushes every 50
/// entries or 3 seconds as one batch, retries a failed batch twice (after 1 s, then 2 s), and
/// drains the queue on shutdown.
/// </summary>
internal sealed class TelemetryWriter : BackgroundService
{
    private const int BatchSize = 50;
    private static readonly TimeSpan FlushInterval = TimeSpan.FromSeconds(3);
    private static readonly TimeSpan DrainTimeout = TimeSpan.FromSeconds(10);

    private readonly VisionaryAgent _agent;
    private readonly RowBuilder _rows;
    private bool _schemaReady;

    public TelemetryWriter(VisionaryAgent agent)
    {
        _agent = agent;
        _rows = new RowBuilder(agent);
    }

    private bool TelemetryTables => _agent.Options.Spans || _agent.Options.Errors || _agent.Version is not null;

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        if (!_agent.Enabled || !(_agent.Options.LogRequests || TelemetryTables))
        {
            return;
        }
        Suppress.Enter();
        await Task.Yield();
        var o = _agent.Options;
        _agent.Logger.LogInformation(
            "visionary: writing to {Store}; requests {Requests} (path prefix \"{Prefix}\", bodies {Bodies}), spans {Spans}, errors {Errors}, service {Service}, version {Version}",
            _agent.Store!.Describe, o.LogRequests ? "on" : "off", o.PathPrefix, o.LogBodies, o.Spans, o.Errors,
            _agent.Service ?? "-", _agent.Version ?? "-");

        await EnsureSchemaAsync(stoppingToken).ConfigureAwait(false);
        if (_agent.Version is not null)
        {
            _agent.Enqueue(new ReleaseRecord(DateTime.UtcNow));
        }
        var retention = Retention.RunAsync(_agent, Retention.LogTables(o), stoppingToken);

        var reader = _agent.Queue.Reader;
        var batch = new List<WriteItem>(BatchSize);
        var deadline = DateTime.UtcNow + FlushInterval;
        while (!stoppingToken.IsCancellationRequested)
        {
            var wait = deadline - DateTime.UtcNow;
            if (wait > TimeSpan.Zero)
            {
                using var timeout = CancellationTokenSource.CreateLinkedTokenSource(stoppingToken);
                timeout.CancelAfter(wait);
                try
                {
                    await reader.WaitToReadAsync(timeout.Token).ConfigureAwait(false);
                }
                catch (OperationCanceledException)
                {
                    // flush interval reached, or shutting down
                }
            }
            while (batch.Count < BatchSize && reader.TryRead(out var item))
            {
                batch.Add(item);
            }
            if (batch.Count >= BatchSize || DateTime.UtcNow >= deadline)
            {
                // Not stoppingToken: a batch in flight at shutdown still gets written.
                await WriteAsync(batch, CancellationToken.None).ConfigureAwait(false);
                batch.Clear();
                deadline = DateTime.UtcNow + FlushInterval;
                var dropped = _agent.TakeDropped();
                if (dropped > 0)
                {
                    _agent.Logger.LogWarning("visionary: write queue full; {Dropped} entries dropped", dropped);
                }
            }
        }

        // Shutting down: drain what is queued, bounded by a timeout.
        using var drain = new CancellationTokenSource(DrainTimeout);
        while (reader.TryRead(out var item))
        {
            batch.Add(item);
            if (batch.Count >= BatchSize)
            {
                await WriteAsync(batch, drain.Token).ConfigureAwait(false);
                batch.Clear();
            }
        }
        await WriteAsync(batch, drain.Token).ConfigureAwait(false);
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
        var store = _agent.Store!;
        using var timeout = CancellationTokenSource.CreateLinkedTokenSource(ct);
        timeout.CancelAfter(TimeSpan.FromSeconds(20));
        try
        {
            if (_agent.Options.LogRequests)
            {
                await store.CreateAsync("requests", "api_logs", Schema.AddedRequestColumns, timeout.Token).ConfigureAwait(false);
            }
            if (TelemetryTables)
            {
                await store.CreateAsync("telemetry", null, null, timeout.Token).ConfigureAwait(false);
            }
            _schemaReady = true;
        }
        catch (Exception e) when (!ct.IsCancellationRequested)
        {
            _agent.Logger.LogError("visionary: create tables failed (retrying on the next write): {Error}", e.Message);
        }
    }

    private async Task WriteAsync(List<WriteItem> batch, CancellationToken ct)
    {
        if (batch.Count == 0)
        {
            return;
        }
        try
        {
            await EnsureSchemaAsync(ct).ConfigureAwait(false);
            var stmts = await _rows.BuildAsync(batch, ct).ConfigureAwait(false);
            Exception? last = null;
            for (var attempt = 1; attempt <= 3; attempt++)
            {
                using var timeout = CancellationTokenSource.CreateLinkedTokenSource(ct);
                timeout.CancelAfter(TimeSpan.FromSeconds(30));
                try
                {
                    await _agent.Store!.BatchAsync(stmts, timeout.Token).ConfigureAwait(false);
                    return;
                }
                catch (Exception e) when (!ct.IsCancellationRequested)
                {
                    last = e;
                }
                if (attempt < 3)
                {
                    await Task.Delay(TimeSpan.FromSeconds(attempt), ct).ConfigureAwait(false);
                }
            }
            _agent.Logger.LogError("visionary: batch write failed; {Count} entries dropped: {Error}", batch.Count, last?.Message);
        }
        catch (Exception e)
        {
            // Shutdown timeout or a bug in row building: never take the writer down with it.
            _agent.Logger.LogError("visionary: batch write failed; {Count} entries dropped: {Error}", batch.Count, e.Message);
        }
    }
}
