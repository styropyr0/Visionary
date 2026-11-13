package visionary

import (
	"context"
	"strings"
)

// Column is a column added to a table after its first release. Tables created
// earlier gain these columns at startup; new tables have them from the start.
type Column struct {
	Name string
	// Kind is "real", "int" or "text".
	Kind string
}

// AddedSystemColumns are system_metrics columns beyond the original set, in
// insert order after systemBaseColumns.
var AddedSystemColumns = []Column{
	{"cpu_count", "int"},
	{"cpu_user_percent", "real"},
	{"cpu_system_percent", "real"},
	{"cpu_iowait_percent", "real"},
	{"cpu_steal_percent", "real"},
	{"host_mem_available_mb", "real"},
	{"host_mem_cached_mb", "real"},
	{"swap_used_mb", "real"},
	{"swap_total_mb", "real"},
	{"disk_total_gb", "real"},
	{"disk_inodes_percent", "real"},
	{"disk_read_kbps", "real"},
	{"disk_write_kbps", "real"},
	{"disk_read_iops", "real"},
	{"disk_write_iops", "real"},
	{"disk_busy_percent", "real"},
	{"disk_await_ms", "real"},
	{"net_rx_pps", "real"},
	{"net_tx_pps", "real"},
	{"net_errors", "int"},
	{"net_drops", "int"},
	{"tcp_established", "int"},
	{"tcp_retrans", "int"},
	{"host_procs", "int"},
	{"procs_running", "int"},
	{"procs_blocked", "int"},
	{"ctx_switches_ps", "real"},
	{"proc_read_kbps", "real"},
	{"proc_write_kbps", "real"},
	{"proc_max_fds", "int"},
	{"service", "text"},
	{"version", "text"},
	{"sdk", "text"},
	{"runtime", "text"},
	{"container_cpu_cores", "real"},
	{"container_cpu_limit", "real"},
	{"container_cpu_throttled_percent", "real"},
	{"container_mem_used_mb", "real"},
	{"container_mem_limit_mb", "real"},
	{"container_oom_kills", "int"},
	{"psi_cpu_some_avg10", "real"},
	{"psi_cpu_some_avg60", "real"},
	{"psi_mem_some_avg10", "real"},
	{"psi_mem_some_avg60", "real"},
	{"psi_mem_full_avg10", "real"},
	{"psi_io_some_avg10", "real"},
	{"psi_io_some_avg60", "real"},
	{"psi_io_full_avg10", "real"},
	{"minor_faults_ps", "real"},
	{"major_faults_ps", "real"},
	{"swap_in_ps", "real"},
	{"swap_out_ps", "real"},
	{"pages_scanned_ps", "real"},
	{"pages_stolen_ps", "real"},
	{"host_oom_kills", "int"},
	{"mem_dirty_mb", "real"},
	{"mem_writeback_mb", "real"},
	{"mem_slab_mb", "real"},
	{"mem_shmem_mb", "real"},
	{"mem_committed_mb", "real"},
	{"mem_commit_limit_mb", "real"},
	{"container_minor_faults_ps", "real"},
	{"container_major_faults_ps", "real"},
	{"container_mem_high_events", "int"},
	{"container_mem_max_events", "int"},
	{"cpu_temp_c", "real"},
	{"cpu_mhz", "real"},
	{"cpu_max_mhz", "real"},
	{"cpu_throttle_events", "int"},
	{"files_open", "int"},
	{"files_max", "int"},
	{"sockets_used", "int"},
	{"tcp_time_wait", "int"},
	{"tcp_orphans", "int"},
	{"tcp_close_wait", "int"},
	{"conntrack_count", "int"},
	{"conntrack_max", "int"},
	{"udp_in_errors", "int"},
	{"udp_buf_errors", "int"},
	{"gpu_count", "int"},
	{"gpu_util_percent", "real"},
	{"gpu_mem_percent", "real"},
	{"gpu_temp_c", "real"},
	{"container_pids", "int"},
	{"container_pids_max", "int"},
	{"container_read_kbps", "real"},
	{"container_write_kbps", "real"},
}

// AddedRequestColumns are api_logs columns beyond the original set, in insert
// order after the original columns: the service and release (version) that
// served the request, how many database and outbound HTTP calls it made and
// how long they took in total, the account a sign-in tried (LoginAttempt),
// and the request's place in a distributed trace (it is the server span)
var AddedRequestColumns = []Column{
	{"service", "text"},
	{"version", "text"},
	{"db_count", "int"},
	{"db_ms", "real"},
	{"http_count", "int"},
	{"http_ms", "real"},
	{"login_account", "text"},
	{"trace_id", "text"},
	{"span_id", "text"},
	{"parent_span_id", "text"},
}

// AddedSpanColumns are spans columns beyond the original set, in insert order
var AddedSpanColumns = []Column{
	{"trace_id", "text"},
	{"span_id", "text"},
	{"parent_span_id", "text"},
}

// MySQL has no IF NOT EXISTS here, so an existing index is error 1061, which callers ignore
func (d Dialect) TraceIndex(table string) string {
	ine := "IF NOT EXISTS "
	if d == MySQL {
		ine = ""
	}
	return "CREATE INDEX " + ine + "idx_" + table + "_trace_id ON " + table + " (trace_id)"
}

// SQLType is the column type of kind in d.
func (d Dialect) SQLType(kind string) string {
	switch {
	case kind == "text" && d == MySQL:
		return "VARCHAR(255)"
	case kind == "text":
		return "TEXT"
	case kind == "int" && d == Postgres, kind == "int" && d == MySQL:
		return "BIGINT"
	case kind == "int":
		return "INTEGER"
	case d == Postgres:
		return "DOUBLE PRECISION"
	case d == MySQL:
		return "DOUBLE"
	}
	return "REAL"
}

// ColumnsQuery lists a table's column names, one per row.
func (d Dialect) ColumnsQuery(table string) string {
	switch d {
	case Postgres:
		return "SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = '" + table + "'"
	case MySQL:
		return "SELECT column_name FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = '" + table + "'"
	}
	return "SELECT name FROM pragma_table_info('" + table + "')"
}

// MissingColumns returns ALTER statements for the added columns that are not
// in existing (names, any case).
func (d Dialect) MissingColumns(table string, added []Column, existing []string) []string {
	have := map[string]bool{}
	for _, c := range existing {
		have[strings.ToLower(c)] = true
	}
	var out []string
	for _, c := range added {
		if !have[c.Name] {
			out = append(out, "ALTER TABLE "+table+" ADD COLUMN "+c.Name+" "+d.SQLType(c.Kind))
		}
	}
	return out
}

// IsDuplicateColumn reports an ALTER that lost a race with another instance.
func IsDuplicateColumn(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate column") || strings.Contains(msg, "already exists") || strings.Contains(msg, "1060")
}

// migrateSystem adds missing system_metrics columns.
func migrateSystem(ctx context.Context, s store) error {
	return migrate(ctx, s, "system_metrics", AddedSystemColumns)
}

func migrateRequests(ctx context.Context, s store) error {
	if err := migrate(ctx, s, "api_logs", AddedRequestColumns); err != nil {
		return err
	}
	return s.execScript(ctx, s.dialect().TraceIndex("api_logs"))
}

func migrateSpans(ctx context.Context, s store) error {
	if err := migrate(ctx, s, "spans", AddedSpanColumns); err != nil {
		return err
	}
	return s.execScript(ctx, s.dialect().TraceIndex("spans"))
}

func migrate(ctx context.Context, s store, table string, added []Column) error {
	d := s.dialect()
	existing, err := s.columns(ctx, d.ColumnsQuery(table))
	if err != nil {
		return err
	}
	for _, stmt := range d.MissingColumns(table, added, existing) {
		if err := s.execScript(ctx, stmt); err != nil && !IsDuplicateColumn(err) {
			return err
		}
	}
	return nil
}
