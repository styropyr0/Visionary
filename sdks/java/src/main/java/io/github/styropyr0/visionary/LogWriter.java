package io.github.styropyr0.visionary;

import java.time.Duration;
import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.List;
import java.util.Set;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.locks.Condition;
import java.util.concurrent.locks.ReentrantLock;

import org.apache.commons.logging.Log;
import org.apache.commons.logging.LogFactory;

/**
 * The background writer for {@code app_logs}, separate from the requests writer
 * so a flood of lines never crowds out request rows: a bounded queue of 10000
 * lines that never blocks the logging thread, flushed every 50 lines or 3
 * seconds with the same retries.
 */
final class LogWriter {

    private static final Log log = LogFactory.getLog(LogWriter.class);

    static final int QUEUE_SIZE = 10_000;
    private static final int ATTEMPTS = 3;
    /** The secret patterns use lazy and alternating groups; a roomy stack keeps long lines from failing. */
    private static final long STACK_SIZE = 8L << 20;
    private static final String HOST = VisionaryConfig.hostname();

    private final Store store;
    private final Origin origin;
    private final Set<String> secretKeys;
    private final FrameNamer namer;

    private final ReentrantLock lock = new ReentrantLock();
    private final Condition wake = lock.newCondition();
    private final ArrayDeque<LogRecord> queue = new ArrayDeque<>();
    private long dropped;
    private boolean closing;

    private final Thread thread;
    private boolean schemaReady;

    /** @param namer names {@code code_file} like CPU profile frames; null keeps the bare file name */
    LogWriter(Store store, Origin origin, Set<String> secretKeys, FrameNamer namer) {
        this.store = store;
        this.origin = origin;
        this.secretKeys = secretKeys;
        this.namer = namer;
        this.thread = new Thread(null, this::run, "visionary-logs", STACK_SIZE);
        this.thread.setDaemon(true);
    }

    void start() {
        thread.start();
    }

    /** Enqueues without blocking; when the queue is full the line is dropped and counted. */
    void enqueue(LogRecord r) {
        lock.lock();
        try {
            if (closing || queue.size() >= QUEUE_SIZE) {
                dropped++;
                return;
            }
            queue.add(r);
            if (queue.size() >= RequestWriter.BATCH_SIZE) {
                wake.signal();
            }
        } finally {
            lock.unlock();
        }
    }

    /** Drains the queue and waits for the final flush, at most {@code timeout}. */
    void close(Duration timeout) {
        lock.lock();
        try {
            closing = true;
            wake.signal();
        } finally {
            lock.unlock();
        }
        if (!thread.isAlive()) {
            return;
        }
        try {
            thread.join(timeout.toMillis());
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
        if (thread.isAlive()) {
            log.warn("visionary: app log flush timed out");
        }
    }

    private void run() {
        long nextFlush = System.nanoTime() + RequestWriter.FLUSH_INTERVAL.toNanos();
        while (true) {
            List<LogRecord> batch;
            long droppedNow;
            boolean last;
            lock.lock();
            try {
                while (!closing && queue.size() < RequestWriter.BATCH_SIZE) {
                    long wait = nextFlush - System.nanoTime();
                    if (wait <= 0) {
                        break;
                    }
                    wake.awaitNanos(wait);
                }
                batch = new ArrayList<>(Math.min(queue.size(), RequestWriter.BATCH_SIZE));
                while (batch.size() < RequestWriter.BATCH_SIZE && !queue.isEmpty()) {
                    batch.add(queue.poll());
                }
                droppedNow = dropped;
                dropped = 0;
                last = closing && queue.isEmpty();
            } catch (InterruptedException e) {
                return;
            } finally {
                lock.unlock();
            }
            if (droppedNow > 0) {
                log.warn("visionary: app log queue full; " + droppedNow + " lines dropped");
            }
            if (!batch.isEmpty()) {
                write(batch);
            }
            if (last) {
                return;
            }
            if (batch.size() < RequestWriter.BATCH_SIZE) {
                nextFlush = System.nanoTime() + RequestWriter.FLUSH_INTERVAL.toNanos();
            }
        }
    }

    private void write(List<LogRecord> batch) {
        List<Object[]> rows = new ArrayList<>(batch.size());
        for (LogRecord r : batch) {
            try {
                rows.add(row(r));
            } catch (RuntimeException | StackOverflowError e) {
                // A line that cannot be scrubbed is not stored at all rather than stored unredacted.
            }
        }
        if (rows.isEmpty()) {
            return;
        }
        Exception last = null;
        for (int attempt = 1; attempt <= ATTEMPTS; attempt++) {
            try {
                if (!schemaReady) {
                    store.createSchema(Table.APP_LOGS);
                    schemaReady = true;
                }
                store.insert(Table.APP_LOGS, rows);
                return;
            } catch (Exception e) {
                last = e;
                if (attempt < ATTEMPTS && !sleep(Duration.ofSeconds(attempt))) {
                    break;
                }
            }
        }
        log.error("visionary: app log write failed; " + rows.size() + " lines dropped: " + last);
    }

    /** The row in {@link Table#APP_LOGS} column order. */
    Object[] row(LogRecord r) {
        String message = Values.capBytes(LogRules.scrub(r.message), LogRules.MAX_MESSAGE);
        if (message == null) {
            message = "";
        }
        String attrs = LogRules.encodeAttrs(r.attrs, secretKeys);
        String file = null;
        String func = null;
        if (r.className != null) {
            file = namer != null ? namer.file(r.className, r.fileName) : r.fileName;
            func = r.method == null ? r.className : r.className + "." + r.method;
        }
        return new Object[] {
            Values.timestamp(r.at),
            origin.service(),
            origin.instance(),
            origin.version(),
            "java",
            Values.cap(HOST, 255),
            null,
            LogRules.name(r.sev),
            (long) r.sev,
            Values.cap(r.logger, 255),
            message,
            attrs,
            Values.cap(r.requestId, 255),
            Values.cap(r.traceId, 64),
            Values.cap(r.spanId, 32),
            Values.cap(r.userId, 255),
            Values.cap(r.sessionId, 255),
            Values.cap(file, 512),
            r.className != null && r.line > 0 ? (Object) (long) r.line : null,
            Values.cap(func, 255),
            (long) (LogRules.bytes(message) + LogRules.bytes(attrs)),
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
