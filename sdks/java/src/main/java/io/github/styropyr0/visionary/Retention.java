package io.github.styropyr0.visionary;

import java.time.Instant;
import java.time.ZoneOffset;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.concurrent.Executors;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.TimeUnit;

import org.apache.commons.logging.Log;
import org.apache.commons.logging.LogFactory;

/**
 * Deletes expired rows a minute after start and then daily, in chunks of 5000
 * so no single statement hits D1's execution limits.
 */
final class Retention {

    private static final Log log = LogFactory.getLog(Retention.class);
    private static final int CHUNK = 5000;
    private static final int MAX_ROUNDS = 200;

    private final Store store;
    private final Map<Table, Integer> days = new LinkedHashMap<>();
    private final ScheduledExecutorService executor;

    Retention(Store store) {
        this.store = store;
        this.executor = Executors.newSingleThreadScheduledExecutor(r -> {
            Thread t = new Thread(r, "visionary-retention");
            t.setDaemon(true);
            return t;
        });
    }

    void add(Table table, int retentionDays) {
        if (retentionDays > 0) {
            days.put(table, retentionDays);
        }
    }

    void start() {
        if (!days.isEmpty()) {
            executor.scheduleAtFixedRate(this::purgeAll, 60, TimeUnit.DAYS.toSeconds(1), TimeUnit.SECONDS);
        }
    }

    void close() {
        executor.shutdownNow();
    }

    private void purgeAll() {
        days.forEach(this::purge);
    }

    private void purge(Table table, int retentionDays) {
        String cutoff = Values.timestamp(Instant.now().atOffset(ZoneOffset.UTC).minusDays(retentionDays).toInstant());
        long total = 0;
        try {
            for (int round = 0; round < MAX_ROUNDS; round++) {
                long changes = store.purgeChunk(table, cutoff);
                total += changes;
                if (changes < CHUNK) {
                    break;
                }
            }
        } catch (Exception e) {
            log.error("visionary: retention purge of " + table.name + " failed after " + total + " rows: " + e);
            return;
        }
        if (total > 0) {
            log.info("visionary: retention purge deleted " + total + " rows from " + table.name
                    + " older than " + cutoff);
        }
    }
}
