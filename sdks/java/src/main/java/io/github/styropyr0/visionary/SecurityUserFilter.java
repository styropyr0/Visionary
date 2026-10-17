package io.github.styropyr0.visionary;

import java.io.IOException;

import jakarta.servlet.FilterChain;
import jakarta.servlet.ServletException;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.servlet.http.HttpServletResponse;

import org.springframework.security.authentication.AnonymousAuthenticationToken;
import org.springframework.security.core.Authentication;
import org.springframework.security.core.context.SecurityContextHolder;
import org.springframework.web.filter.OncePerRequestFilter;

/**
 * Records the Spring Security principal name for the request log. It runs
 * innermost, inside Spring Security's filter chain, because the security
 * context is cleared again before control returns to {@link VisionaryFilter}.
 */
final class SecurityUserFilter extends OncePerRequestFilter {

    /** A literal, so reading it never loads this class (and Spring Security) from the main filter. */
    static final String ATTRIBUTE = "io.github.styropyr0.visionary.SecurityUserFilter.USER";

    @Override
    protected void doFilterInternal(HttpServletRequest request, HttpServletResponse response, FilterChain chain)
            throws ServletException, IOException {
        try {
            chain.doFilter(request, response);
        } finally {
            record(request);
        }
    }

    private static void record(HttpServletRequest request) {
        try {
            Authentication auth = SecurityContextHolder.getContext().getAuthentication();
            if (auth != null && auth.isAuthenticated() && !(auth instanceof AnonymousAuthenticationToken)
                    && auth.getName() != null && !auth.getName().isEmpty()) {
                request.setAttribute(ATTRIBUTE, VisionaryUser.of(auth.getName()));
            }
        } catch (RuntimeException ignored) {
            // the user column is best effort
        }
    }
}
