package io.github.styropyr0.visionary;

import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.time.Instant;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.HexFormat;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.Set;
import java.util.TreeSet;
import java.util.function.BiConsumer;
import java.util.function.UnaryOperator;
import java.util.regex.Pattern;

/**
 * Evaluates the rate-limit rules in force and keeps their counters, a port of
 * {@code sdks/go/ratelimit}. Thread-safe; anything going wrong lets the request through.
 */
final class RateLimiter {

    static final String LIMITED = "limited";
    static final String BLOCKED = "blocked";
    static final String WOULD_LIMIT = "would_limit";
    static final String WOULD_BLOCK = "would_block";

    static final int MAX_WINDOW = 86400;
    static final int MAX_COUNTERS = 10000;
    static final int MAX_AGGREGATES = 1000;

    static final List<String> DEFAULT_COUNTRY_HEADERS = List.of("CF-IPCountry", "CloudFront-Viewer-Country",
            "X-Vercel-IP-Country", "Fastly-Geo-Country", "X-AppEngine-Country", "X-Country-Code");

    private static final Pattern ID = Pattern.compile("[A-Za-z0-9_-]{1,32}");

    /** What the limiter sees of a request; header and query give a first value or "". */
    record Request(String method, String path, String remoteIp, UnaryOperator<String> header,
            UnaryOperator<String> query) {
    }

    record User(String id, String role) {
    }

    /** The answer for a request that is limited or blocked. */
    record Denial(int status, String ruleId, int retryAfter, byte[] body, String contentType) {
    }

    /** One rate_limit_events aggregate. */
    static final class Event {
        final Instant createdAt;
        final String ruleId;
        final String ruleName;
        final String outcome;
        final String keyValue;
        final String ip;
        final String country;
        final String method;
        final String path;
        final String userId;
        long hits;

        Event(Instant createdAt, String ruleId, String ruleName, String outcome, String keyValue, String ip,
                String country, String method, String path, String userId) {
            this.createdAt = createdAt;
            this.ruleId = ruleId;
            this.ruleName = ruleName;
            this.outcome = outcome;
            this.keyValue = keyValue;
            this.ip = ip;
            this.country = country;
            this.method = method;
            this.path = path;
            this.userId = userId;
        }
    }

    /** Carries one request between the two phases. */
    static final class State {
        private RuleSet set;
        private String ip = "";
        private String country = "";
        private boolean allowed;
        private boolean userDone;
        private Denial denial;

        Denial denied() {
            return denial;
        }
    }

    private final String service;
    private volatile RuleSet set;
    private final Object loadLock = new Object();
    private Map<String, String> shapes = new HashMap<>();
    private volatile Set<String> redact;
    private final Object aggLock = new Object();
    private LinkedHashMap<String, Event> agg = new LinkedHashMap<>();

    /** Sees every recorded outcome (tests). */
    volatile BiConsumer<String, String> trace;

    RateLimiter(String service) {
        this.service = service == null ? "" : service;
        setRedactHeaders(Redactor.DEFAULT_HEADERS);
    }

    /** Headers whose values are never recorded as keys. */
    void setRedactHeaders(List<String> names) {
        Set<String> s = new TreeSet<>();
        for (String n : names) {
            s.add(n.trim().toLowerCase(Locale.ROOT));
        }
        redact = s;
    }

    /** Puts a config in force, keeping the counters of rules whose key, window, algorithm and burst are unchanged. */
    void load(RateLimitConfig c) {
        synchronized (loadLock) {
            RuleSet next = compile(c, service);
            RuleSet old = set;
            Map<String, String> nextShapes = new HashMap<>();
            for (Rule r : next.byId.values()) {
                String shape = r.shape();
                nextShapes.put(r.id, shape);
                Rule prev = old == null ? null : old.byId.get(r.id);
                r.counters = prev != null && shape.equals(shapes.get(r.id)) ? prev.counters : new CounterSet();
            }
            shapes = nextShapes;
            set = next;
        }
    }

    void clear() {
        load(RateLimitConfig.EMPTY);
    }

    boolean active() {
        RuleSet s = set;
        return s != null && !s.byId.isEmpty();
    }

    /** The request phase; the state goes to {@link #checkUser} later. */
    State checkRequest(Request req, Instant now) {
        State st = new State();
        RuleSet s = set;
        if (s == null || s.byId.isEmpty()) {
            return st;
        }
        try {
            st.set = s;
            st.ip = clientIp(req, s.ipHeader);
            st.country = country(req, s.countryHeaders);
            run(s.request, st, req, null, null, now);
            return st;
        } catch (RuntimeException e) {
            return new State();
        }
    }

    /** The user phase, once per request; later calls repeat the first outcome. */
    Denial checkUser(State st, Request req, User user, Map<String, String> attrs, Instant now) {
        if (st == null || st.set == null) {
            return null;
        }
        synchronized (st) {
            if (st.userDone || st.allowed || st.denial != null) {
                return st.denial;
            }
            st.userDone = true;
            if (st.set.user.isEmpty()) {
                return null;
            }
            try {
                run(st.set.user, st, req, user == null ? new User("", "") : user, attrs == null ? Map.of() : attrs, now);
                return st.denial;
            } catch (RuntimeException e) {
                return null;
            }
        }
    }

    private void run(List<Rule> rules, State st, Request req, User user, Map<String, String> attrs, Instant now) {
        double t = seconds(now);
        for (Rule r : rules) {
            if (!matches(r, st, req, user, attrs)) {
                continue;
            }
            switch (r.action) {
                case "allow" -> {
                    if (!r.monitor) {
                        st.allowed = true;
                        return;
                    }
                }
                case "block" -> {
                    if (r.monitor) {
                        record(r, WOULD_BLOCK, "", st, req, user, now);
                        continue;
                    }
                    record(r, BLOCKED, "", st, req, user, now);
                    st.denial = r.deny(0);
                    return;
                }
                default -> {
                    String key = value(r.key, st, req, user, attrs);
                    if (key.isEmpty()) {
                        continue;
                    }
                    double limit = r.limit;
                    if (r.limitBy != null) {
                        Double n = r.limits.get(value(r.limitBy, st, req, user, attrs));
                        if (n != null) {
                            limit = n;
                        }
                    }
                    if (limit < 0) {
                        continue;
                    }
                    int retry = r.hit(key, limit, t);
                    if (retry < 0) {
                        continue;
                    }
                    if (r.monitor) {
                        record(r, WOULD_LIMIT, key, st, req, user, now);
                        continue;
                    }
                    record(r, LIMITED, key, st, req, user, now);
                    st.denial = r.deny(retry);
                    return;
                }
            }
        }
    }

    private static boolean matches(Rule r, State st, Request req, User user, Map<String, String> attrs) {
        if (!r.methods.isEmpty() && !r.methods.contains(upper(req.method()))) {
            return false;
        }
        if (!r.paths.isEmpty()) {
            boolean ok = false;
            for (String g : r.paths) {
                if (glob(g, req.path())) {
                    ok = true;
                    break;
                }
            }
            if (!ok) {
                return false;
            }
        }
        if (r.hasIps && !Ip.inPrefixes(r.prefixes, st.ip)) {
            return false;
        }
        if (!r.countries.isEmpty() && (st.country.isEmpty() || !r.countries.contains(st.country))) {
            return false;
        }
        for (Cond c : r.match) {
            String v = value(c.prop, st, req, user, attrs);
            if (c.values.isEmpty()) {
                if (v.isEmpty()) {
                    return false;
                }
            } else if (!c.values.contains(v)) {
                return false;
            }
        }
        return true;
    }

    private static String value(Prop p, State st, Request req, User user, Map<String, String> attrs) {
        String v = switch (p.kind) {
            case IP -> st.ip;
            case COUNTRY -> st.country;
            case METHOD -> upper(req.method());
            case PATH -> req.path();
            case HEADER -> req.header() == null ? null : req.header().apply(p.name);
            case QUERY -> req.query() == null ? null : req.query().apply(p.name);
            case GLOBAL -> "*";
            case USER -> user == null ? null : user.id();
            case ROLE -> user == null ? null : user.role();
            case ATTR -> attrs == null ? null : attrs.get(p.name);
        };
        return v == null ? "" : v;
    }

    /** Drops idle counters of every rule; called at each poll. */
    void sweep(Instant now) {
        RuleSet s = set;
        if (s == null) {
            return;
        }
        double t = seconds(now);
        for (Rule r : s.byId.values()) {
            synchronized (r.counters) {
                r.sweepLocked(t);
            }
        }
    }

    private void record(Rule r, String outcome, String key, State st, Request req, User user, Instant now) {
        BiConsumer<String, String> tr = trace;
        if (tr != null) {
            tr.accept(r.id, outcome);
        }
        if (!key.isEmpty() && r.key.kind == Kind.HEADER && redact.contains(r.key.name)) {
            key = "sha256:" + sha256(key).substring(0, 12);
        }
        key = clip(key, 200);
        String ip = st.ip;
        synchronized (aggLock) {
            String id = r.id + "\0" + outcome + "\0" + key + "\0" + ip;
            Event e = agg.get(id);
            if (e == null && agg.size() >= MAX_AGGREGATES) {
                key = "(other)";
                ip = "";
                id = r.id + "\0" + outcome + "\0" + key + "\0";
                e = agg.get(id);
            }
            if (e == null) {
                e = new Event(now, r.id, r.name, outcome, key, ip, st.country, upper(req.method()),
                        clip(req.path() == null ? "" : req.path(), 300), user == null || user.id() == null ? "" : user.id());
                agg.put(id, e);
            }
            e.hits++;
        }
    }

    /** The outcomes recorded since the last call, oldest first. */
    List<Event> drain() {
        synchronized (aggLock) {
            List<Event> out = new ArrayList<>(agg.values());
            agg = new LinkedHashMap<>();
            return out;
        }
    }

    // ---- compiling -------------------------------------------------------------------------

    private enum Kind { IP, COUNTRY, METHOD, PATH, HEADER, QUERY, GLOBAL, USER, ROLE, ATTR }

    /** A property; name is the header (lower case), query or attribute name. */
    private record Prop(Kind kind, String name) {
        boolean userPhase() {
            return kind == Kind.USER || kind == Kind.ROLE || kind == Kind.ATTR;
        }

        static Prop parse(String s) {
            if (s.startsWith("header:") && s.length() > 7) {
                return new Prop(Kind.HEADER, s.substring(7).toLowerCase(Locale.ROOT));
            }
            if (s.startsWith("query:") && s.length() > 6) {
                return new Prop(Kind.QUERY, s.substring(6));
            }
            if (s.startsWith("attr:") && s.length() > 5) {
                return new Prop(Kind.ATTR, s.substring(5));
            }
            return switch (s) {
                case "ip" -> new Prop(Kind.IP, "");
                case "country" -> new Prop(Kind.COUNTRY, "");
                case "method" -> new Prop(Kind.METHOD, "");
                case "path" -> new Prop(Kind.PATH, "");
                case "global" -> new Prop(Kind.GLOBAL, "");
                case "user" -> new Prop(Kind.USER, "");
                case "role" -> new Prop(Kind.ROLE, "");
                default -> null;
            };
        }
    }

    private record Cond(Prop prop, List<String> values) {
    }

    private static final int SLIDING = 0;
    private static final int FIXED = 1;
    private static final int BUCKET = 2;

    private static final class RuleSet {
        String ipHeader = "";
        List<String> countryHeaders = new ArrayList<>();
        final List<Rule> request = new ArrayList<>();
        final List<Rule> user = new ArrayList<>();
        final Map<String, Rule> byId = new HashMap<>();
    }

    private static RuleSet compile(RateLimitConfig c, String service) {
        RuleSet rs = new RuleSet();
        rs.ipHeader = c.ipHeader() == null ? "" : c.ipHeader().strip();
        for (String h : c.countryHeaders()) {
            if (h != null && !h.strip().isEmpty()) {
                rs.countryHeaders.add(h.strip());
            }
        }
        if (rs.countryHeaders.isEmpty()) {
            rs.countryHeaders = DEFAULT_COUNTRY_HEADERS;
        }
        if (!c.on()) {
            return rs;
        }
        for (RateLimitConfig.Rule src : c.rules()) {
            Rule r = compileRule(src);
            if (r == null || !src.on() || rs.byId.containsKey(r.id) || !servesService(src.services(), service)) {
                continue;
            }
            rs.byId.put(r.id, r);
            (r.userPhase ? rs.user : rs.request).add(r);
        }
        return rs;
    }

    private static boolean servesService(List<String> list, String service) {
        if (list.isEmpty()) {
            return true;
        }
        return !service.isEmpty() && list.contains(service);
    }

    private static Rule compileRule(RateLimitConfig.Rule src) {
        if (!ID.matcher(src.id()).matches()) {
            return null;
        }
        Rule r = new Rule();
        r.id = src.id();
        r.name = src.name().isEmpty() ? r.id : src.name();
        r.action = src.action();
        r.status = src.status();
        r.body = src.body();
        r.contentType = src.contentType();
        switch (src.mode()) {
            case "", "enforce" -> { }
            case "monitor" -> r.monitor = true;
            default -> {
                return null;
            }
        }
        switch (src.action()) {
            case "limit", "block", "allow" -> { }
            default -> {
                return null;
            }
        }
        for (String m : src.methods()) {
            r.methods.add(upper(m.strip()));
        }
        r.paths = src.paths();
        r.hasIps = !src.ips().isEmpty();
        for (String s : src.ips()) {
            Ip.Prefix p = Ip.parsePrefix(s);
            if (p != null) {
                r.prefixes.add(p);
            }
        }
        for (String c : src.countries()) {
            r.countries.add(upper(c.strip()));
        }
        for (Map.Entry<String, List<String>> e : src.match().entrySet()) {
            Prop p = Prop.parse(e.getKey());
            if (p == null || p.kind == Kind.GLOBAL) {
                return null;
            }
            r.match.add(new Cond(p, e.getValue()));
            r.userPhase |= p.userPhase();
        }
        if (!"limit".equals(src.action())) {
            return r;
        }
        Prop k = Prop.parse(src.key().isEmpty() ? "ip" : src.key());
        if (k == null) {
            return null;
        }
        r.key = k;
        r.userPhase |= k.userPhase();
        if (!(src.windowS() >= 1 && src.windowS() <= MAX_WINDOW)) {
            return null;
        }
        r.window = src.windowS();
        r.limit = src.limit();
        r.burst = src.burst();
        switch (src.algorithm()) {
            case "", "sliding" -> r.alg = SLIDING;
            case "fixed" -> r.alg = FIXED;
            case "token_bucket" -> r.alg = BUCKET;
            default -> {
                return null;
            }
        }
        if (!src.limitBy().isEmpty()) {
            Prop p = Prop.parse(src.limitBy());
            if (p == null || p.kind == Kind.GLOBAL) {
                return null;
            }
            r.limitBy = p;
            r.limits = src.limits();
            r.userPhase |= p.userPhase();
        }
        return r;
    }

    private static final class Rule {
        String id;
        String name;
        boolean monitor;
        String action;
        final List<String> methods = new ArrayList<>();
        List<String> paths = List.of();
        boolean hasIps;
        final List<Ip.Prefix> prefixes = new ArrayList<>();
        final List<String> countries = new ArrayList<>();
        final List<Cond> match = new ArrayList<>();
        Prop key = new Prop(Kind.IP, "");
        double limit;
        double window;
        int alg;
        double burst;
        Prop limitBy;
        Map<String, Double> limits = Map.of();
        int status;
        String body;
        String contentType;
        boolean userPhase;
        CounterSet counters = new CounterSet();

        /** What the counters depend on; a change resets them. */
        String shape() {
            return key.name + "|" + key.kind.ordinal() + "|" + window + "|" + alg + "|" + burst;
        }

        Denial deny(int retry) {
            int st = status;
            if (st == 0) {
                st = "block".equals(action) ? 403 : 429;
            }
            String text;
            String type;
            if (!body.isEmpty()) {
                text = body.replace("{retry_after}", Integer.toString(retry));
                String b = stripLeading(body);
                type = b.startsWith("{") || b.startsWith("[") ? "application/json" : "text/plain; charset=utf-8";
            } else if ("block".equals(action)) {
                text = "{\"error\":{\"code\":\"blocked\",\"key\":\"" + id + "\",\"message\":\"Forbidden\"}}";
                type = "application/json";
            } else {
                text = "{\"error\":{\"code\":\"rate_limited\",\"key\":\"" + id + "\",\"message\":\"Too many requests\"}}";
                type = "application/json";
            }
            if (!contentType.isEmpty()) {
                type = contentType;
            }
            return new Denial(st, id, retry, text.getBytes(StandardCharsets.UTF_8), type);
        }

        double capacity(double limit) {
            return burst > 0 ? burst : limit;
        }

        /** Takes one request on key's counter: the seconds to wait when over, else -1. */
        int hit(String k, double limit, double t) {
            double w0 = window;
            if (limit == 0) {
                return (int) Math.ceil(w0);
            }
            CounterSet cs = counters;
            synchronized (cs) {
                Counter c = cs.m.get(k);
                if (c == null) {
                    if (cs.m.size() >= MAX_COUNTERS) {
                        sweepLocked(t);
                        if (cs.m.size() >= MAX_COUNTERS) {
                            cs.m.clear();
                        }
                    }
                    c = new Counter();
                    c.win = Long.MIN_VALUE;
                    c.tokens = capacity(limit);
                    c.last = t;
                    cs.m.put(k, c);
                }
                if (alg == BUCKET) {
                    double cap = capacity(limit);
                    double rate = limit / w0;
                    if (t > c.last) {
                        c.tokens = Math.min(cap, c.tokens + (t - c.last) * rate);
                        c.last = t;
                    }
                    if (c.tokens < 1) {
                        return Math.max(1, (int) Math.ceil((1 - c.tokens) / rate));
                    }
                    c.tokens--;
                    return -1;
                }
                long w = (long) Math.floor(t / w0);
                if (c.win == Long.MIN_VALUE) {
                    c.prev = 0;
                    c.curr = 0;
                } else if (w == c.win + 1) {
                    c.prev = c.curr;
                    c.curr = 0;
                } else if (w > c.win + 1) {
                    c.prev = 0;
                    c.curr = 0;
                }
                c.win = Math.max(c.win, w);
                boolean over;
                if (alg == FIXED) {
                    over = c.curr >= limit;
                } else {
                    double e = t - w * w0;
                    over = c.prev * (w0 - e) / w0 + c.curr + 1 > limit;
                }
                if (over) {
                    return Math.max(1, (int) Math.ceil((w + 1) * w0 - t));
                }
                c.curr++;
                return -1;
            }
        }

        /** Drops idle counters; the caller holds the set's lock. */
        void sweepLocked(double t) {
            long w = (long) Math.floor(t / window);
            counters.m.values().removeIf(c -> {
                if (alg == BUCKET) {
                    // the limit may differ per key; the static one is close enough to call it idle
                    return limit <= 0 || c.tokens + (t - c.last) * (limit / window) >= capacity(limit);
                }
                return c.win < w - 1;
            });
        }
    }

    /** (win, curr, prev) for fixed and sliding windows, (tokens, last) for the token bucket. */
    private static final class Counter {
        long win;
        double curr;
        double prev;
        double tokens;
        double last;
    }

    private static final class CounterSet {
        final Map<String, Counter> m = new HashMap<>();
    }

    // ---- request helpers -------------------------------------------------------------------

    static String clientIp(Request req, String header) {
        if (!header.isEmpty() && req.header() != null) {
            String v = req.header().apply(header.toLowerCase(Locale.ROOT));
            if (v != null && !v.isEmpty()) {
                int comma = v.indexOf(',');
                String first = (comma < 0 ? v : v.substring(0, comma)).strip();
                if (!first.isEmpty()) {
                    return first;
                }
            }
        }
        return req.remoteIp() == null ? "" : req.remoteIp();
    }

    static String country(Request req, List<String> headers) {
        if (req.header() == null) {
            return "";
        }
        for (String h : headers) {
            String v = req.header().apply(h.toLowerCase(Locale.ROOT));
            if (v != null && !v.strip().isEmpty()) {
                return upper(v.strip());
            }
        }
        return "";
    }

    /** Matches a whole path against a pattern where * is any run of characters. */
    static boolean glob(String pattern, String s) {
        if (s == null) {
            s = "";
        }
        int p = 0;
        int i = 0;
        int star = -1;
        int mark = 0;
        while (i < s.length()) {
            if (p < pattern.length() && pattern.charAt(p) == '*') {
                star = p;
                mark = i;
                p++;
            } else if (p < pattern.length() && pattern.charAt(p) == s.charAt(i)) {
                p++;
                i++;
            } else if (star >= 0) {
                p = star + 1;
                mark++;
                i = mark;
            } else {
                return false;
            }
        }
        while (p < pattern.length() && pattern.charAt(p) == '*') {
            p++;
        }
        return p == pattern.length();
    }

    private static double seconds(Instant now) {
        return (now.getEpochSecond() * 1_000_000_000L + now.getNano()) / 1e9;
    }

    private static String upper(String s) {
        return s == null ? "" : s.toUpperCase(Locale.ROOT);
    }

    private static String stripLeading(String s) {
        int i = 0;
        while (i < s.length() && " \t\r\n".indexOf(s.charAt(i)) >= 0) {
            i++;
        }
        return s.substring(i);
    }

    /** At most n code points. */
    static String clip(String s, int n) {
        if (s.length() <= n || s.codePointCount(0, s.length()) <= n) {
            return s;
        }
        return s.substring(0, s.offsetByCodePoints(0, n));
    }

    private static String sha256(String s) {
        try {
            return HexFormat.of().formatHex(MessageDigest.getInstance("SHA-256").digest(s.getBytes(StandardCharsets.UTF_8)));
        } catch (NoSuchAlgorithmException e) {
            throw new IllegalStateException(e);
        }
    }

    // ---- IP addresses, parsed as netip does: literals only, never a DNS lookup -------------

    static final class Ip {

        record Prefix(byte[] addr, int bits) {
            boolean contains(byte[] a) {
                return a.length == addr.length && java.util.Arrays.equals(mask(a, bits), addr);
            }
        }

        private static byte[] mask(byte[] a, int bits) {
            byte[] out = a.clone();
            for (int i = 0; i < out.length; i++) {
                int keep = Math.max(0, Math.min(8, bits - i * 8));
                out[i] &= (byte) (keep == 0 ? 0 : 0xFF << (8 - keep));
            }
            return out;
        }

        static boolean inPrefixes(List<Prefix> list, String ip) {
            byte[] a = parseAddr(ip == null ? "" : ip.strip(), true);
            if (a == null) {
                return false;
            }
            a = unmap(a);
            for (Prefix p : list) {
                if (p.contains(a)) {
                    return true;
                }
            }
            return false;
        }

        /** An IP or CIDR; null when it does not parse. IPv4-mapped addresses become IPv4. */
        static Prefix parsePrefix(String s) {
            s = s.strip();
            int slash = s.indexOf('/');
            if (slash >= 0) {
                byte[] a = parseAddr(s.substring(0, slash), false);
                String bitsText = s.substring(slash + 1);
                if (a == null || !bitsText.matches("0|[1-9][0-9]{0,2}")) {
                    return null;
                }
                int bits = Integer.parseInt(bitsText);
                if (bits > a.length * 8) {
                    return null;
                }
                if (a.length == 16 && isMapped(a) && bits >= 96) {
                    a = unmap(a);
                    bits -= 96;
                }
                return new Prefix(mask(a, bits), bits);
            }
            byte[] a = parseAddr(s, true);
            if (a == null) {
                return null;
            }
            a = unmap(a);
            return new Prefix(a, a.length * 8);
        }

        private static boolean isMapped(byte[] a) {
            for (int i = 0; i < 10; i++) {
                if (a[i] != 0) {
                    return false;
                }
            }
            return a[10] == (byte) 0xFF && a[11] == (byte) 0xFF;
        }

        private static byte[] unmap(byte[] a) {
            return a.length == 16 && isMapped(a) ? java.util.Arrays.copyOfRange(a, 12, 16) : a;
        }

        /** 4 bytes for IPv4, 16 for IPv6 (a zone is allowed and dropped when zoneOk). */
        static byte[] parseAddr(String s, boolean zoneOk) {
            if (s.indexOf(':') >= 0) {
                int pct = s.indexOf('%');
                if (pct >= 0) {
                    if (!zoneOk || pct == s.length() - 1) {
                        return null;
                    }
                    s = s.substring(0, pct);
                }
                return parse6(s);
            }
            return s.indexOf('.') >= 0 ? parse4(s) : null;
        }

        private static byte[] parse4(String s) {
            String[] parts = s.split("\\.", -1);
            if (parts.length != 4) {
                return null;
            }
            byte[] out = new byte[4];
            for (int i = 0; i < 4; i++) {
                String p = parts[i];
                if (p.isEmpty() || p.length() > 3 || (p.length() > 1 && p.charAt(0) == '0')) {
                    return null;
                }
                int v = 0;
                for (int j = 0; j < p.length(); j++) {
                    char c = p.charAt(j);
                    if (c < '0' || c > '9') {
                        return null;
                    }
                    v = v * 10 + (c - '0');
                }
                if (v > 255) {
                    return null;
                }
                out[i] = (byte) v;
            }
            return out;
        }

        private static byte[] parse6(String s) {
            byte[] out = new byte[16];
            int ellipsis = -1;
            int i = 0;
            int n = 0;
            if (s.startsWith("::")) {
                ellipsis = 0;
                i = 2;
                if (i == s.length()) {
                    return out;
                }
            } else if (s.startsWith(":")) {
                return null;
            }
            while (i < s.length()) {
                int start = i;
                int v = 0;
                while (i < s.length() && i - start < 5 && Character.digit(s.charAt(i), 16) >= 0) {
                    v = v * 16 + Character.digit(s.charAt(i), 16);
                    i++;
                }
                int len = i - start;
                if (len == 0 || len > 4) {
                    return null;
                }
                if (i < s.length() && s.charAt(i) == '.') {
                    // embedded IPv4 in the last 32 bits
                    if (n > 12) {
                        return null;
                    }
                    byte[] v4 = parse4(s.substring(start));
                    if (v4 == null) {
                        return null;
                    }
                    System.arraycopy(v4, 0, out, n, 4);
                    n += 4;
                    i = s.length();
                    break;
                }
                if (n > 14) {
                    return null;
                }
                out[n++] = (byte) (v >> 8);
                out[n++] = (byte) v;
                if (i == s.length()) {
                    break;
                }
                if (s.charAt(i) != ':') {
                    return null;
                }
                i++;
                if (i < s.length() && s.charAt(i) == ':') {
                    if (ellipsis >= 0) {
                        return null;
                    }
                    ellipsis = n;
                    i++;
                    if (i == s.length()) {
                        break;
                    }
                } else if (i == s.length()) {
                    return null;
                }
            }
            if (ellipsis < 0) {
                return n == 16 ? out : null;
            }
            if (n == 16) {
                return null;
            }
            int move = n - ellipsis;
            for (int j = move - 1; j >= 0; j--) {
                out[16 - move + j] = out[ellipsis + j];
            }
            for (int j = ellipsis; j < 16 - move; j++) {
                out[j] = 0;
            }
            return out;
        }

        private Ip() {
        }
    }
}
