package io.github.styropyr0.visionary;

import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.Set;
import java.util.TreeSet;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

import com.fasterxml.jackson.core.JsonProcessingException;
import com.fasterxml.jackson.databind.ObjectMapper;

/** Scrubs credentials from headers and bodies before anything is stored. */
final class Redactor {

    static final String REDACTED = "[REDACTED]";

    static final List<String> DEFAULT_HEADERS = List.of(
            "Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie", "X-Api-Key", "X-Auth-Token");
    static final List<String> DEFAULT_FIELDS = List.of(
            "password", "passwd", "secret", "client_secret", "token", "access_token", "refresh_token", "id_token",
            "api_key", "apikey", "otp", "pin", "card_number", "cvv", "private_key");

    private static final String JSON_REPLACEMENT = "$1\"" + Matcher.quoteReplacement(REDACTED) + "\"";
    private static final String FORM_REPLACEMENT = "$1$2=" + Matcher.quoteReplacement(REDACTED);

    private final Set<String> headers = new TreeSet<>(String.CASE_INSENSITIVE_ORDER);
    private final Pattern responseJson;
    private final Pattern requestJson;
    private final Pattern form;
    private final ObjectMapper json;

    Redactor(List<String> extraHeaders, List<String> extraFields, List<String> requestOnly, ObjectMapper json) {
        this.json = json;
        for (String h : concat(DEFAULT_HEADERS, extraHeaders)) {
            headers.add(h.trim());
        }
        String both = keyPattern(concat(DEFAULT_FIELDS, extraFields));
        String reqOnly = keyPattern(requestOnly);
        String reqKeys = reqOnly.isEmpty() ? both : both + "|" + reqOnly;
        this.responseJson = jsonSecrets(both);
        this.requestJson = jsonSecrets(reqKeys);
        this.form = Pattern.compile("(?i)(^|&)(" + reqKeys + ")=[^&]*");
    }

    /** JSON object of the headers with sensitive values replaced; null when there are none. */
    String headers(Map<String, String> raw) {
        if (raw == null || raw.isEmpty()) {
            return null;
        }
        Map<String, String> out = new LinkedHashMap<>();
        raw.forEach((k, v) -> out.put(k, headers.contains(k) ? REDACTED : v));
        try {
            return json.writeValueAsString(out);
        } catch (JsonProcessingException e) {
            return null;
        }
    }

    String requestBody(String body, String contentType) {
        if (body == null || body.isEmpty()) {
            return null;
        }
        if (contentType != null
                && contentType.toLowerCase(Locale.ROOT).contains("application/x-www-form-urlencoded")) {
            return form.matcher(body).replaceAll(FORM_REPLACEMENT);
        }
        return requestJson.matcher(body).replaceAll(JSON_REPLACEMENT);
    }

    String responseBody(String body) {
        if (body == null || body.isEmpty()) {
            return null;
        }
        return responseJson.matcher(body).replaceAll(JSON_REPLACEMENT);
    }

    /**
     * Matches {@code "key": value} pairs. Regex-based on purpose, so bodies cut
     * off at the capture cap (invalid JSON) are still scrubbed. The possessive
     * quantifiers match the same text as SPEC.md's pattern but stop Java's regex
     * engine from recursing once per character on long string values.
     */
    private static Pattern jsonSecrets(String keys) {
        return Pattern.compile(
                "(?i)(\"(?:" + keys + ")\"\\s*+:\\s*+)(\"(?:[^\"\\\\]++|\\\\.)*+\"?|[^,}\\]\\s]++)");
    }

    private static String keyPattern(List<String> names) {
        List<String> quoted = new ArrayList<>();
        for (String n : names) {
            if (!n.isBlank()) {
                quoted.add(Pattern.quote(n.trim()));
            }
        }
        return String.join("|", quoted);
    }

    private static List<String> concat(List<String> a, List<String> b) {
        List<String> out = new ArrayList<>(a);
        out.addAll(b);
        return out;
    }
}
