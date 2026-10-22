using System;
using System.Collections.Generic;
using System.Threading;

namespace Styropyr0.Visionary;

/// <summary>
/// What the CPU profiler needs from the middleware: how many requests have started (it skips a
/// turn when none arrived since the last), and while a profile runs, the milliseconds the
/// requests that finished took per "METHOD route". Costs one increment per request otherwise.
/// </summary>
internal sealed class RequestTally
{
    private readonly object _lock = new();
    private long _started;
    private Dictionary<string, double>? _busy;

    /// <summary>Requests started since the process began.</summary>
    public long Started => Interlocked.Read(ref _started);

    /// <summary>Whether a profile is collecting route times right now.</summary>
    public bool Collecting => Volatile.Read(ref _busy) is not null;

    public void Start() => Interlocked.Increment(ref _started);

    /// <summary>Counts a finished request while a profile runs; a no-op otherwise.</summary>
    public void Finish(string method, string? route, string path, double durationMs)
    {
        if (Volatile.Read(ref _busy) is null)
        {
            return;
        }
        var key = method + " " + (string.IsNullOrEmpty(route) ? path : route);
        lock (_lock)
        {
            if (_busy is { } busy)
            {
                busy[key] = (busy.TryGetValue(key, out var ms) ? ms : 0) + durationMs;
            }
        }
    }

    /// <summary>Starts collecting route times for a new profile.</summary>
    public void BeginProfile()
    {
        lock (_lock)
        {
            Volatile.Write(ref _busy, new Dictionary<string, double>(StringComparer.Ordinal));
        }
    }

    /// <summary>Stops collecting and returns the route times, each rounded and at least 1 ms.</summary>
    public Dictionary<string, long> EndProfile()
    {
        Dictionary<string, double>? busy;
        lock (_lock)
        {
            busy = _busy;
            Volatile.Write(ref _busy, null);
        }
        var routes = new Dictionary<string, long>(StringComparer.Ordinal);
        if (busy is not null)
        {
            foreach (var (k, ms) in busy)
            {
                routes[k] = Math.Max(1, (long)Math.Round(ms));
            }
        }
        return routes;
    }
}
