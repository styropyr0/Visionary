package io.github.styropyr0.visionary;

/** A gauge series from {@link Visionary#gauge(String, String...)} */
public final class VisionaryGauge {

    private final Metrics.Series series;

    VisionaryGauge(Metrics.Series series) {
        this.series = series;
    }

    /** Sets the gauge, ignored when {@code v} is NaN or infinite */
    public void set(double v) {
        if (series != null) {
            series.set(v);
        }
    }

    /** Moves the gauge by {@code delta}, from 0 when it was never set */
    public void add(double delta) {
        if (series != null) {
            series.move(delta);
        }
    }
}
