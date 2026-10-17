package io.github.styropyr0.visionary;

/**
 * Optional bean that fills in a user's handle and role from their id.
 *
 * <p>Called on Visionary's background writer thread, once per distinct user
 * per batch, only for users whose handle or role is missing. Keep it cheap
 * (a cache); return {@code null} when the user is unknown.
 */
@FunctionalInterface
public interface VisionaryUserLookup {

    VisionaryUser lookup(String id);
}
