package io.github.styropyr0.visionary;

import static org.junit.jupiter.api.Assertions.assertArrayEquals;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.io.ByteArrayOutputStream;
import java.lang.reflect.Proxy;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Collections;
import java.util.HashMap;
import java.util.Iterator;
import java.util.List;
import java.util.Map;
import java.util.TreeMap;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.stream.Stream;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;

import jakarta.servlet.ServletOutputStream;
import jakarta.servlet.WriteListener;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.servlet.http.HttpServletResponse;

import org.junit.jupiter.api.DynamicTest;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestFactory;
import org.springframework.core.env.StandardEnvironment;

class RateLimitTest {

    private static final ObjectMapper JSON = new ObjectMapper();
    private static final Path CASES = Path.of("../go/ratelimit/testdata/cases.json");

    /** sdks/go/ratelimit/testdata/cases.json, which every SDK runs. */
    @TestFactory
    Stream<DynamicTest> sharedCases() throws Exception {
        JsonNode cases = JSON.readTree(Files.readString(CASES));
        List<DynamicTest> tests = new ArrayList<>();
        for (JsonNode c : cases) {
            tests.add(DynamicTest.dynamicTest(c.path("name").asText(), () -> runCase(c)));
        }
        assertEquals(25, tests.size());
        return tests.stream();
    }

    private static void runCase(JsonNode c) throws Exception {
        RateLimiter l = new RateLimiter(c.path("service").asText(""));
        List<String> records = new ArrayList<>();
        l.trace = (id, outcome) -> records.add(id + ":" + outcome);
        l.load(RateLimitConfig.parse(c.path("config").toString(), JSON));
        int i = 0;
        for (JsonNode s : c.path("steps")) {
            records.clear();
            RateLimiter.Request req = request(s);
            Instant now = at(s.path("t").asDouble());
            RateLimiter.State st = l.checkRequest(req, now);
            JsonNode u = s.path("user");
            RateLimiter.User user = u.isObject()
                    ? new RateLimiter.User(u.path("id").asText(""), u.path("role").asText(""))
                    : new RateLimiter.User("", "");
            l.checkUser(st, req, user, strings(s.path("attrs")), now);
            RateLimiter.Denial d = st.denied();
            JsonNode want = s.path("expect");
            String step = c.path("name").asText() + " step " + i;
            assertEquals(want.path("status").asInt(0), d == null ? 0 : d.status(), step + ": status");
            if (d != null) {
                if (want.has("rule")) {
                    assertEquals(want.get("rule").asText(), d.ruleId(), step + ": rule");
                }
                if (want.has("retry_after")) {
                    assertEquals(want.get("retry_after").asInt(), d.retryAfter(), step + ": retry after");
                }
                if (want.has("body")) {
                    assertEquals(want.get("body").asText(), new String(d.body(), StandardCharsets.UTF_8), step + ": body");
                }
                if (want.has("content_type")) {
                    assertEquals(want.get("content_type").asText(), d.contentType(), step + ": content type");
                }
            }
            List<String> wantRecords = new ArrayList<>();
            s.path("records").forEach(n -> wantRecords.add(n.asText()));
            assertEquals(wantRecords, records, step + ": records");
            i++;
        }
    }

    private static Instant at(double t) {
        long sec = (long) t;
        return Instant.ofEpochSecond(sec, Math.round((t - sec) * 1e9));
    }

    private static RateLimiter.Request request(JsonNode s) {
        Map<String, String> headers = new TreeMap<>(String.CASE_INSENSITIVE_ORDER);
        headers.putAll(strings(s.path("headers")));
        Map<String, String> query = strings(s.path("query"));
        return new RateLimiter.Request(s.path("method").asText("GET"), s.path("path").asText("/"),
                s.path("remote_ip").asText("203.0.113.1"), headers::get, query::get);
    }

    private static Map<String, String> strings(JsonNode n) {
        Map<String, String> out = new HashMap<>();
        for (Iterator<String> it = n.fieldNames(); it.hasNext();) {
            String name = it.next();
            JsonNode value = n.get(name);
            out.put(name, value.asText());
        }
        return out;
    }

    @Test
    void userCheckRunsOnce() throws Exception {
        RateLimiter l = new RateLimiter("");
        l.load(RateLimitConfig.parse(
                "{\"rules\":[{\"id\":\"u\",\"action\":\"limit\",\"key\":\"user\",\"limit\":1,\"window_s\":60}]}", JSON));
        RateLimiter.Request req = new RateLimiter.Request("GET", "/", "1.1.1.1", n -> "", n -> "");
        Instant now = at(6000);
        RateLimiter.State st = l.checkRequest(req, now);
        assertNull(l.checkUser(st, req, new RateLimiter.User("a", ""), null, now));
        assertNull(l.checkUser(st, req, new RateLimiter.User("a", ""), null, now), "a second check counted again");
        st = l.checkRequest(req, now);
        assertNotNull(l.checkUser(st, req, new RateLimiter.User("a", ""), null, now));
    }

    @Test
    void countersSurviveAReloadUnlessTheirShapeChanges() throws Exception {
        RateLimiter l = new RateLimiter("");
        String rule = "{\"rules\":[{\"id\":\"r\",\"name\":\"%s\",\"action\":\"limit\",\"limit\":1,\"window_s\":%d,"
                + "\"algorithm\":\"fixed\"}]}";
        l.load(RateLimitConfig.parse(rule.formatted("a", 60), JSON));
        RateLimiter.Request req = new RateLimiter.Request("GET", "/", "1.1.1.1", n -> "", n -> "");
        l.checkRequest(req, at(6000));
        l.load(RateLimitConfig.parse(rule.formatted("renamed", 60), JSON));
        assertNotNull(l.checkRequest(req, at(6000)).denied(), "counter lost on a rename");
        l.load(RateLimitConfig.parse(rule.formatted("renamed", 30), JSON));
        assertNull(l.checkRequest(req, at(6000)).denied(), "counter kept after the window changed");
    }

    @Test
    void recordsAggregateRedactAndOverflow() throws Exception {
        RateLimiter l = new RateLimiter("api");
        l.load(RateLimitConfig.parse("{\"rules\":[{\"id\":\"k\",\"name\":\"Keys\",\"action\":\"limit\","
                + "\"key\":\"header:X-Api-Key\",\"limit\":0,\"window_s\":60}]}", JSON));
        RateLimiter.Request req = new RateLimiter.Request("post", "/v1/x", "1.1.1.1",
                n -> n.equals("x-api-key") ? "secret-value" : "", n -> "");
        for (int i = 0; i < 3; i++) {
            l.checkRequest(req, at(6000));
        }
        List<RateLimiter.Event> ev = l.drain();
        assertEquals(1, ev.size());
        assertEquals(3, ev.get(0).hits);
        assertEquals("POST", ev.get(0).method);
        assertEquals("Keys", ev.get(0).ruleName);
        assertTrue(ev.get(0).keyValue.matches("sha256:[0-9a-f]{12}"), ev.get(0).keyValue);
        assertTrue(l.drain().isEmpty());

        l.load(RateLimitConfig.parse("{\"rules\":[{\"id\":\"b\",\"action\":\"block\"}]}", JSON));
        for (int i = 0; i < RateLimiter.MAX_AGGREGATES + 5; i++) {
            l.checkRequest(new RateLimiter.Request("GET", "/", "10.0." + i / 250 + "." + i % 250, n -> "", n -> ""),
                    at(1));
        }
        ev = l.drain();
        assertEquals(RateLimiter.MAX_AGGREGATES + 1, ev.size());
        RateLimiter.Event last = ev.get(ev.size() - 1);
        assertEquals("(other)", last.keyValue);
        assertEquals("", last.ip);
        assertEquals(5, last.hits);
    }

    @Test
    void ipsParseWithoutLookups() {
        assertNull(RateLimiter.Ip.parsePrefix("example.com"));
        assertNull(RateLimiter.Ip.parsePrefix("01.2.3.4"));
        assertNull(RateLimiter.Ip.parsePrefix("10.0.0.0/33"));
        assertNull(RateLimiter.Ip.parsePrefix("1:2:3:4:5:6:7:8::"));
        assertNotNull(RateLimiter.Ip.parsePrefix("::"));
        RateLimiter.Ip.Prefix mapped = RateLimiter.Ip.parsePrefix("::ffff:10.0.0.0/104");
        assertEquals(8, mapped.bits());
        assertArrayEquals(new byte[] {10, 0, 0, 0}, mapped.addr());
        assertTrue(RateLimiter.Ip.inPrefixes(List.of(mapped), "10.200.0.1"));
        assertTrue(RateLimiter.Ip.inPrefixes(List.of(RateLimiter.Ip.parsePrefix("fe80::/10")), "fe80::1%eth0"));
        assertFalse(RateLimiter.Ip.inPrefixes(List.of(RateLimiter.Ip.parsePrefix("::/0")), "1.2.3.4"));
    }

    @Test
    void globs() {
        assertTrue(RateLimiter.glob("/a*", "/a"));
        assertTrue(RateLimiter.glob("/a*", "/a/b/c"));
        assertTrue(RateLimiter.glob("*", "/"));
        assertTrue(RateLimiter.glob("/a/*/c", "/a/b/c"));
        assertFalse(RateLimiter.glob("/a/*/c", "/a/b/d"));
        assertFalse(RateLimiter.glob("/a", "/a/"));
        assertTrue(RateLimiter.glob("*.json", "/x/y.json"));
        assertTrue(RateLimiter.glob("/a**b", "/ab"));
    }

    @Test
    void parseKeepsGoodRules() throws Exception {
        RateLimitConfig c = RateLimitConfig.parse(
                "{\"rules\":[{\"id\":\"a\",\"action\":\"block\",\"limit\":\"x\"},{\"id\":\"b\",\"action\":\"block\"}]}", JSON);
        assertEquals(1, c.rules().size());
        assertEquals("b", c.rules().get(0).id());
        try {
            RateLimitConfig.parse("{\"rules\":", JSON);
            throw new AssertionError("bad JSON accepted");
        } catch (com.fasterxml.jackson.core.JsonProcessingException expected) {
            // ok
        }
    }

    // ---- polling and the filter ------------------------------------------------------------

    private static final String RULES = "{\"rules\":["
            + "{\"id\":\"ip1\",\"name\":\"Per IP\",\"action\":\"limit\",\"paths\":[\"/api/*\"],\"limit\":1,"
            + "\"window_s\":60,\"algorithm\":\"fixed\"},"
            + "{\"id\":\"plan\",\"action\":\"limit\",\"key\":\"user\",\"limit\":5,\"window_s\":60,"
            + "\"limit_by\":\"attr:plan\",\"limits\":{\"free\":0}}]}";

    @Test
    void configIsReadOnlyWhenTheVersionChanges() {
        SettingsStore store = new SettingsStore();
        RateLimits rl = new RateLimits(store, origin(), List.of(), JSON);
        rl.poll();
        assertFalse(rl.limiter.active());
        assertEquals(0, store.configReads.get(), "no version row: the config is not read");

        store.version = "v1";
        store.config = RULES;
        rl.poll();
        rl.poll();
        assertTrue(rl.limiter.active());
        assertEquals(1, store.configReads.get());

        store.version = "v2";
        store.config = "{not json";
        rl.poll();
        rl.poll();
        assertTrue(rl.limiter.active(), "invalid JSON keeps the rules");
        assertEquals(3, store.configReads.get(), "an invalid version is retried at each poll");

        store.fail = true;
        rl.poll();
        assertTrue(rl.limiter.active(), "a failed read keeps the rules");

        store.fail = false;
        store.version = null;
        rl.poll();
        assertFalse(rl.limiter.active(), "the version row gone clears the rules");
    }

    @Test
    void filterAnswersLimitedRequestsAndStillLogsThem() throws Exception {
        SettingsStore store = new SettingsStore();
        store.version = "v1";
        store.config = RULES;
        RateLimits rl = new RateLimits(store, origin(), List.of(), JSON);
        rl.poll();
        VisionaryConfig cfg = VisionaryConfig.from(new StandardEnvironment());
        RequestWriter writer = new RequestWriter(cfg, origin(), store, JSON, null);
        writer.start();
        VisionaryFilter filter = new VisionaryFilter(new HttpCounters(), writer, null, null, null, rl, cfg);

        AtomicInteger calls = new AtomicInteger();
        FakeResponse first = new FakeResponse();
        filter.doFilterInternal(request("/api/orders", "198.51.100.7"), first.proxy(), (req, res) -> calls.incrementAndGet());
        assertEquals(1, calls.get());
        assertEquals(200, first.status);

        FakeResponse second = new FakeResponse();
        filter.doFilterInternal(request("/api/orders", "198.51.100.7"), second.proxy(), (req, res) -> calls.incrementAndGet());
        assertEquals(1, calls.get(), "the handler ran for a limited request");
        assertEquals(429, second.status);
        assertEquals("application/json", second.contentType);
        int retry = Integer.parseInt(second.headers.get("Retry-After"));
        assertTrue(retry >= 1 && retry <= 60, "Retry-After " + retry);
        assertEquals("{\"error\":{\"code\":\"rate_limited\",\"key\":\"ip1\",\"message\":\"Too many requests\"}}",
                second.body.toString(StandardCharsets.UTF_8));

        writer.close(Duration.ofSeconds(10));
        assertEquals(2, store.requests.size());
        Object[] logged = store.requests.get(1);
        assertEquals(429L, ((Number) logged[Table.API_LOGS.indexOf("status")]).longValue());
        assertEquals("rate_limited", logged[Table.API_LOGS.indexOf("error_code")]);

        rl.flush();
        assertEquals(1, store.events.size());
        Object[] e = store.events.get(0);
        assertEquals(13, Table.RATE_LIMIT_EVENTS.columns.size());
        assertEquals(List.of("created_at", "service", "instance", "rule_id", "rule_name", "outcome", "key_value", "ip",
                "country", "method", "path", "user_id", "hits"), Table.RATE_LIMIT_EVENTS.columns);
        assertTrue(((String) e[0]).matches("\\d{4}-\\d\\d-\\d\\dT\\d\\d:\\d\\d:\\d\\d\\.\\d{3}Z"), (String) e[0]);
        assertArrayEquals(new Object[] {"api", "host-1", "ip1", "Per IP", "limited", "198.51.100.7", "198.51.100.7",
                null, "GET", "/api/orders", null, 1L}, java.util.Arrays.copyOfRange(e, 1, 13));
    }

    @Test
    void userCheckWithAttributes() throws Exception {
        SettingsStore store = new SettingsStore();
        store.version = "v1";
        store.config = RULES;
        RateLimits rl = new RateLimits(store, origin(), List.of(), JSON);
        rl.poll();
        VisionaryFilter filter = new VisionaryFilter(new HttpCounters(), null, null, null, null, rl,
                VisionaryConfig.from(new StandardEnvironment()));

        List<Boolean> outcomes = new ArrayList<>();
        FakeResponse free = new FakeResponse();
        filter.doFilterInternal(request("/account", "203.0.113.5"), free.proxy(), (req, res) -> {
            HttpServletRequest r = (HttpServletRequest) req;
            Visionary.setUser(r, "u7");
            outcomes.add(Visionary.checkRateLimit(r, (HttpServletResponse) res, Map.of("plan", "free")));
            outcomes.add(Visionary.checkRateLimit(r, (HttpServletResponse) res, Map.of("plan", "pro")));
        });
        assertEquals(List.of(true, true), outcomes, "later calls repeat the first outcome");
        assertEquals(429, free.status);
        assertEquals(1, free.writes, "the answer is written once");
        assertEquals("60", free.headers.get("Retry-After"));

        outcomes.clear();
        FakeResponse pro = new FakeResponse();
        filter.doFilterInternal(request("/account", "203.0.113.5"), pro.proxy(), (req, res) -> {
            Visionary.setUser((HttpServletRequest) req, "u8");
            outcomes.add(Visionary.checkRateLimit((HttpServletRequest) req, (HttpServletResponse) res,
                    Map.of("plan", "pro")));
            outcomes.add(Visionary.checkRateLimit((HttpServletRequest) req, (HttpServletResponse) res));
        });
        assertEquals(List.of(false, false), outcomes);
        assertEquals(200, pro.status);

        assertFalse(Visionary.checkRateLimit(request("/account", "203.0.113.5"), new FakeResponse().proxy()),
                "outside the filter it does nothing");

        rl.flush();
        assertEquals(1, store.events.size());
        assertEquals("u7", store.events.get(0)[Table.RATE_LIMIT_EVENTS.indexOf("user_id")]);
        assertEquals("u7", store.events.get(0)[Table.RATE_LIMIT_EVENTS.indexOf("key_value")]);
    }

    private static Origin origin() {
        return new Origin("api", null, "host-1", "Java 17", null);
    }

    private static HttpServletRequest request(String path, String ip) {
        Map<String, Object> attrs = new HashMap<>();
        Map<String, String> headers = new TreeMap<>(String.CASE_INSENSITIVE_ORDER);
        return (HttpServletRequest) Proxy.newProxyInstance(RateLimitTest.class.getClassLoader(),
                new Class<?>[] {HttpServletRequest.class}, (p, method, args) -> switch (method.getName()) {
                    case "getHeader" -> headers.get((String) args[0]);
                    case "getHeaderNames", "getAttributeNames" -> Collections.emptyEnumeration();
                    case "getAttribute" -> attrs.get((String) args[0]);
                    case "setAttribute" -> attrs.put((String) args[0], args[1]);
                    case "removeAttribute" -> attrs.remove((String) args[0]);
                    case "getRequestURI" -> path;
                    case "getMethod" -> "GET";
                    case "getRemoteAddr" -> ip;
                    case "getContentLengthLong" -> -1L;
                    default -> defaultValue(method.getReturnType());
                });
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

    /** Records what the filter and the user check answer. */
    private static final class FakeResponse {
        int status = 200;
        String contentType;
        int writes;
        final Map<String, String> headers = new HashMap<>();
        final ByteArrayOutputStream body = new ByteArrayOutputStream();
        private final ServletOutputStream out = new ServletOutputStream() {
            @Override
            public void write(int b) {
                body.write(b);
            }

            @Override
            public void write(byte[] b, int off, int len) {
                writes++;
                body.write(b, off, len);
            }

            @Override
            public boolean isReady() {
                return true;
            }

            @Override
            public void setWriteListener(WriteListener listener) {
            }
        };

        HttpServletResponse proxy() {
            return (HttpServletResponse) Proxy.newProxyInstance(RateLimitTest.class.getClassLoader(),
                    new Class<?>[] {HttpServletResponse.class}, (p, method, args) -> switch (method.getName()) {
                        case "setStatus" -> {
                            status = (Integer) args[0];
                            yield null;
                        }
                        case "getStatus" -> status;
                        case "setContentType" -> {
                            contentType = (String) args[0];
                            yield null;
                        }
                        case "getContentType" -> contentType;
                        case "setHeader" -> {
                            headers.put((String) args[0], (String) args[1]);
                            yield null;
                        }
                        case "getHeader" -> headers.get((String) args[0]);
                        case "getOutputStream" -> out;
                        default -> defaultValue(method.getReturnType());
                    });
        }
    }

    /** Answers the two settings reads and keeps what is written. */
    private static final class SettingsStore implements Store {
        volatile String version;
        volatile String config;
        volatile boolean fail;
        final AtomicInteger configReads = new AtomicInteger();
        final List<Object[]> requests = Collections.synchronizedList(new ArrayList<>());
        final List<Object[]> events = Collections.synchronizedList(new ArrayList<>());

        @Override
        public void createSchema(Table table) {
        }

        @Override
        public void insert(Map<Table, List<Object[]>> rows) {
            requests.addAll(rows.getOrDefault(Table.API_LOGS, List.of()));
            events.addAll(rows.getOrDefault(Table.RATE_LIMIT_EVENTS, List.of()));
        }

        @Override
        public List<String[]> select(String sql) throws Exception {
            if (fail) {
                throw new java.sql.SQLException("database unavailable");
            }
            if (sql.equals(RateLimits.VERSION_QUERY)) {
                return version == null ? List.of() : List.<String[]>of(new String[] {version});
            }
            if (sql.equals(RateLimits.CONFIG_QUERY)) {
                configReads.incrementAndGet();
                return config == null ? List.of() : List.<String[]>of(new String[] {config});
            }
            throw new AssertionError("unexpected query " + sql);
        }

        @Override
        public long purgeChunk(Table table, String cutoff) {
            return 0;
        }

        @Override
        public String describe() {
            return "settings";
        }

        @Override
        public void close() {
        }
    }
}
