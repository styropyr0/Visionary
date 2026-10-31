package io.github.styropyr0.visionary;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.lang.reflect.Proxy;
import java.net.URI;
import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Collections;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.atomic.AtomicReference;

import jakarta.servlet.FilterChain;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.servlet.http.HttpServletResponse;

import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Test;
import org.springframework.core.env.StandardEnvironment;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpMethod;
import org.springframework.http.HttpRequest;
import org.springframework.http.HttpStatusCode;
import org.springframework.http.client.ClientHttpResponse;

class TraceTest {

    private static final String TRACE = "4bf92f3577b34da6a3ce929d0e0e4736";
    private static final String PARENT = "00f067aa0ba902b7";

    private Telemetry telemetry;

    @AfterEach
    void off() {
        if (telemetry != null) {
            telemetry.deactivate();
        }
        TraceContext.restore(null);
        RequestScope.restore(null);
        LogScope.restore(null);
    }

    @Test
    void parsesTraceparent() {
        TraceContext tc = TraceContext.parse("00-" + TRACE + "-" + PARENT + "-01");
        assertEquals(TRACE, tc.traceId);
        assertEquals(PARENT, tc.spanId);
        assertEquals("01", tc.flags);
        assertNotNull(TraceContext.parse("01-" + TRACE + "-" + PARENT + "-00-extra"), "a later version may add fields");
        for (String bad : new String[] {
            null, "", "00-xyz-1-1",
            "00-00000000000000000000000000000000-" + PARENT + "-01",
            "00-" + TRACE + "-0000000000000000-01",
            "ff-" + TRACE + "-" + PARENT + "-01",
            "00-" + TRACE.toUpperCase() + "-" + PARENT + "-01",
            "00-" + TRACE + "-" + PARENT + "-01-extra",
        }) {
            assertNull(TraceContext.parse(bad), bad);
        }
    }

    @Test
    void newIdsAreLowerHex() {
        String t = TraceContext.newTraceId();
        String s = TraceContext.newSpanId();
        assertTrue(t.matches("[0-9a-f]{32}"), t);
        assertTrue(s.matches("[0-9a-f]{16}"), s);
        assertNotEquals(t, TraceContext.newTraceId());
    }

    @Test
    void aRequestContinuesTheCallersTraceOrStartsOne() {
        TraceContext.Server in = TraceContext.server("00-" + TRACE + "-" + PARENT + "-00", "k=v");
        assertEquals(TRACE, in.context().traceId);
        assertEquals(PARENT, in.parentSpanId());
        assertNotEquals(PARENT, in.context().spanId);
        assertEquals("00", in.context().flags);
        assertEquals("k=v", in.context().state);

        TraceContext.Server fresh = TraceContext.server("garbage", "k=v");
        assertNull(fresh.parentSpanId());
        assertEquals("01", fresh.context().flags);
        assertNull(fresh.context().state);
        assertEquals(32, fresh.context().traceId.length());
    }

    @Test
    void loggedRequestRecordsItsTraceAndItsCallsNestUnderIt() throws Exception {
        CapturingStore store = new CapturingStore();
        RequestWriter writer = new RequestWriter(VisionaryConfig.from(new StandardEnvironment()),
                new Origin("api", null, "host-1", "Java 17", null), store,
                new com.fasterxml.jackson.databind.ObjectMapper(), null);
        telemetry = new Telemetry(true, true, 100, 100, writer::enqueue);
        telemetry.activate();
        writer.start();
        VisionaryFilter filter = new VisionaryFilter(new HttpCounters(), writer, telemetry, null, null,
                VisionaryConfig.from(new StandardEnvironment()));

        AtomicReference<TraceContext> server = new AtomicReference<>();
        AtomicReference<String> custom = new AtomicReference<>();
        HttpHeaders sent = new HttpHeaders();
        FilterChain chain = (req, res) -> {
            server.set(TraceContext.current());
            try (VisionarySpan span = Visionary.span("custom", "build report")) {
                custom.set(span.context.spanId);
                Visionary.span("db", "SELECT orders").close();
                sent.putAll(call(new HttpHeaders()));
            }
            assertEquals(server.get(), TraceContext.current(), "closing a span restores its parent");
        };
        filter.doFilterInternal(request(Map.of("traceparent", "00-" + TRACE + "-" + PARENT + "-01",
                "tracestate", "k=v")), response(), chain);
        writer.close(Duration.ofSeconds(10));
        assertNull(TraceContext.current());

        assertEquals(1, store.requests.size());
        Object[] req = store.requests.get(0);
        int n = Table.API_LOGS.columns.size();
        assertEquals(TRACE, req[n - 3]);
        assertEquals(server.get().spanId, req[n - 2]);
        assertEquals(PARENT, req[n - 1]);

        Map<String, Object[]> spans = new HashMap<>();
        for (Object[] row : store.spans) {
            spans.put((String) row[5], row);
        }
        int m = Table.SPANS.columns.size();
        Object[] build = spans.get("build report");
        assertEquals(TRACE, build[m - 3]);
        assertEquals(custom.get(), build[m - 2]);
        assertEquals(server.get().spanId, build[m - 1]);
        assertEquals(custom.get(), spans.get("SELECT orders")[m - 1]);
        Object[] http = spans.get("GET stock");
        assertEquals(custom.get(), http[m - 1]);
        assertEquals("00-" + TRACE + "-" + http[m - 2] + "-01", sent.getFirst("traceparent"));
        assertEquals("k=v", sent.getFirst("tracestate"));
    }

    @Test
    void anUnloggedRequestStillCarriesTheTraceOn() throws Exception {
        VisionaryFilter filter = new VisionaryFilter(new HttpCounters(), null, null, null, null,
                VisionaryConfig.from(new StandardEnvironment()));
        AtomicReference<TraceContext> server = new AtomicReference<>();
        List<HttpHeaders> sent = new ArrayList<>();
        filter.doFilterInternal(request(Map.of()), response(), (req, res) -> {
            server.set(TraceContext.current());
            sent.add(call(new HttpHeaders()));
            HttpHeaders own = new HttpHeaders();
            own.set("traceparent", "00-" + TRACE + "-" + PARENT + "-01");
            sent.add(call(own));
        });
        assertNotNull(server.get());
        assertEquals(server.get().traceparent(), sent.get(0).getFirst("traceparent"));
        assertEquals("00-" + TRACE + "-" + PARENT + "-01", sent.get(1).getFirst("traceparent"),
                "a traceparent the application set is left alone");
    }

    @Test
    void aSpanOutsideAnyTraceStartsOne() {
        telemetry = new Telemetry(true, true, 0, 100, q -> { });
        telemetry.activate();
        VisionarySpan span = Visionary.span("custom", "nightly");
        assertNotNull(span.context);
        assertNull(span.parentSpanId);
        assertEquals(span.context, TraceContext.current());
        span.close();
        assertNull(TraceContext.current());
    }

    @Test
    void backgroundWorkKeepsTheTrace() throws Exception {
        List<VisionarySpan> stored = Collections.synchronizedList(new ArrayList<>());
        telemetry = new Telemetry(true, true, 0, 100, q -> {
            if (q instanceof SpanEntry e) {
                stored.add(e.span());
            }
        });
        telemetry.activate();
        TraceContext request = new TraceContext(TRACE, PARENT, "01", null);
        TraceContext.enter(request);
        AtomicReference<TraceContext> inside = new AtomicReference<>();
        AtomicReference<TraceContext> plain = new AtomicReference<>();
        Runnable named = Visionary.wrap("send email", () -> {
            inside.set(TraceContext.current());
            Visionary.span("http", "POST mail").close();
        });
        Runnable unnamed = Visionary.wrap(() -> plain.set(TraceContext.current()));
        TraceContext.restore(null);

        ExecutorService pool = Executors.newSingleThreadExecutor();
        try {
            pool.submit(named).get();
            pool.submit(unnamed).get();
            pool.submit(() -> assertNull(TraceContext.current(), "the worker's context is restored")).get();
        } finally {
            pool.shutdown();
        }
        assertEquals(request, plain.get());
        VisionarySpan bg = stored.stream().filter(s -> s.kind.equals("background")).findFirst().orElseThrow();
        VisionarySpan mail = stored.stream().filter(s -> s.kind.equals("http")).findFirst().orElseThrow();
        assertEquals(PARENT, bg.parentSpanId);
        assertEquals(TRACE, bg.context.traceId);
        assertEquals(bg.context, inside.get());
        assertEquals(bg.context.spanId, mail.parentSpanId);
    }

    @Test
    void logLinesTakeTheCurrentSpan() {
        List<LogRecord> out = new ArrayList<>();
        AppLogs logs = new AppLogs(null, "api", LogRules.INFO, out::add);
        TraceContext.enter(new TraceContext(TRACE, PARENT, "01", null));
        logs.capture(new LogRecord(Instant.now(), LogRules.WARN, "x", "plain"));
        LogRecord own = new LogRecord(Instant.now(), LogRules.WARN, "x", "own");
        own.traceId = "t";
        logs.capture(own);
        logs.stop(Duration.ofSeconds(1));
        assertEquals(TRACE, out.get(0).traceId);
        assertEquals(PARENT, out.get(0).spanId);
        assertEquals("t", out.get(1).traceId);
        assertNull(out.get(1).spanId);
    }

    @Test
    void addedColumnsAndTraceIndexes() {
        assertEquals(List.of("ALTER TABLE spans ADD COLUMN trace_id TEXT",
                "ALTER TABLE spans ADD COLUMN span_id TEXT", "ALTER TABLE spans ADD COLUMN parent_span_id TEXT"),
                Dialect.SQLITE.missingColumns(Table.SPANS, Table.SPANS.columns.subList(0, 13)));
        assertTrue(Table.API_LOGS.addedColumns.contains("parent_span_id"));
        assertEquals("CREATE INDEX IF NOT EXISTS idx_api_logs_trace_id ON api_logs (trace_id)",
                Dialect.POSTGRES.traceIndex("api_logs"));
        assertEquals("CREATE INDEX idx_spans_trace_id ON spans (trace_id)", Dialect.MYSQL.traceIndex("spans"));
    }

    /** One call through the interceptor, returns the headers it sent */
    private static HttpHeaders call(HttpHeaders headers) throws java.io.IOException {
        HttpRequest req = new HttpRequest() {
            @Override
            public HttpMethod getMethod() {
                return HttpMethod.GET;
            }

            @Override
            public URI getURI() {
                return URI.create("http://stock/items/42");
            }

            @Override
            public HttpHeaders getHeaders() {
                return headers;
            }

            @Override
            public Map<String, Object> getAttributes() {
                return new HashMap<>();
            }
        };
        new HttpClientSpans().intercept(req, new byte[0], (r, body) -> proxy(ClientHttpResponse.class,
                Map.of("getStatusCode", HttpStatusCode.valueOf(200))));
        return headers;
    }

    private static HttpServletRequest request(Map<String, String> headers) {
        Map<String, Object> attrs = new HashMap<>();
        return (HttpServletRequest) Proxy.newProxyInstance(TraceTest.class.getClassLoader(),
                new Class<?>[] {HttpServletRequest.class}, (p, method, args) -> switch (method.getName()) {
                    case "getHeader" -> headers.get(((String) args[0]).toLowerCase());
                    case "getHeaderNames" -> Collections.enumeration(headers.keySet());
                    case "getHeaders" -> Collections.enumeration(List.of(headers.get((String) args[0])));
                    case "getAttribute" -> attrs.get((String) args[0]);
                    case "setAttribute" -> attrs.put((String) args[0], args[1]);
                    case "getRequestURI" -> "/orders";
                    case "getMethod" -> "GET";
                    default -> defaultValue(method.getReturnType());
                });
    }

    private static HttpServletResponse response() {
        return proxy(HttpServletResponse.class, Map.of("getStatus", 200));
    }

    @SuppressWarnings("unchecked")
    private static <T> T proxy(Class<T> type, Map<String, Object> values) {
        return (T) Proxy.newProxyInstance(TraceTest.class.getClassLoader(), new Class<?>[] {type},
                (p, method, args) -> values.containsKey(method.getName()) ? values.get(method.getName())
                        : defaultValue(method.getReturnType()));
    }

    private static Object defaultValue(Class<?> type) {
        if (type == boolean.class) {
            return false;
        }
        if (type == int.class) {
            return 0;
        }
        if (type == long.class) {
            return 0L;
        }
        return null;
    }

    private static final class CapturingStore implements Store {
        final List<Object[]> requests = new ArrayList<>();
        final List<Object[]> spans = new ArrayList<>();

        @Override
        public void createSchema(Table table) {
        }

        @Override
        public synchronized void insert(Map<Table, List<Object[]>> rows) {
            requests.addAll(rows.getOrDefault(Table.API_LOGS, List.of()));
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
