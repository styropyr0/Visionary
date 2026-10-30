using System;
using System.Collections.Concurrent;
using System.Collections.Generic;
using System.Diagnostics.Metrics;
using System.Linq;

namespace Styropyr0.Visionary;

/// <summary>
/// Records System.Diagnostics.Metrics instruments of the meters named in VISIONARY_METRICS_METERS
/// as custom metrics: counters as counters, up-down counters and gauges as gauges, histograms as
/// histograms, with the measurement's tags as labels. Observable counters are left out, since
/// they report running totals.
/// </summary>
internal sealed class MeterBridge : IDisposable
{
    private static readonly ConcurrentDictionary<Instrument, string?> Kinds = new();
    private readonly MeterListener _listener = new();
    private readonly HashSet<string> _meters;

    public MeterBridge(IEnumerable<string> meters)
    {
        _meters = new HashSet<string>(meters, StringComparer.Ordinal);
        _listener.InstrumentPublished = (instrument, listener) =>
        {
            if (_meters.Contains(instrument.Meter.Name) && Kind(instrument) is not null)
            {
                listener.EnableMeasurementEvents(instrument);
            }
        };
        _listener.SetMeasurementEventCallback<byte>((i, v, t, _) => Record(i, v, t));
        _listener.SetMeasurementEventCallback<short>((i, v, t, _) => Record(i, v, t));
        _listener.SetMeasurementEventCallback<int>((i, v, t, _) => Record(i, v, t));
        _listener.SetMeasurementEventCallback<long>((i, v, t, _) => Record(i, v, t));
        _listener.SetMeasurementEventCallback<float>((i, v, t, _) => Record(i, v, t));
        _listener.SetMeasurementEventCallback<double>((i, v, t, _) => Record(i, v, t));
        _listener.SetMeasurementEventCallback<decimal>((i, v, t, _) => Record(i, (double)v, t));
        _listener.Start();
    }

    /// <summary>Reads observable gauges; called before each interval is collected.</summary>
    public void Observe() => _listener.RecordObservableInstruments();

    public void Dispose()
    {
        _listener.Dispose();
        Kinds.Clear();
    }

    private static string? Kind(Instrument i) => Kinds.GetOrAdd(i, KindOf);

    private static string? KindOf(Instrument i)
    {
        var type = i.GetType().Name;
        return type switch
        {
            _ when type.StartsWith("Counter`", StringComparison.Ordinal) => MetricSeries.Counter,
            _ when type.StartsWith("Histogram`", StringComparison.Ordinal) => MetricSeries.Histogram,
            _ when type.StartsWith("UpDownCounter`", StringComparison.Ordinal) => "updown",
            _ when type.StartsWith("Gauge`", StringComparison.Ordinal) || type.StartsWith("ObservableGauge`", StringComparison.Ordinal) => MetricSeries.Gauge,
            _ => null,
        };
    }

    private static void Record(Instrument i, double value, ReadOnlySpan<KeyValuePair<string, object?>> tags)
    {
        var kind = Kind(i);
        if (kind is null)
        {
            return;
        }
        var labels = tags.ToArray().Select(t => new KeyValuePair<string, string?>(t.Key, t.Value?.ToString()));
        if (i.Unit is { Length: > 0 } unit)
        {
            MetricRegistry.Shared.DescribeUnit(i.Name, unit);
        }
        var series = MetricRegistry.Shared.Get(i.Name, kind == "updown" ? MetricSeries.Gauge : kind, labels);
        switch (kind)
        {
            case MetricSeries.Counter:
                new VisionaryCounter(series).Add(value);
                break;
            case MetricSeries.Histogram:
                new VisionaryHistogram(series).Observe(value);
                break;
            case "updown":
                new VisionaryGauge(series).Add(value);
                break;
            default:
                new VisionaryGauge(series).Set(value);
                break;
        }
    }
}
