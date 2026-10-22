package io.github.styropyr0.visionary;

import java.lang.management.ManagementFactory;
import java.lang.management.ThreadInfo;
import java.lang.management.ThreadMXBean;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;

/**
 * The fallback when JDK Flight Recorder is unavailable: every 10 ms, the stacks
 * of RUNNABLE threads whose CPU time advanced since the previous tick, from
 * {@link ThreadMXBean}. Stacks are taken at safepoints, so hot loops without one
 * are attributed to the nearest safepoint, and each tick costs more than JFR.
 */
final class ThreadSampler implements CpuSampler {

    static final long PERIOD_MS = 10;
    private static final int MAX_DEPTH = 256;

    private final FrameNamer namer;
    private final ThreadMXBean threads = ManagementFactory.getThreadMXBean();

    ThreadSampler(FrameNamer namer) {
        this.namer = namer;
    }

    @Override
    public String describe() {
        return "thread sampling";
    }

    @Override
    public void sample(long durationMs, FoldedStacks out) {
        boolean cpuTime = threads.isThreadCpuTimeSupported();
        if (cpuTime && !threads.isThreadCpuTimeEnabled()) {
            try {
                threads.setThreadCpuTimeEnabled(true);
            } catch (UnsupportedOperationException | SecurityException e) {
                cpuTime = false;
            }
        }
        long self = Thread.currentThread().getId();
        Map<Long, Long> lastCpu = new HashMap<>();
        long deadline = System.nanoTime() + durationMs * 1_000_000L;
        boolean first = true;
        while (System.nanoTime() < deadline) {
            long[] ids = threads.getAllThreadIds();
            List<Long> busy = new ArrayList<>();
            for (long id : ids) {
                if (id == self) {
                    continue;
                }
                if (!cpuTime) {
                    busy.add(id);
                    continue;
                }
                long cpu = threads.getThreadCpuTime(id);
                Long prev = lastCpu.put(id, cpu);
                if (cpu > 0 && prev != null && cpu > prev) {
                    busy.add(id);
                }
            }
            if (!first && !busy.isEmpty()) {
                long[] wanted = busy.stream().mapToLong(Long::longValue).toArray();
                for (ThreadInfo info : threads.getThreadInfo(wanted, MAX_DEPTH)) {
                    if (info == null || info.getThreadState() != Thread.State.RUNNABLE
                            || CpuSampler.skipThread(info.getThreadName())) {
                        continue;
                    }
                    add(info.getStackTrace(), out);
                }
            }
            first = false;
            try {
                Thread.sleep(PERIOD_MS);
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
                return;
            }
        }
    }

    private void add(StackTraceElement[] stack, FoldedStacks out) {
        if (stack.length == 0) {
            return;
        }
        List<String> names = new ArrayList<>(stack.length + 1);
        if (stack.length >= MAX_DEPTH) {
            names.add("(truncated)");
        }
        for (int i = stack.length - 1; i >= 0; i--) {
            StackTraceElement f = stack[i];
            // Only the sampled frame keeps its line: callers are one frame per method.
            names.add(namer.name(f.getClassName(), f.getMethodName(), i == 0 ? f.getLineNumber() : 0, f.getFileName()));
        }
        out.add(names);
    }
}
