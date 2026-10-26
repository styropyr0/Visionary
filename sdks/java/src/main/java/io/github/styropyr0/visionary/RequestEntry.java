package io.github.styropyr0.visionary;

import java.time.Instant;
import java.util.Map;

/**
 * The raw capture of one request. Redaction, error extraction and user lookup
 * happen on the writer thread, off the request path. {@code telemetry} holds the
 * request's spans and call totals, null when span capture is off.
 */
record RequestEntry(
        String requestId,
        Instant startedAt,
        String method,
        String path,
        String route,
        String query,
        int status,
        double durationMs,
        String ip,
        String userAgent,
        VisionaryUser user,
        String loginAccount,
        Map<String, String> requestHeaders,
        String requestContentType,
        byte[] requestBody,
        boolean requestTruncated,
        long requestSize,
        String responseContentType,
        byte[] responseBody,
        boolean responseTruncated,
        long responseSize,
        RequestScope.Finished telemetry) implements Queued {
}
