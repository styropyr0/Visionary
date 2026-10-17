package analytics

import (
	"strconv"
	"strings"

	sdk "github.com/styropyr0/visionary/sdks/go"
)

// Rollup tables hold pre-aggregated rows per bucket. created_at is the bucket
// start in the same text format as the raw tables, so {{from}}, {{to}} and
// {{t}} work on them unchanged; bucket_s is 60 or 3600.

// rollupEdges are the latency histogram's upper bounds in ms: h00 counts
// duration_ms < 2, hNN counts edge[NN-1] <= duration_ms < edge[NN], and the
// last bucket is open. Every latencyBuckets bound is an edge, so heatmap bands
// are exact sums of buckets; elsewhere neighbours are at most ~1.3x apart
// between 10 ms and 5 s, which keeps interpolated percentiles within a few
// percent.
var rollupEdges = []float64{2, 5, 10, 15, 20, 25, 30, 40, 50, 65, 80, 100, 130, 160, 200, 250, 320, 400, 500, 650, 800, 1000, 1300, 1600, 2000, 2500, 3200, 4000, 5000, 10000, 30000}

func histCol(i int) string {
	if i < 10 {
		return "h0" + strconv.Itoa(i)
	}
	return "h" + strconv.Itoa(i)
}

func histCols() []string {
	out := make([]string, len(rollupEdges)+1)
	for i := range out {
		out[i] = histCol(i)
	}
	return out
}

// histCond is the duration_ms range of histogram bucket i.
func histCond(i int) string {
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	switch {
	case i == 0:
		return "duration_ms < " + f(rollupEdges[0])
	case i == len(rollupEdges):
		return "duration_ms >= " + f(rollupEdges[i-1])
	}
	return "duration_ms >= " + f(rollupEdges[i-1]) + " AND duration_ms < " + f(rollupEdges[i])
}

type colType int

const (
	colInt colType = iota
	colFloat
	colText
)

// rollupCol is one measure: how it is computed from raw rows (agg, over a
// group; row, for one raw row standing in for a rollup row) and how rollup
// rows combine into coarser ones.
type rollupCol struct {
	name    string
	typ     colType
	agg     string // aggregate over raw rows
	row     string // the same measure for a single raw row
	combine string // over rollup rows of a finer grain
}

func countCol(name, cond string) rollupCol {
	return rollupCol{name, colInt, countIf(cond), "CASE WHEN " + cond + " THEN 1 ELSE 0 END", "SUM(" + name + ")"}
}

func requestCols(apdexMs int) []rollupCol {
	t, t4 := strconv.Itoa(apdexMs), strconv.Itoa(4*apdexMs)
	cols := []rollupCol{
		{"requests", colInt, "COUNT(*)", "1", "SUM(requests)"},
		countCol("errors_5xx", "status >= 500"),
		countCol("errors_4xx", "status >= 400 AND status < 500"),
		{"sum_ms", colFloat, "SUM(duration_ms)", "duration_ms", "SUM(sum_ms)"},
		{"min_ms", colFloat, "MIN(duration_ms)", "duration_ms", "MIN(min_ms)"},
		{"max_ms", colFloat, "MAX(duration_ms)", "duration_ms", "MAX(max_ms)"},
		countCol("apdex_satisfied", "status < 500 AND duration_ms <= "+t),
		countCol("apdex_tolerating", "status < 500 AND duration_ms > "+t+" AND duration_ms <= "+t4),
		countCol("slow_requests", "duration_ms > "+t4),
		{"apdex_ms", colInt, t, t, "MAX(apdex_ms)"},
		{"sum_response_bytes", colInt, "SUM(response_size)", "response_size", "SUM(sum_response_bytes)"},
		{"db_requests", colInt, "COUNT(db_ms)", "CASE WHEN db_ms IS NOT NULL THEN 1 ELSE 0 END", "SUM(db_requests)"},
		{"sum_db_ms", colFloat, "COALESCE(SUM(db_ms), 0)", "COALESCE(db_ms, 0)", "SUM(sum_db_ms)"},
		{"sum_db_count", colInt, "COALESCE(SUM(db_count), 0)", "COALESCE(db_count, 0)", "SUM(sum_db_count)"},
		{"last_at", colText, "MAX(created_at)", "created_at", "MAX(last_at)"},
	}
	for i := range len(rollupEdges) + 1 {
		cols = append(cols, countCol(histCol(i), histCond(i)))
	}
	return cols
}

// Aggregates kept per system metric.
const (
	sysAvg = 1 << iota // <name>_avg and <name>_n (non-null samples), so averages recombine exactly
	sysMax
	sysSum
)

// sysMetric is a system_metrics column, or an expression over columns, whose
// aggregates rollup_system keeps. expr is spelled exactly as the system panels
// spell it, so the panels translate mechanically (see rollupSystemSQL).
type sysMetric struct {
	name string
	expr string
	aggs int
}

var sysMetrics = []sysMetric{
	{"host_cpu_percent", "", sysAvg | sysMax},
	{"proc_cpu_percent", "", sysAvg | sysMax},
	{"cpu_user_percent", "", sysAvg},
	{"cpu_system_percent", "", sysAvg},
	{"cpu_iowait_percent", "", sysAvg},
	{"cpu_steal_percent", "", sysAvg},
	{"load_1", "", sysAvg | sysMax},
	{"load_5", "", sysAvg},
	{"load_15", "", sysAvg},
	{"load_per_core", "load_1 / NULLIF(cpu_count, 0)", sysAvg | sysMax},
	{"host_mem_percent", "", sysAvg | sysMax},
	{"host_mem_used_mb", "", sysAvg},
	{"host_mem_available_mb", "", sysAvg},
	{"host_mem_cached_mb", "", sysAvg},
	{"swap_used_mb", "", sysMax},
	{"swap_total_mb", "", sysMax},
	{"proc_rss_mb", "", sysMax},
	{"heap_inuse_mb", "", sysMax},
	{"goroutines", "", sysMax},
	{"proc_threads", "", sysMax},
	{"gc_pause_ms", "", sysSum},
	{"disk_used_percent", "", sysAvg | sysMax},
	{"disk_inodes_percent", "", sysMax},
	{"disk_busy_percent", "", sysAvg | sysMax},
	{"disk_read_kbps", "", sysAvg | sysMax},
	{"disk_write_kbps", "", sysAvg | sysMax},
	{"disk_read_iops", "", sysAvg},
	{"disk_write_iops", "", sysAvg},
	{"disk_await_ms", "", sysAvg | sysMax},
	{"net_rx_kbps", "", sysAvg},
	{"net_tx_kbps", "", sysAvg},
	{"net_kbps", "net_rx_kbps + net_tx_kbps", sysAvg | sysMax},
	{"net_rx_pps", "", sysAvg},
	{"net_tx_pps", "", sysAvg},
	{"net_errors", "", sysSum},
	{"net_drops", "", sysSum},
	{"tcp_established", "", sysMax},
	{"tcp_retrans", "", sysSum},
	{"procs_running", "", sysMax},
	{"procs_blocked", "", sysMax},
	{"ctx_switches_ps", "", sysAvg},
	{"proc_read_kbps", "", sysAvg},
	{"proc_write_kbps", "", sysAvg},
	{"proc_open_fds", "", sysMax},
	{"proc_max_fds", "", sysMax},
	{"http_in_flight", "", sysAvg | sysMax},
	{"http_rps", "", sysAvg | sysMax},
	{"http_requests", "", sysSum},
	{"http_errors", "", sysSum},
	{"db_acquired_conns", "", sysAvg | sysMax},
	{"db_total_conns", "", sysMax},
	{"db_max_conns", "", sysMax},
	{"db_acquire_wait_ms", "", sysSum | sysMax},
	{"db_empty_acquires", "", sysSum},
}

func (m sysMetric) source() string {
	if m.expr != "" {
		return m.expr
	}
	return m.name
}

func systemCols() []rollupCol {
	cols := []rollupCol{
		{"samples", colInt, "COUNT(*)", "1", "SUM(samples)"},
		{"last_at", colText, "MAX(created_at)", "created_at", "MAX(last_at)"},
	}
	for _, m := range sysMetrics {
		e := m.source()
		if m.aggs&sysAvg != 0 {
			avg, n := m.name+"_avg", m.name+"_n"
			cols = append(cols,
				rollupCol{avg, colFloat, "AVG(" + e + ")", e, "SUM(" + avg + " * " + n + ") / NULLIF(SUM(" + n + "), 0)"},
				rollupCol{n, colInt, "COUNT(" + e + ")", "CASE WHEN " + e + " IS NOT NULL THEN 1 ELSE 0 END", "SUM(" + n + ")"})
		}
		if m.aggs&sysMax != 0 {
			c := m.name + "_max"
			cols = append(cols, rollupCol{c, colFloat, "MAX(" + e + ")", e, "MAX(" + c + ")"})
		}
		if m.aggs&sysSum != 0 {
			c := m.name + "_sum"
			cols = append(cols, rollupCol{c, colFloat, "SUM(" + e + ")", e, "SUM(" + c + ")"})
		}
	}
	return cols
}

// rollupKey is a dimension: its value from raw rows and its column size.
type rollupKey struct {
	name string
	typ  colType
	size int // VARCHAR length on MySQL; values are cut to it
	expr string
}

// rollupTable describes one rollup table for the aggregation and schema code.
type rollupTable struct {
	name   string
	source string // raw table
	keys   []rollupKey
	cols   []rollupCol
}

func requestsTable(apdexMs int, routes []string) rollupTable {
	return rollupTable{
		name:   "rollup_requests",
		source: "api_logs",
		keys: []rollupKey{
			{"service", colText, 64, "COALESCE(service, '')"},
			{"route", colText, 255, routeCap(routes)},
			{"method", colText, 16, "method"},
			{"status", colInt, 0, "status"},
			{"error_key", colText, 64, errorKeyExpr},
		},
		cols: requestCols(apdexMs),
	}
}

// errorKeyExpr names the error of a failed request, as the errors panel does;
// successful requests share the empty key.
const errorKeyExpr = "CASE WHEN status >= 400 THEN COALESCE(error_key, error_code, '—') ELSE '' END"

func systemTable() rollupTable {
	return rollupTable{
		name:   "rollup_system",
		source: "system_metrics",
		keys:   []rollupKey{{"instance", colText, 128, "instance"}},
		cols:   systemCols(),
	}
}

// routeCap keeps routes from the known list and folds the rest (raw paths
// with ids in them, mostly) into "other", so the table cannot grow per URL.
// The names come from the database; only ones made of safe characters are
// inlined.
func routeCap(routes []string) string {
	var known []string
	for _, r := range routes {
		if safeRoute(r) {
			known = append(known, "'"+r+"'")
		}
	}
	if len(known) == 0 {
		return "'other'"
	}
	return "CASE WHEN COALESCE(route, path) IN (" + strings.Join(known, ", ") + ") THEN COALESCE(route, path) ELSE 'other' END"
}

func safeRoute(r string) bool {
	if r == "" || len(r) > 255 {
		return false
	}
	for _, c := range r {
		if c < 0x20 || c == '\'' || c == '\\' || c > 0x7e {
			return false
		}
	}
	return true
}

func (t rollupTable) keyNames() []string {
	out := []string{"bucket_s", "created_at"}
	for _, k := range t.keys {
		out = append(out, k.name)
	}
	return out
}

func (t rollupTable) colNames() []string {
	out := t.keyNames()
	for _, c := range t.cols {
		out = append(out, c.name)
	}
	return append(out, "built_at")
}

func (q sqlDialect) colDDL(typ colType, size int, key bool) string {
	switch typ {
	case colInt:
		if q.d == sdk.SQLite {
			return "INTEGER"
		}
		return "BIGINT"
	case colFloat:
		switch q.d {
		case sdk.Postgres:
			return "DOUBLE PRECISION"
		case sdk.MySQL:
			return "DOUBLE"
		}
		return "REAL"
	}
	if q.d != sdk.MySQL {
		return "TEXT"
	}
	if !key && size == 0 {
		return "VARCHAR(32)"
	}
	return "VARCHAR(" + strconv.Itoa(size) + ")"
}

func (q sqlDialect) rollupDDL(t rollupTable) string {
	var b strings.Builder
	b.WriteString("CREATE TABLE IF NOT EXISTS " + t.name + " (\n")
	b.WriteString("    bucket_s INTEGER NOT NULL,\n")
	b.WriteString("    created_at " + q.colDDL(colText, 24, true) + " NOT NULL,\n")
	for _, k := range t.keys {
		b.WriteString("    " + k.name + " " + q.colDDL(k.typ, k.size, true) + " NOT NULL,\n")
	}
	for _, c := range t.cols {
		b.WriteString("    " + c.name + " " + q.colDDL(c.typ, 32, false) + ",\n")
	}
	b.WriteString("    built_at " + q.colDDL(colText, 24, false) + " NOT NULL,\n")
	b.WriteString("    PRIMARY KEY (" + strings.Join(t.keyNames(), ", ") + ")\n)")
	if q.d == sdk.MySQL {
		// Binary, no-pad collation: keys that differ only in case or trailing
		// spaces must stay distinct rows.
		b.WriteString(" CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin")
	}
	return b.String()
}

const rollupStateTable = "rollup_state"

func (q sqlDialect) rollupStateDDL() string {
	return `CREATE TABLE IF NOT EXISTS rollup_state (
    name       ` + q.colDDL(colText, 32, true) + ` NOT NULL PRIMARY KEY,
    low_at     ` + q.colDDL(colText, 24, true) + ` NOT NULL,
    high_at    ` + q.colDDL(colText, 24, true) + ` NOT NULL,
    updated_at ` + q.colDDL(colText, 24, true) + ` NOT NULL
)`
}

func (q sqlDialect) concat(parts ...string) string {
	if q.d == sdk.MySQL {
		return "CONCAT(" + strings.Join(parts, ", ") + ")"
	}
	return strings.Join(parts, " || ")
}

// bucketText floors created_at to the minute or hour, as text.
func (q sqlDialect) bucketText(grain int64) string {
	if grain == 3600 {
		return q.concat("SUBSTR(created_at, 1, 13)", "':00:00.000Z'")
	}
	return q.concat("SUBSTR(created_at, 1, 16)", "':00.000Z'")
}

func cut(expr string, size int) string {
	if size == 0 {
		return expr
	}
	return "SUBSTR(" + expr + ", 1, " + strconv.Itoa(size) + ")"
}

// aggregateSQL builds the rows of grain over [?, ?) from the raw table
// (from == "") or from the finer rollup rows of grain fromGrain. The last
// parameter is the build stamp.
func (q sqlDialect) aggregateSQL(t rollupTable, grain, fromGrain int64) string {
	g := strconv.FormatInt(grain, 10)
	sel := []string{g + " AS bucket_s", q.bucketText(grain) + " AS created_at"}
	group := []string{"1", "2"}
	for i, k := range t.keys {
		e := cut(k.expr, k.size)
		if fromGrain != 0 {
			e = k.name
		}
		sel = append(sel, e+" AS "+k.name)
		group = append(group, strconv.Itoa(i+3))
	}
	for _, c := range t.cols {
		e := c.agg
		if fromGrain != 0 {
			e = c.combine
		}
		sel = append(sel, e+" AS "+c.name)
	}
	sel = append(sel, q.text("?")+" AS built_at")
	from := t.source + " WHERE created_at >= ? AND created_at < ?"
	if fromGrain != 0 {
		from = t.name + " WHERE bucket_s = " + strconv.FormatInt(fromGrain, 10) + " AND created_at >= ? AND created_at < ?"
	}
	selectSQL := "SELECT " + strings.Join(sel, ",\n  ") + "\nFROM " + from + "\nGROUP BY " + strings.Join(group, ", ")
	return q.upsert(t.name, t.colNames(), t.keyNames(), selectSQL)
}

// upsert inserts the rows of selectSQL, replacing rows with the same key.
func (q sqlDialect) upsert(table string, cols, keys []string, selectSQL string) string {
	isKey := map[string]bool{}
	for _, k := range keys {
		isKey[k] = true
	}
	var set []string
	insert := "INSERT INTO " + table + " (" + strings.Join(cols, ", ") + ")\n"
	if q.d == sdk.MySQL {
		for _, c := range cols {
			if !isKey[c] {
				set = append(set, c+" = s."+c)
			}
		}
		return insert + "SELECT * FROM (\n" + selectSQL + "\n) AS s\nON DUPLICATE KEY UPDATE " + strings.Join(set, ", ")
	}
	for _, c := range cols {
		if !isKey[c] {
			set = append(set, c+" = excluded."+c)
		}
	}
	return insert + selectSQL + "\nON CONFLICT (" + strings.Join(keys, ", ") + ") DO UPDATE SET " + strings.Join(set, ", ")
}

// sweepSQL removes rows of an aggregated range that the last build did not
// write (a key that no longer occurs). Parameters: from, to, build stamp.
func sweepSQL(t rollupTable, grain int64) string {
	return "DELETE FROM " + t.name + " WHERE bucket_s = " + strconv.FormatInt(grain, 10) +
		" AND created_at >= ? AND created_at < ? AND built_at < ?"
}

// rollupRowsSQL selects rollup rows of one grain in a range as the common
// column set the panels read.
func (t rollupTable) rollupRowsSQL(grain int64, from, to string) string {
	cols := []string{"created_at"}
	for _, k := range t.keys {
		cols = append(cols, k.name)
	}
	for _, c := range t.cols {
		cols = append(cols, c.name)
	}
	return "SELECT " + strings.Join(cols, ", ") + " FROM " + t.name +
		" WHERE bucket_s = " + strconv.FormatInt(grain, 10) + " AND created_at >= '" + from + "' AND created_at < '" + to + "'"
}

// rawRowsSQL selects raw rows in a range shaped like rollup rows, for the
// edges of a range that no rollup bucket covers. Routes are not capped here:
// these are a few minutes of rows.
func (t rollupTable) rawRowsSQL(from, to string) string {
	cols := []string{"created_at"}
	for _, k := range t.keys {
		e := k.expr
		if k.name == "route" {
			e = "COALESCE(route, path)"
		}
		cols = append(cols, e+" AS "+k.name)
	}
	for _, c := range t.cols {
		cols = append(cols, c.row+" AS "+c.name)
	}
	return "SELECT " + strings.Join(cols, ", ") + " FROM " + t.source +
		" WHERE created_at >= '" + from + "' AND created_at < '" + to + "'"
}
