package io.github.styropyr0.visionary;

import java.lang.management.ManagementFactory;
import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicReference;

import javax.management.ObjectName;

import jdk.jfr.FlightRecorder;
import jdk.jfr.consumer.RecordedEvent;
import jdk.jfr.consumer.RecordedFrame;
import jdk.jfr.consumer.RecordedStackTrace;
import jdk.jfr.consumer.RecordedThread;
import jdk.jfr.consumer.RecordingStream;

/**
 * CPU samples from JDK Flight Recorder's {@code jdk.ExecutionSample} event every
 * 10 ms. The JVM records it only for threads running Java code, so idle,
 * sleeping, blocked and native-waiting threads are left out by construction,
 * and it samples without safepoint bias. Only touch this class after
 * {@link #available()} returned true: it links against the {@code jdk.jfr} module.
 */
final class JfrSampler implements CpuSampler {

    static final Duration PERIOD = Duration.ofMillis(10);
    /** JFR's default of 64 frames cuts Spring stacks short; set once, before JFR first starts. */
    private static final String STACK_DEPTH = "stackdepth=256";
    private static volatile boolean configured;

    private final FrameNamer namer;

    JfrSampler(FrameNamer namer) {
        this.namer = namer;
    }

    static boolean available() {
        try {
            Class.forName("jdk.jfr.consumer.RecordingStream", false, JfrSampler.class.getClassLoader());
            return FlightRecorder.isAvailable();
        } catch (Throwable t) {
            return false;
        }
    }

    @Override
    public String describe() {
        return "JFR";
    }

    @Override
    public void sample(long durationMs, FoldedStacks out) throws Exception {
        configureStackDepth();
        AtomicReference<Instant> end = new AtomicReference<>();
        AtomicReference<CountDownLatch> flushedAfterEnd = new AtomicReference<>();
        try (RecordingStream rs = new RecordingStream()) {
            rs.enable("jdk.ExecutionSample").withPeriod(PERIOD).withStackTrace();
            rs.setReuse(true);
            rs.onEvent("jdk.ExecutionSample", e -> add(e, end.get(), out));
            rs.onFlush(() -> {
                CountDownLatch l = flushedAfterEnd.get();
                if (l != null) {
                    l.countDown();
                }
            });
            rs.startAsync();
            try {
                Thread.sleep(durationMs);
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
                return;
            }
            end.set(Instant.now());
            // Events reach the stream at its next flush (about once a second); wait for it.
            CountDownLatch flushed = new CountDownLatch(1);
            flushedAfterEnd.set(flushed);
            if (!flushed.await(3, TimeUnit.SECONDS)) {
                return;
            }
        }
    }

    private void add(RecordedEvent e, Instant end, FoldedStacks out) {
        if (end != null && e.getStartTime().isAfter(end)) {
            return;
        }
        RecordedThread thread = e.getThread("sampledThread");
        if (thread != null && CpuSampler.skipThread(thread.getJavaName())) {
            return;
        }
        RecordedStackTrace st = e.getStackTrace();
        if (st == null) {
            return;
        }
        List<RecordedFrame> frames = st.getFrames();
        List<String> names = new ArrayList<>(frames.size() + 1);
        if (st.isTruncated()) {
            names.add("(truncated)");
        }
        for (int i = frames.size() - 1; i >= 0; i--) {
            RecordedFrame f = frames.get(i);
            if (!f.isJavaFrame() || f.getMethod() == null) {
                continue;
            }
            // Only the sampled frame keeps its line: callers are one frame per method.
            names.add(namer.name(f.getMethod().getType().getName(), f.getMethod().getName(),
                    i == 0 ? f.getLineNumber() : 0, null));
        }
        out.add(names);
    }

    /** Raises JFR's stack depth through the DiagnosticCommand MBean; harmless when JFR already runs. */
    private static void configureStackDepth() {
        if (configured) {
            return;
        }
        configured = true;
        try {
            ManagementFactory.getPlatformMBeanServer().invoke(
                    new ObjectName("com.sun.management:type=DiagnosticCommand"), "jfrConfigure",
                    new Object[] {new String[] {STACK_DEPTH}}, new String[] {String[].class.getName()});
        } catch (Exception | LinkageError ignored) {
            // the default depth still works; deep stacks start with "(truncated)"
        }
    }
}
