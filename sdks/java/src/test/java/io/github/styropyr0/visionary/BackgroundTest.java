package io.github.styropyr0.visionary;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertSame;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.Callable;
import java.util.concurrent.CompletionException;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.TimeUnit;

import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Test;

/** Work a request starts and does not wait for: late spans and the background helpers. */
class BackgroundTest {

    private static final double THRESHOLD_MS = 100;
    private static final int MAX = 3;

    private final List<Queued> out = new ArrayList<>();
    private final Telemetry telemetry = new Telemetry(true, true, THRESHOLD_MS, MAX, this::record);

    private synchronized void record(Queued q) {
        out.add(q);
    }

    private synchronized List<VisionarySpan> stored() {
        List<VisionarySpan> spans = new ArrayList<>();
        for (Queued q : out) {
            if (q instanceof SpanEntry e) {
                spans.add(e.span());
            }
        }
        return spans;
    }

    @AfterEach
    void off() {
        telemetry.deactivate();
        RequestScope.restore(null);
    }

    private RequestScope request() {
        telemetry.activate();
        return new RequestScope("req-1", System.nanoTime(), MAX);
    }

    /** A span of the given duration made for {@code scope}, closed now. */
    private VisionarySpan span(RequestScope scope, String kind, String name, double ms) {
        VisionarySpan s = new VisionarySpan(telemetry, scope, kind, name);
        s.durationMs = ms;
        telemetry.finished(s);
        return s;
    }

    private static double offsetMs(VisionarySpan s) {
        return (s.startNanos - s.scope.startNanos) / 1_000_000.0;
    }

    @Test
    void lateSpansKeepTheRequestAndAreStoredUpToTheCapThenFromTheThreshold() throws Exception {
        RequestScope scope = request();
        span(scope, "db", "SELECT users", 2);
        span(scope, "background", "warm cache", 5);
        RequestScope.Finished done = scope.finish();
        assertEquals(1, done.dbCount());
        assertEquals(2.0, done.dbMs(), 1e-9);
        assertEquals(2, done.spans().size(), "background spans finished in time stay with the request");
        assertTrue(stored().isEmpty());

        Thread.sleep(5);
        // The first MAX late spans are stored whatever their duration...
        for (int i = 0; i < MAX; i++) {
            span(scope, "db", "INSERT emails", 1);
        }
        // ...after that only slow ones.
        span(scope, "db", "INSERT emails", 1);
        span(scope, "http", "POST api.mail.test", THRESHOLD_MS);

        List<VisionarySpan> late = stored();
        assertEquals(MAX + 1, late.size());
        for (VisionarySpan s : late) {
            assertSame(scope, s.scope, "late spans keep their request");
            assertTrue(offsetMs(s) >= 5, "offset from the request start: " + offsetMs(s));
        }
        assertEquals("http", late.get(MAX).kind);
        // Totals were taken when the request was logged.
        assertEquals(1, done.dbCount());
        assertEquals(0, done.httpCount());
    }

    @Test
    void lateSpanRowsCarryRequestIdAndOffset() throws Exception {
        CapturingStore store = new CapturingStore();
        RequestWriter writer = new RequestWriter(VisionaryConfig.from(new org.springframework.core.env.StandardEnvironment()),
                new Origin("api", null, "host-1", "Java 17", null), store, new com.fasterxml.jackson.databind.ObjectMapper(),
                null);
        Telemetry t = new Telemetry(true, true, THRESHOLD_MS, MAX, writer::enqueue);
        writer.start();
        try {
            t.activate();
            RequestScope scope = new RequestScope("req-rows", System.nanoTime(), MAX);
            scope.finish();
            Thread.sleep(20);
            RequestScope previous = RequestScope.enter(scope);
            try {
                Visionary.span("db", "SELECT users").close();
            } finally {
                RequestScope.restore(previous);
            }
        } finally {
            t.deactivate();
            writer.close(java.time.Duration.ofSeconds(10));
        }
        assertEquals(1, store.spans.size());
        Object[] row = store.spans.get(0);
        assertEquals("req-rows", row[1]);
        assertEquals("db", row[4]);
        assertTrue(((Number) row[8]).doubleValue() >= 20, "offset_ms " + row[8]);
    }

    @Test
    void spansOutsideARequestNeedTheThreshold() {
        telemetry.activate();
        span(null, "db", "SELECT 1", 1);
        span(null, "db", "SELECT 2", THRESHOLD_MS);
        List<VisionarySpan> s = stored();
        assertEquals(1, s.size());
        assertEquals("SELECT 2", s.get(0).name);
    }

    @Test
    void backgroundRecordsANamedSpanAfterTheRequest() throws Exception {
        RequestScope scope = request();
        RequestScope.enter(scope);
        var future = Visionary.background("send welcome email", () -> {
            sleep(150);
            try (VisionarySpan db = Visionary.span("db", "INSERT emails")) {
                db.target("sqlite/test");
            }
        });
        RequestScope.restore(null);
        RequestScope.Finished done = scope.finish();
        future.get(5, TimeUnit.SECONDS);

        assertEquals(0, done.dbCount());
        List<VisionarySpan> late = stored();
        assertEquals(2, late.size(), "the background span and the call inside it");
        VisionarySpan db = late.get(0);
        VisionarySpan bg = late.get(1);
        assertEquals("db", db.kind);
        assertEquals("background", bg.kind);
        assertEquals("send welcome email", bg.name);
        assertSame(scope, bg.scope);
        assertSame(scope, db.scope);
        assertTrue(!bg.failed);
        assertTrue(bg.durationMs >= 150, "duration " + bg.durationMs);
        assertTrue(offsetMs(db) >= offsetMs(bg) + 150, "the call starts inside the background span");
        assertTrue(Thread.currentThread() != null && RequestScope.current() == null);
    }

    @Test
    void backgroundRecordsTheErrorAndFailsTheFuture() throws Exception {
        RequestScope scope = request();
        RequestScope.enter(scope);
        var future = Visionary.background("charge card", () -> {
            throw new IllegalStateException("card declined");
        });
        RequestScope.restore(null);
        CompletionException e = assertThrows(CompletionException.class, future::join);
        assertEquals("card declined", e.getCause().getMessage());

        // Finished before the request was logged: it stays with the request, uncounted.
        RequestScope.Finished done = scope.finish();
        assertEquals(1, done.spans().size());
        VisionarySpan bg = done.spans().get(0);
        assertEquals("background", bg.kind);
        assertEquals("charge card", bg.name);
        assertTrue(bg.failed);
        assertTrue(bg.error.contains("card declined"), bg.error);
        assertEquals(0, done.dbCount() + done.httpCount());
    }

    @Test
    void wrapCarriesTheRequestToTheAppsExecutor() throws Exception {
        ExecutorService pool = Executors.newSingleThreadExecutor();
        try {
            RequestScope scope = request();
            RequestScope.enter(scope);
            Runnable ok = Visionary.wrap("resize avatar", () -> Visionary.span("http", "PUT s3").close());
            Callable<Integer> fails = Visionary.wrap("rebuild index", () -> {
                throw new java.io.IOException("disk full");
            });
            RequestScope.restore(null);
            sleep(10);
            pool.submit(ok).get(5, TimeUnit.SECONDS);
            var failed = pool.submit(fails);
            Exception e = assertThrows(java.util.concurrent.ExecutionException.class, failed::get);
            assertEquals("disk full", e.getCause().getMessage());
            // the executor thread does not keep the request
            assertNull(pool.submit(RequestScope::current).get());

            RequestScope.Finished done = scope.finish();
            assertEquals(1, done.httpCount(), "the http call inside counts; the background span does not");
            List<VisionarySpan> spans = done.spans();
            assertEquals(3, spans.size());
            VisionarySpan resize = spans.stream().filter(s -> s.name.equals("resize avatar")).findFirst().orElseThrow();
            VisionarySpan index = spans.stream().filter(s -> s.name.equals("rebuild index")).findFirst().orElseThrow();
            assertEquals("background", resize.kind);
            assertTrue(!resize.failed);
            assertTrue(offsetMs(resize) >= 10, "offset from when the work started: " + offsetMs(resize));
            assertTrue(index.failed);
            assertTrue(index.error.contains("disk full"), index.error);
        } finally {
            pool.shutdownNow();
        }
    }

    @Test
    void outsideARequestTheSpanIsStoredFromTheThreshold() throws Exception {
        telemetry.activate();
        Visionary.background("quick", () -> { }).get(5, TimeUnit.SECONDS);
        Visionary.background("slow", () -> sleep(110)).get(5, TimeUnit.SECONDS);
        List<VisionarySpan> s = stored();
        assertEquals(1, s.size());
        assertEquals("slow", s.get(0).name);
        assertNull(s.get(0).scope);
    }

    @Test
    void helpersRunTheTaskWhenVisionaryIsOff() throws Exception {
        int[] ran = {0};
        Visionary.background("off", () -> { ran[0]++; }).get(5, TimeUnit.SECONDS);
        Visionary.wrap("off", () -> { ran[0]++; }).run();
        assertEquals(2, ran[0]);
        assertTrue(stored().isEmpty());
    }

    private static void sleep(long ms) {
        try {
            Thread.sleep(ms);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
    }

    private static final class CapturingStore implements Store {
        final List<Object[]> spans = new ArrayList<>();

        @Override
        public void createSchema(Table table) {
        }

        @Override
        public synchronized void insert(java.util.Map<Table, List<Object[]>> rows) {
            spans.addAll(rows.getOrDefault(Table.SPANS, List.of()));
        }

        @Override
        public List<String[]> select(String sql) {
            return List.of();
        }

        @Override
        public long purgeChunk(Table table, String cutoff) {
            return 0;
        }

        @Override
        public String describe() {
            return "capturing";
        }

        @Override
        public void close() {
        }
    }
}
