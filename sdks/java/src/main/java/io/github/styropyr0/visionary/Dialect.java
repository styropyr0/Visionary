package io.github.styropyr0.visionary;

import java.io.IOException;
import java.io.InputStream;
import java.io.UncheckedIOException;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Collection;
import java.util.HashSet;
import java.util.List;
import java.util.Locale;
import java.util.Set;

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

    /** Lists a table's column names, one per row (SPEC.md "System metrics"). */
    String columnsQuery(String table) {
        return switch (this) {
            case POSTGRES -> "SELECT column_name FROM information_schema.columns"
                    + " WHERE table_schema = current_schema() AND table_name = '" + table + "'";
            case MYSQL -> "SELECT column_name FROM information_schema.columns"
                    + " WHERE table_schema = DATABASE() AND table_name = '" + table + "'";
            case SQLITE, D1 -> "SELECT name FROM pragma_table_info('" + table + "')";
        };
    }

    /** ALTER statements for the table's added columns missing from {@code existing} (any case). */
    List<String> missingColumns(Table table, Collection<String> existing) {
        Set<String> have = new HashSet<>();
        for (String c : existing) {
            have.add(c.toLowerCase(Locale.ROOT));
        }
        List<String> out = new ArrayList<>();
        for (String column : table.addedColumns) {
            if (!have.contains(column)) {
                out.add("ALTER TABLE " + table.name + " ADD COLUMN " + column + " " + sqlType(table.typeOf(column)));
            }
        }
        return out;
    }

    /** Column types as in {@code SQLType} of sdks/go/migrate.go. */
    private String sqlType(Table.Type type) {
        if (type == Table.Type.TEXT) {
            return this == MYSQL ? "VARCHAR(255)" : "TEXT";
        }
        boolean integer = type == Table.Type.INT;
        return switch (this) {
            case POSTGRES -> integer ? "BIGINT" : "DOUBLE PRECISION";
            case MYSQL -> integer ? "BIGINT" : "DOUBLE";
            case SQLITE, D1 -> integer ? "INTEGER" : "REAL";
        };
    }

    /** An ALTER that lost a race with another instance adding the same column. */
    static boolean isDuplicateColumn(Exception e) {
        String msg = String.valueOf(e.getMessage()).toLowerCase(Locale.ROOT);
        return msg.contains("duplicate column") || msg.contains("already exists") || msg.contains("1060");
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
