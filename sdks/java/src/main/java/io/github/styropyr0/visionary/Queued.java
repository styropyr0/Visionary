package io.github.styropyr0.visionary;

/** Something waiting in the writer's queue. */
sealed interface Queued permits RequestEntry, ErrorEntry, SpanEntry, ReleaseEntry, ProfileEntry {
}
