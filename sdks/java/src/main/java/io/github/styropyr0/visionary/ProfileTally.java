package io.github.styropyr0.visionary;

import java.util.Map;
import java.util.TreeMap;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.atomic.AtomicLong;
import java.util.concurrent.atomic.DoubleAdder;

/**
 * What the CPU profiler needs from the filter: the number of requests started
 * so far, and while a profile runs, the milliseconds the requests that finished
 * took per {@code "METHOD route"}. Outside a profile a finished request costs one
 * volatile read.
 */
final class ProfileTally {

    private static final AtomicLong started = new AtomicLong();
    private static volatile ConcurrentHashMap<String, DoubleAdder> busy;

    private ProfileTally() {
    }

    static void started() {
        started.incrementAndGet();
    }

    static long startedCount() {
        return started.get();
    }

    /** @param route the matched route, else the path */
    static void finished(String method, String route, double durationMs) {
        ConcurrentHashMap<String, DoubleAdder> b = busy;
        if (b != null) {
            b.computeIfAbsent(method + " " + route, k -> new DoubleAdder()).add(durationMs);
        }
    }

    static boolean counting() {
        return busy != null;
    }

    /** Starts counting route time for a profile. */
    static void begin() {
        busy = new ConcurrentHashMap<>();
    }

    /** Stops counting and returns the milliseconds per route, each at least 1. */
    static Map<String, Long> end() {
        ConcurrentHashMap<String, DoubleAdder> b = busy;
        busy = null;
        Map<String, Long> out = new TreeMap<>();
        if (b != null) {
            b.forEach((k, v) -> out.put(k, Math.max(1L, Math.round(v.sum()))));
        }
        return out;
    }
}
