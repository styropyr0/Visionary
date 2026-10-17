package io.github.styropyr0.visionary;

import java.io.IOException;
import java.io.InputStream;
import java.io.UncheckedIOException;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.List;

/** A storage dialect and the schema shipped for it. */
enum Dialect {
    D1("sqlite"),
    SQLITE("sqlite"),
    POSTGRES("postgres"),
    MYSQL("mysql");

    private final String schemaDir;

    Dialect(String schemaDir) {
        this.schemaDir = schemaDir;
    }

    /** The statements of {@code schema/<dialect>/<name>.sql}, split on {@code ;}. */
    List<String> schema(String name) {
        String path = "schema/" + schemaDir + "/" + name + ".sql";
        try (InputStream in = Dialect.class.getResourceAsStream(path)) {
            if (in == null) {
                throw new IllegalStateException("missing resource " + path);
            }
            String sql = new String(in.readAllBytes(), StandardCharsets.UTF_8);
            List<String> out = new ArrayList<>();
            for (String stmt : sql.split(";")) {
                if (!stmt.isBlank()) {
                    out.add(stmt.trim());
                }
            }
            return out;
        } catch (IOException e) {
            throw new UncheckedIOException(e);
        }
    }

    /**
     * The chunked retention delete. MySQL rejects LIMIT inside an IN subquery,
     * so it uses the single-table DELETE ... LIMIT form instead.
     */
    String purgeSql(String table) {
        if (this == MYSQL) {
            return "DELETE FROM " + table + " WHERE created_at < ? ORDER BY id LIMIT 5000";
        }
        return "DELETE FROM " + table + " WHERE id IN (SELECT id FROM " + table
                + " WHERE created_at < ? LIMIT 5000)";
    }
}
