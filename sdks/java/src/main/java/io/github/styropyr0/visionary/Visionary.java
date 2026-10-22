package io.github.styropyr0.visionary;

import java.util.Set;
import java.util.concurrent.Callable;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.SynchronousQueue;
import java.util.concurrent.ThreadPoolExecutor;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicInteger;

import jakarta.servlet.http.HttpServletRequest;

/**
 * Application-facing API of the Visionary collector. Every method is harmless
 * when Visionary is not configured.
 *
 * <p>Attach the signed-in user to the request being logged, usually from auth
 * code once the user is known:
 *
 * <pre>{@code
 * Visionary.setUser(request, user.getId(), user.getUsername(), user.getRole());
 * }</pre>
 *
 * A user set here wins over the Spring Security principal.
 *
 * <p>Time a call the automatic instrumentation doesn't see, and record an error
 * the application handled itself:
 *
 * <pre>{@code
 * try (VisionarySpan span = Visionary.span("cache", "GET session")) {
 *     return cache.get(key);
 * } catch (CacheException e) {
 *     Visionary.captureException(e);
 *     return null;
 * }
 * }</pre>
 *
 * <p>Run work the response does not wait for, keeping it linked to the request:
 *
 * <pre>{@code
 * Visionary.background("send welcome email", () -> mailer.sendWelcome(user));
 * executor.submit(Visionary.wrap("rebuild search index", () -> index.rebuild(id)));
 * }</pre>
 */
public final class Visionary {

    /** Request attribute holding the {@link VisionaryUser} for the current request. */
    public static final String USER_ATTRIBUTE = Visionary.class.getName() + ".USER";

    private static final Set<String> KINDS = Set.of("db", "http", "cache", "queue", "custom", "background");

    private Visionary() {
    }

    public static void setUser(HttpServletRequest request, String id, String handle, String role) {
        if (request == null || id == null || id.isEmpty()) {
            return;
        }
        request.setAttribute(USER_ATTRIBUTE, new VisionaryUser(id, handle, role));
    }

    public static void setUser(HttpServletRequest request, String id) {
        setUser(request, id, null, null);
    }

    /**
     * Starts a span; close it (try-with-resources) when the call is done.
     *
     * @param kind {@code db}, {@code http}, {@code cache}, {@code queue}, {@code background} or {@code custom}
     *             (anything else is stored as {@code custom})
     * @param name a short, low-cardinality name used for grouping, e.g. {@code GET session}
     */
    public static VisionarySpan span(String kind, String name) {
        String k = kind != null && KINDS.contains(kind) ? kind : "custom";
        VisionarySpan span = Telemetry.start(k, name == null || name.isBlank() ? k : name);
        return span == null ? VisionarySpan.NOOP : span;
    }

    /** Records an exception the application handled ({@code handled = 1}), linked to the current request. */
    public static void captureException(Throwable error) {
        Telemetry t = Telemetry.active();
        if (t != null && error != null) {
            t.capture(error, true);
        }
    }

    /** The task, running as part of the current request on whichever thread executes it. */
    public static Runnable wrap(Runnable task) {
        RequestScope scope = RequestScope.current();
        if (scope == null || task == null) {
            return task;
        }
        return () -> {
            RequestScope previous = RequestScope.enter(scope);
            try {
                task.run();
            } finally {
                RequestScope.restore(previous);
            }
        };
    }

    /** The task, running as part of the current request on whichever thread executes it. */
    public static <T> Callable<T> wrap(Callable<T> task) {
        RequestScope scope = RequestScope.current();
        if (scope == null || task == null) {
            return task;
        }
        return () -> {
            RequestScope previous = RequestScope.enter(scope);
            try {
                return task.call();
            } finally {
                RequestScope.restore(previous);
            }
        };
    }
    /**
     * Runs the task on a Visionary thread and returns at once. The task stays part
     * of the current request: its calls carry the request's id and offset even after
     * the response was sent, and the task itself is recorded as a {@code background}
     * span named {@code name}, failed with the error if it throws. The future
     * completes with the task, exceptionally if it threw.
     *
     * <p>The threads are daemon threads from a pool that grows as needed and lets
     * idle threads go after a minute, so tasks never queue behind each other and
     * the pool does not keep the JVM alive. Work that must survive a shutdown
     * belongs in a job queue; for your own executor use {@link #wrap(String, Runnable)}.
     */
    public static CompletableFuture<Void> background(String name, Runnable task) {
        if (task == null) {
            return CompletableFuture.completedFuture(null);
        }
        return CompletableFuture.runAsync(wrap(name, task), Background.EXECUTOR);
    }

    /**
     * The task, running as part of the current request on whichever thread executes
     * it and recorded as a {@code background} span named {@code name} (failed with
     * the error if it throws, which is rethrown). Outside a request the span is
     * stored on its own when it takes at least {@code VISIONARY_SPAN_THRESHOLD_MS}.
     */
    public static Runnable wrap(String name, Runnable task) {
        if (task == null) {
            return null;
        }
        Callable<Void> call = wrap(name, () -> {
            task.run();
            return null;
        });
        return () -> {
            try {
                call.call();
            } catch (RuntimeException | Error e) {
                throw e;
            } catch (Exception e) {
                // A Runnable cannot throw a checked exception.
                throw new IllegalStateException(e);
            }
        };
    }

    /** Like {@link #wrap(String, Runnable)}, for a task that returns a value. */
    public static <T> Callable<T> wrap(String name, Callable<T> task) {
        if (task == null) {
            return null;
        }
        RequestScope scope = RequestScope.current();
        String spanName = name == null || name.isBlank() ? "background" : name;
        return () -> {
            RequestScope previous = RequestScope.enter(scope);
            VisionarySpan span = Telemetry.start("background", spanName);
            try {
                return task.call();
            } catch (Throwable t) {
                if (span != null) {
                    span.error(t);
                }
                throw t;
            } finally {
                if (span != null) {
                    span.close();
                }
                RequestScope.restore(previous);
            }
        };
    }

    /** The pool behind {@link #background}, created on first use. */
    private static final class Background {
        static final ExecutorService EXECUTOR = new ThreadPoolExecutor(0, Integer.MAX_VALUE, 60, TimeUnit.SECONDS,
                new SynchronousQueue<>(), daemonThreads());

        private static java.util.concurrent.ThreadFactory daemonThreads() {
            AtomicInteger n = new AtomicInteger();
            return r -> {
                Thread t = new Thread(r, "visionary-background-" + n.incrementAndGet());
                t.setDaemon(true);
                return t;
            };
        }
    }
}
