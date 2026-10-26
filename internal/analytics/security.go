package analytics

import (
	"context"
	"net/http"
	"net/netip"
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
	ignore           []netip.Prefix

	otherAccountFails int // failed sign-ins as another account from a signed-in browser
	portalFailures    int // failed Visionary sign-ins from one IP

	// The slow pass: a longer window, run every slowEvery.
	slowWindow     time.Duration
	slowFailures   int // failed logins from one IP
	slowIPs        int // distinct IPs failing on one account
	distributedIPs int // distinct IPs failing on login routes, each under bruteforce

	userIPs        int           // distinct IPs for one user in a window
	sensitiveLikes []string      // routes whose changes matter after a risky sign-in
	riskyWindow    time.Duration // how long after a risky sign-in changes are flagged

	enumIDs       int // distinct records read through one route
	idorIDs       int // distinct records refused through one route
	injectionHits int // injection-looking requests from one IP
	routeLimits   []routeLimit
	signupLikes   []string
	signupMax     int
	otpLikes      []string
	otpMax        int
}

// defaultLoginWords match login-like routes when the Login routes setting is
// empty; the other word lists do the same for their settings.
var (
	defaultLoginWords     = []string{"login", "signin", "sign-in", "auth", "token", "otp", "password", "session"}
	defaultSensitiveWords = []string{"password", "email", "phone", "mfa", "2fa", "totp", "recovery", "api-key", "apikey", "payout", "bank", "withdraw"}
	defaultSignupWords    = []string{"signup", "sign-up", "register", "create-account"}
	defaultOTPWords       = []string{"otp", "send-code", "verify", "magic-link", "resend", "sms"}
)

// securitySettings reads Settings → Server → Security detectors.
func (s *Server) securitySettings() securitySettings {
	st := securitySettings{
		enabled:          s.confBool("security"),
		interval:         s.confDuration("security_interval"),
		window:           s.confDuration("security_window"),
		dedupe:           s.confDuration("security_dedupe"),
		bruteforce:       max(s.confInt("security_bruteforce"), 1),
		stuffingAccounts: max(s.confInt("security_stuffing_accounts"), 1),
		targetedIPs:      max(s.confInt("security_targeted_ips"), 1),
		scannerHits:      max(s.confInt("security_scanner_hits"), 1),
		rateMin:          max(s.confInt("security_rate_min"), 1),
		rateFactor:       max(s.confFloat("security_rate_factor"), 1),
		forbidden:        max(s.confInt("security_forbidden"), 1),

		otherAccountFails: max(s.confInt("security_other_account_fails"), 1),
		portalFailures:    max(s.confInt("security_portal_failures"), 1),
		slowWindow:        s.confDuration("security_slow_window"),
		slowFailures:      max(s.confInt("security_slow_failures"), 1),
		slowIPs:           max(s.confInt("security_slow_ips"), 1),
		distributedIPs:    max(s.confInt("security_distributed_ips"), 2),
		userIPs:           max(s.confInt("security_user_ips"), 2),
		riskyWindow:       s.confDuration("security_risky_window"),
		enumIDs:           max(s.confInt("security_enum_ids"), 2),
		idorIDs:           max(s.confInt("security_idor_ids"), 2),
		injectionHits:     max(s.confInt("security_injection_hits"), 1),
		signupMax:         max(s.confInt("security_signup_max"), 1),
		otpMax:            max(s.confInt("security_otp_max"), 1),
	}
	st.interval = max(st.interval, 10*time.Second)
	st.window = min(max(st.window, time.Minute), time.Hour)
	st.dedupe = max(st.dedupe, st.interval)
	st.slowWindow = min(max(st.slowWindow, time.Hour), 7*24*time.Hour)
	st.riskyWindow = min(max(st.riskyWindow, 5*time.Minute), 24*time.Hour)
	st.loginLikes = loginLikes(s.conf("security_login_routes"))
	st.sensitiveLikes = wordLikes(s.conf("security_sensitive_routes"), defaultSensitiveWords)
	st.signupLikes = wordLikes(s.conf("security_signup_routes"), defaultSignupWords)
	st.otpLikes = wordLikes(s.conf("security_otp_routes"), defaultOTPWords)
	st.routeLimits = parseRouteLimits(s.conf("security_route_limits"))
	for _, v := range strings.Split(s.conf("security_ignore_ips"), ",") {
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
func loginLikes(env string) []string { return wordLikes(env, defaultLoginWords) }

// wordLikes is loginLikes for any list: the globs in env, else "contains"
// patterns for the default words. A plain word (no / and no wildcard)
// matches anywhere in the route, as the default words do.
func wordLikes(env string, words []string) []string {
	var out []string
	for _, g := range strings.Split(env, ",") {
		g = strings.ToLower(strings.TrimSpace(g))
		if g == "" || strings.ContainsAny(g, `'\"`) || len(g) > 200 {
			continue
		}
		like := strings.NewReplacer("*", "%", "?", "_").Replace(likeEscaper.Replace(g))
		if !strings.ContainsAny(g, "/*?") {
			like = "%" + like + "%"
		}
		out = append(out, like)
	}
	if len(out) == 0 {
		for _, w := range words {
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
	slowAt   time.Time // when the slow pass last ran
}

var securityStates sync.Map // *Server -> *securityState

func (s *Server) security() *securityState {
	if v, ok := securityStates.Load(s); ok {
		return v.(*securityState)
	}
	st := s.securitySettings()
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
	s.securityTriageRoutes(mux)
}

func (s *Server) securityStart(ctx context.Context) {
	sec := s.security()
	go s.auditRoleMapping(ctx)
	go s.intelLoop(ctx)
	if !sec.st.enabled {
		s.logger.Info("security detectors disabled in Settings → Server; the Security page still shows host events and the audit log")
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

func (s *Server) securityPrompt() string {
	return `Table security_events: one row per security finding. Use it for "was there an attack", brute force, scanners and host security questions.
- id INTEGER, created_at TEXT (same format as api_logs; when the finding was first seen)
- source TEXT: 'api' (Visionary's detectors over api_logs), 'host' (the Visionary host agent on a VM, and ssh_bruteforce graded from its events) or 'audit' (Visionary's own audit log)
- kind TEXT. From the API: auth_bruteforce (many failed logins from one IP), credential_stuffing (one IP failing against many accounts), account_targeted (many IPs failing against one account; critical when one of them then signed in), login_as_other (a browser signed in as one user trying to sign in as another account; actor = the signed-in user, target = the account tried), auth_bruteforce_slow / account_targeted_slow / distributed_login_attack (the same over a long window, and many IPs each failing a little), new_login_location, new_login_device, impossible_travel, listed_ip_login (a sign-in from a blocklisted IP), session_spread (one account used from many places at once), risky_account_change (a password, email, MFA or payout change soon after a risky sign-in), enumeration (many records read through one route with an ID), idor_probe (many records refused through such a route), injection_attempt (SQL, script, template, command, JNDI or traversal patterns), route_rate_limit, signup_abuse, otp_abuse, scanner (probe paths such as /.env or /wp-admin, or a scanner user agent), rate_abuse (an IP or user far above its usual request rate), forbidden_spike (a jump in 403s for one user or role). From Visionary's audit log: portal_bruteforce, portal_account_targeted, and portal_* changes (secret_viewed, account_created, account_changed, roles_changed, signin_changed, api_key_created, data_deleted, ip_blocked, two_step_reset). From hosts: ssh_failed, ssh_login, ssh_bruteforce, sudo, port_opened, port_closed, updates_pending (count = updates waiting), reboot, ssh_key_added, ssh_key_removed, file_changed, suspicious_process, container_privileged, container_root, container_new_image
- status TEXT: NULL or '' while open, else acknowledged, resolved or false_positive; note TEXT is what whoever triaged it wrote
- severity TEXT: info, warning or critical
- host TEXT (host rows), service TEXT, actor TEXT (the user or account involved), ip TEXT (values hidden from you; join api_logs.ip = security_events.ip to see an IP's requests), target TEXT (the route, path, port or command involved)
- detail TEXT: a small JSON object with the counts behind the finding and a summary sentence (and place, country, network and listed_in when known); never request bodies or secrets
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
func (st *securitySettings) loginCond(q sqlDialect) string { return routeCond(q, st.loginLikes) }

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

// loginAccountExpr names the account a sign-in was for: the one the app
// recorded with LoginAttempt, else the request's user.
func loginAccountExpr(q sqlDialect) string {
	return "COALESCE(NULLIF(login_account, ''), NULLIF(user_handle, ''), " + q.text("user_id") + ")"
}

// hasAccount is a request that names an account either way.
const hasAccount = "(user_id IS NOT NULL OR (user_handle IS NOT NULL AND user_handle <> '') OR (login_account IS NOT NULL AND login_account <> ''))"

// routeCond matches requests whose route or path fits one of likes.
func routeCond(q sqlDialect, likes []string) string {
	return "(" + likeAny(q, "LOWER(COALESCE(route, ''))", likes) + " OR " + likeAny(q, "LOWER(path)", likes) + ")"
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
