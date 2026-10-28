package io.github.styropyr0.visionary;

import java.nio.charset.StandardCharsets;
import java.time.temporal.TemporalAccessor;
import java.util.ArrayList;
import java.util.Collections;
import java.util.Date;
import java.util.HashSet;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.Set;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

import com.fasterxml.jackson.core.JsonProcessingException;
import com.fasterxml.jackson.databind.ObjectMapper;

/**
 * The rules every source of application logs follows (SPEC.md, "Application
 * logs"; {@code sdks/go/logrules}): level names and numbers, finding a level in
 * plain text, scrubbing secrets and encoding a line's fields.
 */
final class LogRules {

    // Severities as stored in app_logs.severity.
    static final int TRACE = 1;
    static final int DEBUG = 2;
    static final int INFO = 3;
    static final int WARN = 4;
    static final int ERROR = 5;
    static final int FATAL = 6;

    static final int MAX_MESSAGE = 8 << 10;
    static final int MAX_ATTRS = 16 << 10;
    /** A string attribute is cut to this many bytes when the attributes do not fit. */
    private static final int MAX_ATTR_VALUE = 1024;

    private static final String[] NAMES = {"", "trace", "debug", "info", "warn", "error", "fatal"};
    private static final ObjectMapper JSON = new ObjectMapper();

    private LogRules() {
    }

    /** A severity's level name; out-of-range severities are info. */
    static String name(int sev) {
        return sev < TRACE || sev > FATAL ? "info" : NAMES[sev];
    }

    /** Reads a level name the way sources spell them; 0 when unknown. */
    static int parseLevel(String s) {
        if (s == null) {
            return 0;
        }
        return switch (s.trim().toLowerCase(Locale.ROOT)) {
            case "trace", "trc", "finest", "finer", "verbose", "vrb" -> TRACE;
            case "debug", "dbg", "fine", "config" -> DEBUG;
            case "info", "inf", "information", "informational", "notice", "log" -> INFO;
            case "warn", "warning", "wrn" -> WARN;
            case "error", "err", "eror", "severe" -> ERROR;
            case "fatal", "critical", "crit", "alert", "emerg", "emergency", "panic", "ftl" -> FATAL;
            default -> 0;
        };
    }

    private static final Pattern LEVEL_FIELD = Pattern.compile("(?i)\\b(?:level|lvl|severity)=[\"']?([a-z]+)");
    /** A date, a time or both, as loggers print them. */
    private static final Pattern TIMESTAMP = Pattern.compile("^[\\[(]?[0-9][0-9/:.,\\-TZ+]*[\\])]?$");

    /**
     * The level a plain-text line states at its start ({@code ERROR ...}, {@code [warn] ...},
     * {@code error: ...}, {@code 2026-10-28 09:30:12 WARN ...}), or a {@code level=} field in the
     * first 200 characters. A bare lower-case word ({@code error rate is low}) is not a level.
     * 0 when the line states none.
     */
    static int textLevel(String s) {
        String head = s.length() > 200 ? s.substring(0, 200) : s;
        Matcher m = LEVEL_FIELD.matcher(head);
        if (m.find()) {
            int n = parseLevel(m.group(1));
            if (n > 0) {
                return n;
            }
        }
        String trimmed = head.strip();
        if (trimmed.isEmpty()) {
            return 0;
        }
        String[] fields = trimmed.split("\\s+");
        for (int i = 0; i < fields.length && i <= 3; i++) {
            String f = fields[i];
            if (TIMESTAMP.matcher(f).matches()) {
                continue;
            }
            String word = trimChars(f, "[]<>():|");
            int n = parseLevel(word);
            if (n == 0) {
                return 0;
            }
            boolean marked = f.startsWith("[") || f.startsWith("<") || f.startsWith("(")
                    || f.endsWith(":") || f.endsWith("]") || f.endsWith("|");
            return marked || word.equals(word.toUpperCase(Locale.ROOT)) ? n : 0;
        }
        return 0;
    }

    /**
     * Removes a leading upper-case level word that {@link #textLevel} read as {@code sev}
     * ({@code INFO started} → {@code started}, {@code WARNING: disk} → {@code disk}).
     */
    static String stripLevelWord(String s, int sev) {
        int space = s.indexOf(' ');
        if (space < 0) {
            return s;
        }
        String word = s.substring(0, space);
        String rest = s.substring(space + 1);
        if (rest.isBlank() || !word.equals(word.toUpperCase(Locale.ROOT))) {
            return s;
        }
        String w = (word.endsWith(":") ? word.substring(0, word.length() - 1) : word).toLowerCase(Locale.ROOT);
        return w.equals(name(sev)) || w.equals("warning") ? rest : s;
    }

    private static String trimChars(String s, String chars) {
        int start = 0;
        int end = s.length();
        while (start < end && chars.indexOf(s.charAt(start)) >= 0) {
            start++;
        }
        while (end > start && chars.indexOf(s.charAt(end - 1)) >= 0) {
            end--;
        }
        return s.substring(start, end);
    }

    /** Credentials in free text and what replaces them, in the order SPEC.md gives. */
    private static final Pattern[] SECRETS = {
        Pattern.compile("-----BEGIN [A-Z ]*PRIVATE KEY-----[\\s\\S]*?(-----END [A-Z ]*PRIVATE KEY-----|$)"),
        Pattern.compile("\\beyJ[A-Za-z0-9_-]{5,}\\.[A-Za-z0-9_-]{5,}\\.[A-Za-z0-9_-]{5,}"),
        Pattern.compile("(?i)\\b(bearer|basic)\\s+[A-Za-z0-9._~+/=-]{8,}"),
        Pattern.compile("\\b(AKIA|ASIA)[0-9A-Z]{16}\\b"),
        Pattern.compile("(?i)\\b(password|passwd|pwd|secret|token|api[_-]?key|access[_-]?token|refresh[_-]?token"
                + "|client[_-]?secret)\\b([\"']?\\s*[=:]\\s*)(\"[^\"]*\"|'[^']*'|[^\\s,;&\"']+)"),
    };
    private static final String[] SECRET_REPLACEMENTS = {
        "[REDACTED:private-key]", "[REDACTED:jwt]", "$1 [REDACTED]", "[REDACTED:aws-key]", "$1$2[REDACTED]",
    };

    /** Replaces credentials in {@code s}. */
    static String scrub(String s) {
        if (s == null || s.isEmpty()) {
            return s;
        }
        for (int i = 0; i < SECRETS.length; i++) {
            Matcher m = SECRETS[i].matcher(s);
            if (m.find()) {
                s = m.replaceAll(SECRET_REPLACEMENTS[i]);
            }
        }
        return s;
    }

    /** The redacted field names (the request redaction defaults plus {@code extra}), lower case. */
    static Set<String> secretKeys(List<String> extra) {
        Set<String> out = new HashSet<>();
        List<String> all = new ArrayList<>(Redactor.DEFAULT_FIELDS);
        if (extra != null) {
            all.addAll(extra);
        }
        for (String f : all) {
            String k = f.trim().toLowerCase(Locale.ROOT);
            if (!k.isEmpty()) {
                out.add(k);
            }
        }
        return Collections.unmodifiableSet(out);
    }

    /**
     * Adds a value under {@code key} as a string, number or boolean; maps become dotted keys,
     * times their ISO-8601 form, anything else its string form.
     */
    static void putAttr(Map<String, Object> out, String key, Object value) {
        if (key == null || key.isEmpty()) {
            return;
        }
        if (value instanceof Map<?, ?> m) {
            for (Map.Entry<?, ?> e : m.entrySet()) {
                putAttr(out, key + "." + e.getKey(), e.getValue());
            }
            return;
        }
        Object v;
        if (value == null || value instanceof String || value instanceof Boolean) {
            v = value;
        } else if (value instanceof Long || value instanceof Integer || value instanceof Short
                || value instanceof Byte) {
            v = ((Number) value).longValue();
        } else if (value instanceof Double || value instanceof Float) {
            double d = ((Number) value).doubleValue();
            v = Double.isNaN(d) || Double.isInfinite(d) ? String.valueOf(d) : d;
        } else if (value instanceof Date d) {
            v = d.toInstant().toString();
        } else if (value instanceof TemporalAccessor) {
            v = value.toString();
        } else {
            v = String.valueOf(value);
        }
        out.put(key, v);
    }

    /**
     * The attributes as one flat JSON object: values under a secret key (its last dotted
     * part) redacted, strings scrubbed, at most {@link #MAX_ATTRS} bytes. Long string values
     * are cut first, then keys are kept in sorted order while they fit, with
     * {@code "_truncated": true}. Null when there are none.
     */
    static String encodeAttrs(Map<String, Object> attrs, Set<String> secret) {
        if (attrs == null || attrs.isEmpty()) {
            return null;
        }
        Map<String, Object> out = new LinkedHashMap<>();
        for (Map.Entry<String, Object> e : attrs.entrySet()) {
            String k = e.getKey();
            String last = k.substring(k.lastIndexOf('.') + 1).toLowerCase(Locale.ROOT);
            Object v = e.getValue();
            if (secret != null && secret.contains(last)) {
                v = Redactor.REDACTED;
            } else if (v instanceof String s) {
                v = scrub(s);
            }
            out.put(k, v);
        }
        String json = write(out);
        if (json == null || bytes(json) <= MAX_ATTRS) {
            return json;
        }
        for (Map.Entry<String, Object> e : out.entrySet()) {
            if (e.getValue() instanceof String s && bytes(s) > MAX_ATTR_VALUE) {
                e.setValue(Values.capBytes(s, MAX_ATTR_VALUE) + "…");
            }
        }
        json = write(out);
        if (json == null || bytes(json) <= MAX_ATTRS) {
            return json;
        }
        List<String> keys = new ArrayList<>(out.keySet());
        Collections.sort(keys);
        Map<String, Object> kept = new LinkedHashMap<>();
        kept.put("_truncated", true);
        // {"_truncated":true} plus ,"key":value for each kept key
        long size = bytes(write(kept));
        for (String k : keys) {
            Map<String, Object> one = new LinkedHashMap<>();
            one.put(k, out.get(k));
            String entry = write(one);
            long add = bytes(entry) - 2 + 1;
            if (size + add > MAX_ATTRS) {
                break;
            }
            kept.put(k, out.get(k));
            size += add;
        }
        return write(kept);
    }

    private static String write(Map<String, Object> m) {
        try {
            return JSON.writeValueAsString(m);
        } catch (JsonProcessingException e) {
            return null;
        }
    }

    static int bytes(String s) {
        return s == null ? 0 : s.getBytes(StandardCharsets.UTF_8).length;
    }
}
