package io.github.styropyr0.visionary;

import java.util.Set;
import java.util.concurrent.ConcurrentHashMap;

/**
 * A snapshot of the running background writers for request rows, logs and
 * metrics, summed, from {@link Visionary#writerStats()}: how far behind the
 * database they are, for load tests and health checks.
 *
 * @param queued  rows waiting for the database
 * @param written rows written since start
 * @param dropped rows that met a full queue or a write that failed three times
 */
public record VisionaryWriterStats(int queued, long written, long dropped) {

    /** A running writer's counters. */
    interface Source {
        int queued();

        long written();

        long dropped();
    }

    private static final Set<Source> SOURCES = ConcurrentHashMap.newKeySet();

    static void add(Source s) {
        SOURCES.add(s);
    }

    static void remove(Source s) {
        SOURCES.remove(s);
    }

    static VisionaryWriterStats snapshot() {
        int queued = 0;
        long written = 0;
        long dropped = 0;
        for (Source s : SOURCES) {
            queued += s.queued();
            written += s.written();
            dropped += s.dropped();
        }
        return new VisionaryWriterStats(queued, written, dropped);
    }
}
