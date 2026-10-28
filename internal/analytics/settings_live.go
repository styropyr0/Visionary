package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/styropyr0/visionary/sdks/go/logrules"
)

// The settings the server itself runs on (thresholds, retention, the status
// page), kept in visionary_settings and edited in Settings. Readers get them
// from s.live(), refreshed every 30 seconds and right after a change, so
// several servers on one database agree.

// apdexRule is the Apdex threshold T: a default, and overrides for routes
// that are slow by design (exports, uploads).
type apdexRule struct {
	DefaultMs int          `json:"default_ms"`
	Routes    []routeApdex `json:"routes"`
}

type routeApdex struct {
	Method string `json:"method"`
	Route  string `json:"route"`
	Ms     int    `json:"ms"`
}

// sql is T for one api_logs row: a number, or a CASE on its method and route
// when some routes have their own.
func (a apdexRule) sql() string {
	if len(a.Routes) == 0 {
		return strconv.Itoa(a.DefaultMs)
	}
	var b strings.Builder
	b.WriteString("(CASE")
	for _, r := range a.Routes {
		fmt.Fprintf(&b, " WHEN method = %s AND COALESCE(route, path) = %s THEN %d", sqlString(r.Method), sqlString(r.Route), r.Ms)
	}
	fmt.Fprintf(&b, " ELSE %d END)", a.DefaultMs)
	return b.String()
}

// sql4 is 4T, the end of "tolerating".
func (a apdexRule) sql4() string {
	if len(a.Routes) == 0 {
		return strconv.Itoa(4 * a.DefaultMs)
	}
	return "(4 * " + a.sql() + ")"
}

// key tells rules apart, for the caches of SQL built from them.
func (a apdexRule) key() string {
	b, _ := json.Marshal(a)
	return string(b)
}

// retentionKind is one kind of collected data and the tables that hold it.
type retentionKind struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description"`
	DefaultDays int    `json:"default_days"`
	tables      []string
	// where narrows the rows of the tables this kind covers, for a table
	// split between kinds; a constant of this package, never input.
	where string
}

var retentionKinds = []retentionKind{
	{Key: "requests", Label: "Request logs", Description: "Every request with headers and bodies.", DefaultDays: 30, tables: []string{"api_logs"}},
	{Key: "spans", Label: "Calls inside requests", Description: "Queries, outbound calls and custom spans.", DefaultDays: 30, tables: []string{"spans"}},
	{Key: "errors", Label: "Errors", Description: "Captured exceptions and panics.", DefaultDays: 30, tables: []string{"errors"}},
	{Key: "system", Label: "System metrics", Description: "CPU, memory and runtime samples from SDKs and host agents.", DefaultDays: 30, tables: []string{"system_metrics"}},
	{Key: "containers", Label: "Container metrics", Description: "Per-container samples, restarts, kills and health changes.", DefaultDays: 30, tables: []string{"container_metrics", "container_events"}},
	{Key: "hosts", Label: "Host snapshots", Description: "Processes, mounts, services and containers the agent lists.", DefaultDays: 14, tables: []string{"host_processes", "host_mounts", "host_units", "host_containers"}},
	{Key: "profiles", Label: "CPU profiles", Description: "Sampled flame graphs.", DefaultDays: 30, tables: []string{"profiles"}},
	{Key: "logs", Label: "Logs: trace to info", Description: "Application log lines below warn.", DefaultDays: 7, tables: []string{"app_logs"}, where: "severity <= 3"},
	{Key: "logs_important", Label: "Logs: warn and above", Description: "Application log lines at warn, error and fatal.", DefaultDays: 30, tables: []string{"app_logs"}, where: "severity >= 4"},
	{Key: "frontend", Label: "Frontend events", Description: "Web Vitals, page and screen loads, client errors.", DefaultDays: 30, tables: []string{"client_events"}},
	{Key: "uptime", Label: "Uptime results", Description: "Each check's runs and timings.", DefaultDays: 90, tables: []string{"uptime_results"}},
	{Key: "security", Label: "Security findings", Description: "Detector findings and host events.", DefaultDays: 180, tables: []string{"security_events"}},
	{Key: "alerts", Label: "Alert history", Description: "Past firings and resolutions.", DefaultDays: 365, tables: []string{"alert_events", "alert_deliveries"}},
	{Key: "plans", Label: "Query plans", Description: "EXPLAIN results asked for from Queries.", DefaultDays: 30, tables: []string{"explain_requests"}},
	{Key: "releases", Label: "Deploys", Description: "Version starts: the deploy marks on charts.", DefaultDays: 0, tables: []string{"releases"}},
	{Key: "audit", Label: "Audit log", Description: "Who changed what in Visionary.", DefaultDays: 365, tables: []string{"audit_log"}},
	{Key: "rollup_minute", Label: "Per-minute chart summaries", Description: "Pre-aggregated requests and system metrics behind charts of a few days.", DefaultDays: 14},
	{Key: "rollup_hour", Label: "Hourly chart summaries", Description: "Behind charts of weeks and months; they outlive the raw rows.", DefaultDays: 400},
}

// liveSettings are the values in force.
type liveSettings struct {
	Apdex       apdexRule      `json:"apdex"`
	SLOTarget   float64        `json:"slo_target"`
	Retention   map[string]int `json:"retention"` // days by kind; 0 keeps forever
	StatusPage  bool           `json:"status_page"`
	StatusTitle string         `json:"status_title"`
	// Config holds the stored config.* values by key (Settings → Server);
	// never sent as is, since some are secrets.
	Config map[string]string `json:"-"`
	// Logs is Settings → Logs as the SDKs read it (logs.*); LogServer the
	// options only the server uses (logserver.*).
	Logs      *logrules.Settings `json:"-"`
	LogServer *logServerSettings `json:"-"`
}

const (
	settingApdex       = "server.apdex"
	settingSLOTarget   = "server.slo_target"
	settingRetention   = "server.retention."
	settingStatusPage  = "server.status_page"
	settingStatusTitle = "server.status_title"
)

func defaultLiveSettings() *liveSettings {
	l := &liveSettings{Apdex: apdexRule{DefaultMs: 500, Routes: []routeApdex{}}, SLOTarget: 99, Retention: map[string]int{}, Config: map[string]string{}}
	for _, k := range retentionKinds {
		l.Retention[k.Key] = k.DefaultDays
	}
	return l
}

type liveHolder struct{ p atomic.Pointer[liveSettings] }

// live returns the settings in force; the defaults until the first read.
func (s *Server) live() *liveSettings {
	if l := s.liveSet.p.Load(); l != nil {
		return l
	}
	return defaultLiveSettings()
}

func (s *Server) apdex() apdexRule   { return s.live().Apdex }
func (s *Server) sloTarget() float64 { return s.live().SLOTarget }

// retentionDays is how long a kind is kept; 0 keeps it forever.
func (s *Server) retentionDays(kind string) int { return s.live().Retention[kind] }

// refreshLive reads the stored settings over the defaults.
func (s *Server) refreshLive(ctx context.Context) error {
	m, err := s.readSettings(ctx)
	if err != nil {
		return err
	}
	l := defaultLiveSettings()
	if v := m[settingApdex][0]; v != "" {
		var a apdexRule
		if json.Unmarshal([]byte(v), &a) == nil && a.DefaultMs > 0 {
			if a.Routes == nil {
				a.Routes = []routeApdex{}
			}
			l.Apdex = a
		}
	}
	if f, err := strconv.ParseFloat(m[settingSLOTarget][0], 64); err == nil && f > 0 && f < 100 {
		l.SLOTarget = f
	}
	for _, k := range retentionKinds {
		if n, err := strconv.Atoi(m[settingRetention+k.Key][0]); err == nil && n >= 0 {
			l.Retention[k.Key] = n
		}
	}
	l.Logs, l.LogServer = parseLogSettings(m)
	l.StatusPage = m[settingStatusPage][0] == "true"
	l.StatusTitle = m[settingStatusTitle][0]
	for name, v := range m {
		if key, ok := strings.CutPrefix(name, settingConfig); ok {
			l.Config[key] = v[0]
		}
	}
	s.liveSet.p.Store(l)
	s.applySignin(m)
	s.applyConfig()
	return nil
}

// liveStart loads the settings and keeps them fresh.
func (s *Server) liveStart(ctx context.Context) {
	s.loadSessionSecret(ctx)
	if err := s.refreshLive(ctx); err != nil {
		if !missingTable(err, "visionary_settings") {
			s.logger.Warn("reading settings failed; using defaults until it works", "err", err)
		}
		// Sign-in waits for the settings; everything else runs on defaults.
		s.applyConfig()
	}
	go func() {
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				_ = s.refreshLive(ctx)
			}
		}
	}()
	go s.retentionLoop(ctx)
}

// retentionLoop deletes rows past each kind's retention, 90 s after start
// and then every 6 hours, 5000 at a time. Rollups keep their own pace in the
// rollup job.
func (s *Server) retentionLoop(ctx context.Context) {
	timer := time.NewTimer(90 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		for _, k := range retentionKinds {
			days := s.retentionDays(k.Key)
			if days <= 0 {
				continue
			}
			for _, t := range k.tables {
				s.purgeOld(ctx, t, k.where, days)
			}
		}
		timer.Reset(6 * time.Hour)
	}
}

// retentionUsage counts each kind's rows and its oldest one, for Settings.
func (s *Server) retentionUsage(ctx context.Context) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, k := range retentionKinds {
		var rows int64
		oldest := ""
		for _, t := range k.tables {
			q := `SELECT COUNT(*), MIN(created_at) FROM ` + t
			if k.where != "" {
				q += ` WHERE ` + k.where
			}
			res, err := s.db.Query(ctx, q)
			if err != nil || len(res.Rows) == 0 {
				continue
			}
			rows += toInt(res.Rows[0][0])
			if o := toString(res.Rows[0][1]); o != "" && (oldest == "" || o < oldest) {
				oldest = o
			}
		}
		if len(k.tables) > 0 {
			out[k.Key] = map[string]any{"rows": rows, "oldest": oldest}
		}
	}
	return out
}

// validApdexRoutes checks overrides: a method, a route, 1 ms to 10 minutes,
// at most 50, no route twice. They are sorted for a stable key.
func validApdexRoutes(rs []routeApdex) ([]routeApdex, error) {
	if len(rs) > 50 {
		return nil, fmt.Errorf("at most 50 route thresholds")
	}
	seen := map[string]bool{}
	out := make([]routeApdex, 0, len(rs))
	for _, r := range rs {
		r.Method, r.Route = strings.ToUpper(strings.TrimSpace(r.Method)), strings.TrimSpace(r.Route)
		switch {
		case r.Method == "" || len(r.Method) > 10 || strings.ContainsAny(r.Method, " '\"\\;"):
			return nil, fmt.Errorf("each route threshold needs a method such as GET")
		case r.Route == "" || len(r.Route) > 300 || !strings.HasPrefix(r.Route, "/"):
			return nil, fmt.Errorf("routes start with /, e.g. /api/export")
		case r.Ms < 1 || r.Ms > 600000:
			return nil, fmt.Errorf("thresholds are 1 ms to 10 minutes")
		case seen[r.Method+" "+r.Route]:
			return nil, fmt.Errorf("%s %s is listed twice", r.Method, r.Route)
		}
		seen[r.Method+" "+r.Route] = true
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Route+out[i].Method < out[j].Route+out[j].Method })
	return out, nil
}
