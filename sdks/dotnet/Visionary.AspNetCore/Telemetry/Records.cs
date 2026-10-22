using System;
using System.Collections.Generic;

namespace Styropyr0.Visionary;

/// <summary>Something for the writer to store: a request (with its spans), a span, an error or a release.</summary>
internal abstract record WriteItem;

/// <summary>
/// The raw capture of one request. Redaction, error-field extraction and user lookup happen on
/// the writer, off the request path.
/// </summary>
internal sealed record RequestEntry : WriteItem
{
    public required string RequestId { get; init; }
    public required DateTime StartedAt { get; init; }
    public required string Method { get; init; }
    public required string Path { get; init; }
    public string? Route { get; init; }
    public string? Query { get; init; }
    public int Status { get; init; }
    public double DurationMs { get; init; }
    public string? Ip { get; init; }
    public string? UserAgent { get; init; }
    public VisionaryUser? User { get; init; }
    public required IReadOnlyList<KeyValuePair<string, string>> RequestHeaders { get; init; }
    public string? RequestContentType { get; init; }
    public string? RequestBody { get; init; }
    public bool RequestTruncated { get; init; }
    public long RequestSize { get; init; }
    public string? ResponseContentType { get; init; }
    public string? ResponseBody { get; init; }
    public bool ResponseTruncated { get; init; }
    public long ResponseSize { get; init; }
    public SpanTotals? Totals { get; init; }
    public IReadOnlyList<SpanRecord> Spans { get; init; } = Array.Empty<SpanRecord>();
}

/// <summary>Database and outbound HTTP calls a request made; null when spans are off.</summary>
internal sealed record SpanTotals(long DbCount, double DbMs, long HttpCount, double HttpMs);

/// <summary>One call the service made: a database statement, an outbound HTTP request, or a custom operation.</summary>
internal sealed record SpanRecord : WriteItem
{
    public required DateTime StartedAt { get; init; }
    public string? RequestId { get; init; }
    public required string Kind { get; init; }
    public required string Name { get; init; }
    public string? Target { get; init; }
    public string? Detail { get; init; }
    public double? OffsetMs { get; init; }
    public double DurationMs { get; init; }
    public string? Error { get; init; }
    public long? RowCount { get; init; }
}

internal sealed record ErrorRecord : WriteItem
{
    public required DateTime CreatedAt { get; init; }
    public string? RequestId { get; init; }
    public string? Method { get; init; }
    public string? Route { get; init; }
    public string? Path { get; init; }
    public string? UserId { get; init; }
    public required string Type { get; init; }
    public string? Message { get; init; }
    public string? Stack { get; init; }
    public required string Fingerprint { get; init; }
    public bool Handled { get; init; }
}

/// <summary>One CPU profile (SPEC.md "CPU profiles"): folded stacks plus the busy time per route.</summary>
internal sealed record ProfileRecord : WriteItem
{
    public required DateTime CreatedAt { get; init; }
    public double DurationMs { get; init; }
    public long Samples { get; init; }
    public required string Routes { get; init; }
    public required string Folded { get; init; }
}

/// <summary>The deploy marker written once at start when the version is known.</summary>
internal sealed record ReleaseRecord(DateTime CreatedAt) : WriteItem;
