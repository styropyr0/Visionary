import type { Dialect } from "../config.js";

export type Kind = "int" | "real" | "text";

/** Columns added after a table's first release, in insert order after the
 * original ones (sdks/go/migrate.go). Older tables gain them at startup. */
export const ADDED_SYSTEM_COLUMNS: [string, Kind][] = [
  ["cpu_count", "int"],
  ["cpu_user_percent", "real"],
  ["cpu_system_percent", "real"],
  ["cpu_iowait_percent", "real"],
  ["cpu_steal_percent", "real"],
  ["host_mem_available_mb", "real"],
  ["host_mem_cached_mb", "real"],
  ["swap_used_mb", "real"],
  ["swap_total_mb", "real"],
  ["disk_total_gb", "real"],
  ["disk_inodes_percent", "real"],
  ["disk_read_kbps", "real"],
  ["disk_write_kbps", "real"],
  ["disk_read_iops", "real"],
  ["disk_write_iops", "real"],
  ["disk_busy_percent", "real"],
  ["disk_await_ms", "real"],
  ["net_rx_pps", "real"],
  ["net_tx_pps", "real"],
  ["net_errors", "int"],
  ["net_drops", "int"],
  ["tcp_established", "int"],
  ["tcp_retrans", "int"],
  ["host_procs", "int"],
  ["procs_running", "int"],
  ["procs_blocked", "int"],
  ["ctx_switches_ps", "real"],
  ["proc_read_kbps", "real"],
  ["proc_write_kbps", "real"],
  ["proc_max_fds", "int"],
  ["service", "text"],
  ["version", "text"],
  ["sdk", "text"],
  ["runtime", "text"],
];

export const ADDED_REQUEST_COLUMNS: [string, Kind][] = [
  ["service", "text"],
  ["version", "text"],
  ["db_count", "int"],
  ["db_ms", "real"],
  ["http_count", "int"],
  ["http_ms", "real"],
];

export const REQUEST_COLUMNS = [
  "request_id", "created_at", "method", "path", "route", "query", "status", "duration_ms", "ip", "user_agent",
  "user_id", "user_handle", "user_role", "request_headers", "request_content_type", "request_body",
  "request_body_truncated", "request_size", "response_content_type", "response_body",
  "response_body_truncated", "response_size", "error_code", "error_key",
  ...ADDED_REQUEST_COLUMNS.map(([name]) => name),
];

export const SYSTEM_COLUMNS = [
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
  ...ADDED_SYSTEM_COLUMNS.map(([name]) => name),
];

export const SPAN_COLUMNS = [
  "created_at", "request_id", "service", "instance", "kind", "name", "target", "detail",
  "offset_ms", "duration_ms", "status", "error", "row_count",
];

export const ERROR_COLUMNS = [
  "created_at", "service", "instance", "version", "request_id", "method", "route", "path", "user_id",
  "type", "message", "stack", "fingerprint", "handled", "runtime",
];

export const RELEASE_COLUMNS = ["created_at", "service", "instance", "version", "sdk", "sdk_version", "runtime"];

export type Table = "api_logs" | "system_metrics" | "spans" | "errors" | "releases";

export const TABLE_COLUMNS: Record<Table, string[]> = {
  api_logs: REQUEST_COLUMNS,
  system_metrics: SYSTEM_COLUMNS,
  spans: SPAN_COLUMNS,
  errors: ERROR_COLUMNS,
  releases: RELEASE_COLUMNS,
};

/** A typed value: string, number or null. */
export type Value = string | number | null;

export interface Stmt {
  sql: string;
  args: Value[];
}

/** INSERT with `?` placeholders; D1 takes string params only, so there each
 * placeholder is NULLIF(?, '') and nulls travel as empty strings. */
export function insertSQL(table: Table, stringParams: boolean): string {
  const cols = TABLE_COLUMNS[table];
  const ph = stringParams ? "NULLIF(?, '')" : "?";
  return `INSERT INTO ${table} (${cols.join(", ")}) VALUES (${cols.map(() => ph).join(", ")})`;
}

export const PURGE_CHUNK = 5000;

/** Deletes up to 5000 rows older than the cutoff parameter. MySQL rejects
 * LIMIT inside IN (subquery) but allows DELETE ... LIMIT. */
export function purgeSQL(dialect: Dialect, table: Table): string {
  if (dialect === "mysql") return `DELETE FROM ${table} WHERE created_at < ? ORDER BY id LIMIT ${PURGE_CHUNK}`;
  return `DELETE FROM ${table} WHERE id IN (SELECT id FROM ${table} WHERE created_at < ? LIMIT ${PURGE_CHUNK})`;
}

/** "?" to "$1, $2, ..." for PostgreSQL, leaving quoted question marks alone. */
export function rebind(sql: string): string {
  let out = "";
  let n = 0;
  let quote = "";
  for (const c of sql) {
    if (quote) {
      if (c === quote) quote = "";
    } else if (c === "'" || c === '"') {
      quote = c;
    } else if (c === "?") {
      out += "$" + ++n;
      continue;
    }
    out += c;
  }
  return out;
}

/** Splits a schema script on ";". */
export function statements(script: string): string[] {
  return script
    .split(";")
    .map((s) => s.trim())
    .filter(Boolean);
}

export function sqlType(dialect: Dialect, kind: Kind): string {
  if (kind === "text") return dialect === "mysql" ? "VARCHAR(255)" : "TEXT";
  if (kind === "int") return dialect === "sqlite" ? "INTEGER" : "BIGINT";
  return dialect === "postgres" ? "DOUBLE PRECISION" : dialect === "mysql" ? "DOUBLE" : "REAL";
}

export function columnsQuery(dialect: Dialect, table: Table): string {
  if (dialect === "postgres") {
    return `SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = '${table}'`;
  }
  if (dialect === "mysql") {
    return `SELECT column_name FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = '${table}'`;
  }
  return `SELECT name FROM pragma_table_info('${table}')`;
}

export function missingColumns(dialect: Dialect, table: Table, added: [string, Kind][], existing: string[]): string[] {
  const have = new Set(existing.map((c) => c.toLowerCase()));
  return added
    .filter(([name]) => !have.has(name))
    .map(([name, kind]) => `ALTER TABLE ${table} ADD COLUMN ${name} ${sqlType(dialect, kind)}`);
}

/** An ALTER that lost a race with another instance. */
export function isDuplicateColumn(e: unknown): boolean {
  const msg = String((e as Error)?.message ?? e).toLowerCase();
  return msg.includes("duplicate column") || msg.includes("already exists") || msg.includes("1060");
}
