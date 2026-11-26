using System;
using System.Collections.Generic;
using System.Threading;
using System.Threading.Tasks;
using Microsoft.Extensions.Logging.Abstractions;
using Microsoft.Extensions.Options;
using Xunit;

namespace Styropyr0.Visionary.Tests;

[Collection(BackgroundTests.AgentCollection)]
public sealed class SystemSamplerTests
{
    [Theory]
    [InlineData("web-1", "api", "web-1")]
    [InlineData("a1b2c3d4e5f6", "api", "api")]
    [InlineData("a1b2c3d4e5f6", null, "dotnet-app")]
    [InlineData("ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12", "", "dotnet-app")]
    [InlineData("", "api", "api")]
    [InlineData("A1B2C3D4E5F6", null, "A1B2C3D4E5F6")]
    [InlineData("a1b2c3d4e5f", null, "a1b2c3d4e5f")]
    [InlineData("deadbeefcafe-2", "api", "deadbeefcafe-2")]
    public void DefaultInstance(string host, string? service, string want)
    {
        Assert.Equal(want, VisionaryAgent.DefaultInstance(host, service));
    }

    [Fact]
    public void InstanceOptionWins()
    {
        using var agent = new VisionaryAgent(
            Options.Create(new VisionaryOptions { Service = "api", Instance = " i-1 " }),
            NullLoggerFactory.Instance, null, new SwitchStore());
        Assert.Equal("i-1", agent.Instance);
    }

    [Fact]
    public async Task SettingsSwitch()
    {
        var store = new SwitchStore { Fail = true };
        using var agent = new VisionaryAgent(
            Options.Create(new VisionaryOptions { Service = "api", Instance = "i" }),
            NullLoggerFactory.Instance, null, store);
        var sampler = new SystemSampler(agent);
        sampler.Prime();
        var ct = CancellationToken.None;

        await sampler.ReadSwitchAsync(ct);
        Assert.False(sampler.Off);
        await sampler.TickAsync();
        Assert.Equal(1, sampler.Buffered);

        store.Fail = false;
        store.Value = " FALSE ";
        await sampler.ReadSwitchAsync(ct);
        Assert.True(sampler.Off);
        Assert.Equal(0, sampler.Buffered);
        await sampler.TickAsync();
        Assert.Equal(0, sampler.Buffered);

        store.Fail = true;
        await sampler.ReadSwitchAsync(ct);
        Assert.True(sampler.Off);

        store.Fail = false;
        store.Value = null;
        await sampler.ReadSwitchAsync(ct);
        Assert.False(sampler.Off);
        Assert.Equal(0, sampler.Buffered);
        await sampler.TickAsync();
        Assert.Equal(1, sampler.Buffered);

        store.Value = "true";
        await sampler.ReadSwitchAsync(ct);
        Assert.False(sampler.Off);
        Assert.Equal(1, sampler.Buffered);
        Assert.All(store.Queries, q => Assert.Contains("'" + SystemSampler.Setting + "'", q));
    }

    private sealed class SwitchStore : IStore
    {
        public string? Value { get; set; }
        public bool Fail { get; set; }
        public readonly List<string> Queries = new();

        public VisionaryDialect Dialect => VisionaryDialect.Postgres;
        public bool StringParams => false;
        public string Describe => "fake";
        public Task ExecScriptAsync(string script, CancellationToken ct) => Task.CompletedTask;
        public Task BatchAsync(IReadOnlyList<Stmt> stmts, CancellationToken ct) => Task.CompletedTask;
        public Task<long> ExecChangesAsync(string sql, object?[] args, CancellationToken ct) => Task.FromResult(0L);
        public Task<List<string>> ColumnsAsync(string query, CancellationToken ct) => Task.FromResult(new List<string>());

        public Task<List<string?[]>> RowsAsync(string query, CancellationToken ct)
        {
            Queries.Add(query);
            if (Fail)
            {
                throw new InvalidOperationException("no such table: visionary_settings");
            }
            var rows = new List<string?[]>();
            if (Value is not null)
            {
                rows.Add(new string?[] { Value });
            }
            return Task.FromResult(rows);
        }
    }
}
