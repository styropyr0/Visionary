package io.github.styropyr0.visionary;

/**
 * Who writes the rows: the service, its release, this instance and the runtime.
 * {@code service} and {@code version} are null when not configured.
 */
record Origin(String service, String version, String instance, String runtime, String sdkVersion) {

    static Origin of(VisionaryConfig cfg) {
        return new Origin(Values.emptyToNull(cfg.service()), Values.emptyToNull(cfg.release()),
                cfg.resolvedInstance(), javaRuntime(), Visionary.class.getPackage().getImplementationVersion());
    }

    /** For example {@code OpenJDK 21.0.4}: the runtime's first word and the Java version. */
    static String javaRuntime() {
        String name = System.getProperty("java.runtime.name", "Java");
        String first = name.isBlank() ? "Java" : name.trim().split("\s+")[0];
        if (first.startsWith("Java")) {
            first = "Java";
        }
        return first + " " + System.getProperty("java.version", "");
    }
}
