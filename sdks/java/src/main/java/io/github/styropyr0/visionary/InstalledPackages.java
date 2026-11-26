package io.github.styropyr0.visionary;

import java.io.File;
import java.io.IOException;
import java.io.InputStream;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Enumeration;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Properties;
import java.util.jar.JarEntry;
import java.util.jar.JarFile;
import java.util.jar.JarInputStream;
import java.util.regex.Pattern;
import java.util.stream.Stream;

import org.apache.commons.logging.Log;
import org.apache.commons.logging.LogFactory;

/** Reports the class path's Maven artifacts to {@code app_packages} once, 30 seconds after start */
final class InstalledPackages {

    private static final Log log = LogFactory.getLog(InstalledPackages.class);

    static final Duration DELAY = Duration.ofSeconds(30);
    static final int MAX = 5000;
    private static final Pattern POM = Pattern.compile("META-INF/maven/[^/]+/[^/]+/pom\\.properties");
    private static final Pattern NESTED = Pattern.compile("(BOOT-INF|WEB-INF)/lib/[^/]+\\.jar");

    private final Store store;
    private final Origin origin;
    private final Thread thread;

    InstalledPackages(Store store, Origin origin) {
        this.store = store;
        this.origin = origin;
        this.thread = new Thread(this::run, "visionary-packages");
        this.thread.setDaemon(true);
    }

    void start() {
        thread.start();
    }

    void stop() {
        thread.interrupt();
    }

    private void run() {
        try {
            Thread.sleep(DELAY.toMillis());
        } catch (InterruptedException e) {
            return;
        }
        try {
            List<Object[]> rows = rows(scan(System.getProperty("java.class.path", "")), Instant.now());
            if (rows.isEmpty()) {
                return;
            }
            store.createSchema(Table.APP_PACKAGES);
            store.insert(Table.APP_PACKAGES, rows);
        } catch (Exception e) {
            log.warn("visionary: write packages failed: " + e);
        }
    }

    /** groupId:artifactId and version from every pom.properties on the class path, Spring Boot and WAR nested jars included */
    static List<String[]> scan(String classPath) {
        Map<String, String[]> found = new LinkedHashMap<>();
        for (String entry : classPath.split(Pattern.quote(File.pathSeparator))) {
            if (entry.isBlank() || found.size() >= MAX) {
                continue;
            }
            Path p = Path.of(entry);
            try {
                if (Files.isDirectory(p)) {
                    scanDirectory(p, found);
                } else if (Files.isRegularFile(p)) {
                    scanJar(p, found);
                }
            } catch (IOException | RuntimeException e) {
                log.debug("visionary: could not read " + entry + ": " + e);
            }
        }
        return new ArrayList<>(found.values());
    }

    List<Object[]> rows(List<String[]> pkgs, Instant at) {
        String ts = Values.timestamp(at);
        List<Object[]> rows = new ArrayList<>();
        for (String[] p : pkgs) {
            if (rows.size() == MAX) {
                break;
            }
            rows.add(new Object[] {
                ts, origin.service(), origin.instance(), origin.version(), "Maven",
                Values.cap(p[0], 255), Values.cap(p[1], 128),
            });
        }
        return rows;
    }

    private static void scanDirectory(Path dir, Map<String, String[]> found) throws IOException {
        Path maven = dir.resolve("META-INF").resolve("maven");
        if (Files.isDirectory(maven)) {
            try (Stream<Path> poms = Files.find(maven, 3, (f, a) -> f.getFileName().toString().equals("pom.properties"))) {
                for (Path pom : (Iterable<Path>) poms::iterator) {
                    try (InputStream in = Files.newInputStream(pom)) {
                        add(in, found);
                    }
                }
            }
        }
        for (String lib : new String[] {"BOOT-INF", "WEB-INF"}) {
            Path libs = dir.resolve(lib).resolve("lib");
            if (Files.isDirectory(libs)) {
                try (Stream<Path> jars = Files.list(libs)) {
                    for (Path jar : (Iterable<Path>) jars::iterator) {
                        if (jar.toString().endsWith(".jar")) {
                            scanJar(jar, found);
                        }
                    }
                }
            }
        }
    }

    private static void scanJar(Path path, Map<String, String[]> found) throws IOException {
        try (JarFile jar = new JarFile(path.toFile(), false)) {
            Enumeration<JarEntry> entries = jar.entries();
            while (entries.hasMoreElements()) {
                JarEntry e = entries.nextElement();
                if (POM.matcher(e.getName()).matches()) {
                    try (InputStream in = jar.getInputStream(e)) {
                        add(in, found);
                    }
                } else if (NESTED.matcher(e.getName()).matches()) {
                    try (JarInputStream nested = new JarInputStream(jar.getInputStream(e), false)) {
                        for (JarEntry n = nested.getNextJarEntry(); n != null; n = nested.getNextJarEntry()) {
                            if (POM.matcher(n.getName()).matches()) {
                                add(nested, found);
                            }
                        }
                    }
                }
            }
        }
    }

    /** Reads one pom.properties and leaves the stream open for the nested jar's next entry */
    private static void add(InputStream in, Map<String, String[]> found) throws IOException {
        Properties props = new Properties();
        props.load(in);
        String group = props.getProperty("groupId", "").trim();
        String artifact = props.getProperty("artifactId", "").trim();
        String version = props.getProperty("version", "").trim();
        if (group.isEmpty() || artifact.isEmpty() || version.isEmpty() || found.size() >= MAX) {
            return;
        }
        String name = group + ":" + artifact;
        found.putIfAbsent(name + "\n" + version, new String[] {name, version});
    }
}
