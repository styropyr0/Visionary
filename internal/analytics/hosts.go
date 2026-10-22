package analytics

import (
	"context"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/styropyr0/visionary/internal/agentapi"
	sdk "github.com/styropyr0/visionary/sdks/go"
)

// Host monitoring: the standalone agent (cmd/visionary-agent) posts one
// report a minute to POST /api/agent/v1/report (hosts_ingest.go). Its samples
// go to system_metrics with sdk = 'agent', so the System view and the alert
// rules see agent hosts like any instance; inventory, snapshots and security
// findings go to the tables of hosts_schema.go, containers too. The System
// view reads the machine through GET /api/system/machines[/{host}]
// (hosts_api.go), the Containers view its containers through
// GET /api/containers/... (hosts_containers.go), and the Security page the
// findings.

// hostsState is the feature's runtime state, hung off the Server like rum's.
type hostsState struct {
	inflight chan struct{}
	limiter  *rate.Limiter

	mu      sync.Mutex
	reportS map[string]float64 // each host's report interval, from its reports
}

const (
	hostsMaxInFlight = 8
	// hostDefaultReport is the agent's default report interval, assumed for
	// hosts that have not reported since the server started.
	hostDefaultReport = 60.0
	// hostForgetDays drops hosts from the inventory once they are this long
	// silent.
	hostForgetDays = 30
)

var hostsStates sync.Map // *Server -> *hostsState

func (s *Server) hosts() *hostsState {
	if st, ok := hostsStates.Load(s); ok {
		return st.(*hostsState)
	}
	st := &hostsState{
		inflight: make(chan struct{}, hostsMaxInFlight),
		limiter:  rate.NewLimiter(rate.Limit(otlpEnvInt("VISIONARY_AGENT_RATE", 50)), 100),
		reportS:  map[string]float64{},
	}
	actual, _ := hostsStates.LoadOrStore(s, st)
	return actual.(*hostsState)
}

// reportInterval is how often host reports, in seconds.
func (h *hostsState) reportInterval(host string) float64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if v, ok := h.reportS[host]; ok {
		return v
	}
	return hostDefaultReport
}

func (h *hostsState) noteInterval(host string, sec float64) {
	if sec < 5 || sec > 3600 {
		sec = hostDefaultReport
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.reportS) > 10000 {
		h.reportS = map[string]float64{}
	}
	h.reportS[host] = sec
}

func init() {
	// The analyst may read the host tables. security_events is shared with
	// the server's own detectors.
	for _, t := range []string{"HOST_INVENTORY", "HOST_PROCESSES", "HOST_MOUNTS", "HOST_UNITS", "SECURITY_EVENTS", "HOST_CONTAINERS", "CONTAINER_METRICS", "CONTAINER_EVENTS"} {
		allowedTables[t] = true
	}
}

func (s *Server) hostsRoutes(mux *http.ServeMux) {
	// Public: the agent authenticates with a server ingest key, like OTLP.
	mux.HandleFunc("POST "+agentapi.Path, s.handleAgentReport)
	mux.Handle("GET /api/system/machines", s.authed(s.handleMachines))
	mux.Handle("GET /api/system/machines/{host}", s.authed(s.handleMachine))
	s.containerRoutes(mux)
}

func (s *Server) hostsStart(ctx context.Context) {
	go s.hostsRetention(ctx)
}

func (s *Server) hostsPrompt() string {
	return hostsPromptText + "\n" + containersPrompt
}

const hostsPromptText = `Hosts: machines watched by the standalone host agent (cmd/visionary-agent). Their samples are system_metrics rows with sdk = 'agent', instance = the host name and runtime = the OS (e.g. 'Ubuntu 24.04 (linux 6.8.0)'); process, runtime, database and HTTP columns are NULL there, and uptime_s is the host's uptime.
Table host_inventory: one row per host. host (= system_metrics.instance), service, os, kernel, arch, cpu_count, mem_total_mb, boot_time, agent_version, ip, first_seen, last_seen (the last report).
Snapshot tables, one set of rows per host per report (about a minute); for the current state use each host's latest created_at:
- host_processes: created_at, host, pid, name, username, cpu_percent (percent of one core), rss_mb, cmdline. The top processes by CPU and by memory.
- host_mounts: created_at, host, mount, device, fstype, used_percent, free_gb, total_gb, inodes_percent.
- host_units: created_at, host, unit, state ('active', 'inactive', 'failed', ...), sub. Failed systemd services plus the watched ones.
Table security_events: created_at, source ('host' from the agent), kind, severity ('info', 'warning', 'critical'), host, service, actor, ip, target, detail, count (occurrences one row stands for). Host kinds: ssh_failed (actor = user tried, ip), ssh_login, sudo (target = command), port_opened / port_closed (target like 'tcp 0.0.0.0:5432 postgres'), updates_pending (count = updates, detail = 'N security'), reboot. Sum count, not rows, for attempt totals.`

// hostsRetention forgets hosts silent for 30 days, 90 s after start and
// then every 6 hours. Their rows follow Settings → Retention (retentionLoop).
func (s *Server) hostsRetention(ctx context.Context) {
	timer := time.NewTimer(90 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		cutoff := time.Now().UTC().AddDate(0, 0, -hostForgetDays).Format(tsLayout)
		cctx, cancel := context.WithTimeout(ctx, time.Minute)
		if err := s.db.Exec(cctx, `DELETE FROM host_inventory WHERE last_seen < ?`, cutoff); err != nil && ctx.Err() == nil && !missingTable(err, "host_inventory") {
			s.logger.Warn("host_inventory retention", "err", err)
		}
		cancel()
		timer.Reset(6 * time.Hour)
	}
}

// purgeOld deletes rows of table older than days, 5000 at a time like the
// client_events retention. where narrows the rows further; it is a constant
// of this package, never input.
func (s *Server) purgeOld(ctx context.Context, table, where string, days int) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error(table+" retention panicked", "panic", r)
		}
	}()
	cutoff := time.Now().UTC().AddDate(0, 0, -days).Format(tsLayout)
	cond := "created_at < ?"
	if where != "" {
		cond += " AND " + where
	}
	stmt := `DELETE FROM ` + table + ` WHERE id IN (SELECT id FROM ` + table + ` WHERE ` + cond + ` LIMIT 5000)`
	if s.q.d == sdk.MySQL {
		stmt = `DELETE FROM ` + table + ` WHERE ` + cond + ` ORDER BY id LIMIT 5000`
	}
	deleted := int64(0)
	for round := 0; round < 200; round++ {
		// The database layer does not report affected rows, so count first
		// (stopping at 5001) to know whether another round is needed.
		res, err := s.db.Query(ctx, `SELECT COUNT(*) FROM (SELECT id FROM `+table+` WHERE `+cond+` LIMIT 5001) AS old`, cutoff)
		if err == nil {
			var n int64
			if len(res.Rows) > 0 && len(res.Rows[0]) > 0 {
				n = toInt(res.Rows[0][0])
			}
			if n == 0 {
				break
			}
			if err = s.db.Exec(ctx, stmt, cutoff); err == nil {
				deleted += min(n, 5000)
				if n <= 5000 {
					break
				}
				continue
			}
		}
		if ctx.Err() == nil && !missingTable(err, table) {
			s.logger.Warn(table+" retention", "err", err)
		}
		return
	}
	if deleted > 0 {
		s.logger.Info(table+" retention", "deleted", deleted, "older_than_days", days)
	}
}
