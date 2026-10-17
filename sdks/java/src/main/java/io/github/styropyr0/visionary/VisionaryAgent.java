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
    private final RequestWriter requests;
    private final SystemSampler system;
    private final Retention retention;
    private final String summary;
    private volatile boolean running;
    private boolean closed;

    /** A disabled agent: nothing runs and the filter is not registered. */
    VisionaryAgent(VisionaryConfig config) {
        this.config = config;
        this.store = null;
        this.requests = null;
        this.system = null;
        this.retention = null;
        this.summary = "disabled";
    }

    VisionaryAgent(Store store, RequestWriter requests, SystemSampler.PoolStats pool, VisionaryConfig cfg) {
        this.config = cfg;
        this.store = store;
        this.requests = requests;
        this.system = cfg.systemEnabled()
                ? new SystemSampler(store, cfg.resolvedInstance(), cfg.systemInterval(), counters, pool)
                : null;
        this.retention = new Retention(store);
        if (requests != null) {
            retention.add(Table.API_LOGS, cfg.logRetentionDays());
        }
        if (system != null) {
            retention.add(Table.SYSTEM_METRICS, cfg.systemRetentionDays());
        }
        this.summary = "writing to " + store.describe()
                + "; requests " + (requests != null ? "on (prefix '" + cfg.pathPrefix() + "', bodies "
                        + cfg.logBodies() + ", retention " + cfg.logRetentionDays() + "d)" : "off")
                + "; system metrics " + (system != null ? "every " + cfg.systemInterval().toMillis() + "ms" : "off");
    }

    boolean enabled() {
        return store != null && (requests != null || system != null);
    }

    VisionaryConfig config() {
        return config;
    }

    HttpCounters counters() {
        return counters;
    }

    RequestWriter requests() {
        return requests;
    }

    @Override
    public synchronized void start() {
        if (running || !enabled()) {
            return;
        }
        running = true;
        if (requests != null) {
            requests.start();
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
        if (requests != null) {
            requests.close(SHUTDOWN_TIMEOUT);
        }
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
