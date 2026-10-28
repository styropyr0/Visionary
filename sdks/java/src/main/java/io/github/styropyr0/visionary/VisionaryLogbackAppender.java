package io.github.styropyr0.visionary;

import java.time.Instant;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

import ch.qos.logback.classic.Level;
import ch.qos.logback.classic.spi.ILoggingEvent;
import ch.qos.logback.classic.spi.IThrowableProxy;
import ch.qos.logback.classic.spi.ThrowableProxyUtil;
import ch.qos.logback.core.UnsynchronizedAppenderBase;

import org.slf4j.event.KeyValuePair;

/**
 * Copies Logback events to Visionary, linked to the request they were logged in. Declare it in
 * {@code logback-spring.xml}, or set {@code VISIONARY_LOG_CAPTURE=true} to have the starter add it:
 *
 * <pre>{@code
 * <appender name="VISIONARY" class="io.github.styropyr0.visionary.VisionaryLogbackAppender"/>
 * <root level="INFO">
 *     <appender-ref ref="CONSOLE"/>
 *     <appender-ref ref="VISIONARY"/>
 * </root>
 * }</pre>
 *
 * <p>It only sees what logger levels let through. Attach it directly, not behind an
 * {@code AsyncAppender}, so lines stay on the thread of the request that logged them.
 */
public class VisionaryLogbackAppender extends UnsynchronizedAppenderBase<ILoggingEvent> {

    private static final String SDK_LOGGERS = Visionary.class.getPackageName();
    private static final String SDK_THREADS = "visionary-";
    private static final String APP_THREADS = "visionary-background-";

    @Override
    public void start() {
        boolean was = isStarted();
        super.start();
        if (!was && isStarted()) {
            AppLogs.appenderStarted();
        }
    }

    @Override
    public void stop() {
        boolean was = isStarted();
        super.stop();
        if (was) {
            AppLogs.appenderStopped();
        }
    }

    @Override
    protected void append(ILoggingEvent event) {
        AppLogs logs = AppLogs.active();
        if (logs == null || event == null) {
            return;
        }
        try {
            int sev = severity(event.getLevel());
            if (!logs.wants(sev) || own(event)) {
                return;
            }
            logs.capture(record(event, sev));
        } catch (RuntimeException e) {
            // Logging must never fail because of Visionary.
        }
    }

    private static boolean own(ILoggingEvent event) {
        String logger = event.getLoggerName();
        if (logger != null && (logger.equals(SDK_LOGGERS) || logger.startsWith(SDK_LOGGERS + "."))) {
            return true;
        }
        String thread = event.getThreadName();
        return thread != null && thread.startsWith(SDK_THREADS) && !thread.startsWith(APP_THREADS);
    }

    /** Logback has no fatal. */
    static int severity(Level level) {
        int n = level == null ? Level.INFO_INT : level.toInt();
        if (n < Level.DEBUG_INT) {
            return LogRules.TRACE;
        }
        if (n < Level.INFO_INT) {
            return LogRules.DEBUG;
        }
        if (n < Level.WARN_INT) {
            return LogRules.INFO;
        }
        return n < Level.ERROR_INT ? LogRules.WARN : LogRules.ERROR;
    }

    static LogRecord record(ILoggingEvent event, int sev) {
        String message = event.getFormattedMessage();
        IThrowableProxy thrown = event.getThrowableProxy();
        if (thrown != null) {
            String stack = ThrowableProxyUtil.asString(thrown);
            message = message == null || message.isEmpty() ? stack : message + "\n" + stack;
        }
        Instant at = event.getInstant();
        LogRecord r = new LogRecord(at != null ? at : Instant.now(), sev, event.getLoggerName(), message);

        Map<String, Object> attrs = new LinkedHashMap<>();
        Map<String, String> mdc = event.getMDCPropertyMap();
        if (mdc != null) {
            mdc.forEach((k, v) -> LogRules.putAttr(attrs, k, v));
        }
        List<KeyValuePair> pairs = event.getKeyValuePairs();
        if (pairs != null) {
            for (KeyValuePair kv : pairs) {
                if (kv != null) {
                    LogRules.putAttr(attrs, kv.key, kv.value);
                }
            }
        }
        r.take(attrs);

        // Never force caller data: computing it walks the stack.
        if (event.hasCallerData()) {
            StackTraceElement[] caller = event.getCallerData();
            if (caller != null && caller.length > 0 && caller[0] != null) {
                r.className = caller[0].getClassName();
                r.method = caller[0].getMethodName();
                r.fileName = caller[0].getFileName();
                r.line = caller[0].getLineNumber();
            }
        }
        return r;
    }
}
