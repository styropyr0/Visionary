package io.github.styropyr0.visionary;

import java.util.Arrays;

import jakarta.servlet.http.HttpServletRequest;

import org.springframework.web.util.ContentCachingRequestWrapper;

/**
 * Caches up to the body cap of what the application reads, and records
 * whether the body went past it.
 */
final class CapturingRequest extends ContentCachingRequestWrapper {

    private final int limit;
    private boolean overflowed;

    CapturingRequest(HttpServletRequest request, int limit) {
        super(request, limit);
        this.limit = limit;
    }

    @Override
    protected void handleContentOverflow(int contentCacheLimit) {
        overflowed = true;
    }

    /** The cached body, at most the cap. */
    byte[] body() {
        byte[] content = getContentAsByteArray();
        // Form bodies rebuilt from request parameters are not bounded by the cache limit.
        return content.length > limit ? Arrays.copyOf(content, limit) : content;
    }

    boolean truncated() {
        return overflowed || getContentAsByteArray().length > limit;
    }

    long cachedSize() {
        return getContentAsByteArray().length;
    }
}
