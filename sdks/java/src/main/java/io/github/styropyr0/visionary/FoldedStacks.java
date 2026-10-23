package io.github.styropyr0.visionary;

import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Collection;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;

/**
 * Samples as folded stacks: one {@code frame;frame;frame count} line per
 * distinct stack, outermost caller first. Rendered heaviest first up to
 * {@link #MAX_BYTES}; stacks that don't fit are rolled up into their callers.
 */
final class FoldedStacks {

    static final int MAX_BYTES = 90_000;
    /** The heaviest stacks are kept whole up to this many bytes. */
    static final int KEEP_WHOLE = 72_000;
    /** A scheduled profile with fewer samples is dropped; a "Profile now" one is kept with any. */
    static final int MIN_SAMPLES = 20;

    private final Map<String, long[]> lines = new HashMap<>();
    private long samples;

    /** Adds one sample of a stack given outermost caller first. */
    synchronized void add(List<String> framesOuterFirst) {
        if (framesOuterFirst.isEmpty()) {
            return;
        }
        lines.computeIfAbsent(String.join(";", framesOuterFirst), k -> new long[1])[0]++;
        samples++;
    }

    synchronized long samples() {
        return samples;
    }

    /**
     * The folded text within {@link #MAX_BYTES} (sdks/SPEC.md, "CPU profiles"):
     * whole lines up to {@link #KEEP_WHOLE}, then the rest cut after the
     * deepest frame they share with a kept stack and ending in
     * {@code (other)}, then one {@code (other) n} for whatever is left.
     */
    synchronized String render() {
        List<Map.Entry<String, Long>> sorted = heaviestFirst(lines.entrySet().stream()
                .map(e -> Map.entry(e.getKey(), e.getValue()[0])).toList());
        StringBuilder out = new StringBuilder();
        long bytes = 0;
        // Whole lines first, leaving room for the rolled-up ones.
        int i = 0;
        for (; i < sorted.size(); i++) {
            String line = sorted.get(i).getKey() + " " + sorted.get(i).getValue() + "\n";
            int n = line.getBytes(StandardCharsets.UTF_8).length;
            if (bytes + n > KEEP_WHOLE) {
                break;
            }
            out.append(line);
            bytes += n;
        }
        if (i == sorted.size()) {
            return out.toString();
        }
        // The rest are cut after the deepest frame they share with a kept
        // stack and end in "(other)", so their time stays under the right caller.
        Set<String> prefixes = new HashSet<>();
        for (Map.Entry<String, Long> e : sorted.subList(0, i)) {
            String k = e.getKey();
            for (int j = k.indexOf(';'); j >= 0; j = k.indexOf(';', j + 1)) {
                prefixes.add(k.substring(0, j));
            }
            prefixes.add(k);
        }
        Map<String, Long> rolled = new HashMap<>();
        long other = 0;
        for (Map.Entry<String, Long> e : sorted.subList(i, sorted.size())) {
            String k = e.getKey();
            String cut = "";
            for (int j = k.indexOf(';'); j >= 0; j = k.indexOf(';', j + 1)) {
                if (prefixes.contains(k.substring(0, j))) {
                    cut = k.substring(0, j);
                }
            }
            if (prefixes.contains(k)) {
                cut = k; // the whole stack leads into a kept one: nothing to cut
            }
            if (cut.isEmpty()) {
                other += e.getValue();
            } else {
                rolled.merge(cut.equals(k) ? k : cut + ";(other)", e.getValue(), Long::sum);
            }
        }
        for (Map.Entry<String, Long> e : heaviestFirst(rolled.entrySet())) {
            String line = e.getKey() + " " + e.getValue() + "\n";
            int n = line.getBytes(StandardCharsets.UTF_8).length;
            if (bytes + n > MAX_BYTES - 32) { // room for the last "(other) n"
                other += e.getValue();
            } else {
                out.append(line);
                bytes += n;
            }
        }
        if (other > 0) {
            out.append("(other) ").append(other).append('\n');
        }
        return out.toString();
    }

    /** Heaviest first, ties in text order. */
    private static List<Map.Entry<String, Long>> heaviestFirst(Collection<Map.Entry<String, Long>> entries) {
        List<Map.Entry<String, Long>> sorted = new ArrayList<>(entries);
        sorted.sort((a, b) -> {
            int c = Long.compare(b.getValue(), a.getValue());
            return c != 0 ? c : a.getKey().compareTo(b.getKey());
        });
        return sorted;
    }
}
