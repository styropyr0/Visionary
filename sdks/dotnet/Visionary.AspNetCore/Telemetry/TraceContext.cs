using System;
using System.Diagnostics;
using System.Threading;
using Microsoft.AspNetCore.Http;

namespace Styropyr0.Visionary;

/// <summary>A span that calls can belong to: its trace, its id, the trace flags and the caller's tracestate.</summary>
internal readonly record struct SpanContext(string TraceId, string SpanId, string Flags = "01", string? State = null)
{
    /// <summary>The traceparent header naming this span as the parent.</summary>
    public string Traceparent => $"00-{TraceId}-{SpanId}-{Flags}";
}

/// <summary>The ids a span row is written with.</summary>
internal readonly record struct SpanIds(string TraceId, string SpanId, string? ParentSpanId);

/// <summary>A request's server span and the caller's span from traceparent, if any.</summary>
internal readonly record struct RequestTrace(SpanContext Span, string? ParentSpanId);

/// <summary>
/// W3C trace context (https://www.w3.org/TR/trace-context/). A request continues the trace in
/// its traceparent header (or the ASP.NET Core activity that already read it) or starts one;
/// every call gets its own span id; outbound HttpClient requests carry the trace on.
/// </summary>
internal static class TraceContext
{
    public const string TraceparentHeader = "traceparent";
    public const string TracestateHeader = "tracestate";

    private static readonly AsyncLocal<SpanContext?> CurrentSpan = new();

    /// <summary>The innermost span of this async flow: a custom or background span, else the request's. Null outside any trace.</summary>
    public static SpanContext? Current
    {
        get => CurrentSpan.Value;
        set => CurrentSpan.Value = value;
    }

    /// <summary>
    /// Reads a traceparent header. Null for anything malformed, for version ff and for all-zero
    /// ids, which mean "no parent".
    /// </summary>
    public static SpanContext? Parse(string? header)
    {
        var parts = (header ?? "").Trim().Split('-');
        if (parts.Length < 4 || parts[0].Length != 2 || parts[0] == "ff" || parts[1].Length != 32 || parts[2].Length != 16 || parts[3].Length != 2)
        {
            return null;
        }
        // Version 00 has exactly four fields; later versions may append more.
        if (parts[0] == "00" && parts.Length != 4)
        {
            return null;
        }
        for (var i = 0; i < 4; i++)
        {
            if (!IsLowerHex(parts[i]))
            {
                return null;
            }
        }
        if (parts[1].Trim('0').Length == 0 || parts[2].Trim('0').Length == 0)
        {
            return null;
        }
        return new SpanContext(parts[1], parts[2], parts[3]);
    }

    /// <summary>
    /// The server span of an incoming request, made current for everything the handler does.
    /// An ASP.NET Core (or OpenTelemetry) activity has already read the header; its ids are used
    /// so both agree. Without one the header is the source.
    /// </summary>
    public static RequestTrace BeginRequest(HttpContext ctx)
    {
        RequestTrace trace;
        if (FromActivity(Activity.Current) is { } fromActivity)
        {
            trace = fromActivity;
        }
        else if (Parse(ctx.Request.Headers[TraceparentHeader].ToString()) is { } caller)
        {
            var state = ctx.Request.Headers[TracestateHeader].ToString();
            trace = new RequestTrace(caller with { SpanId = NewSpanId(), State = state.Length > 0 ? state : null }, caller.SpanId);
        }
        else
        {
            trace = new RequestTrace(new SpanContext(NewTraceId(), NewSpanId()), null);
        }
        Current = trace.Span;
        return trace;
    }

    internal static RequestTrace? FromActivity(Activity? a)
    {
        if (a is null || a.IdFormat != ActivityIdFormat.W3C || a.TraceId == default || a.SpanId == default)
        {
            return null;
        }
        var parent = a.ParentSpanId == default ? null : a.ParentSpanId.ToHexString();
        // A root activity has no flags of its own; a trace this service starts is sampled.
        var flags = parent is null ? "01" : ((byte)a.ActivityTraceFlags).ToString("x2");
        var state = string.IsNullOrEmpty(a.TraceStateString) ? null : a.TraceStateString;
        return new RequestTrace(new SpanContext(a.TraceId.ToHexString(), a.SpanId.ToHexString(), flags, state), parent);
    }

    /// <summary>
    /// Ids for a new span under the current one; outside any trace the span starts one, so the
    /// service it reaches can still be followed.
    /// </summary>
    public static (SpanContext Span, string? ParentSpanId) Child()
    {
        if (Current is { } parent)
        {
            return (parent with { SpanId = NewSpanId() }, parent.SpanId);
        }
        return (new SpanContext(NewTraceId(), NewSpanId()), null);
    }

    public static SpanIds Ids(SpanContext span, string? parentSpanId) => new(span.TraceId, span.SpanId, parentSpanId);

    public static string NewTraceId() => NewId(16);

    public static string NewSpanId() => NewId(8);

    private static string NewId(int bytes)
    {
        Span<byte> b = stackalloc byte[bytes];
        do
        {
            Random.Shared.NextBytes(b);
        }
        while (b.IndexOfAnyExcept((byte)0) < 0);
        return Convert.ToHexString(b).ToLowerInvariant();
    }

    private static bool IsLowerHex(string s)
    {
        foreach (var c in s)
        {
            if (c is not (>= '0' and <= '9' or >= 'a' and <= 'f'))
            {
                return false;
            }
        }
        return true;
    }
}
