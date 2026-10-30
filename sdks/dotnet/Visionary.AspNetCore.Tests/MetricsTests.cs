using System;
using System.Collections.Generic;
using System.Diagnostics.Metrics;
using System.IO;
using System.Linq;
using System.Threading;
using System.Threading.Tasks;
using Microsoft.Data.Sqlite;
using Microsoft.Extensions.Configuration;
using Microsoft.Extensions.Logging.Abstractions;
using Microsoft.Extensions.Options;
using Xunit;

namespace Styropyr0.Visionary.Tests;

/// <summary>Custom metrics (SPEC.md "Custom metrics"): canonical series, aggregation, the cap and the rows.</summary>
[Collection(BackgroundTests.AgentCollection)]
public sealed class MetricsTests : IDisposable
{
    private readonly string _db = Path.Combine(Path.GetTempPath(), $"visionary-metrics-{Guid.NewGuid():N}.db");
    private readonly List<IDisposable> _owned = new();

    public void Dispose()
    {
        foreach (var d in _owned)
        {
            d.Dispose();
        }
        MetricRegistry.Shared.Off = false;
        MetricRegistry.Shared.MaxSeries = MetricRegistry.DefaultMaxSeries;
        SqliteConnection.ClearAllPools();
        try
        {
            File.Delete(_db);
        }
        catch (IOException)
        {
        }
    }

    private static IEnumerable<KeyValuePair<string, string?>> L(params (string K, string? V)[] kv) =>
        kv.Select(p => new KeyValuePair<string, string?>(p.K, p.V));

    [Fact]
    public void SeriesIsCanonical()
    {
        var text = MetricRegistry.LabelsJson(L(("region", "eu")));
        Assert.Equal("{\"region\":\"eu\"}", text);
        // Fixed in SPEC.md so every SDK can check itself against it.
        Assert.Equal("da4f5c6244c4f3e4", MetricRegistry.SeriesId("orders_placed", text));

        Assert.Equal("{\"a\\\"b\":\"x\\u000ay\",\"region\":\"eu\"}", MetricRegistry.LabelsJson(L(("region", "eu"), ("a\"b", "x\ny"), ("", "dropped"))));
        Assert.Equal("{}", MetricRegistry.LabelsJson(null));
        Assert.Equal("{\"k\":\"<é>\"}", MetricRegistry.LabelsJson(L(("k", "<é>"))));
        Assert.Equal("{\"k\":\"\"}", MetricRegistry.LabelsJson(L(("k", null))));
        // UTF-8 byte order: U+FF5E sorts before U+1F600, unlike UTF-16 order.
        Assert.Equal("{\"～\":\"1\",\"😀\":\"2\"}", MetricRegistry.LabelsJson(L(("😀", "2"), ("～", "1"))));
    }

    [Fact]
    public void LabelsAreCut()
    {
        var many = Enumerable.Range(0, 25).Select(i => ($"k{i:00}", (string?)"v")).ToArray();
        var text = MetricRegistry.LabelsJson(L(many));
        Assert.Contains("\"k19\"", text);
        Assert.DoesNotContain("\"k20\"", text);
        var cut = MetricRegistry.LabelsJson(L((new string('k', 150), new string('é', 150))));
        Assert.Equal("{\"" + new string('k', 100) + "\":\"" + new string('é', 100) + "\"}", cut);
        Assert.Equal(new string('n', 200), MetricRegistry.CleanName("  " + new string('n', 250) + " "));
    }

    [Fact]
    public void CollectAddsUpEachInterval()
    {
        var reg = new MetricRegistry();
        var eu = new VisionaryCounter(reg.Get("orders", MetricSeries.Counter, L(("region", "eu"))));
        eu.Add(2);
        new VisionaryCounter(reg.Get("orders", MetricSeries.Counter, L(("region", "eu")))).Inc();
        eu.Add(-5);
        eu.Add(double.NaN);
        eu.Add(double.PositiveInfinity);
        _ = reg.Get("orders", MetricSeries.Counter, L(("region", "us")));
        new VisionaryGauge(reg.Get("queue", MetricSeries.Gauge, null)).Set(7);
        new VisionaryGauge(reg.Get("orders", MetricSeries.Gauge, L(("region", "eu")))).Add(4);
        reg.Describe("took", "ms", new double[] { 100, 10, 10 });
        var h = new VisionaryHistogram(reg.Get("took", MetricSeries.Histogram, null));
        foreach (var v in new double[] { 5, 10, 50, 500 })
        {
            h.Observe(v);
        }
        h.Observe(double.NaN);

        var now = DateTime.UtcNow;
        var (rows, dropped) = reg.Collect(now);
        Assert.Empty(dropped);
        Assert.Equal(4, rows.Count); // the idle counter is skipped
        var counter = Assert.Single(rows, r => r.Kind == "counter");
        Assert.Equal(3, counter.Value);
        Assert.Equal("{\"region\":\"eu\"}", counter.Labels);
        Assert.Null(counter.Count);
        // Same name and labels as a gauge is a separate series.
        Assert.Equal(4, Assert.Single(rows, r => r.Kind == "gauge" && r.Name == "orders").Value);
        var hist = Assert.Single(rows, r => r.Kind == "histogram");
        Assert.Equal("ms", hist.Unit);
        Assert.Equal(4, hist.Count);
        Assert.Equal(565, hist.Sum);
        Assert.Equal(5, hist.Min);
        Assert.Equal(500, hist.Max);
        Assert.Equal(565 / 4.0, hist.Value);
        Assert.Equal("{\"bounds\":[10,100],\"counts\":[2,1,1]}", hist.Buckets);
        Assert.All(rows, r => Assert.Equal(now, r.At));

        // Next interval: the gauge repeats, the rest had nothing new.
        var (again, _) = reg.Collect(now);
        Assert.Equal(new[] { "gauge", "gauge" }, again.Select(r => r.Kind));
        new VisionaryHistogram(reg.Get("took", MetricSeries.Histogram, null)).Observe(-3);
        var neg = Assert.Single(reg.Collect(now).Rows, r => r.Kind == "histogram");
        Assert.Equal("{\"bounds\":[10,100],\"counts\":[1,0,0]}", neg.Buckets);
    }

    [Fact]
    public void DefaultBucketsAndBoundsAreInclusive()
    {
        var reg = new MetricRegistry();
        var h = new VisionaryHistogram(reg.Get("lat", MetricSeries.Histogram, null));
        h.Observe(0);
        h.Observe(5);
        h.Observe(5.5);
        h.Observe(20000);
        var row = Assert.Single(reg.Collect(DateTime.UtcNow).Rows);
        Assert.Equal("{\"bounds\":[0,5,10,25,50,75,100,250,500,750,1000,2500,5000,7500,10000],\"counts\":[1,1,1,0,0,0,0,0,0,0,0,0,0,0,0,1]}", row.Buckets);
    }

    [Fact]
    public void SeriesPastTheCapAreDroppedAndCounted()
    {
        var reg = new MetricRegistry { MaxSeries = 2 };
        new VisionaryCounter(reg.Get("hits", MetricSeries.Counter, L(("u", "1")))).Inc();
        new VisionaryCounter(reg.Get("hits", MetricSeries.Counter, L(("u", "2")))).Inc();
        Assert.Null(reg.Get("hits", MetricSeries.Counter, L(("u", "3"))));
        Assert.Null(reg.Get("hits", MetricSeries.Counter, L(("u", "4"))));
        new VisionaryCounter(reg.Get("hits", MetricSeries.Counter, L(("u", "1")))).Inc();
        Assert.NotNull(reg.Get("other", MetricSeries.Counter, L(("u", "3"))));
        Assert.Null(reg.Get(" ", MetricSeries.Counter, null));

        var (rows, dropped) = reg.Collect(DateTime.UtcNow);
        Assert.Equal(2, rows.Count);
        Assert.Equal(2, rows.Single(r => r.Labels == "{\"u\":\"1\"}").Value);
        Assert.Equal(2, dropped["hits"]);
        Assert.Empty(reg.Collect(DateTime.UtcNow).Dropped);
    }

    [Fact]
    public async Task WriterCreatesTheTableAndWritesRows()
    {
        var o = new VisionaryOptions { DatabaseUrl = "sqlite:///" + _db.Replace('\\', '/'), Service = "shop" };
        var agent = new VisionaryAgent(Options.Create(o), NullLoggerFactory.Instance);
        _owned.Add(agent);
        var reg = new MetricRegistry();
        var writer = new MetricsWriter(agent) { Registry = reg };
        _owned.Add(writer);

        // Nothing recorded: no table.
        _ = reg.Get("w_idle", MetricSeries.Counter, null);
        await writer.FlushAsync(DateTime.UtcNow, CancellationToken.None);
        Assert.Empty(Query("SELECT name FROM sqlite_master WHERE name = 'metrics'"));

        reg.Describe("w_checkout_ms", "ms", new double[] { 100 });
        new VisionaryCounter(reg.Get("w_orders_placed", MetricSeries.Counter, L(("region", "eu")))).Add(2);
        new VisionaryHistogram(reg.Get("w_checkout_ms", MetricSeries.Histogram, L(("method", "card")))).Observe(42);
        var at = new DateTime(2026, 10, 30, 12, 0, 15, DateTimeKind.Utc);
        await writer.FlushAsync(at, CancellationToken.None);

        var rows = Query("SELECT created_at, service, name, kind, unit, labels, series, value, count, sum, min, max, buckets FROM metrics ORDER BY name");
        Assert.Equal(2, rows.Count);
        Assert.Equal(new[] { "2026-10-30T12:00:15.000Z", "shop", "w_checkout_ms", "histogram", "ms", "{\"method\":\"card\"}" }, rows[0][..6]);
        Assert.Equal(new[] { "42", "1", "42", "42", "42", "{\"bounds\":[100],\"counts\":[1,0]}" }, rows[0][7..]);
        Assert.Equal(new[] { "w_orders_placed", "counter", null, "{\"region\":\"eu\"}", MetricRegistry.SeriesId("w_orders_placed", "{\"region\":\"eu\"}"), "2", null }, rows[1][2..9]);
    }

    [Fact]
    public void PublicApiRecordsOnTheSharedRegistry()
    {
        Visionary.DescribeMetric("p_took", "ms");
        Visionary.Counter("p_orders", ("region", "eu")).Add(2);
        Visionary.Counter("p_orders", new Dictionary<string, string?> { ["region"] = "eu" }).Inc();
        Visionary.Gauge("p_depth").Set(4);
        Visionary.Histogram("p_took").Observe(3);
        var rows = MetricRegistry.Shared.Collect(DateTime.UtcNow).Rows.Where(r => r.Name.StartsWith("p_", StringComparison.Ordinal)).ToList();
        Assert.Equal(3, Assert.Single(rows, r => r.Name == "p_orders").Value);
        Assert.Equal(4, Assert.Single(rows, r => r.Name == "p_depth").Value);
        Assert.Equal("ms", Assert.Single(rows, r => r.Name == "p_took").Unit);
    }

    [Fact]
    public void MetricsOffRecordsNothing()
    {
        var o = new VisionaryOptions { Metrics = false };
        _owned.Add(new MetricsWriter(new VisionaryAgent(Options.Create(o), NullLoggerFactory.Instance)));
        Assert.Null(MetricRegistry.Shared.Get("off_metric", MetricSeries.Counter, null));
        Visionary.Counter("off_metric").Inc();
    }

    [Fact]
    public void OptionsFromEnvironment()
    {
        var config = new ConfigurationBuilder().AddInMemoryCollection(new Dictionary<string, string?>
        {
            ["VISIONARY_METRICS"] = "false",
            ["VISIONARY_METRICS_INTERVAL"] = "5",
            ["VISIONARY_METRICS_MAX_SERIES"] = "50",
            ["VISIONARY_METRICS_METERS"] = "Shop, Billing",
        }).Build();
        var o = new VisionaryOptions();
        o.Load(config);
        Assert.False(o.Metrics);
        Assert.Equal(TimeSpan.FromSeconds(5), o.MetricsInterval);
        Assert.Equal(50, o.MetricsMaxSeries);
        Assert.Equal(new[] { "Shop", "Billing" }, o.MetricsMeters);
    }

    [Fact]
    public void MeterInstrumentsBecomeMetrics()
    {
        using var meter = new Meter("Visionary.Tests.Bridge");
        using var bridge = new MeterBridge(new[] { meter.Name });
        meter.CreateCounter<long>("b_jobs", "jobs").Add(3, new KeyValuePair<string, object?>("queue", "mail"));
        meter.CreateUpDownCounter<int>("b_open").Add(2);
        meter.CreateHistogram<double>("b_took").Record(12.5);
        var level = 9;
        meter.CreateObservableGauge("b_level", () => level);
        bridge.Observe();

        var rows = MetricRegistry.Shared.Collect(DateTime.UtcNow).Rows.Where(r => r.Name.StartsWith("b_", StringComparison.Ordinal)).ToList();
        var jobs = Assert.Single(rows, r => r.Name == "b_jobs");
        Assert.Equal(("counter", 3.0, "jobs", "{\"queue\":\"mail\"}"), (jobs.Kind, jobs.Value, jobs.Unit, jobs.Labels));
        Assert.Equal(("gauge", 2.0), Assert.Single(rows, r => r.Name == "b_open") is var open ? (open.Kind, open.Value) : default);
        Assert.Equal(1, Assert.Single(rows, r => r.Name == "b_took").Count);
        Assert.Equal(9, Assert.Single(rows, r => r.Name == "b_level").Value);
    }

    private List<string?[]> Query(string sql)
    {
        using var conn = new SqliteConnection("Data Source=" + _db);
        conn.Open();
        using var cmd = conn.CreateCommand();
        cmd.CommandText = sql;
        using var reader = cmd.ExecuteReader();
        var rows = new List<string?[]>();
        while (reader.Read())
        {
            var row = new string?[reader.FieldCount];
            for (var i = 0; i < row.Length; i++)
            {
                row[i] = reader.IsDBNull(i) ? null : Convert.ToString(reader.GetValue(i), System.Globalization.CultureInfo.InvariantCulture);
            }
            rows.Add(row);
        }
        return rows;
    }
}
