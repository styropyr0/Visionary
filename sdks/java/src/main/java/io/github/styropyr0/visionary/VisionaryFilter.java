package io.github.styropyr0.visionary;

import java.io.IOException;
import java.time.Instant;
import java.util.Map;

import jakarta.servlet.AsyncEvent;
import jakarta.servlet.AsyncListener;
import jakarta.servlet.FilterChain;
import jakarta.servlet.ServletException;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.servlet.http.HttpServletResponse;

import org.springframework.web.filter.OncePerRequestFilter;
import org.springframework.web.servlet.HandlerMapping;

/**
 * Logs each request and counts throughput. Registered outermost so failures
 * are logged with the status the client got. Async requests are finished from
 * an {@link AsyncListener} when the response completes.
 */
final class VisionaryFilter extends OncePerRequestFilter {

    private static final String STATIC_CATCH_ALL = "/**";

    private final HttpCounters counters;
    private final RequestWriter writer;
    private final String pathPrefix;
    private final boolean bodies;
    private final int maxBodyBytes;

    VisionaryFilter(HttpCounters counters, RequestWriter writer, VisionaryConfig cfg) {
        this.counters = counters;
        this.writer = writer;
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

        Runnable finish = new Runnable() {
            private boolean done;

            @Override
            public synchronized void run() {
                if (done) {
                    return;
                }
                done = true;
                finish(request, capturedRequest, capturedResponse, requestId, startedAt, start, headers,
                        requestType, capturedResponse.getStatus());
            }
        };

        boolean failed = true;
        try {
            chain.doFilter(capturedRequest != null ? capturedRequest : request, capturedResponse);
            failed = false;
        } finally {
            if (failed) {
                // The container turns the exception into a 500 unless something was already sent.
                int status = response.isCommitted() ? capturedResponse.getStatus() : 500;
                finish(request, capturedRequest, capturedResponse, requestId, startedAt, start, headers,
                        requestType, status);
            } else if (request.isAsyncStarted()) {
                request.getAsyncContext().addListener(new CompletionListener(finish));
            } else {
                finish.run();
            }
        }
    }

    private void countOnly(HttpServletRequest request, HttpServletResponse response, FilterChain chain)
            throws ServletException, IOException {
        boolean failed = true;
        try {
            chain.doFilter(request, response);
            failed = false;
        } finally {
            if (failed) {
                counters.end(response.isCommitted() ? response.getStatus() : 500);
            } else if (request.isAsyncStarted()) {
                request.getAsyncContext().addListener(new CompletionListener(() -> counters.end(response.getStatus())));
            } else {
                counters.end(response.getStatus());
            }
        }
    }

    private void finish(HttpServletRequest request, CapturingRequest capturedRequest,
            CapturingResponse capturedResponse, String requestId, Instant startedAt, long start,
            Map<String, String> headers, String requestType, int status) {
        counters.end(status);
        try {
            double durationMs = (System.nanoTime() - start) / 1_000_000.0;
            byte[] requestBody = capturedRequest != null ? capturedRequest.body() : null;
            long requestSize = Math.max(request.getContentLengthLong(),
                    capturedRequest != null ? capturedRequest.cachedSize() : 0);
            writer.log(new RequestEntry(
                    requestId,
                    startedAt,
                    request.getMethod(),
                    request.getRequestURI(),
                    route(request, status),
                    request.getQueryString(),
                    status,
                    durationMs,
                    HttpCapture.clientIp(request),
                    request.getHeader("User-Agent"),
                    user(request),
                    headers,
                    requestType,
                    requestBody,
                    capturedRequest != null && capturedRequest.truncated(),
                    Math.max(requestSize, 0),
                    capturedResponse.getContentType(),
                    capturedResponse.body(),
                    capturedResponse.truncated(),
                    capturedResponse.size()));
        } catch (RuntimeException e) {
            // Logging must never affect the response.
            logger.debug("visionary: could not capture request", e);
        }
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
