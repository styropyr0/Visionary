package io.github.styropyr0.visionary;

import java.time.Duration;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.Executors;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.TimeUnit;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;

import org.apache.commons.logging.Log;
import org.apache.commons.logging.LogFactory;

/**
 * Settings → Data's skip list, kept in {@code visionary_settings} as {@code requests.skip}:
 * a JSON array of exact paths, or prefixes ending in {@code *}. A skipped request is
 * treated like one outside the path prefix.
 */
final class RequestSkip {

    private static final Log log = LogFactory.getLog(RequestSkip.class);
    static final String SETTINGS_QUERY = "SELECT name, value FROM visionary_settings WHERE name LIKE 'requests.%'";
    static final String SETTING = "requests.skip";
    static final Duration POLL_INTERVAL = Duration.ofSeconds(30);

    private final Store store;
    private final ObjectMapper json;
    private volatile List<String> list = List.of();

    private ScheduledExecutorService poller;
    private boolean readFailureLogged;

    RequestSkip(Store store, ObjectMapper json) {
        this.store = store;
        this.json = json;
    }

    synchronized void start() {
        if (store == null || poller != null) {
            return;
        }
        poller = Executors.newSingleThreadScheduledExecutor(r -> {
            Thread t = new Thread(r, "visionary-request-settings");
            t.setDaemon(true);
            return t;
        });
        poller.scheduleWithFixedDelay(this::pollSafely, 0, POLL_INTERVAL.toSeconds(), TimeUnit.SECONDS);
    }

    synchronized void stop() {
        if (poller != null) {
            poller.shutdownNow();
            poller = null;
        }
    }

    private void pollSafely() {
        try {
            poll();
        } catch (RuntimeException | LinkageError e) {
            log.debug("visionary: request settings poll failed", e);
        }
    }

    /** A missing table or failed read keeps the last list. */
    void poll() {
        List<String[]> rows;
        try {
            rows = store.select(SETTINGS_QUERY);
        } catch (Exception e) {
            if (!readFailureLogged) {
                readFailureLogged = true;
                log.debug("visionary: request settings unavailable (" + e.getMessage() + ")");
            }
            return;
        }
        readFailureLogged = false;
        List<String> next = List.of();
        for (String[] row : rows) {
            if (row.length >= 2 && SETTING.equals(row[0])) {
                next = parse(row[1], json);
            }
        }
        list = next;
    }

    boolean skipped(String path) {
        return match(list, path);
    }

    /** Anything but a JSON array of strings skips nothing. */
    static List<String> parse(String value, ObjectMapper json) {
        if (value == null) {
            return List.of();
        }
        JsonNode node;
        try {
            node = json.readTree(value);
        } catch (Exception e) {
            return List.of();
        }
        if (node == null || !node.isArray()) {
            return List.of();
        }
        List<String> out = new ArrayList<>();
        for (JsonNode n : node) {
            if (!n.isTextual()) {
                return List.of();
            }
            String p = n.asText().strip();
            if (!p.isEmpty()) {
                out.add(p);
            }
        }
        return List.copyOf(out);
    }

    /** True when the path equals an entry, or starts with one ending in {@code *}. */
    static boolean match(List<String> list, String path) {
        if (path == null) {
            return false;
        }
        for (String p : list) {
            if (p.endsWith("*")) {
                if (path.startsWith(p.substring(0, p.length() - 1))) {
                    return true;
                }
            } else if (path.equals(p)) {
                return true;
            }
        }
        return false;
    }
}
