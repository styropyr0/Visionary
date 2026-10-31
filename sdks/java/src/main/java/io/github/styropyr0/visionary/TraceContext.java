package io.github.styropyr0.visionary;

import java.lang.reflect.Method;
import java.util.concurrent.ThreadLocalRandom;

/** W3C trace context for the current thread (https://www.w3.org/TR/trace-context/) */
final class TraceContext {

    static final String TRACEPARENT = "traceparent";
    static final String TRACESTATE = "tracestate";

    private static final ThreadLocal<TraceContext> CURRENT = new ThreadLocal<>();

    final String traceId;
    final String spanId;
    /** The trace-flags byte in hex ("01" sampled) */
    final String flags;
    /** The caller's tracestate, passed on unchanged, or null */
    final String state;

    TraceContext(String traceId, String spanId, String flags, String state) {
        this.traceId = traceId;
        this.spanId = spanId;
        this.flags = flags == null || flags.isEmpty() ? "01" : flags;
        this.state = state == null || state.isEmpty() ? null : state;
    }

    /** The innermost span on this thread, or null outside any trace */
    static TraceContext current() {
        return CURRENT.get();
    }

    static TraceContext enter(TraceContext tc) {
        TraceContext previous = CURRENT.get();
        restore(tc);
        return previous;
    }

    static void restore(TraceContext previous) {
        if (previous == null) {
            CURRENT.remove();
        } else {
            CURRENT.set(previous);
        }
    }

    TraceContext child(String spanId) {
        return new TraceContext(traceId, spanId, flags, state);
    }

    String traceparent() {
        return "00-" + traceId + "-" + spanId + "-" + flags;
    }

    /** The server span of an incoming request and its caller's span id, null when it starts a trace */
    record Server(TraceContext context, String parentSpanId) {
    }

    /**
     * A child of the active OpenTelemetry span when there is one, else of the
     * caller's span when {@code traceparent} names one, else the root of a new trace
     */
    static Server server(String traceparent, String tracestate) {
        TraceContext in = OpenTelemetry.current();
        if (in == null) {
            in = parse(traceparent);
        }
        if (in != null) {
            TraceContext server = new TraceContext(in.traceId, newSpanId(), in.flags, tracestate);
            return new Server(server, in.spanId);
        }
        return new Server(new TraceContext(newTraceId(), newSpanId(), "01", null), null);
    }

    /** Null for anything malformed, version ff and all-zero ids */
    static TraceContext parse(String header) {
        if (header == null) {
            return null;
        }
        String[] parts = header.trim().split("-", -1);
        if (parts.length < 4 || parts[0].length() != 2 || parts[0].equals("ff") || parts[1].length() != 32
                || parts[2].length() != 16 || parts[3].length() != 2) {
            return null;
        }
        // Version 00 has exactly four fields, later versions may append more
        if (parts[0].equals("00") && parts.length != 4) {
            return null;
        }
        for (int i = 0; i < 4; i++) {
            if (!isLowerHex(parts[i])) {
                return null;
            }
        }
        if (allZero(parts[1]) || allZero(parts[2])) {
            return null;
        }
        return new TraceContext(parts[1], parts[2], parts[3], null);
    }

    private static boolean isLowerHex(String s) {
        for (int i = 0; i < s.length(); i++) {
            char c = s.charAt(i);
            if ((c < '0' || c > '9') && (c < 'a' || c > 'f')) {
                return false;
            }
        }
        return true;
    }

    private static boolean allZero(String s) {
        for (int i = 0; i < s.length(); i++) {
            if (s.charAt(i) != '0') {
                return false;
            }
        }
        return true;
    }

    static String newTraceId() {
        ThreadLocalRandom r = ThreadLocalRandom.current();
        long hi;
        long lo;
        do {
            hi = r.nextLong();
            lo = r.nextLong();
        } while (hi == 0 && lo == 0);
        return hex(hi) + hex(lo);
    }

    static String newSpanId() {
        ThreadLocalRandom r = ThreadLocalRandom.current();
        long v;
        do {
            v = r.nextLong();
        } while (v == 0);
        return hex(v);
    }

    private static String hex(long v) {
        String s = Long.toHexString(v);
        return "0".repeat(16 - s.length()) + s;
    }

    /** Read by reflection so the SDK needs no OpenTelemetry dependency */
    private static final class OpenTelemetry {

        private static final Method CURRENT_SPAN;
        private static final Method SPAN_CONTEXT;
        private static final Method IS_VALID;
        private static final Method TRACE_ID;
        private static final Method SPAN_ID;
        private static final Method SAMPLED;

        static {
            Method[] m = new Method[6];
            try {
                Class<?> span = Class.forName("io.opentelemetry.api.trace.Span");
                Class<?> ctx = Class.forName("io.opentelemetry.api.trace.SpanContext");
                m[0] = span.getMethod("current");
                m[1] = span.getMethod("getSpanContext");
                m[2] = ctx.getMethod("isValid");
                m[3] = ctx.getMethod("getTraceId");
                m[4] = ctx.getMethod("getSpanId");
                m[5] = ctx.getMethod("isSampled");
            } catch (ReflectiveOperationException | LinkageError e) {
                m = new Method[6];
            }
            CURRENT_SPAN = m[0];
            SPAN_CONTEXT = m[1];
            IS_VALID = m[2];
            TRACE_ID = m[3];
            SPAN_ID = m[4];
            SAMPLED = m[5];
        }

        static TraceContext current() {
            if (CURRENT_SPAN == null) {
                return null;
            }
            try {
                Object sc = SPAN_CONTEXT.invoke(CURRENT_SPAN.invoke(null));
                if (!Boolean.TRUE.equals(IS_VALID.invoke(sc))) {
                    return null;
                }
                String flags = Boolean.TRUE.equals(SAMPLED.invoke(sc)) ? "01" : "00";
                return new TraceContext((String) TRACE_ID.invoke(sc), (String) SPAN_ID.invoke(sc), flags, null);
            } catch (ReflectiveOperationException | RuntimeException e) {
                return null;
            }
        }
    }
}
