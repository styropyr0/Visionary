package io.github.styropyr0.visionary;

import java.sql.Connection;
import java.sql.Driver;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.sql.Statement;
import java.util.ArrayList;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.Properties;

import javax.sql.DataSource;

/**
 * PostgreSQL, MySQL/MariaDB or SQLite over JDBC with typed parameters.
 *
 * <p>With its own URL the store keeps one connection (shared by the background
 * threads under a lock) and reopens it after an error, so a database restart
 * heals by itself. With the application's DataSource it borrows a pooled
 * connection per operation.
 */
final class JdbcStore implements Store {

    private static final int MYSQL_DUPLICATE_KEY_NAME = 1061;

    /** Null until the first connection when using the application's DataSource. */
    private Dialect dialect;
    private final String description;
    private final ConnectionSource source;
    private Connection cached;

    private JdbcStore(Dialect dialect, String description, ConnectionSource source) {
        this.dialect = dialect;
        this.description = description;
        this.source = source;
    }

    static JdbcStore forDriver(Dialect dialect, Driver driver, String url, Properties props, String description) {
        return new JdbcStore(dialect, description, new ConnectionSource(true) {
            @Override
            Connection open() throws SQLException {
                Connection c = driver.connect(url, props);
                if (c == null) {
                    throw new SQLException("driver " + driver.getClass().getName() + " does not accept the URL");
                }
                return c;
            }
        });
    }

    static JdbcStore forDataSource(DataSource dataSource, String description) {
        return new JdbcStore(null, description, new ConnectionSource(false) {
            @Override
            Connection open() throws SQLException {
                return dataSource.getConnection();
            }
        });
    }

    @Override
    public String describe() {
        return dialect == null ? description : description + " (" + dialect.name().toLowerCase(Locale.ROOT) + ")";
    }

    @Override
    public synchronized void createSchema(Table table) throws SQLException {
        run(conn -> {
            conn.setAutoCommit(true);
            try (Statement st = conn.createStatement()) {
                for (String sql : dialect.schema(table.schemaName)) {
                    try {
                        st.execute(sql);
                    } catch (SQLException e) {
                        // MySQL has no CREATE INDEX IF NOT EXISTS; an existing index is fine.
                        if (dialect != Dialect.MYSQL || e.getErrorCode() != MYSQL_DUPLICATE_KEY_NAME) {
                            throw e;
                        }
                    }
                }
                if (!table.addedColumns.isEmpty()) {
                    addMissingColumns(st, table);
                }
            }
            return null;
        });
    }

    /** Upgrades a table created by an older release in place. */
    private void addMissingColumns(Statement st, Table table) throws SQLException {
        List<String> existing = new ArrayList<>();
        try (ResultSet rs = st.executeQuery(dialect.columnsQuery(table.name))) {
            while (rs.next()) {
                existing.add(rs.getString(1));
            }
        }
        for (String alter : dialect.missingColumns(table, existing)) {
            try {
                st.execute(alter);
            } catch (SQLException e) {
                if (!Dialect.isDuplicateColumn(e)) {
                    throw e;
                }
            }
        }
    }

    @Override
    public synchronized void insert(Map<Table, List<Object[]>> rows) throws SQLException {
        run(conn -> {
            conn.setAutoCommit(false);
            try {
                for (Map.Entry<Table, List<Object[]>> e : rows.entrySet()) {
                    if (!e.getValue().isEmpty()) {
                        insert(conn, e.getKey(), e.getValue());
                    }
                }
                conn.commit();
            } catch (SQLException | RuntimeException e) {
                rollbackQuietly(conn);
                throw e;
            } finally {
                resetAutoCommit(conn);
            }
            return null;
        });
    }

    private void insert(Connection conn, Table table, List<Object[]> rows) throws SQLException {
        try (PreparedStatement ps = conn.prepareStatement(table.insertSql("?"))) {
            for (Object[] row : rows) {
                for (int i = 0; i < row.length; i++) {
                    bind(ps, i + 1, row[i], table.types.get(i));
                }
                ps.addBatch();
            }
            ps.executeBatch();
        }
    }

    @Override
    public synchronized long purgeChunk(Table table, String cutoff) throws SQLException {
        return run(conn -> {
            conn.setAutoCommit(true);
            try (PreparedStatement ps = conn.prepareStatement(dialect.purgeSql(table.name))) {
                ps.setString(1, cutoff);
                return (long) ps.executeUpdate();
            }
        });
    }

    @Override
    public synchronized void close() {
        closeCached();
    }

    private void bind(PreparedStatement ps, int index, Object v, Table.Type type) throws SQLException {
        if (v == null) {
            ps.setNull(index, type.sqlType);
        } else if (v instanceof Long l) {
            ps.setLong(index, l);
        } else if (v instanceof Double d) {
            ps.setDouble(index, d);
        } else {
            String s = v.toString();
            // PostgreSQL text cannot hold NUL; one stray byte must not fail the batch.
            ps.setString(index, dialect == Dialect.POSTGRES ? s.replace('\u0000', '�') : s);
        }
    }

    private <T> T run(SqlWork<T> work) throws SQLException {
        Connection conn = source.reuse ? cachedConnection() : source.open();
        try {
            if (dialect == null) {
                dialect = detect(conn.getMetaData().getDatabaseProductName());
            }
            return work.apply(conn);
        } catch (SQLException e) {
            if (source.reuse) {
                closeCached();
            }
            throw e;
        } finally {
            if (!source.reuse) {
                conn.close();
            }
        }
    }

    private static Dialect detect(String product) throws SQLException {
        String p = product == null ? "" : product.toLowerCase(Locale.ROOT);
        if (p.contains("postgres")) {
            return Dialect.POSTGRES;
        }
        if (p.contains("mysql") || p.contains("mariadb")) {
            return Dialect.MYSQL;
        }
        if (p.contains("sqlite")) {
            return Dialect.SQLITE;
        }
        throw new SQLException("visionary supports PostgreSQL, MySQL/MariaDB and SQLite, not " + product);
    }

    private Connection cachedConnection() throws SQLException {
        if (cached != null) {
            try {
                if (!cached.isClosed()) {
                    return cached;
                }
            } catch (SQLException ignored) {
                // fall through and reconnect
            }
        }
        cached = source.open();
        return cached;
    }

    private void closeCached() {
        if (cached != null) {
            try {
                cached.close();
            } catch (SQLException ignored) {
                // already broken
            }
            cached = null;
        }
    }

    private static void rollbackQuietly(Connection conn) {
        try {
            conn.rollback();
        } catch (SQLException ignored) {
            // the original error is the one worth reporting
        }
    }

    private static void resetAutoCommit(Connection conn) {
        try {
            conn.setAutoCommit(true);
        } catch (SQLException ignored) {
            // the connection will be discarded on the next error
        }
    }

    @FunctionalInterface
    private interface SqlWork<T> {
        T apply(Connection conn) throws SQLException;
    }

    private abstract static class ConnectionSource {
        final boolean reuse;

        ConnectionSource(boolean reuse) {
            this.reuse = reuse;
        }

        abstract Connection open() throws SQLException;
    }
}
