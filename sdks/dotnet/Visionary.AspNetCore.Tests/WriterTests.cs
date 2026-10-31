using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Threading;
using System.Threading.Tasks;
using Microsoft.Data.Sqlite;
using Microsoft.Extensions.Logging.Abstractions;
using Microsoft.Extensions.Options;
using Xunit;

namespace Styropyr0.Visionary.Tests;

[Collection(BackgroundTests.AgentCollection)]
public sealed class WriterTests : IDisposable
{
    private readonly string _db = Path.Combine(Path.GetTempPath(), $"visionary-writer-{Guid.NewGuid():N}.db");
    private readonly List<VisionaryAgent> _agents = new();

    public void Dispose()
    {
        foreach (var a in _agents)
        {
            a.Dispose();
        }
        SqliteConnection.ClearAllPools();
        foreach (var f in new[] { _db, _db + "-wal", _db + "-shm" })
        {
            try
            {
                File.Delete(f);
            }
            catch (IOException)
            {
            }
        }
    }

    [Fact]
    public void MultiRowInsertsSplitAtTheParameterLimit()
    {
        var store = new FakeStore(VisionaryDialect.Postgres, stringParams: false);
        var sql = Schema.InsertSql("app_logs", Schema.LogColumns, false);
        var width = Schema.LogColumns.Length;
        var rows = Enumerable.Range(0, 3000).Select(i => new Stmt(sql, Row(width, i))).ToList();
        rows.Add(new Stmt(Schema.InsertSql("spans", Schema.SpanColumns, false), Row(Schema.SpanColumns.Length, 0)));

        var stmts = Batches.Combine(store, rows);

        var per = Batches.MaxParamsPerStatement / width;
        Assert.Equal(new[] { per, per, 3000 - 2 * per, 1 }, stmts.Select(s => s.Rows));
        Assert.Equal(3001, Batches.RowCount(stmts));
        foreach (var s in stmts)
        {
            Assert.True(s.Args.Length <= Batches.MaxParamsPerStatement);
            Assert.Equal(s.Args.Length, s.Sql.Count(c => c == '?'));
            Assert.Equal(s.Rows, s.Sql.Split("), (").Length);
            Assert.StartsWith("INSERT INTO ", s.Sql);
        }
        Assert.Equal(per, stmts[1].Args[0]);
        Assert.Null(stmts[0].Args[1]);
    }

    [Fact]
    public void SqliteKeepsStatementsSmall()
    {
        var store = new FakeStore(VisionaryDialect.Sqlite, stringParams: false);
        var sql = Schema.InsertSql("spans", Schema.SpanColumns, false);
        var stmts = Batches.Combine(store, Enumerable.Range(0, 500).Select(i => new Stmt(sql, Row(Schema.SpanColumns.Length, i))).ToList());
        Assert.All(stmts, s => Assert.True(s.Args.Length <= Batches.MaxSqliteParams));
        Assert.Equal(500, Batches.RowCount(stmts));
        Assert.Equal(1, Batches.Flushers(store));
        Assert.Equal(Batches.ParallelFlushes, Batches.Flushers(new FakeStore(VisionaryDialect.MySql, false)));
    }

    [Fact]
    public async Task D1KeepsOneInsertPerRow()
    {
        var o = new VisionaryOptions { D1AccountId = "acct", D1DatabaseId = "db", D1ApiToken = "token", D1ApiBase = "http://127.0.0.1:9", BatchSize = 500 };
        var agent = new VisionaryAgent(Options.Create(o), NullLoggerFactory.Instance);
        _agents.Add(agent);
        Assert.IsType<D1Store>(agent.Store);
        Assert.Equal(Batches.D1Size, Batches.Size(agent.Store, o.BatchSize));
        Assert.Equal(1, Batches.Flushers(agent.Store));

        var now = DateTime.UtcNow;
        var items = Enumerable.Range(0, 30).Select(i => (WriteItem)new RequestEntry
        {
            RequestId = "r" + i, StartedAt = now, Method = "GET", Path = "/p", Status = 200,
            RequestHeaders = Array.Empty<KeyValuePair<string, string>>(),
            Spans = new[] { new SpanRecord { StartedAt = now, Kind = "db", Name = "q" } },
        }).ToList();
        var stmts = await new RowBuilder(agent).BuildAsync(items, CancellationToken.None);
        Assert.Equal(60, stmts.Count);
        Assert.All(stmts, s => Assert.Equal(1, s.Rows));
        Assert.All(stmts, s => Assert.Contains("VALUES (NULLIF(?, ''), ", s.Sql));
    }

    [Fact]
    public async Task WritesNullsAndCountsRowsOnSqlite()
    {
        var o = new VisionaryOptions { DatabaseUrl = "sqlite:///" + _db.Replace('\\', '/'), SystemMetrics = false };
        var agent = new VisionaryAgent(Options.Create(o), NullLoggerFactory.Instance);
        _agents.Add(agent);
        var writer = new TelemetryWriter(agent);
        await writer.StartAsync(CancellationToken.None);
        var now = DateTime.UtcNow;
        for (var i = 0; i < 700; i++)
        {
            var id = "r" + i;
            agent.Enqueue(new RequestEntry
            {
                RequestId = id, StartedAt = now, Method = "GET", Path = "/p", Status = 200, DurationMs = 1,
                Route = i % 2 == 0 ? "/p" : null,
                RequestHeaders = Array.Empty<KeyValuePair<string, string>>(),
                Spans = new[] { new SpanRecord { StartedAt = now, RequestId = id, Kind = "db", Name = "q", RowCount = i % 3 == 0 ? null : i } },
            });
        }
        var deadline = DateTime.UtcNow.AddSeconds(15);
        while (agent.Writes.Written < 1400 && DateTime.UtcNow < deadline)
        {
            await Task.Delay(50);
        }
        await writer.StopAsync(CancellationToken.None);

        var rows = await agent.Store!.RowsAsync(
            "SELECT (SELECT COUNT(*) FROM api_logs), (SELECT COUNT(*) FROM api_logs WHERE route IS NULL), " +
            "(SELECT COUNT(*) FROM spans), (SELECT COUNT(*) FROM spans WHERE row_count IS NULL)", CancellationToken.None);
        Assert.Equal(new[] { "700", "350", "700", "234" }, rows[0]);
        Assert.Equal(1400, agent.Writes.Written);
        Assert.Equal(0, agent.Writes.Dropped);
        var mode = await agent.Store.RowsAsync("PRAGMA journal_mode", CancellationToken.None);
        Assert.Equal("wal", mode[0][0]);
    }

    private static object?[] Row(int width, int n)
    {
        var row = new object?[width];
        row[0] = n;
        for (var i = 2; i < width; i++)
        {
            row[i] = "v";
        }
        return row;
    }

    private sealed class FakeStore : IStore
    {
        public FakeStore(VisionaryDialect dialect, bool stringParams)
        {
            Dialect = dialect;
            StringParams = stringParams;
        }

        public VisionaryDialect Dialect { get; }
        public bool StringParams { get; }
        public string Describe => "fake";
        public Task ExecScriptAsync(string script, CancellationToken ct) => Task.CompletedTask;
        public Task BatchAsync(IReadOnlyList<Stmt> stmts, CancellationToken ct) => Task.CompletedTask;
        public Task<long> ExecChangesAsync(string sql, object?[] args, CancellationToken ct) => Task.FromResult(0L);
        public Task<List<string>> ColumnsAsync(string query, CancellationToken ct) => Task.FromResult(new List<string>());
        public Task<List<string?[]>> RowsAsync(string query, CancellationToken ct) => Task.FromResult(new List<string?[]>());
    }
}
