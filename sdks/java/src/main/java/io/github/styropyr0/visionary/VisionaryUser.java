package io.github.styropyr0.visionary;

/**
 * The user who made a request. Only {@code id} is required; {@code handle} and
 * {@code role} make the dashboards friendlier and let the analyst group by role.
 */
public record VisionaryUser(String id, String handle, String role) {

    public static VisionaryUser of(String id) {
        return new VisionaryUser(id, null, null);
    }
}
