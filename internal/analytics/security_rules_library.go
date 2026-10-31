package analytics

type ruleTemplate struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Severity    string            `json:"severity"`
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
	failedLogin  = map[string]string{"login": "yes", "auth_failed": "yes"}
	traversal    = "../,..%2f,%2e%2e,..\\,/etc/passwd,/proc/self,win.ini,boot.ini"
	sqlInjection = "union%20select,union+select,union select,%27%20or%20,' or ',%27or%27,or%201=1,or 1=1,sleep(,benchmark(,information_schema,pg_sleep,waitfor%20delay,xp_cmdshell"
)

var ruleTemplates = []ruleTemplate{
	{ID: "login_bruteforce", Name: "Brute force on login", Severity: "warning", Tactic: "TA0006", Technique: "T1110.001",
		Description: "Many failed sign-ins from one IP on login routes.",
		Source:      "requests", Filter: failedLogin, Logic: "threshold", GroupBy: []string{"ip"}, Window: "10m", Threshold: 20},
	{ID: "credential_stuffing", Name: "Credential stuffing across accounts", Severity: "critical", Tactic: "TA0006", Technique: "T1110.004",
		Description: "One IP failing to sign in as many different accounts: a leaked password list being tried.",
		Source:      "requests", Filter: failedLogin, Logic: "threshold", GroupBy: []string{"ip"}, Distinct: "account", Window: "15m", Threshold: 10,
		Needs: "LoginAttempt in your login handler, so the account tried is known"},
	{ID: "password_spray", Name: "Account targeted from many IPs", Severity: "warning", Tactic: "TA0006", Technique: "T1110.003",
		Description: "Many addresses failing to sign in as one account.",
		Source:      "requests", Filter: failedLogin, Logic: "threshold", GroupBy: []string{"account"}, Distinct: "ip", Window: "30m", Threshold: 5,
		Needs: "LoginAttempt in your login handler"},
	{ID: "failed_then_success", Name: "Failed logins, then a success", Severity: "critical", Tactic: "TA0006", Technique: "T1110",
		Description: "An IP fails to sign in several times, then signs in: a guessed password.",
		Source:      "requests", Filter: failedLogin, Logic: "sequence", GroupBy: []string{"ip"}, Window: "30m", Threshold: 5,
		Then: map[string]string{"login": "yes", "auth_failed": "no"}},
	{ID: "new_country_for_user", Name: "Sign-in from a new country", Severity: "warning", Tactic: "TA0001", Technique: "T1078",
		Description: "A user's requests come from a country they never used in the last 30 days: travel, or a stolen session.",
		Source:      "requests", Filter: map[string]string{"auth_failed": "no"}, Logic: "new_value", GroupBy: []string{"user"}, Field: "country", Window: "1h", Learn: "30d",
		Needs: "a location database in Settings → Server → Threat intelligence"},
	{ID: "admin_new_ip", Name: "Visionary admin from a new IP", Severity: "warning", Tactic: "TA0001", Technique: "T1078",
		Description: "A Visionary admin signed in from an address they have not used in 30 days.",
		Source:      "audit", Filter: map[string]string{"action": "auth.sign_in", "role": "admin"}, Logic: "new_value", GroupBy: []string{"user"}, Field: "ip", Window: "1h", Learn: "30d"},
	{ID: "app_user_new_ip_admin_route", Name: "Admin route from a new IP", Severity: "warning", Tactic: "TA0004", Technique: "T1078",
		Description: "A user calls admin routes from an address they have not used for them before.",
		Source:      "requests", Filter: map[string]string{"route": "*admin*", "auth_failed": "no"}, Logic: "new_value", GroupBy: []string{"user"}, Field: "ip", Window: "1h", Learn: "14d"},
	{ID: "auth_error_spike", Name: "Spike in 401 and 403", Severity: "warning", Tactic: "TA0006", Technique: "T1110",
		Description: "Far more unauthorized and forbidden answers than usual across the API.",
		Source:      "requests", Filter: map[string]string{"status": "401,403"}, Logic: "anomaly", Window: "10m", Threshold: 50, Factor: 4},
	{ID: "forbidden_per_user", Name: "One user hitting 403s", Severity: "warning", Tactic: "TA0007", Technique: "T1069",
		Description: "A signed-in user refused many times: probing for what their role can reach.",
		Source:      "requests", Filter: map[string]string{"status": "403"}, Logic: "threshold", GroupBy: []string{"user"}, Window: "15m", Threshold: 30},
	{ID: "path_traversal", Name: "Path traversal attempts", Severity: "critical", Tactic: "TA0001", Technique: "T1190",
		Description: "Requests with ../ or encoded variants, or system files, in the path or query string.",
		Source:      "requests", Filter: map[string]string{"path": traversal}, Logic: "threshold", GroupBy: []string{"ip"}, Window: "10m", Threshold: 3},
	{ID: "sql_injection", Name: "SQL injection patterns", Severity: "critical", Tactic: "TA0001", Technique: "T1190",
		Description: "UNION SELECT, OR 1=1, sleep() and similar in the path or query string.",
		Source:      "requests", Filter: map[string]string{"path": sqlInjection}, Logic: "threshold", GroupBy: []string{"ip"}, Window: "10m", Threshold: 3},
	{ID: "forced_browsing", Name: "Forced browsing (many 404s)", Severity: "warning", Tactic: "TA0043", Technique: "T1595.003",
		Description: "One IP asking for many paths that don't exist: a wordlist scan.",
		Source:      "requests", Filter: map[string]string{"status": "404"}, Logic: "threshold", GroupBy: []string{"ip"}, Distinct: "path", Window: "5m", Threshold: 50},
	{ID: "scanner_agent", Name: "Vulnerability scanner user agent", Severity: "warning", Tactic: "TA0043", Technique: "T1595.002",
		Description: "Requests from sqlmap, nikto, nuclei and other scanners that name themselves.",
		Source:      "requests", Filter: map[string]string{"user_agent": "sqlmap,nikto,nuclei,masscan,zgrab,gobuster,dirbuster,wpscan,nmap,acunetix,ffuf,feroxbuster,wfuzz,hydra"},
		Logic: "threshold", GroupBy: []string{"ip"}, Window: "10m", Threshold: 1},
	{ID: "mass_export", Name: "Mass data export", Severity: "warning", Tactic: "TA0010", Technique: "T1567",
		Description: "One user calling export or download routes many times.",
		Source:      "requests", Filter: map[string]string{"route": "export,download,backup,dump", "auth_failed": "no"}, Logic: "threshold", GroupBy: []string{"user"}, Window: "15m", Threshold: 50},
	{ID: "secrets_in_logs", Name: "Secrets reached the logs", Severity: "warning", Tactic: "TA0006", Technique: "T1552.001",
		Description: "The log scanner redacted a credential or card number; this notifies on it.",
		Source:      "events", Filter: map[string]string{"kind": "secret_in_logs"}, Logic: "threshold", GroupBy: []string{"service"}, Window: "1h", Threshold: 1},
	{ID: "log_auth_failures", Name: "Auth failures in app logs", Severity: "warning", Tactic: "TA0006", Technique: "T1110",
		Description: "Your services log many authentication failures.",
		Source:      "logs", Filter: map[string]string{"query": `"authentication failed" OR "invalid token" OR "invalid password" OR "unauthorized"`},
		Logic: "threshold", GroupBy: []string{"service"}, Window: "10m", Threshold: 50},
	{ID: "ssh_bruteforce_then_login", Name: "SSH failures, then a login", Severity: "critical", Tactic: "TA0008", Technique: "T1021.004",
		Description: "An address fails SSH logins on a host, then logs in.",
		Source:      "events", Filter: map[string]string{"kind": "ssh_failed"}, Logic: "sequence", GroupBy: []string{"host", "ip"}, Window: "1h", Threshold: 10,
		Then: map[string]string{"kind": "ssh_login"}, Needs: "the host agent"},
	{ID: "ssh_login_new_ip", Name: "SSH login from a new IP", Severity: "warning", Tactic: "TA0008", Technique: "T1021.004",
		Description: "A host accepted an SSH login from an address it has not seen in 30 days.",
		Source:      "events", Filter: map[string]string{"kind": "ssh_login"}, Logic: "new_value", GroupBy: []string{"host"}, Field: "ip", Window: "1h", Learn: "30d",
		Needs: "the host agent"},
	{ID: "sudo_new_user", Name: "sudo by a new user", Severity: "warning", Tactic: "TA0004", Technique: "T1548.003",
		Description: "Someone ran sudo on a host for the first time in 30 days.",
		Source:      "events", Filter: map[string]string{"kind": "sudo"}, Logic: "new_value", GroupBy: []string{"host"}, Field: "user", Window: "1h", Learn: "30d",
		Needs: "the host agent"},
	{ID: "ssh_key_added", Name: "SSH key added", Severity: "warning", Tactic: "TA0003", Technique: "T1098.004",
		Description: "A new key in an authorized_keys file: someone may have given themselves a way back in.",
		Source:      "events", Filter: map[string]string{"kind": "ssh_key_added"}, Logic: "threshold", GroupBy: []string{"host"}, Window: "1h", Threshold: 1,
		Needs: "the host agent"},
	{ID: "suspicious_process", Name: "Suspicious process on a host", Severity: "critical", Tactic: "TA0040", Technique: "T1496",
		Description: "A miner, a process from /tmp or a deleted file, or a shell on a socket.",
		Source:      "events", Filter: map[string]string{"kind": "suspicious_process"}, Logic: "threshold", GroupBy: []string{"host"}, Window: "1h", Threshold: 1,
		Needs: "the host agent"},
	{ID: "new_listening_port", Name: "New listening port", Severity: "info", Tactic: "TA0011", Technique: "T1571",
		Description: "A host listens on a port it has not opened in 30 days.",
		Source:      "events", Filter: map[string]string{"kind": "port_opened"}, Logic: "new_value", GroupBy: []string{"host"}, Field: "target", Window: "1h", Learn: "30d",
		Needs: "the host agent"},
	{ID: "new_api_key", Name: "New Visionary API key", Severity: "info", Tactic: "TA0003", Technique: "T1098.001",
		Description: "Someone created a Visionary API key.",
		Source:      "audit", Filter: map[string]string{"action": "api_key.create"}, Logic: "threshold", GroupBy: []string{"user"}, Window: "1h", Threshold: 1},
	{ID: "visionary_data_deleted", Name: "Visionary data deleted", Severity: "warning", Tactic: "TA0040", Technique: "T1485",
		Description: "Stored data was deleted from Settings → Data.",
		Source:      "audit", Filter: map[string]string{"action": "data.delete"}, Logic: "threshold", GroupBy: []string{"user"}, Window: "1h", Threshold: 1},
	{ID: "visionary_log_export", Name: "Bulk log exports from Visionary", Severity: "warning", Tactic: "TA0010", Technique: "T1567",
		Description: "One person exporting logs many times.",
		Source:      "audit", Filter: map[string]string{"action": "logs.export"}, Logic: "threshold", GroupBy: []string{"user"}, Window: "1h", Threshold: 5},
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

// mitreTechniques lists the techniques the templates use; a rule may name
// any technique id.
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
}

// mitreName is "TA0006 Credential Access", or the id when unknown.
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
