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

    /** At most {@code max} characters, never splitting a surrogate pair; null and "" become null. */
    static String cap(String s, int max) {
        if (s == null || s.isEmpty() || max <= 0) {
            return null;
        }
        if (s.length() <= max) {
            return s;
        }
        int end = Character.isHighSurrogate(s.charAt(max - 1)) ? max - 1 : max;
        return s.substring(0, end);
    }

    /** At most {@code max} bytes of UTF-8, cut on a character boundary. */
    static String capBytes(String s, int max) {
        if (s == null || s.length() * 3L <= max) {
            return s;
        }
        int bytes = 0;
        for (int i = 0; i < s.length(); i++) {
            char c = s.charAt(i);
            int n = c < 0x80 ? 1 : c < 0x800 ? 2 : Character.isSurrogate(c) ? 2 : 3;
            if (bytes + n > max) {
                return cap(s, i);
            }
            bytes += n;
        }
        return s;
    }
}
