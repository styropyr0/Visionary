package io.github.styropyr0.visionary;

/** One finished CPU profile, already in {@link Table#PROFILES} column order. */
record ProfileEntry(Object[] row) implements Queued {
}
