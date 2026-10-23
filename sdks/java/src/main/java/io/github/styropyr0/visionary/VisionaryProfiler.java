package io.github.styropyr0.visionary;

import java.time.Duration;
import java.time.Instant;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.concurrent.Executors;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.function.Consumer;

import com.fasterxml.jackson.core.JsonProcessingException;
import com.fasterxml.jackson.databind.ObjectMapper;

import org.apache.commons.logging.Log;
import org.apache.commons.logging.LogFactory;
import org.springframework.context.SmartLifecycle;

/**
 * Dashboard-controlled CPU profiles (SPEC.md, "CPU profiles"). Reads the
 * {@code profile.*} settings at start and every 30 seconds on its own thread;
 * while profiling is on for this service it samples the CPU for
 * {@code duration_ms} every {@code interval_s}, skipping a turn when no request
 * arrived since the last one, and hands each profile with at least
 * {@link FoldedStacks#MIN_SAMPLES} samples to the writer, whether or not a
 * request finished while it ran. A fresh {@code profile.now} takes one profile
 * at once, kept with any samples. {@code VISIONARY_PROFILE=false}
 * turns all of it off, settings reads included.
 */
final class VisionaryProfiler implements SmartLifecycle {

    private static final Log log = LogFactory.getLog(VisionaryProfiler.class);
    static final String SETTINGS_QUERY = "SELECT name, value FROM visionary_settings WHERE name LIKE 'profile.%'";
    static final Duration POLL_INTERVAL = Duration.ofSeconds(30);
    /** One profile at a time per process. */
    private static final AtomicBoolean sampling = new AtomicBoolean();

    private final Store store;
    private final Origin origin;
    private final Consumer<ProfileEntry> sink;
    private final boolean allowed;
    private final ObjectMapper json;
    private final Instant processStart = Instant.now();
    private CpuSampler sampler;
    private final CpuSampler fallback;

    private ScheduledExecutorService executor;
    private volatile boolean running;

    // State below is touched by the profiler thread only (and by tests calling tick directly).
    private ProfileSettings settings = ProfileSettings.OFF;
    private boolean active;
    private boolean oneOff;
    private String lastNow;
    private boolean readFailureLogged;
    private boolean sampleFailureLogged;
    private long nextPoll = Long.MIN_VALUE;
    private long nextProfile;
    private long lastStarted;

    VisionaryProfiler(VisionaryAgent agent, List<String> appPackages, ClassLoader loader) {
        this(agent.store(), agent.origin(),
                agent.writer() == null ? e -> { } : agent.writer()::enqueue,
                agent.enabled() && agent.config() != null && agent.config().profile(),
                new ObjectMapper(), samplers(new FrameNamer(appPackages, loader)));
    }

    VisionaryProfiler(Store store, Origin origin, Consumer<ProfileEntry> sink, boolean allowed, ObjectMapper json,
            CpuSampler[] samplers) {
        this.store = store;
        this.origin = origin;
        this.sink = sink;
        this.allowed = allowed && store != null;
        this.json = json;
        this.sampler = samplers[0];
        this.fallback = samplers.length > 1 ? samplers[1] : null;
    }

    /** JFR first when the runtime has it, else thread sampling; the second is the fallback. */
    private static CpuSampler[] samplers(FrameNamer namer) {
        ThreadSampler threads = new ThreadSampler(namer);
        return JfrSampler.available() ? new CpuSampler[] {new JfrSampler(namer), threads} : new CpuSampler[] {threads};
    }

    @Override
    public synchronized void start() {
        if (running) {
            return;
        }
        running = true;
        if (!allowed) {
            return;
        }
        executor = Executors.newSingleThreadScheduledExecutor(r -> {
            Thread t = new Thread(r, "visionary-profiler");
            t.setDaemon(true);
            return t;
        });
        executor.scheduleWithFixedDelay(this::tickSafely, 0, 1, TimeUnit.SECONDS);
    }

    @Override
    public synchronized void stop() {
        if (!running) {
            return;
        }
        running = false;
        if (executor != null) {
            executor.shutdownNow();
            try {
                executor.awaitTermination(5, TimeUnit.SECONDS);
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
            }
            executor = null;
        }
    }

    @Override
    public boolean isRunning() {
        return running;
    }

    /** Starts after the agent (so the writer runs) and stops before it. */
    @Override
    public int getPhase() {
        return SmartLifecycle.DEFAULT_PHASE - 4095;
    }

    private void tickSafely() {
        try {
            tick(System.nanoTime(), Instant.now());
        } catch (RuntimeException | LinkageError e) {
            log.debug("visionary: profiler tick failed", e);
        }
    }

    /** One step of the schedule: read the settings when due, then profile when due. */
    void tick(long nanos, Instant wall) {
        if (!allowed) {
            return;
        }
        if (nanos - nextPoll >= 0 || nextPoll == Long.MIN_VALUE) {
            poll(nanos, wall);
            nextPoll = nanos + POLL_INTERVAL.toNanos();
        }
        if (oneOff) {
            oneOff = false;
            capture(1);
            return;
        }
        if (active && nanos - nextProfile >= 0) {
            nextProfile = nanos + settings.intervalS() * 1_000_000_000L;
            long started = ProfileTally.startedCount();
            if (started != lastStarted) {
                capture(FoldedStacks.MIN_SAMPLES);
            }
        }
    }

    /** Reads the settings; a missing table or a failed read means off, logged once. */
    void poll(long nanos, Instant wall) {
        ProfileSettings s;
        try {
            Map<String, String> values = new HashMap<>();
            for (String[] row : store.select(SETTINGS_QUERY)) {
                if (row.length >= 2 && row[0] != null && row[1] != null) {
                    values.put(row[0], row[1]);
                }
            }
            s = ProfileSettings.parse(values, json);
        } catch (Exception e) {
            if (!readFailureLogged) {
                readFailureLogged = true;
                log.info("visionary: CPU profiling off: cannot read visionary_settings (" + e.getMessage()
                        + "); it is created by the Visionary server");
            }
            s = ProfileSettings.OFF;
        }
        boolean included = s.includes(origin == null ? null : origin.service());
        if (ProfileSettings.freshNow(s.now(), lastNow, processStart, wall) && included) {
            oneOff = true;
        }
        if (s.now() != null) {
            lastNow = s.now();
        }
        boolean nowActive = s.enabled() && included;
        long intervalNanos = s.intervalS() * 1_000_000_000L;
        if (nowActive && !active) {
            // The first profile waits one interval and needs a request after this point.
            nextProfile = nanos + intervalNanos;
            lastStarted = ProfileTally.startedCount();
            log.info("visionary: CPU profiling on (" + s.durationMs() + "ms every " + s.intervalS() + "s, "
                    + sampler.describe() + ")");
        } else if (!nowActive && active) {
            log.info("visionary: CPU profiling off");
        } else if (nowActive && nextProfile - (nanos + intervalNanos) > 0) {
            nextProfile = nanos + intervalNanos;
        }
        active = nowActive;
        settings = s;
    }

    boolean active() {
        return active;
    }

    boolean oneOffPending() {
        return oneOff;
    }

    ProfileSettings settings() {
        return settings;
    }

    /**
     * Samples for {@code duration_ms} and hands the profile to the writer when it has at least
     * {@code minSamples} samples; {@code routes} is {@code {}} when no request finished. A profile
     * interrupted by shutdown is dropped.
     */
    void capture(int minSamples) {
        if (!sampling.compareAndSet(false, true)) {
            return;
        }
        lastStarted = ProfileTally.startedCount();
        FoldedStacks stacks = new FoldedStacks();
        Instant startedAt = Instant.now();
        long t0 = System.nanoTime();
        Map<String, Long> routes;
        ProfileTally.begin();
        try {
            sample(stacks);
        } finally {
            routes = ProfileTally.end();
            sampling.set(false);
        }
        double measuredMs = (System.nanoTime() - t0) / 1_000_000.0;
        if (Thread.currentThread().isInterrupted() || stacks.samples() < minSamples) {
            return;
        }
        String routesJson;
        try {
            routesJson = json.writeValueAsString(routes);
        } catch (JsonProcessingException e) {
            return;
        }
        sink.accept(new ProfileEntry(new Object[] {
            Values.timestamp(startedAt),
            origin == null ? null : origin.service(),
            origin == null ? "unknown" : origin.instance(),
            origin == null ? null : origin.version(),
            "java",
            Values.round(measuredMs, 3),
            stacks.samples(),
            routesJson,
            stacks.render(),
        }));
    }

    private void sample(FoldedStacks stacks) {
        try {
            sampler.sample(settings.durationMs(), stacks);
        } catch (Exception | LinkageError e) {
            if (!sampleFailureLogged) {
                sampleFailureLogged = true;
                log.warn("visionary: CPU profile with " + sampler.describe() + " failed: " + e
                        + (fallback != null ? "; using thread sampling from now on" : ""));
            }
            if (fallback != null && sampler != fallback) {
                sampler = fallback;
            }
        }
    }
}
