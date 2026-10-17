package io.github.styropyr0.visionary;

import java.net.URI;

/**
 * Span and error capture. The JDBC proxy, the HTTP client interceptors and the
 * public API reach it through {@link #active()}, which is null until the agent
 * starts and after it stops, so they cost one volatile read when Visionary is off.
 */
final class Telemetry {

    private static final int MAX_DETAIL = 2000;
    private static volatile Telemetry active;

    final boolean spans;
    final boolean errors;
    final double spanThresholdMs;
    final int spanMax;
    private final RequestWriter writer;
    private final Errors limiter = new Errors();

    Telemetry(VisionaryConfig cfg, RequestWriter writer) {
        this.spans = cfg.spans();
        this.errors = cfg.errors();
        this.spanThresholdMs = cfg.spanThresholdMs();
        this.spanMax = cfg.spanMax();
        this.writer = writer;
    }

    static Telemetry active() {
        return active;
    }

    void activate() {
        active = this;
    }

    void deactivate() {
        if (active == this) {
            active = null;
        }
    }

    /** A running span, or null when spans are off. */
    static VisionarySpan start(String kind, String name) {
        Telemetry t = active;
        if (t == null || !t.spans) {
            return null;
        }
        return new VisionarySpan(t, RequestScope.current(), kind, name);
    }

    /** An {@code http} span named {@code METHOD host}; the URL is stored without its query. */
    static VisionarySpan startHttp(String method, URI uri) {
        Telemetry t = active;
        if (t == null || !t.spans) {
            return null;
        }
        String host = uri.getHost() == null ? "" : uri.getHost();
        VisionarySpan span = new VisionarySpan(t, RequestScope.current(), "http", method + " " + host);
        span.target = host;
        String port = uri.getPort() < 0 ? "" : ":" + uri.getPort();
        String path = uri.getRawPath() == null ? "" : uri.getRawPath();
        span.detail = method + " " + uri.getScheme() + "://" + host + port + path;
        return span;
    }

    static void httpStatus(VisionarySpan span, int status) {
        if (status >= 500) {
            span.error("HTTP " + status);
        }
    }

    void finished(VisionarySpan span) {
        span.detail = Values.cap(span.detail, MAX_DETAIL);
        RequestScope scope = span.scope;
        if (scope != null && scope.add(span)) {
            return;
        }
        if (span.durationMs >= spanThresholdMs) {
            writer.enqueue(new SpanEntry(span));
        }
    }

    /** Records an exception; inside a logged request it waits for the request to finish. */
    void capture(Throwable error, boolean handled) {
        ErrorEntry e = newError(error, handled);
        if (e == null) {
            return;
        }
        RequestScope scope = RequestScope.current();
        if (scope == null || !scope.add(e)) {
            writer.enqueue(scope == null ? e : e.withRequest(scope.requestId, null, null, null, null));
        }
    }

    /** A rate-limited error entry, or null when error capture is off or the limit is reached. */
    ErrorEntry newError(Throwable error, boolean handled) {
        if (!errors || error == null) {
            return null;
        }
        ErrorEntry e = ErrorEntry.of(error, handled);
        return limiter.allow(e.fingerprint(), System.currentTimeMillis()) ? e : null;
    }

    void enqueue(ErrorEntry e) {
        writer.enqueue(e);
    }
}
