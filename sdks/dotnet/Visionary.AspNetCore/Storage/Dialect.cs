using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Reflection;
using System.Text;

namespace Styropyr0.Visionary;

/// <summary>The SQL flavour of the database the tables live in. Cloudflare D1 speaks SQLite.</summary>
public enum VisionaryDialect
{
    /// <summary>SQLite, and Cloudflare D1.</summary>
    Sqlite,
    /// <summary>PostgreSQL.</summary>
    Postgres,
    /// <summary>MySQL 8+ and MariaDB.</summary>
    MySql,
}

/// <summary>A column added to a table after its first release (sdks/go/migrate.go).</summary>
internal readonly record struct AddedColumn(string Name, string Kind);

/// <summary>Table definitions, migrations and the dialect-specific SQL the writers need.</summary>
internal static class Schema
{
    public static readonly string[] RequestColumns =
    {
        "request_id", "created_at", "method", "path", "route", "query", "status", "duration_ms", "ip", "user_agent",
        "user_id", "user_handle", "user_role", "request_headers", "request_content_type", "request_body",
        "request_body_truncated", "request_size", "response_content_type", "response_body",
        "response_body_truncated", "response_size", "error_code", "error_key",
        "service", "version", "db_count", "db_ms", "http_count", "http_ms",
    };

    /// <summary>AddedRequestColumns in sdks/go/migrate.go.</summary>
    public static readonly AddedColumn[] AddedRequestColumns =
    {
        new("service", "text"), new("version", "text"),
        new("db_count", "int"), new("db_ms", "real"),
        new("http_count", "int"), new("http_ms", "real"),
    };

    private static readonly string[] SystemBaseColumns =
    {
        "created_at", "instance", "interval_s",
        "host_cpu_percent", "load_1", "load_5", "load_15",
        "host_mem_used_mb", "host_mem_total_mb", "host_mem_percent",
        "disk_used_percent", "disk_free_gb", "net_rx_kbps", "net_tx_kbps",
        "proc_cpu_percent", "proc_rss_mb", "proc_threads", "proc_open_fds",
        "goroutines", "heap_alloc_mb", "heap_inuse_mb", "heap_objects",
        "gc_count", "gc_pause_ms", "gc_cpu_percent",
        "db_total_conns", "db_idle_conns", "db_acquired_conns", "db_max_conns",
        "db_acquires", "db_empty_acquires", "db_acquire_wait_ms",
        "http_requests", "http_errors", "http_rps", "http_in_flight",
        "uptime_s",
    };

    /// <summary>AddedSystemColumns in sdks/go/migrate.go: the canonical list and insert order.</summary>
    public static readonly AddedColumn[] AddedSystemColumns =
    {
        new("cpu_count", "int"),
        new("cpu_user_percent", "real"),
        new("cpu_system_percent", "real"),
        new("cpu_iowait_percent", "real"),
        new("cpu_steal_percent", "real"),
        new("host_mem_available_mb", "real"),
        new("host_mem_cached_mb", "real"),
        new("swap_used_mb", "real"),
        new("swap_total_mb", "real"),
        new("disk_total_gb", "real"),
        new("disk_inodes_percent", "real"),
        new("disk_read_kbps", "real"),
        new("disk_write_kbps", "real"),
        new("disk_read_iops", "real"),
        new("disk_write_iops", "real"),
        new("disk_busy_percent", "real"),
        new("disk_await_ms", "real"),
        new("net_rx_pps", "real"),
        new("net_tx_pps", "real"),
        new("net_errors", "int"),
        new("net_drops", "int"),
        new("tcp_established", "int"),
        new("tcp_retrans", "int"),
        new("host_procs", "int"),
        new("procs_running", "int"),
        new("procs_blocked", "int"),
        new("ctx_switches_ps", "real"),
        new("proc_read_kbps", "real"),
        new("proc_write_kbps", "real"),
        new("proc_max_fds", "int"),
        new("service", "text"),
        new("version", "text"),
        new("sdk", "text"),
        new("runtime", "text"),
        new("container_cpu_cores", "real"),
        new("container_cpu_limit", "real"),
        new("container_cpu_throttled_percent", "real"),
        new("container_mem_used_mb", "real"),
        new("container_mem_limit_mb", "real"),
        new("container_oom_kills", "int"),
    };

    public static readonly string[] SystemColumns = SystemBaseColumns.Concat(AddedSystemColumns.Select(c => c.Name)).ToArray();

    public static readonly string[] SpanColumns =
    {
        "created_at", "request_id", "service", "instance", "kind", "name", "target", "detail",
        "offset_ms", "duration_ms", "status", "error", "row_count",
    };

    public static readonly string[] ErrorColumns =
    {
        "created_at", "service", "instance", "version", "request_id", "method", "route", "path", "user_id",
        "type", "message", "stack", "fingerprint", "handled", "runtime",
    };

    /// <summary>The profiles table of SPEC.md "CPU profiles"; the Visionary server creates it.</summary>
    public static readonly string[] ProfileColumns =
    {
        "created_at", "service", "instance", "version", "sdk", "duration_ms", "samples", "routes", "folded",
    };

    public static readonly string[] ReleaseColumns =
    {
        "created_at", "service", "instance", "version", "sdk", "sdk_version", "runtime",
    };

    /// <summary>The CREATE statements of sdks/go/schema/&lt;dialect&gt;/&lt;name&gt;.sql.</summary>
    public static string Script(VisionaryDialect d, string name)
    {
        var folder = d switch
        {
            VisionaryDialect.Postgres => "postgres",
            VisionaryDialect.MySql => "mysql",
            _ => "sqlite",
        };
        var asm = typeof(Schema).Assembly;
        // RecursiveDir in the logical name carries the OS path separator of the build machine.
        var resource = asm.GetManifestResourceNames()
            .FirstOrDefault(n => n.Replace('\\', '/') == $"Visionary.Schema.{folder}/{name}.sql")
            ?? throw new InvalidOperationException($"visionary: missing schema {name} for {folder}");
        using var stream = asm.GetManifestResourceStream(resource)!;
        using var reader = new StreamReader(stream, Encoding.UTF8);
        return reader.ReadToEnd();
    }

    /// <summary>Splits a schema script into statements.</summary>
    public static List<string> Statements(string script) =>
        script.Split(';').Select(s => s.Trim()).Where(s => s.Length > 0).ToList();

    public static string SqlType(VisionaryDialect d, string kind) => (kind, d) switch
    {
        ("text", VisionaryDialect.MySql) => "VARCHAR(255)",
        ("text", _) => "TEXT",
        ("int", VisionaryDialect.Sqlite) => "INTEGER",
        ("int", _) => "BIGINT",
        (_, VisionaryDialect.Postgres) => "DOUBLE PRECISION",
        (_, VisionaryDialect.MySql) => "DOUBLE",
        _ => "REAL",
    };

    /// <summary>Lists a table's column names, one per row.</summary>
    public static string ColumnsQuery(VisionaryDialect d, string table) => d switch
    {
        VisionaryDialect.Postgres => $"SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = '{table}'",
        VisionaryDialect.MySql => $"SELECT column_name FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = '{table}'",
        _ => $"SELECT name FROM pragma_table_info('{table}')",
    };

    /// <summary>ALTER statements for the added columns missing from <paramref name="existing"/>.</summary>
    public static IEnumerable<string> MissingColumns(VisionaryDialect d, string table, IEnumerable<AddedColumn> added, IEnumerable<string> existing)
    {
        var have = new HashSet<string>(existing, StringComparer.OrdinalIgnoreCase);
        return added.Where(c => !have.Contains(c.Name))
            .Select(c => $"ALTER TABLE {table} ADD COLUMN {c.Name} {SqlType(d, c.Kind)}");
    }

    /// <summary>An ALTER that lost a race with another instance.</summary>
    public static bool IsDuplicateColumn(Exception e)
    {
        var msg = e.Message.ToLowerInvariant();
        return msg.Contains("duplicate column") || msg.Contains("already exists") || msg.Contains("1060");
    }

    /// <summary>
    /// Deletes up to 5000 rows older than the cutoff parameter. MySQL rejects LIMIT inside
    /// IN (subquery) but allows DELETE ... LIMIT; the others are the other way round.
    /// </summary>
    public static string PurgeSql(VisionaryDialect d, string table) => d == VisionaryDialect.MySql
        ? $"DELETE FROM {table} WHERE created_at < ? ORDER BY id LIMIT 5000"
        : $"DELETE FROM {table} WHERE id IN (SELECT id FROM {table} WHERE created_at < ? LIMIT 5000)";

    /// <summary>
    /// An INSERT for <paramref name="columns"/>. D1 takes string params only, so there each
    /// placeholder becomes NULLIF(?, '') and nulls travel as empty strings.
    /// </summary>
    public static string InsertSql(string table, IReadOnlyList<string> columns, bool stringParams)
    {
        var mark = stringParams ? "NULLIF(?, '')" : "?";
        return $"INSERT INTO {table} ({string.Join(", ", columns)}) VALUES ({string.Join(", ", Enumerable.Repeat(mark, columns.Count))})";
    }
}
