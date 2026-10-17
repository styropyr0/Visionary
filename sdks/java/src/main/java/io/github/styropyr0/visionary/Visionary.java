package io.github.styropyr0.visionary;

import jakarta.servlet.http.HttpServletRequest;

/**
 * Application-facing API of the Visionary collector.
 *
 * <p>Attach the signed-in user to the request being logged, usually from auth
 * code once the user is known:
 *
 * <pre>{@code
 * Visionary.setUser(request, user.getId(), user.getUsername(), user.getRole());
 * }</pre>
 *
 * A user set here wins over the Spring Security principal. Calling it when
 * Visionary is not configured is harmless.
 */
public final class Visionary {

    /** Request attribute holding the {@link VisionaryUser} for the current request. */
    public static final String USER_ATTRIBUTE = Visionary.class.getName() + ".USER";

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
}
