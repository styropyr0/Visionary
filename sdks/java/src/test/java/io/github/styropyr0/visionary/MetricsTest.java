package io.github.styropyr0.visionary;

import static org.junit.jupiter.api.Assertions.assertArrayEquals;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Test;

class MetricsTest {

    private final Metrics reg = Metrics.REGISTRY;

    @AfterEach
    void reset() {
        reg.reset();
    }

    private Map<String, Metrics.Row> collect() {
        Map<String, Metrics.Row> out = new LinkedHashMap<>();
        for (Metrics.Row r : reg.collect(Instant.now())) {
            out.put(r.kind() + " " + r.name() + " " + r.labels(), r);
        }
        return out;
    }

    @Test
    void seriesTextAndIdMatchTheContract() {
        String[] s = Metrics.series("orders_placed", Map.of("region", "eu"));
        assertEquals("{\"region\":\"eu\"}", s[0]);
        assertEquals("da4f5c6244c4f3e4", s[1]);
        assertEquals("{}", Metrics.series("x", Map.of())[0]);

        Map<String, String> labels = new LinkedHashMap<>();
        labels.put("b", "q\"\\\n<&é");
        labels.put(" ", "dropped");
        labels.put("a", "1");
        assertEquals("{\"a\":\"1\",\"b\":\"q\\\"\\\\\\u000a<&é\"}", Metrics.series("x", labels)[0]);

        // U+FF21 sorts before a supplementary character in UTF-8, after it in UTF-16
        Map<String, String> order = new LinkedHashMap<>();
        order.put("😀", "2");
        order.put("Ａ", "1");
        assertEquals("{\"Ａ\":\"1\",\"😀\":\"2\"}", Metrics.series("x", order)[0]);
    }

    @Test
    void labelsAreCutAndCapped() {
        Map<String, String> many = new LinkedHashMap<>();
        for (int i = 0; i < 25; i++) {
            many.put(String.format("k%02d", i), "v");
        }
        String text = Metrics.series("x", many)[0];
        assertTrue(text.contains("\"k19\"") && !text.contains("\"k20\""), text);
        assertEquals(100, Metrics.cut("é".repeat(80), 101).getBytes(java.nio.charset.StandardCharsets.UTF_8).length);
        assertEquals("ab", Metrics.cleanName("  ab  "));
        assertEquals(Map.of("a", "1", "b", ""), Metrics.pairs("a", "1", "b"));
    }

    @Test
    void countersGaugesAndHistogramsAggregatePerInterval() {
        Visionary.counter("orders", "region", "eu").add(2);
        Visionary.counter("orders", "region", "eu").inc();
        Visionary.counter("orders", "region", "eu").add(-1);
        Visionary.counter("orders", "region", "eu").add(Double.POSITIVE_INFINITY);
        Visionary.gauge("depth").add(4);
        Visionary.gauge("depth").add(-1);
        Visionary.gauge("depth").set(Double.NaN);
        VisionaryHistogram h = Visionary.histogram("took_ms");
        h.observe(3);
        h.observe(5);
        h.observe(20000);
        h.observe(Double.NaN);
        Visionary.counter("", "a", "b").inc();
        Visionary.counter("orders", "region", "eu");

        Map<String, Metrics.Row> rows = collect();
        assertEquals(3, rows.size(), rows.keySet().toString());
        assertEquals(3.0, rows.get("counter orders {\"region\":\"eu\"}").value());
        assertNull(rows.get("counter orders {\"region\":\"eu\"}").count());
        assertEquals(3.0, rows.get("gauge depth {}").value());
        Metrics.Row hist = rows.get("histogram took_ms {}");
        assertEquals(3L, hist.count());
        assertEquals(20008.0, hist.sum());
        assertEquals(3.0, hist.min());
        assertEquals(20000.0, hist.max());
        assertEquals(20008.0 / 3, hist.value(), 1e-9);
        assertEquals("{\"bounds\":[0,5,10,25,50,75,100,250,500,750,1000,2500,5000,7500,10000],"
                + "\"counts\":[0,2,0,0,0,0,0,0,0,0,0,0,0,0,0,1]}", hist.buckets());

        rows = collect();
        assertEquals(List.of("gauge depth {}"), new ArrayList<>(rows.keySet()));
    }

    @Test
    void kindsAreSeparateSeriesAndDescribeSetsUnitAndBuckets() {
        Visionary.describeMetric("lat", "ms", 100, 10, 10, 50);
        Visionary.histogram("lat").observe(10);
        Visionary.histogram("lat").observe(60);
        Visionary.gauge("lat").set(1);
        Map<String, Metrics.Row> rows = collect();
        assertEquals(2, rows.size());
        Metrics.Row h = rows.get("histogram lat {}");
        assertEquals("ms", h.unit());
        assertEquals("{\"bounds\":[10,50,100],\"counts\":[1,0,1,0]}", h.buckets());
        assertEquals("{\"bounds\":[0.5,1.25],\"counts\":[0,0,0]}", Metrics.buckets(new double[] {0.5, 1.25}, new long[3]));
    }

    @Test
    void seriesPastTheCapAreDroppedAndCounted() {
        reg.maxSeries = 2;
        Visionary.counter("users", "id", "1").inc();
        Visionary.counter("users", "id", "2").inc();
        Visionary.counter("users", "id", "3").inc();
        Visionary.counter("users", "id", "4").inc();
        Visionary.counter("users", "id", "1").inc();
        assertEquals(2, collect().size());
        assertEquals(Map.of("users", 2L), reg.takeDropped());
        assertTrue(reg.takeDropped().isEmpty());
    }

    @Test
    void turnedOffEveryCallRecordsNothing() {
        reg.enabled = false;
        Visionary.counter("orders").inc();
        Visionary.gauge("depth").set(1);
        assertTrue(collect().isEmpty());
    }

    @Test
    void theWriterCreatesTheTableOnlyWhenThereIsSomethingToWrite() {
        RecordingStore store = new RecordingStore();
        MetricsWriter w = new MetricsWriter(store, new Origin("api", null, "host-1", "Java 17", null), reg,
                Duration.ofHours(1), 0);
        w.flush();
        assertTrue(store.schemas.isEmpty());

        Visionary.histogram("took_ms", "route", "/x").observe(7);
        Visionary.counter("orders").add(2);
        w.flush();
        assertEquals(List.of(Table.METRICS), store.schemas);
        assertEquals(2, store.rows.size());
        for (Object[] row : store.rows) {
            assertEquals(Table.METRICS.columns.size(), row.length);
            assertEquals("api", row[1]);
            assertEquals("host-1", row[2]);
        }
        Object[] counter = store.rows.stream().filter(r -> "counter".equals(r[4])).findFirst().orElseThrow();
        assertEquals("orders", counter[3]);
        assertEquals("{}", counter[6]);
        assertEquals(2.0, counter[8]);
        assertNull(counter[9]);
        Object[] hist = store.rows.stream().filter(r -> "histogram".equals(r[4])).findFirst().orElseThrow();
        assertEquals(1L, hist[9]);
        assertArrayEquals(new Object[] {7.0, 7.0, 7.0}, new Object[] {hist[10], hist[11], hist[12]});
    }

    @Test
    void configReadsTheMetricsVariables() {
        org.springframework.core.env.StandardEnvironment std = new org.springframework.core.env.StandardEnvironment();
        std.getPropertySources().addFirst(new org.springframework.core.env.MapPropertySource("t", Map.of(
                "VISIONARY_METRICS", "false", "VISIONARY_METRICS_INTERVAL", "30", "VISIONARY_METRICS_MAX_SERIES", "5")));
        VisionaryConfig cfg = VisionaryConfig.from(std);
        assertEquals(false, cfg.metrics());
        assertEquals(Duration.ofSeconds(30), cfg.metricsInterval());
        assertEquals(5, cfg.metricsMaxSeries());
        VisionaryConfig def = VisionaryConfig.from(new org.springframework.core.env.StandardEnvironment());
        assertTrue(def.metrics());
        assertEquals(Duration.ofSeconds(15), def.metricsInterval());
        assertEquals(1000, def.metricsMaxSeries());
    }

    static final class RecordingStore implements Store {
        final List<Table> schemas = new ArrayList<>();
        final List<Object[]> rows = new ArrayList<>();

        @Override
        public void createSchema(Table table) {
            schemas.add(table);
        }

        @Override
        public synchronized void insert(Map<Table, List<Object[]>> rows) {
            rows.values().forEach(this.rows::addAll);
        }

        @Override
        public List<String[]> select(String sql) {
            return List.of();
        }

        @Override
        public long purgeChunk(Table table, String cutoff) {
            return 0;
        }

        @Override
        public String describe() {
            return "recording";
        }

        @Override
        public void close() {
        }
    }
}
