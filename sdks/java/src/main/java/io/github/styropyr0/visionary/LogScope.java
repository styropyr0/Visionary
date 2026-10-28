package io.github.styropyr0.visionary;

import java.util.ArrayDeque;
import java.util.function.Supplier;

/**
 * The request a thread is logging for: its id, trace and user for the lines'
 * columns, and the lines held below the level until it ends (SPEC.md, "What is
 * kept"). Like {@link RequestScope} it follows work handed over with
 * {@link Visionary#wrap(Runnable)}, {@link Visionary#background} or
 * {@link VisionaryTaskDecorator}; lines logged there after the request ended are
 * decided at once with its route and user.
 */
final class LogScope {

    private static final ThreadLocal<LogScope> CURRENT = new ThreadLocal<>();

    /** Null when the request is not logged to {@code api_logs}. */
    final String requestId;
    /** The W3C trace id of the request's {@code traceparent}, or null. */
    final String traceId;
    private final Supplier<String> user;

    // Guarded by this.
    ArrayDeque<LogRecord> held;
    boolean done;
    String route;
    private String finalUser;
    /** An error was captured while the request ran. */
    volatile boolean errored;

    /**
     * @param user the request's user id as it is now (it may be set while the request runs);
     *             read only until the request ends
     */
    LogScope(String requestId, String traceId, Supplier<String> user) {
        this.requestId = requestId;
        this.traceId = traceId;
        this.user = user;
    }

    static LogScope current() {
        return CURRENT.get();
    }

    /** Makes {@code scope} current on this thread and returns the one it replaced, for {@link #restore}. */
    static LogScope enter(LogScope scope) {
        LogScope previous = CURRENT.get();
        restore(scope);
        return previous;
    }

    static void restore(LogScope previous) {
        if (previous == null) {
            CURRENT.remove();
        } else {
            CURRENT.set(previous);
        }
    }

    /** Marks the current request as failed for the buffer: an error was captured in it. */
    static void markError() {
        LogScope s = CURRENT.get();
        if (s != null) {
            s.errored = true;
        }
    }

    /** The request's user id, frozen when it ended. Call with this locked. */
    String userId() {
        if (done) {
            return finalUser;
        }
        try {
            return Values.emptyToNull(user == null ? null : user.get());
        } catch (RuntimeException e) {
            return null;
        }
    }

    /** Marks the request ended on {@code route}; returns its held lines (null if none). Call with this locked. */
    ArrayDeque<LogRecord> end(String finalRoute) {
        finalUser = userId();
        route = finalRoute == null ? "" : finalRoute;
        done = true;
        ArrayDeque<LogRecord> out = held;
        held = null;
        return out;
    }

    /** The id of a W3C {@code traceparent} header ({@code 00-<32 hex>-<16 hex>-<2 hex>}), null when there is none. */
    static String traceParentId(String header) {
        if (header == null) {
            return null;
        }
        String[] parts = header.trim().split("-");
        if (parts.length < 4 || parts[1].length() != 32) {
            return null;
        }
        boolean zero = true;
        for (int i = 0; i < 32; i++) {
            char c = parts[1].charAt(i);
            if ((c < '0' || c > '9') && (c < 'a' || c > 'f')) {
                return null;
            }
            zero &= c == '0';
        }
        return zero ? null : parts[1];
    }
}
