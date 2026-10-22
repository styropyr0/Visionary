using System.Collections.Generic;
using System.Threading;
using System.Threading.Tasks;
using Microsoft.Extensions.Configuration;
using Xunit;

namespace Styropyr0.Visionary.Tests;

/// <summary>Retention is the Visionary server's job: the SDK purges only when asked.</summary>
public class RetentionTests
{
    [Fact]
    public void DefaultsAreOff()
    {
        var o = new VisionaryOptions();
        Assert.Equal(0, o.LogRetentionDays);
        Assert.Equal(0, o.SystemRetentionDays);

        var loaded = new VisionaryOptions();
        loaded.Load(new ConfigurationBuilder().AddInMemoryCollection(new Dictionary<string, string?>
        {
            ["VISIONARY_LOG_RETENTION_DAYS"] = "",
            ["VISIONARY_SYSTEM_RETENTION_DAYS"] = "",
        }).Build());
        Assert.Equal(0, loaded.LogRetentionDays);
        Assert.Equal(0, loaded.SystemRetentionDays);

        var set = new VisionaryOptions();
        set.Load(new ConfigurationBuilder().AddInMemoryCollection(new Dictionary<string, string?>
        {
            ["VISIONARY_LOG_RETENTION_DAYS"] = "90",
            ["VISIONARY_SYSTEM_RETENTION_DAYS"] = "7",
        }).Build());
        Assert.Equal(90, set.LogRetentionDays);
        Assert.Equal(7, set.SystemRetentionDays);
    }

    [Fact]
    public async Task NothingIsPurgedByDefault()
    {
        var o = new VisionaryOptions();
        Assert.Empty(Retention.LogTables(o));

        // With no tables the loop returns at once instead of waiting a minute to purge.
        var run = Retention.RunAsync(null!, Retention.LogTables(o), CancellationToken.None);
        Assert.True(run.IsCompleted);
        await run;

        o.LogRetentionDays = 30;
        Assert.Equal(3, Retention.LogTables(o).Count);
    }
}
