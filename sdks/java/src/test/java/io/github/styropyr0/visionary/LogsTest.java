package io.github.styropyr0.visionary;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Comparator;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.stream.Collectors;

import ch.qos.logback.classic.Level;
import ch.qos.logback.classic.Logger;
import ch.qos.logback.classic.LoggerContext;
import com.fasterxml.jackson.databind.ObjectMapper;

import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Test;
import org.slf4j.LoggerFactory;
import org.slf4j.MDC;

class LogsTest {

    private final ObjectMapper json = new ObjectMapper();
    private final List<LogRecord> out = new ArrayList<>();
    private final AppLogs logs = new AppLogs(null, "api", LogRules.INFO, this::record);
    // Each line gets a later time, so sorting by time gives the logged order.
    private Instant clock = Instant.parse("2026-10-28T09:30:00.000Z");

    private synchronized void record(LogRecord r) {
        out.add(r);
    }

    @AfterEach
    void reset() {
        if (appender != null) {
            ((LoggerContext) LoggerFactory.getILoggerFactory()).getLogger(Logger.ROOT_LOGGER_NAME)
                    .detachAppender(appender);
            appender.stop();
        }
        logs.stop(Duration.ofSeconds(1));
        LogScope.restore(null);
        RequestScope.restore(null);
        MDC.clear();
    }

    private void settings(String... nameValue) {
        List<String[]> rows = new ArrayList<>();
        for (int i = 0; i < nameValue.length; i += 2) {
            rows.add(new String[] {nameValue[i], nameValue[i + 1]});
        }
        logs.use(LogSettings.parse(rows, LogRules.INFO, json));
    }

    private LogRecord line(int sev, String message) {
        clock = clock.plusMillis(1);
        return new LogRecord(clock, sev, "com.example.Orders", message);
    }

    private void log(int sev, String message) {
        logs.capture(line(sev, message));
    }

    private void threeLines() {
        log(LogRules.DEBUG, "debug line");
        log(LogRules.INFO, "info line");
        log(LogRules.WARN, "warn line");
    }

    private LogScope serve(String route, int status, double tookMs, String user, Runnable body) {
        LogScope scope = new LogScope("req-1", "4bf92f3577b34da6a3ce929d0e0e4736", () -> user);
        LogScope previous = LogScope.enter(scope);
        try {
            body.run();
        } finally {
            LogScope.restore(previous);
        }
        logs.finish(scope, route, status >= 500, tookMs);
        return scope;
    }

    private synchronized String taken() {
        String s = out.stream().sorted(Comparator.comparing(r -> r.at)).map(r -> r.message)
                .collect(Collectors.joining(","));
        out.clear();
        return s;
    }

    @Test
    void parsesSettingsAndFloors() throws Exception {
        String until = Values.timestamp(Instant.now().plusSeconds(3600));
        String past = Values.timestamp(Instant.now().minusSeconds(3600));
        String overrides = json.writeValueAsString(List.of(
                Map.of("route", "/checkout", "level", "debug", "until", until),
                Map.of("user", "42", "level", "trace"),
                Map.of("route", "/health", "level", "warn"),
                Map.of("route", "/admin/*", "level", "debug"),
                Map.of("route", "/old", "level", "debug", "until", past),
                Map.of("service", "other", "level", "debug"),
                Map.of("level", "nonsense"),
                Map.of("route", "/bad", "level", "debug", "until", "tomorrow")));
        LogSettings s = LogSettings.parse(List.of(
                new String[] {"logs.level", "warning"}, new String[] {"logs.overrides", overrides},
                new String[] {"logs.rate", "-1"}, new String[] {"logs.slow_ms", "250"},
                new String[] {"logs.buffer", "nope"}, new String[] {"logs.enabled", "false"}), 0, json);
        assertEquals(LogRules.WARN, s.level());
        assertEquals(6000, s.rate());
        assertEquals(250, s.slowMs());
        assertTrue(s.buffer());
        assertFalse(s.enabled());
        assertEquals(6, s.overrides().size());

        Instant now = Instant.now();
        assertEquals(LogRules.DEBUG, s.floor("api", "/checkout", "", now));
        assertEquals(LogRules.WARN, s.floor("api", "/health", "", now));
        assertEquals(LogRules.DEBUG, s.floor("api", "/admin/users", "", now));
        assertEquals(LogRules.WARN, s.floor("api", "/old", "", now), "expired override");
        assertEquals(LogRules.TRACE, s.floor("api", "/any", "42", now));
        assertEquals(LogRules.TRACE, s.floor("api", "/health", "42", now), "a user beats a route");
        assertEquals(LogRules.WARN, s.floor("api", "/any", "", now));
        assertEquals(LogRules.DEBUG, s.floor("other", "", "", now), "service override");
        assertEquals(LogRules.WARN, s.holdBelow("api", now));
    }

    @Test
    void defaultsBeforeAnyRead() {
        LogSettings d = LogSettings.defaults(0);
        assertTrue(d.enabled());
        assertEquals(LogRules.INFO, d.level());
        assertEquals(LogRules.DEBUG, d.bufferLevel());
        assertEquals(1000, d.slowMs());
        assertEquals(6000, d.rate());
        assertEquals(LogRules.DEBUG, LogSettings.defaults(LogRules.parseLevel("debug")).level());
    }

    @Test
    void dropRules() throws Exception {
        String drops = json.writeValueAsString(List.of(Map.of("contains", "healthcheck"),
                Map.of("logger", "noisy", "level", "info"), Map.of("service", "web", "level", "debug"), Map.of()));
        LogSettings s = LogSettings.parse(List.<String[]>of(new String[] {"logs.drop", drops}), LogRules.INFO, json);
        assertEquals(3, s.drops().size());
        assertTrue(s.dropped("api", "", "healthcheck ok", LogRules.ERROR));
        assertTrue(s.dropped("api", "noisy", "x", LogRules.INFO));
        assertFalse(s.dropped("api", "noisy", "x", LogRules.WARN));
        assertTrue(s.dropped("web", "", "x", LogRules.DEBUG));
        assertFalse(s.dropped("web", "", "x", LogRules.INFO));
        assertFalse(s.dropped("api", "", "x", LogRules.TRACE));
    }

    @Test
    void debugIsKeptOnlyForFailedRequests() {
        serve("/ok", 200, 1, null, this::threeLines);
        assertEquals("info line,warn line", taken());

        serve("/fail", 500, 1, null, this::threeLines);
        List<LogRecord> got = new ArrayList<>(out);
        assertEquals("debug line,info line,warn line", taken());
        for (LogRecord r : got) {
            assertEquals("req-1", r.requestId);
            assertEquals("4bf92f3577b34da6a3ce929d0e0e4736", r.traceId);
        }

        // An error captured in the request counts as failed.
        serve("/handled", 200, 1, null, () -> {
            threeLines();
            Visionary.captureException(new IllegalStateException("card declined"));
        });
        assertEquals("debug line,info line,warn line", taken());
    }

    @Test
    void debugIsKeptForSlowRequests() {
        settings("logs.slow_ms", "20");
        serve("/slow", 200, 30, null, this::threeLines);
        assertEquals("debug line,info line,warn line", taken());

        settings("logs.slow_ms", "20", "logs.buffer", "false");
        serve("/slow", 500, 30, null, this::threeLines);
        assertEquals("info line,warn line", taken());
    }

    @Test
    void overridesByRouteAndUser() throws Exception {
        String until = Values.timestamp(Instant.now().plusSeconds(3600));
        String past = Values.timestamp(Instant.now().minusSeconds(3600));
        settings("logs.overrides", json.writeValueAsString(List.of(
                Map.of("route", "/checkout", "level", "debug", "until", until),
                Map.of("user", "42", "level", "trace"),
                Map.of("route", "/health", "level", "warn"),
                Map.of("route", "/old", "level", "debug", "until", past),
                Map.of("service", "other", "level", "debug"))));

        serve("/checkout", 200, 1, null, this::threeLines);
        assertEquals("debug line,info line,warn line", taken(), "route override");
        serve("/health", 200, 1, null, this::threeLines);
        assertEquals("warn line", taken(), "quieter route");
        serve("/old", 200, 1, null, this::threeLines);
        assertEquals("info line,warn line", taken(), "expired override");

        serve("/any", 200, 1, "42", () -> {
            log(LogRules.TRACE, "trace line");
            threeLines();
        });
        List<LogRecord> got = new ArrayList<>(out);
        assertEquals("trace line,debug line,info line,warn line", taken(), "user override");
        assertTrue(got.stream().allMatch(r -> "42".equals(r.userId)), "user on every line");
    }

    @Test
    void heldLinesAreCapped() {
        serve("/fail", 500, 1, null, () -> {
            for (int i = 0; i < AppLogs.MAX_HELD + 20; i++) {
                log(LogRules.DEBUG, "d" + i);
            }
        });
        String[] kept = taken().split(",");
        assertEquals(AppLogs.MAX_HELD, kept.length);
        assertEquals("d20", kept[0], "the oldest are dropped");
    }

    @Test
    void linesAfterTheRequestEndedAreDecidedAtOnce() throws Exception {
        settings("logs.overrides", json.writeValueAsString(List.of(Map.of("route", "/jobs", "level", "debug"))));
        LogScope scope = serve("/jobs", 200, 1, "7", () -> { });
        LogScope previous = LogScope.enter(scope);
        try {
            log(LogRules.DEBUG, "late debug");
            log(LogRules.TRACE, "late trace");
        } finally {
            LogScope.restore(previous);
        }
        List<LogRecord> got = new ArrayList<>(out);
        assertEquals("late debug", taken());
        assertEquals("7", got.get(0).userId);
        assertEquals("req-1", got.get(0).requestId);
    }

    @Test
    void outsideRequestsTheLevelApplies() {
        settings("logs.level", "warn");
        log(LogRules.INFO, "startup");
        log(LogRules.ERROR, "boom");
        assertEquals("boom", taken());
    }

    @Test
    void wrappedWorkJoinsTheRequest() {
        Runnable[] task = new Runnable[1];
        serve("/fail", 500, 1, null, () -> {
            task[0] = Visionary.wrap(() -> log(LogRules.DEBUG, "in task"));
            Thread t = new Thread(task[0]);
            t.start();
            try {
                t.join();
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
            }
        });
        assertEquals("in task", taken());
    }

    @Test
    void dropsAndTheRateCap() throws Exception {
        settings("logs.drop", json.writeValueAsString(List.of(Map.of("contains", "healthcheck"),
                Map.of("logger", "noisy", "level", "info"))), "logs.rate", "3");
        log(LogRules.INFO, "healthcheck ok");
        LogRecord chatty = line(LogRules.INFO, "chatty");
        chatty.logger = "noisy";
        logs.capture(chatty);
        LogRecord kept = line(LogRules.WARN, "kept");
        kept.logger = "noisy";
        logs.capture(kept);
        for (int i = 0; i < 5; i++) {
            log(LogRules.INFO, "flood");
        }
        log(LogRules.ERROR, "still kept");
        assertEquals("kept,flood,flood,still kept", taken());

        // The next minute starts with one line about the drops.
        clock = clock.plusSeconds(60);
        log(LogRules.INFO, "next minute");
        List<LogRecord> got = new ArrayList<>(out);
        taken();
        assertEquals(2, got.size());
        LogRecord warn = got.get(0);
        assertEquals(LogRules.WARN, warn.sev);
        assertEquals("visionary", warn.logger);
        assertEquals("visionary: 3 log lines dropped over the cap of 3 a minute (Settings → Logs)", warn.message);
    }

    @Test
    void theSdksOwnMessagesAreSkipped() {
        log(LogRules.ERROR, "visionary: write failed");
        assertEquals("", taken());
    }

    private VisionaryLogbackAppender appender;

    private Logger logback(String name) {
        LoggerContext context = (LoggerContext) LoggerFactory.getILoggerFactory();
        appender = new VisionaryLogbackAppender();
        appender.setContext(context);
        appender.start();
        context.getLogger(Logger.ROOT_LOGGER_NAME).addAppender(appender);
        logs.start();
        Logger l = context.getLogger(name);
        l.setLevel(Level.TRACE);
        return l;
    }

    @Test
    void logbackLevelsMdcAndKeyValues() {
        Logger l = logback("com.example.Orders");
        MDC.put("request_id", "from-mdc");
        MDC.put("traceId", "0af7651916cd43dd8448eb211c80319c");
        MDC.put("tenant", "acme");
        l.atWarn().addKeyValue("order", 7).addKeyValue("paid", true).addKeyValue("session_id", "s-1")
                .addKeyValue("customer", Map.of("id", "c9")).log("order {} late", 7);
        l.debug("quiet");
        assertEquals(1, out.size(), "debug is below the level outside requests");
        LogRecord r = out.get(0);
        assertEquals(LogRules.WARN, r.sev);
        assertEquals("com.example.Orders", r.logger);
        assertEquals("order 7 late", r.message);
        assertEquals("from-mdc", r.requestId);
        assertEquals("0af7651916cd43dd8448eb211c80319c", r.traceId);
        assertEquals("s-1", r.sessionId);
        assertEquals(Map.of("tenant", "acme", "order", 7L, "paid", true, "customer.id", "c9"), r.attrs);
        assertNull(r.className, "no caller data unless Logback computed it");
    }

    @Test
    void logbackThrowableIsInTheMessage() {
        Logger l = logback("com.example.Payments");
        l.error("charge failed", new IllegalStateException("card declined"));
        LogRecord r = out.get(0);
        assertEquals(LogRules.ERROR, r.sev);
        assertTrue(r.message.startsWith("charge failed\njava.lang.IllegalStateException: card declined"), r.message);
        assertTrue(r.message.contains("\tat io.github.styropyr0.visionary.LogsTest"), r.message);
    }

    @Test
    void logbackSkipsTheSdkAndItsThreads() throws Exception {
        LoggerContext context = logback("com.example.Mail").getLoggerContext();
        context.getLogger(RequestWriter.class).error("visionary: batch write failed");
        context.getLogger("io.github.styropyr0.visionary.D1Store").error("driver trouble");
        context.getLogger("com.example.Mail").error("visionary: looks like ours");
        Thread t = new Thread(() -> context.getLogger("org.postgresql.Driver").error("on the writer"), "visionary-logs");
        t.start();
        t.join();
        Thread app = new Thread(() -> context.getLogger("com.example.Mail").error("sent"), "visionary-background-1");
        app.start();
        app.join();
        assertEquals("sent", taken());
    }

    private static boolean onRoot() {
        Logger root = ((LoggerContext) LoggerFactory.getILoggerFactory()).getLogger(Logger.ROOT_LOGGER_NAME);
        for (var it = root.iteratorForAppenders(); it.hasNext();) {
            if (it.next() instanceof VisionaryLogbackAppender) {
                return true;
            }
        }
        return false;
    }

    @Test
    void noAppenderIsAttachedByDefault() {
        AppLogs.ready(logs);
        LogbackInstaller installer = new LogbackInstaller(logs, false);
        installer.start();
        try {
            assertFalse(onRoot(), "capture is opt-in");
            assertFalse(logs.capturing(), "no writer, no settings poll, no app_logs without an appender");
        } finally {
            installer.stop();
        }
    }

    @Test
    void captureStartsOnceAnAppenderExists() {
        AppLogs.ready(logs);
        assertFalse(logs.capturing());
        // VISIONARY_LOG_CAPTURE=true: the starter attaches one to the root logger.
        LogbackInstaller installer = new LogbackInstaller(logs, true);
        installer.start();
        try {
            assertTrue(onRoot());
            assertTrue(logs.capturing());
        } finally {
            installer.stop();
        }
        assertFalse(onRoot(), "removed again when the application stops");
    }

    @Test
    void anAppenderFromTheLogbackConfigurationStartsCaptureWithTheAgent() {
        // Logback reads its configuration before Spring starts the agent.
        logback("com.example.Orders");
        AppLogs later = new AppLogs(null, "api", LogRules.INFO, this::record);
        try {
            AppLogs.ready(later);
            assertTrue(later.capturing());
        } finally {
            later.stop(Duration.ofSeconds(1));
        }
    }

    @Test
    void logbackLevelMapping() {
        assertEquals(LogRules.TRACE, VisionaryLogbackAppender.severity(Level.TRACE));
        assertEquals(LogRules.DEBUG, VisionaryLogbackAppender.severity(Level.DEBUG));
        assertEquals(LogRules.INFO, VisionaryLogbackAppender.severity(Level.INFO));
        assertEquals(LogRules.WARN, VisionaryLogbackAppender.severity(Level.WARN));
        assertEquals(LogRules.ERROR, VisionaryLogbackAppender.severity(Level.ERROR));
    }

    @Test
    void textLevel() {
        Map<String, Integer> cases = new LinkedHashMap<>();
        cases.put("ERROR something", LogRules.ERROR);
        cases.put("2026-10-28 WARN disk", LogRules.WARN);
        cases.put("level=debug msg=x", LogRules.DEBUG);
        cases.put("[info] started", LogRules.INFO);
        cases.put("time=x level=WARN msg=y", LogRules.WARN);
        cases.put("INFO handled", LogRules.INFO);
        cases.put("payment failed", 0);
        cases.put("user error rate is low but ok", 0);
        cases.put("error: connection refused", LogRules.ERROR);
        cases.put("Error handling request", 0);
        cases.put("10:03:44 [ERROR] x", LogRules.ERROR);
        cases.put("2026/10/28 10:03:44 FATAL x", LogRules.FATAL);
        cases.put("<warning> x", LogRules.WARN);
        cases.forEach((in, want) -> assertEquals(want, LogRules.textLevel(in), in));
    }

    @Test
    void stripLevelWord() {
        Map<String, String> cases = new LinkedHashMap<>();
        cases.put("INFO started", "started");
        cases.put("WARNING: disk", "disk");
        cases.put("ERROR", "ERROR");
        cases.put("Error handling", "Error handling");
        cases.put("[warn] slow", "[warn] slow");
        cases.put("WARN", "WARN");
        cases.put("DEBUG   spaced x", "  spaced x");
        cases.forEach((in, want) -> {
            int sev = LogRules.textLevel(in);
            assertEquals(want, LogRules.stripLevelWord(in, sev == 0 ? LogRules.INFO : sev), in);
        });
    }

    @Test
    void parseLevelAndNames() {
        assertEquals(LogRules.WARN, LogRules.parseLevel("WARNING"));
        assertEquals(LogRules.FATAL, LogRules.parseLevel("crit"));
        assertEquals(LogRules.INFO, LogRules.parseLevel("notice"));
        assertEquals(LogRules.ERROR, LogRules.parseLevel("Severe"));
        assertEquals(0, LogRules.parseLevel(""));
        assertEquals(0, LogRules.parseLevel("loud"));
        assertEquals("info", LogRules.name(0));
        assertEquals("fatal", LogRules.name(LogRules.FATAL));
    }

    @Test
    void scrubsSecrets() {
        String in = "login password=hunter2 token: \"abc def\" Authorization: Bearer abcdefghijkl key "
                + "AKIAABCDEFGHIJKLMNOP jwt eyJhbGciOiJ.eyJzdWIiOiIx.c2lnbmF0dXJl -----BEGIN RSA PRIVATE KEY-----\n"
                + "MIIE\n-----END RSA PRIVATE KEY----- done";
        String got = LogRules.scrub(in);
        for (String leak : List.of("hunter2", "abc def", "abcdefghijkl", "AKIAABCDEFGHIJKLMNOP", "eyJhbGciOiJ", "MIIE")) {
            assertFalse(got.contains(leak), leak + " leaked in " + got);
        }
        assertTrue(got.contains("password=[REDACTED]"), got);
        assertTrue(got.contains("Bearer [REDACTED]"), got);
        assertTrue(got.endsWith(" done"), got);
        assertEquals("plain text", LogRules.scrub("plain text"));
    }

    @Test
    void encodesAttrs() throws Exception {
        Map<String, Object> attrs = new LinkedHashMap<>();
        attrs.put("user.password", "x");
        attrs.put("n", 1L);
        attrs.put("note", "token=abc");
        attrs.put("ssn", "1");
        String s = LogRules.encodeAttrs(attrs, LogRules.secretKeys(List.of("SSN")));
        assertTrue(s.contains("\"user.password\":\"[REDACTED]\""), s);
        assertTrue(s.contains("\"note\":\"token=[REDACTED]\""), s);
        assertTrue(s.contains("\"ssn\":\"[REDACTED]\""), s);
        assertTrue(s.contains("\"n\":1"), s);
        assertNull(LogRules.encodeAttrs(Map.of(), Set.of()));

        // Long values are cut first...
        Map<String, Object> big = new LinkedHashMap<>();
        for (int i = 0; i < 10; i++) {
            big.put("k".repeat(i + 1), "v".repeat(2000));
        }
        s = LogRules.encodeAttrs(big, Set.of());
        assertTrue(LogRules.bytes(s) <= LogRules.MAX_ATTRS, "too big: " + LogRules.bytes(s));
        assertTrue(json.readTree(s).get("k").asText().endsWith("…"));
        assertFalse(s.contains("_truncated"));

        // ...then keys are kept in sorted order while they fit.
        Map<String, Object> many = new LinkedHashMap<>();
        for (int i = 0; i < 2000; i++) {
            many.put(String.format("key%04d", i), "value");
        }
        s = LogRules.encodeAttrs(many, Set.of());
        assertTrue(LogRules.bytes(s) <= LogRules.MAX_ATTRS, "too big: " + LogRules.bytes(s));
        var tree = json.readTree(s);
        assertTrue(tree.get("_truncated").asBoolean());
        assertTrue(tree.has("key0000"));
        assertFalse(tree.has("key1999"));
    }

    @Test
    void rowMatchesTheInsert() {
        Origin origin = new Origin("api", "v2", "i-1", "OpenJDK 21", null);
        LogWriter w = new LogWriter(null, origin, LogRules.secretKeys(List.of()), null);
        LogRecord r = new LogRecord(Instant.parse("2026-10-28T09:30:12.345Z"), LogRules.WARN, "com.example.Orders",
                "hi password=hunter2");
        r.attrs = new LinkedHashMap<>(Map.of("a", 1L));
        r.requestId = "req-1";
        r.className = "com.example.Orders";
        r.method = "place";
        r.fileName = "Orders.java";
        r.line = 3;
        Object[] row = w.row(r);
        assertEquals(21, Table.APP_LOGS.columns.size());
        assertEquals(Table.APP_LOGS.columns.size(), row.length);
        assertEquals(Table.APP_LOGS.columns.size(), Table.APP_LOGS.insertSql("?").chars().filter(c -> c == '?').count());
        for (int i = 0; i < row.length; i++) {
            if (row[i] == null) {
                continue;
            }
            Class<?> want = switch (Table.APP_LOGS.types.get(i)) {
                case TEXT -> String.class;
                case INT -> Long.class;
                case REAL -> Double.class;
            };
            assertEquals(want, row[i].getClass(), Table.APP_LOGS.columns.get(i));
        }
        Map<String, Object> byColumn = new LinkedHashMap<>();
        for (int i = 0; i < row.length; i++) {
            byColumn.put(Table.APP_LOGS.columns.get(i), row[i]);
        }
        assertEquals("2026-10-28T09:30:12.345Z", byColumn.get("created_at"));
        assertEquals("java", byColumn.get("source"));
        assertEquals("warn", byColumn.get("level"));
        assertEquals(4L, byColumn.get("severity"));
        assertEquals("hi password=[REDACTED]", byColumn.get("message"));
        assertEquals("{\"a\":1}", byColumn.get("attrs"));
        assertEquals("req-1", byColumn.get("request_id"));
        assertEquals("Orders.java", byColumn.get("code_file"));
        assertEquals(3L, byColumn.get("code_line"));
        assertEquals("com.example.Orders.place", byColumn.get("code_func"));
        assertNull(byColumn.get("container"));
        assertEquals((long) ("hi password=[REDACTED]".length() + "{\"a\":1}".length()), byColumn.get("size"));
    }

    @Test
    void longMessagesAreCut() {
        LogWriter w = new LogWriter(null, new Origin(null, null, "i", "r", null), Set.of(), null);
        Object[] row = w.row(new LogRecord(Instant.now(), LogRules.INFO, null, "é".repeat(LogRules.MAX_MESSAGE)));
        String message = (String) row[Table.APP_LOGS.indexOf("message")];
        assertTrue(LogRules.bytes(message) <= LogRules.MAX_MESSAGE);
        assertNull(row[Table.APP_LOGS.indexOf("logger")]);
        assertNull(row[Table.APP_LOGS.indexOf("attrs")]);
    }

    @Test
    void traceParent() {
        assertEquals("4bf92f3577b34da6a3ce929d0e0e4736",
                LogScope.traceParentId("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"));
        for (String bad : new String[] {null, "", "00-xyz-1-1", "00-00000000000000000000000000000000-00f067aa0ba902b7-01"}) {
            assertNull(LogScope.traceParentId(bad), bad);
        }
    }

    @Test
    void codeFileIsNamedLikeProfileFrames() {
        FrameNamer namer = new FrameNamer(List.of("com.example.demo"), getClass().getClassLoader());
        assertEquals("com/example/demo/web/Orders.java", namer.file("com.example.demo.web.Orders$1", null));
        assertEquals("std:java/util/HashMap.java", namer.file("java.util.HashMap", "HashMap.java"));
    }
}
