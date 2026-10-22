package io.github.styropyr0.visionary;

/** Samples the stacks of threads running Java code for a while. */
interface CpuSampler {

    /** Fills {@code out}; returns early, without error, when interrupted. */
    void sample(long durationMs, FoldedStacks out) throws Exception;

    /** For the log line: {@code JFR} or {@code thread sampling}. */
    String describe();

    /** Threads left out of profiles: the SDK's own and the JDK's flight recorder. */
    static boolean skipThread(String name) {
        return name == null || name.startsWith("visionary-") || name.startsWith("JFR ");
    }
}
