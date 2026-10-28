package io.github.styropyr0.visionary;

import java.util.Iterator;

import ch.qos.logback.classic.Logger;
import ch.qos.logback.classic.LoggerContext;
import ch.qos.logback.classic.spi.ILoggingEvent;
import ch.qos.logback.core.Appender;

import org.slf4j.ILoggerFactory;
import org.slf4j.LoggerFactory;
import org.springframework.context.SmartLifecycle;

/**
 * With {@code VISIONARY_LOG_CAPTURE=true}, adds a {@link VisionaryLogbackAppender}
 * to Logback's root logger while the agent runs, next to the appenders already
 * there. Levels, filters and the other appenders are left as they are, so what
 * the application prints does not change. Does nothing by default (capture is
 * opt-in), when SLF4J is bound to another backend, when logs are off, or when
 * the application's Logback configuration already has one.
 */
final class LogbackInstaller implements SmartLifecycle {

    private final AppLogs logs;
    private final boolean attach;
    private Logger root;
    private VisionaryLogbackAppender appender;
    private volatile boolean running;

    /**
     * @param logs   null when logs are off
     * @param attach {@code VISIONARY_LOG_CAPTURE}: add the appender to the root logger
     */
    LogbackInstaller(AppLogs logs, boolean attach) {
        this.logs = logs;
        this.attach = attach;
    }

    @Override
    public synchronized void start() {
        if (running) {
            return;
        }
        running = true;
        if (logs == null || !attach) {
            return;
        }
        ILoggerFactory factory = LoggerFactory.getILoggerFactory();
        if (!(factory instanceof LoggerContext context)) {
            return;
        }
        Logger r = context.getLogger(Logger.ROOT_LOGGER_NAME);
        for (Iterator<Appender<ILoggingEvent>> it = r.iteratorForAppenders(); it.hasNext();) {
            if (it.next() instanceof VisionaryLogbackAppender) {
                return;
            }
        }
        VisionaryLogbackAppender a = new VisionaryLogbackAppender();
        a.setContext(context);
        a.setName("VISIONARY");
        a.start();
        // Added last: appenders run in order, so caller data a console pattern asks for is already there.
        r.addAppender(a);
        root = r;
        appender = a;
    }

    @Override
    public synchronized void stop() {
        if (!running) {
            return;
        }
        running = false;
        if (appender != null) {
            root.detachAppender(appender);
            appender.stop();
            appender = null;
            root = null;
        }
    }

    @Override
    public boolean isRunning() {
        return running;
    }

    /** Starts after the agent (so lines have somewhere to go) and stops before it. */
    @Override
    public int getPhase() {
        return SmartLifecycle.DEFAULT_PHASE - 4095;
    }
}
