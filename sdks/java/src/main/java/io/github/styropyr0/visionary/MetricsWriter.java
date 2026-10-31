package io.github.styropyr0.visionary;

import java.time.Duration;
import java.time.Instant;
import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicLong;
import java.util.concurrent.locks.Condition;
import java.util.concurrent.locks.ReentrantLock;

import org.apache.commons.logging.Log;
import org.apache.commons.logging.LogFactory;

/** Runs on its own thread so metrics never crowd out request rows */
final class MetricsWriter implements VisionaryWriterStats.Source {

    private static final Log log = LogFactory.getLog(MetricsWriter.class);

    static final int QUEUE_SIZE = 20_000;
    private static final int ATTEMPTS = 3;

    private final Store store;
    private final Origin origin;
    private final Metrics registry;
    private final Duration interval;
    private final int batchSize;

    private final ReentrantLock lock = new ReentrantLock();
    private final Condition wake = lock.newCondition();
    private final ArrayDeque<Object[]> queue = new ArrayDeque<>();
    private long dropped;
    private final AtomicLong lost = new AtomicLong();
    private final AtomicLong written = new AtomicLong();
    private boolean closing;

    private final Thread thread;
    private boolean schemaReady;

    MetricsWriter(Store store, Origin origin, Metrics registry, Duration interval, int maxSeries) {
        this.store = store;
        this.origin = origin;
        this.registry = registry;
        this.batchSize = store.batchSize();
        this.interval = interval.isZero() || interval.isNegative() ? Duration.ofSeconds(15) : interval;
        if (maxSeries > 0) {
            registry.maxSeries = maxSeries;
        }
        this.thread = new Thread(this::run, "visionary-metrics");
        this.thread.setDaemon(true);
    }

    void start() {
        VisionaryWriterStats.add(this);
        thread.start();
    }

    void close(Duration timeout) {
        lock.lock();
        try {
            closing = true;
            wake.signal();
        } finally {
            lock.unlock();
        }
        if (!thread.isAlive()) {
            VisionaryWriterStats.remove(this);
            return;
        }
        try {
            thread.join(timeout.toMillis());
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
        if (thread.isAlive()) {
            log.warn("visionary: metrics flush timed out");
        }
        VisionaryWriterStats.remove(this);
    }

    @Override
    public int queued() {
        lock.lock();
        try {
            return queue.size();
        } finally {
            lock.unlock();
        }
    }

    @Override
    public long written() {
        return written.get();
    }

    @Override
    public long dropped() {
        return lost.get();
    }

    private void run() {
        long next = System.nanoTime() + interval.toNanos();
        while (true) {
            boolean last;
            lock.lock();
            try {
                while (!closing) {
                    long wait = next - System.nanoTime();
                    if (wait <= 0) {
                        break;
                    }
                    wake.awaitNanos(wait);
                }
                last = closing;
            } catch (InterruptedException e) {
                return;
            } finally {
                lock.unlock();
            }
            next = System.nanoTime() + interval.toNanos();
            flush();
            if (last) {
                return;
            }
        }
    }

    void flush() {
        for (Map.Entry<String, Long> e : registry.takeDropped().entrySet()) {
            log.warn("visionary: metric series dropped over the limit (metric " + e.getKey() + ", dropped "
                    + e.getValue() + ", limit " + registry.maxSeries + ")");
        }
        for (Metrics.Row r : registry.collect(Instant.now())) {
            enqueue(row(r));
        }
        while (true) {
            List<Object[]> batch = new ArrayList<>(batchSize);
            long droppedNow;
            lock.lock();
            try {
                while (batch.size() < batchSize && !queue.isEmpty()) {
                    batch.add(queue.poll());
                }
                droppedNow = dropped;
                dropped = 0;
            } finally {
                lock.unlock();
            }
            if (droppedNow > 0) {
                log.warn("visionary: metrics queue full; " + droppedNow + " rows dropped");
            }
            if (batch.isEmpty()) {
                return;
            }
            write(batch);
        }
    }

    private void enqueue(Object[] row) {
        lock.lock();
        try {
            if (queue.size() >= QUEUE_SIZE) {
                dropped++;
                lost.incrementAndGet();
                return;
            }
            queue.add(row);
        } finally {
            lock.unlock();
        }
    }

    private void write(List<Object[]> rows) {
        Exception last = null;
        for (int attempt = 1; attempt <= ATTEMPTS; attempt++) {
            try {
                if (!schemaReady) {
                    store.createSchema(Table.METRICS);
                    schemaReady = true;
                }
                store.insert(Table.METRICS, rows);
                written.addAndGet(rows.size());
                return;
            } catch (Exception e) {
                last = e;
                if (attempt < ATTEMPTS && !sleep(Duration.ofSeconds(attempt))) {
                    break;
                }
            }
        }
        lost.addAndGet(rows.size());
        log.error("visionary: metrics write failed; " + rows.size() + " rows dropped: " + last);
    }

    Object[] row(Metrics.Row r) {
        return new Object[] {
            Values.timestamp(r.at()),
            origin.service(),
            origin.instance(),
            r.name(),
            r.kind(),
            r.unit(),
            r.labels(),
            r.series(),
            r.value(),
            r.count(),
            r.sum(),
            r.min(),
            r.max(),
            r.buckets(),
        };
    }

    private static boolean sleep(Duration d) {
        try {
            TimeUnit.MILLISECONDS.sleep(d.toMillis());
            return true;
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            return false;
        }
    }
}
