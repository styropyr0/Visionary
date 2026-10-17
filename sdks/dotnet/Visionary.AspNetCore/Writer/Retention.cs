using System;
using System.Collections.Generic;
using System.Threading;
using System.Threading.Tasks;
using Microsoft.Extensions.Logging;

namespace Styropyr0.Visionary;

/// <summary>
/// Deletes expired rows a minute after start and then daily, in chunks of 5000 so no single
/// statement runs long or hits D1's limits. Releases are kept.
/// </summary>
internal static class Retention
{
    private const int Chunk = 5000;
    private const int MaxRounds = 200;

    /// <summary>The tables the writer owns, with VISIONARY_LOG_RETENTION_DAYS.</summary>
    public static List<(string Table, int Days)> LogTables(VisionaryOptions o)
    {
        var tables = new List<(string Table, int Days)>();
        if (o.LogRetentionDays > 0)
        {
            if (o.LogRequests)
            {
                tables.Add(("api_logs", o.LogRetentionDays));
            }
            if (o.Spans)
            {
                tables.Add(("spans", o.LogRetentionDays));
            }
            if (o.Errors)
            {
                tables.Add(("errors", o.LogRetentionDays));
            }
        }
        return tables;
    }

    public static async Task RunAsync(VisionaryAgent agent, List<(string Table, int Days)> tables, CancellationToken ct)
    {
        Suppress.Enter();
        if (tables.Count == 0)
        {
            return;
        }
        var delay = TimeSpan.FromMinutes(1);
        while (true)
        {
            await Task.Delay(delay, ct).ConfigureAwait(false);
            delay = TimeSpan.FromHours(24);
            foreach (var (table, days) in tables)
            {
                await PurgeAsync(agent, table, days, ct).ConfigureAwait(false);
            }
        }
    }

    private static async Task PurgeAsync(VisionaryAgent agent, string table, int days, CancellationToken ct)
    {
        var store = agent.Store!;
        var sql = Schema.PurgeSql(store.Dialect, table);
        var cutoff = RowBuilder.Timestamp(DateTime.UtcNow.AddDays(-days));
        long total = 0;
        try
        {
            for (var round = 0; round < MaxRounds; round++)
            {
                using var timeout = CancellationTokenSource.CreateLinkedTokenSource(ct);
                timeout.CancelAfter(TimeSpan.FromSeconds(30));
                var changes = await store.ExecChangesAsync(sql, new object?[] { cutoff }, timeout.Token).ConfigureAwait(false);
                total += changes;
                if (changes < Chunk)
                {
                    break;
                }
            }
        }
        catch (Exception e) when (!ct.IsCancellationRequested)
        {
            agent.Logger.LogError("visionary: retention purge of {Table} failed after {Deleted} rows: {Error}", table, total, e.Message);
            return;
        }
        if (total > 0)
        {
            agent.Logger.LogInformation("visionary: retention purge deleted {Deleted} rows from {Table} (before {Cutoff})", total, table, cutoff);
        }
    }
}
