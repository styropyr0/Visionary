package io.github.styropyr0.visionary;

import java.net.URL;
import java.util.List;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;
import java.util.regex.Pattern;

/**
 * Names a sampled frame {@code pkg.Class.method File.java:line}, with the file
 * prefixed by whose code it is (SPEC.md, "CPU profiles"):
 * <ul>
 * <li>the JDK: {@code std:java/util/HashMap.java};
 * <li>the application (its auto-configuration packages, or classes loaded from a
 * directory rather than a jar): {@code com/example/OrderService.java};
 * <li>everything else, the SDK included: {@code lib:<jar without version>/File.java},
 * or {@code lib:<top two package segments>/File.java} when the jar is unknown.
 * </ul>
 * The owner is looked up once per package.
 */
final class FrameNamer {

    private static final List<String> STD = List.of("java.", "javax.", "jdk.", "sun.", "com.sun.");
    private static final String SDK_PACKAGE = FrameNamer.class.getPackageName();
    private static final Pattern HIDDEN_SUFFIX = Pattern.compile("[./]0x[0-9a-fA-F]+$");
    private static final Pattern JAR_VERSION = Pattern.compile("-\\d[\\w.+-]*$");
    private static final String APP = "";

    private final List<String> appPackages;
    private final ClassLoader loader;
    /** Package to file prefix: "" for the application, "std:", "lib:name/". */
    private final Map<String, String> owners = new ConcurrentHashMap<>();
    private final Map<String, String> names = new ConcurrentHashMap<>();

    FrameNamer(List<String> appPackages, ClassLoader loader) {
        this.appPackages = List.copyOf(appPackages);
        this.loader = loader;
    }

    /**
     * @param className binary class name, {@code com.example.Foo$Inner}
     * @param line      the source line, or a value below 1 when unknown
     * @param fileName  the source file when the stack carries it, else null
     */
    String name(String className, String method, int line, String fileName) {
        String key = className + '#' + method + '#' + line + '#' + fileName;
        String cached = names.get(key);
        if (cached != null) {
            return cached;
        }
        String cls = HIDDEN_SUFFIX.matcher(className).replaceFirst("");
        String path = path(cls, className, fileName);
        String out = (cls + "." + method + (path == null ? "" : " " + path + (line > 0 ? ":" + line : "")))
                .replace(';', ',').replace('\n', ' ');
        if (names.size() < 50_000) {
            names.put(key, out);
        }
        return out;
    }

    /**
     * The file part of a frame name, for {@code app_logs.code_file}: {@code std:java/util/HashMap.java},
     * {@code com/example/OrderService.java}, {@code lib:spring-webmvc/DispatcherServlet.java}; null when
     * there is no file.
     */
    String file(String className, String fileName) {
        return path(HIDDEN_SUFFIX.matcher(className).replaceFirst(""), className, fileName);
    }

    private String path(String cls, String className, String fileName) {
        int dot = cls.lastIndexOf('.');
        String pkg = dot < 0 ? "" : cls.substring(0, dot);
        String simple = dot < 0 ? cls : cls.substring(dot + 1);
        String file = fileName != null && !fileName.isEmpty() ? fileName : sourceFile(simple);
        if (file == null) {
            return null;
        }
        String owner = owner(pkg, className);
        // Library frames name the jar and the file; the JDK and the application the package path.
        return owner.startsWith("lib:") ? owner + file : owner + packagePath(pkg, file);
    }

    /** {@code Foo$Bar$$Lambda} → {@code Foo.java}; null for names that are not a class. */
    static String sourceFile(String simpleName) {
        int dollar = simpleName.indexOf('$');
        String outer = dollar < 0 ? simpleName : simpleName.substring(0, dollar);
        return outer.isEmpty() ? null : outer + ".java";
    }

    private static String packagePath(String pkg, String file) {
        return pkg.isEmpty() ? file : pkg.replace('.', '/') + "/" + file;
    }

    /** The file prefix for a package: "std:", "lib:artifact/" or "" for the application. */
    String owner(String pkg, String className) {
        String o = owners.get(pkg);
        if (o == null) {
            o = classify(pkg, className);
            owners.put(pkg, o);
        }
        return o;
    }

    private String classify(String pkg, String className) {
        String withDot = pkg + ".";
        for (String p : STD) {
            if (withDot.startsWith(p)) {
                return "std:";
            }
        }
        boolean sdk = pkg.equals(SDK_PACKAGE) || pkg.startsWith(SDK_PACKAGE + ".");
        if (!sdk) {
            for (String app : appPackages) {
                if (pkg.equals(app) || pkg.startsWith(app + ".")) {
                    return APP;
                }
            }
        }
        String location = location(className);
        if (!sdk && location != null && isDirectory(location)) {
            return APP;
        }
        String artifact = location == null ? null : artifact(location);
        if (artifact == null || artifact.isEmpty()) {
            String[] parts = pkg.split("\\.");
            artifact = parts.length >= 2 ? parts[0] + "." + parts[1] : (pkg.isEmpty() ? "default" : pkg);
        }
        return "lib:" + artifact + "/";
    }

    private String location(String className) {
        if (loader == null) {
            return null;
        }
        int dollar = className.indexOf('$');
        String outer = dollar < 0 ? className : className.substring(0, dollar);
        try {
            URL url = loader.getResource(outer.replace('.', '/') + ".class");
            return url == null ? null : url.toString();
        } catch (RuntimeException e) {
            return null;
        }
    }

    /** Classes compiled into a directory, or a Boot jar's own {@code BOOT-INF/classes}. */
    static boolean isDirectory(String location) {
        return location.startsWith("file:") || location.contains("BOOT-INF/classes")
                || location.contains("WEB-INF/classes");
    }

    /** {@code jar:file:/m2/spring-webmvc-6.2.1.jar!/org/...} → {@code spring-webmvc}. */
    static String artifact(String location) {
        int bang = location.lastIndexOf("!/");
        String jar = bang < 0 ? location : location.substring(0, bang);
        if (jar.endsWith("/")) {
            jar = jar.substring(0, jar.length() - 1);
        }
        if (!jar.toLowerCase(java.util.Locale.ROOT).endsWith(".jar")) {
            if (location.startsWith("jrt:/")) {
                String rest = location.substring(5);
                int slash = rest.indexOf('/');
                return slash < 0 ? rest : rest.substring(0, slash);
            }
            return null;
        }
        jar = jar.substring(0, jar.length() - 4);
        int cut = Math.max(jar.lastIndexOf('/'), Math.max(jar.lastIndexOf('!'), jar.lastIndexOf(':')));
        String name = jar.substring(cut + 1);
        String bare = JAR_VERSION.matcher(name).replaceFirst("");
        return bare.isEmpty() ? name : bare;
    }
}
