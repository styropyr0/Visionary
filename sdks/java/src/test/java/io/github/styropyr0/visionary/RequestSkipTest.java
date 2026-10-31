package io.github.styropyr0.visionary;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.util.ArrayList;
import java.util.List;
import java.util.Map;

import com.fasterxml.jackson.databind.ObjectMapper;

import org.junit.jupiter.api.Test;
import org.springframework.core.env.MapPropertySource;
import org.springframework.core.env.StandardEnvironment;

class RequestSkipTest {

    private final ObjectMapper json = new ObjectMapper();

    @Test
    void parseTrimsAndDropsEmptyEntries() {
        assertEquals(List.of("/health", "/static/*"),
                RequestSkip.parse("[\" /health \", \"\", \"  \", \"/static/*\"]", json));
    }

    @Test
    void anythingButAnArrayOfStringsSkipsNothing() {
        for (String v : new String[] {null, "", "not json", "{\"a\":1}", "\"/health\"", "null",
                "[\"/a\", 1]", "[\"/a\", null]"}) {
            assertEquals(List.of(), RequestSkip.parse(v, json), String.valueOf(v));
        }
    }

    @Test
    void exactOrPrefixMatch() {
        List<String> list = List.of("/health", "/static/*");
        assertTrue(RequestSkip.match(list, "/health"));
        assertFalse(RequestSkip.match(list, "/health/"));
        assertFalse(RequestSkip.match(list, "/Health"));
        assertTrue(RequestSkip.match(list, "/static/app.js"));
        assertTrue(RequestSkip.match(list, "/static/"));
        assertFalse(RequestSkip.match(list, "/static"));
        assertFalse(RequestSkip.match(List.of(), "/health"));
        assertTrue(RequestSkip.match(List.of("*"), "/anything"));
    }

    @Test
    void failedReadKeepsTheLastList() {
        FakeStore store = new FakeStore();
        RequestSkip skip = new RequestSkip(store, json);
        store.fail = true;
        skip.poll();
        assertFalse(skip.skipped("/health"));

        store.fail = false;
        store.rows.add(new String[] {"requests.other", "[\"/other\"]"});
        store.rows.add(new String[] {"requests.skip", "[\"/health\"]"});
        skip.poll();
        assertTrue(skip.skipped("/health"));
        assertFalse(skip.skipped("/other"));

        store.fail = true;
        skip.poll();
        assertTrue(skip.skipped("/health"));

        // the row removed: nothing skipped
        store.fail = false;
        store.rows.clear();
        skip.poll();
        assertFalse(skip.skipped("/health"));
    }

    @Test
    void filterTreatsSkippedLikeOutsideThePrefix() {
        StandardEnvironment env = new StandardEnvironment();
        env.getPropertySources().addFirst(new MapPropertySource("test",
                Map.of("VISIONARY_LOG_PATH_PREFIX", "/api")));
        VisionaryConfig cfg = VisionaryConfig.from(env);
        FakeStore store = new FakeStore();
        store.rows.add(new String[] {"requests.skip", "[\"/api/health\"]"});
        RequestSkip skip = new RequestSkip(store, json);
        skip.poll();
        RequestWriter writer = new RequestWriter(cfg, new Origin("api", null, "host-1", "Java 17", null), store,
                json, null);
        VisionaryFilter filter = new VisionaryFilter(new HttpCounters(), writer, null, null, skip, cfg);
        assertTrue(filter.logged("/api/users"));
        assertFalse(filter.logged("/api/health"));
        assertFalse(filter.logged("/other"));
        assertTrue(new VisionaryFilter(new HttpCounters(), writer, null, null, null, cfg).logged("/api/health"));
    }

    private static final class FakeStore implements Store {
        final List<String[]> rows = new ArrayList<>();
        volatile boolean fail;

        @Override
        public void createSchema(Table table) {
        }

        @Override
        public void insert(Map<Table, List<Object[]>> rows) {
        }

        @Override
        public List<String[]> select(String sql) throws Exception {
            if (fail) {
                throw new java.sql.SQLException("no such table: visionary_settings");
            }
            assertEquals(RequestSkip.SETTINGS_QUERY, sql);
            return new ArrayList<>(rows);
        }

        @Override
        public long purgeChunk(Table table, String cutoff) {
            return 0;
        }

        @Override
        public String describe() {
            return "fake";
        }

        @Override
        public void close() {
        }
    }
}
