package io.github.styropyr0.visionary;

import java.util.ArrayList;
import java.util.Comparator;
import java.util.List;

/**
 * The logged request a thread is working for. Spans and captured errors made
 * while it is current attach to it; the filter collects them when the request
 * finishes. Threads other than the request thread see it only when the work was
 * handed over with {@link Visionary#wrap(Runnable)} or {@link VisionaryTaskDecorator}.
 */
final class RequestScope {

    private static final ThreadLocal<RequestScope> CURRENT = new ThreadLocal<>();

    final String requestId;
    final long startNanos;
    private final int maxSpans;

    private final List<VisionarySpan> spans = new ArrayList<>();
    private List<ErrorEntry> errors;
    private long dbCount;
    private double dbMs;
    private long httpCount;
    private double httpMs;
    private boolean finished;

    RequestScope(String requestId, long startNanos, int maxSpans) {
        this.requestId = requestId;
        this.startNanos = startNanos;
        this.maxSpans = maxSpans;
    }

    static RequestScope current() {
        return CURRENT.get();
    }

    /** Makes {@code scope} current on this thread and returns the one it replaced, for {@link #restore}. */
    static RequestScope enter(RequestScope scope) {
        RequestScope previous = CURRENT.get();
        restore(scope);
        return previous;
    }

    static void restore(RequestScope previous) {
        if (previous == null) {
            CURRENT.remove();
        } else {
            CURRENT.set(previous);
        }
    }

    /** Counts the span and keeps it while under the cap; false once the request has finished. */
    synchronized boolean add(VisionarySpan span) {
        if (finished) {
            return false;
        }
        if ("db".equals(span.kind)) {
            dbCount++;
            dbMs += span.durationMs;
        } else if ("http".equals(span.kind)) {
            httpCount++;
            httpMs += span.durationMs;
        }
        if (spans.size() < maxSpans) {
            spans.add(span);
        }
        return true;
    }

    /** Holds the error until the request's route and user are known; false once finished. */
    synchronized boolean add(ErrorEntry error) {
        if (finished) {
            return false;
        }
        if (errors == null) {
            errors = new ArrayList<>(1);
        }
        errors.add(error);
        return true;
    }

    synchronized Finished finish() {
        finished = true;
        List<VisionarySpan> ordered = new ArrayList<>(spans);
        ordered.sort(Comparator.comparingLong(s -> s.startNanos));
        return new Finished(startNanos, ordered, errors == null ? List.of() : List.copyOf(errors),
                dbCount, dbMs, httpCount, httpMs);
    }

    /** What a request collected: spans in start order (capped) and the totals over all of them. */
    record Finished(long startNanos, List<VisionarySpan> spans, List<ErrorEntry> errors,
            long dbCount, double dbMs, long httpCount, double httpMs) {
    }
}
