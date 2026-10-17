package io.github.styropyr0.visionary;

import java.util.ArrayList;
import java.util.List;
import java.util.Locale;
import java.util.Set;
import java.util.regex.Pattern;

/**
 * Statement text for {@code db} spans: literals replaced so parameter values are
 * never stored, and a short grouping name such as {@code SELECT users}.
 */
final class SqlText {

    private static final Pattern IN_LIST = Pattern.compile("(?i)\\b(IN) ?\\( ?\\?(?: ?, ?\\?)* ?\\)");
    private static final Set<String> MAIN_KEYWORDS = Set.of("SELECT", "INSERT", "UPDATE", "DELETE", "MERGE",
            "REPLACE");
    private static final Set<String> UPDATE_MODIFIERS = Set.of("ONLY", "LOW_PRIORITY", "IGNORE");

    private SqlText() {
    }

    /**
     * Replaces string literals ({@code '...'}, doubled quotes inside) and numeric
     * literals with {@code ?}, collapses {@code IN (?, ?, ...)} to {@code IN (?)} and
     * runs of whitespace to one space, and drops comments. Quoted identifiers and
     * names containing digits ({@code u1_0}, {@code $1}) are kept.
     */
    static String sanitize(String sql) {
        int n = sql.length();
        StringBuilder out = new StringBuilder(Math.min(n, 4096));
        int i = 0;
        while (i < n) {
            char c = sql.charAt(i);
            if (c == '\'') {
                i = skipString(sql, i);
                out.append('?');
            } else if (c == '"' || c == '`') {
                int end = sql.indexOf(c, i + 1);
                end = end < 0 ? n : end + 1;
                out.append(sql, i, end);
                i = end;
            } else if (c == '-' && i + 1 < n && sql.charAt(i + 1) == '-') {
                int end = sql.indexOf('\n', i);
                i = end < 0 ? n : end;
                space(out);
            } else if (c == '/' && i + 1 < n && sql.charAt(i + 1) == '*') {
                int end = sql.indexOf("*/", i + 2);
                i = end < 0 ? n : end + 2;
                space(out);
            } else if (identifierStart(c)) {
                int start = i;
                while (i < n && identifierPart(sql.charAt(i))) {
                    i++;
                }
                out.append(sql, start, i);
            } else if (Character.isDigit(c) || (c == '.' && i + 1 < n && Character.isDigit(sql.charAt(i + 1)))) {
                i = skipNumber(sql, i);
                out.append('?');
            } else if (Character.isWhitespace(c)) {
                while (i < n && Character.isWhitespace(sql.charAt(i))) {
                    i++;
                }
                space(out);
            } else {
                out.append(c);
                i++;
            }
        }
        return IN_LIST.matcher(out).replaceAll("$1 (?)").trim();
    }

    /**
     * The first keyword and the main table: {@code SELECT users}, {@code INSERT bookings},
     * {@code UPDATE trips}; just the keyword ({@code BEGIN}, {@code SET}) otherwise.
     */
    static String name(String sanitized) {
        List<Word> words = words(sanitized);
        if (words.isEmpty()) {
            return "SQL";
        }
        int at = 0;
        String keyword = words.get(0).keyword();
        if (keyword.equals("WITH")) {
            for (int i = 1; i < words.size(); i++) {
                Word w = words.get(i);
                if (w.depth == 0 && MAIN_KEYWORDS.contains(w.keyword())) {
                    at = i;
                    keyword = w.keyword();
                    break;
                }
            }
        }
        String table = switch (keyword) {
            case "SELECT", "DELETE" -> after(words, at, "FROM");
            case "INSERT", "REPLACE", "MERGE" -> after(words, at, "INTO");
            case "UPDATE" -> next(words, at);
            case "CALL", "EXEC", "EXECUTE" -> next(words, at);
            default -> null;
        };
        return Values.cap(table == null ? keyword : keyword + " " + table, 255);
    }

    private static String after(List<Word> words, int from, String keyword) {
        for (int i = from + 1; i < words.size(); i++) {
            Word w = words.get(i);
            if (w.depth == 0 && !w.quoted && w.keyword().equals(keyword)) {
                return next(words, i);
            }
        }
        return null;
    }

    /** The table name after position {@code at}, or null when a subquery follows. */
    private static String next(List<Word> words, int at) {
        int depth = words.get(at).depth;
        for (int i = at + 1; i < words.size(); i++) {
            Word w = words.get(i);
            if (w.text.equals("(")) {
                return null;
            }
            if (w.depth == depth && !(UPDATE_MODIFIERS.contains(w.keyword()) && !w.quoted)) {
                return w.text;
            }
        }
        return null;
    }

    /** Names, keywords and opening parentheses with their nesting depth; quotes removed. */
    private static List<Word> words(String sql) {
        List<Word> out = new ArrayList<>();
        int depth = 0;
        int n = sql.length();
        int i = 0;
        while (i < n) {
            char c = sql.charAt(i);
            if (c == '(') {
                out.add(new Word("(", depth, false));
                depth++;
                i++;
            } else if (c == ')') {
                depth = Math.max(0, depth - 1);
                i++;
            } else if (c == '\'') {
                i = skipString(sql, i);
            } else if (identifierStart(c) || c == '"' || c == '`' || c == '[') {
                StringBuilder name = new StringBuilder();
                boolean quoted = false;
                while (i < n) {
                    char d = sql.charAt(i);
                    if (d == '"' || d == '`' || d == '[') {
                        char close = d == '[' ? ']' : d;
                        int end = sql.indexOf(close, i + 1);
                        end = end < 0 ? n : end;
                        name.append(sql, i + 1, end);
                        quoted = true;
                        i = Math.min(n, end + 1);
                    } else if (identifierPart(d) || d == '.') {
                        name.append(d);
                        i++;
                    } else {
                        break;
                    }
                }
                if (name.length() > 0) {
                    out.add(new Word(name.toString(), depth, quoted));
                }
            } else {
                i++;
            }
        }
        return out;
    }

    private static int skipString(String sql, int i) {
        int n = sql.length();
        i++;
        while (i < n) {
            if (sql.charAt(i) == '\'') {
                if (i + 1 < n && sql.charAt(i + 1) == '\'') {
                    i += 2;
                    continue;
                }
                return i + 1;
            }
            i++;
        }
        return n;
    }

    private static int skipNumber(String sql, int i) {
        int n = sql.length();
        if (sql.startsWith("0x", i) || sql.startsWith("0X", i)) {
            i += 2;
            while (i < n && Character.digit(sql.charAt(i), 16) >= 0) {
                i++;
            }
            return i;
        }
        while (i < n && (Character.isDigit(sql.charAt(i)) || sql.charAt(i) == '.')) {
            i++;
        }
        if (i < n && (sql.charAt(i) == 'e' || sql.charAt(i) == 'E')) {
            int j = i + 1;
            if (j < n && (sql.charAt(j) == '+' || sql.charAt(j) == '-')) {
                j++;
            }
            if (j < n && Character.isDigit(sql.charAt(j))) {
                i = j;
                while (i < n && Character.isDigit(sql.charAt(i))) {
                    i++;
                }
            }
        }
        return i;
    }

    private static boolean identifierStart(char c) {
        return Character.isLetter(c) || c == '_' || c == '$';
    }

    /** Digits inside a name ({@code u1_0}, {@code $1}) belong to it and are not literals. */
    private static boolean identifierPart(char c) {
        return identifierStart(c) || Character.isDigit(c);
    }

    private static void space(StringBuilder out) {
        if (out.length() > 0 && out.charAt(out.length() - 1) != ' ') {
            out.append(' ');
        }
    }

    private record Word(String text, int depth, boolean quoted) {
        String keyword() {
            return quoted ? text : text.toUpperCase(Locale.ROOT);
        }
    }
}
