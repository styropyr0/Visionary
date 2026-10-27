package analytics

import (
	"context"
	"encoding/json"
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/styropyr0/visionary/internal/agentapi"
	sdk "github.com/styropyr0/visionary/sdks/go"
)

// POST /api/agent/v1/report: one report of the host agent (agentapi.Report),
// authenticated with a server ingest key the way OTLP is. The whole report is
// written in one transaction (one batch call on D1), then the host's
// inventory row is upserted. 200 means stored; 400, 401 and 413 must not be
// retried; 429 and 503 should be.

// Caps per report. A minute's report is far below them; an agent that was
// offline sends its buffered reports one by one.
const (
	hostMaxSamples   = 1000
	hostMaxProcesses = 100
	hostMaxMounts    = 200
	hostMaxUnits     = 500
	hostMaxEvents    = 1000
	// A container sample per running container per host sample.
	hostMaxContainers       = 500
	hostMaxContainerSamples = 20000
)

var (
	hostSystemTable = &otlpTable{"system_metrics", append([]string{"created_at", "instance", "service", "sdk", "runtime"}, sdk.HostColumns...)}
	hostProcTable   = &otlpTable{"host_processes", []string{"created_at", "host", "pid", "name", "username", "cpu_percent", "rss_mb", "cmdline"}}
	hostMountTable  = &otlpTable{"host_mounts", []string{"created_at", "host", "mount", "device", "fstype", "used_percent", "free_gb", "total_gb", "inodes_percent"}}
	hostUnitTable   = &otlpTable{"host_units", []string{"created_at", "host", "unit", "state", "sub"}}
	securityTable   = &otlpTable{"security_events", []string{"created_at", "source", "kind", "severity", "host", "service", "actor", "ip", "target", "detail", "count"}}
)

// hostIntCols are the HostColumns stored as integers.
var hostIntCols = func() map[string]bool {
	out := map[string]bool{"uptime_s": true}
	for _, c := range sdk.AddedSystemColumns {
		if c.Kind == "int" {
			out[c.Name] = true
		}
	}
	return out
}()

// hostEventKinds are the kinds an agent may report.
var hostEventKinds = map[string]bool{
	"ssh_failed": true, "ssh_login": true, "sudo": true, "port_opened": true, "port_closed": true, "updates_pending": true,
	"ssh_key_added": true, "ssh_key_removed": true, "file_changed": true, "suspicious_process": true,
	"container_privileged": true, "container_root": true, "container_new_image": true,
}

func (s *Server) handleAgentReport(w http.ResponseWriter, r *http.Request) {
	if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	if !s.ingestKeyValid(otlpKey(r), "server") {
		writeError(w, http.StatusUnauthorized, "missing or invalid ingest key")
		return
	}
	h := s.hosts()
	select {
	case h.inflight <- struct{}{}:
		defer func() { <-h.inflight }()
	default:
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusTooManyRequests, "too many concurrent reports")
		return
	}
	syncLimit(h.limiter, s.confInt("agent_rate"))
	if !h.limiter.Allow() {
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusTooManyRequests, "report rate exceeded")
		return
	}
	body, status, err := otlpReadBody(w, r)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	var rep agentapi.Report
	if err := json.Unmarshal(body, &rep); err != nil {
		writeError(w, http.StatusBadRequest, "the body is not a valid agent report")
		return
	}
	rep.Host = strings.TrimSpace(rep.Host)
	if !instanceName.MatchString(rep.Host) {
		writeError(w, http.StatusBadRequest, "host must be 1-128 letters, digits, '.', '_', ':' or '-'")
		return
	}
	at, ok := reportTime(rep.At, time.Now())
	if !ok {
		writeError(w, http.StatusBadRequest, "at must be an RFC 3339 time no later than now")
		return
	}

	rows, n := hostRows(&rep, at)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	prevBoot, err := s.hostBootTime(ctx, rep.Host)
	if err == nil {
		if ev := rebootEvent(&rep, prevBoot, at); ev != nil {
			rows.add(securityTable, ev)
			n["events"]++
		}
		err = s.otlp().write(ctx, rows)
	}
	if err == nil {
		err = s.upsertHost(ctx, &rep, at)
	}
	if err != nil {
		s.logger.Warn("agent report write", "host", rep.Host, "err", err)
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, "the database is unavailable; retry later")
		return
	}
	h.noteInterval(rep.Host, rep.ReportS)
	writeJSON(w, http.StatusOK, map[string]any{"accepted": n})
}

// reportTime parses a report or sample time. Times a little ahead of the
// server's clock are pulled back to now; far ahead is an error.
func reportTime(v string, now time.Time) (string, bool) {
	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(v))
	if err != nil {
		return "", false
	}
	if t.After(now.Add(5 * time.Minute)) {
		return "", false
	}
	if t.After(now) {
		t = now
	}
	return t.UTC().Format(tsLayout), true
}

// hostRows turns a report into rows, dropping what does not validate, and
// counts what it kept per kind.
func hostRows(rep *agentapi.Report, at string) (*otlpRows, map[string]int) {
	rows := &otlpRows{}
	n := map[string]int{"samples": 0, "processes": 0, "mounts": 0, "units": 0, "events": 0, "containers": 0, "container_samples": 0, "container_events": 0}
	host, service := rep.Host, otlpNullStr(clip(rep.Service, 128))
	runtime := otlpNullStr(clip(rep.Runtime, 128))
	now := time.Now()

	for _, smp := range capped(rep.Samples, hostMaxSamples) {
		ts, ok := reportTime(smp.At, now)
		if !ok || len(smp.Values) == 0 {
			continue
		}
		row := []any{ts, host, service, "agent", runtime}
		for _, c := range sdk.HostColumns {
			row = append(row, hostValue(c, smp.Values))
		}
		if row[5] == nil { // interval_s is NOT NULL
			row[5] = 15.0
		}
		rows.add(hostSystemTable, row)
		n["samples"]++
	}
	for _, p := range capped(rep.Processes, hostMaxProcesses) {
		name := clip(p.Name, 256)
		if name == "" || p.PID <= 0 {
			continue
		}
		rows.add(hostProcTable, []any{at, host, int64(p.PID), name, otlpNullStr(clip(p.Username, 128)),
			finite(p.CPUPercent, 1), finite(p.RSSMB, 1), otlpNullStr(clip(p.Cmdline, 300))})
		n["processes"]++
	}
	for _, m := range capped(rep.Mounts, hostMaxMounts) {
		mount := clip(m.Mount, 512)
		if mount == "" {
			continue
		}
		var inodes any
		if m.InodesPercent != nil {
			inodes = finite(*m.InodesPercent, 1)
		}
		rows.add(hostMountTable, []any{at, host, mount, otlpNullStr(clip(m.Device, 512)), otlpNullStr(clip(m.FSType, 64)),
			finite(m.UsedPercent, 1), finite(m.FreeGB, 2), finite(m.TotalGB, 2), inodes})
		n["mounts"]++
	}
	for _, u := range capped(rep.Units, hostMaxUnits) {
		unit, state := clip(u.Unit, 256), clip(u.State, 32)
		if unit == "" || state == "" {
			continue
		}
		rows.add(hostUnitTable, []any{at, host, unit, state, otlpNullStr(clip(u.Sub, 32))})
		n["units"]++
	}
	for _, e := range capped(rep.Events, hostMaxEvents) {
		if !hostEventKinds[e.Kind] {
			continue
		}
		count := max(e.Count, 1)
		detail := clip(e.Detail, 1000)
		if e.Kind == "updates_pending" && detail == "" {
			detail = strconv.Itoa(e.Security) + " security"
		}
		rows.add(securityTable, []any{at, "host", e.Kind, hostSeverity(e), host, service,
			otlpNullStr(clip(e.Actor, 128)), otlpNullStr(clip(e.IP, 64)), otlpNullStr(clip(e.Target, 300)),
			otlpNullStr(detail), int64(count)})
		n["events"]++
	}
	containerRows(rep, at, rows, n)
	return rows, n
}

// hostSeverity grades an agent finding.
func hostSeverity(e agentapi.Event) string {
	switch e.Kind {
	case "ssh_failed":
		if e.Count >= 10 {
			return "warning"
		}
	case "port_opened", "container_privileged":
		return "warning"
	case "updates_pending":
		if e.Security > 0 {
			return "warning"
		}
	case "ssh_key_added":
		if e.Actor == "root" {
			return "critical"
		}
		return "warning"
	case "file_changed":
		// A preload library, sudo rights or a new UID 0 account is how an
		// intruder stays.
		if e.Target == "/etc/ld.so.preload" || strings.HasPrefix(e.Target, "/etc/sudoers") || strings.Contains(e.Detail, "new UID 0") {
			return "critical"
		}
		return "warning"
	case "suspicious_process":
		return "critical"
	}
	return "info"
}

// hostValue is one system_metrics value: an integer for integer columns,
// nil when missing or not finite.
func hostValue(col string, values map[string]float64) any {
	v, ok := values[col]
	if !ok || math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	if hostIntCols[col] {
		return int64(math.Round(v))
	}
	return v
}

func finite(v float64, prec int) any {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	p := math.Pow(10, float64(prec))
	return math.Round(v*p) / p
}

func capped[T any](xs []T, n int) []T {
	if len(xs) > n {
		return xs[:n]
	}
	return xs
}

// hostBootTime is the boot time stored for host, "" when the host is new.
func (s *Server) hostBootTime(ctx context.Context, host string) (string, error) {
	res, err := s.db.Query(ctx, `SELECT boot_time FROM host_inventory WHERE host = ?`, host)
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		return "", nil
	}
	return toString(res.Rows[0][0]), nil
}

// rebootEvent is a security_events row when the host booted again since its
// previous report. Boot times read from the kernel can move by a second or
// two, so only a later boot by more than a minute counts.
func rebootEvent(rep *agentapi.Report, prev, at string) []any {
	if prev == "" {
		return nil
	}
	was, err1 := time.Parse(tsLayout, prev)
	now, err2 := time.Parse(time.RFC3339Nano, rep.Inventory.BootTime)
	if err1 != nil || err2 != nil || now.Sub(was) < time.Minute {
		return nil
	}
	when := now.UTC().Format(tsLayout)
	if when > at {
		when = at
	}
	return []any{when, "host", "reboot", "info", rep.Host, otlpNullStr(clip(rep.Service, 128)), nil, nil, nil,
		"booted at " + now.UTC().Format(time.RFC3339) + "; previous boot " + was.Format(time.RFC3339), int64(1)}
}

// upsertHost replaces the host's inventory row, keeping first_seen.
func (s *Server) upsertHost(ctx context.Context, rep *agentapi.Report, at string) error {
	inv := rep.Inventory
	boot := any(nil)
	if t, err := time.Parse(time.RFC3339Nano, inv.BootTime); err == nil {
		boot = t.UTC().Format(tsLayout)
	}
	cols := []string{"host", "service", "os", "kernel", "arch", "cpu_count", "mem_total_mb", "boot_time", "agent_version", "ip", "first_seen", "last_seen"}
	args := []any{rep.Host, otlpNullStr(clip(rep.Service, 128)), otlpNullStr(clip(inv.OS, 128)), otlpNullStr(clip(inv.Kernel, 128)),
		otlpNullStr(clip(inv.Arch, 32)), nullInt(inv.CPUCount), finite(inv.MemTotalMB, 1), boot,
		otlpNullStr(clip(inv.AgentVersion, 64)), otlpNullStr(clip(inv.IP, 64)), at, at}
	ph := "?"
	if s.cfg.DatabaseURL == "" || strings.HasPrefix(strings.ToLower(s.cfg.DatabaseURL), "d1:") {
		ph = "NULLIF(?, '')" // D1 sends every parameter as a string
	}
	values := strings.TrimSuffix(strings.Repeat(ph+", ", len(cols)), ", ")
	var set []string
	for _, c := range cols[1:] {
		switch {
		case c == "first_seen":
		case s.q.d == sdk.MySQL:
			set = append(set, c+" = VALUES("+c+")")
		default:
			set = append(set, c+" = excluded."+c)
		}
	}
	stmt := "INSERT INTO host_inventory (" + strings.Join(cols, ", ") + ") VALUES (" + values + ")"
	if s.q.d == sdk.MySQL {
		stmt += " ON DUPLICATE KEY UPDATE " + strings.Join(set, ", ")
	} else {
		stmt += " ON CONFLICT (host) DO UPDATE SET " + strings.Join(set, ", ")
	}
	return s.db.Exec(ctx, stmt, args...)
}

func nullInt(n int) any {
	if n <= 0 {
		return nil
	}
	return int64(n)
}
