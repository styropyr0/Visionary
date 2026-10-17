package io.github.styropyr0.visionary;

import java.util.Set;
import java.util.concurrent.Callable;

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
 */
public final class Visionary {

    /** Request attribute holding the {@link VisionaryUser} for the current request. */
    public static final String USER_ATTRIBUTE = Visionary.class.getName() + ".USER";

    private static final Set<String> KINDS = Set.of("db", "http", "cache", "queue", "custom");

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
     * @param kind {@code db}, {@code http}, {@code cache}, {@code queue} or {@code custom}
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
}
