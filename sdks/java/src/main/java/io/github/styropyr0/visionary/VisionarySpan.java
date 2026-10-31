package io.github.styropyr0.visionary;

import java.time.Instant;

/**
 * One timed call the service made: a database statement, an outbound HTTP
 * request, or anything timed with {@link Visionary#span(String, String)}.
 * Close it when the call is done; closing twice is harmless.
 *
 * <pre>{@code
 * try (VisionarySpan span = Visionary.span("cache", "GET session")) {
 *     return cache.get(key);
 * }
 * }</pre>
 *
 * When Visionary is off, or spans are disabled, the span does nothing.
 */
public final class VisionarySpan implements AutoCloseable {

    static final VisionarySpan NOOP = new VisionarySpan(null, null, "custom", "");

    private final Telemetry telemetry;
    final RequestScope scope;
    final String kind;
    final Instant startedAt;
    final long startNanos;
    /** For {@code db} spans without an explicit name: the raw statement, sanitized on the writer thread. */
    String sql;
    String name;
    String target;
    String detail;
    String error;
    boolean failed;
    double durationMs;
    /** Rows returned or affected; -1 when unknown. Result sets update it as they are read. */
    volatile long rowCount = -1;
    final TraceContext context;
    final String parentSpanId;
    private Thread owner;
    private TraceContext previous;
    private boolean closed;

    VisionarySpan(Telemetry telemetry, RequestScope scope, String kind, String name) {
        this.telemetry = telemetry;
        this.scope = scope;
        this.kind = kind;
        this.name = name;
        this.startedAt = telemetry == null ? null : Instant.now();
        this.startNanos = System.nanoTime();
        if (telemetry == null) {
            this.context = null;
            this.parentSpanId = null;
            return;
        }
        TraceContext parent = TraceContext.current();
        String spanId = TraceContext.newSpanId();
        this.context = parent != null ? parent.child(spanId)
                : new TraceContext(TraceContext.newTraceId(), spanId, "01", null);
        this.parentSpanId = parent == null ? null : parent.spanId;
    }

    /** Makes this span the parent of calls made on this thread until it closes */
    VisionarySpan makeCurrent() {
        if (context != null) {
            owner = Thread.currentThread();
            previous = TraceContext.enter(context);
        }
        return this;
    }

    /** What was called: for example {@code redis/sessions} or a host name. */
    public VisionarySpan target(String target) {
        if (telemetry != null) {
            this.target = target;
        }
        return this;
    }

    /** A longer description, such as the command or URL; stored up to 2000 characters. */
    public VisionarySpan detail(String detail) {
        if (telemetry != null) {
            this.detail = detail;
        }
        return this;
    }

    public VisionarySpan rowCount(long rows) {
        if (telemetry != null) {
            this.rowCount = rows;
        }
        return this;
    }

    /** Marks the span as failed with the error's message. */
    public VisionarySpan error(Throwable error) {
        return error(error == null ? null : Errors.shortMessage(error));
    }

    /** Marks the span as failed. */
    public VisionarySpan error(String message) {
        if (telemetry != null) {
            this.failed = true;
            this.error = message;
        }
        return this;
    }

    @Override
    public void close() {
        if (telemetry == null) {
            return;
        }
        synchronized (this) {
            if (closed) {
                return;
            }
            closed = true;
        }
        if (owner == Thread.currentThread()) {
            TraceContext.restore(previous);
        }
        durationMs = (System.nanoTime() - startNanos) / 1_000_000.0;
        telemetry.finished(this);
    }

    void addRow() {
        long n = rowCount;
        rowCount = n < 0 ? 1 : n + 1;
    }
}
