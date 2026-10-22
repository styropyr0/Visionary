using System;
using System.IO;
using System.Linq;
using System.Runtime.CompilerServices;
using System.Threading;
using System.Threading.Tasks;
using Styropyr0.Visionary.Profiling;
using Xunit;

namespace Styropyr0.Visionary.Tests;

/// <summary>Samples this test process for real through its own diagnostics port.</summary>
public class EventPipeTests
{
    private static volatile bool _stop;

    [MethodImpl(MethodImplOptions.NoInlining)]
    private static double Burn()
    {
        double s = 0;
        for (var i = 0; i < 200_000; i++)
        {
            s += Math.Sqrt(i) * Math.Sin(i);
        }
        return s;
    }

    [Fact]
    public async Task SamplesBusyThreadsAndSkipsIdleOnes()
    {
        _stop = false;
        var busy = new Thread(() =>
        {
            while (!_stop)
            {
                Burn();
            }
        }) { IsBackground = true };
        busy.Start();
        using var gate = new ManualResetEventSlim();
        var idle = Enumerable.Range(0, 4).Select(_ => new Thread(() => gate.Wait()) { IsBackground = true }).ToList();
        idle.ForEach(t => t.Start());
        try
        {
            var rec = await EventPipeSampler.RecordAsync(TimeSpan.FromSeconds(1.5), CancellationToken.None);
            Assert.True(File.Exists(rec.TracePath));
            Assert.InRange(rec.DurationMs, 1400, 5000);
            var namer = new FrameNamer(CodeOwners.ForCurrentApp(), Directory.GetCurrentDirectory());
            var profile = EventPipeSampler.Fold(rec, namer);
            Assert.False(File.Exists(rec.TracePath));

            var lines = profile.Folded.TrimEnd('\n').Split('\n');
            Assert.True(profile.Samples >= Folder.MinSamples, profile.Folded);
            var burn = lines.Where(l => l.Contains("EventPipeTests.Burn ", StringComparison.Ordinal)).ToList();
            Assert.NotEmpty(burn);
            // The busy thread dominates; the waiting ones are not there at all.
            var burnSamples = burn.Sum(l => long.Parse(l[(l.LastIndexOf(' ') + 1)..]));
            Assert.True(burnSamples * 2 > profile.Samples, profile.Folded);
            Assert.DoesNotContain(lines, l => l.Contains("ManualResetEventSlim.Wait", StringComparison.Ordinal));
            // The test assembly's PDB gives file and line, on the sampled frame only.
            Assert.Contains(burn, l => l[..l.LastIndexOf(' ')].Split(';')[^1].Contains("EventPipeTests.cs:", StringComparison.Ordinal));
            Assert.All(lines, l => Assert.All(l[..l.LastIndexOf(' ')].Split(';')[..^1],
                f => Assert.DoesNotMatch(@"\.cs:\d+$", f)));
            Assert.Contains(lines, l => l.Contains(" std:System.Private.CoreLib/", StringComparison.Ordinal));
        }
        finally
        {
            _stop = true;
            gate.Set();
        }
    }
}
