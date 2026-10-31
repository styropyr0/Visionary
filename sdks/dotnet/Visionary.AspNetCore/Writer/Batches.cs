using System;
using System.Collections.Generic;
using System.Text;
using System.Threading;
using System.Threading.Tasks;

namespace Styropyr0.Visionary;

/// <summary>
/// How the writers batch: D1 gets one INSERT per row, 50 to a call; SQL backends get each
/// table's rows as multi-row INSERTs, kept under every backend's bound-parameter limit.
/// </summary>
internal static class Batches
{
    public const int D1Size = 50;
    public const int DefaultSqlSize = 500;
    public const int MaxSqlSize = 2000;
    public const int MaxParamsPerStatement = 30_000;

    // Microsoft.Data.Sqlite binds every parameter by name, which costs the square of a
    // statement's parameters: 30000 takes seconds, a few hundred a millisecond.
    public const int MaxSqliteParams = 250;
    public const int ParallelFlushes = 4;

    /// <summary>Rows per batch for this store, honouring <see cref="VisionaryOptions.BatchSize"/>.</summary>
    public static int Size(IStore? store, int configured)
    {
        if (store is null || store.StringParams)
        {
            return configured is > 0 and <= D1Size ? configured : D1Size;
        }
        return configured is > 0 and <= MaxSqlSize ? configured : DefaultSqlSize;
    }

    /// <summary>How many batches may be written at once: SQLite and D1 take one writer at a time.</summary>
    public static int Flushers(IStore? store) =>
        store is null || store.StringParams || store.Dialect == VisionaryDialect.Sqlite ? 1 : ParallelFlushes;

    /// <summary>
    /// Merges single-row INSERTs into multi-row ones per table, for SQL backends; D1 statements
    /// pass through unchanged.
    /// </summary>
    public static List<Stmt> Combine(IStore? store, List<Stmt> stmts)
    {
        if (store is null || store.StringParams || stmts.Count < 2)
        {
            return stmts;
        }
        var groups = new Dictionary<string, List<Stmt>>(StringComparer.Ordinal);
        var order = new List<string>();
        foreach (var st in stmts)
        {
            if (!groups.TryGetValue(st.Sql, out var g))
            {
                groups[st.Sql] = g = new List<Stmt>();
                order.Add(st.Sql);
            }
            g.Add(st);
        }
        var output = new List<Stmt>();
        foreach (var sql in order)
        {
            var rows = groups[sql];
            var values = sql.IndexOf(" VALUES ", StringComparison.Ordinal);
            if (rows.Count == 1 || values < 0 || !sql.StartsWith("INSERT INTO ", StringComparison.Ordinal))
            {
                output.AddRange(rows);
                continue;
            }
            var head = sql[..(values + 8)];
            var tuple = sql[(values + 8)..];
            var width = Math.Max(1, rows[0].Args.Length);
            var cap = store.Dialect == VisionaryDialect.Sqlite ? MaxSqliteParams : MaxParamsPerStatement;
            var per = Math.Max(1, cap / width);
            for (var i = 0; i < rows.Count; i += per)
            {
                var n = Math.Min(per, rows.Count - i);
                output.Add(new Stmt(RowsSql(head, tuple, n), Flatten(rows, i, n, width), n));
            }
        }
        return output;
    }

    public static long RowCount(IReadOnlyList<Stmt> stmts)
    {
        long n = 0;
        foreach (var st in stmts)
        {
            n += Math.Max(st.Rows, 1);
        }
        return n;
    }

    private static string RowsSql(string head, string tuple, int n)
    {
        var b = new StringBuilder(head.Length + n * (tuple.Length + 2));
        b.Append(head);
        for (var i = 0; i < n; i++)
        {
            if (i > 0)
            {
                b.Append(", ");
            }
            b.Append(tuple);
        }
        return b.ToString();
    }

    private static object?[] Flatten(List<Stmt> rows, int start, int n, int width)
    {
        var args = new object?[n * width];
        for (var i = 0; i < n; i++)
        {
            rows[start + i].Args.CopyTo(args, i * width);
        }
        return args;
    }
}

/// <summary>
/// Runs up to a fixed number of batch writes at once; the writer loop waits for a free slot
/// while the queue absorbs the load.
/// </summary>
internal sealed class Flushes
{
    private readonly SemaphoreSlim _slots;
    private readonly List<Task> _running = new();

    public Flushes(int parallel) => _slots = new SemaphoreSlim(parallel, parallel);

    public async Task RunAsync(Func<Task> write)
    {
        await _slots.WaitAsync().ConfigureAwait(false);
        lock (_running)
        {
            _running.RemoveAll(t => t.IsCompleted);
            _running.Add(Task.Run(async () =>
            {
                try
                {
                    await write().ConfigureAwait(false);
                }
                finally
                {
                    _slots.Release();
                }
            }));
        }
    }

    public Task DrainAsync()
    {
        lock (_running)
        {
            return Task.WhenAll(_running.ToArray());
        }
    }
}

/// <summary>Rows written and dropped since start, summed over the writers.</summary>
internal sealed class WriterCounters
{
    private long _written;
    private long _dropped;

    public long Written => Interlocked.Read(ref _written);
    public long Dropped => Interlocked.Read(ref _dropped);

    public void Wrote(long rows) => Interlocked.Add(ref _written, rows);
    public void Lost(long rows) => Interlocked.Add(ref _dropped, rows);
}
