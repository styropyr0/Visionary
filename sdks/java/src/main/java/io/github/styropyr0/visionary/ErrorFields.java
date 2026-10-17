package io.github.styropyr0.visionary;

import java.io.IOException;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

import com.fasterxml.jackson.databind.DeserializationFeature;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;

/**
 * Reads the error code and key from a JSON error response at dotted paths.
 * Bodies that aren't valid JSON (for example cut at the capture cap) fall back
 * to a pattern match on the path's last segment.
 */
final class ErrorFields {

    record Result(String code, String key) {
        static final Result NONE = new Result(null, null);
    }

    private final ObjectMapper strict;
    private final String codePath;
    private final String keyPath;
    private final Pattern codeFallback;
    private final Pattern keyFallback;

    ErrorFields(ObjectMapper json, String codePath, String keyPath) {
        this.strict = json.copy().enable(DeserializationFeature.FAIL_ON_TRAILING_TOKENS);
        this.codePath = codePath;
        this.keyPath = keyPath;
        this.codeFallback = fallback(codePath);
        this.keyFallback = fallback(keyPath);
    }

    Result extract(String body) {
        if (body == null || body.isEmpty()) {
            return Result.NONE;
        }
        JsonNode root = null;
        try {
            root = strict.readTree(body);
        } catch (IOException ignored) {
            // not JSON or truncated: use the pattern fallback
        }
        if (root != null && !root.isMissingNode()) {
            return new Result(lookup(root, codePath), lookup(root, keyPath));
        }
        return new Result(match(codeFallback, body), match(keyFallback, body));
    }

    private static String lookup(JsonNode node, String path) {
        if (path.isEmpty()) {
            return null;
        }
        for (String seg : path.split("\\.")) {
            if (!node.isObject()) {
                return null;
            }
            node = node.path(seg);
        }
        if (node.isTextual()) {
            return Values.emptyToNull(node.textValue());
        }
        if (node.isIntegralNumber()) {
            return node.bigIntegerValue().toString();
        }
        if (node.isNumber()) {
            return node.decimalValue().stripTrailingZeros().toPlainString();
        }
        return null;
    }

    private static Pattern fallback(String path) {
        if (path.isEmpty()) {
            return null;
        }
        String last = path.substring(path.lastIndexOf('.') + 1);
        return Pattern.compile("\"" + Pattern.quote(last) + "\"\\s*:\\s*\"([^\"]*)\"");
    }

    private static String match(Pattern p, String body) {
        if (p == null) {
            return null;
        }
        Matcher m = p.matcher(body);
        return m.find() ? Values.emptyToNull(m.group(1)) : null;
    }
}
