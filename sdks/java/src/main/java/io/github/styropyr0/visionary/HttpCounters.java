package io.github.styropyr0.visionary;

import java.util.concurrent.atomic.AtomicLong;

/** Throughput counters fed by the filter and read by the system sampler. */
final class HttpCounters {

    final AtomicLong requests = new AtomicLong();
    final AtomicLong errors = new AtomicLong();
    final AtomicLong inFlight = new AtomicLong();

    void begin() {
        inFlight.incrementAndGet();
    }

    void end(int status) {
        inFlight.decrementAndGet();
        requests.incrementAndGet();
        if (status >= 500) {
            errors.incrementAndGet();
        }
    }
}
