package analytics

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/styropyr0/visionary/internal/db"
)

// The System view's machine endpoints, for hosts the agent watches. Charts
// come from GET /api/system/{panel} with instance = the host, so only the
// machine list and the snapshots live here. Security findings are the
// Security page's.

// machineColumns is the machine list, one row per host.
var machineColumns = []string{
	"host", "service", "os", "status", "cpu_percent", "mem_percent", "disk_percent", "load_per_core", "uptime_s",
	"failed_units", "containers", "last_seen", "ip", "agent_version", "report_s",
}

// hostStatus is online within three report intervals of the last report,
// stale for a while after, then offline.
func hostStatus(lastSeen string, reportS float64, now time.Time) (string, float64) {
	t, err := time.Parse(tsLayout, lastSeen)
	if err != nil {
		return "offline", 0
	}
	age := now.Sub(t).Seconds()
	switch {
	case age < 3*reportS:
		return "online", age
	case age < max(15*60, 10*reportS):
		return "stale", age
	}
	return "offline", age
}

// GET /api/system/machines: every host in host_inventory with its latest
// metrics, fullest disk, failed units and running containers.
func (s *Server) handleMachines(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := time.Now().UTC()
	out := &Result{Columns: machineColumns, Rows: [][]any{}}
	inv, err := s.db.Query(ctx, `SELECT host, service, os, last_seen, ip, agent_version FROM host_inventory ORDER BY host LIMIT 5000`)
	if err != nil {
		if missingTable(err, "host_inventory") {
			writeJSON(w, http.StatusOK, out)
			return
		}
		s.fail(w, err)
		return
	}
	// Current values come from the last hour only: an older sample says
	// nothing about the host now.
	recent := now.Add(-time.Hour).Format(tsLayout)
	metrics := s.hostLookup(ctx, `
SELECT m.instance, m.host_cpu_percent, m.host_mem_percent, m.load_1 / NULLIF(m.cpu_count, 0), m.uptime_s
FROM system_metrics AS m
JOIN (SELECT instance, MAX(created_at) AS at FROM system_metrics WHERE sdk = 'agent' AND created_at >= ? GROUP BY instance) AS l
  ON l.instance = m.instance AND l.at = m.created_at
WHERE m.sdk = 'agent'`, recent)
	disks := s.hostLookup(ctx, `
SELECT d.host, MAX(d.used_percent)
FROM host_mounts AS d
JOIN (SELECT host, MAX(created_at) AS at FROM host_mounts WHERE created_at >= ? GROUP BY host) AS l
  ON l.host = d.host AND l.at = d.created_at
GROUP BY d.host`, recent)
	units := s.hostLookup(ctx, `
SELECT u.host, `+countIf("u.state = 'failed'")+`
FROM host_units AS u
JOIN (SELECT host, MAX(created_at) AS at FROM host_units WHERE created_at >= ? GROUP BY host) AS l
  ON l.host = u.host AND l.at = u.created_at
GROUP BY u.host`, recent)
	containers := s.hostLookup(ctx, `
SELECT c.host, `+countIf("c.state = 'running'")+`
FROM host_containers AS c
JOIN (SELECT host, MAX(created_at) AS at FROM host_containers WHERE created_at >= ? GROUP BY host) AS l
  ON l.host = c.host AND l.at = c.created_at
GROUP BY c.host`, recent)

	h := s.hosts()
	for _, row := range inv.Rows {
		host := toString(row[0])
		ri := h.reportInterval(host)
		status, _ := hostStatus(toString(row[3]), ri, now)
		m := metrics[host]
		cell := func(vals []any, i int) any {
			if i < len(vals) {
				return vals[i]
			}
			return nil
		}
		failed := cell(units[host], 0)               // NULL when systemd is not readable
		running := cell(containers[host], 0) // NULL without a container engine
		out.Rows = append(out.Rows, []any{
			host, row[1], row[2], status,
			cell(m, 0), cell(m, 1), cell(disks[host], 0), roundCell(cell(m, 2), 2), cell(m, 3),
			failed, running, row[3], row[4], row[5], ri,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// hostLookup runs a query whose first column is the host and returns the
// other columns by host. A missing table or a failed query is no data: the
// fleet still lists its hosts.
func (s *Server) hostLookup(ctx context.Context, sql string, args ...any) map[string][]any {
	out := map[string][]any{}
	res, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		if ctx.Err() == nil && !strings.Contains(strings.ToLower(err.Error()), "no such table") && !strings.Contains(strings.ToLower(err.Error()), "exist") {
			s.logger.Warn("hosts lookup", "err", err)
		}
		return out
	}
	for _, row := range res.Rows {
		if len(row) > 1 {
			out[toString(row[0])] = row[1:]
		}
	}
	return out
}

func roundCell(v any, prec int) any {
	f, ok := cellFloat(v)
	if !ok {
		return nil
	}
	return finite(f, prec)
}

// GET /api/system/machines/{host}: the host's inventory and status, and its
// latest process, mount and unit snapshots.
func (s *Server) handleMachine(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	if !instanceName.MatchString(host) {
		writeError(w, http.StatusBadRequest, "invalid host name")
		return
	}
	ctx := r.Context()
	inv, err := s.db.Query(ctx, `SELECT host, service, os, kernel, arch, cpu_count, mem_total_mb, boot_time, agent_version, ip, first_seen, last_seen FROM host_inventory WHERE host = ?`, host)
	if err != nil && !missingTable(err, "host_inventory") {
		s.fail(w, err)
		return
	}
	if err != nil || len(inv.Rows) == 0 {
		writeError(w, http.StatusNotFound, "no agent has reported for this host")
		return
	}
	info := map[string]any{}
	for i, c := range inv.Columns {
		info[c] = inv.Rows[0][i]
	}
	ri := s.hosts().reportInterval(host)
	status, age := hostStatus(toString(info["last_seen"]), ri, time.Now().UTC())
	info["status"], info["age_s"], info["report_s"] = status, int64(age), ri

	snapshot := func(table, cols, order string) *Result {
		res, err := s.db.Query(ctx, `SELECT MAX(created_at) FROM `+table+` WHERE host = ?`, host)
		if err != nil || len(res.Rows) == 0 || res.Rows[0][0] == nil {
			return emptyResult(strings.Split(cols, ", "))
		}
		at := toString(res.Rows[0][0])
		q := `SELECT ` + cols + ` FROM ` + table + ` WHERE host = ? AND created_at = ? ORDER BY ` + order
		rows, err := s.db.Query(ctx, q, host, at)
		if err != nil {
			s.logger.Warn("host snapshot", "table", table, "err", err)
			return emptyResult(strings.Split(cols, ", "))
		}
		return dbResult(rows, q)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"host":      info,
		"processes": snapshot("host_processes", "created_at, pid, name, username, cpu_percent, rss_mb, cmdline", "cpu_percent DESC, rss_mb DESC"),
		"mounts":    snapshot("host_mounts", "created_at, mount, device, fstype, used_percent, free_gb, total_gb, inodes_percent", "mount"),
		"units":     snapshot("host_units", "created_at, unit, state, sub", "unit"),
	})
}

func emptyResult(cols []string) *Result { return &Result{Columns: cols, Rows: [][]any{}} }

func dbResult(rows *db.Rows, sql string) *Result {
	out := &Result{Columns: rows.Columns, Rows: rows.Rows, RowsRead: rows.RowsRead, DurationMs: rows.DurationMs, SQL: sql}
	if out.Rows == nil {
		out.Rows = [][]any{}
	}
	return out
}
