package io.github.styropyr0.visionary;

import java.time.Instant;

/**
 * One captured exception. The fingerprint is computed on the capturing thread
 * (the rate limit needs it); message and stack are formatted on the writer.
 */
record ErrorEntry(
        Instant createdAt,
        Throwable error,
        String type,
        String fingerprint,
        boolean handled,
        String requestId,
        String method,
        String route,
        String path,
        String userId) implements Queued {

    static ErrorEntry of(Throwable error, boolean handled) {
        return new ErrorEntry(Instant.now(), error, error.getClass().getName(), Errors.fingerprint(error), handled,
                null, null, null, null, null);
    }

    ErrorEntry withRequest(String requestId, String method, String route, String path, String userId) {
        return new ErrorEntry(createdAt, error, type, fingerprint, handled, requestId, method, route, path, userId);
    }
}
