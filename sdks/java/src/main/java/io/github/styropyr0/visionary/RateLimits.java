package io.github.styropyr0.visionary;

import java.io.IOException;
import java.net.URLDecoder;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.concurrent.Executors;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.TimeUnit;
import java.util.function.UnaryOperator;

import com.fasterxml.jackson.core.JsonProcessingException;
import com.fasterxml.jackson.databind.ObjectMapper;

import jakarta.servlet.http.HttpServletRequest;
import jakarta.servlet.http.HttpServletResponse;

import org.apache.commons.logging.Log;
import org.apache.commons.logging.LogFactory;
import org.springframework.util.ClassUtils;

/**
 * Rate limits from Security → Rate limits: polled from {@code visionary_settings},
 * enforced by {@link VisionaryFilter} (request phase) and
 * {@link Visionary#checkRateLimit} (user phase), with outcomes written to
 * {@code rate_limit_events}. Counters are per process.
 */
final class RateLimits {

    private static final Log log = LogFactory.getLog(RateLimits.class);
    static final String ATTRIBUTE = RateLimits.class.getName() + ".CHECK";
    static final String VERSION_QUERY = "SELECT value FROM visionary_settings WHERE name = 'ratelimit.version'";
    static final String CONFIG_QUERY = "SELECT value FROM visionary_settings WHERE name = 'ratelimit.config'";
    static final Duration POLL_INTERVAL = Duration.ofSeconds(15);
    private static final boolean SECURITY = ClassUtils.isPresent(
            "org.springframework.security.core.context.SecurityContextHolder", RateLimits.class.getClassLoader());

    private final Store store;
    private final Origin origin;
    private final ObjectMapper json;
    final RateLimiter limiter;

    private ScheduledExecutorService poller;
    private String version = "";
    private String badVersion;
    private boolean writeFailed;

    RateLimits(Store store, Origin origin, List<String> redactHeaders, ObjectMapper json) {
        this.store = store;
        this.origin = origin;
        this.json = json;
        this.limiter = new RateLimiter(origin.service());
        List<String> redact = new ArrayList<>(Redactor.DEFAULT_HEADERS);
        redact.addAll(redactHeaders);
        limiter.setRedactHeaders(redact);
    }

    synchronized void start() {
        if (poller != null) {
            return;
        }
        poller = Executors.newSingleThreadScheduledExecutor(r -> {
            Thread t = new Thread(r, "visionary-rate-limits");
            t.setDaemon(true);
            return t;
        });
        poller.scheduleWithFixedDelay(this::pollSafely, 0, POLL_INTERVAL.toSeconds(), TimeUnit.SECONDS);
    }

    /** Stops polling and writes what is still counted. */
    void stop(Duration timeout) {
        ScheduledExecutorService p;
        synchronized (this) {
            p = poller;
            poller = null;
        }
        if (p == null) {
            return;
        }
        p.shutdown();
        try {
            p.awaitTermination(timeout.toMillis(), TimeUnit.MILLISECONDS);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
        flush();
    }

    private void pollSafely() {
        try {
            poll();
        } catch (RuntimeException | LinkageError e) {
            log.debug("visionary: rate limit poll failed", e);
        }
    }

    /** Reads the config only when the version changed; a failed read keeps the rules in force. */
    synchronized void poll() {
        try {
            List<String[]> rows = store.select(VERSION_QUERY);
            if (rows.isEmpty() || rows.get(0).length == 0 || rows.get(0)[0] == null) {
                if (!version.isEmpty()) {
                    limiter.clear();
                }
                version = "";
            } else if (!rows.get(0)[0].equals(version)) {
                load(rows.get(0)[0]);
            }
        } catch (Exception e) {
            // no table yet or the read failed: keep the rules in force
        }
        limiter.sweep(Instant.now());
        flush();
    }

    private void load(String next) throws Exception {
        List<String[]> cfg = store.select(CONFIG_QUERY);
        String raw = cfg.isEmpty() || cfg.get(0).length == 0 || cfg.get(0)[0] == null ? "" : cfg.get(0)[0];
        RateLimitConfig parsed;
        if (raw.isEmpty()) {
            parsed = RateLimitConfig.EMPTY;
        } else {
            try {
                parsed = RateLimitConfig.parse(raw, json);
            } catch (JsonProcessingException e) {
                if (!next.equals(badVersion)) {
                    badVersion = next;
                    log.warn("visionary: rate limit rules are not valid JSON; keeping the previous ones: "
                            + e.getOriginalMessage());
                }
                return;
            }
        }
        limiter.load(parsed);
        version = next;
    }

    synchronized void flush() {
        List<RateLimiter.Event> events = limiter.drain();
        if (events.isEmpty()) {
            return;
        }
        List<Object[]> rows = new ArrayList<>(events.size());
        for (RateLimiter.Event e : events) {
            rows.add(new Object[] {Values.timestamp(e.createdAt), origin.service(), origin.instance(), e.ruleId,
                    e.ruleName, e.outcome, Values.emptyToNull(e.keyValue), Values.emptyToNull(e.ip),
                    Values.emptyToNull(e.country), Values.emptyToNull(e.method), Values.emptyToNull(e.path),
                    Values.emptyToNull(e.userId), e.hits});
        }
        try {
            store.insert(Table.RATE_LIMIT_EVENTS, rows);
        } catch (Exception e) {
            if (!writeFailed) {
                writeFailed = true;
                log.info("visionary: could not record rate-limited requests (the Visionary server creates "
                        + "rate_limit_events): " + e);
            }
        }
    }

    // ---- the filter and the user check -----------------------------------------------------

    /** One request's check, kept from the filter for the user phase. */
    private static final class Check {
        final RateLimiter limiter;
        final RateLimiter.State state;
        final RateLimiter.Request request;
        boolean answered;

        Check(RateLimiter limiter, RateLimiter.State state, RateLimiter.Request request) {
            this.limiter = limiter;
            this.state = state;
            this.request = request;
        }
    }

    /** The request phase: true when it answered the request. */
    boolean checkRequest(HttpServletRequest request, HttpServletResponse response) throws IOException {
        RateLimiter.Denial d;
        try {
            if (!limiter.active()) {
                return false;
            }
            RateLimiter.Request req = request(request);
            RateLimiter.State st = limiter.checkRequest(req, Instant.now());
            request.setAttribute(ATTRIBUTE, new Check(limiter, st, req));
            d = st.denied();
        } catch (RuntimeException | LinkageError e) {
            return false;
        }
        if (d == null) {
            return false;
        }
        write(response, d);
        return true;
    }

    /** The user phase, once per request; false outside a request the filter checked. */
    static boolean checkUser(HttpServletRequest request, HttpServletResponse response, Map<String, String> attrs)
            throws IOException {
        RateLimiter.Denial d;
        try {
            if (request == null || !(request.getAttribute(ATTRIBUTE) instanceof Check c)) {
                return false;
            }
            synchronized (c) {
                if (c.answered) {
                    return true;
                }
                VisionaryUser u = user(request);
                d = c.limiter.checkUser(c.state, c.request,
                        new RateLimiter.User(u == null ? "" : u.id(), u == null ? "" : u.role()), attrs, Instant.now());
                if (d == null) {
                    return false;
                }
                c.answered = true;
            }
        } catch (RuntimeException | LinkageError e) {
            return false;
        }
        if (response != null) {
            write(response, d);
        }
        return true;
    }

    private static VisionaryUser user(HttpServletRequest request) {
        if (request.getAttribute(Visionary.USER_ATTRIBUTE) instanceof VisionaryUser u) {
            return u;
        }
        if (request.getAttribute(SecurityUserFilter.ATTRIBUTE) instanceof VisionaryUser u) {
            return u;
        }
        return SECURITY ? SecurityUserFilter.current() : null;
    }

    static void write(HttpServletResponse response, RateLimiter.Denial d) throws IOException {
        response.setStatus(d.status());
        response.setContentType(d.contentType());
        if (d.retryAfter() > 0) {
            response.setHeader("Retry-After", Integer.toString(d.retryAfter()));
        }
        response.setContentLength(d.body().length);
        response.getOutputStream().write(d.body());
    }

    /** The request as the limiter sees it: the raw path, the logged IP, the decoded query. */
    static RateLimiter.Request request(HttpServletRequest r) {
        return new RateLimiter.Request(r.getMethod(), r.getRequestURI(), HttpCapture.clientIp(r), r::getHeader,
                new Query(r.getQueryString()));
    }

    /** Parsed on first use: most rules never look at the query. */
    private static final class Query implements UnaryOperator<String> {
        private final String raw;
        private Map<String, String> values;

        Query(String raw) {
            this.raw = raw;
        }

        @Override
        public synchronized String apply(String name) {
            if (values == null) {
                values = parseQuery(raw);
            }
            return values.get(name);
        }
    }

    /** First value per name, as Go's url.ParseQuery decodes them; malformed pairs are skipped. */
    static Map<String, String> parseQuery(String query) {
        Map<String, String> out = new HashMap<>();
        if (query == null || query.isEmpty()) {
            return out;
        }
        for (String pair : query.split("&")) {
            if (pair.isEmpty() || pair.indexOf(';') >= 0) {
                continue;
            }
            int eq = pair.indexOf('=');
            try {
                String k = URLDecoder.decode(eq < 0 ? pair : pair.substring(0, eq), StandardCharsets.UTF_8);
                String v = eq < 0 ? "" : URLDecoder.decode(pair.substring(eq + 1), StandardCharsets.UTF_8);
                out.putIfAbsent(k, v);
            } catch (IllegalArgumentException e) {
                // bad escape
            }
        }
        return out;
    }
}
