package io.github.styropyr0.visionary;

import java.security.SecureRandom;
import java.util.Collections;
import java.util.HexFormat;
import java.util.LinkedHashMap;
import java.util.Locale;
import java.util.Map;

import jakarta.servlet.http.HttpServletRequest;

/** Small request-thread helpers for the filter. */
final class HttpCapture {

    static final String REQUEST_ID_HEADER = "X-Request-ID";

    private static final SecureRandom RANDOM = new SecureRandom();
    private static final String[] NOT_CAPTURABLE = {
        "multipart/", "image/", "video/", "audio/", "application/octet-stream", "application/zip", "application/pdf",
    };

    private HttpCapture() {
    }

    /** Binary and multipart payloads are useless as text and expensive to store. */
    static boolean capturable(String contentType) {
        if (contentType == null || contentType.isEmpty()) {
            return true;
        }
        String ct = contentType.toLowerCase(Locale.ROOT);
        for (String prefix : NOT_CAPTURABLE) {
            if (ct.startsWith(prefix)) {
                return false;
            }
        }
        return true;
    }

    static String requestId(HttpServletRequest request) {
        String id = request.getHeader(REQUEST_ID_HEADER);
        if (id != null && !id.isEmpty() && id.length() <= 128) {
            return id;
        }
        byte[] b = new byte[16];
        RANDOM.nextBytes(b);
        return HexFormat.of().formatHex(b);
    }

    /** First X-Forwarded-For hop when behind a proxy; informational only. */
    static String clientIp(HttpServletRequest request) {
        String fwd = request.getHeader("X-Forwarded-For");
        if (fwd != null && !fwd.isBlank()) {
            int comma = fwd.indexOf(',');
            return (comma < 0 ? fwd : fwd.substring(0, comma)).trim();
        }
        return request.getRemoteAddr();
    }

    /**
     * All headers with canonical names (as the Go SDK stores them, e.g.
     * {@code X-Forwarded-For}), repeated values joined with ", ".
     */
    static Map<String, String> headers(HttpServletRequest request) {
        Map<String, String> out = new LinkedHashMap<>();
        for (String name : Collections.list(request.getHeaderNames())) {
            // getHeaders is case-insensitive, so the first spelling already has every value.
            out.computeIfAbsent(canonical(name), k -> String.join(", ", Collections.list(request.getHeaders(name))));
        }
        return out;
    }

    static String canonical(String name) {
        char[] c = name.toLowerCase(Locale.ROOT).toCharArray();
        boolean upper = true;
        for (int i = 0; i < c.length; i++) {
            if (upper && c[i] >= 'a' && c[i] <= 'z') {
                c[i] = (char) (c[i] - 'a' + 'A');
            }
            upper = c[i] == '-';
        }
        return new String(c);
    }
}
