package io.github.styropyr0.visionary;

import java.math.BigDecimal;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.HashMap;
import java.util.HexFormat;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.TreeMap;
import java.util.concurrent.ConcurrentHashMap;

/**
 * Custom metrics: counters, gauges and histograms the application defines.
 * Values add up in memory and the started agent writes one {@code metrics} row
 * per series every interval ({@link MetricsWriter}). Recording never waits for
 * the database. Each distinct label set is one series; past the cap per metric
 * name new ones are dropped and counted, so a label holding user ids cannot fill
 * the database.
 */
final class Metrics {

    static final String COUNTER = "counter";
    static final String GAUGE = "gauge";
    static final String HISTOGRAM = "histogram";

    static final int MAX_NAME = 200;
    static final int MAX_LABELS = 20;
    static final int MAX_LABEL_KEY = 100;
    static final int MAX_LABEL_VALUE = 200;
    static final int DEFAULT_MAX_SERIES = 1000;

    /** OpenTelemetry's defaults, which suit milliseconds. */
    static final double[] DEFAULT_BUCKETS = {0, 5, 10, 25, 50, 75, 100, 250, 500, 750, 1000, 2500, 5000, 7500, 10000};

    static final Metrics REGISTRY = new Metrics();

    private final Map<String, Series> series = new HashMap<>();
    private final Map<String, Integer> perName = new HashMap<>();
    private Map<String, Long> dropped = new HashMap<>();
    private final Map<String, Options> options = new ConcurrentHashMap<>();
    volatile int maxSeries = DEFAULT_MAX_SERIES;
    /** False with VISIONARY_METRICS=false: every call records nothing. */
    volatile boolean enabled = true;

    private record Options(String unit, double[] buckets) {
    }

    /** One series: a name, a kind and a label set. Guarded by itself. */
    static final class Series {
        final String name;
        final String kind;
        final String unit;
        final String labels;
        final String id;
        final double[] bounds;

        boolean touched;
        double total;
        double value;
        long[] counts;
        long count;
        double sum;
        double min;
        double max;

        Series(String name, String kind, String unit, String labels, String id, double[] bounds) {
            this.name = name;
            this.kind = kind;
            this.unit = unit;
            this.labels = labels;
            this.id = id;
            this.bounds = bounds;
            this.counts = bounds == null ? null : new long[bounds.length + 1];
        }

        synchronized void add(double n) {
            if (!(n > 0) || Double.isInfinite(n)) {
                return;
            }
            total += n;
            touched = true;
        }

        synchronized void set(double v) {
            if (!Double.isFinite(v)) {
                return;
            }
            value = v;
            touched = true;
        }

        synchronized void move(double delta) {
            if (!Double.isFinite(delta)) {
                return;
            }
            value += delta;
            touched = true;
        }

        synchronized void observe(double v) {
            if (!Double.isFinite(v)) {
                return;
            }
            int i = Arrays.binarySearch(bounds, v);
            counts[i >= 0 ? i : -i - 1]++;
            if (count == 0 || v < min) {
                min = v;
            }
            if (count == 0 || v > max) {
                max = v;
            }
            count++;
            sum += v;
        }
    }

    /** One {@code metrics} row. */
    record Row(Instant at, String name, String kind, String unit, String labels, String series, double value,
            Long count, Double sum, Double min, Double max, String buckets) {
    }

    void describe(String name, String unit, double... buckets) {
        name = cleanName(name);
        if (name.isEmpty()) {
            return;
        }
        double[] b = null;
        if (buckets != null && buckets.length > 0) {
            b = Arrays.stream(buckets).filter(Double::isFinite).sorted().distinct().toArray();
        }
        options.put(name, new Options(unit == null || unit.isBlank() ? null : unit, b));
    }

    /** The series, or null when the name is empty or the name is over its series cap. */
    synchronized Series get(String name, String kind, Map<String, String> labels) {
        name = cleanName(name);
        if (!enabled || name.isEmpty()) {
            return null;
        }
        String key = key(name, kind, labels);
        Series s = series.get(key);
        if (s != null) {
            return s;
        }
        if (perName.getOrDefault(name, 0) >= maxSeries) {
            dropped.merge(name, 1L, Long::sum);
            return null;
        }
        Options o = options.get(name);
        double[] bounds = null;
        if (HISTOGRAM.equals(kind)) {
            bounds = o != null && o.buckets() != null && o.buckets().length > 0 ? o.buckets() : DEFAULT_BUCKETS;
        }
        String[] text = series(name, labels);
        s = new Series(name, kind, o == null ? null : o.unit(), text[0], text[1], bounds);
        series.put(key, s);
        perName.merge(name, 1, Integer::sum);
        return s;
    }

    /** Identifies a series in the registry without building its JSON. */
    private static String key(String name, String kind, Map<String, String> labels) {
        StringBuilder b = new StringBuilder(name).append('\u0000').append(kind);
        if (labels != null && !labels.isEmpty()) {
            TreeMap<String, String> sorted = new TreeMap<>();
            labels.forEach((k, v) -> sorted.put(k == null ? "" : k, v == null ? "" : v));
            sorted.forEach((k, v) -> b.append('\u0000').append(k).append('\u0001').append(v));
        }
        return b.toString();
    }

    /** Dropped series counts by name since the last call. */
    synchronized Map<String, Long> takeDropped() {
        Map<String, Long> out = dropped;
        if (!out.isEmpty()) {
            dropped = new HashMap<>();
        }
        return out;
    }

    /** Takes each series' interval: counters and histograms that moved, and every gauge ever set. */
    List<Row> collect(Instant now) {
        List<Series> all;
        synchronized (this) {
            all = new ArrayList<>(series.values());
        }
        List<Row> out = new ArrayList<>();
        for (Series s : all) {
            synchronized (s) {
                switch (s.kind) {
                    case COUNTER -> {
                        if (s.total > 0) {
                            out.add(new Row(now, s.name, s.kind, s.unit, s.labels, s.id, s.total,
                                    null, null, null, null, null));
                            s.total = 0;
                        }
                    }
                    case GAUGE -> {
                        if (s.touched) {
                            out.add(new Row(now, s.name, s.kind, s.unit, s.labels, s.id, s.value,
                                    null, null, null, null, null));
                        }
                    }
                    default -> {
                        if (s.count > 0) {
                            out.add(new Row(now, s.name, s.kind, s.unit, s.labels, s.id, s.sum / s.count,
                                    s.count, s.sum, s.min, s.max, buckets(s.bounds, s.counts)));
                            s.count = 0;
                            s.sum = 0;
                            s.min = 0;
                            s.max = 0;
                            Arrays.fill(s.counts, 0);
                        }
                    }
                }
            }
        }
        out.sort((a, b) -> a.series().compareTo(b.series()));
        return out;
    }

    /** For tests: forgets every series and option. */
    synchronized void reset() {
        series.clear();
        perName.clear();
        dropped = new HashMap<>();
        options.clear();
        maxSeries = DEFAULT_MAX_SERIES;
        enabled = true;
    }

    static String buckets(double[] bounds, long[] counts) {
        StringBuilder b = new StringBuilder("{\"bounds\":[");
        for (int i = 0; i < bounds.length; i++) {
            if (i > 0) {
                b.append(',');
            }
            b.append(number(bounds[i]));
        }
        b.append("],\"counts\":[");
        for (int i = 0; i < counts.length; i++) {
            if (i > 0) {
                b.append(',');
            }
            b.append(counts[i]);
        }
        return b.append("]}").toString();
    }

    /** A JSON number as Go writes a float64: no ".0" on whole numbers. */
    static String number(double v) {
        if (v == Math.rint(v) && Math.abs(v) < 1e21) {
            return Long.toString((long) v);
        }
        double a = Math.abs(v);
        if (a >= 1e-6 && a < 1e21) {
            return new BigDecimal(Double.toString(v)).toPlainString();
        }
        return Double.toString(v).replace("E", "e");
    }

    /**
     * The canonical labels text of a series and its id, as the SDK contract
     * defines them: a JSON object with keys in UTF-8 byte order, and the first 16
     * hex digits of SHA-256 over the name, a newline and that text.
     */
    static String[] series(String name, Map<String, String> labels) {
        TreeMap<byte[], String[]> sorted = new TreeMap<>(Arrays::compareUnsigned);
        if (labels != null) {
            for (Map.Entry<String, String> e : labels.entrySet()) {
                String k = cut(e.getKey() == null ? "" : e.getKey().strip(), MAX_LABEL_KEY);
                if (k.isEmpty()) {
                    continue;
                }
                sorted.put(k.getBytes(StandardCharsets.UTF_8),
                        new String[] {k, cut(e.getValue() == null ? "" : e.getValue(), MAX_LABEL_VALUE)});
            }
        }
        StringBuilder b = new StringBuilder("{");
        int n = 0;
        for (String[] kv : sorted.values()) {
            if (n == MAX_LABELS) {
                break;
            }
            if (n++ > 0) {
                b.append(',');
            }
            quote(b, kv[0]);
            b.append(':');
            quote(b, kv[1]);
        }
        String text = b.append('}').toString();
        try {
            byte[] sum = MessageDigest.getInstance("SHA-256")
                    .digest((name + "\n" + text).getBytes(StandardCharsets.UTF_8));
            return new String[] {text, HexFormat.of().formatHex(sum, 0, 8)};
        } catch (NoSuchAlgorithmException e) {
            throw new IllegalStateException(e);
        }
    }

    /** Escapes only what JSON requires, so every SDK produces the same bytes. */
    private static void quote(StringBuilder b, String s) {
        b.append('"');
        for (int i = 0; i < s.length(); i++) {
            char c = s.charAt(i);
            if (c == '"' || c == '\\') {
                b.append('\\').append(c);
            } else if (c < 0x20) {
                b.append("\\u00").append(Character.forDigit(c >> 4, 16)).append(Character.forDigit(c & 0xf, 16));
            } else {
                b.append(c);
            }
        }
        b.append('"');
    }

    static String cleanName(String name) {
        return name == null ? "" : cut(name.strip(), MAX_NAME);
    }

    /** At most {@code max} bytes of UTF-8, cut on a character boundary; lone surrogates become U+FFFD. */
    static String cut(String s, int max) {
        StringBuilder valid = null;
        for (int i = 0; i < s.length(); i++) {
            char c = s.charAt(i);
            boolean lone = Character.isHighSurrogate(c)
                    ? i + 1 >= s.length() || !Character.isLowSurrogate(s.charAt(i + 1))
                    : Character.isLowSurrogate(c) && (i == 0 || !Character.isHighSurrogate(s.charAt(i - 1)));
            if (lone && valid == null) {
                valid = new StringBuilder(s.substring(0, i));
            }
            if (valid != null) {
                valid.append(lone ? '�' : c);
            }
        }
        if (valid != null) {
            s = valid.toString();
        }
        byte[] b = s.getBytes(StandardCharsets.UTF_8);
        if (b.length <= max) {
            return s;
        }
        int n = max;
        while (n > 0 && (b[n] & 0xC0) == 0x80) {
            n--;
        }
        return new String(b, 0, n, StandardCharsets.UTF_8);
    }

    /** Key/value pairs; a key without a value gets "". */
    static Map<String, String> pairs(String... kv) {
        if (kv == null || kv.length == 0) {
            return Map.of();
        }
        Map<String, String> m = new LinkedHashMap<>();
        for (int i = 0; i < kv.length; i += 2) {
            m.put(kv[i], i + 1 < kv.length ? kv[i + 1] : "");
        }
        return m;
    }
}
