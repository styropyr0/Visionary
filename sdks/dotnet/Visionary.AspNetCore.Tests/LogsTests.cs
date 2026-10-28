using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Linq;
using System.Text;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using Microsoft.AspNetCore.Http;
using Microsoft.Data.Sqlite;
using Microsoft.Extensions.Configuration;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Logging;
using Microsoft.Extensions.Logging.Abstractions;
using Microsoft.Extensions.Options;
using Xunit;

namespace Styropyr0.Visionary.Tests;

[Collection(BackgroundTests.AgentCollection)]
public sealed class LogsTests : IDisposable
{
    private readonly string _db = Path.Combine(Path.GetTempPath(), $"visionary-logs-{Guid.NewGuid():N}.db");
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

    private (VisionaryAgent Agent, ILoggerFactory Loggers) Setup(params string[][] rows)
    {
        var o = new VisionaryOptions { DatabaseUrl = "sqlite:///" + _db.Replace('\\', '/'), Service = "api", LogsAdded = true };
        var agent = new VisionaryAgent(Options.Create(o), NullLoggerFactory.Instance);
        _owned.Add(agent);
        Assert.NotNull(agent.Logs);
        agent.Logs!.Settings = LogSettings.Parse(rows, Severity.Info);
        var services = new ServiceCollection();
        services.AddLogging(b => b.AddVisionary());
        var sp = services.BuildServiceProvider();
        _owned.Add(sp);
        return (agent, sp.GetRequiredService<ILoggerFactory>());
    }

    private static List<LogRecord> Queued(VisionaryAgent agent)
    {
        var list = new List<LogRecord>();
        while (agent.Logs!.Queue.Reader.TryRead(out var r))
        {
            list.Add(r);
        }
        return list;
    }

    private static string Messages(IEnumerable<LogRecord> recs) =>
        string.Join(",", recs.OrderBy(r => r.At).Select(r => r.Message));

    private static async Task Serve(VisionaryAgent agent, ILoggerFactory loggers, string route, int status, int tookMs, string? user,
        Action<ILogger> log, Exception? fail = null)
    {
        var logger = loggers.CreateLogger("App.Handler");
        var mw = new VisionaryMiddleware(async ctx =>
        {
            if (user is not null)
            {
                Visionary.SetUser(ctx, user);
            }
            Visionary.SetRoute(ctx, route);
            log(logger);
            if (tookMs > 0)
            {
                await Task.Delay(tookMs);
            }
            if (fail is not null)
            {
                throw fail;
            }
            ctx.Response.StatusCode = status;
        }, agent);
        var http = new DefaultHttpContext();
        http.Request.Method = "GET";
        http.Request.Path = route;
        try
        {
            await mw.InvokeAsync(http);
        }
        catch (Exception e) when (ReferenceEquals(e, fail))
        {
        }
    }

    // Spaced apart so their timestamps order them.
    private static void ThreeLines(ILogger l)
    {
        l.LogDebug("debug line");
        Thread.Sleep(2);
        l.LogInformation("info line");
        Thread.Sleep(2);
        l.LogWarning("warn line");
    }

    [Fact]
    public async Task KeepDebugOnlyForFailedRequests()
    {
        var (agent, loggers) = Setup();
        await Serve(agent, loggers, "/ok", 200, 0, null, ThreeLines);
        Assert.Equal("info line,warn line", Messages(Queued(agent)));

        await Serve(agent, loggers, "/fail", 500, 0, null, ThreeLines);
        var got = Queued(agent);
        Assert.Equal("debug line,info line,warn line", Messages(got));
        Assert.All(got, r => Assert.False(string.IsNullOrEmpty(r.RequestId)));

        await Serve(agent, loggers, "/throws", 200, 0, null, ThreeLines, new InvalidOperationException("boom"));
        Assert.Equal("debug line,info line,warn line", Messages(Queued(agent)));

        // A handled error captured in the request counts as a failure too.
        await Serve(agent, loggers, "/captured", 200, 0, null, l =>
        {
            ThreeLines(l);
            Visionary.CaptureException(new InvalidOperationException("handled"));
        });
        Assert.Equal("debug line,info line,warn line", Messages(Queued(agent)));
    }

    [Fact]
    public async Task KeepDebugForSlowRequests()
    {
        var (agent, loggers) = Setup(new[] { "logs.slow_ms", "20" });
        await Serve(agent, loggers, "/slow", 200, 40, null, ThreeLines);
        Assert.Equal("debug line,info line,warn line", Messages(Queued(agent)));

        agent.Logs!.Settings = LogSettings.Parse(new[] { new[] { "logs.slow_ms", "20" }, new[] { "logs.buffer", "false" } }, Severity.Info);
        await Serve(agent, loggers, "/slow", 500, 40, null, ThreeLines);
        Assert.Equal("info line,warn line", Messages(Queued(agent)));
    }

    [Fact]
    public async Task Overrides()
    {
        var until = RowBuilder.Timestamp(DateTime.UtcNow.AddHours(1));
        var past = RowBuilder.Timestamp(DateTime.UtcNow.AddHours(-1));
        var ov = JsonSerializer.Serialize(new[]
        {
            new Dictionary<string, string> { ["route"] = "/checkout", ["level"] = "debug", ["until"] = until },
            new Dictionary<string, string> { ["user"] = "42", ["level"] = "trace" },
            new Dictionary<string, string> { ["route"] = "/health", ["level"] = "warn" },
            new Dictionary<string, string> { ["route"] = "/old", ["level"] = "debug", ["until"] = past },
            new Dictionary<string, string> { ["service"] = "other", ["level"] = "debug" },
        });
        var (agent, loggers) = Setup(new[] { "logs.overrides", ov });

        await Serve(agent, loggers, "/checkout", 200, 0, null, ThreeLines);
        Assert.Equal("debug line,info line,warn line", Messages(Queued(agent)));
        await Serve(agent, loggers, "/health", 200, 0, null, ThreeLines);
        Assert.Equal("warn line", Messages(Queued(agent)));
        await Serve(agent, loggers, "/old", 200, 0, null, ThreeLines);
        Assert.Equal("info line,warn line", Messages(Queued(agent)));

        await Serve(agent, loggers, "/any", 200, 0, "42", l =>
        {
            l.LogTrace("trace line");
            Thread.Sleep(2);
            ThreeLines(l);
        });
        var got = Queued(agent).OrderBy(r => r.At).ToList();
        Assert.Equal("trace line,debug line,info line,warn line", Messages(got));
        Assert.Equal("42", got[0].UserId);
    }

    [Fact]
    public async Task LinesAfterTheRequestEndedAreDecidedAtOnce()
    {
        var ov = JsonSerializer.Serialize(new[] { new Dictionary<string, string> { ["route"] = "/jobs", ["level"] = "debug" } });
        var (agent, loggers) = Setup(new[] { "logs.overrides", ov });
        Task? late = null;
        await Serve(agent, loggers, "/jobs", 200, 0, "7", l =>
        {
            late = Task.Run(async () =>
            {
                await Task.Delay(50);
                l.LogDebug("late debug");
                l.LogTrace("late trace");
            });
        });
        await late!;
        var got = Assert.Single(Queued(agent));
        Assert.Equal("late debug", got.Message);
        Assert.Equal("7", got.UserId);
        Assert.False(string.IsNullOrEmpty(got.RequestId));
    }

    [Fact]
    public async Task HeldLinesAreCapped()
    {
        var (agent, loggers) = Setup();
        await Serve(agent, loggers, "/many", 500, 0, null, l =>
        {
            for (var i = 0; i < 600; i++)
            {
                l.LogDebug("line {N}", i);
            }
        });
        var got = Queued(agent);
        Assert.Equal(AppLogs.MaxHeld, got.Count);
        Assert.Equal("line 100", got[0].Message);
    }

    [Fact]
    public void OutsideRequestsWithStateScopesAndExceptions()
    {
        var (agent, loggers) = Setup(new[] { "logs.level", "warn" });
        var l = loggers.CreateLogger("Shop.Orders");
        l.LogInformation("startup");
        using (l.BeginScope(new Dictionary<string, object?> { ["tenant"] = "acme", ["session_id"] = "s-1" }))
        using (l.BeginScope("Processing order {OrderId}", 17))
        {
            l.LogError(new InvalidOperationException("disk full"), "Order {OrderId} failed for {request_id} after {Ms} ms ok={Ok}", 17, "abc", 12.5, true);
        }
        var got = Assert.Single(Queued(agent));
        Assert.StartsWith("Order 17 failed for abc after 12.5 ms ok=True\nSystem.InvalidOperationException: disk full", got.Message);
        Assert.Equal("Shop.Orders", got.Logger);
        Assert.Equal(Severity.Error, got.Sev);
        Assert.Equal("abc", got.RequestId);
        Assert.Equal("s-1", got.SessionId);
        var attrs = got.Attrs!;
        Assert.Equal(17L, attrs["OrderId"]);
        Assert.Equal(12.5, attrs["Ms"]);
        Assert.Equal(true, attrs["Ok"]);
        Assert.Equal("acme", attrs["tenant"]);
        Assert.False(attrs.ContainsKey("{OriginalFormat}"));
        Assert.False(attrs.ContainsKey("request_id"));
        Assert.False(attrs.ContainsKey("session_id"));
    }

    [Fact]
    public void TraceFromTheCurrentActivity()
    {
        var (agent, loggers) = Setup();
        using var activity = new Activity("work").SetIdFormat(ActivityIdFormat.W3C).Start();
        loggers.CreateLogger("App").LogWarning("traced");
        var got = Assert.Single(Queued(agent));
        Assert.Equal(activity.TraceId.ToHexString(), got.TraceId);
        Assert.Equal(activity.SpanId.ToHexString(), got.SpanId);
        Assert.False(got.Attrs?.ContainsKey("TraceId") ?? false);
    }

    [Fact]
    public async Task TraceFromTheTraceparentHeader()
    {
        var (agent, _) = Setup();
        var previous = Activity.Current;
        Activity.Current = null;
        try
        {
            var logs = new RequestLogs(new DefaultHttpContext(), LogText.TraceParentId("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"));
            await Task.Run(() =>
            {
                logs.Enter();
                agent.Logs!.Capture(new LogRecord { At = DateTime.UtcNow, Sev = Severity.Warn, Message = "in request" });
            });
        }
        finally
        {
            Activity.Current = previous;
        }
        Assert.Equal("4bf92f3577b34da6a3ce929d0e0e4736", Assert.Single(Queued(agent)).TraceId);
    }

    [Fact]
    public void OtherProvidersKeepTheirLevels()
    {
        var o = new VisionaryOptions { DatabaseUrl = "sqlite:///" + _db.Replace('\\', '/'), LogsAdded = true };
        var agent = new VisionaryAgent(Options.Create(o), NullLoggerFactory.Instance);
        _owned.Add(agent);
        var other = new ListProvider();
        var services = new ServiceCollection();
        services.AddLogging(b => b.SetMinimumLevel(LogLevel.Information).AddProvider(other).AddVisionary(v => v.DatabaseUrl = o.DatabaseUrl));
        var sp = services.BuildServiceProvider();
        _owned.Add(sp);
        var l = sp.GetRequiredService<ILoggerFactory>().CreateLogger("App");
        l.LogDebug("quiet");
        l.LogWarning("loud");
        Assert.Equal(new[] { "loud" }, other.Lines);
        // Visionary still sees debug lines, so a failed request can keep them.
        Assert.True(l.IsEnabled(LogLevel.Debug));
        Assert.False(l.IsEnabled(LogLevel.Trace));
        Assert.Equal("loud", Messages(Queued(agent)));
    }

    [Fact]
    public void AddVisionaryAloneCapturesNoLogs()
    {
        var services = new ServiceCollection();
        services.AddLogging();
        services.AddVisionary(v => v.DatabaseUrl = "sqlite:///" + _db.Replace('\\', '/'));
        Assert.DoesNotContain(services, d => d.ServiceType == typeof(ILoggerProvider) && d.ImplementationType == typeof(VisionaryLoggerProvider));
        Assert.DoesNotContain(services, d => d.ImplementationType == typeof(AppLogWriter));
        var sp = services.BuildServiceProvider();
        _owned.Add(sp);
        Assert.DoesNotContain(sp.GetServices<ILoggerProvider>(), p => p is VisionaryLoggerProvider);
        Assert.DoesNotContain(sp.GetRequiredService<IOptions<LoggerFilterOptions>>().Value.Rules,
            r => r.ProviderName == typeof(VisionaryLoggerProvider).FullName);
        var agent = sp.GetRequiredService<VisionaryAgent>();
        Assert.True(agent.Enabled);
        Assert.Null(agent.Logs);

        // Routing the logs here turns it on; a second call adds nothing more.
        services = new ServiceCollection();
        services.AddLogging(b => b.AddVisionary().AddVisionary());
        Assert.Single(services, d => d.ServiceType == typeof(ILoggerProvider) && d.ImplementationType == typeof(VisionaryLoggerProvider));
        Assert.Single(services, d => d.ImplementationType == typeof(AppLogWriter));
    }

    [Fact]
    public void SkipTheSDKsOwnLines()
    {
        var (agent, loggers) = Setup();
        loggers.CreateLogger("App").LogError("visionary: write failed");
        loggers.CreateLogger("Visionary").LogError("anything");
        loggers.CreateLogger("Styropyr0.Visionary.AppLogWriter").LogError("anything");
        Task.Run(() =>
        {
            Suppress.Enter();
            loggers.CreateLogger("Npgsql.Command").LogError("inside the writer");
        }).Wait();
        Assert.Empty(Queued(agent));
    }

    [Fact]
    public void DropsAndRate()
    {
        var drops = JsonSerializer.Serialize(new[]
        {
            new Dictionary<string, string> { ["contains"] = "healthcheck" },
            new Dictionary<string, string> { ["logger"] = "noisy", ["level"] = "info" },
        });
        var (agent, loggers) = Setup(new[] { "logs.drop", drops }, new[] { "logs.rate", "3" });
        var now = new DateTime(2026, 10, 28, 9, 30, 0, DateTimeKind.Utc);
        agent.Logs!.Clock = () => now;
        var l = loggers.CreateLogger("App");
        l.LogInformation("healthcheck ok");
        l.LogInformation("chatty {logger}", "noisy");
        l.LogWarning("kept {logger}", "noisy");
        for (var i = 0; i < 5; i++)
        {
            l.LogInformation("flood");
        }
        l.LogError("still kept");
        var got = Queued(agent);
        Assert.Equal("kept noisy,flood,flood,still kept", string.Join(",", got.Select(r => r.Message)));
        Assert.Equal("noisy", got[0].Logger);

        now = now.AddMinutes(1);
        l.LogWarning("next minute");
        got = Queued(agent);
        Assert.Equal(2, got.Count);
        Assert.Equal("visionary: 3 log lines dropped over the cap of 3 a minute (Settings → Logs)", got[0].Message);
        Assert.Equal("visionary", got[0].Logger);
        Assert.Equal(Severity.Warn, got[0].Sev);
    }

    [Fact]
    public void ParseAndFloors()
    {
        var until = RowBuilder.Timestamp(DateTime.UtcNow.AddHours(1));
        var past = RowBuilder.Timestamp(DateTime.UtcNow.AddHours(-1));
        var ov = JsonSerializer.Serialize(new[]
        {
            new Dictionary<string, string> { ["route"] = "/checkout", ["level"] = "debug", ["until"] = until },
            new Dictionary<string, string> { ["user"] = "42", ["level"] = "trace" },
            new Dictionary<string, string> { ["route"] = "/health", ["level"] = "warn" },
            new Dictionary<string, string> { ["route"] = "/admin/*", ["level"] = "debug" },
            new Dictionary<string, string> { ["route"] = "/old", ["level"] = "debug", ["until"] = past },
            new Dictionary<string, string> { ["service"] = "other", ["level"] = "debug" },
            new Dictionary<string, string> { ["level"] = "nonsense" },
            new Dictionary<string, string> { ["route"] = "/bad", ["level"] = "debug", ["until"] = "tomorrow" },
        });
        var s = LogSettings.Parse(new[]
        {
            new[] { "logs.level", "warning" }, new[] { "logs.overrides", ov }, new[] { "logs.rate", "-1" },
            new[] { "logs.slow_ms", "250" }, new[] { "logs.buffer", "nope" }, new[] { "logs.enabled", "false" },
        }, 0);
        Assert.Equal(Severity.Warn, s.Level);
        Assert.Equal(6000, s.Rate);
        Assert.Equal(250.0, s.SlowMs);
        Assert.True(s.Buffer);
        Assert.False(s.Enabled);
        Assert.Equal(6, s.Overrides.Count);
        Assert.Equal(Severity.Debug, s.BufferLevel);

        var now = DateTime.UtcNow;
        foreach (var (route, user, want) in new[]
        {
            ("/checkout", "", Severity.Debug),
            ("/health", "", Severity.Warn),
            ("/admin/users", "", Severity.Debug),
            ("/old", "", Severity.Warn),
            ("/any", "42", Severity.Trace),
            ("/health", "42", Severity.Trace),
            ("/any", "", Severity.Warn),
        })
        {
            Assert.True(want == s.Floor("api", route, user, now), $"floor {route} {user}");
        }
        Assert.Equal(Severity.Debug, s.Floor("other", null, null, now));
        Assert.Equal(Severity.Warn, s.HoldBelow("api", now));

        var defaults = LogSettings.Parse(Array.Empty<string?[]>(), Severity.Debug);
        Assert.Equal(Severity.Debug, defaults.Level);
        Assert.True(defaults.Enabled);
        Assert.Equal(1000.0, defaults.SlowMs);
    }

    [Fact]
    public void Dropped()
    {
        var drops = JsonSerializer.Serialize(new[]
        {
            new Dictionary<string, string> { ["contains"] = "healthcheck" },
            new Dictionary<string, string> { ["logger"] = "noisy", ["level"] = "info" },
            new Dictionary<string, string> { ["service"] = "web", ["level"] = "debug" },
            new Dictionary<string, string>(),
        });
        var s = LogSettings.Parse(new[] { new[] { "logs.drop", drops } }, Severity.Info);
        Assert.Equal(3, s.Drops.Count);
        foreach (var (service, logger, msg, sev, want) in new[]
        {
            ("api", "", "healthcheck ok", Severity.Error, true),
            ("api", "noisy", "x", Severity.Info, true),
            ("api", "noisy", "x", Severity.Warn, false),
            ("web", "", "x", Severity.Debug, true),
            ("web", "", "x", Severity.Info, false),
            ("api", "", "x", Severity.Trace, false),
        })
        {
            Assert.True(want == s.Dropped(service, logger, msg, sev), $"{service} {logger} {msg} {sev}");
        }
    }

    [Fact]
    public void Levels()
    {
        foreach (var (name, want) in new[] { ("WARNING", Severity.Warn), ("crit", Severity.Fatal), ("notice", Severity.Info), ("Severe", Severity.Error), ("", 0), ("loud", 0) })
        {
            Assert.Equal(want, Severity.Parse(name));
        }
        Assert.Equal("info", Severity.Name(0));
        Assert.Equal("fatal", Severity.Name(Severity.Fatal));
        Assert.Equal(Severity.Trace, Severity.From(LogLevel.Trace));
        Assert.Equal(Severity.Info, Severity.From(LogLevel.Information));
        Assert.Equal(Severity.Fatal, Severity.From(LogLevel.Critical));
        Assert.Equal(0, Severity.From(LogLevel.None));
    }

    [Fact]
    public void TextLevel()
    {
        foreach (var (line, want) in new[]
        {
            ("ERROR something", Severity.Error),
            ("2026-10-28 WARN disk", Severity.Warn),
            ("level=debug msg=x", Severity.Debug),
            ("[info] started", Severity.Info),
            ("time=x level=WARN msg=y", Severity.Warn),
            ("INFO handled", Severity.Info),
            ("payment failed", 0),
            ("user error rate is low but ok", 0),
            ("error: connection refused", Severity.Error),
            ("Error handling request", 0),
            ("10:03:44 [ERROR] x", Severity.Error),
            ("2026/10/28 10:03:44 FATAL x", Severity.Fatal),
            ("<warning> x", Severity.Warn),
        })
        {
            Assert.True(want == LogText.TextLevel(line), $"{line}: {LogText.TextLevel(line)}, want {want}");
        }
    }

    [Fact]
    public void StripLevelWord()
    {
        foreach (var (line, want) in new[]
        {
            ("INFO started", "started"),
            ("WARNING: disk", "disk"),
            ("ERROR", "ERROR"),
            ("Error handling", "Error handling"),
            ("[warn] slow", "[warn] slow"),
            ("WARN", "WARN"),
            ("DEBUG   spaced x", "  spaced x"),
        })
        {
            var sev = LogText.TextLevel(line);
            Assert.Equal(want, LogText.StripLevelWord(line, sev == 0 ? Severity.Info : sev));
        }
    }

    [Fact]
    public void Scrub()
    {
        var input = "login password=hunter2 token: \"abc def\" Authorization: Bearer abcdefghijkl key AKIAABCDEFGHIJKLMNOP " +
            "jwt eyJhbGciOiJ.eyJzdWIiOiIx.c2lnbmF0dXJl -----BEGIN RSA PRIVATE KEY-----\nMIIE\n-----END RSA PRIVATE KEY----- done";
        var got = LogText.Scrub(input);
        foreach (var leak in new[] { "hunter2", "abc def", "abcdefghijkl", "AKIAABCDEFGHIJKLMNOP", "eyJhbGciOiJ", "MIIE" })
        {
            Assert.DoesNotContain(leak, got);
        }
        Assert.Contains("password=[REDACTED]", got);
        Assert.Contains("Bearer [REDACTED]", got);
        Assert.EndsWith(" done", got);
        Assert.Equal("plain text", LogText.Scrub("plain text"));
    }

    [Fact]
    public void EncodeAttrs()
    {
        var s = LogText.EncodeAttrs(new Dictionary<string, object> { ["user.password"] = "x", ["n"] = 1L, ["note"] = "token=abc", ["ssn"] = "1" },
            LogText.SecretKeys(new[] { "SSN" }));
        Assert.Equal("{\"n\":1,\"note\":\"token=[REDACTED]\",\"ssn\":\"[REDACTED]\",\"user.password\":\"[REDACTED]\"}", s);

        var big = new Dictionary<string, object>();
        for (var i = 0; i < 40; i++)
        {
            big[new string('k', i + 1)] = new string('v', 2000);
        }
        s = LogText.EncodeAttrs(big, new HashSet<string>());
        Assert.True(Encoding.UTF8.GetByteCount(s) <= LogText.MaxAttrs);
        using (var doc = JsonDocument.Parse(s))
        {
            Assert.Equal(new string('v', 1024) + "…", doc.RootElement.GetProperty("k").GetString());
        }

        var many = new Dictionary<string, object>();
        for (var i = 0; i < 2000; i++)
        {
            many[$"key-{i:D5}-{new string('x', 10)}"] = "value";
        }
        s = LogText.EncodeAttrs(many, new HashSet<string>());
        Assert.True(Encoding.UTF8.GetByteCount(s) <= LogText.MaxAttrs);
        using (var doc = JsonDocument.Parse(s))
        {
            Assert.True(doc.RootElement.GetProperty("_truncated").GetBoolean());
            Assert.True(doc.RootElement.TryGetProperty("key-00000-xxxxxxxxxx", out _));
        }
    }

    [Fact]
    public void Truncate()
    {
        Assert.Equal("h", LogText.Truncate("héllo", 2));
        Assert.Equal("abc", LogText.Truncate("abc", 5));
        Assert.Equal("a", LogText.Truncate("a😀", 4));
    }

    [Fact]
    public void TraceParentId()
    {
        Assert.Equal("4bf92f3577b34da6a3ce929d0e0e4736", LogText.TraceParentId("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"));
        foreach (var bad in new[] { "", "00-xyz-1-1", "00-00000000000000000000000000000000-00f067aa0ba902b7-01" })
        {
            Assert.Null(LogText.TraceParentId(bad));
        }
    }

    [Fact]
    public void LogRowMatchesTheColumns()
    {
        var (agent, _) = Setup();
        var rows = new RowBuilder(agent);
        var r = new LogRecord
        {
            At = new DateTime(2026, 10, 28, 9, 30, 12, 345, DateTimeKind.Utc),
            Sev = Severity.Warn,
            Message = "hi password=x",
            Logger = "App",
            Attrs = new Dictionary<string, object> { ["a"] = 1L },
            RequestId = "req-1",
        };
        var p = rows.LogRow(r, agent.Logs!.Secrets);
        Assert.Equal(Schema.LogColumns.Length, p.Length);
        var stmt = rows.Log(r, agent.Logs.Secrets);
        Assert.Equal(Schema.LogColumns.Length, stmt.Sql.Count(c => c == '?'));
        object?[] row = p;
        Assert.Equal("2026-10-28T09:30:12.345Z", row[0]);
        Assert.Equal("api", row[1]);
        Assert.Equal("dotnet", row[4]);
        Assert.Null(row[6]);
        Assert.Equal("warn", row[7]);
        Assert.Equal((long)Severity.Warn, row[8]);
        Assert.Equal("App", row[9]);
        Assert.Equal("hi password=[REDACTED]", row[10]);
        Assert.Equal("{\"a\":1}", row[11]);
        Assert.Equal("req-1", row[12]);
        Assert.Null(row[17]);
        Assert.Null(row[18]);
        Assert.Equal((long)("hi password=[REDACTED]".Length + "{\"a\":1}".Length), row[20]);
    }

    [Fact]
    public async Task WritesToTheDatabase()
    {
        var (agent, _) = Setup();
        await agent.Store!.CreateAsync("logs", null, null, CancellationToken.None);
        var rows = new RowBuilder(agent);
        await agent.Store.BatchAsync(new[]
        {
            rows.Log(new LogRecord { At = DateTime.UtcNow, Sev = Severity.Error, Message = "stored", Attrs = new() { ["k"] = "v" } }, agent.Logs!.Secrets),
        }, CancellationToken.None);
        var got = await agent.Store.RowsAsync("SELECT level, severity, message, attrs, source FROM app_logs", CancellationToken.None);
        var row = Assert.Single(got);
        Assert.Equal(new string?[] { "error", "5", "stored", "{\"k\":\"v\"}", "dotnet" }, row);
    }

    [Fact]
    public void OptionsFromEnvironment()
    {
        var o = new VisionaryOptions();
        Assert.True(o.Logs);
        Assert.Equal("info", o.LogLevel);
        o.Load(new ConfigurationBuilder().AddInMemoryCollection(new Dictionary<string, string?>
        {
            ["VISIONARY_LOGS"] = "false",
            ["VISIONARY_LOG_LEVEL"] = "debug",
        }).Build());
        Assert.False(o.Logs);
        Assert.Equal("debug", o.LogLevel);
        var agent = new VisionaryAgent(Options.Create(new VisionaryOptions { DatabaseUrl = "sqlite:///" + _db.Replace('\\', '/'), Logs = false, LogsAdded = true }),
            NullLoggerFactory.Instance);
        _owned.Add(agent);
        Assert.Null(agent.Logs);
    }

    private sealed class ListProvider : ILoggerProvider
    {
        public List<string> Lines { get; } = new();

        public ILogger CreateLogger(string categoryName) => new ListLogger(Lines);

        public void Dispose()
        {
        }

        private sealed class ListLogger(List<string> lines) : ILogger
        {
            public IDisposable? BeginScope<TState>(TState state) where TState : notnull => null;

            public bool IsEnabled(LogLevel logLevel) => true;

            public void Log<TState>(LogLevel logLevel, EventId eventId, TState state, Exception? exception, Func<TState, Exception?, string> formatter) =>
                lines.Add(formatter(state, exception));
        }
    }
}
