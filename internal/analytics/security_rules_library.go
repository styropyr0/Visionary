package analytics

type ruleTemplate struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Severity    string            `json:"severity"`
	Category    string            `json:"category"`
	Tactic      string            `json:"tactic"`
	Technique   string            `json:"technique"`
	Source      string            `json:"source"`
	Filter      map[string]string `json:"filter,omitempty"`
	Logic       string            `json:"logic"`
	GroupBy     []string          `json:"group_by,omitempty"`
	Window      string            `json:"window"`
	Threshold   int               `json:"threshold,omitempty"`
	Distinct    string            `json:"distinct,omitempty"`
	Field       string            `json:"field,omitempty"`
	Learn       string            `json:"learn,omitempty"`
	Then        map[string]string `json:"then,omitempty"`
	Factor      float64           `json:"factor,omitempty"`
	Needs       string            `json:"needs,omitempty"`
}

func (t ruleTemplate) rule() detectionRule {
	return detectionRule{Name: t.Name, Description: t.Description, Template: t.ID, Severity: t.Severity, Tactic: t.Tactic, Technique: t.Technique,
		Source: t.Source, Filter: copyMap(t.Filter), Logic: t.Logic, GroupBy: append([]string(nil), t.GroupBy...), Window: t.Window,
		Threshold: t.Threshold, Distinct: t.Distinct, Field: t.Field, Learn: t.Learn, Then: copyMap(t.Then), Factor: t.Factor}
}

func copyMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func templateByID(id string) (ruleTemplate, bool) {
	for _, t := range ruleTemplates {
		if t.ID == id {
			return t, true
		}
	}
	return ruleTemplate{}, false
}

var (
	failedLogin = map[string]string{"login": "yes", "auth_failed": "yes"}
	// a value with a slash matches the whole path unless it has a *
	traversal    = "*../*,..%2f,%2e%2e,..\\,*/etc/passwd*,*/proc/self*,win.ini,boot.ini"
	sqlInjection = "union%20select,union+select,union select,%27%20or%20,' or ',%27or%27,or%201=1,or 1=1,sleep(,benchmark(,information_schema,pg_sleep,waitfor%20delay,xp_cmdshell"
)

var ruleTemplates = []ruleTemplate{
	{ID: "login_bruteforce", Category: "signin", Name: "Brute force on login", Severity: "warning", Tactic: "TA0006", Technique: "T1110.001",
		Description: "Many failed sign-ins from one IP on login routes.",
		Source:      "requests", Filter: failedLogin, Logic: "threshold", GroupBy: []string{"ip"}, Window: "10m", Threshold: 20},
	{ID: "credential_stuffing", Category: "signin", Name: "Credential stuffing across accounts", Severity: "critical", Tactic: "TA0006", Technique: "T1110.004",
		Description: "One IP failing to sign in as many different accounts: a leaked password list being tried.",
		Source:      "requests", Filter: failedLogin, Logic: "threshold", GroupBy: []string{"ip"}, Distinct: "account", Window: "15m", Threshold: 10,
		Needs: "LoginAttempt in your login handler, so the account tried is known"},
	{ID: "password_spray", Category: "signin", Name: "Account targeted from many IPs", Severity: "warning", Tactic: "TA0006", Technique: "T1110.003",
		Description: "Many addresses failing to sign in as one account.",
		Source:      "requests", Filter: failedLogin, Logic: "threshold", GroupBy: []string{"account"}, Distinct: "ip", Window: "30m", Threshold: 5,
		Needs: "LoginAttempt in your login handler"},
	{ID: "failed_then_success", Category: "signin", Name: "Failed logins, then a success", Severity: "critical", Tactic: "TA0006", Technique: "T1110",
		Description: "An IP fails to sign in several times, then signs in: a guessed password.",
		Source:      "requests", Filter: failedLogin, Logic: "sequence", GroupBy: []string{"ip"}, Window: "30m", Threshold: 5,
		Then: map[string]string{"login": "yes", "auth_failed": "no"}},
	{ID: "new_country_for_user", Category: "signin", Name: "Sign-in from a new country", Severity: "warning", Tactic: "TA0001", Technique: "T1078",
		Description: "A user's requests come from a country they never used in the last 30 days: travel, or a stolen session.",
		Source:      "requests", Filter: map[string]string{"auth_failed": "no"}, Logic: "new_value", GroupBy: []string{"user"}, Field: "country", Window: "1h", Learn: "30d",
		Needs: "a location database in Settings → Server → Threat intelligence"},
	{ID: "admin_new_ip", Category: "visionary", Name: "Visionary admin from a new IP", Severity: "warning", Tactic: "TA0001", Technique: "T1078",
		Description: "A Visionary admin signed in from an address they have not used in 30 days.",
		Source:      "audit", Filter: map[string]string{"action": "auth.sign_in", "role": "admin"}, Logic: "new_value", GroupBy: []string{"user"}, Field: "ip", Window: "1h", Learn: "30d"},
	{ID: "app_user_new_ip_admin_route", Category: "data", Name: "Admin route from a new IP", Severity: "warning", Tactic: "TA0004", Technique: "T1078",
		Description: "A user calls admin routes from an address they have not used for them before.",
		Source:      "requests", Filter: map[string]string{"route": "*admin*", "auth_failed": "no"}, Logic: "new_value", GroupBy: []string{"user"}, Field: "ip", Window: "1h", Learn: "14d"},
	{ID: "auth_error_spike", Category: "signin", Name: "Spike in 401 and 403", Severity: "warning", Tactic: "TA0006", Technique: "T1110",
		Description: "Far more unauthorized and forbidden answers than usual across the API.",
		Source:      "requests", Filter: map[string]string{"status": "401,403"}, Logic: "anomaly", Window: "10m", Threshold: 50, Factor: 4},
	{ID: "forbidden_per_user", Category: "data", Name: "One user hitting 403s", Severity: "warning", Tactic: "TA0007", Technique: "T1069",
		Description: "A signed-in user refused many times: probing for what their role can reach.",
		Source:      "requests", Filter: map[string]string{"status": "403"}, Logic: "threshold", GroupBy: []string{"user"}, Window: "15m", Threshold: 30},
	{ID: "path_traversal", Category: "web", Name: "Path traversal attempts", Severity: "critical", Tactic: "TA0001", Technique: "T1190",
		Description: "Requests with ../ or encoded variants, or system files, in the path or query string.",
		Source:      "requests", Filter: map[string]string{"path": traversal}, Logic: "threshold", GroupBy: []string{"ip"}, Window: "10m", Threshold: 3},
	{ID: "sql_injection", Category: "web", Name: "SQL injection patterns", Severity: "critical", Tactic: "TA0001", Technique: "T1190",
		Description: "UNION SELECT, OR 1=1, sleep() and similar in the path or query string.",
		Source:      "requests", Filter: map[string]string{"path": sqlInjection}, Logic: "threshold", GroupBy: []string{"ip"}, Window: "10m", Threshold: 3},
	{ID: "forced_browsing", Category: "web", Name: "Forced browsing (many 404s)", Severity: "warning", Tactic: "TA0043", Technique: "T1595.003",
		Description: "One IP asking for many paths that don't exist: a wordlist scan.",
		Source:      "requests", Filter: map[string]string{"status": "404"}, Logic: "threshold", GroupBy: []string{"ip"}, Distinct: "path", Window: "5m", Threshold: 50},
	{ID: "scanner_agent", Category: "web", Name: "Vulnerability scanner user agent", Severity: "warning", Tactic: "TA0043", Technique: "T1595.002",
		Description: "Requests from sqlmap, nikto, nuclei and other scanners that name themselves.",
		Source:      "requests", Filter: map[string]string{"user_agent": "sqlmap,nikto,nuclei,masscan,zgrab,gobuster,dirbuster,wpscan,nmap,acunetix,ffuf,feroxbuster,wfuzz,hydra"},
		Logic: "threshold", GroupBy: []string{"ip"}, Window: "10m", Threshold: 1},
	{ID: "mass_export", Category: "data", Name: "Mass data export", Severity: "warning", Tactic: "TA0010", Technique: "T1567",
		Description: "One user calling export or download routes many times.",
		Source:      "requests", Filter: map[string]string{"route": "export,download,backup,dump", "auth_failed": "no"}, Logic: "threshold", GroupBy: []string{"user"}, Window: "15m", Threshold: 50},
	{ID: "secrets_in_logs", Category: "logs", Name: "Secrets reached the logs", Severity: "warning", Tactic: "TA0006", Technique: "T1552.001",
		Description: "The log scanner redacted a credential or card number; this notifies on it.",
		Source:      "events", Filter: map[string]string{"kind": "secret_in_logs"}, Logic: "threshold", GroupBy: []string{"service"}, Window: "1h", Threshold: 1},
	{ID: "log_auth_failures", Category: "logs", Name: "Auth failures in app logs", Severity: "warning", Tactic: "TA0006", Technique: "T1110",
		Description: "Your services log many authentication failures.",
		Source:      "logs", Filter: map[string]string{"query": `"authentication failed" OR "invalid token" OR "invalid password" OR "unauthorized"`},
		Logic: "threshold", GroupBy: []string{"service"}, Window: "10m", Threshold: 50},
	{ID: "ssh_bruteforce_then_login", Category: "hosts", Name: "SSH failures, then a login", Severity: "critical", Tactic: "TA0008", Technique: "T1021.004",
		Description: "An address fails SSH logins on a host, then logs in.",
		Source:      "events", Filter: map[string]string{"kind": "ssh_failed"}, Logic: "sequence", GroupBy: []string{"host", "ip"}, Window: "1h", Threshold: 10,
		Then: map[string]string{"kind": "ssh_login"}, Needs: "the host agent"},
	{ID: "ssh_login_new_ip", Category: "hosts", Name: "SSH login from a new IP", Severity: "warning", Tactic: "TA0008", Technique: "T1021.004",
		Description: "A host accepted an SSH login from an address it has not seen in 30 days.",
		Source:      "events", Filter: map[string]string{"kind": "ssh_login"}, Logic: "new_value", GroupBy: []string{"host"}, Field: "ip", Window: "1h", Learn: "30d",
		Needs: "the host agent"},
	{ID: "sudo_new_user", Category: "hosts", Name: "sudo by a new user", Severity: "warning", Tactic: "TA0004", Technique: "T1548.003",
		Description: "Someone ran sudo on a host for the first time in 30 days.",
		Source:      "events", Filter: map[string]string{"kind": "sudo"}, Logic: "new_value", GroupBy: []string{"host"}, Field: "user", Window: "1h", Learn: "30d",
		Needs: "the host agent"},
	{ID: "ssh_key_added", Category: "hosts", Name: "SSH key added", Severity: "warning", Tactic: "TA0003", Technique: "T1098.004",
		Description: "A new key in an authorized_keys file: someone may have given themselves a way back in.",
		Source:      "events", Filter: map[string]string{"kind": "ssh_key_added"}, Logic: "threshold", GroupBy: []string{"host"}, Window: "1h", Threshold: 1,
		Needs: "the host agent"},
	{ID: "suspicious_process", Category: "hosts", Name: "Suspicious process on a host", Severity: "critical", Tactic: "TA0040", Technique: "T1496",
		Description: "A miner, a process from /tmp or a deleted file, or a shell on a socket.",
		Source:      "events", Filter: map[string]string{"kind": "suspicious_process"}, Logic: "threshold", GroupBy: []string{"host"}, Window: "1h", Threshold: 1,
		Needs: "the host agent"},
	{ID: "new_listening_port", Category: "hosts", Name: "New listening port", Severity: "info", Tactic: "TA0011", Technique: "T1571",
		Description: "A host listens on a port it has not opened in 30 days.",
		Source:      "events", Filter: map[string]string{"kind": "port_opened"}, Logic: "new_value", GroupBy: []string{"host"}, Field: "target", Window: "1h", Learn: "30d",
		Needs: "the host agent"},
	{ID: "new_api_key", Category: "visionary", Name: "New Visionary API key", Severity: "info", Tactic: "TA0003", Technique: "T1098.001",
		Description: "Someone created a Visionary API key.",
		Source:      "audit", Filter: map[string]string{"action": "api_key.create"}, Logic: "threshold", GroupBy: []string{"user"}, Window: "1h", Threshold: 1},
	{ID: "visionary_data_deleted", Category: "visionary", Name: "Visionary data deleted", Severity: "warning", Tactic: "TA0040", Technique: "T1485",
		Description: "Stored data was deleted from Settings → Data.",
		Source:      "audit", Filter: map[string]string{"action": "data.delete"}, Logic: "threshold", GroupBy: []string{"user"}, Window: "1h", Threshold: 1},
	{ID: "visionary_log_export", Category: "visionary", Name: "Bulk log exports from Visionary", Severity: "warning", Tactic: "TA0010", Technique: "T1567",
		Description: "One person exporting logs many times.",
		Source:      "audit", Filter: map[string]string{"action": "logs.export"}, Logic: "threshold", GroupBy: []string{"user"}, Window: "1h", Threshold: 5},
	// Sign-in and accounts
	{ID: "otp_guessing", Category: "signin", Name: "Guessing sign-in codes", Severity: "warning", Tactic: "TA0006", Technique: "T1110",
		Description: "One IP getting many wrong one-time or two-step codes.",
		Source:      "requests", Filter: map[string]string{"route": "otp,2fa,mfa,totp,verify,one-time", "status": "400,401,403,422"}, Logic: "threshold", GroupBy: []string{"ip"}, Window: "10m", Threshold: 10},
	{ID: "password_reset_flood", Category: "signin", Name: "Password reset flood", Severity: "warning", Tactic: "TA0043", Technique: "T1589",
		Description: "One IP asking for many password resets: finding which emails have accounts, or flooding someone's inbox.",
		Source:      "requests", Filter: map[string]string{"route": "reset,forgot,recover", "method": "POST"}, Logic: "threshold", GroupBy: []string{"ip"}, Window: "15m", Threshold: 20},
	{ID: "signup_probing", Category: "signin", Name: "Probing which emails have accounts", Severity: "warning", Tactic: "TA0043", Technique: "T1589",
		Description: "One IP's sign-ups refused many times as taken or invalid.",
		Source:      "requests", Filter: map[string]string{"route": "register,signup,sign-up,join", "method": "POST", "status": "409,422"}, Logic: "threshold", GroupBy: []string{"ip"}, Window: "15m", Threshold: 20},
	{ID: "rejected_tokens", Category: "signin", Name: "Many rejected tokens", Severity: "warning", Tactic: "TA0005", Technique: "T1550.001",
		Description: "One IP sending many tokens your API refuses: replayed, forged or stolen ones.",
		Source:      "requests", Filter: map[string]string{"route": "token,refresh,oauth", "status": "400,401"}, Logic: "threshold", GroupBy: []string{"ip"}, Window: "10m", Threshold: 50},
	{ID: "scripted_logins", Category: "signin", Name: "Scripted sign-in attempts", Severity: "warning", Tactic: "TA0006", Technique: "T1110",
		Description: "Sign-ins from curl, python-requests and other HTTP libraries rather than a browser or your app.",
		Source:      "requests", Filter: map[string]string{"login": "yes", "user_agent": "python-requests,curl,go-http-client,wget,okhttp,headlesschrome,phantomjs,scrapy,aiohttp,httpx,axios,node-fetch"},
		Logic: "threshold", GroupBy: []string{"ip"}, Window: "10m", Threshold: 10},
	{ID: "account_many_ips", Category: "signin", Name: "One account signing in from many addresses", Severity: "warning", Tactic: "TA0001", Technique: "T1078",
		Description: "Successful sign-ins to one account from many addresses within the hour: a shared or stolen password.",
		Source:      "requests", Filter: map[string]string{"login": "yes", "auth_failed": "no"}, Logic: "threshold", GroupBy: []string{"user"}, Distinct: "ip", Window: "1h", Threshold: 10},
	{ID: "new_browser_for_user", Category: "signin", Name: "Sign-in from a new browser", Severity: "info", Tactic: "TA0001", Technique: "T1078",
		Description: "A user signs in with a browser and system they have not used in 30 days.",
		Source:      "requests", Filter: map[string]string{"login": "yes", "auth_failed": "no"}, Logic: "new_value", GroupBy: []string{"user"}, Field: "user_agent", Window: "1h", Learn: "30d"},
	{ID: "scan_then_login", Category: "signin", Name: "Scanning, then a successful sign-in", Severity: "critical", Tactic: "TA0001", Technique: "T1078",
		Description: "An IP asks for many paths that don't exist, then signs in.",
		Source:      "requests", Filter: map[string]string{"status": "404"}, Logic: "sequence", GroupBy: []string{"ip"}, Window: "1h", Threshold: 30,
		Then: map[string]string{"login": "yes", "auth_failed": "no"}},

	// Web attacks
	{ID: "xss_attempts", Category: "web", Name: "Cross-site scripting attempts", Severity: "warning", Tactic: "TA0002", Technique: "T1059.007",
		Description: "<script>, javascript:, onerror= and similar in the path or query string.",
		Source:      "requests", Filter: map[string]string{"path": "<script,%3cscript,javascript:,onerror=,onload=,%3csvg,%3cimg,document.cookie,alert(,alert%28"},
		Logic: "threshold", GroupBy: []string{"ip"}, Window: "10m", Threshold: 3},
	{ID: "command_injection", Category: "web", Name: "Command injection attempts", Severity: "critical", Tactic: "TA0002", Technique: "T1059.004",
		Description: "Shell commands in the path or query string: ;cat, |sh, $(…), wget, /bin/sh.",
		Source:      "requests", Filter: map[string]string{"path": ";cat%20,%3bcat,|cat%20,%7ccat,$(,%24%28,;wget,%3bwget,;curl,%3bcurl,|sh,%7csh,*/bin/sh*,*/bin/bash*,%2fbin%2fsh,nc%20-e,cmd.exe,powershell"},
		Logic: "threshold", GroupBy: []string{"ip"}, Window: "10m", Threshold: 2},
	{ID: "jndi_lookup", Category: "web", Name: "Log4Shell (JNDI) probes", Severity: "critical", Tactic: "TA0001", Technique: "T1190",
		Description: "${jndi:…} in the path or query string: the Log4j exploit.",
		Source:      "requests", Filter: map[string]string{"path": "${jndi:,%24%7bjndi,$%7bjndi,%24{jndi"}, Logic: "threshold", GroupBy: []string{"ip"}, Window: "1h", Threshold: 1},
	{ID: "cloud_metadata", Category: "web", Name: "Cloud metadata address in a request", Severity: "critical", Tactic: "TA0006", Technique: "T1552.005",
		Description: "169.254.169.254 or a metadata hostname in the path or query string: trying to make your server fetch cloud credentials.",
		Source:      "requests", Filter: map[string]string{"path": "169.254.169.254,metadata.google.internal,100.100.100.200,fd00:ec2::254,169.254.170.2"},
		Logic: "threshold", GroupBy: []string{"ip"}, Window: "1h", Threshold: 1},
	{ID: "config_file_probing", Category: "web", Name: "Probing for secrets and config files", Severity: "warning", Tactic: "TA0006", Technique: "T1552.001",
		Description: "Requests for .env, .git, cloud credentials, backups and database dumps.",
		Source:      "requests", Filter: map[string]string{"path": "*/.env*,*/.git/*,*/.aws/*,*/.ssh/*,*/id_rsa*,*wp-config*,*.sql,*.bak,*.backup,*/phpinfo*,*/server-status*,*/actuator/env*,*/.docker*,*/config.json"},
		Logic: "threshold", GroupBy: []string{"ip"}, Distinct: "path", Window: "10m", Threshold: 3},
	{ID: "cms_admin_probing", Category: "web", Name: "Probing for admin panels", Severity: "info", Tactic: "TA0043", Technique: "T1595.003",
		Description: "Requests for WordPress, phpMyAdmin, Tomcat and other admin pages your app doesn't have.",
		Source:      "requests", Filter: map[string]string{"path": "*/wp-admin*,*/wp-login*,*/xmlrpc.php*,*/phpmyadmin*,*/pma/*,*/admin.php*,*/manager/html*,*/cgi-bin/*,*/solr/*,*/jenkins*", "status": "404"},
		Logic: "threshold", GroupBy: []string{"ip"}, Window: "10m", Threshold: 5},
	{ID: "odd_http_methods", Category: "web", Name: "Unusual HTTP methods", Severity: "info", Tactic: "TA0043", Technique: "T1595.002",
		Description: "TRACE, CONNECT, PROPFIND and similar methods a normal client doesn't send.",
		Source:      "requests", Filter: map[string]string{"method": "TRACE,TRACK,CONNECT,PROPFIND,PROPPATCH,MKCOL,DEBUG,MOVE,COPY,LOCK"}, Logic: "threshold", GroupBy: []string{"ip"}, Window: "10m", Threshold: 5},
	{ID: "graphql_introspection", Category: "web", Name: "GraphQL schema probing", Severity: "info", Tactic: "TA0043", Technique: "T1595.002",
		Description: "Introspection queries asking for your whole GraphQL schema.",
		Source:      "requests", Filter: map[string]string{"path": "__schema,__type,introspectionquery"}, Logic: "threshold", GroupBy: []string{"ip"}, Window: "10m", Threshold: 3},
	{ID: "injection_then_errors", Category: "web", Name: "Injection attempt, then server errors", Severity: "critical", Tactic: "TA0001", Technique: "T1190",
		Description: "An IP sends SQL injection patterns, then gets 5xx answers: the payload may have reached your database.",
		Source:      "requests", Filter: map[string]string{"path": sqlInjection}, Logic: "sequence", GroupBy: []string{"ip"}, Window: "30m", Threshold: 1,
		Then: map[string]string{"status": "5xx"}},
	{ID: "errors_from_one_ip", Category: "web", Name: "One address causing many server errors", Severity: "warning", Tactic: "TA0040", Technique: "T1499",
		Description: "Many 5xx answers to one IP: fuzzing, or a request that keeps breaking something.",
		Source:      "requests", Filter: map[string]string{"status": "5xx"}, Logic: "threshold", GroupBy: []string{"ip"}, Window: "10m", Threshold: 50},
	{ID: "request_flood_ip", Category: "web", Name: "Request flood from one address", Severity: "warning", Tactic: "TA0040", Technique: "T1499.002",
		Description: "One IP sending thousands of requests in a few minutes.",
		Source:      "requests", Logic: "threshold", GroupBy: []string{"ip"}, Window: "5m", Threshold: 3000},
	{ID: "forged_webhooks", Category: "web", Name: "Forged webhook calls", Severity: "warning", Tactic: "TA0001", Technique: "T1190",
		Description: "Your webhook routes refusing many calls from one IP: bad signatures or replayed events.",
		Source:      "requests", Filter: map[string]string{"route": "webhook,hook,callback", "status": "400,401,403"}, Logic: "threshold", GroupBy: []string{"ip"}, Window: "10m", Threshold: 20},

	// Abuse and data access
	{ID: "card_testing", Category: "data", Name: "Card testing", Severity: "critical", Tactic: "TA0040", Technique: "T1657",
		Description: "One IP getting many declined payments: stolen card numbers being tried out.",
		Source:      "requests", Filter: map[string]string{"route": "payment,checkout,charge,card,billing", "status": "402,4xx"}, Logic: "threshold", GroupBy: []string{"ip"}, Window: "15m", Threshold: 15},
	{ID: "mass_delete", Category: "data", Name: "Mass deletion by one user", Severity: "warning", Tactic: "TA0040", Technique: "T1485",
		Description: "One user deleting many records in a few minutes.",
		Source:      "requests", Filter: map[string]string{"method": "DELETE", "auth_failed": "no"}, Logic: "threshold", GroupBy: []string{"user"}, Window: "10m", Threshold: 100},
	{ID: "records_walk", Category: "data", Name: "One user reading many missing records", Severity: "warning", Tactic: "TA0009", Technique: "T1213",
		Description: "A signed-in user getting 404s for many different paths: walking through IDs.",
		Source:      "requests", Filter: map[string]string{"status": "404"}, Logic: "threshold", GroupBy: []string{"user"}, Distinct: "path", Window: "15m", Threshold: 100},
	{ID: "new_admin_user", Category: "data", Name: "A new account using an admin role", Severity: "warning", Tactic: "TA0004", Technique: "T1098",
		Description: "Requests from a user with an admin role who never used it in the last 30 days.",
		Source:      "requests", Filter: map[string]string{"role": "admin,owner,superuser,root", "auth_failed": "no"}, Logic: "new_value", GroupBy: []string{"role"}, Field: "user", Window: "1h", Learn: "30d"},
	{ID: "export_new_ip", Category: "data", Name: "Export from a new address", Severity: "warning", Tactic: "TA0010", Technique: "T1567",
		Description: "A user calls export or download routes from an address they never used for them.",
		Source:      "requests", Filter: map[string]string{"route": "export,download,backup,dump", "auth_failed": "no"}, Logic: "new_value", GroupBy: []string{"user"}, Field: "ip", Window: "1h", Learn: "30d"},

	// Hosts
	{ID: "ssh_root_login", Category: "hosts", Name: "SSH login as root", Severity: "warning", Tactic: "TA0001", Technique: "T1078.003",
		Description: "Someone logged in over SSH directly as root.",
		Source:      "events", Filter: map[string]string{"kind": "ssh_login", "user": "root"}, Logic: "threshold", GroupBy: []string{"host", "ip"}, Window: "1h", Threshold: 1,
		Needs: "the host agent"},
	{ID: "ssh_distributed", Category: "hosts", Name: "SSH attacked from many addresses", Severity: "warning", Tactic: "TA0006", Technique: "T1110.003",
		Description: "Failed SSH logins on one host from many different addresses.",
		Source:      "events", Filter: map[string]string{"kind": "ssh_failed"}, Logic: "threshold", GroupBy: []string{"host"}, Distinct: "ip", Window: "1h", Threshold: 20,
		Needs: "the host agent"},
	{ID: "sudoers_changed", Category: "hosts", Name: "sudoers changed", Severity: "critical", Tactic: "TA0004", Technique: "T1548.003",
		Description: "The sudoers file or a file in sudoers.d changed: who may run commands as root.",
		Source:      "events", Filter: map[string]string{"kind": "file_changed", "target": "sudoers"}, Logic: "threshold", GroupBy: []string{"host"}, Window: "1h", Threshold: 1,
		Needs: "the host agent"},
	{ID: "accounts_changed", Category: "hosts", Name: "Local accounts changed", Severity: "warning", Tactic: "TA0003", Technique: "T1136.001",
		Description: "passwd, shadow or group changed on a host: an account was added, removed or given a new password.",
		Source:      "events", Filter: map[string]string{"kind": "file_changed", "target": "passwd,shadow,group,gshadow"}, Logic: "threshold", GroupBy: []string{"host"}, Window: "1h", Threshold: 1,
		Needs: "the host agent"},
	{ID: "cron_changed", Category: "hosts", Name: "Scheduled job changed", Severity: "warning", Tactic: "TA0003", Technique: "T1053.003",
		Description: "A crontab or a file in cron.d changed: a way to run code again later.",
		Source:      "events", Filter: map[string]string{"kind": "file_changed", "target": "cron"}, Logic: "threshold", GroupBy: []string{"host"}, Window: "1h", Threshold: 1,
		Needs: "the host agent"},
	{ID: "systemd_unit_changed", Category: "hosts", Name: "systemd service changed", Severity: "warning", Tactic: "TA0003", Technique: "T1543.002",
		Description: "A systemd unit file changed: a service that starts with the machine.",
		Source:      "events", Filter: map[string]string{"kind": "file_changed", "target": "systemd"}, Logic: "threshold", GroupBy: []string{"host"}, Window: "1h", Threshold: 1,
		Needs: "the host agent"},
	{ID: "ssh_key_removed", Category: "hosts", Name: "SSH key removed", Severity: "info", Tactic: "TA0040", Technique: "T1531",
		Description: "A key left an authorized_keys file: someone may be locking others out.",
		Source:      "events", Filter: map[string]string{"kind": "ssh_key_removed"}, Logic: "threshold", GroupBy: []string{"host"}, Window: "1h", Threshold: 1,
		Needs: "the host agent"},
	{ID: "login_then_port", Category: "hosts", Name: "SSH login, then a new listening port", Severity: "warning", Tactic: "TA0011", Technique: "T1571",
		Description: "A host opens a new port soon after an SSH login: a backdoor or tunnel being set up.",
		Source:      "events", Filter: map[string]string{"kind": "ssh_login"}, Logic: "sequence", GroupBy: []string{"host"}, Window: "1h", Threshold: 1,
		Then: map[string]string{"kind": "port_opened"}, Needs: "the host agent"},
	{ID: "login_then_file_change", Category: "hosts", Name: "SSH login, then a system file changed", Severity: "warning", Tactic: "TA0003", Technique: "T1098",
		Description: "A watched system file changes soon after an SSH login.",
		Source:      "events", Filter: map[string]string{"kind": "ssh_login"}, Logic: "sequence", GroupBy: []string{"host"}, Window: "1h", Threshold: 1,
		Then: map[string]string{"kind": "file_changed"}, Needs: "the host agent"},
	{ID: "security_updates", Category: "hosts", Name: "Security updates waiting", Severity: "info", Tactic: "TA0001", Technique: "T1190",
		Description: "A host has security updates it hasn't installed.",
		Source:      "events", Filter: map[string]string{"kind": "updates_pending"}, Logic: "threshold", GroupBy: []string{"host"}, Window: "24h", Threshold: 1,
		Needs: "the host agent"},
	{ID: "host_reboot", Category: "hosts", Name: "Host restarted", Severity: "info", Tactic: "TA0040", Technique: "T1529",
		Description: "A host rebooted. Expected after updates; otherwise worth a look.",
		Source:      "events", Filter: map[string]string{"kind": "reboot"}, Logic: "threshold", GroupBy: []string{"host"}, Window: "1h", Threshold: 1,
		Needs: "the host agent"},

	// Containers
	{ID: "privileged_container", Category: "containers", Name: "Privileged container", Severity: "critical", Tactic: "TA0004", Technique: "T1611",
		Description: "A container started privileged, on the host network, with the Docker socket or with broad capabilities: one step from the host.",
		Source:      "events", Filter: map[string]string{"kind": "container_privileged"}, Logic: "threshold", GroupBy: []string{"host", "target"}, Window: "1h", Threshold: 1,
		Needs: "the host agent"},
	{ID: "root_container", Category: "containers", Name: "Container running as root", Severity: "info", Tactic: "TA0004", Technique: "T1611",
		Description: "A running container whose user is root.",
		Source:      "events", Filter: map[string]string{"kind": "container_root"}, Logic: "threshold", GroupBy: []string{"host", "target"}, Window: "24h", Threshold: 1,
		Needs: "the host agent"},
	{ID: "new_container_image", Category: "containers", Name: "New container image", Severity: "info", Tactic: "TA0002", Technique: "T1610",
		Description: "A host runs an image it hasn't run before.",
		Source:      "events", Filter: map[string]string{"kind": "container_new_image"}, Logic: "threshold", GroupBy: []string{"host"}, Window: "1h", Threshold: 1,
		Needs: "the host agent"},
	{ID: "new_image_then_port", Category: "containers", Name: "New image, then a new listening port", Severity: "warning", Tactic: "TA0002", Technique: "T1610",
		Description: "A host opens a new port soon after running an image it hadn't run before.",
		Source:      "events", Filter: map[string]string{"kind": "container_new_image"}, Logic: "sequence", GroupBy: []string{"host"}, Window: "1h", Threshold: 1,
		Then: map[string]string{"kind": "port_opened"}, Needs: "the host agent"},
	{ID: "new_image_then_suspicious", Category: "containers", Name: "New image, then a suspicious process", Severity: "critical", Tactic: "TA0040", Technique: "T1496",
		Description: "A miner or a shell on a socket shows up soon after a new image runs.",
		Source:      "events", Filter: map[string]string{"kind": "container_new_image"}, Logic: "sequence", GroupBy: []string{"host"}, Window: "2h", Threshold: 1,
		Then: map[string]string{"kind": "suspicious_process"}, Needs: "the host agent"},

	// App logs
	{ID: "db_auth_failures", Category: "logs", Name: "Database refusing a password", Severity: "warning", Tactic: "TA0006", Technique: "T1110",
		Description: "Postgres, MySQL or SQL Server rejecting logins in your logs: a rotated secret, or someone guessing.",
		Source:      "logs", Filter: map[string]string{"query": `"password authentication failed" OR "Access denied for user" OR "Login failed for user" OR "authentication failed for user"`},
		Logic: "threshold", GroupBy: []string{"service"}, Window: "10m", Threshold: 10},
	{ID: "token_errors_logs", Category: "logs", Name: "Token errors in app logs", Severity: "info", Tactic: "TA0005", Technique: "T1550.001",
		Description: "Many expired, malformed or badly signed tokens logged by your services.",
		Source:      "logs", Filter: map[string]string{"query": `"jwt expired" OR "jwt malformed" OR "invalid signature" OR "signature verification failed" OR "token is expired" OR "invalid token"`},
		Logic: "threshold", GroupBy: []string{"service"}, Window: "10m", Threshold: 100},
	{ID: "csrf_failures", Category: "logs", Name: "CSRF check failures", Severity: "warning", Tactic: "TA0006", Technique: "T1185",
		Description: "Your services refusing requests for a missing or wrong CSRF token or origin.",
		Source:      "logs", Filter: map[string]string{"query": `csrf OR xsrf OR "invalid origin" OR "origin not allowed"`},
		Logic: "threshold", GroupBy: []string{"service"}, Window: "10m", Threshold: 20},
	{ID: "permission_denied_logs", Category: "logs", Name: "Permission errors in app logs", Severity: "info", Tactic: "TA0007", Technique: "T1069",
		Description: "Many permission denied or access denied lines from one service.",
		Source:      "logs", Filter: map[string]string{"query": `"permission denied" OR "access denied" OR "not authorized"`},
		Logic: "threshold", GroupBy: []string{"service"}, Window: "10m", Threshold: 100},
	{ID: "rate_limited_logs", Category: "logs", Name: "Rate limiting in app logs", Severity: "info", Tactic: "TA0040", Technique: "T1499",
		Description: "Your services throttling many requests.",
		Source:      "logs", Filter: map[string]string{"query": `"rate limit" OR "too many requests" OR throttled`},
		Logic: "threshold", GroupBy: []string{"service"}, Window: "10m", Threshold: 200},

	// Visionary
	{ID: "two_step_off", Category: "visionary", Name: "Two-step sign-in turned off", Severity: "warning", Tactic: "TA0006", Technique: "T1556.006",
		Description: "Someone turned off two-step sign-in for their Visionary account, or an admin reset it for someone.",
		Source:      "audit", Filter: map[string]string{"action": "auth.two_step_disabled,auth.two_step_reset"}, Logic: "threshold", GroupBy: []string{"user"}, Window: "1h", Threshold: 1},
	{ID: "visionary_signin_changed", Category: "visionary", Name: "Visionary sign-in settings changed", Severity: "warning", Tactic: "TA0006", Technique: "T1556",
		Description: "Password rules, SSO or who gets which role changed.",
		Source:      "audit", Filter: map[string]string{"action": "settings.sign_in,settings.sso"}, Logic: "threshold", GroupBy: []string{"user"}, Window: "1h", Threshold: 1},
	{ID: "visionary_accounts", Category: "visionary", Name: "Visionary account added or changed", Severity: "info", Tactic: "TA0003", Technique: "T1098",
		Description: "An account was added, or its role changed, by hand or through SCIM.",
		Source:      "audit", Filter: map[string]string{"action": "team.account_created,team.account_updated,scim.user_created,scim.role_changed"}, Logic: "threshold", GroupBy: []string{"user"}, Window: "1h", Threshold: 1},
	{ID: "visionary_secret_viewed", Category: "visionary", Name: "Stored secret shown", Severity: "warning", Tactic: "TA0006", Technique: "T1552",
		Description: "Someone showed a stored secret in Settings → Server.",
		Source:      "audit", Filter: map[string]string{"action": "settings.secret_viewed"}, Logic: "threshold", GroupBy: []string{"user"}, Window: "1h", Threshold: 1},
	{ID: "detections_disabled", Category: "visionary", Name: "Detections turned off", Severity: "warning", Tactic: "TA0005", Technique: "T1562.001",
		Description: "A detection rule was deleted or suppressed, or a finding suppression added.",
		Source:      "audit", Filter: map[string]string{"action": "security.rule_deleted,security.rule_suppressed,security.suppression_created"}, Logic: "threshold", GroupBy: []string{"user"}, Window: "1h", Threshold: 1},
	{ID: "alerts_silenced", Category: "visionary", Name: "Alerts silenced or removed", Severity: "info", Tactic: "TA0005", Technique: "T1562",
		Description: "An alert rule or channel was deleted, or a silence added.",
		Source:      "audit", Filter: map[string]string{"action": "alert.rule.delete,alert.channel.delete,alert.silence.create"}, Logic: "threshold", GroupBy: []string{"user"}, Window: "1h", Threshold: 1},
	{ID: "retention_changed", Category: "visionary", Name: "Retention changed", Severity: "info", Tactic: "TA0005", Technique: "T1070",
		Description: "How long Visionary keeps data changed. Shorter retention erases history.",
		Source:      "audit", Filter: map[string]string{"action": "settings.retention"}, Logic: "threshold", GroupBy: []string{"user"}, Window: "1h", Threshold: 1},
	{ID: "visionary_config_export", Category: "visionary", Name: "Configuration or audit log exported", Severity: "info", Tactic: "TA0010", Technique: "T1567",
		Description: "Someone exported Visionary's configuration or the security audit log.",
		Source:      "audit", Filter: map[string]string{"action": "config.export,security.audit_exported"}, Logic: "threshold", GroupBy: []string{"user"}, Window: "1h", Threshold: 1},
}

type mitreEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

var mitreTactics = []mitreEntry{
	{"TA0043", "Reconnaissance"}, {"TA0042", "Resource Development"}, {"TA0001", "Initial Access"}, {"TA0002", "Execution"},
	{"TA0003", "Persistence"}, {"TA0004", "Privilege Escalation"}, {"TA0005", "Defense Evasion"}, {"TA0006", "Credential Access"},
	{"TA0007", "Discovery"}, {"TA0008", "Lateral Movement"}, {"TA0009", "Collection"}, {"TA0011", "Command and Control"},
	{"TA0010", "Exfiltration"}, {"TA0040", "Impact"},
}

var mitreTechniques = []mitreEntry{
	{"T1078", "Valid Accounts"}, {"T1078.004", "Valid Accounts: Cloud Accounts"},
	{"T1110", "Brute Force"}, {"T1110.001", "Brute Force: Password Guessing"}, {"T1110.003", "Brute Force: Password Spraying"},
	{"T1110.004", "Brute Force: Credential Stuffing"}, {"T1190", "Exploit Public-Facing Application"},
	{"T1595.002", "Active Scanning: Vulnerability Scanning"}, {"T1595.003", "Active Scanning: Wordlist Scanning"},
	{"T1069", "Permission Groups Discovery"}, {"T1083", "File and Directory Discovery"},
	{"T1567", "Exfiltration Over Web Service"}, {"T1530", "Data from Cloud Storage"}, {"T1213", "Data from Information Repositories"},
	{"T1552.001", "Unsecured Credentials: Credentials In Files"}, {"T1021.004", "Remote Services: SSH"},
	{"T1548.003", "Abuse Elevation Control Mechanism: Sudo and Sudo Caching"}, {"T1098.001", "Account Manipulation: Additional Cloud Credentials"},
	{"T1098.004", "Account Manipulation: SSH Authorized Keys"}, {"T1496", "Resource Hijacking"}, {"T1571", "Non-Standard Port"},
	{"T1485", "Data Destruction"}, {"T1499", "Endpoint Denial of Service"}, {"T1556", "Modify Authentication Process"},
	{"T1539", "Steal Web Session Cookie"}, {"T1059", "Command and Scripting Interpreter"}, {"T1610", "Deploy Container"},
	{"T1589", "Gather Victim Identity Information"}, {"T1550.001", "Use Alternate Authentication Material: Application Access Token"},
	{"T1059.004", "Command and Scripting Interpreter: Unix Shell"}, {"T1059.007", "Command and Scripting Interpreter: JavaScript"},
	{"T1552", "Unsecured Credentials"}, {"T1552.005", "Unsecured Credentials: Cloud Instance Metadata API"}, {"T1499.002", "Endpoint Denial of Service: Service Exhaustion Flood"},
	{"T1657", "Financial Theft"}, {"T1098", "Account Manipulation"}, {"T1078.003", "Valid Accounts: Local Accounts"},
	{"T1136.001", "Create Account: Local Account"}, {"T1053.003", "Scheduled Task/Job: Cron"}, {"T1543.002", "Create or Modify System Process: Systemd Service"},
	{"T1531", "Account Access Removal"}, {"T1529", "System Shutdown/Reboot"}, {"T1611", "Escape to Host"},
	{"T1185", "Browser Session Hijacking"}, {"T1556.006", "Modify Authentication Process: Multi-Factor Authentication"},
	{"T1562", "Impair Defenses"}, {"T1562.001", "Impair Defenses: Disable or Modify Tools"}, {"T1070", "Indicator Removal"},
}

func mitreName(id string) string {
	for _, list := range [][]mitreEntry{mitreTactics, mitreTechniques} {
		for _, e := range list {
			if e.ID == id {
				return id + " " + e.Name
			}
		}
	}
	return id
}
