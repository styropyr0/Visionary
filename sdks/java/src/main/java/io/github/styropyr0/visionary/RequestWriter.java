package io.github.styropyr0.visionary;

import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.locks.Condition;
import java.util.concurrent.locks.ReentrantLock;
import java.util.function.Supplier;

import com.fasterxml.jackson.databind.ObjectMapper;

import org.apache.commons.logging.Log;
import org.apache.commons.logging.LogFactory;

/**
 * The single background writer for {@code api_logs}: a bounded queue that never
 * blocks the request thread, flushed every 50 entries or 3 seconds.
 */
final class RequestWriter {

    private static final Log log = LogFactory.getLog(RequestWriter.class);

    static final int QUEUE_SIZE = 5000;
    static final int BATCH_SIZE = 50;
    static final Duration FLUSH_INTERVAL = Duration.ofSeconds(3);
    private static final int ATTEMPTS = 3;
    /** Redaction regexes recurse; a roomy stack keeps pathological bodies from failing. */
    private static final long STACK_SIZE = 8L << 20;

    private final Store store;
    private final Redactor redactor;
    private final ErrorFields errorFields;
    private final VisionaryUserLookup userLookup;
    private final boolean bodies;

    private final ReentrantLock lock = new ReentrantLock();
    private final Condition wake = lock.newCondition();
    private final ArrayDeque<RequestEntry> queue = new ArrayDeque<>();
    private long dropped;
    private boolean closing;

    private final Thread thread;
    private boolean schemaReady;

    RequestWriter(VisionaryConfig cfg, Store store, ObjectMapper json, VisionaryUserLookup userLookup) {
        this.store = store;
        this.redactor = new Redactor(cfg.redactHeaders(), cfg.redactFields(), cfg.redactRequestFields(), json);
        this.errorFields = new ErrorFields(json, cfg.errorCodeField(), cfg.errorKeyField());
        this.userLookup = userLookup;
        this.bodies = cfg.logBodies();
        this.thread = new Thread(null, this::run, "visionary-requests", STACK_SIZE);
        this.thread.setDaemon(true);
    }

    void start() {
        thread.start();
    }

    /** Enqueues without blocking; when the queue is full the entry is dropped and counted. */
    void log(RequestEntry e) {
        lock.lock();
        try {
            if (closing || queue.size() >= QUEUE_SIZE) {
                dropped++;
                return;
            }
            queue.add(e);
            if (queue.size() >= BATCH_SIZE) {
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
        try {
            thread.join(timeout.toMillis());
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
        if (thread.isAlive()) {
            log.warn("visionary: request log flush timed out");
        }
    }

    private void run() {
        try {
            ensureSchema();
        } catch (Exception e) {
            log.warn("visionary: create api_logs failed (retrying on the next write): " + e);
        }
        long nextFlush = System.nanoTime() + FLUSH_INTERVAL.toNanos();
        while (true) {
            List<RequestEntry> batch;
            long droppedNow;
            boolean last;
            lock.lock();
            try {
                while (!closing && queue.size() < BATCH_SIZE) {
                    long wait = nextFlush - System.nanoTime();
                    if (wait <= 0) {
                        break;
                    }
                    wake.awaitNanos(wait);
                }
                batch = new ArrayList<>(Math.min(queue.size(), BATCH_SIZE));
                while (batch.size() < BATCH_SIZE && !queue.isEmpty()) {
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
                log.warn("visionary: request queue full; " + droppedNow + " entries dropped");
            }
            if (!batch.isEmpty()) {
                write(batch);
            }
            if (last) {
                return;
            }
            if (batch.size() < BATCH_SIZE) {
                nextFlush = System.nanoTime() + FLUSH_INTERVAL.toNanos();
            }
        }
    }

    private void write(List<RequestEntry> batch) {
        List<Object[]> rows;
        try {
            Map<String, VisionaryUser> users = lookupUsers(batch);
            rows = new ArrayList<>(batch.size());
            for (RequestEntry e : batch) {
                rows.add(row(e, users));
            }
        } catch (RuntimeException e) {
            log.error("visionary: could not prepare " + batch.size() + " request rows; dropped", e);
            return;
        }
        Exception last = null;
        for (int attempt = 1; attempt <= ATTEMPTS; attempt++) {
            try {
                ensureSchema();
                store.insert(Table.API_LOGS, rows);
                return;
            } catch (Exception e) {
                last = e;
                if (attempt < ATTEMPTS && !sleep(Duration.ofSeconds(attempt))) {
                    break;
                }
            }
        }
        log.error("visionary: request batch write failed; " + batch.size() + " entries dropped: " + last);
    }

    private void ensureSchema() throws Exception {
        if (!schemaReady) {
            store.createSchema(Table.API_LOGS);
            schemaReady = true;
        }
    }

    /** Resolves each distinct user missing a handle or role once per batch. */
    private Map<String, VisionaryUser> lookupUsers(List<RequestEntry> batch) {
        Map<String, VisionaryUser> out = new HashMap<>();
        if (userLookup == null) {
            return out;
        }
        for (RequestEntry e : batch) {
            VisionaryUser u = e.user();
            if (u == null || isEmpty(u.id()) || (!isEmpty(u.handle()) && !isEmpty(u.role()))
                    || out.containsKey(u.id())) {
                continue;
            }
            VisionaryUser found = null;
            try {
                found = userLookup.lookup(u.id());
            } catch (RuntimeException ex) {
                log.warn("visionary: user lookup failed for " + u.id() + ": " + ex);
            }
            out.put(u.id(), found);
        }
        return out;
    }

    private Object[] row(RequestEntry e, Map<String, VisionaryUser> users) {
        VisionaryUser u = e.user();
        String userId = u == null ? null : u.id();
        String handle = u == null ? null : u.handle();
        String role = u == null ? null : u.role();
        VisionaryUser found = userId == null ? null : users.get(userId);
        if (found != null) {
            handle = isEmpty(handle) ? found.handle() : handle;
            role = isEmpty(role) ? found.role() : role;
        }

        String responseText = text(e.responseBody());
        ErrorFields.Result err = e.status() >= 400 ? errorFields.extract(responseText) : ErrorFields.Result.NONE;
        String requestBody = null;
        String responseBody = null;
        if (bodies) {
            requestBody = scrub(() -> redactor.requestBody(text(e.requestBody()), e.requestContentType()));
            responseBody = scrub(() -> redactor.responseBody(responseText));
        }

        return new Object[] {
            e.requestId(),
            Values.timestamp(e.startedAt()),
            e.method(),
            e.path(),
            Values.emptyToNull(e.route()),
            Values.emptyToNull(e.query()),
            (long) e.status(),
            Values.round(e.durationMs(), 3),
            Values.emptyToNull(e.ip()),
            Values.emptyToNull(e.userAgent()),
            Values.emptyToNull(userId),
            Values.emptyToNull(handle),
            Values.emptyToNull(role),
            redactor.headers(e.requestHeaders()),
            Values.emptyToNull(e.requestContentType()),
            requestBody,
            e.requestTruncated() ? 1L : 0L,
            e.requestSize(),
            Values.emptyToNull(e.responseContentType()),
            responseBody,
            e.responseTruncated() ? 1L : 0L,
            e.responseSize(),
            err.code(),
            err.key(),
        };
    }

    /** A body that cannot be scrubbed is not stored at all rather than stored unredacted. */
    private static String scrub(Supplier<String> redaction) {
        try {
            return redaction.get();
        } catch (RuntimeException | StackOverflowError e) {
            return null;
        }
    }

    private static String text(byte[] body) {
        return body == null || body.length == 0 ? null : new String(body, StandardCharsets.UTF_8);
    }

    private static boolean isEmpty(String s) {
        return s == null || s.isEmpty();
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
