using System;
using System.Collections.Generic;
using System.Globalization;
using System.Threading;
using System.Threading.Tasks;

namespace Styropyr0.Visionary;

/// <summary>One write with typed args: string, long, double or null.</summary>
internal readonly record struct Stmt(string Sql, object?[] Args);

/// <summary>Where rows go: D1 over REST, or an ADO.NET database.</summary>
internal interface IStore
{
    VisionaryDialect Dialect { get; }

    /// <summary>Whether args must travel as strings (D1).</summary>
    bool StringParams { get; }

    /// <summary>A short description for the startup log line, without credentials.</summary>
    string Describe { get; }

    Task ExecScriptAsync(string script, CancellationToken ct);

    /// <summary>Writes all statements together: one transaction, or one D1 batch call.</summary>
    Task BatchAsync(IReadOnlyList<Stmt> stmts, CancellationToken ct);

    Task<long> ExecChangesAsync(string sql, object?[] args, CancellationToken ct);

    /// <summary>Runs a query returning one name per row.</summary>
    Task<List<string>> ColumnsAsync(string query, CancellationToken ct);
}

internal static class StoreExtensions
{
    /// <summary>Creates tables from a schema file and adds columns newer SDKs introduced.</summary>
    public static async Task CreateAsync(this IStore store, string schemaFile, string? table, IReadOnlyList<AddedColumn>? added, CancellationToken ct)
    {
        await store.ExecScriptAsync(Schema.Script(store.Dialect, schemaFile), ct).ConfigureAwait(false);
        if (table is null || added is null)
        {
            return;
        }
        var existing = await store.ColumnsAsync(Schema.ColumnsQuery(store.Dialect, table), ct).ConfigureAwait(false);
        foreach (var alter in Schema.MissingColumns(store.Dialect, table, added, existing))
        {
            try
            {
                await store.ExecScriptAsync(alter, ct).ConfigureAwait(false);
            }
            catch (Exception e) when (Schema.IsDuplicateColumn(e))
            {
                // another instance added it first
            }
        }
    }

    /// <summary>The D1 form of a value: every param is a string, "" for null.</summary>
    public static string AsString(object? v) => v switch
    {
        null => "",
        string s => s,
        long l => l.ToString(CultureInfo.InvariantCulture),
        int i => i.ToString(CultureInfo.InvariantCulture),
        double d => d.ToString("R", CultureInfo.InvariantCulture),
        bool b => b ? "1" : "0",
        IFormattable f => f.ToString(null, CultureInfo.InvariantCulture),
        _ => v.ToString() ?? "",
    };
}
