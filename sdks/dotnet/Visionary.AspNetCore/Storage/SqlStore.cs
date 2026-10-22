using System;
using System.Collections.Generic;
using System.Data.Common;
using System.Globalization;
using System.Text;
using System.Threading;
using System.Threading.Tasks;

namespace Styropyr0.Visionary;

/// <summary>
/// Writes through ADO.NET: a <see cref="DbDataSource"/> or connection factory the application
/// supplied, or one the SDK built from VISIONARY_DATABASE_URL. Each call opens (or borrows from
/// the pool) a connection and returns it straight after.
/// </summary>
internal sealed class SqlStore : IStore
{
    private readonly Func<CancellationToken, ValueTask<DbConnection>> _open;

    // SQLite allows one writer at a time; queueing here beats SQLITE_BUSY retries.
    private readonly SemaphoreSlim? _gate;

    public SqlStore(Func<CancellationToken, ValueTask<DbConnection>> open, VisionaryDialect dialect, string describe)
    {
        _open = open;
        Dialect = dialect;
        Describe = describe;
        _gate = dialect == VisionaryDialect.Sqlite ? new SemaphoreSlim(1, 1) : null;
    }

    public VisionaryDialect Dialect { get; }

    public bool StringParams => false;

    public string Describe { get; }

    public async Task ExecScriptAsync(string script, CancellationToken ct)
    {
        await using var lease = await OpenAsync(ct).ConfigureAwait(false);
        foreach (var sql in Schema.Statements(script))
        {
            await using var cmd = lease.Connection.CreateCommand();
            cmd.CommandText = sql;
            try
            {
                await cmd.ExecuteNonQueryAsync(ct).ConfigureAwait(false);
            }
            catch (DbException e) when (Dialect == VisionaryDialect.MySql && IsDuplicateIndex(e))
            {
                // MySQL has no CREATE INDEX IF NOT EXISTS; an existing index is fine.
            }
        }
    }

    public async Task BatchAsync(IReadOnlyList<Stmt> stmts, CancellationToken ct)
    {
        if (stmts.Count == 0)
        {
            return;
        }
        await using var lease = await OpenAsync(ct).ConfigureAwait(false);
        await using var tx = await lease.Connection.BeginTransactionAsync(ct).ConfigureAwait(false);
        foreach (var st in stmts)
        {
            await using var cmd = Command(lease.Connection, st.Sql, st.Args);
            cmd.Transaction = tx;
            await cmd.ExecuteNonQueryAsync(ct).ConfigureAwait(false);
        }
        await tx.CommitAsync(ct).ConfigureAwait(false);
    }

    public async Task<long> ExecChangesAsync(string sql, object?[] args, CancellationToken ct)
    {
        await using var lease = await OpenAsync(ct).ConfigureAwait(false);
        await using var cmd = Command(lease.Connection, sql, args);
        return await cmd.ExecuteNonQueryAsync(ct).ConfigureAwait(false);
    }

    public async Task<List<string>> ColumnsAsync(string query, CancellationToken ct)
    {
        await using var lease = await OpenAsync(ct).ConfigureAwait(false);
        await using var cmd = lease.Connection.CreateCommand();
        cmd.CommandText = query;
        var names = new List<string>();
        await using var reader = await cmd.ExecuteReaderAsync(ct).ConfigureAwait(false);
        while (await reader.ReadAsync(ct).ConfigureAwait(false))
        {
            names.Add(Convert.ToString(reader.GetValue(0), CultureInfo.InvariantCulture) ?? "");
        }
        return names;
    }

    public async Task<List<string?[]>> RowsAsync(string query, CancellationToken ct)
    {
        await using var lease = await OpenAsync(ct).ConfigureAwait(false);
        await using var cmd = lease.Connection.CreateCommand();
        cmd.CommandText = query;
        var rows = new List<string?[]>();
        await using var reader = await cmd.ExecuteReaderAsync(ct).ConfigureAwait(false);
        while (await reader.ReadAsync(ct).ConfigureAwait(false))
        {
            var row = new string?[reader.FieldCount];
            for (var i = 0; i < row.Length; i++)
            {
                row[i] = reader.IsDBNull(i) ? null : Convert.ToString(reader.GetValue(i), CultureInfo.InvariantCulture);
            }
            rows.Add(row);
        }
        return rows;
    }

    private async Task<Lease> OpenAsync(CancellationToken ct)
    {
        if (_gate is not null)
        {
            await _gate.WaitAsync(ct).ConfigureAwait(false);
        }
        try
        {
            var conn = await _open(ct).ConfigureAwait(false);
            return new Lease(conn, _gate);
        }
        catch
        {
            _gate?.Release();
            throw;
        }
    }

    /// <summary>
    /// Builds a command with positional parameters: "$1, $2" with unnamed parameters for Npgsql,
    /// "@p1, @p2" elsewhere (Microsoft.Data.Sqlite and MySqlConnector both bind names).
    /// </summary>
    private DbCommand Command(DbConnection conn, string sql, object?[] args)
    {
        var cmd = conn.CreateCommand();
        cmd.CommandText = Rebind(sql);
        for (var i = 0; i < args.Length; i++)
        {
            var p = cmd.CreateParameter();
            if (Dialect != VisionaryDialect.Postgres)
            {
                p.ParameterName = "@p" + (i + 1).ToString(CultureInfo.InvariantCulture);
            }
            p.Value = args[i] ?? DBNull.Value;
            cmd.Parameters.Add(p);
        }
        return cmd;
    }

    /// <summary>Turns "?" placeholders into the dialect's form, leaving quoted text alone.</summary>
    private string Rebind(string sql)
    {
        if (!sql.Contains('?'))
        {
            return sql;
        }
        var prefix = Dialect == VisionaryDialect.Postgres ? "$" : "@p";
        var b = new StringBuilder(sql.Length + 32);
        var n = 0;
        char quote = '\0';
        foreach (var c in sql)
        {
            if (quote != '\0')
            {
                if (c == quote)
                {
                    quote = '\0';
                }
            }
            else if (c is '\'' or '"')
            {
                quote = c;
            }
            else if (c == '?')
            {
                b.Append(prefix).Append(++n);
                continue;
            }
            b.Append(c);
        }
        return b.ToString();
    }

    private static bool IsDuplicateIndex(DbException e) =>
        e.Message.Contains("Duplicate key name", StringComparison.OrdinalIgnoreCase) ||
        e.Message.Contains("1061", StringComparison.Ordinal) ||
        e.GetType().GetProperty("Number")?.GetValue(e) is 1061 ||
        e.GetType().GetProperty("ErrorCode")?.GetValue(e) is 1061;

    /// <summary>An open connection plus the SQLite gate it holds, released together.</summary>
    private sealed class Lease : IAsyncDisposable
    {
        private readonly SemaphoreSlim? _gate;

        public Lease(DbConnection connection, SemaphoreSlim? gate)
        {
            Connection = connection;
            _gate = gate;
        }

        public DbConnection Connection { get; }

        public async ValueTask DisposeAsync()
        {
            try
            {
                await Connection.DisposeAsync().ConfigureAwait(false);
            }
            finally
            {
                _gate?.Release();
            }
        }
    }
}
