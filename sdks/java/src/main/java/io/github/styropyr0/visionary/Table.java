package io.github.styropyr0.visionary;

import java.sql.Types;
import java.util.ArrayList;
import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/**
 * Column order and value types of an insert. Rows are {@code Object[]} in
 * column order holding {@link String}, {@link Long}, {@link Double} or null.
 */
final class Table {

    enum Type {
        TEXT(Types.VARCHAR), INT(Types.BIGINT), REAL(Types.DOUBLE);

        final int sqlType;

        Type(int sqlType) {
            this.sqlType = sqlType;
        }
    }

    static final Table API_LOGS = builder("api_logs", "requests")
            .text("request_id", "created_at", "method", "path", "route", "query")
            .integer("status").real("duration_ms")
            .text("ip", "user_agent", "user_id", "user_handle", "user_role", "request_headers",
                    "request_content_type", "request_body")
            .integer("request_body_truncated", "request_size")
            .text("response_content_type", "response_body")
            .integer("response_body_truncated", "response_size")
            .text("error_code", "error_key")
            .build();

    static final Table SYSTEM_METRICS = builder("system_metrics", "system")
            .text("created_at", "instance")
            .real("interval_s", "host_cpu_percent", "load_1", "load_5", "load_15",
                    "host_mem_used_mb", "host_mem_total_mb", "host_mem_percent",
                    "disk_used_percent", "disk_free_gb", "net_rx_kbps", "net_tx_kbps",
                    "proc_cpu_percent", "proc_rss_mb")
            .integer("proc_threads", "proc_open_fds", "goroutines")
            .real("heap_alloc_mb", "heap_inuse_mb")
            .integer("heap_objects", "gc_count")
            .real("gc_pause_ms", "gc_cpu_percent")
            .integer("db_total_conns", "db_idle_conns", "db_acquired_conns", "db_max_conns",
                    "db_acquires", "db_empty_acquires")
            .real("db_acquire_wait_ms")
            .integer("http_requests", "http_errors")
            .real("http_rps")
            .integer("http_in_flight", "uptime_s")
            .build();

    final String name;
    /** Base name of the schema file, {@code schema/<dialect>/<schemaName>.sql}. */
    final String schemaName;
    final List<String> columns;
    final List<Type> types;
    private final Map<String, Integer> index;

    private Table(String name, String schemaName, Map<String, Type> cols) {
        this.name = name;
        this.schemaName = schemaName;
        this.columns = List.copyOf(cols.keySet());
        this.types = List.copyOf(cols.values());
        Map<String, Integer> idx = new LinkedHashMap<>();
        for (int i = 0; i < columns.size(); i++) {
            idx.put(columns.get(i), i);
        }
        this.index = Collections.unmodifiableMap(idx);
    }

    int indexOf(String column) {
        Integer i = index.get(column);
        if (i == null) {
            throw new IllegalArgumentException("unknown column " + name + "." + column);
        }
        return i;
    }

    String insertSql(String placeholder) {
        List<String> ph = new ArrayList<>(Collections.nCopies(columns.size(), placeholder));
        return "INSERT INTO " + name + " (" + String.join(", ", columns) + ") VALUES ("
                + String.join(", ", ph) + ")";
    }

    private static Builder builder(String name, String schemaName) {
        return new Builder(name, schemaName);
    }

    private static final class Builder {
        private final String name;
        private final String schemaName;
        private final Map<String, Type> cols = new LinkedHashMap<>();

        Builder(String name, String schemaName) {
            this.name = name;
            this.schemaName = schemaName;
        }

        Builder text(String... names) {
            return add(Type.TEXT, names);
        }

        Builder integer(String... names) {
            return add(Type.INT, names);
        }

        Builder real(String... names) {
            return add(Type.REAL, names);
        }

        private Builder add(Type type, String... names) {
            for (String n : names) {
                cols.put(n, type);
            }
            return this;
        }

        Table build() {
            return new Table(name, schemaName, cols);
        }
    }
}
