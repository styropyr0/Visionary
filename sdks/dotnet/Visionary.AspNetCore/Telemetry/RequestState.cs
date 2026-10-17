using System;
using System.Collections.Generic;
using System.Threading;
using Microsoft.AspNetCore.Http;

namespace Styropyr0.Visionary;

/// <summary>
/// What the middleware knows about the request in progress. It flows with the request's
/// async context, so spans and errors raised anywhere below the middleware find it.
/// </summary>
internal sealed class RequestState
{
    private static readonly AsyncLocal<RequestState?> CurrentState = new();

    private readonly object _lock = new();
    private readonly List<SpanRecord> _spans = new();
    private readonly int _maxSpans;
    private long _dbCount;
    private double _dbMs;
    private long _httpCount;
    private double _httpMs;
    private bool _closed;

    public RequestState(HttpContext context, string requestId, DateTime startedAt, int maxSpans)
    {
        Context = context;
        RequestId = requestId;
        StartedAt = startedAt;
        _maxSpans = maxSpans;
    }

    /// <summary>The request in progress on this async flow, if any.</summary>
    public static RequestState? Current
    {
        get
        {
            var s = CurrentState.Value;
            return s is null || s._closed ? null : s;
        }
    }

    public HttpContext Context { get; }

    public string RequestId { get; }

    public DateTime StartedAt { get; }

    public void Enter() => CurrentState.Value = this;

    /// <summary>
    /// Counts a finished call into the totals and keeps its row while under the cap. Returns
    /// false once the request has ended (a call that outlived it is treated as background work).
    /// </summary>
    public bool Add(SpanRecord span)
    {
        lock (_lock)
        {
            if (_closed)
            {
                return false;
            }
            switch (span.Kind)
            {
                case "db":
                    _dbCount++;
                    _dbMs += span.DurationMs;
                    break;
                case "http":
                    _httpCount++;
                    _httpMs += span.DurationMs;
                    break;
            }
            if (_spans.Count < _maxSpans)
            {
                _spans.Add(span);
            }
            return true;
        }
    }

    /// <summary>Ends span collection: the totals and the rows, in start order.</summary>
    public (SpanTotals Totals, List<SpanRecord> Spans) Close()
    {
        lock (_lock)
        {
            _closed = true;
            _spans.Sort((a, b) => a.StartedAt.CompareTo(b.StartedAt));
            return (new SpanTotals(_dbCount, Math.Round(_dbMs, 3), _httpCount, Math.Round(_httpMs, 3)), _spans);
        }
    }
}

/// <summary>
/// Marks the SDK's own background work, so its database writes and D1 calls never show up as
/// the application's spans.
/// </summary>
internal static class Suppress
{
    private static readonly AsyncLocal<bool> Flag = new();

    public static bool Active => Flag.Value;

    /// <summary>Suppresses spans for the rest of the calling async method and everything it calls.</summary>
    public static void Enter() => Flag.Value = true;
}
