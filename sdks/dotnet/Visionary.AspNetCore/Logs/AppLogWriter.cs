using System;
using System.Collections.Generic;
using System.Threading;
using System.Threading.Tasks;
using Microsoft.Extensions.Hosting;
using Microsoft.Extensions.Logging;

namespace Styropyr0.Visionary;

// Separate from TelemetryWriter so a flood of log lines can't crowd out request rows.
internal sealed class AppLogWriter : BackgroundService
{
    private const int BatchSize = 50;
    private static readonly TimeSpan FlushInterval = TimeSpan.FromSeconds(3);
    private static readonly TimeSpan DrainTimeout = TimeSpan.FromSeconds(10);
    private static readonly TimeSpan Poll = TimeSpan.FromSeconds(30);

    private readonly VisionaryAgent _agent;
    private readonly RowBuilder _rows;
    private bool _schemaReady;
    private bool _readFailedLogged;

    public AppLogWriter(VisionaryAgent agent)
    {
        _agent = agent;
        _rows = new RowBuilder(agent);
    }

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        var logs = _agent.Logs;
        if (logs is null)
        {
            return;
        }
        Suppress.Enter();
        await Task.Yield();
        _agent.Logger.LogInformation("visionary: capturing application logs into app_logs (level {Level} until Settings → Logs is read)",
            Severity.Name(logs.BaseLevel));
        await EnsureSchemaAsync(stoppingToken).ConfigureAwait(false);
        var poll = PollAsync(logs, stoppingToken);
        var tables = new List<(string Table, int Days)>();
        if (_agent.Options.LogRetentionDays > 0)
        {
            tables.Add(("app_logs", _agent.Options.LogRetentionDays));
        }
        var retention = Retention.RunAsync(_agent, tables, stoppingToken);

        var reader = logs.Queue.Reader;
        var batch = new List<LogRecord>(BatchSize);
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
                var dropped = logs.TakeDropped();
                if (dropped > 0)
                {
                    _agent.Logger.LogWarning("visionary: log queue full; {Dropped} lines dropped", dropped);
                }
            }
        }

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
        foreach (var task in new[] { poll, retention })
        {
            try
            {
                await task.ConfigureAwait(false);
            }
            catch (OperationCanceledException)
            {
                // stopped with the host
            }
        }
    }

    private async Task PollAsync(AppLogs logs, CancellationToken ct)
    {
        while (!ct.IsCancellationRequested)
        {
            await ReadSettingsAsync(logs, ct).ConfigureAwait(false);
            await Task.Delay(Poll, ct).ConfigureAwait(false);
        }
    }

    internal async Task ReadSettingsAsync(AppLogs logs, CancellationToken ct)
    {
        try
        {
            using var timeout = CancellationTokenSource.CreateLinkedTokenSource(ct);
            timeout.CancelAfter(TimeSpan.FromSeconds(15));
            var rows = await _agent.Store!.RowsAsync(LogSettings.Query, timeout.Token).ConfigureAwait(false);
            logs.Settings = LogSettings.Parse(rows, logs.BaseLevel);
            _readFailedLogged = false;
        }
        catch (Exception e) when (!ct.IsCancellationRequested)
        {
            // No server yet or the table is missing; keep the previous settings.
            if (!_readFailedLogged)
            {
                _readFailedLogged = true;
                _agent.Logger.LogDebug("visionary: log settings unavailable; keeping the current ones: {Error}", e.Message);
            }
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
            await _agent.Store!.CreateAsync("logs", null, null, timeout.Token).ConfigureAwait(false);
            _schemaReady = true;
        }
        catch (Exception e) when (!ct.IsCancellationRequested)
        {
            _agent.Logger.LogError("visionary: create app_logs failed (retrying on the next write): {Error}", e.Message);
        }
    }

    private async Task WriteAsync(List<LogRecord> batch, CancellationToken ct)
    {
        if (batch.Count == 0)
        {
            return;
        }
        try
        {
            await EnsureSchemaAsync(ct).ConfigureAwait(false);
            var secrets = _agent.Logs!.Secrets;
            var stmts = new List<Stmt>(batch.Count);
            foreach (var r in batch)
            {
                stmts.Add(_rows.Log(r, secrets));
            }
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
            _agent.Logger.LogError("visionary: log batch write failed; {Count} lines dropped: {Error}", batch.Count, last?.Message);
        }
        catch (Exception e)
        {
            // Never let one bad batch take the writer down.
            _agent.Logger.LogError("visionary: log batch write failed; {Count} lines dropped: {Error}", batch.Count, e.Message);
        }
    }
}
