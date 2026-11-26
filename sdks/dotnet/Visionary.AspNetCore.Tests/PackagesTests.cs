using System;
using System.Collections.Generic;
using System.IO;
using System.Threading;
using System.Threading.Tasks;
using Microsoft.Data.Sqlite;
using Microsoft.Extensions.Configuration;
using Microsoft.Extensions.Logging.Abstractions;
using Microsoft.Extensions.Options;
using Xunit;

namespace Styropyr0.Visionary.Tests;

public sealed class PackagesTests : IDisposable
{
    private const string Deps = """
        {
          "runtimeTarget": { "name": ".NETCoreApp,Version=v8.0" },
          "libraries": {
            "MyApp/1.0.0": { "type": "project", "serviceable": false, "sha512": "" },
            "Npgsql/8.0.5": { "type": "package", "serviceable": true, "sha512": "sha512-x", "path": "npgsql/8.0.5" },
            "Serilog/4.1.0": { "type": "package", "serviceable": true, "sha512": "sha512-y", "path": "serilog/4.1.0" },
            "System.Runtime.Reference/8.0.0": { "type": "reference" },
            "Broken": { "type": "package" }
          }
        }
        """;

    private readonly string _dir = Path.Combine(Path.GetTempPath(), $"visionary-packages-{Guid.NewGuid():N}");

    public void Dispose()
    {
        SqliteConnection.ClearAllPools();
        try
        {
            Directory.Delete(_dir, true);
        }
        catch (IOException)
        {
        }
    }

    [Fact]
    public void ReadsPackageLibrariesOnly()
    {
        Assert.Equal(new[] { ("Npgsql", "8.0.5"), ("Serilog", "4.1.0") }, PackageReporter.Read(Deps));
        Assert.Empty(PackageReporter.Read("{}"));
    }

    [Fact]
    public async Task WritesOneReport()
    {
        Directory.CreateDirectory(_dir);
        var deps = Path.Combine(_dir, "MyApp.deps.json");
        await File.WriteAllTextAsync(deps, Deps);
        var db = Path.Combine(_dir, "v.db");
        var o = new VisionaryOptions { DatabaseUrl = "sqlite:///" + db.Replace('\\', '/'), Service = "api", SystemMetrics = false };
        using var agent = new VisionaryAgent(Options.Create(o), NullLoggerFactory.Instance);

        await new PackageReporter(agent).ReportAsync(deps, CancellationToken.None);

        var rows = await agent.Store!.RowsAsync(
            "SELECT service, ecosystem, name, pkg_version, version FROM app_packages ORDER BY name", CancellationToken.None);
        Assert.Equal(2, rows.Count);
        Assert.Equal(new[] { "api", "NuGet", "Npgsql", "8.0.5", null }, rows[0]);
        var times = await agent.Store.RowsAsync("SELECT COUNT(DISTINCT created_at) FROM app_packages", CancellationToken.None);
        Assert.Equal("1", times[0][0]);
    }

    [Fact]
    public void EnvTurnsItOff()
    {
        Assert.True(new VisionaryOptions().Packages);
        var off = new VisionaryOptions();
        off.Load(new ConfigurationBuilder().AddInMemoryCollection(new Dictionary<string, string?>
        {
            ["VISIONARY_PACKAGES"] = "false",
        }).Build());
        Assert.False(off.Packages);
    }
}
