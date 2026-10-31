package io.github.styropyr0.visionary;

/**
 * One counter series from {@link Visionary#counter(String, String...)}. Rows hold
 * the increase over each interval. Does nothing when the name is empty or over
 * its series cap.
 */
public final class VisionaryCounter {

    private final Metrics.Series series;

    VisionaryCounter(Metrics.Series series) {
        this.series = series;
    }

    /** Adds {@code n} (ignored unless positive and finite). */
    public void add(double n) {
        if (series != null) {
            series.add(n);
        }
    }

    public void inc() {
        add(1);
    }
}
