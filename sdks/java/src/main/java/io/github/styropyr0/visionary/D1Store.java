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
    private final String token;

    D1Store(String apiBase, String accountId, String databaseId, String token, ObjectMapper json) {
        String base = apiBase.endsWith("/") ? apiBase.substring(0, apiBase.length() - 1) : apiBase;
        this.queryUri = URI.create(base + "/accounts/" + accountId + "/d1/database/" + databaseId + "/query");
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
        post(Map.of("batch", stmts));
    }

    @Override
    public void insert(Table table, List<Object[]> rows) throws IOException, InterruptedException {
        String sql = table.insertSql("NULLIF(?, '')");
        for (int from = 0; from < rows.size(); from += MAX_STATEMENTS_PER_BATCH) {
            List<Map<String, Object>> stmts = new ArrayList<>();
            for (Object[] row : rows.subList(from, Math.min(rows.size(), from + MAX_STATEMENTS_PER_BATCH))) {
                List<String> params = new ArrayList<>(row.length);
                for (Object v : row) {
                    params.add(param(v));
                }
                stmts.add(Map.of("sql", sql, "params", params));
            }
            post(Map.of("batch", stmts));
        }
    }

    @Override
    public long purgeChunk(Table table, String cutoff) throws IOException, InterruptedException {
        JsonNode res = post(Map.of("sql", Dialect.D1.purgeSql(table.name), "params", List.of(cutoff)));
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

    private JsonNode post(Object body) throws IOException, InterruptedException {
        HttpRequest req = HttpRequest.newBuilder(queryUri)
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
