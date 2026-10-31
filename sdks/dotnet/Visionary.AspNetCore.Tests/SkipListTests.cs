using System;
using System.Collections.Generic;
using System.IO;
using System.Threading;
using System.Threading.Tasks;
using Microsoft.AspNetCore.Http;
using Microsoft.Data.Sqlite;
using Microsoft.Extensions.Logging.Abstractions;
using Microsoft.Extensions.Options;
using Xunit;

namespace Styropyr0.Visionary.Tests;

[Collection(BackgroundTests.AgentCollection)]
public sealed class SkipListTests : IDisposable
{
    private readonly string _db = Path.Combine(Path.GetTempPath(), $"visionary-skip-{Guid.NewGuid():N}.db");
    private readonly List<IDisposable> _owned = new();

    public void Dispose()
    {
        foreach (var d in _owned)
        {
            d.Dispose();
        }
        SqliteConnection.ClearAllPools();
        try
        {
            File.Delete(_db);
        }
        catch (IOException)
        {
        }
    }

    private VisionaryAgent Agent(string prefix = "")
    {
        var o = new VisionaryOptions { DatabaseUrl = "sqlite:///" + _db.Replace('\\', '/'), PathPrefix = prefix };
        var agent = new VisionaryAgent(Options.Create(o), NullLoggerFactory.Instance);
        _owned.Add(agent);
        return agent;
    }

    [Fact]
    public void ParseAndMatch()
    {
        var list = SkipList.Parse(" [\" /health \", \"\", \"  \", null, \"/static/*\", \"/metrics\"] ");
        Assert.Equal(new[] { "/health", "/static/*", "/metrics" }, list);
        foreach (var (path, want) in new[]
        {
            ("/health", true),
            ("/health/live", false),
            ("/static/app.js", true),
            ("/static/", true),
            ("/static", false),
            ("/Static/app.js", false),
            ("/metrics", true),
            ("/api", false),
        })
        {
            Assert.True(want == SkipList.Match(list, path), path);
        }
        Assert.True(SkipList.Match(SkipList.Parse("[\"*\"]"), "/anything"));

        foreach (var bad in new[] { "", "null", "{\"a\":1}", "\"/health\"", "[1]", "[\"/a\", 2]", "not json" })
        {
            Assert.Empty(SkipList.Parse(bad));
        }
    }

    [Fact]
    public async Task ReadKeepsLastListOnError()
    {
        var agent = Agent();
        var writer = new TelemetryWriter(agent);

        // No table yet: nothing skipped, and no error.
        await writer.ReadSkipAsync(CancellationToken.None);
        Assert.Empty(agent.Skip);

        using (var c = new SqliteConnection($"Data Source={_db}"))
        {
            c.Open();
            using var cmd = c.CreateCommand();
            cmd.CommandText = "CREATE TABLE visionary_settings (name TEXT PRIMARY KEY, value TEXT);" +
                "INSERT INTO visionary_settings VALUES ('requests.skip', '[\"/health\", \"/static/*\"]'), ('logs.level', 'debug');";
            cmd.ExecuteNonQuery();
        }
        await writer.ReadSkipAsync(CancellationToken.None);
        Assert.Equal(new[] { "/health", "/static/*" }, agent.Skip);

        using (var c = new SqliteConnection($"Data Source={_db}"))
        {
            c.Open();
            using var cmd = c.CreateCommand();
            cmd.CommandText = "DROP TABLE visionary_settings";
            cmd.ExecuteNonQuery();
        }
        await writer.ReadSkipAsync(CancellationToken.None);
        Assert.Equal(new[] { "/health", "/static/*" }, agent.Skip);
    }

    [Fact]
    public async Task SkippedRequestIsNotLogged()
    {
        var agent = Agent("/api");
        agent.Skip = SkipList.Parse("[\"/api/health\", \"/api/static/*\"]");
        Assert.True(agent.LogsPath("/api/users"));
        Assert.False(agent.LogsPath("/other"));

        foreach (var (path, logged) in new[] { ("/api/users", true), ("/api/health", false), ("/api/static/a.js", false) })
        {
            var mw = new VisionaryMiddleware(ctx =>
            {
                ctx.Response.StatusCode = 500;
                return Task.CompletedTask;
            }, agent);
            var http = new DefaultHttpContext();
            http.Request.Method = "GET";
            http.Request.Path = path;
            http.Request.QueryString = new QueryString("?x=1");
            await mw.InvokeAsync(http);

            Assert.Equal(logged, http.Response.Headers.ContainsKey(VisionaryMiddleware.RequestIdHeader));
            var queued = 0;
            while (agent.Queue.Reader.TryRead(out var item))
            {
                queued += item is RequestEntry ? 1 : 0;
            }
            Assert.Equal(logged ? 1 : 0, queued);
        }
        // Skipped requests still count toward throughput.
        Assert.Equal(3, agent.Http.Requests);
        Assert.Equal(3, agent.Http.Errors);
    }
}
