using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Linq;
using System.Net.Http;
using System.Threading.Tasks;
using Microsoft.AspNetCore.Builder;
using Microsoft.AspNetCore.Hosting;
using Microsoft.AspNetCore.Http;
using Microsoft.Data.Sqlite;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Logging.Abstractions;
using Microsoft.Extensions.Options;
using Xunit;

namespace Styropyr0.Visionary.Tests;

[Collection(BackgroundTests.AgentCollection)]
public sealed class TraceTests : IDisposable
{
    private const string InTrace = "4bf92f3577b34da6a3ce929d0e0e4736";
    private const string InParent = "00f067aa0ba902b7";

    private readonly string _db = Path.Combine(Path.GetTempPath(), $"visionary-trace-{Guid.NewGuid():N}.db");
    private readonly List<IDisposable> _owned = new();
    private readonly Activity? _activity = Activity.Current;

    public TraceTests()
    {
        Activity.Current = null;
        TraceContext.Current = null;
    }

    public void Dispose()
    {
        foreach (var d in _owned)
        {
            d.Dispose();
        }
        Activity.Current = _activity;
        TraceContext.Current = null;
        SqliteConnection.ClearAllPools();
        try
        {
            File.Delete(_db);
        }
        catch (IOException)
        {
        }
    }

    private VisionaryAgent Agent()
    {
        var o = new VisionaryOptions { DatabaseUrl = "sqlite:///" + _db.Replace('\\', '/'), SpanThresholdMs = 0 };
        var agent = new VisionaryAgent(Options.Create(o), NullLoggerFactory.Instance);
        _owned.Add(agent);
        return agent;
    }

    private static List<SpanRecord> Queued(VisionaryAgent agent)
    {
        var spans = new List<SpanRecord>();
        while (agent.Queue.Reader.TryRead(out var item))
        {
            if (item is SpanRecord s)
            {
                spans.Add(s);
            }
        }
        return spans;
    }

    [Fact]
    public void ParseTraceparent()
    {
        Assert.Equal(new SpanContext(InTrace, InParent, "01"), TraceContext.Parse($"00-{InTrace}-{InParent}-01"));
        Assert.NotNull(TraceContext.Parse($"01-{InTrace}-{InParent}-00-extra"));
        foreach (var bad in new[]
        {
            "", "00-xyz-1-1",
            $"00-00000000000000000000000000000000-{InParent}-01",
            $"00-{InTrace}-0000000000000000-01",
            $"ff-{InTrace}-{InParent}-01",
            $"00-{InTrace.ToUpperInvariant()}-{InParent}-01",
            $"00-{InTrace}-{InParent}-01-extra",
        })
        {
            Assert.Null(TraceContext.Parse(bad));
        }
    }

    [Fact]
    public void NewIds()
    {
        var (t, s) = (TraceContext.NewTraceId(), TraceContext.NewSpanId());
        Assert.Matches("^[0-9a-f]{32}$", t);
        Assert.Matches("^[0-9a-f]{16}$", s);
        Assert.NotEqual(t, TraceContext.NewTraceId());
    }

    [Fact]
    public void RequestContinuesTheHeaderOrStartsATrace()
    {
        var ctx = new DefaultHttpContext();
        ctx.Request.Headers["traceparent"] = $"00-{InTrace}-{InParent}-00";
        ctx.Request.Headers["tracestate"] = "vendor=1";
        var trace = TraceContext.BeginRequest(ctx);
        Assert.Equal(InTrace, trace.Span.TraceId);
        Assert.Equal(InParent, trace.ParentSpanId);
        Assert.NotEqual(InParent, trace.Span.SpanId);
        Assert.Equal(("00", "vendor=1"), (trace.Span.Flags, trace.Span.State));
        Assert.Equal(trace.Span, TraceContext.Current);

        var bad = new DefaultHttpContext();
        bad.Request.Headers["traceparent"] = "garbage";
        bad.Request.Headers["tracestate"] = "vendor=1";
        var fresh = TraceContext.BeginRequest(bad);
        Assert.NotEqual(InTrace, fresh.Span.TraceId);
        Assert.Null(fresh.ParentSpanId);
        Assert.Equal(("01", (string?)null), (fresh.Span.Flags, fresh.Span.State));
    }

    [Fact]
    public void RequestTakesTheActivityIds()
    {
        using var activity = new Activity("Microsoft.AspNetCore.Hosting.HttpRequestIn")
            .SetParentId($"00-{InTrace}-{InParent}-01").Start();
        activity.TraceStateString = "vendor=2";
        var ctx = new DefaultHttpContext();
        ctx.Request.Headers["traceparent"] = $"00-{TraceContext.NewTraceId()}-{InParent}-01";
        var trace = TraceContext.BeginRequest(ctx);
        Assert.Equal(new SpanContext(InTrace, activity.SpanId.ToHexString(), "01", "vendor=2"), trace.Span);
        Assert.Equal(InParent, trace.ParentSpanId);
    }

    [Fact]
    public void SpansNestUnderTheCurrentSpan()
    {
        var agent = Agent();
        var server = new SpanContext(InTrace, TraceContext.NewSpanId());
        TraceContext.Current = server;
        using (var custom = Visionary.StartSpan("custom", "build report"))
        {
            agent.RecordSpan("db", "SELECT orders", null, null, DateTime.UtcNow, 1, null, null);
        }
        Assert.Equal(server, TraceContext.Current);
        agent.RecordSpan("cache", "GET session", null, null, DateTime.UtcNow, 1, null, null);

        var spans = Queued(agent);
        var report = Assert.Single(spans, s => s.Name == "build report");
        var db = Assert.Single(spans, s => s.Kind == "db");
        var cache = Assert.Single(spans, s => s.Kind == "cache");
        Assert.All(spans, s => Assert.Equal(InTrace, s.TraceId));
        Assert.Equal(server.SpanId, report.ParentSpanId);
        Assert.Equal(report.SpanId, db.ParentSpanId);
        Assert.Equal(server.SpanId, cache.ParentSpanId);
        Assert.Equal(3, spans.Select(s => s.SpanId).Distinct().Count());
    }

    [Fact]
    public void ACallOutsideAnyTraceStartsOne()
    {
        var agent = Agent();
        agent.RecordSpan("db", "SELECT 1", null, null, DateTime.UtcNow, 1, null, null);
        var span = Assert.Single(Queued(agent));
        Assert.Matches("^[0-9a-f]{32}$", span.TraceId!);
        Assert.Null(span.ParentSpanId);
    }

    [Fact]
    public async Task BackgroundKeepsTheTrace()
    {
        var agent = Agent();
        var server = new SpanContext(InTrace, TraceContext.NewSpanId());
        TraceContext.Current = server;
        await Visionary.Background("send email", async _ =>
        {
            await Task.Yield();
            agent.RecordSpan("http", "POST mail", null, null, DateTime.UtcNow, 1, null, null);
        });
        var spans = Queued(agent);
        var bg = Assert.Single(spans, s => s.Kind == "background");
        var inner = Assert.Single(spans, s => s.Kind == "http");
        Assert.Equal((InTrace, server.SpanId), (bg.TraceId, bg.ParentSpanId));
        Assert.Equal((InTrace, bg.SpanId), (inner.TraceId, inner.ParentSpanId));
    }

    [Fact]
    public void OutboundSetsTheHeaderWithoutAnActivity()
    {
        var server = new SpanContext(InTrace, TraceContext.NewSpanId(), "01", "vendor=1");
        TraceContext.Current = server;
        var req = new HttpRequestMessage(HttpMethod.Get, "http://stock/items");
        var ids = SpanListeners.OutboundTrace(req);
        Assert.Equal((InTrace, server.SpanId), (ids.TraceId, ids.ParentSpanId));
        Assert.Equal($"00-{InTrace}-{ids.SpanId}-01", Assert.Single(req.Headers.GetValues("traceparent")));
        Assert.Equal("vendor=1", Assert.Single(req.Headers.GetValues("tracestate")));

        var own = new HttpRequestMessage(HttpMethod.Get, "http://stock/items");
        own.Headers.TryAddWithoutValidation("traceparent", $"00-{TraceContext.NewTraceId()}-{InParent}-01");
        SpanListeners.OutboundTrace(own);
        Assert.EndsWith($"-{InParent}-01", Assert.Single(own.Headers.GetValues("traceparent")));
    }

    [Fact]
    public void LogLinesTakeTheCurrentSpan()
    {
        var o = new VisionaryOptions { DatabaseUrl = "sqlite:///" + _db.Replace('\\', '/'), LogsAdded = true };
        var agent = new VisionaryAgent(Options.Create(o), NullLoggerFactory.Instance);
        _owned.Add(agent);
        var services = new ServiceCollection();
        services.AddLogging(b => Microsoft.Extensions.Logging.VisionaryLoggingBuilderExtensions.AddVisionary(b));
        var sp = services.BuildServiceProvider();
        _owned.Add(sp);
        var server = new SpanContext(InTrace, TraceContext.NewSpanId());
        TraceContext.Current = server;
        using var other = new Activity("app").SetIdFormat(ActivityIdFormat.W3C).Start();
        Microsoft.Extensions.Logging.LoggerExtensions.LogWarning(sp.GetRequiredService<Microsoft.Extensions.Logging.ILoggerFactory>().CreateLogger("App"), "traced");
        Assert.True(agent.Logs!.Queue.Reader.TryRead(out var line));
        Assert.Equal((InTrace, server.SpanId), (line.TraceId, line.SpanId));
    }

    // /b should land in the caller's trace under /a's http span
    [Fact]
    public async Task ATraceSpansTwoRequests()
    {
        var received = new List<string>();
        string? activitySpan = null;
        var builder = WebApplication.CreateBuilder();
        builder.WebHost.UseUrls("http://127.0.0.1:0");
        builder.Services.AddVisionary(o =>
        {
            o.DatabaseUrl = "sqlite:///" + _db.Replace('\\', '/');
            o.SystemMetrics = false;
            o.SpanThresholdMs = 0;
        });
        builder.Services.AddHttpClient();
        var app = builder.Build();
        app.UseVisionary();
        app.MapGet("/a", async (HttpContext ctx, IHttpClientFactory clients) =>
        {
            activitySpan = Activity.Current?.SpanId.ToHexString();
            using var span = Visionary.StartSpan("custom", "fetch b");
            var url = $"{ctx.Request.Scheme}://{ctx.Request.Host}/b";
            return await clients.CreateClient().GetStringAsync(url);
        });
        app.MapGet("/b", (HttpContext ctx) =>
        {
            received.Add(string.Join("|", ctx.Request.Headers["traceparent"].ToArray()));
            return "b";
        });
        await app.StartAsync();
        try
        {
            // Stopping before the tables exist would leave nothing to read
            for (var i = 0; i < 100 && Rows("SELECT name FROM sqlite_master WHERE name = 'idx_spans_trace_id'").Count == 0; i++)
            {
                await Task.Delay(50);
            }
            var addr = app.Urls.First();
            using var client = new HttpClient();
            using var req = new HttpRequestMessage(HttpMethod.Get, addr + "/a");
            req.Headers.TryAddWithoutValidation("traceparent", $"00-{InTrace}-{InParent}-01");
            var res = await client.SendAsync(req);
            Assert.Equal("b", await res.Content.ReadAsStringAsync());

            TraceContext.Current = new SpanContext(InTrace, InParent);
            Assert.Equal("b", await client.GetStringAsync(addr + "/b"));
            TraceContext.Current = null;
        }
        finally
        {
            await app.StopAsync();
            await app.DisposeAsync();
        }

        var requests = Rows("SELECT path, trace_id, span_id, parent_span_id FROM api_logs ORDER BY id").ToList();
        var spans = Rows("SELECT kind, name, trace_id, span_id, parent_span_id FROM spans").ToList();
        var a = Assert.Single(requests, r => r[0] == "/a");
        var b = requests.First(r => r[0] == "/b");
        var custom = Assert.Single(spans, s => s[0] == "custom");
        // The test's own call to /a is an http span too
        var http = Assert.Single(spans, s => s[0] == "http" && s[4] == custom[3]);

        Assert.NotNull(activitySpan);
        Assert.Equal(new[] { InTrace, activitySpan, InParent }, a[1..]);
        Assert.Equal(new[] { InTrace, a[2] }, new[] { custom[2], custom[4] });
        Assert.Equal(new[] { InTrace, custom[3] }, new[] { http[2], http[4] });
        Assert.Equal(new[] { InTrace, http[3] }, new[] { b[1], b[3] });
        Assert.Equal($"00-{InTrace}-{http[3]}-01", received[0]);
        var direct = Assert.Single(spans, s => s[0] == "http" && s[4] == InParent);
        Assert.Equal($"00-{InTrace}-{direct[3]}-01", received[1]);
    }

    private List<string?[]> Rows(string sql)
    {
        using var conn = new SqliteConnection("Data Source=" + _db);
        conn.Open();
        using var cmd = conn.CreateCommand();
        cmd.CommandText = sql;
        using var reader = cmd.ExecuteReader();
        var rows = new List<string?[]>();
        while (reader.Read())
        {
            var row = new string?[reader.FieldCount];
            for (var i = 0; i < row.Length; i++)
            {
                row[i] = reader.IsDBNull(i) ? null : reader.GetString(i);
            }
            rows.Add(row);
        }
        return rows;
    }
}
