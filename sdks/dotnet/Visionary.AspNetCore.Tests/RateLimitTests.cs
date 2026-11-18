using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using Microsoft.AspNetCore.Http;
using Microsoft.Extensions.Logging.Abstractions;
using Microsoft.Extensions.Options;
using Xunit;

namespace Styropyr0.Visionary.Tests;

[Collection(BackgroundTests.AgentCollection)]
public sealed class RateLimitTests
{
    private static DateTime At(double t) => DateTime.UnixEpoch.AddTicks((long)Math.Round(t * 1e7));

    private static string CasesFile()
    {
        for (var dir = new DirectoryInfo(AppContext.BaseDirectory); dir is not null; dir = dir.Parent)
        {
            var path = Path.Combine(dir.FullName, "sdks", "go", "ratelimit", "testdata", "cases.json");
            if (File.Exists(path))
            {
                return path;
            }
        }
        throw new FileNotFoundException("sdks/go/ratelimit/testdata/cases.json not found above " + AppContext.BaseDirectory);
    }

    private static string Str(JsonElement e, string name, string def = "") =>
        e.TryGetProperty(name, out var v) && v.ValueKind == JsonValueKind.String ? v.GetString()! : def;

    private static Dictionary<string, string> Map(JsonElement e, string name)
    {
        var m = new Dictionary<string, string>();
        if (e.TryGetProperty(name, out var v) && v.ValueKind == JsonValueKind.Object)
        {
            foreach (var p in v.EnumerateObject())
            {
                m[p.Name] = p.Value.GetString()!;
            }
        }
        return m;
    }

    // The cases every SDK runs; see "Shared test cases" in sdks/SPEC.md.
    [Fact]
    public void SharedCases()
    {
        using var doc = JsonDocument.Parse(File.ReadAllText(CasesFile()));
        var ran = 0;
        foreach (var c in doc.RootElement.EnumerateArray())
        {
            var name = Str(c, "name");
            var limiter = new RateLimits(Str(c, "service"));
            var records = new List<string>();
            limiter.Trace = (id, outcome) => records.Add(id + ":" + outcome);
            limiter.Load(RateLimitConfig.Parse(c.GetProperty("config").GetRawText()));
            var i = 0;
            foreach (var s in c.GetProperty("steps").EnumerateArray())
            {
                records.Clear();
                var headers = Map(s, "headers");
                var query = Map(s, "query");
                var req = new LimitRequest
                {
                    Method = Str(s, "method", "GET"),
                    Path = Str(s, "path", "/"),
                    RemoteIp = Str(s, "remote_ip", "203.0.113.1"),
                    Header = n => headers.FirstOrDefault(h => string.Equals(h.Key, n, StringComparison.OrdinalIgnoreCase)).Value ?? "",
                    Query = n => query.TryGetValue(n, out var v) ? v : "",
                };
                var now = At(s.GetProperty("t").GetDouble());
                var st = limiter.CheckRequest(req, now);
                var user = s.TryGetProperty("user", out var u) && u.ValueKind == JsonValueKind.Object
                    ? new LimitUser(Str(u, "id"), Str(u, "role"))
                    : new LimitUser(null, null);
                limiter.CheckUser(st, req, user, Map(s, "attrs"), now);
                var d = st.Denied;
                var where = $"{name}, step {i}";
                var expect = s.GetProperty("expect");
                Assert.True(expect.GetProperty("status").GetInt32() == (d?.Status ?? 0), $"{where}: status {d?.Status ?? 0}");
                if (d is not null)
                {
                    if (expect.TryGetProperty("rule", out var rule))
                    {
                        Assert.True(rule.GetString() == d.RuleId, $"{where}: rule {d.RuleId}");
                    }
                    if (expect.TryGetProperty("retry_after", out var retry))
                    {
                        Assert.True(retry.GetInt32() == d.RetryAfter, $"{where}: retry after {d.RetryAfter}");
                    }
                    if (expect.TryGetProperty("body", out var body))
                    {
                        Assert.True(body.GetString() == d.Body, $"{where}: body {d.Body}");
                    }
                    if (expect.TryGetProperty("content_type", out var type))
                    {
                        Assert.True(type.GetString() == d.ContentType, $"{where}: content type {d.ContentType}");
                    }
                }
                var want = s.TryGetProperty("records", out var r) ? r.EnumerateArray().Select(x => x.GetString()!).ToList() : new List<string>();
                Assert.True(want.SequenceEqual(records), $"{where}: records [{string.Join(", ", records)}], want [{string.Join(", ", want)}]");
                i++;
            }
            ran++;
        }
        Assert.Equal(25, ran);
    }

    [Fact]
    public void CountersSurviveReloadUnlessShapeChanges()
    {
        var limiter = new RateLimits(null);
        const string rule = "{{\"rules\":[{{\"id\":\"r\",\"name\":\"{0}\",\"action\":\"limit\",\"limit\":1,\"window_s\":{1},\"algorithm\":\"fixed\"}}]}}";
        limiter.Load(RateLimitConfig.Parse(string.Format(rule, "a", 60)));
        var req = new LimitRequest { RemoteIp = "1.1.1.1" };
        limiter.CheckRequest(req, At(6000));
        limiter.Load(RateLimitConfig.Parse(string.Format(rule, "renamed", 60)));
        Assert.NotNull(limiter.CheckRequest(req, At(6000)).Denied);
        limiter.Load(RateLimitConfig.Parse(string.Format(rule, "renamed", 30)));
        Assert.Null(limiter.CheckRequest(req, At(6000)).Denied);
    }

    [Fact]
    public void RecordsAggregateRedactAndOverflow()
    {
        var limiter = new RateLimits("api");
        limiter.Load(RateLimitConfig.Parse("{\"rules\":[{\"id\":\"k\",\"name\":\"Keys\",\"action\":\"limit\",\"key\":\"header:X-Api-Key\",\"limit\":0,\"window_s\":60}]}"));
        var req = new LimitRequest { Method = "post", Path = "/v1/x", RemoteIp = "1.1.1.1", Header = n => n == "x-api-key" ? "secret-value" : "" };
        for (var i = 0; i < 3; i++)
        {
            limiter.CheckRequest(req, At(6000));
        }
        var ev = Assert.Single(limiter.Drain());
        Assert.Equal((3L, "limited", "POST", "Keys"), (ev.Hits, ev.Outcome, ev.Method, ev.RuleName));
        Assert.StartsWith("sha256:", ev.KeyValue);
        Assert.Equal(19, ev.KeyValue.Length);
        Assert.Empty(limiter.Drain());

        limiter.Load(RateLimitConfig.Parse("{\"rules\":[{\"id\":\"b\",\"action\":\"block\"}]}"));
        for (var i = 0; i < RateLimits.MaxAggregates + 5; i++)
        {
            limiter.CheckRequest(new LimitRequest { RemoteIp = $"10.0.{i / 250}.{i % 250}" }, At(1));
        }
        var all = limiter.Drain();
        Assert.Equal(RateLimits.MaxAggregates + 1, all.Count);
        Assert.Equal(("(other)", "", 5L), (all[^1].KeyValue, all[^1].Ip, all[^1].Hits));
    }

    [Fact]
    public void CounterCapDropsIdleCounters()
    {
        var limiter = new RateLimits(null);
        limiter.Load(RateLimitConfig.Parse("{\"rules\":[{\"id\":\"c\",\"action\":\"limit\",\"limit\":1,\"window_s\":1,\"algorithm\":\"fixed\"}]}"));
        for (var i = 0; i < RateLimits.MaxCounters; i++)
        {
            limiter.CheckRequest(new LimitRequest { RemoteIp = "k" + i }, At(100));
        }
        // Everything above is idle two windows later; a new key sweeps it instead of resetting the busy one.
        limiter.CheckRequest(new LimitRequest { RemoteIp = "busy" }, At(103));
        Assert.NotNull(limiter.CheckRequest(new LimitRequest { RemoteIp = "busy" }, At(103)).Denied);
    }

    [Fact]
    public void ParseKeepsGoodRules()
    {
        var c = RateLimitConfig.Parse("{\"rules\":[{\"id\":\"a\",\"action\":\"block\",\"limit\":\"x\"},{\"id\":\"b\",\"action\":\"block\"}]}");
        Assert.Equal("b", Assert.Single(c.Rules).Id);
        Assert.ThrowsAny<JsonException>(() => RateLimitConfig.Parse("{\"rules\":"));
    }

    private VisionaryAgent Agent(RateLimitStore store, bool on = true) => new(
        Options.Create(new VisionaryOptions { Service = "api", Instance = "i-1", RateLimit = on }),
        NullLoggerFactory.Instance, null, store);

    private static DefaultHttpContext Http(string method, string target)
    {
        var http = new DefaultHttpContext();
        var q = target.IndexOf('?');
        http.Request.Method = method;
        http.Request.Path = q < 0 ? target : target[..q];
        http.Request.QueryString = q < 0 ? QueryString.Empty : new QueryString(target[q..]);
        http.Response.Body = new MemoryStream();
        http.Connection.RemoteIpAddress = System.Net.IPAddress.Parse("203.0.113.1");
        return http;
    }

    private static string BodyOf(HttpContext http)
    {
        http.Response.Body.Position = 0;
        return new StreamReader(http.Response.Body).ReadToEnd();
    }

    [Fact]
    public async Task MiddlewareEnforcesRateLimits()
    {
        var store = new RateLimitStore
        {
            Version = "v1",
            Config = """
                {"rules":[
                  {"id":"r1","name":"Export","action":"limit","methods":["POST"],"paths":["/export*"],"limit":1,"window_s":60,"algorithm":"fixed"},
                  {"id":"r2","action":"limit","key":"user","limit":0,"window_s":60,"match":{"attr:plan":["free"]}}
                ]}
                """,
        };
        using var agent = Agent(store);
        var poller = new RateLimitPoller(agent);
        await poller.PollAsync(CancellationToken.None);
        Assert.True(agent.RateLimits!.Active);

        var handled = 0;
        var repeated = true;
        var mw = new VisionaryMiddleware(async ctx =>
        {
            Visionary.SetUser(ctx, "u1");
            var attrs = new Dictionary<string, string> { ["plan"] = ctx.Request.Query["plan"].ToString() };
            if (await Visionary.RateLimitedAsync(ctx, attrs))
            {
                repeated &= await Visionary.RateLimitedAsync(ctx, attrs);
                return;
            }
            handled++;
            ctx.Response.StatusCode = 204;
        }, agent);
        async Task<HttpContext> Do(string method, string target)
        {
            var http = Http(method, target);
            await mw.InvokeAsync(http);
            return http;
        }

        Assert.Equal(204, (await Do("POST", "/export/1")).Response.StatusCode);
        var limited = await Do("POST", "/export/2");
        Assert.Equal(429, limited.Response.StatusCode);
        Assert.InRange(int.Parse(limited.Response.Headers.RetryAfter.ToString()), 1, 60);
        Assert.Equal("application/json", limited.Response.ContentType);
        Assert.Equal("{\"error\":{\"code\":\"rate_limited\",\"key\":\"r1\",\"message\":\"Too many requests\"}}", BodyOf(limited));
        Assert.Equal(1, handled);

        Assert.Equal(204, (await Do("GET", "/other?plan=pro")).Response.StatusCode);
        var free = await Do("GET", "/other?plan=free");
        Assert.Equal(429, free.Response.StatusCode);
        Assert.Contains("\"key\":\"r2\"", BodyOf(free));
        Assert.Equal(2, handled);
        Assert.True(repeated);

        // Limited requests are still logged.
        var logged = new List<int>();
        while (agent.Queue.Reader.TryRead(out var item))
        {
            if (item is RequestEntry e)
            {
                logged.Add(e.Status);
            }
        }
        Assert.Equal(new[] { 204, 429, 204, 429 }, logged);

        Assert.False(await Visionary.RateLimitedAsync(Http("GET", "/"), null));

        await poller.FlushAsync(CancellationToken.None);
        var stmts = store.Written;
        Assert.Single(stmts);
        Assert.Equal(2, stmts[0].Rows);
        Assert.StartsWith("INSERT INTO rate_limit_events (created_at, service, instance, rule_id, rule_name, outcome, key_value, ip, country, method, path, user_id, hits) VALUES (", stmts[0].Sql);
        var a = stmts[0].Args;
        Assert.Equal(26, a.Length);
        Assert.Equal(new object?[] { "api", "i-1", "r1", "Export", "limited", "203.0.113.1", "203.0.113.1", null, "POST", "/export/2", null, 1L }, a[1..13]);
        Assert.Equal(new object?[] { "api", "i-1", "r2", "r2", "limited", "u1", "203.0.113.1", null, "GET", "/other", "u1", 1L }, a[14..26]);
        Assert.Matches(@"^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$", (string)a[0]!);
    }

    [Fact]
    public async Task D1WritesOneStatementPerRowWithNullif()
    {
        var store = new RateLimitStore { StringParams = true, Version = "v1", Config = "{\"rules\":[{\"id\":\"b\",\"action\":\"block\"}]}" };
        using var agent = Agent(store);
        var poller = new RateLimitPoller(agent);
        await poller.PollAsync(CancellationToken.None);
        for (var i = 0; i < 120; i++)
        {
            agent.RateLimits!.CheckRequest(new LimitRequest { RemoteIp = "10.0.0." + i }, DateTime.UtcNow);
        }
        await poller.FlushAsync(CancellationToken.None);
        Assert.Equal(new[] { 50, 50, 20 }, store.Batches.Select(b => b.Count));
        Assert.Contains("VALUES (NULLIF(?, ''), NULLIF(?, '')", store.Written[0].Sql);
    }

    [Fact]
    public async Task ConfigIsReadOnlyWhenTheVersionChanges()
    {
        var store = new RateLimitStore { Version = "v1", Config = "{\"rules\":[{\"id\":\"b\",\"action\":\"block\"}]}" };
        using var agent = Agent(store);
        var poller = new RateLimitPoller(agent);
        await poller.PollAsync(CancellationToken.None);
        await poller.PollAsync(CancellationToken.None);
        Assert.Equal(1, store.ConfigReads);
        Assert.Equal(2, store.VersionReads);
        Assert.True(agent.RateLimits!.Active);

        // Not JSON: the rules in force stay.
        store.Version = "v2";
        store.Config = "{\"rules\":";
        await poller.PollAsync(CancellationToken.None);
        Assert.Equal(2, store.ConfigReads);
        Assert.True(agent.RateLimits.Active);

        // A failed read keeps them too.
        store.Fail = true;
        await poller.PollAsync(CancellationToken.None);
        Assert.True(agent.RateLimits.Active);
        store.Fail = false;

        store.Version = "v3";
        store.Config = "{\"rules\":[]}";
        await poller.PollAsync(CancellationToken.None);
        Assert.Equal(3, store.ConfigReads);
        Assert.False(agent.RateLimits.Active);

        store.Version = "v4";
        store.Config = "{\"rules\":[{\"id\":\"b\",\"action\":\"block\"}]}";
        await poller.PollAsync(CancellationToken.None);
        Assert.True(agent.RateLimits.Active);
        store.Version = null;
        await poller.PollAsync(CancellationToken.None);
        Assert.False(agent.RateLimits.Active);
    }

    [Fact]
    public void RateLimitOffMakesNoLimiter()
    {
        using var agent = Agent(new RateLimitStore(), on: false);
        Assert.Null(agent.RateLimits);
    }

    private sealed class RateLimitStore : IStore
    {
        private readonly object _lock = new();
        public string? Version { get; set; }
        public string Config { get; set; } = "";
        public bool Fail { get; set; }
        public int VersionReads;
        public int ConfigReads;
        public readonly List<Stmt> Written = new();
        public readonly List<IReadOnlyList<Stmt>> Batches = new();

        public VisionaryDialect Dialect => VisionaryDialect.Postgres;
        public bool StringParams { get; init; }
        public string Describe => "fake";
        public Task ExecScriptAsync(string script, CancellationToken ct) => Task.CompletedTask;

        public Task BatchAsync(IReadOnlyList<Stmt> stmts, CancellationToken ct)
        {
            lock (_lock)
            {
                Batches.Add(stmts);
                Written.AddRange(stmts);
            }
            return Task.CompletedTask;
        }

        public Task<long> ExecChangesAsync(string sql, object?[] args, CancellationToken ct) => Task.FromResult(0L);
        public Task<List<string>> ColumnsAsync(string query, CancellationToken ct) => Task.FromResult(new List<string>());

        public Task<List<string?[]>> RowsAsync(string query, CancellationToken ct)
        {
            if (Fail)
            {
                throw new InvalidOperationException("database away");
            }
            var rows = new List<string?[]>();
            if (query.Contains("'ratelimit.version'"))
            {
                Interlocked.Increment(ref VersionReads);
                if (Version is not null)
                {
                    rows.Add(new string?[] { Version });
                }
            }
            else if (query.Contains("'ratelimit.config'"))
            {
                Interlocked.Increment(ref ConfigReads);
                rows.Add(new string?[] { Config });
            }
            return Task.FromResult(rows);
        }
    }
}
