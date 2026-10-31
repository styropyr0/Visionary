package io.github.styropyr0.visionary;

import java.time.Duration;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.Semaphore;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicInteger;

/** Runs batches on several threads when the store allows parallel writes */
final class WriterFlushes {

    private final Store store;
    private final String name;
    private final long stackSize;
    private ExecutorService pool;
    private Semaphore slots;

    /** @param stackSize as the writer thread's, since rows are built where they are written */
    WriterFlushes(Store store, String name, long stackSize) {
        this.store = store;
        this.name = name;
        this.stackSize = stackSize;
    }

    /** Called from the writer thread only */
    void run(Runnable write) {
        int n = store == null ? 1 : store.parallelWrites();
        if (n <= 1) {
            write.run();
            return;
        }
        if (pool == null) {
            AtomicInteger count = new AtomicInteger();
            pool = Executors.newFixedThreadPool(n, r -> {
                Thread t = new Thread(null, r, name + "-" + count.incrementAndGet(), stackSize);
                t.setDaemon(true);
                return t;
            });
            slots = new Semaphore(n);
        }
        slots.acquireUninterruptibly();
        try {
            pool.execute(() -> {
                try {
                    write.run();
                } finally {
                    slots.release();
                }
            });
        } catch (RuntimeException e) {
            slots.release();
            throw e;
        }
    }

    void finish(Duration timeout) {
        if (pool == null) {
            return;
        }
        pool.shutdown();
        try {
            pool.awaitTermination(timeout.toMillis(), TimeUnit.MILLISECONDS);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
    }
}
