package io.github.styropyr0.visionary;

import java.time.Duration;
import java.time.Instant;
import java.util.ArrayDeque;
import java.util.List;
import java.util.concurrent.Executors;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.TimeUnit;
import java.util.function.Consumer;

import com.fasterxml.jackson.databind.ObjectMapper;

import org.apache.commons.logging.Log;
import org.apache.commons.logging.LogFactory;

/**
 * Application logs ({@code app_logs}): lines the application logs through
 * Logback (see {@link VisionaryLogbackAppender}), linked to the request they
 * were logged in.
 *
 * <p>What is kept follows the dashboard (Settings → Logs), read from
 * {@code visionary_settings} at start and every 30 seconds like the profiling
 * switch: lines below {@code logs.level} are dropped, except inside a request
 * that failed (5xx, an exception, a captured error) or was slow, where the
 * lines down to {@code logs.buffer_level} that were held back are written too.
 * Overrides change the level for a service, route or user until a time; drop
 * rules and a per-minute cap thin the rest. Lines below the level are held in
 * the request until it ends; nothing on a request path waits for the database.
 */
final class AppLogs {

    private static final Log log = LogFactory.getLog(AppLogs.class);
    static final String SETTINGS_QUERY = "SELECT name, value FROM visionary_settings WHERE name LIKE 'logs.%'";
    static final Duration POLL_INTERVAL = Duration.ofSeconds(30);
    /** Lines one request holds back; past it, the oldest are dropped. */
    static final int MAX_HELD = 500;
    /** Messages the SDK logs itself; never captured, so a failed write is not queued behind itself. */
    static final String OWN_PREFIX = "visionary: ";

    private static volatile AppLogs active;
    /** The agent's pipeline once it runs, waiting for an appender; guarded by the class. */
    private static AppLogs ready;
    /** Started {@link VisionaryLogbackAppender}s; guarded by the class. */
    private static int appenders;

    private final Store store;
    private final String service;
    private final int baseLevel;
    private final ObjectMapper json;
    private final Consumer<LogRecord> sink;
    private final LogWriter writer;
    private volatile LogSettings settings;

    // The per-minute cap: lines counted in the current minute, and those dropped by it.
    private final Object rateLock = new Object();
    private long rateMinute = Long.MIN_VALUE;
    private int rateCount;
    private int rateDropped;

    private ScheduledExecutorService poller;
    private boolean readFailureLogged;
    private boolean started;
    private boolean stopped;

    AppLogs(Store store, Origin origin, VisionaryConfig cfg, FrameNamer namer) {
        this(store, origin.service(), LogRules.parseLevel(cfg.logLevel()),
                new LogWriter(store, origin, LogRules.secretKeys(cfg.redactFields()), namer));
    }

    private AppLogs(Store store, String service, int baseLevel, LogWriter writer) {
        this(store, service, baseLevel, writer::enqueue, writer);
    }

    /** For tests: lines go to {@code sink} instead of a writer. */
    AppLogs(Store store, String service, int baseLevel, Consumer<LogRecord> sink) {
        this(store, service, baseLevel, sink, null);
    }

    private AppLogs(Store store, String service, int baseLevel, Consumer<LogRecord> sink, LogWriter writer) {
        this.store = store;
        this.service = service == null ? "" : service;
        this.baseLevel = baseLevel;
        this.json = new ObjectMapper();
        this.sink = sink;
        this.writer = writer;
        this.settings = LogSettings.defaults(baseLevel);
    }

    /** The running pipeline, or null before the agent starts and after it stops. */
    static AppLogs active() {
        return active;
    }

    LogSettings settings() {
        return settings;
    }

    /**
     * The agent runs: capture starts as soon as a {@link VisionaryLogbackAppender} exists, now
     * if the application's Logback configuration already has one. Until then nothing runs and
     * {@code app_logs} is not created.
     */
    static void ready(AppLogs logs) {
        boolean go;
        synchronized (AppLogs.class) {
            ready = logs;
            go = appenders > 0;
        }
        if (go) {
            logs.start();
        }
    }

    /** An appender started (from the Logback configuration, or attached by the starter). */
    static void appenderStarted() {
        AppLogs logs;
        synchronized (AppLogs.class) {
            appenders++;
            logs = ready;
        }
        if (logs != null) {
            logs.start();
        }
    }

    static void appenderStopped() {
        synchronized (AppLogs.class) {
            appenders = Math.max(0, appenders - 1);
        }
    }

    /** Whether lines are being taken: an appender exists and the agent runs. */
    boolean capturing() {
        return active == this;
    }

    /** Starts the writer and the settings poll, then takes lines. Once; not again after {@link #stop}. */
    synchronized void start() {
        if (started || stopped) {
            return;
        }
        started = true;
        if (writer != null) {
            log.info("visionary: app logs on (level " + LogRules.name(settings.level()) + ")");
            writer.start();
        }
        if (store != null && poller == null) {
            poller = Executors.newSingleThreadScheduledExecutor(r -> {
                Thread t = new Thread(r, "visionary-log-settings");
                t.setDaemon(true);
                return t;
            });
            poller.scheduleWithFixedDelay(this::pollSafely, 0, POLL_INTERVAL.toSeconds(), TimeUnit.SECONDS);
        }
        active = this;
    }

    /** Stops taking lines and flushes what is queued, at most {@code timeout}. */
    synchronized void stop(Duration timeout) {
        stopped = true;
        synchronized (AppLogs.class) {
            if (ready == this) {
                ready = null;
            }
        }
        if (active == this) {
            active = null;
        }
        if (poller != null) {
            poller.shutdownNow();
            poller = null;
        }
        if (writer != null) {
            writer.close(timeout);
        }
    }

    private void pollSafely() {
        try {
            poll();
        } catch (RuntimeException | LinkageError e) {
            log.debug("visionary: log settings poll failed", e);
        }
    }

    /** Reads Settings → Logs; a failed read keeps the previous values (at first the defaults). */
    void poll() {
        List<String[]> rows;
        try {
            rows = store.select(SETTINGS_QUERY);
        } catch (Exception e) {
            if (!readFailureLogged) {
                readFailureLogged = true;
                log.debug("visionary: log settings unavailable (" + e.getMessage() + "); using defaults");
            }
            return;
        }
        readFailureLogged = false;
        settings = LogSettings.parse(rows, baseLevel, json);
    }

    /** For tests. */
    void use(LogSettings s) {
        settings = s;
    }

    /** Whether a line at {@code sev} could be kept at all; a cheap check before building it. */
    boolean wants(int sev) {
        LogSettings s = settings;
        return s.enabled() && sev >= s.lowest();
    }

    /** Takes one line logged on this thread. Never blocks on the database. */
    void capture(LogRecord r) {
        LogSettings s = settings;
        if (!s.enabled() || r.message.startsWith(OWN_PREFIX)) {
            return;
        }
        if (s.dropped(service, r.logger, r.message, r.sev)) {
            return;
        }
        LogScope scope = LogScope.current();
        if (scope == null) {
            if (r.sev >= s.floor(service, "", "", r.at)) {
                emit(r);
            }
            return;
        }
        int keepFrom;
        synchronized (scope) {
            if (r.requestId == null) {
                r.requestId = scope.requestId;
            }
            if (r.traceId == null) {
                r.traceId = scope.traceId;
            }
            if (r.userId == null) {
                r.userId = scope.userId();
            }
            if (scope.done) {
                // Work the request left running: its route and user are known now.
                keepFrom = s.floor(service, scope.route, r.userId == null ? "" : r.userId, r.at);
            } else if (r.sev >= s.holdBelow(service, r.at)) {
                keepFrom = r.sev;
            } else {
                if (scope.held == null) {
                    scope.held = new ArrayDeque<>();
                }
                if (scope.held.size() >= MAX_HELD) {
                    scope.held.pollFirst();
                }
                scope.held.add(r);
                return;
            }
        }
        if (r.sev >= keepFrom) {
            emit(r);
        }
    }

    /**
     * Decides a request's held lines once it has ended: kept when they reach the level for
     * its route and user, or, when the request failed or was slow, the buffer's level.
     */
    void finish(LogScope scope, String route, boolean failed, double tookMs) {
        ArrayDeque<LogRecord> held;
        String user;
        synchronized (scope) {
            held = scope.end(route);
            user = scope.userId();
            failed |= scope.errored;
        }
        if (held == null || held.isEmpty()) {
            return;
        }
        LogSettings s = settings;
        int floor = s.floor(service, route == null ? "" : route, user == null ? "" : user, Instant.now());
        boolean keepAll = s.buffer() && (failed || (s.slowMs() > 0 && tookMs >= s.slowMs()));
        for (LogRecord r : held) {
            if (r.userId == null) {
                r.userId = user;
            }
            if (r.sev >= floor || (keepAll && r.sev >= s.bufferLevel())) {
                emit(r);
            }
        }
    }

    /**
     * Queues a line within the per-minute cap. Errors and fatals pass the cap up to ten
     * times over, so a flood of info lines never hides them.
     */
    void emit(LogRecord r) {
        int rate = settings.rate();
        if (rate > 0) {
            long minute = Math.floorDiv(r.at.getEpochSecond(), 60);
            int dropped = 0;
            boolean over;
            synchronized (rateLock) {
                if (minute != rateMinute) {
                    dropped = rateDropped;
                    rateMinute = minute;
                    rateCount = 0;
                    rateDropped = 0;
                }
                rateCount++;
                over = rateCount > rate && (r.sev < LogRules.ERROR || rateCount > 10L * rate);
                if (over) {
                    rateDropped++;
                }
            }
            if (dropped > 0) {
                LogRecord warn = new LogRecord(Instant.now(), LogRules.WARN, "visionary",
                        OWN_PREFIX + dropped + " log lines dropped over the cap of " + rate
                                + " a minute (Settings → Logs)");
                sink.accept(warn);
            }
            if (over) {
                return;
            }
        }
        sink.accept(r);
    }
}
