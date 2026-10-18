package analytics

import (
	"context"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/styropyr0/visionary/sdks/go"
)

// Security monitoring over data Visionary already has. The pieces:
//
//	security.go         settings, routes, the background job and SQL conditions
//	security_detect.go  detectors that read api_logs and write security_events
//	security_panels.go  the Security page's fixed panels, per dialect
//	security_api.go     findings, a finding's related requests, the audit feed
//	security_alerts.go  the security_events and failed_logins alert metrics
//	security_digest.go  the security part of the weekly digest
//
// security_events itself is created in hosts_schema.go: the host agent writes
// rows with source 'host', the detectors here write source 'api'. Visionary's
// own audit log is read in place (never copied).

// securitySettings are the detectors' environment knobs.
type securitySettings struct {
	enabled  bool
	interval time.Duration // how often the detectors run
	window   time.Duration // the sliding window each run looks at
	dedupe   time.Duration // one row per kind and key at most this often
	// loginLikes are LIKE patterns (lower case, "!" escapes) for login routes.
	loginLikes []string

	bruteforce       int     // failed logins from one IP per window
	stuffingAccounts int     // distinct accounts one IP failed against
	targetedIPs      int     // distinct IPs failing against one account
	scannerHits      int     // probe-path requests from one IP
	rateMin          int     // requests per minute before rate abuse is considered
	rateFactor       float64 // times the key's own baseline
	forbidden        int     // 403s for one user or role per window
	retentionDays    int     // 0 keeps findings forever
	ignore           []netip.Prefix
}

// defaultLoginWords match login-like routes when VISIONARY_SECURITY_LOGIN_ROUTES
// is not set.
var defaultLoginWords = []string{"login", "signin", "sign-in", "auth", "token", "otp", "password", "session"}

func securitySettingsFromEnv() securitySettings {
	st := securitySettings{
		enabled:          getBool("VISIONARY_SECURITY", true),
		interval:         getDuration("VISIONARY_SECURITY_INTERVAL", time.Minute),
		window:           getDuration("VISIONARY_SECURITY_WINDOW", 5*time.Minute),
		dedupe:           getDuration("VISIONARY_SECURITY_DEDUPE", 10*time.Minute),
		bruteforce:       getInt("VISIONARY_SECURITY_BRUTEFORCE", 10),
		stuffingAccounts: getInt("VISIONARY_SECURITY_STUFFING_ACCOUNTS", 5),
		targetedIPs:      getInt("VISIONARY_SECURITY_TARGETED_IPS", 5),
		scannerHits:      getInt("VISIONARY_SECURITY_SCANNER_HITS", 3),
		rateMin:          getInt("VISIONARY_SECURITY_RATE_MIN", 120),
		rateFactor:       5,
		forbidden:        getInt("VISIONARY_SECURITY_FORBIDDEN", 20),
		retentionDays:    180,
	}
	if v, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv("VISIONARY_SECURITY_RATE_FACTOR")), 64); err == nil && v >= 1 {
		st.rateFactor = v
	}
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("VISIONARY_SECURITY_RETENTION_DAYS"))); err == nil && v >= 0 {
		st.retentionDays = v
	}
	st.interval = max(st.interval, 10*time.Second)
	st.window = min(max(st.window, time.Minute), time.Hour)
	st.dedupe = max(st.dedupe, st.interval)
	st.loginLikes = loginLikes(os.Getenv("VISIONARY_SECURITY_LOGIN_ROUTES"))
	for _, v := range strings.Split(os.Getenv("VISIONARY_SECURITY_IGNORE_IPS"), ",") {
		if v = strings.TrimSpace(v); v == "" {
			continue
		}
		if p, err := netip.ParsePrefix(v); err == nil {
			st.ignore = append(st.ignore, p.Masked())
		} else if a, err := netip.ParseAddr(v); err == nil {
			st.ignore = append(st.ignore, netip.PrefixFrom(a, a.BitLen()))
		}
	}
	return st
}

// loginLikes turns the comma list of path globs (* any run, ? one character)
// into LIKE patterns. Globs with quotes or backslashes are dropped: the
// patterns are inlined into SQL.
func loginLikes(env string) []string {
	var out []string
	for _, g := range strings.Split(env, ",") {
		g = strings.ToLower(strings.TrimSpace(g))
		if g == "" || strings.ContainsAny(g, `'\"`) || len(g) > 200 {
			continue
		}
		like := strings.NewReplacer("*", "%", "?", "_").Replace(likeEscaper.Replace(g))
		out = append(out, like)
	}
	if len(out) == 0 {
		for _, w := range defaultLoginWords {
			out = append(out, "%"+likeEscaper.Replace(w)+"%")
		}
	}
	return out
}

// ignored reports an address the operator excluded from detection.
func (st *securitySettings) ignored(ip string) bool {
	a, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return false
	}
	for _, p := range st.ignore {
		if p.Contains(a.Unmap()) {
			return true
		}
	}
	return false
}

// securityState is the feature's in-memory state, kept beside the server
// like the other features'.
type securityState struct {
	st     securitySettings
	panels map[string]string

	mu       sync.Mutex
	cursor   time.Time            // end of the previous run's window
	runs     int                  // completed runs since start
	active   map[string]bool      // keys that were breaching on the previous run
	baseline map[string][]float64 // recent per-window counts per key
	lastErr  string
	lastRun  time.Time
}

var securityStates sync.Map // *Server -> *securityState

func (s *Server) security() *securityState {
	if v, ok := securityStates.Load(s); ok {
		return v.(*securityState)
	}
	st := securitySettingsFromEnv()
	v, _ := securityStates.LoadOrStore(s, &securityState{
		st: st, panels: securityPanels(s.q, &st),
		active: map[string]bool{}, baseline: map[string][]float64{},
	})
	return v.(*securityState)
}

func (s *Server) securityRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/security/meta", s.authed(s.handleSecurityMeta))
	mux.Handle("GET /api/security/findings", s.authed(s.handleSecurityFindings))
	mux.Handle("GET /api/security/findings/{id}", s.authed(s.handleSecurityFinding))
	mux.Handle("GET /api/security/audit", s.authed(s.handleSecurityAudit))
	mux.Handle("GET /api/security/panels/{panel}", s.authed(s.handleSecurityPanel))
}

func (s *Server) securityStart(ctx context.Context) {
	sec := s.security()
	go s.auditRoleMapping(ctx)
	go s.securityRetention(ctx)
	if !sec.st.enabled {
		s.logger.Info("security detectors disabled (VISIONARY_SECURITY=false); the Security page still shows host events and the audit log")
		return
	}
	go s.securityLoop(ctx)
}

func (s *Server) securityLoop(ctx context.Context) {
	sec := s.security()
	sec.mu.Lock()
	// The first run counts only the last interval as new, so findings already
	// written before a restart are not counted twice.
	sec.cursor = time.Now().UTC().Add(-securityLag - sec.st.interval)
	sec.mu.Unlock()
	tick := time.NewTicker(sec.st.interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.runDetectors(ctx, time.Now().UTC())
		}
	}
}

// securityRetention deletes findings older than VISIONARY_SECURITY_RETENTION_DAYS
// (all sources), at startup and every six hours.
func (s *Server) securityRetention(ctx context.Context) {
	days := s.security().st.retentionDays
	if days <= 0 {
		return
	}
	tick := time.NewTicker(6 * time.Hour)
	defer tick.Stop()
	for {
		cutoff := time.Now().UTC().AddDate(0, 0, -days).Format(tsLayout)
		cctx, cancel := context.WithTimeout(ctx, time.Minute)
		if err := s.db.Exec(cctx, `DELETE FROM security_events WHERE created_at < ?`, cutoff); err != nil && ctx.Err() == nil && !missingTable(err, "security_events") {
			s.logger.Warn("security retention", "err", err)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (s *Server) securityPrompt() string {
	return `Table security_events: one row per security finding. Use it for "was there an attack", brute force, scanners and host security questions.
- id INTEGER, created_at TEXT (same format as api_logs; when the finding was first seen)
- source TEXT: 'api' (Visionary's detectors over api_logs) or 'host' (the Visionary host agent on a VM)
- kind TEXT. From the API: auth_bruteforce (many failed logins from one IP), credential_stuffing (one IP failing against many accounts), account_targeted (many IPs failing against one account), scanner (probe paths such as /.env or /wp-admin, or a scanner user agent), rate_abuse (an IP or user far above its usual request rate), forbidden_spike (a jump in 403s for one user or role). From hosts: ssh_failed, ssh_login, sudo, port_opened, port_closed, updates_pending (count = updates waiting), reboot
- severity TEXT: info, warning or critical
- host TEXT (host rows), service TEXT, actor TEXT (the user or account involved), ip TEXT (values hidden from you; join api_logs.ip = security_events.ip to see an IP's requests), target TEXT (the route, path, port or command involved)
- detail TEXT: a small JSON object with the counts behind the finding and a summary sentence; never request bodies or secrets
- count INTEGER: occurrences the row stands for. API findings are written at most once per kind and key every ` + fmtSpan(s.security().st.dedupe) + `, and count grows while the activity goes on: total occurrences is SUM(count), not COUNT(*).`
}

// securityLag keeps the newest seconds out of a run: SDKs write api_logs in
// batches, so the very latest rows may not have arrived yet.
const securityLag = 30 * time.Second

// sqlString quotes a value we generated (patterns, kinds) for inlining.
func (q sqlDialect) sqlString(v string) string {
	v = strings.ReplaceAll(v, "'", "''")
	if q.d == sdk.MySQL {
		v = strings.ReplaceAll(v, `\`, `\\`)
	}
	return "'" + v + "'"
}

func likeAny(q sqlDialect, expr string, likes []string) string {
	parts := make([]string, len(likes))
	for i, l := range likes {
		parts[i] = expr + " LIKE " + q.sqlString(l) + " ESCAPE '!'"
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}

// loginCond matches requests to login-like routes, by route pattern or path.
func (st *securitySettings) loginCond(q sqlDialect) string {
	return "(" + likeAny(q, "LOWER(COALESCE(route, ''))", st.loginLikes) + " OR " + likeAny(q, "LOWER(path)", st.loginLikes) + ")"
}

// authFailCond is a rejected sign-in: 401 or 403, or a 4xx whose error key
// says the credentials or code were wrong (APIs that answer bad passwords
// with 400 or 422).
func authFailCond(q sqlDialect) string {
	keys := []string{"%credential%", "%password%", "%unauthori%", "%invalid!_code%", "%invalid!_otp%", "%wrong%", "%login!_failed%", "%auth!_failed%", "%bad!_login%"}
	return "(status IN (401, 403) OR (status >= 400 AND status < 500 AND " +
		likeAny(q, "LOWER(COALESCE(error_key, error_code, ''))", keys) + "))"
}

// probePaths are paths only scanners ask for on an API.
var probePaths = []string{
	"%/.env%", "%/.git/%", "%/.git", "%/.svn%", "%/.hg/%", "%/.aws%", "%/.ssh%", "%/.htaccess%", "%/.htpasswd%",
	"%/.ds!_store%", "%/wp-admin%", "%/wp-login.php%", "%/wp-content%", "%/wp-includes%", "%/xmlrpc.php%",
	"%/phpmyadmin%", "%/pma/%", "%/phpinfo%", "%/actuator%", "%/cgi-bin%", "%/server-status%", "%/server-info%",
	"%/vendor/phpunit%", "%/etc/passwd%", "%/proc/self%", "%/boaform%", "%/hnap1%", "%/web.config%",
	"%/config.php%", "%/.vscode%", "%/.idea/%", "%/druid/%", "%/solr/admin%", "%/manager/html%",
	"%/telescope%", "%/!_ignition%", "%/autodiscover%", "%/owa/%",
	"%../%", "%..!%2f%", "%!%2e!%2e%", `%..\%`,
}

// scannerAgents are tools that announce themselves in the user agent.
var scannerAgents = []string{
	"sqlmap", "nikto", "nuclei", "masscan", "zgrab", "gobuster", "dirbuster", "dirb/", "wpscan", "nmap", "acunetix",
	"nessus", "openvas", "ffuf", "feroxbuster", "wfuzz", "hydra", "whatweb", "arachni", "w3af", "netsparker",
	"jaeles", "zmeu", "morfeus", "skipfish", "joomscan", "commix", "xsstrike",
}

// probeCond matches probe paths. The patterns use "!" escapes; the backslash
// in the last one is literal (sqlString doubles it for MySQL).
func probeCond(q sqlDialect) string { return likeAny(q, "LOWER(path)", probePaths) }

func scannerAgentCond(q sqlDialect) string {
	likes := make([]string, len(scannerAgents))
	for i, a := range scannerAgents {
		likes[i] = "%" + likeEscaper.Replace(a) + "%"
	}
	return likeAny(q, "LOWER(COALESCE(user_agent, ''))", likes)
}

// accountExpr names the account a request was for: the handle, else the id.
func accountExpr(q sqlDialect) string {
	return "COALESCE(NULLIF(user_handle, ''), " + q.text("user_id") + ")"
}

func (s *Server) handleSecurityMeta(w http.ResponseWriter, r *http.Request) {
	sec := s.security()
	sec.mu.Lock()
	lastRun, lastErr, runs := sec.lastRun, sec.lastErr, sec.runs
	sec.mu.Unlock()
	hosts := int64(0)
	if res, err := s.db.Query(r.Context(), `SELECT COUNT(*) FROM host_inventory`); err == nil && len(res.Rows) == 1 {
		hosts = toInt(res.Rows[0][0])
	}
	st := sec.st
	last := ""
	if !lastRun.IsZero() {
		last = lastRun.Format(tsLayout)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":  st.enabled,
		"interval": fmtSpan(st.interval),
		"window":   fmtSpan(st.window),
		"dedupe":   fmtSpan(st.dedupe),
		"last_run": last,
		"runs":     runs,
		"error":    lastErr,
		"hosts":    hosts,
		"is_admin": s.can(r, "admin"),
		"thresholds": map[string]any{
			"bruteforce": st.bruteforce, "stuffing_accounts": st.stuffingAccounts, "targeted_ips": st.targetedIPs,
			"scanner_hits": st.scannerHits, "rate_min": st.rateMin, "rate_factor": st.rateFactor, "forbidden": st.forbidden,
		},
	})
}
