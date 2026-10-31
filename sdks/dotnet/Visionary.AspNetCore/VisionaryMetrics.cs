using System;
using System.Collections.Generic;
using System.Linq;

namespace Styropyr0.Visionary;

public static partial class Visionary
{
    /// <summary>A counter, keep user or request ids out of labels (1000 series per name)</summary>
    /// <example><c>Visionary.Counter("orders_placed", ("region", "eu")).Add(1);</c></example>
    public static VisionaryCounter Counter(string name, params (string Key, string? Value)[] labels) =>
        new(MetricRegistry.Shared.Get(name, MetricSeries.Counter, Pairs(labels)));

    /// <summary>A counter with labels from a dictionary</summary>
    public static VisionaryCounter Counter(string name, IReadOnlyDictionary<string, string?> labels) =>
        new(MetricRegistry.Shared.Get(name, MetricSeries.Counter, labels));

    /// <summary>A gauge, every interval writes its last value once set</summary>
    /// <example><c>Visionary.Gauge("queue_depth", ("queue", "emails")).Set(n);</c></example>
    public static VisionaryGauge Gauge(string name, params (string Key, string? Value)[] labels) =>
        new(MetricRegistry.Shared.Get(name, MetricSeries.Gauge, Pairs(labels)));

    /// <summary>A gauge with labels from a dictionary</summary>
    public static VisionaryGauge Gauge(string name, IReadOnlyDictionary<string, string?> labels) =>
        new(MetricRegistry.Shared.Get(name, MetricSeries.Gauge, labels));

    /// <summary>A histogram of values such as durations in ms</summary>
    /// <example><c>Visionary.Histogram("checkout_ms").Observe(elapsed.TotalMilliseconds);</c></example>
    public static VisionaryHistogram Histogram(string name, params (string Key, string? Value)[] labels) =>
        new(MetricRegistry.Shared.Get(name, MetricSeries.Histogram, Pairs(labels)));

    /// <summary>A histogram with labels from a dictionary</summary>
    public static VisionaryHistogram Histogram(string name, IReadOnlyDictionary<string, string?> labels) =>
        new(MetricRegistry.Shared.Get(name, MetricSeries.Histogram, labels));

    /// <summary>Sets a metric's unit and histogram buckets, call it before the metric's first use</summary>
    public static void DescribeMetric(string name, string? unit = null, IEnumerable<double>? buckets = null) =>
        MetricRegistry.Shared.Describe(name, unit, buckets);

    private static IEnumerable<KeyValuePair<string, string?>> Pairs((string Key, string? Value)[] labels) =>
        labels.Select(l => new KeyValuePair<string, string?>(l.Key, l.Value));
}

/// <summary>One counter series</summary>
public sealed class VisionaryCounter
{
    private readonly MetricSeries? _s;

    internal VisionaryCounter(MetricSeries? s) => _s = s;

    /// <summary>Adds <paramref name="n"/>, ignored unless positive and finite</summary>
    public void Add(double n)
    {
        if (_s is null || !(n > 0) || double.IsInfinity(n))
        {
            return;
        }
        lock (_s.Lock)
        {
            _s.Total += n;
            _s.Touched = true;
        }
    }

    /// <summary>Adds 1</summary>
    public void Inc() => Add(1);
}

/// <summary>One gauge series</summary>
public sealed class VisionaryGauge
{
    private readonly MetricSeries? _s;

    internal VisionaryGauge(MetricSeries? s) => _s = s;

    /// <summary>Sets the gauge, ignored when not finite</summary>
    public void Set(double value)
    {
        if (_s is null || !double.IsFinite(value))
        {
            return;
        }
        lock (_s.Lock)
        {
            _s.Value = value;
            _s.Touched = true;
        }
    }

    /// <summary>Moves the gauge by <paramref name="delta"/>, from 0 when never set</summary>
    public void Add(double delta)
    {
        if (_s is null || !double.IsFinite(delta))
        {
            return;
        }
        lock (_s.Lock)
        {
            _s.Value += delta;
            _s.Touched = true;
        }
    }
}

/// <summary>One histogram series</summary>
public sealed class VisionaryHistogram
{
    private readonly MetricSeries? _s;

    internal VisionaryHistogram(MetricSeries? s) => _s = s;

    /// <summary>Records one value, ignored when not finite</summary>
    public void Observe(double value)
    {
        if (_s is null || !double.IsFinite(value))
        {
            return;
        }
        var i = Array.BinarySearch(_s.Bounds, value);
        if (i < 0)
        {
            i = ~i;
        }
        lock (_s.Lock)
        {
            _s.Counts[i]++;
            if (_s.Count == 0 || value < _s.Min)
            {
                _s.Min = value;
            }
            if (_s.Count == 0 || value > _s.Max)
            {
                _s.Max = value;
            }
            _s.Count++;
            _s.Sum += value;
        }
    }
}
