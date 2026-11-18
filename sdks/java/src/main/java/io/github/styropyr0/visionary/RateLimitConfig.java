package io.github.styropyr0.visionary;

import java.util.ArrayList;
import java.util.Collections;
import java.util.Iterator;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;

import com.fasterxml.jackson.core.JsonProcessingException;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;

/**
 * The {@code ratelimit.config} document (SPEC.md, Rate limits), read leniently
 * like the Go reference: a rule with a field of the wrong type is dropped, the
 * rest are kept. Zero values mean the field is missing.
 */
record RateLimitConfig(Boolean enabled, String ipHeader, List<String> countryHeaders, List<Rule> rules) {

    static final RateLimitConfig EMPTY = new RateLimitConfig(null, "", List.of(), List.of());

    record Rule(String id, String name, Boolean enabled, String mode, String action,
            List<String> services, List<String> methods, List<String> paths, List<String> ips,
            List<String> countries, Map<String, List<String>> match, String key, double limit, double windowS,
            String algorithm, double burst, String limitBy, Map<String, Double> limits, int status, String body,
            String contentType) {

        boolean on() {
            return enabled == null || enabled;
        }
    }

    boolean on() {
        return enabled == null || enabled;
    }

    /** Only text that is not JSON throws. */
    static RateLimitConfig parse(String raw, ObjectMapper json) throws JsonProcessingException {
        JsonNode doc = json.readTree(raw);
        if (doc == null || !doc.isObject()) {
            return EMPTY;
        }
        Map<String, JsonNode> f = fields(doc);
        Boolean enabled = f.get("enabled") != null && f.get("enabled").isBoolean() ? f.get("enabled").booleanValue() : null;
        String ipHeader = f.get("ip_header") != null && f.get("ip_header").isTextual() ? f.get("ip_header").textValue() : "";
        List<String> countryHeaders = new ArrayList<>();
        JsonNode ch = f.get("country_headers");
        if (ch != null && ch.isArray()) {
            for (JsonNode n : ch) {
                if (n.isTextual()) {
                    countryHeaders.add(n.textValue());
                }
            }
        }
        List<Rule> rules = new ArrayList<>();
        JsonNode rs = f.get("rules");
        if (rs != null && rs.isArray()) {
            for (JsonNode n : rs) {
                try {
                    rules.add(rule(n));
                } catch (IllegalArgumentException e) {
                    // a field of the wrong type: the rule is dropped
                }
            }
        }
        return new RateLimitConfig(enabled, ipHeader, countryHeaders, rules);
    }

    /** Field names match case-insensitively and the last duplicate wins, as in Go's encoding/json. */
    private static Map<String, JsonNode> fields(JsonNode obj) {
        Map<String, JsonNode> out = new LinkedHashMap<>();
        for (Iterator<String> it = obj.fieldNames(); it.hasNext();) {
            String name = it.next();
            JsonNode value = obj.get(name);
            out.put(name.toLowerCase(Locale.ROOT), value);
        }
        return out;
    }

    private static Rule rule(JsonNode n) {
        if (n.isNull()) {
            return new Rule("", "", null, "", "", List.of(), List.of(), List.of(), List.of(), List.of(), Map.of(),
                    "", 0, 0, "", 0, "", Map.of(), 0, "", "");
        }
        if (!n.isObject()) {
            throw new IllegalArgumentException("not an object");
        }
        Map<String, JsonNode> f = fields(n);
        return new Rule(text(f, "id"), text(f, "name"), bool(f, "enabled"), text(f, "mode"), text(f, "action"),
                list(f.get("services")), list(f.get("methods")), list(f.get("paths")), list(f.get("ips")),
                list(f.get("countries")), match(f.get("match")), text(f, "key"), number(f.get("limit")),
                number(f.get("window_s")), text(f, "algorithm"), number(f.get("burst")), text(f, "limit_by"),
                limits(f.get("limits")), integer(f.get("status")), text(f, "body"), text(f, "content_type"));
    }

    private static boolean absent(JsonNode n) {
        return n == null || n.isNull();
    }

    private static String text(Map<String, JsonNode> f, String name) {
        return string(f.get(name));
    }

    private static String string(JsonNode n) {
        if (absent(n)) {
            return "";
        }
        if (!n.isTextual()) {
            throw new IllegalArgumentException("not text");
        }
        return n.textValue();
    }

    private static Boolean bool(Map<String, JsonNode> f, String name) {
        JsonNode n = f.get(name);
        if (absent(n)) {
            return null;
        }
        if (!n.isBoolean()) {
            throw new IllegalArgumentException("not a boolean");
        }
        return n.booleanValue();
    }

    private static double number(JsonNode n) {
        if (absent(n)) {
            return 0;
        }
        if (!n.isNumber()) {
            throw new IllegalArgumentException("not a number");
        }
        return n.doubleValue();
    }

    private static int integer(JsonNode n) {
        if (absent(n)) {
            return 0;
        }
        if (!n.isIntegralNumber() || !n.canConvertToInt()) {
            throw new IllegalArgumentException("not a whole number");
        }
        return n.intValue();
    }

    private static List<String> list(JsonNode n) {
        if (absent(n)) {
            return List.of();
        }
        if (!n.isArray()) {
            throw new IllegalArgumentException("not a list");
        }
        List<String> out = new ArrayList<>(n.size());
        for (JsonNode v : n) {
            out.add(string(v));
        }
        return Collections.unmodifiableList(out);
    }

    private static Map<String, List<String>> match(JsonNode n) {
        if (absent(n)) {
            return Map.of();
        }
        if (!n.isObject()) {
            throw new IllegalArgumentException("not an object");
        }
        Map<String, List<String>> out = new LinkedHashMap<>();
        for (Iterator<String> it = n.fieldNames(); it.hasNext();) {
            String name = it.next();
            JsonNode value = n.get(name);
            out.put(name, list(value));
        }
        return Collections.unmodifiableMap(out);
    }

    private static Map<String, Double> limits(JsonNode n) {
        if (absent(n)) {
            return Map.of();
        }
        if (!n.isObject()) {
            throw new IllegalArgumentException("not an object");
        }
        Map<String, Double> out = new LinkedHashMap<>();
        for (Iterator<String> it = n.fieldNames(); it.hasNext();) {
            String name = it.next();
            JsonNode value = n.get(name);
            out.put(name, number(value));
        }
        return Collections.unmodifiableMap(out);
    }
}
