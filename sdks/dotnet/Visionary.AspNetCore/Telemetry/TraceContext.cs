using System;
using System.Diagnostics;
using System.Threading;
using Microsoft.AspNetCore.Http;

namespace Styropyr0.Visionary;

internal readonly record struct SpanContext(string TraceId, string SpanId, string Flags = "01", string? State = null)
{
    public string Traceparent => $"00-{TraceId}-{SpanId}-{Flags}";
}

internal readonly record struct SpanIds(string TraceId, string SpanId, string? ParentSpanId);

internal readonly record struct RequestTrace(SpanContext Span, string? ParentSpanId);

internal static class TraceContext
{
    public const string TraceparentHeader = "traceparent";
    public const string TracestateHeader = "tracestate";

    private static readonly AsyncLocal<SpanContext?> CurrentSpan = new();

    public static SpanContext? Current
    {
        get => CurrentSpan.Value;
        set => CurrentSpan.Value = value;
    }

    // Null for anything malformed, version ff or all-zero ids
    public static SpanContext? Parse(string? header)
    {
        var parts = (header ?? "").Trim().Split('-');
        if (parts.Length < 4 || parts[0].Length != 2 || parts[0] == "ff" || parts[1].Length != 32 || parts[2].Length != 16 || parts[3].Length != 2)
        {
            return null;
        }
        // Version 00 has exactly four fields, later versions may append more
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

    // Reuses the ids of an ASP.NET Core activity when there is one so both agree
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
        // A root activity has no flags, and traces this service starts are sampled
        var flags = parent is null ? "01" : ((byte)a.ActivityTraceFlags).ToString("x2");
        var state = string.IsNullOrEmpty(a.TraceStateString) ? null : a.TraceStateString;
        return new RequestTrace(new SpanContext(a.TraceId.ToHexString(), a.SpanId.ToHexString(), flags, state), parent);
    }

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
