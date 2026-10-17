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
            String sql = e.getKey().insertSql("NULLIF(?, '')");
            for (Object[] row : e.getValue()) {
                List<String> params = new ArrayList<>(row.length);
                for (Object v : row) {
                    params.add(param(v));
                }
                stmts.add(Map.of("sql", sql, "params", params));
            }
        }
        for (int from = 0; from < stmts.size(); from += MAX_STATEMENTS_PER_BATCH) {
            int to = Math.min(stmts.size(), from + MAX_STATEMENTS_PER_BATCH);
            post(queryUri, Map.of("batch", stmts.subList(from, to)));
        }
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
