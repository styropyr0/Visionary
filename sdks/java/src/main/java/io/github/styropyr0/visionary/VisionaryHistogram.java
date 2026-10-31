package io.github.styropyr0.visionary;

/** A histogram series from {@link Visionary#histogram(String, String...)} */
public final class VisionaryHistogram {

    private final Metrics.Series series;

    VisionaryHistogram(Metrics.Series series) {
        this.series = series;
    }

    /** Records one value, ignored when NaN or infinite */
    public void observe(double v) {
        if (series != null) {
            series.observe(v);
        }
    }
}
