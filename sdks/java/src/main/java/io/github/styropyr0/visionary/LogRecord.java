package io.github.styropyr0.visionary;

import java.time.Instant;
import java.util.Map;

/**
 * One captured log line. Filled on the logging thread; scrubbing, encoding and
 * naming the source file happen on the writer thread.
 */
final class LogRecord {

    /** Attributes that have columns of their own; Micrometer Tracing's MDC names count as the active span. */
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
    /** Caller data, when the logger recorded it: binary class name, method, source file, line. */
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

    /** Moves the attributes with columns of their own out of {@code attrs} and keeps the rest. */
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
