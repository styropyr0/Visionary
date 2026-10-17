package io.github.styropyr0.visionary;

import java.net.InetAddress;
import java.time.Duration;
import java.util.ArrayList;
import java.util.List;
import java.util.Locale;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

import org.springframework.core.env.Environment;

/**
 * Settings read from the environment variables in SPEC.md. They are looked up
 * through Spring's {@link Environment}, so system properties and application
 * properties with the same names work too.
 */
record VisionaryConfig(
        String databaseUrl,
        String d1AccountId,
        String d1DatabaseId,
        String d1ApiToken,
        String d1ApiBase,
        boolean useAppDataSource,
        String instance,
        boolean logRequests,
        String pathPrefix,
        boolean logBodies,
        int maxBodyBytes,
        int logRetentionDays,
        List<String> redactHeaders,
        List<String> redactFields,
        List<String> redactRequestFields,
        String errorCodeField,
        String errorKeyField,
        boolean systemMetrics,
        Duration systemInterval,
        int systemRetentionDays,
        String service,
        String release,
        boolean spans,
        double spanThresholdMs,
        int spanMax,
        boolean errors) {

    static final String DEFAULT_D1_API_BASE = "https://api.cloudflare.com/client/v4";

    static VisionaryConfig from(Environment env) {
        Reader r = new Reader(env);
        return new VisionaryConfig(
                r.string("VISIONARY_DATABASE_URL", ""),
                r.string("D1_ACCOUNT_ID", ""),
                r.string("D1_DATABASE_ID", ""),
                r.string("D1_API_TOKEN", ""),
                r.string("D1_API_BASE", DEFAULT_D1_API_BASE),
                r.bool("VISIONARY_USE_APP_DATASOURCE", false),
                r.string("VISIONARY_INSTANCE", ""),
                r.bool("VISIONARY_LOG_REQUESTS", true),
                r.string("VISIONARY_LOG_PATH_PREFIX", ""),
                r.bool("VISIONARY_LOG_BODIES", true),
                r.positiveInt("VISIONARY_LOG_MAX_BODY_BYTES", 8192),
                r.nonNegativeInt("VISIONARY_LOG_RETENTION_DAYS", 30),
                r.list("VISIONARY_LOG_REDACT_HEADERS"),
                r.list("VISIONARY_LOG_REDACT_FIELDS"),
                r.list("VISIONARY_LOG_REDACT_REQUEST_FIELDS"),
                r.string("VISIONARY_LOG_ERROR_CODE_FIELD", "error.code"),
                r.string("VISIONARY_LOG_ERROR_KEY_FIELD", "error.key"),
                r.bool("VISIONARY_SYSTEM_METRICS", true),
                r.duration("VISIONARY_SYSTEM_INTERVAL", Duration.ofSeconds(15)),
                r.nonNegativeInt("VISIONARY_SYSTEM_RETENTION_DAYS", 30),
                r.string("VISIONARY_SERVICE", ""),
                release(r),
                r.bool("VISIONARY_SPANS", true),
                r.nonNegativeDouble("VISIONARY_SPAN_THRESHOLD_MS", 100),
                r.nonNegativeInt("VISIONARY_SPAN_MAX", 100),
                r.bool("VISIONARY_ERRORS", true));
    }

    /** Platform variables that carry the deployed commit, in the order SPEC.md gives. */
    private static final List<String> COMMIT_VARIABLES = List.of(
            "RAILWAY_GIT_COMMIT_SHA", "RENDER_GIT_COMMIT", "VERCEL_GIT_COMMIT_SHA", "HEROKU_SLUG_COMMIT",
            "SOURCE_VERSION", "GIT_COMMIT", "GITHUB_SHA", "K_REVISION");
    private static final Pattern COMMIT_HASH = Pattern.compile("[0-9a-fA-F]{13,64}");

    /** {@code VISIONARY_RELEASE}, else the platform's commit; hashes shortened to 12 characters. */
    private static String release(Reader r) {
        String v = r.string("VISIONARY_RELEASE", "");
        for (int i = 0; v.isEmpty() && i < COMMIT_VARIABLES.size(); i++) {
            v = r.string(COMMIT_VARIABLES.get(i), "");
        }
        return COMMIT_HASH.matcher(v).matches() ? v.substring(0, 12) : v;
    }

    boolean hasD1Credentials() {
        return !d1AccountId.isEmpty() && !d1DatabaseId.isEmpty() && !d1ApiToken.isEmpty();
    }

    /** Whether anything points at a database; without one, collection is off. */
    boolean databaseConfigured() {
        return !databaseUrl.isEmpty() || hasD1Credentials() || useAppDataSource;
    }

    boolean systemEnabled() {
        return systemMetrics && !systemInterval.isZero() && !systemInterval.isNegative();
    }

    String resolvedInstance() {
        if (!instance.isEmpty()) {
            return instance;
        }
        try {
            return InetAddress.getLocalHost().getHostName();
        } catch (Exception e) {
            String name = System.getenv("COMPUTERNAME");
            if (name == null || name.isEmpty()) {
                name = System.getenv("HOSTNAME");
            }
            return name == null || name.isEmpty() ? "unknown" : name;
        }
    }

    private record Reader(Environment env) {

        private static final Pattern DURATION_PART = Pattern.compile("(\\d+(?:\\.\\d+)?)(ms|s|m|h)");

        String string(String key, String def) {
            String v = env.getProperty(key);
            return v == null || v.isBlank() ? def : v.trim();
        }

        boolean bool(String key, boolean def) {
            return switch (string(key, "").toLowerCase(Locale.ROOT)) {
                case "true", "1", "yes" -> true;
                case "false", "0", "no" -> false;
                default -> def;
            };
        }

        int nonNegativeInt(String key, int def) {
            try {
                int v = Integer.parseInt(string(key, ""));
                return v >= 0 ? v : def;
            } catch (NumberFormatException e) {
                return def;
            }
        }

        double nonNegativeDouble(String key, double def) {
            try {
                double v = Double.parseDouble(string(key, ""));
                return v >= 0 && !Double.isInfinite(v) ? v : def;
            } catch (NumberFormatException e) {
                return def;
            }
        }

        int positiveInt(String key, int def) {
            int v = nonNegativeInt(key, def);
            return v > 0 ? v : def;
        }

        List<String> list(String key) {
            List<String> out = new ArrayList<>();
            for (String s : string(key, "").split(",")) {
                if (!s.isBlank()) {
                    out.add(s.trim());
                }
            }
            return List.copyOf(out);
        }

        /** A plain number is seconds; otherwise Go-style units such as 500ms, 15s, 1m30s. */
        Duration duration(String key, Duration def) {
            String v = string(key, "").toLowerCase(Locale.ROOT);
            if (v.isEmpty()) {
                return def;
            }
            if (v.matches("\\d+")) {
                return Duration.ofSeconds(Long.parseLong(v));
            }
            Matcher m = DURATION_PART.matcher(v);
            double millis = 0;
            int end = 0;
            while (m.find() && m.start() == end) {
                double n = Double.parseDouble(m.group(1));
                millis += switch (m.group(2)) {
                    case "ms" -> n;
                    case "s" -> n * 1_000;
                    case "m" -> n * 60_000;
                    default -> n * 3_600_000;
                };
                end = m.end();
            }
            return end == v.length() ? Duration.ofMillis(Math.round(millis)) : def;
        }
    }
}
