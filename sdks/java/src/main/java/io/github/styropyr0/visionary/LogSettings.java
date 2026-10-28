package io.github.styropyr0.visionary;

import java.time.Instant;
import java.util.ArrayList;
import java.util.List;
import java.util.Locale;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;

/**
 * @param buffer whether a failed or slow request keeps its held lines down to {@code bufferLevel}
 * @param rate   lines per minute per process, 0 for no cap
 */
record LogSettings(boolean enabled, int level, boolean buffer, int bufferLevel, double slowMs, int rate,
        List<LevelOverride> overrides, List<Drop> drops) {

    /** Empty fields match anything; a null {@code until} never expires. */
    record LevelOverride(String service, String route, String user, int level, Instant until) {

        boolean active(String svc, Instant now) {
            return (until == null || !now.isAfter(until)) && (service.isEmpty() || service.equals(svc));
        }

        boolean matches(String r, String u) {
            return (route.isEmpty() || routeMatches(route, r)) && (user.isEmpty() || user.equals(u));
        }
    }

    /** Empty fields match anything; a non-zero {@code level} drops only lines at or below it. */
    record Drop(String service, String logger, String contains, int level) {
    }

    static LogSettings defaults(int level) {
        return new LogSettings(true, level > 0 ? level : LogRules.INFO, true, LogRules.DEBUG, 1000, 6000,
                List.of(), List.of());
    }

    static LogSettings parse(List<String[]> rows, int base, ObjectMapper json) {
        LogSettings d = defaults(base);
        boolean enabled = d.enabled;
        int level = d.level;
        boolean buffer = d.buffer;
        int bufferLevel = d.bufferLevel;
        double slowMs = d.slowMs;
        int rate = d.rate;
        List<LevelOverride> overrides = new ArrayList<>();
        List<Drop> drops = new ArrayList<>();
        for (String[] row : rows) {
            if (row.length < 2 || row[0] == null || row[1] == null) {
                continue;
            }
            String v = row[1].trim();
            switch (row[0]) {
                case "logs.enabled" -> enabled = bool(v, enabled);
                case "logs.level" -> level = LogRules.parseLevel(v) > 0 ? LogRules.parseLevel(v) : level;
                case "logs.buffer" -> buffer = bool(v, buffer);
                case "logs.buffer_level" ->
                        bufferLevel = LogRules.parseLevel(v) > 0 ? LogRules.parseLevel(v) : bufferLevel;
                case "logs.slow_ms" -> {
                    try {
                        double n = Double.parseDouble(v);
                        if (n > 0 && !Double.isInfinite(n)) {
                            slowMs = n;
                        }
                    } catch (NumberFormatException ignored) {
                        // keep the default
                    }
                }
                case "logs.rate" -> {
                    try {
                        int n = Integer.parseInt(v);
                        if (n >= 0) {
                            rate = n;
                        }
                    } catch (NumberFormatException ignored) {
                        // keep the default
                    }
                }
                case "logs.overrides" -> {
                    for (JsonNode o : array(v, json)) {
                        int sev = LogRules.parseLevel(text(o, "level"));
                        if (sev == 0) {
                            continue;
                        }
                        Instant until = null;
                        String u = text(o, "until");
                        if (!u.isEmpty()) {
                            until = ProfileSettings.parseTime(u);
                            if (until == null) {
                                continue;
                            }
                        }
                        overrides.add(new LevelOverride(text(o, "service"), text(o, "route"), text(o, "user"), sev,
                                until));
                    }
                }
                case "logs.drop" -> {
                    for (JsonNode o : array(v, json)) {
                        Drop drop = new Drop(text(o, "service"), text(o, "logger"), text(o, "contains"),
                                LogRules.parseLevel(text(o, "level")));
                        if (!drop.service.isEmpty() || !drop.logger.isEmpty() || !drop.contains.isEmpty()
                                || drop.level != 0) {
                            drops.add(drop);
                        }
                    }
                }
                default -> {
                    // not ours
                }
            }
        }
        return new LogSettings(enabled, level, buffer, bufferLevel, slowMs, rate, List.copyOf(overrides),
                List.copyOf(drops));
    }

    /** A user override beats a route one, which beats a service-wide one; later ones win ties. */
    int floor(String service, String route, String user, Instant now) {
        int out = level;
        int rank = 0;
        for (LevelOverride o : overrides) {
            if (!o.active(service, now) || !o.matches(route, user)) {
                continue;
            }
            int r = !o.user.isEmpty() ? 3 : !o.route.isEmpty() ? 2 : 1;
            if (r >= rank) {
                out = o.level;
                rank = r;
            }
        }
        return out;
    }

    /** Lines below this are held until the request ends, since a route or user override may still keep them. */
    int holdBelow(String service, Instant now) {
        int out = level;
        for (LevelOverride o : overrides) {
            if (o.active(service, now) && o.route.isEmpty() && o.user.isEmpty()) {
                out = o.level;
            }
        }
        for (LevelOverride o : overrides) {
            if (o.active(service, now) && (!o.route.isEmpty() || !o.user.isEmpty())) {
                out = Math.max(out, o.level);
            }
        }
        return out;
    }

    int lowest() {
        int out = buffer ? Math.min(level, bufferLevel) : level;
        for (LevelOverride o : overrides) {
            out = Math.min(out, o.level);
        }
        return out;
    }

    boolean dropped(String service, String logger, String message, int sev) {
        for (Drop d : drops) {
            if ((!d.service.isEmpty() && !d.service.equals(service))
                    || (!d.logger.isEmpty() && !d.logger.equals(logger))
                    || (d.level != 0 && sev > d.level)
                    || (!d.contains.isEmpty() && (message == null || !message.contains(d.contains)))) {
                continue;
            }
            return true;
        }
        return false;
    }

    static boolean routeMatches(String pattern, String route) {
        String r = route == null ? "" : route;
        if (pattern.endsWith("*")) {
            return r.startsWith(pattern.substring(0, pattern.length() - 1));
        }
        return pattern.equals(r);
    }

    private static boolean bool(String v, boolean def) {
        return switch (v.toLowerCase(Locale.ROOT)) {
            case "true", "t", "1", "yes" -> true;
            case "false", "f", "0", "no" -> false;
            default -> def;
        };
    }

    private static Iterable<JsonNode> array(String v, ObjectMapper json) {
        try {
            JsonNode node = json.readTree(v);
            if (node != null && node.isArray()) {
                List<JsonNode> out = new ArrayList<>();
                node.forEach(n -> {
                    if (n.isObject()) {
                        out.add(n);
                    }
                });
                return out;
            }
        } catch (Exception ignored) {
            // unreadable: none
        }
        return List.of();
    }

    private static String text(JsonNode o, String field) {
        JsonNode n = o.get(field);
        return n == null || n.isNull() ? "" : n.asText().trim();
    }
}
