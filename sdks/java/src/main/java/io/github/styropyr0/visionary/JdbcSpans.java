package io.github.styropyr0.visionary;

import java.lang.reflect.InvocationHandler;
import java.lang.reflect.InvocationTargetException;
import java.lang.reflect.Method;
import java.lang.reflect.Modifier;
import java.lang.reflect.Proxy;
import java.sql.Connection;
import java.sql.DatabaseMetaData;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.sql.Statement;
import java.util.ArrayList;
import java.util.List;
import java.util.Locale;

import javax.sql.DataSource;

import org.springframework.util.ClassUtils;

/**
 * A {@code db} span per JDBC statement, from {@link java.lang.reflect.Proxy}
 * wrappers around the application's DataSource, its connections, statements and
 * result sets. The proxies keep every interface of the object they wrap, and
 * {@code unwrap}/{@code isWrapperFor} reach the original, so pool-specific code
 * (Hikari metrics, Boot's {@code DataSourceUnwrapper}) still finds it.
 */
final class JdbcSpans {

    /** What {@link #common} returns for a method it leaves to the caller. */
    private static final Object NOT_HANDLED = new Object();

    /** The public interfaces of each wrapped class, so a proxy keeps all of them. */
    private static final ClassValue<Class<?>[]> INTERFACES = new ClassValue<>() {
        @Override
        protected Class<?>[] computeValue(Class<?> type) {
            List<Class<?>> out = new ArrayList<>();
            for (Class<?> i : ClassUtils.getAllInterfacesForClassAsSet(type)) {
                if (Modifier.isPublic(i.getModifiers())) {
                    out.add(i);
                }
            }
            return out.toArray(new Class<?>[0]);
        }
    };

    private JdbcSpans() {
    }

    /** The DataSource with statement spans; already wrapped ones are returned as they are. */
    static DataSource wrap(DataSource dataSource) {
        if (handler(dataSource, DataSourceHandler.class) != null) {
            return dataSource;
        }
        return (DataSource) proxy(dataSource, new DataSourceHandler(dataSource));
    }

    /** The original DataSource behind a wrapper, so Visionary's own writes are never traced. */
    static DataSource unwrap(DataSource dataSource) {
        DataSourceHandler h = handler(dataSource, DataSourceHandler.class);
        return h == null ? dataSource : h.target;
    }

    private static Object proxy(Object target, InvocationHandler handler) {
        ClassLoader loader = target.getClass().getClassLoader();
        return Proxy.newProxyInstance(loader == null ? JdbcSpans.class.getClassLoader() : loader,
                INTERFACES.get(target.getClass()), handler);
    }

    private static <H> H handler(Object o, Class<H> type) {
        if (o != null && Proxy.isProxyClass(o.getClass())) {
            InvocationHandler h = Proxy.getInvocationHandler(o);
            if (type.isInstance(h)) {
                return type.cast(h);
            }
        }
        return null;
    }

    /** Calls the method on the target, rethrowing what the target threw. */
    private static Object call(Object target, Method method, Object[] args) throws Throwable {
        try {
            return method.invoke(target, args);
        } catch (InvocationTargetException e) {
            throw e.getCause();
        }
    }

    /** {@code equals}, {@code hashCode}, {@code toString}, {@code unwrap}, {@code isWrapperFor}; else NOT_HANDLED. */
    private static Object common(Object proxy, Object target, Method method, Object[] args) {
        switch (method.getName()) {
            case "equals":
                return args != null && args.length == 1 ? proxy == args[0] : NOT_HANDLED;
            case "hashCode":
                return args == null || args.length == 0 ? System.identityHashCode(proxy) : NOT_HANDLED;
            case "toString":
                return args == null || args.length == 0 ? target.toString() : NOT_HANDLED;
            case "unwrap":
                if (args != null && args.length == 1 && args[0] instanceof Class<?> c && c.isInstance(target)) {
                    return target;
                }
                return NOT_HANDLED;
            case "isWrapperFor":
                if (args != null && args.length == 1 && args[0] instanceof Class<?> c && c.isInstance(target)) {
                    return true;
                }
                return NOT_HANDLED;
            default:
                return NOT_HANDLED;
        }
    }

    private static final class DataSourceHandler implements InvocationHandler {
        final DataSource target;
        /** {@code system/database}, e.g. {@code postgresql/voygram}; read from the first connection. */
        private volatile String dbTarget;

        DataSourceHandler(DataSource target) {
            this.target = target;
        }

        @Override
        public Object invoke(Object proxy, Method method, Object[] args) throws Throwable {
            Object r = common(proxy, target, method, args);
            if (r != NOT_HANDLED) {
                return r;
            }
            r = call(target, method, args);
            if (r instanceof Connection c && method.getName().equals("getConnection")
                    && handler(c, ConnectionHandler.class) == null) {
                // A DataSource wrapping another wrapped one gets an already traced connection.
                return proxy(c, new ConnectionHandler(c, this));
            }
            return r;
        }

        String dbTarget(Connection c) {
            String t = dbTarget;
            if (t == null) {
                t = describe(c);
                dbTarget = t;
            }
            return t;
        }

        private static String describe(Connection c) {
            String system = "db";
            String database = null;
            try {
                DatabaseMetaData md = c.getMetaData();
                system = system(md.getDatabaseProductName());
                database = c.getCatalog();
                if (database == null || database.isEmpty()) {
                    database = lastSegment(md.getURL());
                }
            } catch (SQLException | RuntimeException ignored) {
                // the system alone is still useful
            }
            return database == null || database.isEmpty() ? system : system + "/" + database;
        }

        private static String system(String product) {
            String p = product == null ? "" : product.toLowerCase(Locale.ROOT);
            if (p.contains("postgres")) {
                return "postgresql";
            }
            if (p.contains("sql server")) {
                return "mssql";
            }
            for (String known : new String[] {"mariadb", "mysql", "sqlite", "oracle", "h2", "hsql", "db2", "derby"}) {
                if (p.contains(known)) {
                    return known;
                }
            }
            return p.isEmpty() ? "db" : p.replaceAll("\\s+", "_");
        }

        /** {@code jdbc:sqlite:C:/data/app.db} → {@code app.db}. */
        private static String lastSegment(String url) {
            if (url == null) {
                return null;
            }
            int q = url.indexOf('?');
            String u = q < 0 ? url : url.substring(0, q);
            int cut = Math.max(u.lastIndexOf('/'), Math.max(u.lastIndexOf('\\'), u.lastIndexOf(':')));
            return u.substring(cut + 1);
        }
    }

    private static final class ConnectionHandler implements InvocationHandler {
        private final Connection target;
        private final DataSourceHandler source;

        ConnectionHandler(Connection target, DataSourceHandler source) {
            this.target = target;
            this.source = source;
        }

        @Override
        public Object invoke(Object proxy, Method method, Object[] args) throws Throwable {
            Object r = common(proxy, target, method, args);
            if (r != NOT_HANDLED) {
                return r;
            }
            String name = method.getName();
            if ((name.equals("commit") || name.equals("rollback")) && Telemetry.active() != null) {
                return timed(name.toUpperCase(Locale.ROOT), method, args);
            }
            r = call(target, method, args);
            if (r instanceof Statement st) {
                // prepareStatement(sql, ...) and prepareCall(sql, ...); createStatement has no SQL yet.
                String sql = args != null && args.length > 0 && args[0] instanceof String s ? s : null;
                return proxy(st, new StatementHandler(st, sql, (Connection) proxy, this));
            }
            return r;
        }

        private Object timed(String name, Method method, Object[] args) throws Throwable {
            VisionarySpan span = Telemetry.start("db", name);
            if (span == null) {
                return call(target, method, args);
            }
            span.target = source.dbTarget(target);
            try {
                return call(target, method, args);
            } catch (Throwable t) {
                span.error(t);
                throw t;
            } finally {
                span.close();
            }
        }

        VisionarySpan start(String sql) {
            VisionarySpan span = Telemetry.start("db", null);
            if (span != null) {
                span.sql = sql == null ? "" : sql;
                span.target = source.dbTarget(target);
            }
            return span;
        }
    }

    private static final class StatementHandler implements InvocationHandler {
        private final Statement target;
        /** The prepared statement's SQL; null for a plain Statement. */
        private final String sql;
        private final Connection connection;
        private final ConnectionHandler owner;
        /** A plain Statement's first batched statement, named for the whole batch. */
        private String batchSql;
        /** The last statement's span, which a later {@code getResultSet()} counts rows into. */
        private VisionarySpan last;

        StatementHandler(Statement target, String sql, Connection connection, ConnectionHandler owner) {
            this.target = target;
            this.sql = sql;
            this.connection = connection;
            this.owner = owner;
        }

        @Override
        public Object invoke(Object proxy, Method method, Object[] args) throws Throwable {
            Object r = common(proxy, target, method, args);
            if (r != NOT_HANDLED) {
                return r;
            }
            String name = method.getName();
            if (name.startsWith("execute")) {
                return execute(name, method, args);
            }
            if (name.equals("addBatch") && args != null && args.length == 1 && args[0] instanceof String s
                    && batchSql == null) {
                batchSql = s;
            } else if (name.equals("getConnection")) {
                return connection;
            }
            r = call(target, method, args);
            if (name.equals("getResultSet") && r instanceof ResultSet rs && last != null) {
                return proxy(rs, new ResultSetHandler(rs, last));
            }
            return r;
        }

        private Object execute(String name, Method method, Object[] args) throws Throwable {
            String text = args != null && args.length > 0 && args[0] instanceof String s ? s
                    : name.endsWith("Batch") && sql == null ? batchSql : sql;
            VisionarySpan span = owner.start(text);
            if (span == null) {
                return call(target, method, args);
            }
            last = span;
            Object r;
            try {
                r = call(target, method, args);
            } catch (Throwable t) {
                span.error(t);
                span.close();
                throw t;
            }
            if (r instanceof Integer n && n >= 0) {
                span.rowCount = n;
            } else if (r instanceof Long n && n >= 0) {
                span.rowCount = n;
            } else if (r instanceof int[] counts) {
                span.rowCount = affected(counts.length, i -> counts[i]);
            } else if (r instanceof long[] counts) {
                span.rowCount = affected(counts.length, i -> counts[i]);
            }
            if (name.endsWith("Batch")) {
                batchSql = null;
            }
            if (r instanceof ResultSet rs) {
                span.rowCount = 0;
                span.close();
                return proxy(rs, new ResultSetHandler(rs, span));
            }
            span.close();
            return r;
        }

        /** Sum of the batch's update counts; unknown when the driver reported none. */
        private static long affected(int n, java.util.function.IntToLongFunction count) {
            long total = 0;
            boolean known = false;
            for (int i = 0; i < n; i++) {
                long c = count.applyAsLong(i);
                if (c >= 0) {
                    total += c;
                    known = true;
                }
            }
            return known ? total : -1;
        }
    }

    /** Counts the rows read into the statement's span. */
    private static final class ResultSetHandler implements InvocationHandler {
        private final ResultSet target;
        private final VisionarySpan span;

        ResultSetHandler(ResultSet target, VisionarySpan span) {
            this.target = target;
            this.span = span;
        }

        @Override
        public Object invoke(Object proxy, Method method, Object[] args) throws Throwable {
            if (method.getName().equals("next") && method.getParameterCount() == 0) {
                Object r = call(target, method, args);
                if (Boolean.TRUE.equals(r)) {
                    span.addRow();
                }
                return r;
            }
            Object r = common(proxy, target, method, args);
            return r != NOT_HANDLED ? r : call(target, method, args);
        }
    }
}
