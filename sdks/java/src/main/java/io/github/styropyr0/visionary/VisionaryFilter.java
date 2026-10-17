package io.github.styropyr0.visionary;

import java.io.IOException;
import java.time.Instant;
import java.util.Map;

import jakarta.servlet.AsyncEvent;
import jakarta.servlet.AsyncListener;
import jakarta.servlet.FilterChain;
import jakarta.servlet.RequestDispatcher;
import jakarta.servlet.ServletException;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.servlet.http.HttpServletResponse;

import org.springframework.web.filter.OncePerRequestFilter;
import org.springframework.web.servlet.DispatcherServlet;
import org.springframework.web.servlet.HandlerMapping;

/**
 * Logs each request, counts throughput and records unhandled exceptions.
 * Registered outermost so failures are logged with the status the client got.
 * Async requests are finished from an {@link AsyncListener} when the response
 * completes.
 */
final class VisionaryFilter extends OncePerRequestFilter {

    private static final String STATIC_CATCH_ALL = "/**";

    private final HttpCounters counters;
    private final RequestWriter writer;
    private final Telemetry telemetry;
    private final String pathPrefix;
    private final boolean bodies;
    private final int maxBodyBytes;

    /**
     * @param writer    null when request logging is off
     * @param telemetry null when spans and errors are both off
     */
    VisionaryFilter(HttpCounters counters, RequestWriter writer, Telemetry telemetry, VisionaryConfig cfg) {
        this.counters = counters;
        this.writer = writer;
        this.telemetry = telemetry;
        this.pathPrefix = cfg == null ? "" : cfg.pathPrefix();
        this.bodies = cfg != null && cfg.logBodies();
        this.maxBodyBytes = cfg == null ? 8192 : cfg.maxBodyBytes();
    }

    @Override
    protected void doFilterInternal(HttpServletRequest request, HttpServletResponse response, FilterChain chain)
            throws ServletException, IOException {
        counters.begin();
        if (writer == null || !request.getRequestURI().startsWith(pathPrefix)) {
            countOnly(request, response, chain);
            return;
        }

        Instant startedAt = Instant.now();
        long start = System.nanoTime();
        String requestId = HttpCapture.requestId(request);
        response.setHeader(HttpCapture.REQUEST_ID_HEADER, requestId);
        Map<String, String> headers = HttpCapture.headers(request);
        String requestType = request.getContentType();

        CapturingRequest capturedRequest = bodies && HttpCapture.capturable(requestType)
                ? new CapturingRequest(request, maxBodyBytes) : null;
        CapturingResponse capturedResponse = new CapturingResponse(response, bodies, maxBodyBytes);
        RequestScope scope = telemetry != null ? new RequestScope(requestId, start, telemetry.spanMax) : null;
        Capture capture = new Capture(request, capturedRequest, capturedResponse, requestId, startedAt, start,
                headers, requestType, scope);

        Runnable finish = new Runnable() {
            private boolean done;

            @Override
            public synchronized void run() {
                if (done) {
                    return;
                }
                done = true;
                finish(capture, capturedResponse.getStatus(), null);
            }
        };

        RequestScope previous = RequestScope.enter(scope);
        Throwable thrown = null;
        try {
            chain.doFilter(capturedRequest != null ? capturedRequest : request, capturedResponse);
        } catch (Throwable t) {
            thrown = t;
            throw t;
        } finally {
            RequestScope.restore(previous);
            if (thrown != null) {
                // The container turns the exception into a 500 unless something was already sent.
                int status = response.isCommitted() ? capturedResponse.getStatus() : 500;
                finish(capture, status, thrown);
            } else if (request.isAsyncStarted()) {
                request.getAsyncContext().addListener(new CompletionListener(finish));
            } else {
                finish.run();
            }
        }
    }

    private void countOnly(HttpServletRequest request, HttpServletResponse response, FilterChain chain)
            throws ServletException, IOException {
        Throwable thrown = null;
        try {
            chain.doFilter(request, response);
        } catch (Throwable t) {
            thrown = t;
            throw t;
        } finally {
            if (thrown != null) {
                int status = response.isCommitted() ? response.getStatus() : 500;
                counters.end(status);
                recordUnlinked(request, status, thrown);
            } else if (request.isAsyncStarted()) {
                request.getAsyncContext().addListener(new CompletionListener(() -> {
                    counters.end(response.getStatus());
                    recordUnlinked(request, response.getStatus(), null);
                }));
            } else {
                counters.end(response.getStatus());
                recordUnlinked(request, response.getStatus(), null);
            }
        }
    }

    private void finish(Capture c, int status, Throwable thrown) {
        counters.end(status);
        HttpServletRequest request = c.request;
        try {
            double durationMs = (System.nanoTime() - c.start) / 1_000_000.0;
            byte[] requestBody = c.capturedRequest != null ? c.capturedRequest.body() : null;
            long requestSize = Math.max(request.getContentLengthLong(),
                    c.capturedRequest != null ? c.capturedRequest.cachedSize() : 0);
            String route = route(request, status);
            VisionaryUser user = user(request);
            RequestScope.Finished collected = c.scope != null ? c.scope.finish() : null;
            if (collected != null) {
                String userId = user == null ? null : user.id();
                ErrorEntry unhandled = unhandled(request, status, thrown);
                if (unhandled != null) {
                    telemetry.enqueue(unhandled.withRequest(c.requestId, request.getMethod(), route,
                            request.getRequestURI(), userId));
                }
                for (ErrorEntry e : collected.errors()) {
                    telemetry.enqueue(e.withRequest(c.requestId, request.getMethod(), route,
                            request.getRequestURI(), userId));
                }
                if (!telemetry.spans) {
                    collected = null;
                }
            }
            CapturingResponse response = c.capturedResponse;
            writer.log(new RequestEntry(
                    c.requestId,
                    c.startedAt,
                    request.getMethod(),
                    request.getRequestURI(),
                    route,
                    request.getQueryString(),
                    status,
                    durationMs,
                    HttpCapture.clientIp(request),
                    request.getHeader("User-Agent"),
                    user,
                    c.headers,
                    c.requestType,
                    requestBody,
                    c.capturedRequest != null && c.capturedRequest.truncated(),
                    Math.max(requestSize, 0),
                    response.getContentType(),
                    response.body(),
                    response.truncated(),
                    response.size(),
                    collected));
        } catch (RuntimeException e) {
            // Logging must never affect the response.
            logger.debug("visionary: could not capture request", e);
        }
    }

    /** An unhandled exception from a request that is not logged: stored without a request id. */
    private void recordUnlinked(HttpServletRequest request, int status, Throwable thrown) {
        if (telemetry == null) {
            return;
        }
        try {
            ErrorEntry e = unhandled(request, status, thrown);
            if (e != null) {
                VisionaryUser user = user(request);
                telemetry.enqueue(e.withRequest(null, request.getMethod(), route(request, status),
                        request.getRequestURI(), user == null ? null : user.id()));
            }
        } catch (RuntimeException e) {
            logger.debug("visionary: could not capture error", e);
        }
    }

    /**
     * The exception that escaped the chain, else the one Spring MVC resolved into a
     * 5xx response, as an unhandled ({@code handled = 0}) error; null if neither.
     */
    private ErrorEntry unhandled(HttpServletRequest request, int status, Throwable thrown) {
        Throwable error = thrown;
        if (error == null && status >= 500) {
            Object resolved = request.getAttribute(DispatcherServlet.EXCEPTION_ATTRIBUTE);
            if (!(resolved instanceof Throwable)) {
                resolved = request.getAttribute(RequestDispatcher.ERROR_EXCEPTION);
            }
            error = resolved instanceof Throwable t ? t : null;
        }
        // FrameworkServlet wraps handler exceptions; the application's own is the interesting one.
        while (error instanceof ServletException && error.getCause() != null) {
            error = error.getCause();
        }
        return error == null ? null : telemetry.newError(error, false);
    }

    private static String route(HttpServletRequest request, int status) {
        Object pattern = request.getAttribute(HandlerMapping.BEST_MATCHING_PATTERN_ATTRIBUTE);
        if (!(pattern instanceof String route)) {
            return null;
        }
        // Unmatched paths fall through to the static resource handler's catch-all; that is no route.
        return status == 404 && STATIC_CATCH_ALL.equals(route) ? null : route;
    }

    private static VisionaryUser user(HttpServletRequest request) {
        if (request.getAttribute(Visionary.USER_ATTRIBUTE) instanceof VisionaryUser u) {
            return u;
        }
        if (request.getAttribute(SecurityUserFilter.ATTRIBUTE) instanceof VisionaryUser u) {
            return u;
        }
        return null;
    }

    /** What the request thread captured before the handler ran. */
    private record Capture(HttpServletRequest request, CapturingRequest capturedRequest,
            CapturingResponse capturedResponse, String requestId, Instant startedAt, long start,
            Map<String, String> headers, String requestType, RequestScope scope) {
    }

    /** Runs the callback once the async response is complete, following re-dispatches. */
    private record CompletionListener(Runnable onComplete) implements AsyncListener {

        @Override
        public void onComplete(AsyncEvent event) {
            onComplete.run();
        }

        @Override
        public void onTimeout(AsyncEvent event) {
            // onComplete follows
        }

        @Override
        public void onError(AsyncEvent event) {
            // onComplete follows
        }

        @Override
        public void onStartAsync(AsyncEvent event) {
            event.getAsyncContext().addListener(this);
        }
    }
}
