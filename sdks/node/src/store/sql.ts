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
  ["container_cpu_cores", "real"],
  ["container_cpu_limit", "real"],
  ["container_cpu_throttled_percent", "real"],
  ["container_mem_used_mb", "real"],
  ["container_mem_limit_mb", "real"],
  ["container_oom_kills", "int"],
  ["psi_cpu_some_avg10", "real"],
  ["psi_cpu_some_avg60", "real"],
  ["psi_mem_some_avg10", "real"],
  ["psi_mem_some_avg60", "real"],
  ["psi_mem_full_avg10", "real"],
  ["psi_io_some_avg10", "real"],
  ["psi_io_some_avg60", "real"],
  ["psi_io_full_avg10", "real"],
  ["minor_faults_ps", "real"],
  ["major_faults_ps", "real"],
  ["swap_in_ps", "real"],
  ["swap_out_ps", "real"],
  ["pages_scanned_ps", "real"],
  ["pages_stolen_ps", "real"],
  ["host_oom_kills", "int"],
  ["mem_dirty_mb", "real"],
  ["mem_writeback_mb", "real"],
  ["mem_slab_mb", "real"],
  ["mem_shmem_mb", "real"],
  ["mem_committed_mb", "real"],
  ["mem_commit_limit_mb", "real"],
  ["container_minor_faults_ps", "real"],
  ["container_major_faults_ps", "real"],
  ["container_mem_high_events", "int"],
  ["container_mem_max_events", "int"],
  ["container_pids", "int"],
  ["container_pids_max", "int"],
  ["container_read_kbps", "real"],
  ["container_write_kbps", "real"],
];

export const ADDED_REQUEST_COLUMNS: [string, Kind][] = [
  ["service", "text"],
  ["version", "text"],
  ["db_count", "int"],
  ["db_ms", "real"],
  ["http_count", "int"],
  ["http_ms", "real"],
  ["login_account", "text"],
  ["trace_id", "text"],
  ["span_id", "text"],
  ["parent_span_id", "text"],
];

export const ADDED_SPAN_COLUMNS: [string, Kind][] = [
  ["trace_id", "text"],
  ["span_id", "text"],
  ["parent_span_id", "text"],
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
  ...ADDED_SPAN_COLUMNS.map(([name]) => name),
];

export const ERROR_COLUMNS = [
  "created_at", "service", "instance", "version", "request_id", "method", "route", "path", "user_id",
  "type", "message", "stack", "fingerprint", "handled", "runtime",
];

export const RELEASE_COLUMNS = ["created_at", "service", "instance", "version", "sdk", "sdk_version", "runtime"];

export const PROFILE_COLUMNS = ["created_at", "service", "instance", "version", "sdk", "duration_ms", "samples", "routes", "folded"];

export const LOG_COLUMNS = [
  "created_at", "service", "instance", "version", "source", "host", "container", "level", "severity", "logger",
  "message", "attrs", "request_id", "trace_id", "span_id", "user_id", "session_id", "code_file", "code_line",
  "code_func", "size",
];

export const METRIC_COLUMNS = [
  "created_at", "service", "instance", "name", "kind", "unit", "labels", "series", "value", "count", "sum", "min", "max", "buckets",
];

export type Table = "api_logs" | "system_metrics" | "spans" | "errors" | "releases" | "profiles" | "app_logs" | "metrics";

export const TABLE_COLUMNS: Record<Table, string[]> = {
  api_logs: REQUEST_COLUMNS,
  system_metrics: SYSTEM_COLUMNS,
  spans: SPAN_COLUMNS,
  errors: ERROR_COLUMNS,
  releases: RELEASE_COLUMNS,
  profiles: PROFILE_COLUMNS,
  app_logs: LOG_COLUMNS,
  metrics: METRIC_COLUMNS,
};

/** A typed value: string, number or null. */
export type Value = string | number | null;

export interface Stmt {
  sql: string;
  args: Value[];
  rows?: number;
}

/** INSERT with `?` placeholders; D1 takes string params only, so there each
 * placeholder is NULLIF(?, '') and nulls travel as empty strings. */
export function insertSQL(table: Table, stringParams: boolean): string {
  const cols = TABLE_COLUMNS[table];
  const ph = stringParams ? "NULLIF(?, '')" : "?";
  return `INSERT INTO ${table} (${cols.join(", ")}) VALUES (${cols.map(() => ph).join(", ")})`;
}

/** Under every backend's limit (SQLite's is 32766) */
export const MAX_PARAMS = 30_000;

export function insertRowsSQL(table: Table, n: number): string {
  const cols = TABLE_COLUMNS[table];
  const row = `(${cols.map(() => "?").join(", ")})`;
  return `INSERT INTO ${table} (${cols.join(", ")}) VALUES ${Array(n).fill(row).join(", ")}`;
}

export function insertRows(table: Table, rows: Value[][]): Stmt[] {
  const per = Math.max(1, Math.floor(MAX_PARAMS / TABLE_COLUMNS[table].length));
  const out: Stmt[] = [];
  for (let i = 0; i < rows.length; i += per) {
    const chunk = rows.slice(i, i + per);
    out.push({ sql: insertRowsSQL(table, chunk.length), args: chunk.flat(), rows: chunk.length });
  }
  return out;
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

// Runs after the added columns, since older tables only get trace_id then
export function traceIndexSQL(dialect: Dialect, table: Table): string {
  const ine = dialect === "mysql" ? "" : "IF NOT EXISTS ";
  return `CREATE INDEX ${ine}idx_${table}_trace_id ON ${table} (trace_id)`;
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
