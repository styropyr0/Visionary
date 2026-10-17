package io.github.styropyr0.visionary;

import java.io.File;
import java.io.IOException;
import java.lang.management.GarbageCollectorMXBean;
import java.lang.management.ManagementFactory;
import java.lang.management.MemoryMXBean;
import java.lang.management.MemoryUsage;
import java.lang.management.OperatingSystemMXBean;
import java.lang.management.ThreadMXBean;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.Executors;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.TimeUnit;

import org.apache.commons.logging.Log;
import org.apache.commons.logging.LogFactory;

/**
 * Samples host, process and JVM metrics every interval into {@code system_metrics},
 * four rows per write. A source this runtime or OS doesn't offer is stored as NULL.
 */
final class SystemSampler {

    private static final Log log = LogFactory.getLog(SystemSampler.class);

    static final int SAMPLES_PER_WRITE = 4;
    private static final double MB = 1024.0 * 1024.0;
    private static final double GB = MB * 1024.0;
    private static final Path PROC_STATUS = Path.of("/proc/self/status");

    private final Store store;
    private final String instance;
    private final Duration interval;
    private final HttpCounters http;
    private final PoolStats pool;
    private final File disk = new File(System.getProperty("user.dir", ".")).getAbsoluteFile();
    private final ScheduledExecutorService executor;

    private final OperatingSystemMXBean os = ManagementFactory.getOperatingSystemMXBean();
    private final ThreadMXBean threads = ManagementFactory.getThreadMXBean();
    private final MemoryMXBean memory = ManagementFactory.getMemoryMXBean();
    private final int cores = Runtime.getRuntime().availableProcessors();
    private final long started = System.nanoTime();

    // Previous readings, touched only on the sampler thread.
    private long last;
    private long requests;
    private long errors;
    private long gcCount;
    private long gcTimeMs;
    private long procCpuNs;
    private final List<Object[]> buffer = new ArrayList<>();
    /** Buffer size that triggers the next write; after a failure, one more batch's worth. */
    private int writeAt = SAMPLES_PER_WRITE;
    private boolean schemaReady;

    SystemSampler(Store store, String instance, Duration interval, HttpCounters http, PoolStats pool) {
        this.store = store;
        this.instance = instance;
        this.interval = interval;
        this.http = http;
        this.pool = pool;
        this.executor = Executors.newSingleThreadScheduledExecutor(r -> {
            Thread t = new Thread(r, "visionary-system");
            t.setDaemon(true);
            return t;
        });
    }

    void start() {
        executor.execute(this::prime);
        long ms = interval.toMillis();
        executor.scheduleAtFixedRate(this::tick, ms, ms, TimeUnit.MILLISECONDS);
    }

    /** Flushes buffered rows and stops, waiting at most {@code timeout}. */
    void close(Duration timeout) {
        executor.execute(this::flush);
        executor.shutdown();
        try {
            if (!executor.awaitTermination(timeout.toMillis(), TimeUnit.MILLISECONDS)) {
                executor.shutdownNow();
            }
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
    }

    private void ensureSchema() throws Exception {
        if (!schemaReady) {
            store.createSchema(Table.SYSTEM_METRICS);
            schemaReady = true;
        }
    }

    /** Baseline readings so the first row has real deltas, and the table up front. */
    private void prime() {
        try {
            ensureSchema();
        } catch (Exception e) {
            log.warn("visionary: create system_metrics failed (retrying on the next write): " + e);
        }
        last = System.nanoTime();
        requests = http.requests.get();
        errors = http.errors.get();
        long[] gc = gcTotals();
        gcCount = gc[0];
        gcTimeMs = gc[1];
        procCpuNs = processCpuTime();
        if (os instanceof com.sun.management.OperatingSystemMXBean sun) {
            sun.getCpuLoad();
        }
    }

    private void tick() {
        try {
            buffer.add(sample());
        } catch (RuntimeException e) {
            log.debug("visionary: system sample failed", e);
        }
        if (buffer.size() >= writeAt) {
            flush();
        }
    }

    private void flush() {
        if (buffer.isEmpty()) {
            return;
        }
        try {
            ensureSchema();
            store.insert(Table.SYSTEM_METRICS, buffer);
            buffer.clear();
            writeAt = SAMPLES_PER_WRITE;
        } catch (Exception e) {
            log.warn("visionary: system metrics write failed (" + buffer.size() + " rows kept): " + e);
            // Keep at most an hour of samples while the database is unreachable.
            int keep = (int) Math.max(1, Duration.ofHours(1).toMillis() / Math.max(1, interval.toMillis()));
            if (buffer.size() > keep) {
                buffer.subList(0, buffer.size() - keep).clear();
            }
            writeAt = buffer.size() + SAMPLES_PER_WRITE;
        }
    }

    private Object[] sample() {
        Table t = Table.SYSTEM_METRICS;
        Object[] row = new Object[t.columns.size()];
        long now = System.nanoTime();
        double secs = (now - last) / 1e9;
        long elapsedNs = now - last;
        last = now;
        if (secs <= 0) {
            secs = interval.toMillis() / 1000.0;
            elapsedNs = interval.toNanos();
        }
        set(row, "created_at", Values.timestamp(Instant.now()));
        set(row, "instance", instance);
        set(row, "interval_s", Values.round(secs, 2));

        double load = os.getSystemLoadAverage();
        if (load >= 0) {
            set(row, "load_1", Values.round(load, 2));
        }
        if (os instanceof com.sun.management.OperatingSystemMXBean sun) {
            double cpu = sun.getCpuLoad();
            if (cpu >= 0) {
                set(row, "host_cpu_percent", Values.round(cpu * 100, 1));
            }
            long total = sun.getTotalMemorySize();
            long free = sun.getFreeMemorySize();
            if (total > 0) {
                set(row, "host_mem_used_mb", Values.round((total - free) / MB, 1));
                set(row, "host_mem_total_mb", Values.round(total / MB, 1));
                set(row, "host_mem_percent", Values.round((total - free) * 100.0 / total, 1));
            }
        }
        if (os instanceof com.sun.management.UnixOperatingSystemMXBean unix) {
            long fds = unix.getOpenFileDescriptorCount();
            if (fds >= 0) {
                set(row, "proc_open_fds", fds);
            }
        }
        sampleDisk(row);

        long cpuNs = processCpuTime();
        if (cpuNs >= 0 && procCpuNs >= 0) {
            set(row, "proc_cpu_percent", Values.round((cpuNs - procCpuNs) * 100.0 / elapsedNs, 1));
        } else if (os instanceof com.sun.management.OperatingSystemMXBean sun && sun.getProcessCpuLoad() >= 0) {
            set(row, "proc_cpu_percent", Values.round(sun.getProcessCpuLoad() * 100 * cores, 1));
        }
        procCpuNs = cpuNs;
        Long rssKb = residentKb();
        if (rssKb != null) {
            set(row, "proc_rss_mb", Values.round(rssKb / 1024.0, 1));
        }
        long threadCount = threads.getThreadCount();
        set(row, "proc_threads", threadCount);
        set(row, "goroutines", threadCount);

        MemoryUsage heap = memory.getHeapMemoryUsage();
        set(row, "heap_alloc_mb", Values.round(heap.getUsed() / MB, 1));
        set(row, "heap_inuse_mb", Values.round(heap.getCommitted() / MB, 1));
        long[] gc = gcTotals();
        double pauseMs = gc[1] - gcTimeMs;
        set(row, "gc_count", gc[0] - gcCount);
        set(row, "gc_pause_ms", Values.round(pauseMs, 2));
        set(row, "gc_cpu_percent", Values.round(pauseMs / (secs * 1000) * 100, 2));
        gcCount = gc[0];
        gcTimeMs = gc[1];

        long[] p = pool == null ? null : pool.read();
        if (p != null) {
            set(row, "db_total_conns", p[0]);
            set(row, "db_idle_conns", p[1]);
            set(row, "db_acquired_conns", p[2]);
            set(row, "db_max_conns", p[3]);
        }

        long reqs = http.requests.get();
        long errs = http.errors.get();
        long dReqs = reqs - requests;
        set(row, "http_requests", dReqs);
        set(row, "http_errors", errs - errors);
        set(row, "http_rps", Values.round(dReqs / secs, 2));
        set(row, "http_in_flight", Math.max(0, http.inFlight.get()));
        requests = reqs;
        errors = errs;

        set(row, "uptime_s", (now - started) / 1_000_000_000L);
        return row;
    }

    /** Same definitions as the Go SDK (gopsutil): used / (used + available to us). */
    private void sampleDisk(Object[] row) {
        long total = disk.getTotalSpace();
        long free = disk.getFreeSpace();
        long usable = disk.getUsableSpace();
        if (total <= 0) {
            return;
        }
        long used = total - free;
        if (used + usable > 0) {
            set(row, "disk_used_percent", Values.round(used * 100.0 / (used + usable), 1));
        }
        set(row, "disk_free_gb", Values.round(usable / GB, 2));
    }

    private long[] gcTotals() {
        long count = 0;
        long timeMs = 0;
        for (GarbageCollectorMXBean gc : ManagementFactory.getGarbageCollectorMXBeans()) {
            count += Math.max(0, gc.getCollectionCount());
            timeMs += Math.max(0, gc.getCollectionTime());
        }
        return new long[] {count, timeMs};
    }

    private long processCpuTime() {
        if (os instanceof com.sun.management.OperatingSystemMXBean sun) {
            return sun.getProcessCpuTime();
        }
        return -1;
    }

    /** VmRSS from procfs on Linux; null elsewhere. */
    private static Long residentKb() {
        if (!Files.isReadable(PROC_STATUS)) {
            return null;
        }
        try {
            for (String line : Files.readAllLines(PROC_STATUS)) {
                if (line.startsWith("VmRSS:")) {
                    return Long.parseLong(line.replaceAll("\\D+", ""));
                }
            }
        } catch (IOException | RuntimeException ignored) {
            // treated as unavailable
        }
        return null;
    }

    private static void set(Object[] row, String column, Object value) {
        row[Table.SYSTEM_METRICS.indexOf(column)] = value;
    }

    /** Database pool snapshot: total, idle, active, max; null when unavailable. */
    @FunctionalInterface
    interface PoolStats {
        long[] read();
    }
}
