package io.github.styropyr0.visionary;

import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.junit.jupiter.api.Assumptions.assumeTrue;

import java.util.List;

import org.junit.jupiter.api.Test;

/** Both samplers against a thread that burns CPU in a known method. */
class SamplerTest {

    static volatile long sink;

    static void burnCpu(long untilNanos) {
        long x = 0;
        while (System.nanoTime() < untilNanos) {
            for (int i = 0; i < 10_000; i++) {
                x += i * 31L ^ (x >>> 3);
            }
        }
        sink = x;
    }

    private static String profile(CpuSampler sampler, long ms) throws Exception {
        Thread hot = new Thread(() -> burnCpu(System.nanoTime() + (ms + 3000) * 1_000_000L), "hot-worker");
        hot.setDaemon(true);
        hot.start();
        Thread idle = new Thread(() -> {
            try {
                Thread.sleep(ms + 3000);
            } catch (InterruptedException ignored) {
                // done
            }
        }, "idle-worker");
        idle.setDaemon(true);
        idle.start();
        FoldedStacks out = new FoldedStacks();
        sampler.sample(ms, out);
        hot.interrupt();
        idle.interrupt();
        assertTrue(out.samples() >= FoldedStacks.MIN_SAMPLES, "samples: " + out.samples());
        return out.render();
    }

    private final FrameNamer namer = new FrameNamer(List.of("io.github.styropyr0.visionary"), getClass().getClassLoader());

    @Test
    void jfrSeesTheHotMethodAndNotTheSleepingThread() throws Exception {
        assumeTrue(JfrSampler.available(), "JFR not available");
        String folded = profile(new JfrSampler(namer), 800);
        assertTrue(folded.contains("SamplerTest.burnCpu "), folded);
        assertTrue(folded.contains("java.lang.Thread.run std:java/lang/Thread.java"), folded);
        assertTrue(!folded.contains("Thread.sleep"), folded);
    }

    @Test
    void threadSamplingSeesTheHotMethodAndNotTheSleepingThread() throws Exception {
        String folded = profile(new ThreadSampler(namer), 800);
        assertTrue(folded.contains("SamplerTest.burnCpu "), folded);
        assertTrue(!folded.contains("Thread.sleep"), folded);
    }
}
