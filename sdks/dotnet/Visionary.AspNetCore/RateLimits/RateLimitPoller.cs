using System;
using System.Collections.Generic;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using Microsoft.Extensions.Hosting;
using Microsoft.Extensions.Logging;

namespace Styropyr0.Visionary;

/// <summary>
/// Reads Security → Rate limits from visionary_settings every 15 seconds (the config only when
/// the version changed), sweeps idle counters and writes what was limited to rate_limit_events.
/// </summary>
internal sealed class RateLimitPoller : BackgroundService
{
    private static readonly TimeSpan Poll = TimeSpan.FromSeconds(15);
    private const string VersionQuery = "SELECT value FROM visionary_settings WHERE name = '" + RateLimitConfig.SettingVersion + "'";
    private const string ConfigQuery = "SELECT value FROM visionary_settings WHERE name = '" + RateLimitConfig.SettingConfig + "'";

    private readonly VisionaryAgent _agent;
    private string _version = "";
    private string _badVersion = "";
    private bool _writeFailed;

    public RateLimitPoller(VisionaryAgent agent) => _agent = agent;

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        if (_agent.RateLimits is null)
        {
            return;
        }
        Suppress.Enter();
        await Task.Yield();
        try
        {
            while (!stoppingToken.IsCancellationRequested)
            {
                await PollAsync(stoppingToken).ConfigureAwait(false);
                _agent.RateLimits.Sweep(DateTime.UtcNow);
                await FlushAsync(stoppingToken).ConfigureAwait(false);
                await Task.Delay(Poll, stoppingToken).ConfigureAwait(false);
            }
        }
        catch (OperationCanceledException)
        {
        }
        using var drain = new CancellationTokenSource(TimeSpan.FromSeconds(10));
        await FlushAsync(drain.Token).ConfigureAwait(false);
    }

    internal async Task PollAsync(CancellationToken ct)
    {
        var limiter = _agent.RateLimits!;
        try
        {
            using var timeout = CancellationTokenSource.CreateLinkedTokenSource(ct);
            timeout.CancelAfter(TimeSpan.FromSeconds(10));
            var rows = await _agent.Store!.RowsAsync(VersionQuery, timeout.Token).ConfigureAwait(false);
            if (rows.Count == 0 || rows[0].Length == 0 || rows[0][0] is null)
            {
                if (_version.Length > 0)
                {
                    limiter.Clear();
                }
                _version = "";
                return;
            }
            var next = rows[0][0]!;
            if (next == _version)
            {
                return;
            }
            var cfg = await _agent.Store.RowsAsync(ConfigQuery, timeout.Token).ConfigureAwait(false);
            var raw = cfg.Count > 0 && cfg[0].Length > 0 ? cfg[0][0] ?? "" : "";
            RateLimitConfig parsed;
            try
            {
                parsed = raw.Length == 0 ? new RateLimitConfig() : RateLimitConfig.Parse(raw);
            }
            catch (JsonException e)
            {
                if (_badVersion != next)
                {
                    _badVersion = next;
                    _agent.Logger.LogWarning("visionary: rate limit rules are not valid JSON; keeping the previous ones: {Error}", e.Message);
                }
                return;
            }
            limiter.Load(parsed);
            _version = next;
        }
        catch (Exception e) when (!ct.IsCancellationRequested)
        {
            // No table yet or the database is away: keep the rules in force.
            _agent.Logger.LogDebug("visionary: rate limit rules unavailable; keeping the current ones: {Error}", e.Message);
        }
    }

    internal async Task FlushAsync(CancellationToken ct)
    {
        var events = _agent.RateLimits!.Drain();
        if (events.Count == 0)
        {
            return;
        }
        var store = _agent.Store!;
        var insert = Schema.InsertSql("rate_limit_events", Schema.RateLimitColumns, store.StringParams);
        var stmts = new List<Stmt>(events.Count);
        foreach (var e in events)
        {
            stmts.Add(new Stmt(insert, new object?[]
            {
                RowBuilder.Timestamp(e.CreatedAt), _agent.Service, _agent.Instance, e.RuleId, e.RuleName, e.Outcome,
                Null(e.KeyValue), Null(e.Ip), Null(e.Country), Null(e.Method), Null(e.Path), Null(e.UserId), e.Hits,
            }));
        }
        var combined = Batches.Combine(store, stmts);
        try
        {
            using var timeout = CancellationTokenSource.CreateLinkedTokenSource(ct);
            timeout.CancelAfter(TimeSpan.FromSeconds(15));
            for (var i = 0; i < combined.Count; i += Batches.D1Size)
            {
                var part = combined.GetRange(i, Math.Min(Batches.D1Size, combined.Count - i));
                await store.BatchAsync(part, timeout.Token).ConfigureAwait(false);
                _agent.Writes.Wrote(Batches.RowCount(part));
            }
        }
        catch (Exception e)
        {
            if (!_writeFailed)
            {
                _writeFailed = true;
                _agent.Logger.LogInformation("visionary: could not record rate-limited requests (the Visionary server creates rate_limit_events): {Error}", e.Message);
            }
        }
    }

    private static string? Null(string s) => s.Length == 0 ? null : s;
}
