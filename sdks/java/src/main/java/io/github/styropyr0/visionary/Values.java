package io.github.styropyr0.visionary;

import java.time.Instant;
import java.time.ZoneOffset;
import java.time.format.DateTimeFormatter;

/** Formatting shared by the writers. */
final class Values {

    /** {@code created_at} everywhere: fixed width so string order is time order. */
    static final DateTimeFormatter TIMESTAMP =
            DateTimeFormatter.ofPattern("yyyy-MM-dd'T'HH:mm:ss.SSS'Z'").withZone(ZoneOffset.UTC);

    private Values() {
    }

    static String timestamp(Instant t) {
        return TIMESTAMP.format(t);
    }

    static Double round(double v, int decimals) {
        if (Double.isNaN(v) || Double.isInfinite(v)) {
            return null;
        }
        double scale = Math.pow(10, decimals);
        return Math.round(v * scale) / scale;
    }

    static String emptyToNull(String s) {
        return s == null || s.isEmpty() ? null : s;
    }
}
