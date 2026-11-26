package io.github.styropyr0.visionary;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.time.Duration;
import java.util.List;
import java.util.Map;

import org.junit.jupiter.api.Test;

/** The default instance and the system metrics switch in Settings → Server */
class SystemTest {

    @Test
    void defaultInstanceSkipsContainerIds() {
        String[][] cases = {
                {"web-1", "api", "web-1"},
                {"a1b2c3d4e5f6", "api", "api"},
                {"a1b2c3d4e5f6", "", "java-app"},
                {"ab12".repeat(16), "", "java-app"},
                {"", "api", "api"},
                {"A1B2C3D4E5F6", "", "A1B2C3D4E5F6"},
                {"a1b2c3d4e5f", "", "a1b2c3d4e5f"},
                {"deadbeefcafe-2", "api", "deadbeefcafe-2"},
        };
        for (String[] c : cases) {
            assertEquals(c[2], VisionaryConfig.defaultInstance(c[0], c[1]), c[0] + ", " + c[1]);
        }
    }

    @Test
    void settingTurnsSamplingOffAndBackOn() {
        SettingsStore store = new SettingsStore();
        store.error = new Exception("no such table: visionary_settings");
        SystemSampler s = new SystemSampler(store, new Origin("api", null, "i", "Java 17", null),
                Duration.ofSeconds(15), new HttpCounters(), null);

        s.readSwitch();
        assertFalse(s.off(), "no table: on");
        s.tick();
        assertEquals(1, s.buffered());

        store.error = null;
        store.rows = List.<String[]>of(new String[] {" FALSE "});
        s.readSwitch();
        assertTrue(s.off());
        assertEquals(0, s.buffered(), "unwritten rows dropped");
        s.tick();
        assertEquals(0, s.buffered(), "no samples while off");

        store.error = new Exception("database is locked");
        s.readSwitch();
        assertTrue(s.off(), "read error: stays off");

        store.error = null;
        store.rows = List.of();
        s.readSwitch();
        assertFalse(s.off(), "row deleted: on");
        assertEquals(0, s.buffered(), "the baseline sample is thrown away");

        store.rows = List.<String[]>of(new String[] {"true"});
        s.readSwitch();
        assertFalse(s.off(), "true: on");
        assertEquals(SystemSampler.SETTING_QUERY, store.lastQuery);
    }

    private static final class SettingsStore implements Store {
        List<String[]> rows = List.of();
        Exception error;
        String lastQuery;

        @Override
        public void createSchema(Table table) {
        }

        @Override
        public void insert(Map<Table, List<Object[]>> rows) {
        }

        @Override
        public List<String[]> select(String sql) throws Exception {
            lastQuery = sql;
            if (error != null) {
                throw error;
            }
            return rows;
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
