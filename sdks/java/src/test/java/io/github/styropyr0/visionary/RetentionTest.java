package io.github.styropyr0.visionary;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.util.Map;

import org.junit.jupiter.api.Test;
import org.springframework.core.env.AbstractEnvironment;
import org.springframework.core.env.MapPropertySource;

/** Retention is the Visionary server's job: the starter purges only when asked. */
class RetentionTest {

    private static VisionaryConfig config(Map<String, Object> props) {
        AbstractEnvironment env = new AbstractEnvironment() {};
        env.getPropertySources().addFirst(new MapPropertySource("test", props));
        return VisionaryConfig.from(env);
    }

    private static boolean retentionThreadRunning() {
        return Thread.getAllStackTraces().keySet().stream()
                .anyMatch(t -> t.isAlive() && "visionary-retention".equals(t.getName()));
    }

    @Test
    void defaultsAreOff() {
        VisionaryConfig cfg = config(Map.of());
        assertEquals(0, cfg.logRetentionDays());
        assertEquals(0, cfg.systemRetentionDays());

        cfg = config(Map.of("VISIONARY_LOG_RETENTION_DAYS", "90", "VISIONARY_SYSTEM_RETENTION_DAYS", "7"));
        assertEquals(90, cfg.logRetentionDays());
        assertEquals(7, cfg.systemRetentionDays());
    }

    @Test
    void nothingIsScheduledAtZero() throws Exception {
        Retention off = new Retention(null);
        for (Table t : new Table[] {Table.API_LOGS, Table.SPANS, Table.ERRORS, Table.PROFILES, Table.SYSTEM_METRICS}) {
            off.add(t, 0);
        }
        off.start();
        assertFalse(retentionThreadRunning(), "no retention thread with every table at 0");
        off.close();

        Retention on = new Retention(null);
        on.add(Table.API_LOGS, 30);
        on.start();
        try {
            assertTrue(retentionThreadRunning(), "a retention thread once a table is above 0");
        } finally {
            on.close();
        }
    }
}
