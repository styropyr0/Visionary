package io.github.styropyr0.visionary;

import java.io.PrintWriter;
import java.io.StringWriter;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.HexFormat;
import java.util.List;
import java.util.Map;
import java.util.regex.Pattern;

/** Fingerprinting, formatting and rate limiting of captured exceptions (SPEC.md "Errors"). */
final class Errors {

    static final int MAX_MESSAGE = 1000;
    static final int MAX_STACK_BYTES = 16 * 1024;
    static final int PER_FINGERPRINT_PER_MINUTE = 20;
    private static final int FINGERPRINT_FRAMES = 5;
    private static final int MAX_SHORT_MESSAGE = 500;

    /** Frames from these packages are not the application's own code. */
    private static final String[] LIBRARY_PREFIXES = {
        "java.", "javax.", "jakarta.", "sun.", "jdk.", "org.springframework.", "org.apache.",
        "io.github.styropyr0.visionary.",
    };
    private static final Pattern DIGITS = Pattern.compile("\\d+");

    private final Map<String, long[]> windows = new HashMap<>();

    /**
     * SHA-1 over the type and the top five in-app frames as {@code Class.method@File.java}
     * (no line numbers), first 16 hex characters. Without in-app frames, the top five
     * frames of any kind (innermost first); with no frames at all, the type and the
     * message with every digit run replaced by 0.
     */
    static String fingerprint(Throwable t) {
        String type = t.getClass().getName();
        StackTraceElement[] stack = t.getStackTrace();
        List<String> frames = frames(stack, true);
        if (frames.isEmpty()) {
            frames = frames(stack, false);
        }
        String basis = frames.isEmpty()
                ? type + "\n" + DIGITS.matcher(t.getMessage() == null ? "" : t.getMessage()).replaceAll("0")
                : type + "\n" + String.join("\n", frames);
        try {
            byte[] sum = MessageDigest.getInstance("SHA-1").digest(basis.getBytes(StandardCharsets.UTF_8));
            return HexFormat.of().formatHex(sum).substring(0, 16);
        } catch (NoSuchAlgorithmException e) {
            throw new IllegalStateException("SHA-1 is required of every JDK", e);
        }
    }

    /** The top five frames, innermost first; only the application's own when {@code inAppOnly}. */
    private static List<String> frames(StackTraceElement[] stack, boolean inAppOnly) {
        List<String> out = new ArrayList<>(FINGERPRINT_FRAMES);
        for (StackTraceElement f : stack) {
            if (!inAppOnly || inApp(f.getClassName())) {
                String file = f.getFileName() == null ? "" : f.getFileName();
                out.add(f.getClassName() + "." + f.getMethodName() + "@" + file);
                if (out.size() == FINGERPRINT_FRAMES) {
                    break;
                }
            }
        }
        return out;
    }

    static boolean inApp(String className) {
        for (String p : LIBRARY_PREFIXES) {
            if (className.startsWith(p)) {
                return false;
            }
        }
        return true;
    }

    /** At most 20 per fingerprint per wall-clock minute in this process. */
    synchronized boolean allow(String fingerprint, long nowMillis) {
        long minute = nowMillis / 60_000;
        long[] w = windows.get(fingerprint);
        if (w == null || w[0] != minute) {
            if (windows.size() >= 10_000) {
                windows.values().removeIf(old -> old[0] != minute);
            }
            w = new long[] {minute, 0};
            windows.put(fingerprint, w);
        }
        return ++w[1] <= PER_FINGERPRINT_PER_MINUTE;
    }

    static String message(Throwable t) {
        return Values.cap(t.getMessage(), MAX_MESSAGE);
    }

    /** {@code printStackTrace} output, cut to 16 KB of UTF-8. */
    static String stack(Throwable t) {
        StringWriter out = new StringWriter();
        t.printStackTrace(new PrintWriter(out));
        return Values.capBytes(out.toString(), MAX_STACK_BYTES);
    }

    /** For a span's {@code error}: the message, or the class name when there is none. */
    static String shortMessage(Throwable t) {
        String msg = t.getMessage();
        return Values.cap(msg == null || msg.isBlank() ? t.getClass().getName() : msg, MAX_SHORT_MESSAGE);
    }
}
