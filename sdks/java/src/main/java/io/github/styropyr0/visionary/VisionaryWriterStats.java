package io.github.styropyr0.visionary;

import java.util.Set;
import java.util.concurrent.ConcurrentHashMap;

/**
 * How far behind the database the background writers are, summed across
 * requests, logs and metrics
 *
 * @param queued  rows waiting for the database
 * @param written rows written since start
 * @param dropped rows that met a full queue or a write that failed three times
 */
public record VisionaryWriterStats(int queued, long written, long dropped) {

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
