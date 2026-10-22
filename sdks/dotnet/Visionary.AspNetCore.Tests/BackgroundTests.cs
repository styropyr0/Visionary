using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Threading.Tasks;
using Microsoft.AspNetCore.Http;
using Microsoft.Data.Sqlite;
using Microsoft.Extensions.Logging.Abstractions;
using Microsoft.Extensions.Options;
using Xunit;

namespace Styropyr0.Visionary.Tests;

/// <summary>
/// Work a request starts and does not wait for (SPEC.md "Work a request starts in the
/// background"): late calls keep the request, stay out of its totals, and are stored up to the
/// cap whatever their duration.
/// </summary>
[Collection(AgentCollection)]
public sealed class BackgroundTests : IDisposable
{
    /// <summary>Tests that construct an agent share <see cref="VisionaryAgent.Current"/>, so they run one at a time.</summary>
    public const string AgentCollection = "VisionaryAgent";

    private readonly string _db = Path.Combine(Path.GetTempPath(), $"visionary-bg-{Guid.NewGuid():N}.db");
    private readonly List<VisionaryAgent> _agents = new();

    public void Dispose()
    {
        foreach (var a in _agents)
        {
            a.Dispose();
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

    private VisionaryAgent Agent(int spanMax = 100, double thresholdMs = 100)
    {
        var o = new VisionaryOptions
        {
            DatabaseUrl = "sqlite:///" + _db.Replace('\\', '/'),
            SpanMax = spanMax,
            SpanThresholdMs = thresholdMs,
        };
        var agent = new VisionaryAgent(Options.Create(o), NullLoggerFactory.Instance);
        Assert.True(agent.Enabled);
        _agents.Add(agent);
        return agent;
    }

    private static RequestState Request(VisionaryAgent agent, DateTime startedAt)
    {
        var state = new RequestState(new DefaultHttpContext(), "req-1", startedAt, agent.Options.SpanMax);
        state.Enter();
        return state;
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
    public void LateSpansKeepTheRequestAndItsOffset()
    {
        var agent = Agent();
        var start = DateTime.UtcNow.AddSeconds(-1);
        var state = Request(agent, start);
        state.Close();

        agent.RecordSpan("db", "SELECT users", "sqlite/app.db", "SELECT * FROM users", start.AddMilliseconds(1500), 3, null, 1);

        var span = Assert.Single(Queued(agent));
        Assert.Equal("req-1", span.RequestId);
        Assert.Equal(1500, span.OffsetMs);
        Assert.Equal(3, span.DurationMs);
    }

    [Fact]
    public void LateSpansAreStoredUpToTheCapThenOnlyWhenSlow()
    {
        var agent = Agent(spanMax: 2, thresholdMs: 100);
        var start = DateTime.UtcNow;
        var state = Request(agent, start);
        // The request's own rows fill its cap; the late cap is separate.
        agent.RecordSpan("db", "SELECT a", null, null, start, 1, null, null);
        agent.RecordSpan("db", "SELECT b", null, null, start, 1, null, null);
        state.Close();

        agent.RecordSpan("db", "SELECT late1", null, null, start, 5, null, null);
        agent.RecordSpan("db", "SELECT late2", null, null, start, 5, null, null);
        agent.RecordSpan("db", "SELECT late3", null, null, start, 5, null, null);
        agent.RecordSpan("db", "SELECT late4", null, null, start, 150, null, null);

        var names = Queued(agent).Select(s => s.Name).ToList();
        Assert.Equal(new[] { "SELECT late1", "SELECT late2", "SELECT late4" }, names);
    }

    [Fact]
    public void LateSpansAreLeftOutOfTheTotals()
    {
        var agent = Agent();
        var start = DateTime.UtcNow;
        var state = Request(agent, start);
        agent.RecordSpan("db", "SELECT a", null, null, start, 10, null, null);
        agent.RecordSpan("http", "GET example.com", null, null, start, 20, null, null);
        agent.RecordSpan("background", "warm cache", null, null, start, 30, null, null);
        var (totals, spans) = state.Close();

        agent.RecordSpan("db", "SELECT late", null, null, start, 40, null, null);
        agent.RecordSpan("http", "GET late.example.com", null, null, start, 50, null, null);

        Assert.Equal(new SpanTotals(1, 10, 1, 20), totals);
        Assert.Equal(3, spans.Count);
        Assert.False(state.Add(new SpanRecord { StartedAt = start, Kind = "db", Name = "x", DurationMs = 1 }));
        var late = Queued(agent);
        Assert.Equal(2, late.Count);
        Assert.All(late, s => Assert.Equal("req-1", s.RequestId));
    }

    [Fact]
    public async Task BackgroundRecordsASpanForTheRequest()
    {
        var agent = Agent();
        var start = DateTime.UtcNow;
        var state = Request(agent, start);
        var gate = new TaskCompletionSource();

        var task = Visionary.Background("send welcome email", async ct =>
        {
            await gate.Task;
            await Task.Delay(50, ct);
            agent.RecordSpan("db", "INSERT emails", null, null, DateTime.UtcNow, 1, null, null);
        });
        var (_, inRequest) = state.Close(); // the response went out before the work finished
        var requestMs = (DateTime.UtcNow - start).TotalMilliseconds;
        gate.SetResult();
        await task;

        Assert.Empty(inRequest);
        var spans = Queued(agent);
        var bg = Assert.Single(spans, s => s.Kind == "background");
        Assert.Equal("send welcome email", bg.Name);
        Assert.Equal("req-1", bg.RequestId);
        Assert.Null(bg.Error);
        Assert.True(bg.DurationMs >= 45, $"duration {bg.DurationMs}");
        Assert.True(bg.OffsetMs >= 0 && bg.OffsetMs < 1000, $"offset {bg.OffsetMs}");
        Assert.True(bg.OffsetMs + bg.DurationMs > requestMs);
        var inner = Assert.Single(spans, s => s.Kind == "db");
        Assert.Equal("req-1", inner.RequestId);
        Assert.True(inner.OffsetMs >= bg.OffsetMs);
    }

    [Fact]
    public async Task BackgroundRecordsTheErrorWhenTheWorkThrows()
    {
        var agent = Agent();
        var state = Request(agent, DateTime.UtcNow);
        state.Close();

        var task = Visionary.Background("charge card", async _ =>
        {
            await Task.Yield();
            throw new InvalidOperationException("card declined");
        });
        var ex = await Assert.ThrowsAsync<InvalidOperationException>(() => task);
        Assert.Equal("card declined", ex.Message);

        var bg = Assert.Single(Queued(agent));
        Assert.Equal("background", bg.Kind);
        Assert.Equal("charge card", bg.Name);
        Assert.Equal("req-1", bg.RequestId);
        Assert.NotNull(bg.OffsetMs);
        Assert.Equal("card declined", bg.Error);
    }

    [Fact]
    public async Task BackgroundOutsideARequestIsKeptLikeOtherLoneSpans()
    {
        var agent = Agent(thresholdMs: 100);

        await Visionary.Background("quick", _ => Task.CompletedTask);
        await Visionary.Background("slow", ct => Task.Delay(150, ct));

        var bg = Assert.Single(Queued(agent));
        Assert.Equal("slow", bg.Name);
        Assert.Null(bg.RequestId);
        Assert.Null(bg.OffsetMs);
    }

    [Fact]
    public async Task BackgroundWorkIsNotCancelledWithTheRequest()
    {
        var agent = Agent();
        var ctx = new DefaultHttpContext();
        using var aborted = new System.Threading.CancellationTokenSource();
        ctx.RequestAborted = aborted.Token;
        var state = new RequestState(ctx, "req-2", DateTime.UtcNow, 100);
        state.Enter();

        var cancelled = true;
        var task = Visionary.Background("after abort", async ct =>
        {
            await Task.Delay(30);
            cancelled = ct.IsCancellationRequested;
        });
        aborted.Cancel();
        state.Close();
        await task;

        Assert.False(cancelled);
        Assert.Null(Assert.Single(Queued(agent)).Error);
    }
}
