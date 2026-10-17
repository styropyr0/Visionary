package io.github.styropyr0.visionary;

import java.time.Duration;

import org.apache.commons.logging.Log;
import org.apache.commons.logging.LogFactory;
import org.springframework.beans.factory.DisposableBean;
import org.springframework.context.SmartLifecycle;

/**
 * Owns the background workers. Its lifecycle phase is far below the web
 * server's, so it starts before requests arrive and stops (flushing what is
 * queued) only after the server has stopped taking them.
 */
final class VisionaryAgent implements SmartLifecycle, DisposableBean {

    private static final Log log = LogFactory.getLog(VisionaryAgent.class);
    private static final Duration SHUTDOWN_TIMEOUT = Duration.ofSeconds(10);

    private final VisionaryConfig config;
    private final Store store;
    private final HttpCounters counters = new HttpCounters();
    private final RequestWriter writer;
    private final boolean logRequests;
    private final Telemetry telemetry;
    private final SystemSampler system;
    private final Retention retention;
    private final String summary;
    private volatile boolean running;
    private boolean closed;

    /** A disabled agent: nothing runs and the filter is not registered. */
    VisionaryAgent(VisionaryConfig config) {
        this.config = config;
        this.store = null;
        this.writer = null;
        this.logRequests = false;
        this.telemetry = null;
        this.system = null;
        this.retention = null;
        this.summary = "disabled";
    }

    VisionaryAgent(Store store, Origin origin, RequestWriter writer, SystemSampler.PoolStats pool,
            VisionaryConfig cfg) {
        this.config = cfg;
        this.store = store;
        this.writer = writer;
        this.logRequests = cfg.logRequests();
        this.telemetry = cfg.spans() || cfg.errors() ? new Telemetry(cfg, writer) : null;
        this.system = cfg.systemEnabled()
                ? new SystemSampler(store, origin, cfg.systemInterval(), counters, pool)
                : null;
        this.retention = new Retention(store);
        if (logRequests) {
            retention.add(Table.API_LOGS, cfg.logRetentionDays());
        }
        if (cfg.spans()) {
            retention.add(Table.SPANS, cfg.logRetentionDays());
        }
        if (cfg.errors()) {
            retention.add(Table.ERRORS, cfg.logRetentionDays());
        }
        if (system != null) {
            retention.add(Table.SYSTEM_METRICS, cfg.systemRetentionDays());
        }
        this.summary = "writing to " + store.describe()
                + "; service " + (origin.service() == null ? "(none)" : origin.service())
                + ", version " + (origin.version() == null ? "(none)" : origin.version())
                + "; requests " + (logRequests ? "on (prefix '" + cfg.pathPrefix() + "', bodies "
                        + cfg.logBodies() + ", retention " + cfg.logRetentionDays() + "d)" : "off")
                + "; spans " + (cfg.spans() ? "on (threshold " + cfg.spanThresholdMs() + "ms, max "
                        + cfg.spanMax() + ")" : "off")
                + "; errors " + (cfg.errors() ? "on" : "off")
                + "; system metrics " + (system != null ? "every " + cfg.systemInterval().toMillis() + "ms" : "off");
    }

    boolean enabled() {
        return store != null;
    }

    VisionaryConfig config() {
        return config;
    }

    HttpCounters counters() {
        return counters;
    }

    /** The writer when request logging is on, else null. */
    RequestWriter requests() {
        return logRequests ? writer : null;
    }

    /** Span and error capture, null when both are off or Visionary is disabled. */
    Telemetry telemetry() {
        return telemetry;
    }

    @Override
    public synchronized void start() {
        if (running || !enabled()) {
            return;
        }
        running = true;
        writer.start();
        if (telemetry != null) {
            telemetry.activate();
        }
        if (system != null) {
            system.start();
        }
        retention.start();
        log.info("visionary: " + summary);
    }

    @Override
    public synchronized void stop() {
        if (!running) {
            return;
        }
        running = false;
        if (telemetry != null) {
            telemetry.deactivate();
        }
        writer.close(SHUTDOWN_TIMEOUT);
        if (system != null) {
            system.close(SHUTDOWN_TIMEOUT);
        }
        retention.close();
    }

    @Override
    public boolean isRunning() {
        return running;
    }

    @Override
    public int getPhase() {
        return SmartLifecycle.DEFAULT_PHASE - 4096;
    }

    @Override
    public synchronized void destroy() {
        stop();
        if (store != null && !closed) {
            closed = true;
            store.close();
        }
    }
}
