package io.github.styropyr0.visionary;

/**
 * One histogram series from {@link Visionary#histogram(String, String...)}. Rows
 * hold each interval's count, sum, min, max and bucket counts.
 */
public final class VisionaryHistogram {

    private final Metrics.Series series;

    VisionaryHistogram(Metrics.Series series) {
        this.series = series;
    }

    /** Records one value (ignored when NaN or infinite). */
    public void observe(double v) {
        if (series != null) {
            series.observe(v);
        }
    }
}
