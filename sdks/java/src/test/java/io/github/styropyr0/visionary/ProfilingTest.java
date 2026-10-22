package io.github.styropyr0.visionary;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.time.Instant;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.concurrent.atomic.AtomicInteger;

import com.fasterxml.jackson.databind.ObjectMapper;

import org.junit.jupiter.api.Test;
import org.springframework.core.env.MapPropertySource;
import org.springframework.core.env.StandardEnvironment;

class ProfilingTest {

    private final ObjectMapper json = new ObjectMapper();

    // ---- frame naming ----

    private final FrameNamer namer = new FrameNamer(List.of("com.example.demo"), getClass().getClassLoader());

    @Test
    void jdkFramesAreStd() {
        assertEquals("java.util.HashMap.putVal std:java/util/HashMap.java:631",
                namer.name("java.util.HashMap", "putVal", 631, null));
        assertEquals("sun.nio.ch.Net.poll std:sun/nio/ch/Net.java",
                namer.name("sun.nio.ch.Net", "poll", -2, null));
    }

    @Test
    void applicationFramesUseThePackagePath() {
        assertEquals("com.example.demo.web.Hot$Inner.spin com/example/demo/web/Hot.java:12",
                namer.name("com.example.demo.web.Hot$Inner", "spin", 12, null));
        // a file name from the stack wins over the guess
        assertEquals("com.example.demo.Kt.run com/example/demo/Kt.kt:3",
                namer.name("com.example.demo.Kt", "run", 3, "Kt.kt"));
    }

    @Test
    void otherFramesAreLibraries() {
        // the SDK is library code even though its classes come from a directory in this build
        assertEquals("io.github.styropyr0.visionary.Values.round lib:io.github/Values.java:20",
                namer.name("io.github.styropyr0.visionary.Values", "round", 20, null));
        // unknown jar: the top two package segments
        assertEquals("org.nowhere.thing.Foo.bar lib:org.nowhere/Foo.java:1",
                namer.name("org.nowhere.thing.Foo", "bar", 1, null));
        // a class from a jar on the test class path: the jar name without its version
        String jackson = namer.name("com.fasterxml.jackson.databind.ObjectMapper", "readTree", 7, null);
        assertEquals("com.fasterxml.jackson.databind.ObjectMapper.readTree lib:jackson-databind/ObjectMapper.java:7",
                jackson);
    }

    @Test
    void hiddenClassSuffixAndSemicolonsAreCleaned() {
        assertEquals("com.example.demo.A$$Lambda.run com/example/demo/A.java",
                namer.name("com.example.demo.A$$Lambda/0x000001f0010a2c00", "run", 0, null));
        assertEquals("com.example.demo.A.we,ird com/example/demo/A.java:1",
                namer.name("com.example.demo.A", "we;ird", 1, null));
    }

    @Test
    void artifactFromJarUrls() {
        assertEquals("spring-webmvc", FrameNamer.artifact(
                "jar:file:/C:/m2/org/springframework/spring-webmvc/6.2.1/spring-webmvc-6.2.1.jar!/org/x/Y.class"));
        assertEquals("tomcat-embed-core", FrameNamer.artifact(
                "jar:nested:/app/app.jar/!BOOT-INF/lib/tomcat-embed-core-10.1.34.jar!/org/apache/X.class"));
        assertEquals("jakarta.servlet-api", FrameNamer.artifact(
                "jar:file:/l/jakarta.servlet-api-6.0.0.jar!/jakarta/servlet/Filter.class"));
        assertEquals("plain", FrameNamer.artifact("jar:file:/l/plain.jar!/a/B.class"));
        assertNull(FrameNamer.artifact("file:/C:/project/target/classes/a/B.class"));
        assertTrue(FrameNamer.isDirectory("jar:nested:/app.jar/!BOOT-INF/classes/!/com/example/A.class"));
    }

    // ---- folding and the cap ----

    @Test
    void foldsHeaviestFirst() {
        FoldedStacks f = new FoldedStacks();
        for (int i = 0; i < 3; i++) {
            f.add(List.of("a", "b"));
        }
        f.add(List.of("a", "c"));
        f.add(List.of());
        assertEquals(4, f.samples());
        assertEquals("a;b 3\na;c 1\n", f.render());
    }

    @Test
    void capSumsTheRestIntoOther() {
        FoldedStacks f = new FoldedStacks();
        String frame = "x".repeat(1000);
        for (int i = 0; i < 200; i++) {
            List<String> stack = List.of("root", frame + i);
            for (int n = 0; n <= i % 3; n++) {
                f.add(stack);
            }
        }
        String out = f.render();
        assertTrue(out.getBytes(java.nio.charset.StandardCharsets.UTF_8).length <= FoldedStacks.MAX_BYTES + 32);
        String[] lines = out.split("\n");
        String last = lines[lines.length - 1];
        // every stack starts at "root", so the rest roll up under it
        assertTrue(last.startsWith("root;(other) "), last);
        long total = 0;
        long prev = Long.MAX_VALUE;
        for (String line : lines) {
            long n = Long.parseLong(line.substring(line.lastIndexOf(' ') + 1));
            if (!line.contains("(other)")) {
                assertTrue(n <= prev, "not heaviest first at " + line);
                prev = n;
            }
            total += n;
        }
        assertEquals(f.samples(), total);
        // heaviest first: the first line has the maximum count
        assertTrue(lines[0].endsWith(" 3"));
        assertEquals(FoldedStacks.MAX_BYTES, 90_000);
    }

    /** Stacks that don't fit are cut after the deepest frame they share with a kept stack. */
    @Test
    void foldRollsUpIntoCallers() {
        FoldedStacks f = new FoldedStacks();
        String heavy = "h".repeat(FoldedStacks.KEEP_WHOLE - 40);
        addTimes(f, List.of("serve", "handler", heavy), 100);
        addTimes(f, List.of("serve", "handler", "json", "encode", "e".repeat(30_000)), 7);
        addTimes(f, List.of("serve", "handler", "json", "encode", "f".repeat(30_000)), 5);
        addTimes(f, List.of("serve", "handler"), 4); // leads into the kept stack: kept as it is
        addTimes(f, List.of("worker", "loop", "w".repeat(30_000)), 3);
        String out = f.render();
        for (String want : List.of("serve;handler;" + heavy + " 100\n", "serve;handler;(other) 12\n",
                "serve;handler 4\n", "(other) 3\n")) {
            assertTrue(out.contains(want), "missing " + want.substring(0, Math.min(40, want.length())));
        }
        assertTrue(out.getBytes(java.nio.charset.StandardCharsets.UTF_8).length <= FoldedStacks.MAX_BYTES);
    }

    private static void addTimes(FoldedStacks f, List<String> stack, int times) {
        for (int i = 0; i < times; i++) {
            f.add(stack);
        }
    }

    // ---- settings ----

    @Test
    void missingSettingsMeanOff() {
        ProfileSettings s = ProfileSettings.parse(Map.of(), json);
        assertFalse(s.enabled());
        assertEquals(300, s.intervalS());
        assertEquals(10_000, s.durationMs());
        assertNull(s.now());
        assertTrue(s.includes("api"));
        assertTrue(s.includes(null));
    }

    @Test
    void servicesFilter() {
        ProfileSettings s = ProfileSettings.parse(Map.of("profile.enabled", "true",
                "profile.services", "[\"api\",\"jobs\"]"), json);
        assertTrue(s.includes("api"));
        assertFalse(s.includes("web"));
        assertFalse(s.includes(null));
        assertFalse(s.includes(""));
        ProfileSettings empty = ProfileSettings.parse(Map.of("profile.services", "[]"), json);
        assertTrue(empty.includes(null));
        assertTrue(empty.includes("web"));
    }

    @Test
    void profileNowFreshness() {
        Instant start = Instant.parse("2026-10-22T10:00:00Z");
        Instant now = Instant.parse("2026-10-22T10:10:00Z");
        assertTrue(ProfileSettings.freshNow("2026-10-22T10:08:00.000Z", null, start, now));
        assertTrue(ProfileSettings.freshNow("2026-10-22 10:08:00", null, start, now));
        // unchanged since the last read
        assertFalse(ProfileSettings.freshNow("2026-10-22T10:08:00.000Z", "2026-10-22T10:08:00.000Z", start, now));
        // older than five minutes
        assertFalse(ProfileSettings.freshNow("2026-10-22T10:04:59.000Z", null, start, now));
        // before this process started
        assertFalse(ProfileSettings.freshNow("2026-10-22T09:59:00.000Z", null, start,
                Instant.parse("2026-10-22T10:01:00Z")));
        assertFalse(ProfileSettings.freshNow("not a time", null, start, now));
        assertFalse(ProfileSettings.freshNow(null, null, start, now));
    }

    @Test
    void hardOffFromConfig() {
        StandardEnvironment env = new StandardEnvironment();
        env.getPropertySources().addFirst(new MapPropertySource("test", Map.of("VISIONARY_PROFILE", "false")));
        assertFalse(VisionaryConfig.from(env).profile());
        StandardEnvironment on = new StandardEnvironment();
        on.getPropertySources().addFirst(new MapPropertySource("test", Map.of("VISIONARY_PROFILE", "true")));
        assertTrue(VisionaryConfig.from(on).profile());
    }

    // ---- the profiler's schedule against a fake store and sampler ----

    private static final class FakeStore implements Store {
        final AtomicInteger selects = new AtomicInteger();
        volatile List<String[]> rows = new ArrayList<>();
        volatile boolean fail;

        @Override
        public void createSchema(Table table) {
        }

        @Override
        public void insert(Map<Table, List<Object[]>> rows) {
        }

        @Override
        public List<String[]> select(String sql) throws Exception {
            selects.incrementAndGet();
            if (fail) {
                throw new java.sql.SQLException("no such table: visionary_settings");
            }
            return rows;
        }

        @Override
        public long purgeChunk(Table table, String cutoff) {
            return 0;
        }

        @Override
        public String describe() {
            return "fake";
        }

        @Override
        public void close() {
        }
    }

    /** Emits a fixed number of samples and finishes one request while "sampling". */
    private static final class FakeSampler implements CpuSampler {
        final AtomicInteger runs = new AtomicInteger();
        int samples = 30;

        @Override
        public void sample(long durationMs, FoldedStacks out) {
            runs.incrementAndGet();
            for (int i = 0; i < samples; i++) {
                out.add(List.of("main", "com.example.demo.Hot.spin com/example/demo/Hot.java:5"));
            }
            ProfileTally.finished("GET", "/hot", 12.4);
        }

        @Override
        public String describe() {
            return "fake";
        }
    }

    private static final long SECOND = 1_000_000_000L;

    private VisionaryProfiler profiler(FakeStore store, FakeSampler sampler, List<ProfileEntry> out, String service,
            boolean allowed) {
        Origin origin = new Origin(service, null, "host-1", "Java 17", null);
        return new VisionaryProfiler(store, origin, out::add, allowed, json, new CpuSampler[] {sampler});
    }

    private static List<String[]> rows(String... kv) {
        List<String[]> r = new ArrayList<>();
        for (int i = 0; i < kv.length; i += 2) {
            r.add(new String[] {kv[i], kv[i + 1]});
        }
        return r;
    }

    @Test
    void hardOffNeverReadsOrSamples() {
        FakeStore store = new FakeStore();
        store.rows = rows("profile.enabled", "true", "profile.interval_s", "1");
        FakeSampler sampler = new FakeSampler();
        List<ProfileEntry> out = new ArrayList<>();
        VisionaryProfiler p = profiler(store, sampler, out, "api", false);
        for (int i = 0; i < 100; i++) {
            ProfileTally.started();
            p.tick(i * SECOND, Instant.now());
        }
        assertEquals(0, store.selects.get());
        assertEquals(0, sampler.runs.get());
        assertTrue(out.isEmpty());
    }

    @Test
    void offUntilEnabledThenEveryInterval() {
        FakeStore store = new FakeStore();
        FakeSampler sampler = new FakeSampler();
        List<ProfileEntry> out = new ArrayList<>();
        VisionaryProfiler p = profiler(store, sampler, out, "api", true);
        long t = 0;
        p.tick(t, Instant.now());
        ProfileTally.started();
        p.tick(t += 10 * SECOND, Instant.now());
        assertFalse(p.active());
        assertEquals(0, sampler.runs.get());

        store.rows = rows("profile.enabled", "true", "profile.interval_s", "5", "profile.duration_ms", "200");
        p.tick(t += 30 * SECOND, Instant.now()); // poll: on, first profile one interval later
        assertTrue(p.active());
        p.tick(t += 5 * SECOND, Instant.now()); // due, but no request since enabling: skipped
        assertEquals(0, sampler.runs.get());
        ProfileTally.started();
        p.tick(t += 5 * SECOND, Instant.now());
        assertEquals(1, sampler.runs.get());
        assertEquals(1, out.size());
        Object[] row = out.get(0).row();
        assertEquals("api", row[1]);
        assertEquals("host-1", row[2]);
        assertEquals("java", row[4]);
        assertEquals(30L, row[6]);
        assertEquals("{\"GET /hot\":12}", row[7]);
        assertEquals("main;com.example.demo.Hot.spin com/example/demo/Hot.java:5 30\n", row[8]);

        store.rows = rows("profile.enabled", "false");
        p.tick(t += 30 * SECOND, Instant.now());
        assertFalse(p.active());
    }

    @Test
    void servicesFilterExcludesThisService() {
        FakeStore store = new FakeStore();
        store.rows = rows("profile.enabled", "true", "profile.services", "[\"other\"]", "profile.interval_s", "1",
                "profile.now", Values.timestamp(Instant.now().plusSeconds(1)));
        FakeSampler sampler = new FakeSampler();
        List<ProfileEntry> out = new ArrayList<>();
        VisionaryProfiler p = profiler(store, sampler, out, "api", true);
        for (int i = 0; i < 50; i++) {
            ProfileTally.started();
            p.tick(i * SECOND, Instant.now().plusSeconds(2));
        }
        assertFalse(p.active());
        assertEquals(0, sampler.runs.get());
    }

    @Test
    void profileNowTakesOneProfileWhileDisabled() {
        FakeStore store = new FakeStore();
        FakeSampler sampler = new FakeSampler();
        List<ProfileEntry> out = new ArrayList<>();
        VisionaryProfiler p = profiler(store, sampler, out, null, true);
        p.tick(0, Instant.now());
        String stamp = Values.timestamp(Instant.now().plusSeconds(1));
        store.rows = rows("profile.now", stamp);
        Instant later = Instant.now().plusSeconds(2);
        p.tick(30 * SECOND, later);
        assertEquals(1, sampler.runs.get());
        assertEquals(1, out.size());
        // the same value again does nothing
        p.tick(60 * SECOND, later);
        p.tick(90 * SECOND, later);
        assertEquals(1, sampler.runs.get());
    }

    @Test
    void tooFewSamplesOrNoRequestIsDropped() {
        FakeStore store = new FakeStore();
        FakeSampler sampler = new FakeSampler();
        sampler.samples = FoldedStacks.MIN_SAMPLES - 1;
        List<ProfileEntry> out = new ArrayList<>();
        VisionaryProfiler p = profiler(store, sampler, out, null, true);
        p.tick(0, Instant.now());
        store.rows = rows("profile.now", Values.timestamp(Instant.now().plusSeconds(1)));
        p.tick(30 * SECOND, Instant.now().plusSeconds(2));
        assertEquals(1, sampler.runs.get());
        assertTrue(out.isEmpty());
    }

    @Test
    void failedReadMeansOff() {
        FakeStore store = new FakeStore();
        store.fail = true;
        FakeSampler sampler = new FakeSampler();
        VisionaryProfiler p = profiler(store, sampler, new ArrayList<>(), "api", true);
        p.tick(0, Instant.now());
        p.tick(30 * SECOND, Instant.now());
        assertFalse(p.active());
        assertEquals(2, store.selects.get());
        assertEquals(0, sampler.runs.get());
    }
}
