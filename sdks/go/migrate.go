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
}

// AddedRequestColumns are api_logs columns beyond the original set, in insert
// order after the original columns: the service and release (version) that
// served the request, and how many database and outbound HTTP calls it made
// and how long they took in total.
var AddedRequestColumns = []Column{
	{"service", "text"},
	{"version", "text"},
	{"db_count", "int"},
	{"db_ms", "real"},
	{"http_count", "int"},
	{"http_ms", "real"},
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

// migrateRequests adds missing api_logs columns.
func migrateRequests(ctx context.Context, s store) error {
	return migrate(ctx, s, "api_logs", AddedRequestColumns)
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
