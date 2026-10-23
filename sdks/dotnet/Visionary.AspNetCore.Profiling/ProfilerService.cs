using System;
using System.Collections.Generic;
using System.IO;
using System.Text.Encodings.Web;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using Microsoft.Extensions.Hosting;
using Microsoft.Extensions.Logging;

namespace Styropyr0.Visionary.Profiling;

/// <summary>
/// Takes CPU profiles while the dashboard has profiling on (SPEC.md "CPU profiles"): reads
/// visionary_settings at start and every 30 seconds, samples for duration_ms every interval_s
/// while requests are being served, and takes one at once for a fresh "Profile now". A scheduled
/// profile is kept with at least <see cref="Folder.MinSamples"/> samples, a "Profile now" one with
/// any, whether or not a request finished while it ran. Runs on its own background service,
/// never on the request path.
/// </summary>
internal sealed class ProfilerService : BackgroundService
{
    private static readonly TimeSpan Poll = TimeSpan.FromSeconds(30);
    private static readonly TimeSpan Tick = TimeSpan.FromSeconds(5);

    private static readonly JsonSerializerOptions RouteJson = new() { Encoder = JavaScriptEncoder.UnsafeRelaxedJsonEscaping };

    private readonly VisionaryAgent _agent;
    private readonly Func<DateTime> _clock;
    private readonly Func<TimeSpan, CancellationToken, Task<Recording>> _record;
    private readonly Func<Recording, FoldedProfile> _fold;
    private readonly DateTime _startedAt;
    private ProfileSettings? _settings;
    private DateTime _polledAt = DateTime.MinValue;
    private DateTime _nextAt;
    private string _seenNow = "";
    private bool _on;
    private bool _readFailedLogged;
    private bool _recordFailedLogged;
    private long _lastStarted;
    private FrameNamer? _namer;

    public ProfilerService(VisionaryAgent agent) : this(agent, null, null, null)
    {
    }

    /// <summary>For tests: a clock, and a recorder and folder that don't sample.</summary>
    internal ProfilerService(VisionaryAgent agent, Func<DateTime>? clock,
        Func<TimeSpan, CancellationToken, Task<Recording>>? record, Func<Recording, FoldedProfile>? fold)
    {
        _agent = agent;
        _clock = clock ?? (() => DateTime.UtcNow);
        _record = record ?? EventPipeSampler.RecordAsync;
        _fold = fold ?? FoldWithEventPipe;
        _startedAt = _clock();
    }

    /// <summary>Whether scheduled profiling is on for this service.</summary>
    internal bool On => _on;

    /// <summary>Profiles queued for writing so far.</summary>
    internal int Taken { get; private set; }

    /// <summary>Sampling windows run so far, kept or dropped.</summary>
    internal int Recorded { get; private set; }

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        if (!_agent.Enabled)
        {
            return;
        }
        if (!_agent.Options.Profile)
        {
            _agent.Logger.LogInformation("visionary: CPU profiles off (VISIONARY_PROFILE=false)");
            return;
        }
        // The settings reads are the SDK's own work, not the app's spans.
        Suppress.Enter();
        await Task.Yield();
        while (!stoppingToken.IsCancellationRequested)
        {
            try
            {
                await TickAsync(stoppingToken).ConfigureAwait(false);
                await Task.Delay(Tick, stoppingToken).ConfigureAwait(false);
            }
            catch (OperationCanceledException) when (stoppingToken.IsCancellationRequested)
            {
                break;
            }
            catch (Exception e)
            {
                _agent.Logger.LogWarning("visionary: CPU profiler: {Error}", e.Message);
                try
                {
                    await Task.Delay(Tick, stoppingToken).ConfigureAwait(false);
                }
                catch (OperationCanceledException)
                {
                    break;
                }
            }
        }
    }

    /// <summary>One turn: re-read the settings when due, then profile if asked to.</summary>
    internal async Task TickAsync(CancellationToken ct)
    {
        var now = _clock();
        if (now - _polledAt >= Poll)
        {
            _polledAt = now;
            _settings = await ReadSettingsAsync(ct).ConfigureAwait(false);
        }
        var s = _settings;
        var mine = s is not null && s.Serves(_agent.Service);
        var on = mine && s!.Enabled;
        if (on != _on)
        {
            _on = on;
            if (on)
            {
                _agent.Logger.LogInformation("visionary: CPU profiles on, {Duration} ms every {Interval} s while serving requests",
                    s!.DurationMs, s.IntervalS);
            }
            else
            {
                _agent.Logger.LogInformation("visionary: CPU profiles off");
            }
            _nextAt = now;
        }
        if (s is null || !mine)
        {
            return;
        }
        // "Profile now": a stamp not seen before, set while this process was up and not long ago.
        if (s.Now.Length > 0 && s.Now != _seenNow)
        {
            _seenNow = s.Now;
            if (ProfileSettings.FreshNow(s.Now, _startedAt, now))
            {
                await ProfileAsync(s, 1, ct).ConfigureAwait(false);
                return;
            }
        }
        if (!on || now < _nextAt)
        {
            return;
        }
        _nextAt = now + TimeSpan.FromSeconds(s.IntervalS);
        // No request since the last profile: nothing worth sampling.
        if (_agent.Tally.Started == _lastStarted)
        {
            return;
        }
        await ProfileAsync(s, Folder.MinSamples, ct).ConfigureAwait(false);
    }

    private async Task<ProfileSettings?> ReadSettingsAsync(CancellationToken ct)
    {
        try
        {
            using var timeout = CancellationTokenSource.CreateLinkedTokenSource(ct);
            timeout.CancelAfter(TimeSpan.FromSeconds(20));
            var rows = await _agent.Store!.RowsAsync(ProfileSettings.Query, timeout.Token).ConfigureAwait(false);
            return ProfileSettings.Parse(rows);
        }
        catch (Exception e) when (!ct.IsCancellationRequested)
        {
            // No table yet: the Visionary server hasn't started. Off until it has.
            if (!_readFailedLogged)
            {
                _readFailedLogged = true;
                _agent.Logger.LogInformation("visionary: CPU profiles off until visionary_settings can be read: {Error}", e.Message);
            }
            return null;
        }
    }

    /// <summary>
    /// Samples for duration_ms and queues the profile when it has at least
    /// <paramref name="minSamples"/> samples; routes is {} when no request finished.
    /// </summary>
    private async Task ProfileAsync(ProfileSettings s, int minSamples, CancellationToken ct)
    {
        _lastStarted = _agent.Tally.Started;
        _agent.Tally.BeginProfile();
        Recording rec;
        Dictionary<string, long> routes;
        try
        {
            rec = await _record(TimeSpan.FromMilliseconds(s.DurationMs), ct).ConfigureAwait(false);
        }
        catch (Exception e) when (!ct.IsCancellationRequested)
        {
            // DOTNET_EnableDiagnostics=0, no writable temp directory, or no diagnostics port.
            if (!_recordFailedLogged)
            {
                _recordFailedLogged = true;
                _agent.Logger.LogWarning("visionary: CPU profile failed: {Error}", e.Message);
            }
            return;
        }
        finally
        {
            // Only requests that finished while sampling count.
            routes = _agent.Tally.EndProfile();
        }
        Recorded++;
        FoldedProfile profile;
        var reading = System.Diagnostics.Stopwatch.StartNew();
        try
        {
            // Reading the trace back is CPU work of its own: off the timer's thread.
            profile = await Task.Run(() => _fold(rec), CancellationToken.None).ConfigureAwait(false);
            _agent.Logger.LogDebug("visionary: CPU profile of {Duration:0} ms: {Samples} samples, read back in {Read:0} ms",
                rec.DurationMs, profile.Samples, reading.Elapsed.TotalMilliseconds);
        }
        catch (Exception e)
        {
            _agent.Logger.LogWarning("visionary: reading a CPU profile failed: {Error}", e.Message);
            return;
        }
        if (profile.Samples < minSamples)
        {
            _agent.Logger.LogDebug("visionary: CPU profile dropped ({Samples} samples, {Routes} routes)", profile.Samples, routes.Count);
            return;
        }
        Taken++;
        _agent.Enqueue(new ProfileRecord
        {
            CreatedAt = rec.StartedAt,
            DurationMs = rec.DurationMs,
            Samples = profile.Samples,
            Routes = JsonSerializer.Serialize(routes, RouteJson),
            Folded = profile.Folded,
        });
    }

    private FoldedProfile FoldWithEventPipe(Recording rec)
    {
        _namer ??= new FrameNamer(CodeOwners.ForCurrentApp(), Directory.GetCurrentDirectory());
        return EventPipeSampler.Fold(rec, _namer);
    }
}
