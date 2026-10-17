package io.github.styropyr0.visionary;

import java.sql.SQLException;

import javax.sql.DataSource;

import com.zaxxer.hikari.HikariDataSource;
import com.zaxxer.hikari.HikariPoolMXBean;

/**
 * Pool stats of the application's HikariCP pool. Only loaded when HikariCP is
 * on the classpath. Hikari exposes no cumulative acquire counters through its
 * MXBean, so the db_acquires/db_empty_acquires/db_acquire_wait_ms columns stay NULL.
 */
final class HikariPoolStats implements SystemSampler.PoolStats {

    private final HikariDataSource hikari;

    private HikariPoolStats(HikariDataSource hikari) {
        this.hikari = hikari;
    }

    /** Null unless {@code dataSource} is, or wraps, a HikariDataSource. */
    static SystemSampler.PoolStats of(DataSource dataSource) {
        if (dataSource instanceof HikariDataSource h) {
            return new HikariPoolStats(h);
        }
        try {
            if (dataSource != null && dataSource.isWrapperFor(HikariDataSource.class)) {
                return new HikariPoolStats(dataSource.unwrap(HikariDataSource.class));
            }
        } catch (SQLException ignored) {
            // not a Hikari pool
        }
        return null;
    }

    @Override
    public long[] read() {
        HikariPoolMXBean bean = hikari.getHikariPoolMXBean();
        if (bean == null) {
            // The pool starts lazily on first use.
            return null;
        }
        return new long[] {
            bean.getTotalConnections(), bean.getIdleConnections(), bean.getActiveConnections(),
            hikari.getMaximumPoolSize(),
        };
    }
}
