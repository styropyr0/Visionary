package io.github.styropyr0.visionary;

import java.time.Instant;

/** The deploy marker written once at start when the version is known. */
record ReleaseEntry(Instant createdAt) implements Queued {
}
