package io.github.styropyr0.visionary;

/** A span made outside a logged request (or after it finished), stored on its own. */
record SpanEntry(VisionarySpan span) implements Queued {
}
