package io.github.styropyr0.visionary;

import java.sql.Connection;
import java.sql.Driver;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.sql.Statement;
import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.Properties;

import javax.sql.DataSource;

/**
 * PostgreSQL, MySQL/MariaDB or SQLite over JDBC with typed parameters.
 *
 * <p>With its own URL it keeps a few idle connections and drops one after an
 * error, so it recovers from a database restart. With the application's
 * DataSource it borrows a pooled connection per operation. Rows go in as
 * multi-row INSERTs built here, since SQLite's driver has no batch rewriting
 */
final class JdbcStore implements Store {

    private static final int MYSQL_DUPLICATE_KEY_NAME = 1061;
    /** Below every backend's bound-parameter limit (SQLite's is 32766) */
    static final int MAX_PARAMS_PER_STATEMENT = 30_000;
    static final int BATCH_SIZE = 500;
    static final int PARALLEL_WRITES = 4;

    /** Null until the first connection when using the application's DataSource. */
    private volatile Dialect dialect;
    private final String description;
    private final ConnectionSource source;
    private final ArrayDeque<Connection> idle = new ArrayDeque<>();
    private final Object sqliteTurn = new Object();
    private boolean closed;

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
    public void createSchema(Table table) throws SQLException {
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
                if (table.traceIndex) {
                    try {
                        st.execute(dialect.traceIndex(table.name));
                    } catch (SQLException e) {
                        if (dialect != Dialect.MYSQL || e.getErrorCode() != MYSQL_DUPLICATE_KEY_NAME) {
                            throw e;
                        }
                    }
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
    public void insert(Map<Table, List<Object[]>> rows) throws SQLException {
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
        int per = rowsPerStatement(table);
        PreparedStatement ps = null;
        int prepared = 0;
        try {
            for (int from = 0; from < rows.size(); from += per) {
                int n = Math.min(per, rows.size() - from);
                if (n != prepared) {
                    if (ps != null) {
                        ps.close();
                    }
                    ps = conn.prepareStatement(table.insertRowsSql(n));
                    prepared = n;
                }
                int index = 1;
                for (Object[] row : rows.subList(from, from + n)) {
                    for (int i = 0; i < row.length; i++) {
                        bind(ps, index++, row[i], table.types.get(i));
                    }
                }
                ps.executeUpdate();
            }
        } finally {
            if (ps != null) {
                ps.close();
            }
        }
    }

    static int rowsPerStatement(Table table) {
        return Math.max(1, MAX_PARAMS_PER_STATEMENT / table.columns.size());
    }

    @Override
    public int batchSize() {
        return BATCH_SIZE;
    }

    /** PostgreSQL and MySQL insert in parallel, SQLite takes one writer at a time */
    @Override
    public int parallelWrites() {
        Dialect d = dialect;
        return d == Dialect.POSTGRES || d == Dialect.MYSQL ? PARALLEL_WRITES : 1;
    }

    @Override
    public List<String[]> select(String sql) throws SQLException {
        try {
            return run(conn -> {
                conn.setAutoCommit(true);
                try (Statement st = conn.createStatement(); ResultSet rs = st.executeQuery(sql)) {
                    int n = rs.getMetaData().getColumnCount();
                    List<String[]> out = new ArrayList<>();
                    while (rs.next()) {
                        String[] row = new String[n];
                        for (int i = 0; i < n; i++) {
                            row[i] = rs.getString(i + 1);
                        }
                        out.add(row);
                    }
                    return out;
                } catch (SQLException e) {
                    // A failed query (a missing table) is no reason to drop the shared connection.
                    throw new QueryFailed(e);
                }
            });
        } catch (QueryFailed e) {
            throw e.sqlError;
        }
    }

    /** Carries a query's own error past {@link #run}, which reconnects on SQL errors. */
    private static final class QueryFailed extends RuntimeException {
        final SQLException sqlError;

        QueryFailed(SQLException cause) {
            super(cause.getMessage(), cause, false, false);
            this.sqlError = cause;
        }
    }

    @Override
    public long purgeChunk(Table table, String cutoff) throws SQLException {
        return run(conn -> {
            conn.setAutoCommit(true);
            try (PreparedStatement ps = conn.prepareStatement(dialect.purgeSql(table.name))) {
                ps.setString(1, cutoff);
                return (long) ps.executeUpdate();
            }
        });
    }

    @Override
    public void close() {
        synchronized (idle) {
            closed = true;
            for (Connection c : idle) {
                closeQuietly(c);
            }
            idle.clear();
        }
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
        if (source.reuse && dialect == Dialect.SQLITE) {
            synchronized (sqliteTurn) {
                return runOn(work);
            }
        }
        return runOn(work);
    }

    private <T> T runOn(SqlWork<T> work) throws SQLException {
        Connection conn = source.reuse ? borrow() : source.open();
        boolean broken = false;
        try {
            if (dialect == null) {
                dialect = detect(conn.getMetaData().getDatabaseProductName());
            }
            return work.apply(conn);
        } catch (SQLException e) {
            broken = true;
            throw e;
        } finally {
            if (!source.reuse) {
                conn.close();
            } else if (broken) {
                closeQuietly(conn);
            } else {
                giveBack(conn);
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

    private Connection borrow() throws SQLException {
        while (true) {
            Connection c;
            synchronized (idle) {
                c = idle.poll();
            }
            if (c == null) {
                return source.open();
            }
            try {
                if (!c.isClosed()) {
                    return c;
                }
            } catch (SQLException ignored) {
            }
        }
    }

    private void giveBack(Connection c) {
        synchronized (idle) {
            if (!closed && idle.size() < PARALLEL_WRITES) {
                idle.push(c);
                return;
            }
        }
        closeQuietly(c);
    }

    private static void closeQuietly(Connection c) {
        try {
            c.close();
        } catch (SQLException ignored) {
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
