package io.github.styropyr0.visionary;

import java.io.IOException;
import java.math.BigDecimal;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.Map;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;

/**
 * Cloudflare D1 over its REST API. Every param is sent as a string ({@code ""}
 * for null) and every placeholder is wrapped in {@code NULLIF(?, '')}, so column
 * affinity restores numbers and empty strings become NULL.
 */
final class D1Store implements Store {

    private static final int MAX_STATEMENTS_PER_BATCH = 50;
    static final int MAX_PARAMS = 100;
    private static final Duration REQUEST_TIMEOUT = Duration.ofSeconds(30);

    private final ObjectMapper json;
    private final HttpClient http;
    private final URI queryUri;
    private final URI rawUri;
    private final String token;

    D1Store(String apiBase, String accountId, String databaseId, String token, ObjectMapper json) {
        String base = apiBase.endsWith("/") ? apiBase.substring(0, apiBase.length() - 1) : apiBase;
        String database = base + "/accounts/" + accountId + "/d1/database/" + databaseId;
        this.queryUri = URI.create(database + "/query");
        this.rawUri = URI.create(database + "/raw");
        this.token = token;
        this.json = json;
        this.http = HttpClient.newBuilder().connectTimeout(Duration.ofSeconds(10)).build();
    }

    @Override
    public void createSchema(Table table) throws IOException, InterruptedException {
        List<Map<String, Object>> stmts = new ArrayList<>();
        for (String sql : Dialect.D1.schema(table.schemaName)) {
            stmts.add(Map.of("sql", sql, "params", List.of()));
        }
        post(queryUri, Map.of("batch", stmts));
        if (!table.addedColumns.isEmpty()) {
            addMissingColumns(table);
        }
        if (table.traceIndex) {
            post(queryUri, Map.of("sql", Dialect.D1.traceIndex(table.name), "params", List.of()));
        }
    }

    /** Upgrades a table created by an older release in place. */
    private void addMissingColumns(Table table) throws IOException, InterruptedException {
        JsonNode res = post(rawUri, Map.of("sql", Dialect.D1.columnsQuery(table.name), "params", List.of()));
        List<String> existing = new ArrayList<>();
        for (JsonNode row : res.path("result").path(0).path("results").path("rows")) {
            existing.add(row.path(0).asText());
        }
        for (String alter : Dialect.D1.missingColumns(table, existing)) {
            try {
                post(queryUri, Map.of("sql", alter, "params", List.of()));
            } catch (IOException e) {
                if (!Dialect.isDuplicateColumn(e)) {
                    throw e;
                }
            }
        }
    }

    @Override
    public void insert(Map<Table, List<Object[]>> rows) throws IOException, InterruptedException {
        List<Map<String, Object>> stmts = new ArrayList<>();
        for (Map.Entry<Table, List<Object[]>> e : rows.entrySet()) {
            Table t = e.getKey();
            String[] sql = rowSql(t);
            for (Object[] row : e.getValue()) {
                if (stmts.size() + sql.length > MAX_STATEMENTS_PER_BATCH) {
                    post(queryUri, Map.of("batch", List.copyOf(stmts)));
                    stmts.clear();
                }
                int split = Math.min(row.length, MAX_PARAMS);
                stmts.add(Map.of("sql", sql[0], "params", params(row, 0, split)));
                if (sql.length > 1) {
                    stmts.add(Map.of("sql", sql[1], "params", params(row, split, row.length)));
                }
            }
        }
        if (!stmts.isEmpty()) {
            post(queryUri, Map.of("batch", stmts));
        }
    }

    /**
     * The row's INSERT. D1 binds at most 100 params, so a wider row inserts the first
     * 100 columns and sets the rest with an UPDATE of the row just inserted, kept in
     * the same batch
     */
    static String[] rowSql(Table t) {
        if (t.columns.size() <= MAX_PARAMS) {
            return new String[] {t.insertSql("NULLIF(?, '')")};
        }
        List<String> head = t.columns.subList(0, MAX_PARAMS);
        String insert = "INSERT INTO " + t.name + " (" + String.join(", ", head) + ") VALUES ("
                + String.join(", ", Collections.nCopies(MAX_PARAMS, "NULLIF(?, '')")) + ")";
        List<String> set = new ArrayList<>();
        for (String c : t.columns.subList(MAX_PARAMS, t.columns.size())) {
            set.add(c + " = NULLIF(?, '')");
        }
        return new String[] {insert, "UPDATE " + t.name + " SET " + String.join(", ", set) + " WHERE id = last_insert_rowid()"};
    }

    private static List<String> params(Object[] row, int from, int to) {
        List<String> out = new ArrayList<>(to - from);
        for (int i = from; i < to; i++) {
            out.add(param(row[i]));
        }
        return out;
    }

    @Override
    public List<String[]> select(String sql) throws IOException, InterruptedException {
        JsonNode res = post(rawUri, Map.of("sql", sql, "params", List.of()));
        List<String[]> out = new ArrayList<>();
        for (JsonNode row : res.path("result").path(0).path("results").path("rows")) {
            String[] values = new String[row.size()];
            for (int i = 0; i < row.size(); i++) {
                values[i] = row.path(i).isNull() ? null : row.path(i).asText();
            }
            out.add(values);
        }
        return out;
    }

    @Override
    public long purgeChunk(Table table, String cutoff) throws IOException, InterruptedException {
        JsonNode res = post(queryUri, Map.of("sql", Dialect.D1.purgeSql(table.name), "params", List.of(cutoff)));
        return res.path("result").path(0).path("meta").path("changes").asLong(0);
    }

    @Override
    public String describe() {
        return "Cloudflare D1";
    }

    @Override
    public void close() {
        // HttpClient has nothing to release on Java 17.
    }

    private JsonNode post(URI uri, Object body) throws IOException, InterruptedException {
        HttpRequest req = HttpRequest.newBuilder(uri)
                .timeout(REQUEST_TIMEOUT)
                .header("Authorization", "Bearer " + token)
                .header("Content-Type", "application/json")
                .POST(HttpRequest.BodyPublishers.ofByteArray(json.writeValueAsBytes(body)))
                .build();
        HttpResponse<byte[]> resp = http.send(req, HttpResponse.BodyHandlers.ofByteArray());
        JsonNode node;
        try {
            node = json.readTree(resp.body());
        } catch (IOException e) {
            node = null;
        }
        if (resp.statusCode() >= 300 || node == null || !node.path("success").asBoolean(false)) {
            throw new IOException("d1: HTTP " + resp.statusCode() + ": " + errorMessage(node, resp.body()));
        }
        return node;
    }

    private static String errorMessage(JsonNode node, byte[] body) {
        if (node != null && node.path("errors").isArray() && !node.path("errors").isEmpty()) {
            List<String> parts = new ArrayList<>();
            for (JsonNode e : node.path("errors")) {
                parts.add(e.path("code").asText() + " " + e.path("message").asText());
            }
            return String.join("; ", parts);
        }
        String text = new String(body, StandardCharsets.UTF_8);
        return text.length() > 200 ? text.substring(0, 200) : text;
    }

    static String param(Object v) {
        if (v == null) {
            return "";
        }
        if (v instanceof Double d) {
            return d.isNaN() || d.isInfinite() ? "" : BigDecimal.valueOf(d).toPlainString();
        }
        return v.toString();
    }
}
