package io.github.styropyr0.visionary;

import java.time.Instant;
import java.util.Map;

/** Filled on the logging thread; scrubbing and encoding happen on the writer thread. */
final class LogRecord {

    /** traceId and spanId are Micrometer Tracing's MDC names. */
    private static final String[][] COLUMNS = {
        {"request_id"}, {"trace_id", "traceId"}, {"span_id", "spanId"}, {"user_id"}, {"session_id"}, {"logger"},
    };

    final Instant at;
    final int sev;
    String logger;
    final String message;
    /** Null when the line has none. */
    Map<String, Object> attrs;
    String requestId;
    String traceId;
    String spanId;
    String userId;
    String sessionId;
    /** Set only when the logger recorded caller data. */
    String className;
    String method;
    String fileName;
    int line;

    LogRecord(Instant at, int sev, String logger, String message) {
        this.at = at;
        this.sev = sev;
        this.logger = logger == null || logger.isEmpty() ? null : logger;
        this.message = message == null ? "" : message;
    }

    /** Moves keys that have their own columns out of {@code attrs}. */
    LogRecord take(Map<String, Object> fields) {
        for (String[] names : COLUMNS) {
            for (String name : names) {
                if (!(fields.get(name) instanceof String s)) {
                    continue;
                }
                fields.remove(name);
                if (s.isEmpty()) {
                    continue;
                }
                switch (names[0]) {
                    case "request_id" -> requestId = requestId == null ? s : requestId;
                    case "trace_id" -> traceId = traceId == null ? s : traceId;
                    case "span_id" -> spanId = spanId == null ? s : spanId;
                    case "user_id" -> userId = userId == null ? s : userId;
                    case "session_id" -> sessionId = sessionId == null ? s : sessionId;
                    default -> logger = s;
                }
            }
        }
        attrs = fields.isEmpty() ? null : fields;
        return this;
    }
}
