using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Threading;
using System.Threading.Tasks;
using Microsoft.Data.Sqlite;
using Microsoft.Extensions.Configuration;
using Microsoft.Extensions.Logging.Abstractions;
using Microsoft.Extensions.Options;
using Styropyr0.Visionary.Profiling;
using Xunit;

namespace Styropyr0.Visionary.Tests;

[Collection(BackgroundTests.AgentCollection)]
public sealed class ProfilerServiceTests : IDisposable
{
    private readonly string _db = Path.Combine(Path.GetTempPath(), $"visionary-test-{Guid.NewGuid():N}.db");
    private DateTime _now = new(2026, 10, 22, 12, 0, 0, DateTimeKind.Utc);
    private long _samples = 30;
    private bool _finishRequest = true;

    public void Dispose()
    {
        SqliteConnection.ClearAllPools();
        try
        {
            File.Delete(_db);
        }
        catch (IOException)
        {
        }
    }

    private void Settings(Dictionary<string, string> values, bool createTable = true)
    {
        using var conn = new SqliteConnection($"Data Source={_db}");
        conn.Open();
        using var cmd = conn.CreateCommand();
        cmd.CommandText = "CREATE TABLE IF NOT EXISTS visionary_settings (name TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TEXT NOT NULL); DELETE FROM visionary_settings;";
        cmd.ExecuteNonQuery();
        foreach (var (k, v) in values)
        {
            using var ins = conn.CreateCommand();
            ins.CommandText = "INSERT INTO visionary_settings (name, value, updated_at) VALUES ($k, $v, '2026-10-22T12:00:00.000Z')";
            ins.Parameters.AddWithValue("$k", k);
            ins.Parameters.AddWithValue("$v", v);
            ins.ExecuteNonQuery();
        }
    }

    private (VisionaryAgent Agent, ProfilerService Service) Make(string? service = "api", bool profile = true)
    {
        var o = new VisionaryOptions { DatabaseUrl = "sqlite:///" + _db.Replace('\\', '/'), Service = service, Profile = profile, ProfilingAdded = true };
        var agent = new VisionaryAgent(Options.Create(o), NullLoggerFactory.Instance);
        Assert.True(agent.Enabled);
        var svc = new ProfilerService(agent, () => _now,
            (d, ct) =>
            {
                if (_finishRequest)
                {
                    agent.Tally.Finish("GET", "/work/{n}", "/work/7", 4.4);
                    agent.Tally.Finish("GET", "/work/{n}", "/work/8", 0.2);
                    agent.Tally.Finish("POST", null, "/raw", 0.1);
                }
                return Task.FromResult(new Recording("unused", _now, d.TotalMilliseconds));
            },
            _ => new FoldedProfile("App.Main;App.Burn 30\n", _samples));
        return (agent, svc);
    }

    private static List<ProfileRecord> Written(VisionaryAgent agent)
    {
        var list = new List<ProfileRecord>();
        while (agent.Queue.Reader.TryRead(out var item))
        {
            if (item is ProfileRecord p)
            {
                list.Add(p);
            }
        }
        return list;
    }

    private async Task Advance(ProfilerService svc, double seconds)
    {
        _now = _now.AddSeconds(seconds);
        await svc.TickAsync(CancellationToken.None);
    }

    [Fact]
    public async Task OffWhileEnabledMissingOrFalse()
    {
        Settings(new() { ["profile.interval_s"] = "10" });
        var (agent, svc) = Make();
        agent.Tally.Start();
        await svc.TickAsync(CancellationToken.None);
        Assert.False(svc.On);
        Assert.Equal(0, svc.Recorded);

        Settings(new() { ["profile.enabled"] = "false" });
        agent.Tally.Start();
        await Advance(svc, 30);
        Assert.Equal(0, svc.Recorded);
    }

    [Fact]
    public async Task ProfilesOnScheduleWhileServing()
    {
        Settings(new() { ["profile.enabled"] = "true", ["profile.interval_s"] = "60", ["profile.duration_ms"] = "2500" });
        var (agent, svc) = Make();
        agent.Tally.Start();
        await svc.TickAsync(CancellationToken.None);
        Assert.True(svc.On);
        Assert.Equal(1, svc.Taken);
        var p = Assert.Single(Written(agent));
        Assert.Equal(2500, p.DurationMs);
        Assert.Equal(30, p.Samples);
        Assert.Equal("{\"GET /work/{n}\":5,\"POST /raw\":1}", p.Routes);
        Assert.Equal("App.Main;App.Burn 30\n", p.Folded);

        // Not due yet.
        await Advance(svc, 30);
        Assert.Equal(1, svc.Recorded);
        // Due, but no request arrived since the last profile: skipped.
        await Advance(svc, 30);
        Assert.Equal(1, svc.Recorded);
        await Advance(svc, 60);
        Assert.Equal(1, svc.Recorded);
        // A request arrives: the next turn profiles.
        agent.Tally.Start();
        await Advance(svc, 60);
        Assert.Equal(2, svc.Recorded);

        // Turned off in the dashboard.
        Settings(new() { ["profile.enabled"] = "false" });
        agent.Tally.Start();
        await Advance(svc, 60);
        Assert.False(svc.On);
        Assert.Equal(2, svc.Recorded);
    }

    [Fact]
    public async Task DropsThinOrIdleProfiles()
    {
        Settings(new() { ["profile.enabled"] = "true", ["profile.interval_s"] = "10" });
        var (agent, svc) = Make();
        _samples = 19;
        agent.Tally.Start();
        await svc.TickAsync(CancellationToken.None);
        Assert.Equal(1, svc.Recorded);
        Assert.Equal(0, svc.Taken);

        _samples = 500;
        _finishRequest = false;
        agent.Tally.Start();
        await Advance(svc, 10);
        Assert.Equal(2, svc.Recorded);
        Assert.Equal(0, svc.Taken);
        Assert.Empty(Written(agent));
    }

    [Theory]
    [InlineData("api", "[\"api\",\"worker\"]", true)]
    [InlineData("api", "[\"worker\"]", false)]
    [InlineData("api", "[]", true)]
    [InlineData("api", null, true)]
    [InlineData("api", "not json", true)]
    [InlineData(null, "[\"api\"]", false)]
    [InlineData(null, "[]", true)]
    public async Task ServicesFilter(string? service, string? services, bool want)
    {
        var values = new Dictionary<string, string> { ["profile.enabled"] = "true" };
        if (services is not null)
        {
            values["profile.services"] = services;
        }
        Settings(values);
        var (agent, svc) = Make(service);
        agent.Tally.Start();
        await svc.TickAsync(CancellationToken.None);
        Assert.Equal(want, svc.On);
        Assert.Equal(want ? 1 : 0, svc.Recorded);
    }

    [Fact]
    public async Task ProfileNowTakesOneWhenFresh()
    {
        Settings(new());
        var (agent, svc) = Make();
        var started = _now;
        await svc.TickAsync(CancellationToken.None);
        Assert.Equal(0, svc.Recorded);

        // Older than this process: ignored.
        Settings(new() { ["profile.now"] = RowBuilder.Timestamp(started.AddMinutes(-1)) });
        await Advance(svc, 30);
        Assert.Equal(0, svc.Recorded);

        // Fresh: one profile though profiling is off, and no request is needed to start it.
        var stamp = RowBuilder.Timestamp(_now.AddSeconds(25));
        Settings(new() { ["profile.now"] = stamp });
        await Advance(svc, 30);
        Assert.False(svc.On);
        Assert.Equal(1, svc.Recorded);
        Assert.Single(Written(agent));

        // The same stamp again: nothing more.
        await Advance(svc, 30);
        Assert.Equal(1, svc.Recorded);

        // A new stamp, read more than five minutes after it was set: stale.
        Settings(new() { ["profile.now"] = RowBuilder.Timestamp(_now.AddSeconds(5)) });
        _now = _now.AddMinutes(6);
        await svc.TickAsync(CancellationToken.None);
        Assert.Equal(1, svc.Recorded);
    }

    [Fact]
    public async Task ProfileNowHonoursServices()
    {
        Settings(new() { ["profile.services"] = "[\"worker\"]", ["profile.now"] = RowBuilder.Timestamp(_now.AddSeconds(1)) });
        var (_, svc) = Make();
        _now = _now.AddSeconds(2);
        await svc.TickAsync(CancellationToken.None);
        Assert.Equal(0, svc.Recorded);
    }

    [Fact]
    public async Task MissingTableMeansOff()
    {
        var (agent, svc) = Make();
        agent.Tally.Start();
        await svc.TickAsync(CancellationToken.None);
        Assert.False(svc.On);
        Assert.Equal(0, svc.Recorded);
    }

    [Fact]
    public async Task VisionaryProfileFalseNeverProfiles()
    {
        var o = new VisionaryOptions();
        o.Load(new ConfigurationBuilder().AddInMemoryCollection(new Dictionary<string, string?> { ["VISIONARY_PROFILE"] = "false" }).Build());
        Assert.False(o.Profile);
        var d = new VisionaryOptions();
        d.Load(new ConfigurationBuilder().Build());
        Assert.True(d.Profile);

        Settings(new() { ["profile.enabled"] = "true", ["profile.now"] = RowBuilder.Timestamp(_now.AddSeconds(1)) });
        var (agent, svc) = Make(profile: false);
        agent.Tally.Start();
        _now = _now.AddSeconds(2);
        await svc.StartAsync(CancellationToken.None);
        await Task.Delay(300);
        await svc.StopAsync(CancellationToken.None);
        Assert.False(svc.On);
        Assert.Equal(0, svc.Recorded);
    }

    [Fact]
    public void ParsesSettings()
    {
        var s = ProfileSettings.Parse(new[]
        {
            new[] { "profile.enabled", "true" }, new[] { "profile.interval_s", "abc" },
            new[] { "profile.duration_ms", "-5" }, new string?[] { "profile.now", null },
        });
        Assert.True(s.Enabled);
        Assert.Equal(300, s.IntervalS);
        Assert.Equal(10_000, s.DurationMs);
        Assert.Equal("", s.Now);
        Assert.Empty(s.Services);

        var since = new DateTime(2026, 10, 22, 12, 0, 0, DateTimeKind.Utc);
        Assert.True(ProfileSettings.FreshNow("2026-10-22T12:01:00.000Z", since, since.AddMinutes(2)));
        Assert.True(ProfileSettings.FreshNow("2026-10-22 12:01:00", since, since.AddMinutes(2)));
        Assert.False(ProfileSettings.FreshNow("2026-10-22T11:59:00.000Z", since, since.AddMinutes(2)));
        Assert.False(ProfileSettings.FreshNow("2026-10-22T12:01:00.000Z", since, since.AddMinutes(6.5)));
        Assert.False(ProfileSettings.FreshNow("yesterday", since, since));
    }
}
