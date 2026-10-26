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
            // Added after the first release; older tables gain them at startup (sdks/go/migrate.go).
            .added()
            .text("service", "version")
            .integer("db_count").real("db_ms")
            .integer("http_count").real("http_ms")
            .text("login_account")
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
            // Added after the first release; older tables gain them at startup (sdks/go/migrate.go).
            .added()
            .integer("cpu_count")
            .real("cpu_user_percent", "cpu_system_percent", "cpu_iowait_percent", "cpu_steal_percent",
                    "host_mem_available_mb", "host_mem_cached_mb", "swap_used_mb", "swap_total_mb",
                    "disk_total_gb", "disk_inodes_percent", "disk_read_kbps", "disk_write_kbps",
                    "disk_read_iops", "disk_write_iops", "disk_busy_percent", "disk_await_ms",
                    "net_rx_pps", "net_tx_pps")
            .integer("net_errors", "net_drops", "tcp_established", "tcp_retrans", "host_procs",
                    "procs_running", "procs_blocked")
            .real("ctx_switches_ps", "proc_read_kbps", "proc_write_kbps")
            .integer("proc_max_fds")
            .text("service", "version", "sdk", "runtime")
            .real("container_cpu_cores", "container_cpu_limit", "container_cpu_throttled_percent",
                    "container_mem_used_mb", "container_mem_limit_mb")
            .integer("container_oom_kills")
            .build();

    // The three telemetry tables share one schema file, telemetry.sql.
    static final Table SPANS = builder("spans", "telemetry")
            .text("created_at", "request_id", "service", "instance", "kind", "name", "target", "detail")
            .real("offset_ms", "duration_ms")
            .text("status", "error")
            .integer("row_count")
            .build();

    static final Table ERRORS = builder("errors", "telemetry")
            .text("created_at", "service", "instance", "version", "request_id", "method", "route", "path",
                    "user_id", "type", "message", "stack", "fingerprint")
            .integer("handled")
            .text("runtime")
            .build();

    static final Table RELEASES = builder("releases", "telemetry")
            .text("created_at", "service", "instance", "version", "sdk", "sdk_version", "runtime")
            .build();

    /** Created by the Visionary server, never by the SDK; written only while profiling. */
    static final Table PROFILES = builder("profiles", null)
            .text("created_at", "service", "instance", "version", "sdk")
            .real("duration_ms")
            .integer("samples")
            .text("routes", "folded")
            .build();

    final String name;
    /** Base name of the schema file, {@code schema/<dialect>/<schemaName>.sql}. */
    final String schemaName;
    final List<String> columns;
    final List<Type> types;
    /** Columns added after the first release, in insert order. */
    final List<String> addedColumns;
    private final Map<String, Integer> index;

    private Table(String name, String schemaName, Map<String, Type> cols, List<String> added) {
        this.name = name;
        this.schemaName = schemaName;
        this.columns = List.copyOf(cols.keySet());
        this.types = List.copyOf(cols.values());
        this.addedColumns = List.copyOf(added);
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

    Type typeOf(String column) {
        return types.get(indexOf(column));
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
        private final List<String> added = new ArrayList<>();
        private boolean adding;

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

        /** Marks the columns that follow as added to an existing table. */
        Builder added() {
            adding = true;
            return this;
        }

        private Builder add(Type type, String... names) {
            for (String n : names) {
                cols.put(n, type);
                if (adding) {
                    added.add(n);
                }
            }
            return this;
        }

        Table build() {
            return new Table(name, schemaName, cols, added);
        }
    }
}
