package io.github.styropyr0.visionary;

import java.time.Duration;
import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.List;
import java.util.Set;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicLong;
import java.util.concurrent.locks.Condition;
import java.util.concurrent.locks.ReentrantLock;

import org.apache.commons.logging.Log;
import org.apache.commons.logging.LogFactory;

/** Separate from the requests writer so a flood of log lines can't crowd out request rows. */
final class LogWriter implements VisionaryWriterStats.Source {

    private static final Log log = LogFactory.getLog(LogWriter.class);

    static final int QUEUE_SIZE = 10_000;
    private static final int ATTEMPTS = 3;
    /** The secret regexes recurse deeply on long lines, so give the thread a big stack. */
    private static final long STACK_SIZE = 8L << 20;
    private static final String HOST = VisionaryConfig.hostname();

    private final Store store;
    private final Origin origin;
    private final Set<String> secretKeys;
    private final FrameNamer namer;
    private final int batchSize;
    private final WriterFlushes flushes;

    private final ReentrantLock lock = new ReentrantLock();
    private final Condition wake = lock.newCondition();
    private final ArrayDeque<LogRecord> queue = new ArrayDeque<>();
    private long dropped;
    private final AtomicLong lost = new AtomicLong();
    private final AtomicLong written = new AtomicLong();
    private boolean closing;

    private final Thread thread;
    private boolean schemaReady;

    /** @param namer null keeps the bare file name */
    LogWriter(Store store, Origin origin, Set<String> secretKeys, FrameNamer namer) {
        this.store = store;
        this.origin = origin;
        this.secretKeys = secretKeys;
        this.namer = namer;
        this.batchSize = store == null ? Store.D1_BATCH_SIZE : store.batchSize();
        this.flushes = new WriterFlushes(store, "visionary-logs", STACK_SIZE);
        this.thread = new Thread(null, this::run, "visionary-logs", STACK_SIZE);
        this.thread.setDaemon(true);
    }

    void start() {
        VisionaryWriterStats.add(this);
        thread.start();
    }

    /** Never blocks; when the queue is full the line is dropped and counted. */
    void enqueue(LogRecord r) {
        lock.lock();
        try {
            if (closing || queue.size() >= QUEUE_SIZE) {
                dropped++;
                lost.incrementAndGet();
                return;
            }
            queue.add(r);
            if (queue.size() >= batchSize) {
                wake.signal();
            }
        } finally {
            lock.unlock();
        }
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
            log.warn("visionary: app log flush timed out");
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
        long nextFlush = System.nanoTime() + RequestWriter.FLUSH_INTERVAL.toNanos();
        while (true) {
            List<LogRecord> batch;
            long droppedNow;
            boolean last;
            lock.lock();
            try {
                while (!closing && queue.size() < batchSize) {
                    long wait = nextFlush - System.nanoTime();
                    if (wait <= 0) {
                        break;
                    }
                    wake.awaitNanos(wait);
                }
                batch = new ArrayList<>(Math.min(queue.size(), batchSize));
                while (batch.size() < batchSize && !queue.isEmpty()) {
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
                flushes.run(() -> write(batch));
            }
            if (last) {
                flushes.finish(Duration.ofSeconds(60));
                return;
            }
            if (batch.size() < batchSize) {
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
                // Better to drop a line we can't scrub than store it unredacted.
                lost.incrementAndGet();
            }
        }
        if (rows.isEmpty()) {
            return;
        }
        Exception last = null;
        for (int attempt = 1; attempt <= ATTEMPTS; attempt++) {
            try {
                ensureSchema();
                store.insert(Table.APP_LOGS, rows);
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
        log.error("visionary: app log write failed; " + rows.size() + " lines dropped: " + last);
    }

    private synchronized void ensureSchema() throws Exception {
        if (!schemaReady) {
            store.createSchema(Table.APP_LOGS);
            schemaReady = true;
        }
    }

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
