package io.github.styropyr0.visionary;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.io.PrintWriter;
import java.lang.reflect.InvocationTargetException;
import java.lang.reflect.Proxy;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.nio.file.Path;
import java.sql.Connection;
import java.sql.DriverManager;
import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.Map;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.logging.Logger;

import javax.sql.DataSource;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.sun.net.httpserver.HttpServer;

import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

class WriterTest {

    @TempDir
    Path dir;

    @Test
    void multiRowInsertSql() {
        assertEquals("INSERT INTO releases (created_at, service, instance, version, sdk, sdk_version, runtime) VALUES "
                + "(?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?)", Table.RELEASES.insertRowsSql(2));
        assertEquals(30_000 / 34, JdbcStore.rowsPerStatement(Table.API_LOGS));
    }

    @Test
    void rowsAreSplitUnderTheParameterLimitAndKeepNulls() throws Exception {
        List<String> statements = Collections.synchronizedList(new ArrayList<>());
        JdbcStore store = JdbcStore.forDataSource(recording("jdbc:sqlite:" + dir.resolve("v.db"), statements), "test");
        store.createSchema(Table.APP_LOGS);
        assertEquals(1, store.parallelWrites());
        assertEquals(JdbcStore.BATCH_SIZE, store.batchSize());

        int per = JdbcStore.rowsPerStatement(Table.APP_LOGS);
        LogWriter w = new LogWriter(null, new Origin("api", null, "i-1", "Java", null), java.util.Set.of(), null);
        List<Object[]> rows = new ArrayList<>();
        for (int i = 0; i < per + 10; i++) {
            rows.add(w.row(new LogRecord(Instant.now(), LogRules.INFO, null, "line " + i)));
        }
        statements.clear();
        store.insert(Table.APP_LOGS, rows);

        List<String> inserts = statements.stream().filter(s -> s.startsWith("INSERT")).toList();
        assertEquals(2, inserts.size());
        for (String sql : inserts) {
            long params = sql.chars().filter(c -> c == '?').count();
            assertTrue(params <= JdbcStore.MAX_PARAMS_PER_STATEMENT, params + " parameters");
        }
        List<String[]> got = store.select("SELECT COUNT(*), COUNT(version), COUNT(logger), MAX(message) FROM app_logs");
        assertEquals(String.valueOf(per + 10), got.get(0)[0]);
        assertEquals("0", got.get(0)[1]);
        assertEquals("0", got.get(0)[2]);
        assertEquals("line 999", got.get(0)[3]);
        assertNull(store.select("SELECT version FROM app_logs LIMIT 1").get(0)[0]);
        store.close();
    }

    @Test
    void sqliteUrlsWaitForTheLock() {
        StoreFactory.JdbcTarget t = StoreFactory.JdbcTarget.parse("sqlite:///" + dir.resolve("v.db"));
        t.sharedFile();
        assertEquals("5000", t.props().getProperty("busy_timeout"));
        assertEquals("WAL", t.props().getProperty("journal_mode"));
        StoreFactory.JdbcTarget own = StoreFactory.JdbcTarget.parse("jdbc:sqlite:v.db?busy_timeout=100");
        own.sharedFile();
        assertNull(own.props().getProperty("busy_timeout"));
    }

    @Test
    void d1KeepsOneStatementPerRowAndFiftyPerCall() throws Exception {
        List<JsonNode> calls = Collections.synchronizedList(new ArrayList<>());
        ObjectMapper json = new ObjectMapper();
        HttpServer server = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        server.createContext("/", ex -> {
            calls.add(json.readTree(ex.getRequestBody()));
            byte[] ok = "{\"success\":true,\"result\":[]}".getBytes(StandardCharsets.UTF_8);
            ex.sendResponseHeaders(200, ok.length);
            ex.getResponseBody().write(ok);
            ex.close();
        });
        server.start();
        try {
            D1Store store = new D1Store("http://127.0.0.1:" + server.getAddress().getPort(), "a", "d", "t", json);
            assertEquals(50, store.batchSize());
            assertEquals(1, store.parallelWrites());
            List<Object[]> rows = new ArrayList<>();
            for (int i = 0; i < 60; i++) {
                rows.add(new Object[] {"2026-01-01T00:00:00Z", "api", "i", null, "java", null, "Java"});
            }
            store.insert(Table.RELEASES, rows);
        } finally {
            server.stop(0);
        }
        assertEquals(2, calls.size());
        assertEquals(50, calls.get(0).path("batch").size());
        assertEquals(10, calls.get(1).path("batch").size());
        JsonNode first = calls.get(0).path("batch").path(0);
        assertTrue(first.path("sql").asText().endsWith("VALUES (NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), "
                + "NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''))"));
        assertEquals("", first.path("params").path(3).asText());
    }

    @Test
    void parallelBatchesAndStats() throws Exception {
        BatchStore store = new BatchStore();
        LogWriter w = new LogWriter(store, new Origin("api", null, "i-1", "Java", null), java.util.Set.of(), null);
        for (int i = 0; i < LogWriter.QUEUE_SIZE + 5; i++) {
            w.enqueue(new LogRecord(Instant.now(), LogRules.INFO, null, "line"));
        }
        assertEquals(LogWriter.QUEUE_SIZE, w.queued());
        assertEquals(5, w.dropped());
        w.start();
        VisionaryWriterStats running = Visionary.writerStats();
        assertTrue(running.dropped() >= 5);
        w.close(Duration.ofSeconds(10));
        assertEquals(LogWriter.QUEUE_SIZE, w.written());
        assertEquals(0, w.queued());
        assertEquals(LogWriter.QUEUE_SIZE / 500, store.batches.get());
        assertTrue(store.maxInFlight.get() <= 4, "in flight " + store.maxInFlight.get());
    }

    /** Takes 500-row batches, up to four at once, slowly enough for them to overlap. */
    private static final class BatchStore implements Store {
        final AtomicInteger batches = new AtomicInteger();
        final AtomicInteger inFlight = new AtomicInteger();
        final AtomicInteger maxInFlight = new AtomicInteger();

        @Override
        public void createSchema(Table table) {
        }

        @Override
        public void insert(Map<Table, List<Object[]>> rows) throws InterruptedException {
            maxInFlight.accumulateAndGet(inFlight.incrementAndGet(), Math::max);
            assertTrue(rows.get(Table.APP_LOGS).size() <= 500);
            Thread.sleep(20);
            batches.incrementAndGet();
            inFlight.decrementAndGet();
        }

        @Override
        public int batchSize() {
            return 500;
        }

        @Override
        public int parallelWrites() {
            return 4;
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
            return "batches";
        }

        @Override
        public void close() {
        }
    }

    /** A DataSource over the URL that records every statement prepared. */
    private static DataSource recording(String url, List<String> statements) {
        return (DataSource) Proxy.newProxyInstance(WriterTest.class.getClassLoader(), new Class<?>[] {DataSource.class},
                (proxy, method, args) -> switch (method.getName()) {
                    case "getConnection" -> {
                        Connection c = DriverManager.getConnection(url);
                        yield Proxy.newProxyInstance(WriterTest.class.getClassLoader(),
                                new Class<?>[] {Connection.class}, (p, m, a) -> {
                                    if (m.getName().equals("prepareStatement")) {
                                        statements.add((String) a[0]);
                                    }
                                    try {
                                        return m.invoke(c, a);
                                    } catch (InvocationTargetException e) {
                                        throw e.getCause();
                                    }
                                });
                    }
                    case "getLogWriter" -> (PrintWriter) null;
                    case "getParentLogger" -> Logger.getGlobal();
                    default -> throw new UnsupportedOperationException(method.getName());
                });
    }
}
