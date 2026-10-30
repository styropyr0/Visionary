using System;
using System.Collections.Generic;
using System.Globalization;
using System.Linq;
using System.Security.Cryptography;
using System.Text;
using System.Threading;

namespace Styropyr0.Visionary;

// Custom metrics (SPEC.md "Custom metrics"): values add up in memory and the metrics writer
// stores one row per series every interval. Recording never waits for the database.

internal sealed class MetricSeries
{
    public const string Counter = "counter";
    public const string Gauge = "gauge";
    public const string Histogram = "histogram";

    public required string Name { get; init; }
    public required string Kind { get; init; }
    public string? Unit { get; init; }
    public required string Labels { get; init; }
    public required string Id { get; init; }
    public double[] Bounds { get; init; } = Array.Empty<double>();

    public readonly object Lock = new();
    public bool Touched;
    public double Total;
    public double Value;
    public long[] Counts = Array.Empty<long>();
    public long Count;
    public double Sum;
    public double Min;
    public double Max;
}

/// <summary>One row of metrics.</summary>
internal sealed record MetricRecord
{
    public required DateTime At { get; init; }
    public required string Name { get; init; }
    public required string Kind { get; init; }
    public string? Unit { get; init; }
    public required string Labels { get; init; }
    public required string Series { get; init; }
    public double Value { get; init; }
    public long? Count { get; init; }
    public double? Sum { get; init; }
    public double? Min { get; init; }
    public double? Max { get; init; }
    public string? Buckets { get; init; }
}

/// <summary>Every series the process has recorded; series are never forgotten.</summary>
internal sealed class MetricRegistry
{
    public const int MaxName = 200;
    public const int MaxLabels = 20;
    public const int MaxLabelKey = 100;
    public const int MaxLabelValue = 200;
    public const int DefaultMaxSeries = 1000;

    /// <summary>OpenTelemetry's default histogram bounds, which suit milliseconds.</summary>
    public static readonly double[] DefaultBuckets = { 0, 5, 10, 25, 50, 75, 100, 250, 500, 750, 1000, 2500, 5000, 7500, 10000 };

    public static MetricRegistry Shared { get; } = new();

    private readonly object _lock = new();
    private readonly Dictionary<string, MetricSeries> _series = new(StringComparer.Ordinal);
    private readonly Dictionary<string, int> _perName = new(StringComparer.Ordinal);
    private Dictionary<string, long> _dropped = new(StringComparer.Ordinal);
    private readonly Dictionary<string, (string? Unit, double[]? Buckets)> _described = new(StringComparer.Ordinal);
    private int _maxSeries = DefaultMaxSeries;
    private volatile bool _off;

    public int MaxSeries
    {
        get => Volatile.Read(ref _maxSeries);
        set => Volatile.Write(ref _maxSeries, value > 0 ? value : DefaultMaxSeries);
    }

    /// <summary>VISIONARY_METRICS=false: every call records nothing.</summary>
    public bool Off
    {
        get => _off;
        set => _off = value;
    }

    public void Describe(string name, string? unit, IEnumerable<double>? buckets)
    {
        name = CleanName(name);
        if (name.Length == 0)
        {
            return;
        }
        var b = buckets?.Where(double.IsFinite).Distinct().Order().ToArray();
        lock (_lock)
        {
            _described[name] = (string.IsNullOrWhiteSpace(unit) ? null : unit, b is { Length: > 0 } ? b : null);
        }
    }

    /// <summary>Sets the unit of a metric that was not described.</summary>
    public void DescribeUnit(string name, string unit)
    {
        name = CleanName(name);
        lock (_lock)
        {
            _described.TryAdd(name, (unit, null));
        }
    }

    /// <summary>The series, created on first use; null when it records nothing (no name, off, over the cap).</summary>
    public MetricSeries? Get(string? name, string kind, IEnumerable<KeyValuePair<string, string?>>? labels)
    {
        if (_off)
        {
            return null;
        }
        name = CleanName(name);
        if (name.Length == 0)
        {
            return null;
        }
        var text = LabelsJson(labels);
        var key = kind + "\0" + name + "\0" + text;
        lock (_lock)
        {
            if (_series.TryGetValue(key, out var s))
            {
                return s;
            }
            _perName.TryGetValue(name, out var n);
            if (n >= MaxSeries)
            {
                _dropped[name] = _dropped.GetValueOrDefault(name) + 1;
                return null;
            }
            _described.TryGetValue(name, out var d);
            var bounds = kind == MetricSeries.Histogram ? d.Buckets ?? DefaultBuckets : Array.Empty<double>();
            s = new MetricSeries
            {
                Name = name,
                Kind = kind,
                Unit = d.Unit,
                Labels = text,
                Id = SeriesId(name, text),
                Bounds = bounds,
                Counts = kind == MetricSeries.Histogram ? new long[bounds.Length + 1] : Array.Empty<long>(),
            };
            _series[key] = s;
            _perName[name] = n + 1;
            return s;
        }
    }

    /// <summary>
    /// Takes each series' interval: counters and histograms that moved, and every gauge that was
    /// ever set. Series dropped over the cap come back counted by name.
    /// </summary>
    public (List<MetricRecord> Rows, Dictionary<string, long> Dropped) Collect(DateTime now)
    {
        MetricSeries[] all;
        Dictionary<string, long> dropped;
        lock (_lock)
        {
            all = _series.Values.ToArray();
            dropped = _dropped;
            if (dropped.Count > 0)
            {
                _dropped = new Dictionary<string, long>(StringComparer.Ordinal);
            }
        }
        var rows = new List<MetricRecord>();
        foreach (var s in all)
        {
            MetricRecord? row = null;
            lock (s.Lock)
            {
                switch (s.Kind)
                {
                    case MetricSeries.Counter when s.Total > 0:
                        row = Row(s, now, s.Total);
                        s.Total = 0;
                        break;
                    case MetricSeries.Gauge when s.Touched:
                        row = Row(s, now, s.Value);
                        break;
                    case MetricSeries.Histogram when s.Count > 0:
                        row = Row(s, now, s.Sum / s.Count) with
                        {
                            Count = s.Count,
                            Sum = s.Sum,
                            Min = s.Min,
                            Max = s.Max,
                            Buckets = BucketsJson(s.Bounds, s.Counts),
                        };
                        s.Count = 0;
                        s.Sum = s.Min = s.Max = 0;
                        Array.Clear(s.Counts);
                        break;
                }
            }
            if (row is not null)
            {
                rows.Add(row);
            }
        }
        rows.Sort((a, b) => string.CompareOrdinal(a.Series, b.Series));
        return (rows, dropped);
    }

    private static MetricRecord Row(MetricSeries s, DateTime now, double value) => new()
    {
        At = now,
        Name = s.Name,
        Kind = s.Kind,
        Unit = s.Unit,
        Labels = s.Labels,
        Series = s.Id,
        Value = value,
    };

    /// <summary>
    /// The canonical labels text: a JSON object with keys in UTF-8 byte order, escaped only where
    /// JSON requires, so every SDK writes the same bytes. Empty keys are dropped, keys and values
    /// cut to 100 and 200 bytes, and only the first 20 keys kept.
    /// </summary>
    public static string LabelsJson(IEnumerable<KeyValuePair<string, string?>>? labels)
    {
        var clean = new Dictionary<string, string>(StringComparer.Ordinal);
        if (labels is not null)
        {
            foreach (var (k, v) in labels)
            {
                var key = CutUtf8((k ?? "").Trim(), MaxLabelKey);
                if (key.Length > 0)
                {
                    clean[key] = CutUtf8(v ?? "", MaxLabelValue);
                }
            }
        }
        var keys = clean.Keys.Select(k => (Key: k, Bytes: Encoding.UTF8.GetBytes(k))).ToList();
        keys.Sort((a, b) => a.Bytes.AsSpan().SequenceCompareTo(b.Bytes));
        var sb = new StringBuilder("{");
        foreach (var (k, _) in keys.Take(MaxLabels))
        {
            if (sb.Length > 1)
            {
                sb.Append(',');
            }
            JsonString(sb, k);
            sb.Append(':');
            JsonString(sb, clean[k]);
        }
        return sb.Append('}').ToString();
    }

    /// <summary>The first 16 hex digits of SHA-256 over the name, a newline and the labels text.</summary>
    public static string SeriesId(string name, string labels) =>
        Convert.ToHexString(SHA256.HashData(Encoding.UTF8.GetBytes(name + "\n" + labels)), 0, 8).ToLowerInvariant();

    private static void JsonString(StringBuilder sb, string s)
    {
        sb.Append('"');
        foreach (var c in s)
        {
            if (c is '"' or '\\')
            {
                sb.Append('\\').Append(c);
            }
            else if (c < 0x20)
            {
                sb.Append("\\u00").Append(((int)c).ToString("x2", CultureInfo.InvariantCulture));
            }
            else
            {
                sb.Append(c);
            }
        }
        sb.Append('"');
    }

    private static string BucketsJson(double[] bounds, long[] counts)
    {
        var sb = new StringBuilder("{\"bounds\":[");
        sb.AppendJoin(',', bounds.Select(Number));
        sb.Append("],\"counts\":[");
        sb.AppendJoin(',', counts.Select(c => c.ToString(CultureInfo.InvariantCulture)));
        return sb.Append("]}").ToString();
    }

    // Shortest round-trip form, as Go's encoding/json writes it (1e+21, not 1E+21).
    private static string Number(double d) => d.ToString("R", CultureInfo.InvariantCulture).Replace('E', 'e');

    public static string CleanName(string? name) => CutUtf8((name ?? "").Trim(), MaxName);

    /// <summary>Cuts s to at most n UTF-8 bytes on a character boundary; broken surrogates become U+FFFD.</summary>
    public static string CutUtf8(string s, int n)
    {
        var bytes = Encoding.UTF8.GetBytes(s);
        if (bytes.Length <= n)
        {
            return Encoding.UTF8.GetString(bytes);
        }
        while (n > 0 && (bytes[n] & 0xC0) == 0x80)
        {
            n--;
        }
        return Encoding.UTF8.GetString(bytes, 0, n);
    }
}
