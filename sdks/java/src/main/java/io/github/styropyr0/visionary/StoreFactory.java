package io.github.styropyr0.visionary;

import java.net.URI;
import java.net.URLDecoder;
import java.nio.charset.StandardCharsets;
import java.sql.Driver;
import java.sql.DriverManager;
import java.sql.SQLException;
import java.util.List;
import java.util.Locale;
import java.util.Properties;

import javax.sql.DataSource;

import com.fasterxml.jackson.databind.ObjectMapper;

import org.springframework.util.ClassUtils;

/**
 * Picks the backend: {@code VISIONARY_DATABASE_URL} first, then the {@code D1_*}
 * variables, then the application's DataSource when
 * {@code VISIONARY_USE_APP_DATASOURCE=true}.
 */
final class StoreFactory {

    private StoreFactory() {
    }

    /** Returns null when nothing is configured; throws with a readable message when misconfigured. */
    static Store create(VisionaryConfig cfg, DataSource appDataSource, ObjectMapper json, ClassLoader loader) {
        String url = cfg.databaseUrl();
        if (!url.isEmpty()) {
            if (url.toLowerCase(Locale.ROOT).startsWith("d1:")) {
                return d1(cfg, json);
            }
            return jdbc(url, loader);
        }
        if (cfg.hasD1Credentials()) {
            return d1(cfg, json);
        }
        if (cfg.useAppDataSource()) {
            if (appDataSource == null) {
                throw new IllegalStateException(
                        "VISIONARY_USE_APP_DATASOURCE=true but the application has no single DataSource bean");
            }
            return JdbcStore.forDataSource(appDataSource, "application DataSource");
        }
        return null;
    }

    private static Store d1(VisionaryConfig cfg, ObjectMapper json) {
        if (!cfg.hasD1Credentials()) {
            throw new IllegalStateException("D1 needs D1_ACCOUNT_ID, D1_DATABASE_ID and D1_API_TOKEN");
        }
        return new D1Store(cfg.d1ApiBase(), cfg.d1AccountId(), cfg.d1DatabaseId(), cfg.d1ApiToken(), json);
    }

    private static Store jdbc(String rawUrl, ClassLoader loader) {
        JdbcTarget target = JdbcTarget.parse(rawUrl);
        Driver driver = driver(target, loader);
        return JdbcStore.forDriver(target.dialect, driver, target.url, target.props, target.describe());
    }

    private static Driver driver(JdbcTarget target, ClassLoader loader) {
        for (String name : target.driverClasses()) {
            if (ClassUtils.isPresent(name, loader)) {
                try {
                    Class<?> type = ClassUtils.forName(name, loader);
                    return (Driver) type.getDeclaredConstructor().newInstance();
                } catch (ReflectiveOperationException | LinkageError e) {
                    throw new IllegalStateException("cannot load JDBC driver " + name + ": " + e, e);
                }
            }
        }
        try {
            return DriverManager.getDriver(target.url);
        } catch (SQLException e) {
            throw new IllegalStateException("no JDBC driver for " + target.describe() + "; add " + target.driverHint()
                    + " to the application's dependencies");
        }
    }

    /** A JDBC URL plus credentials, converted from the URL forms SPEC.md allows. */
    record JdbcTarget(Dialect dialect, String url, Properties props) {

        static JdbcTarget parse(String raw) {
            String lower = raw.toLowerCase(Locale.ROOT);
            if (lower.startsWith("jdbc:")) {
                return new JdbcTarget(dialectOf(lower), raw, new Properties());
            }
            if (lower.startsWith("postgres://") || lower.startsWith("postgresql://")) {
                return fromNetworkUrl(raw, Dialect.POSTGRES, "jdbc:postgresql://");
            }
            if (lower.startsWith("mysql://") || lower.startsWith("mariadb://")) {
                return fromNetworkUrl(raw, Dialect.MYSQL,
                        lower.startsWith("mariadb") ? "jdbc:mariadb://" : "jdbc:mysql://");
            }
            if (lower.startsWith("sqlite://")) {
                String path = raw.substring("sqlite://".length());
                // sqlite:///C:/data/v.db is an absolute Windows path, not /C:/data/v.db.
                if (path.matches("/[A-Za-z]:[/\\\\].*")) {
                    path = path.substring(1);
                }
                return new JdbcTarget(Dialect.SQLITE, "jdbc:sqlite:" + path, new Properties());
            }
            throw new IllegalArgumentException("unsupported VISIONARY_DATABASE_URL scheme in " + redact(raw)
                    + " (use postgres://, mysql://, sqlite://, d1:// or a jdbc: URL)");
        }

        private static Dialect dialectOf(String jdbcUrl) {
            if (jdbcUrl.startsWith("jdbc:postgresql:")) {
                return Dialect.POSTGRES;
            }
            if (jdbcUrl.startsWith("jdbc:mysql:") || jdbcUrl.startsWith("jdbc:mariadb:")) {
                return Dialect.MYSQL;
            }
            if (jdbcUrl.startsWith("jdbc:sqlite:")) {
                return Dialect.SQLITE;
            }
            throw new IllegalArgumentException("unsupported JDBC URL " + redact(jdbcUrl)
                    + " (PostgreSQL, MySQL/MariaDB and SQLite are supported)");
        }

        private static JdbcTarget fromNetworkUrl(String raw, Dialect dialect, String jdbcPrefix) {
            URI uri = URI.create(raw);
            Properties props = new Properties();
            String userInfo = uri.getRawUserInfo();
            if (userInfo != null && !userInfo.isEmpty()) {
                int colon = userInfo.indexOf(':');
                props.setProperty("user", decode(colon < 0 ? userInfo : userInfo.substring(0, colon)));
                if (colon >= 0) {
                    props.setProperty("password", decode(userInfo.substring(colon + 1)));
                }
            }
            String host = uri.getHost() == null ? "" : uri.getHost();
            String port = uri.getPort() < 0 ? "" : ":" + uri.getPort();
            String path = uri.getRawPath() == null ? "" : uri.getRawPath();
            String query = uri.getRawQuery() == null ? "" : "?" + uri.getRawQuery();
            return new JdbcTarget(dialect, jdbcPrefix + host + port + path + query, props);
        }

        private static String decode(String s) {
            return URLDecoder.decode(s.replace("+", "%2B"), StandardCharsets.UTF_8);
        }

        private static String redact(String url) {
            return url.replaceAll("//([^:/@]*):[^@/]*@", "//$1:***@");
        }

        List<String> driverClasses() {
            return switch (dialect) {
                case POSTGRES -> List.of("org.postgresql.Driver");
                case MYSQL -> url.startsWith("jdbc:mariadb:")
                        ? List.of("org.mariadb.jdbc.Driver")
                        : List.of("com.mysql.cj.jdbc.Driver", "org.mariadb.jdbc.Driver");
                case SQLITE -> List.of("org.sqlite.JDBC");
                case D1 -> List.of();
            };
        }

        String driverHint() {
            return switch (dialect) {
                case POSTGRES -> "org.postgresql:postgresql";
                case MYSQL -> "com.mysql:mysql-connector-j (or org.mariadb.jdbc:mariadb-java-client)";
                case SQLITE -> "org.xerial:sqlite-jdbc";
                case D1 -> "nothing";
            };
        }

        String describe() {
            String u = redact(url);
            int q = u.indexOf('?');
            return q < 0 ? u : u.substring(0, q);
        }
    }
}
