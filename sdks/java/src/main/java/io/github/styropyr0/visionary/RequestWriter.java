package io.github.styropyr0.visionary;

import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.time.Instant;
import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.LinkedHashMap;
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
 * The single background writer for {@code api_logs}, {@code spans}, {@code errors}
 * and {@code releases}: a bounded queue that never blocks the request thread,
 * flushed every 50 entries or 3 seconds. A request and the spans it stores count
 * as one entry; each batch is written as one unit.
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
    private final Origin origin;
    private final boolean logRequests;
    private final Redactor redactor;
    private final ErrorFields errorFields;
    private final VisionaryUserLookup userLookup;
    private final boolean bodies;

    private final ReentrantLock lock = new ReentrantLock();
    private final Condition wake = lock.newCondition();
    private final ArrayDeque<Queued> queue = new ArrayDeque<>();
    private long dropped;
    private boolean closing;

    private final Thread thread;
    private boolean requestsReady;
    private boolean telemetryReady;

    RequestWriter(VisionaryConfig cfg, Origin origin, Store store, ObjectMapper json, VisionaryUserLookup userLookup) {
        this.store = store;
        this.origin = origin;
        this.logRequests = cfg.logRequests();
        this.redactor = new Redactor(cfg.redactHeaders(), cfg.redactFields(), cfg.redactRequestFields(), json);
        this.errorFields = new ErrorFields(json, cfg.errorCodeField(), cfg.errorKeyField());
        this.userLookup = userLookup;
        this.bodies = cfg.logBodies();
        this.thread = new Thread(null, this::run, "visionary-requests", STACK_SIZE);
        this.thread.setDaemon(true);
    }

    /** Starts the worker; the deploy marker goes first when the release is known. */
    void start() {
        if (origin.version() != null) {
            enqueue(new ReleaseEntry(Instant.now()));
        }
        thread.start();
    }

    void log(RequestEntry e) {
        enqueue(e);
    }

    /** Enqueues without blocking; when the queue is full the entry is dropped and counted. */
    void enqueue(Queued e) {
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
            log.warn("visionary: create tables failed (retrying on the next write): " + e);
        }
        long nextFlush = System.nanoTime() + FLUSH_INTERVAL.toNanos();
        while (true) {
            List<Queued> batch;
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

    private void write(List<Queued> batch) {
        Map<Table, List<Object[]>> rows = new LinkedHashMap<>();
        for (Table t : List.of(Table.API_LOGS, Table.SPANS, Table.ERRORS, Table.RELEASES)) {
            rows.put(t, new ArrayList<>());
        }
        try {
            Map<String, VisionaryUser> users = lookupUsers(batch);
            for (Queued q : batch) {
                if (q instanceof RequestEntry e) {
                    rows.get(Table.API_LOGS).add(row(e, users));
                    if (keepSpans(e)) {
                        for (VisionarySpan span : e.telemetry().spans()) {
                            rows.get(Table.SPANS).add(spanRow(span, e.requestId(), e.telemetry().startNanos()));
                        }
                    }
                } else if (q instanceof SpanEntry e) {
                    RequestScope scope = e.span().scope;
                    rows.get(Table.SPANS).add(scope == null
                            ? spanRow(e.span(), null, 0)
                            : spanRow(e.span(), scope.requestId, scope.startNanos));
                } else if (q instanceof ErrorEntry e) {
                    rows.get(Table.ERRORS).add(errorRow(e));
                } else if (q instanceof ReleaseEntry e) {
                    rows.get(Table.RELEASES).add(releaseRow(e));
                }
            }
        } catch (RuntimeException e) {
            log.error("visionary: could not prepare " + batch.size() + " rows; dropped", e);
            return;
        }
        rows.values().removeIf(List::isEmpty);
        Exception last = null;
        for (int attempt = 1; attempt <= ATTEMPTS; attempt++) {
            try {
                ensureSchema();
                store.insert(rows);
                return;
            } catch (Exception e) {
                last = e;
                if (attempt < ATTEMPTS && !sleep(Duration.ofSeconds(attempt))) {
                    break;
                }
            }
        }
        log.error("visionary: batch write failed; " + batch.size() + " entries dropped: " + last);
    }

    private void ensureSchema() throws Exception {
        if (logRequests && !requestsReady) {
            store.createSchema(Table.API_LOGS);
            requestsReady = true;
        }
        if (!telemetryReady) {
            // One script creates spans, errors and releases.
            store.createSchema(Table.SPANS);
            telemetryReady = true;
        }
    }

    /** Span rows are kept for every request that recorded them. */
    private boolean keepSpans(RequestEntry e) {
        return e.telemetry() != null;
    }

    /** Resolves each distinct user missing a handle or role once per batch. */
    private Map<String, VisionaryUser> lookupUsers(List<Queued> batch) {
        Map<String, VisionaryUser> out = new HashMap<>();
        if (userLookup == null) {
            return out;
        }
        for (Queued q : batch) {
            if (!(q instanceof RequestEntry e)) {
                continue;
            }
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

        RequestScope.Finished t = e.telemetry();
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
            origin.service(),
            origin.version(),
            t == null ? null : t.dbCount(),
            t == null ? null : Values.round(t.dbMs(), 3),
            t == null ? null : t.httpCount(),
            t == null ? null : Values.round(t.httpMs(), 3),
        };
    }

    private Object[] spanRow(VisionarySpan s, String requestId, long requestStartNanos) {
        String name = s.name;
        String detail = s.detail;
        if (s.sql != null) {
            detail = Values.cap(SqlText.sanitize(s.sql), 2000);
            if (name == null) {
                name = SqlText.name(detail == null ? "" : detail);
            }
        }
        long rows = s.rowCount;
        return new Object[] {
            Values.timestamp(s.startedAt),
            requestId,
            origin.service(),
            origin.instance(),
            s.kind,
            Values.cap(name == null || name.isEmpty() ? s.kind : name, 255),
            Values.cap(s.target, 255),
            detail,
            requestId == null ? null : Values.round((s.startNanos - requestStartNanos) / 1_000_000.0, 3),
            Values.round(s.durationMs, 3),
            s.failed ? "error" : "ok",
            Values.cap(s.error, 500),
            rows < 0 ? null : rows,
        };
    }

    private Object[] errorRow(ErrorEntry e) {
        return new Object[] {
            Values.timestamp(e.createdAt()),
            origin.service(),
            origin.instance(),
            origin.version(),
            e.requestId(),
            Values.emptyToNull(e.method()),
            Values.cap(e.route(), 255),
            Values.emptyToNull(e.path()),
            Values.cap(e.userId(), 255),
            Values.cap(e.type(), 255),
            Errors.message(e.error()),
            scrub(() -> Errors.stack(e.error())),
            e.fingerprint(),
            e.handled() ? 1L : 0L,
            origin.runtime(),
        };
    }

    private Object[] releaseRow(ReleaseEntry e) {
        return new Object[] {
            Values.timestamp(e.createdAt()),
            origin.service(),
            origin.instance(),
            origin.version(),
            "java",
            origin.sdkVersion(),
            origin.runtime(),
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
