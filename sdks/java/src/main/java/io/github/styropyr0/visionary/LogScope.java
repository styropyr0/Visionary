package io.github.styropyr0.visionary;

import java.util.ArrayDeque;
import java.util.function.Supplier;

/**
 * Follows handed-over work like {@link RequestScope}; lines logged after the request ended
 * are decided at once.
 */
final class LogScope {

    private static final ThreadLocal<LogScope> CURRENT = new ThreadLocal<>();

    /** Null when the request is not logged to {@code api_logs}. */
    final String requestId;
    final String traceId;
    private final Supplier<String> user;

    // Guarded by this.
    ArrayDeque<LogRecord> held;
    boolean done;
    String route;
    private String finalUser;
    volatile boolean errored;

    /** @param user read until the request ends, since the user may be set while it runs */
    LogScope(String requestId, String traceId, Supplier<String> user) {
        this.requestId = requestId;
        this.traceId = traceId;
        this.user = user;
    }

    static LogScope current() {
        return CURRENT.get();
    }

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

    static void markError() {
        LogScope s = CURRENT.get();
        if (s != null) {
            s.errored = true;
        }
    }

    /** Frozen when the request ends. Call with this locked. */
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

    /** Returns the held lines, or null. Call with this locked. */
    ArrayDeque<LogRecord> end(String finalRoute) {
        finalUser = userId();
        route = finalRoute == null ? "" : finalRoute;
        done = true;
        ArrayDeque<LogRecord> out = held;
        held = null;
        return out;
    }

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
