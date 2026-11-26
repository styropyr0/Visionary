package io.github.styropyr0.visionary;

import static org.junit.jupiter.api.Assertions.assertArrayEquals;
import static org.junit.jupiter.api.Assertions.assertEquals;

import java.io.ByteArrayOutputStream;
import java.io.File;
import java.io.IOException;
import java.io.OutputStream;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Instant;
import java.util.ArrayList;
import java.util.List;
import java.util.jar.JarEntry;
import java.util.jar.JarOutputStream;

import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

class PackagesTest {

    @TempDir
    Path dir;

    private static byte[] pom(String group, String artifact, String version) {
        return ("groupId=" + group + "\nartifactId=" + artifact + "\nversion=" + version + "\n")
                .getBytes(StandardCharsets.UTF_8);
    }

    private static void entry(JarOutputStream jar, String name, byte[] data) throws IOException {
        jar.putNextEntry(new JarEntry(name));
        jar.write(data);
        jar.closeEntry();
    }

    private static byte[] jar(String group, String artifact, String version) throws IOException {
        ByteArrayOutputStream out = new ByteArrayOutputStream();
        try (JarOutputStream jar = new JarOutputStream(out)) {
            entry(jar, "META-INF/maven/" + group + "/" + artifact + "/pom.properties", pom(group, artifact, version));
            entry(jar, "x/Y.class", new byte[] {1, 2, 3});
        }
        return out.toByteArray();
    }

    @Test
    void scansPlainNestedAndExplodedEntries() throws IOException {
        Path plain = dir.resolve("plain.jar");
        Files.write(plain, jar("org.slf4j", "slf4j-api", "2.0.16"));

        Path fat = dir.resolve("app.jar");
        try (OutputStream f = Files.newOutputStream(fat); JarOutputStream jar = new JarOutputStream(f)) {
            entry(jar, "META-INF/maven/com.example/app/pom.properties", pom("com.example", "app", "1.0.0"));
            entry(jar, "BOOT-INF/lib/jackson-databind-2.18.2.jar",
                    jar("com.fasterxml.jackson.core", "jackson-databind", "2.18.2"));
            entry(jar, "BOOT-INF/lib/slf4j-api-2.0.16.jar", jar("org.slf4j", "slf4j-api", "2.0.16"));
            entry(jar, "BOOT-INF/lib/broken.jar", jar("org.broken", "broken", ""));
        }

        Path classes = dir.resolve("classes");
        Path maven = classes.resolve("META-INF/maven/org.acme/lib");
        Files.createDirectories(maven);
        Files.write(maven.resolve("pom.properties"), pom("org.acme", "lib", "0.3.1"));

        String cp = String.join(File.pathSeparator, plain.toString(), fat.toString(), classes.toString(),
                dir.resolve("missing.jar").toString());
        List<String> got = new ArrayList<>();
        for (String[] p : InstalledPackages.scan(cp)) {
            got.add(p[0] + "@" + p[1]);
        }
        assertEquals(List.of("org.slf4j:slf4j-api@2.0.16", "com.example:app@1.0.0",
                "com.fasterxml.jackson.core:jackson-databind@2.18.2", "org.acme:lib@0.3.1"), got);
    }

    @Test
    void rowsCarryTheOriginAndCap() {
        InstalledPackages packages = new InstalledPackages(null, new Origin(null, "abc", "i", "Java", null));
        List<Object[]> rows = packages.rows(List.<String[]>of(new String[] {"g:a", "1.0"}),
                Instant.parse("2026-01-01T00:00:00Z"));
        assertArrayEquals(new Object[] {"2026-01-01T00:00:00.000Z", null, "i", "abc", "Maven", "g:a", "1.0"},
                rows.get(0));
        assertEquals(Table.APP_PACKAGES.columns.size(), rows.get(0).length);

        List<String[]> many = new ArrayList<>();
        for (int i = 0; i < 6000; i++) {
            many.add(new String[] {"g:a" + i, "1.0"});
        }
        assertEquals(InstalledPackages.MAX, packages.rows(many, Instant.now()).size());
    }
}
