package io.github.styropyr0.visionary;

import java.time.Duration;
import java.time.Instant;
import java.time.LocalDateTime;
import java.time.ZoneOffset;
import java.time.format.DateTimeParseException;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;

/**
 * The {@code profile.*} rows of {@code visionary_settings}, as the dashboard's
 * Settings → Profiling page writes them (SPEC.md, "CPU profiles").
 *
 * @param services null when every service takes part
 * @param now      the last "Profile now" timestamp, or null
 */
record ProfileSettings(boolean enabled, List<String> services, int intervalS, int durationMs, String now) {

    static final int DEFAULT_INTERVAL_S = 300;
    static final int DEFAULT_DURATION_MS = 10_000;
    /** A "Profile now" request older than this is ignored. */
    static final Duration NOW_FRESHNESS = Duration.ofMinutes(5);
    private static final int MAX_DURATION_MS = 60_000;
    private static final int MIN_DURATION_MS = 100;

    static final ProfileSettings OFF = new ProfileSettings(false, null, DEFAULT_INTERVAL_S, DEFAULT_DURATION_MS, null);

    static ProfileSettings parse(Map<String, String> values, ObjectMapper json) {
        boolean enabled = "true".equalsIgnoreCase(trim(values.get("profile.enabled")));
        int interval = positive(values.get("profile.interval_s"), DEFAULT_INTERVAL_S);
        int duration = Math.max(MIN_DURATION_MS,
                Math.min(MAX_DURATION_MS, positive(values.get("profile.duration_ms"), DEFAULT_DURATION_MS)));
        String now = trim(values.get("profile.now"));
        return new ProfileSettings(enabled, services(values.get("profile.services"), json), interval, duration,
                now == null || now.isEmpty() ? null : now);
    }

    /**
     * Whether a service takes part: any name when the list is missing or empty;
     * a service without a name only then.
     */
    boolean includes(String service) {
        if (services == null || services.isEmpty()) {
            return true;
        }
        return service != null && !service.isEmpty() && services.contains(service);
    }

    /**
     * Whether {@code profile.now} asks for a one-off profile: it changed since the
     * last read, and its time is after this process started and under five minutes ago.
     */
    static boolean freshNow(String value, String previous, Instant processStart, Instant now) {
        if (value == null || value.equals(previous)) {
            return false;
        }
        Instant at = parseTime(value);
        return at != null && at.isAfter(processStart) && !at.isAfter(now.plusSeconds(60))
                && Duration.between(at, now).compareTo(NOW_FRESHNESS) < 0;
    }

    /** {@code 2026-10-22T09:30:00.000Z}, also without the zone or with a space for the T. */
    static Instant parseTime(String s) {
        String v = s.trim().replace(' ', 'T');
        try {
            return Instant.parse(v);
        } catch (DateTimeParseException e) {
            try {
                return LocalDateTime.parse(v).toInstant(ZoneOffset.UTC);
            } catch (DateTimeParseException e2) {
                return null;
            }
        }
    }

    private static List<String> services(String raw, ObjectMapper json) {
        String v = trim(raw);
        if (v == null || v.isEmpty()) {
            return null;
        }
        try {
            JsonNode node = json.readTree(v);
            if (!node.isArray()) {
                return null;
            }
            List<String> out = new ArrayList<>();
            for (JsonNode n : node) {
                if (n.isTextual() && !n.asText().isBlank()) {
                    out.add(n.asText().trim());
                }
            }
            return List.copyOf(out);
        } catch (Exception e) {
            return null;
        }
    }

    private static int positive(String raw, int def) {
        String v = trim(raw);
        if (v == null) {
            return def;
        }
        try {
            double d = Double.parseDouble(v);
            return d >= 1 && d < Integer.MAX_VALUE ? (int) Math.round(d) : def;
        } catch (NumberFormatException e) {
            return def;
        }
    }

    private static String trim(String s) {
        return s == null ? null : s.trim();
    }
}
