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
 * Lines below the level are held per request and written only if the request fails or is
 * slow. Nothing on the request path waits for the database.
 */
final class AppLogs {

    private static final Log log = LogFactory.getLog(AppLogs.class);
    static final String SETTINGS_QUERY = "SELECT name, value FROM visionary_settings WHERE name LIKE 'logs.%'";
    static final Duration POLL_INTERVAL = Duration.ofSeconds(30);
    /** Per request; past this the oldest held lines are dropped. */
    static final int MAX_HELD = 500;
    /** Never captured, or a failed write would queue a line behind itself. */
    static final String OWN_PREFIX = "visionary: ";

    private static volatile AppLogs active;
    /** Waits here for an appender; guarded by the class. */
    private static AppLogs ready;
    /** Guarded by the class. */
    private static int appenders;

    private final Store store;
    private final String service;
    private final int baseLevel;
    private final ObjectMapper json;
    private final Consumer<LogRecord> sink;
    private final LogWriter writer;
    private volatile LogSettings settings;

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

    /** For tests. */
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

    static AppLogs active() {
        return active;
    }

    LogSettings settings() {
        return settings;
    }

    /** Capture starts once an appender exists; until then {@code app_logs} isn't created. */
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

    boolean capturing() {
        return active == this;
    }

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

    boolean wants(int sev) {
        LogSettings s = settings;
        return s.enabled() && sev >= s.lowest();
    }

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
                // Logged after the request ended, so its route and user are final.
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

    /** Errors and fatals get ten times the per-minute cap so an info flood can't hide them. */
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
