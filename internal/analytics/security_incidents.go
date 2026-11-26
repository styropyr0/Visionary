package analytics

import (
	"context"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Incidents group findings by who did them (an address, else an account, else
// a host) into runs with no gap over incidentGap, and say whether it worked

const (
	incidentGap      = time.Hour
	incidentFindings = 5000
	// loginLead lets a sign-in that went through before the finding was written still count
	loginLead = 10 * time.Minute
)

// compromiseKinds mean something already happened, not just an attempt
var compromiseKinds = map[string]bool{
	"risky_account_change": true, "listed_ip_login": true, "impossible_travel": true, "session_spread": true,
	"suspicious_process": true, "ssh_key_added": true, "secret_in_logs": true,
}

// noiseKinds alone, when nothing worked, are the internet's background scans
var noiseKinds = map[string]bool{"scanner": true, "injection_attempt": true}

// stateKinds describe how things are set up rather than an attack: they
// belong to Posture and the audit log, not to incidents
var stateKinds = map[string]bool{
	"container_privileged": true, "container_root": true, "container_new_image": true, "updates_pending": true,
	"port_opened": true, "port_closed": true, "reboot": true, "portal_account_created": true, "portal_account_changed": true,
	"portal_account_deleted": true, "portal_roles_changed": true, "portal_signin_changed": true, "portal_api_key_created": true,
	"portal_data_deleted": true, "portal_password_changed": true, "portal_secret_viewed": true, "portal_two_step_reset": true,
}

// loginAttackKinds followed by a successful sign-in from the same place mean
// the guessing worked
var loginAttackKinds = map[string]bool{
	"auth_bruteforce": true, "auth_bruteforce_slow": true, "credential_stuffing": true, "distributed_login_attack": true,
	"account_targeted": true, "account_targeted_slow": true, "ssh_bruteforce": true, "portal_bruteforce": true,
}

type secStep struct {
	Text   string `json:"text"`
	Action string `json:"action,omitempty"`
	Target string `json:"target,omitempty"`
}

type secIncident struct {
	Key      string    `json:"key"`
	By       string    `json:"by"`
	Value    string    `json:"value"`
	First    string    `json:"first"`
	Last     string    `json:"last"`
	Severity string    `json:"severity"`
	Outcome  string    `json:"outcome"`
	Why      []string  `json:"why"`
	Kinds    []string  `json:"kinds"`
	Findings []int64   `json:"findings"`
	Hosts    []string  `json:"hosts"`
	Accounts []string  `json:"accounts"`
	Count    int64     `json:"count"`
	Open     bool      `json:"open"`
	Steps    []secStep `json:"steps"`
	Block    *block    `json:"block"`
	Summary  string    `json:"summary"`

	firstAt, lastAt time.Time
	kinds           map[string]bool
}

type incidentFinding struct {
	id                                      int64
	at                                      time.Time
	source, kind, severity, host, actor, ip string
	summary                                 string
	target, status                          string
	count                                   int64
}

func (s *Server) handleSecurityIncidents(w http.ResponseWriter, r *http.Request) {
	tr, err := parseRange(r.URL.Query(), s.maxRangeDays())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	list, noise, err := s.incidents(r.Context(), tr)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"incidents": list, "noise": noise})
}

func (s *Server) loadIncidentFindings(ctx context.Context, tr timeRange) ([]incidentFinding, error) {
	res, err := s.db.Query(ctx, `SELECT `+findingColumns+` FROM security_events WHERE created_at >= ? AND created_at < ?
ORDER BY created_at LIMIT `+strconv.Itoa(incidentFindings), tr.From.Format(tsLayout), tr.To.Format(tsLayout))
	if err != nil {
		return nil, dbErr(err)
	}
	out := make([]incidentFinding, 0, len(res.Rows))
	for _, row := range res.Rows {
		f := findingView(row)
		if f == nil {
			continue
		}
		at, err := time.Parse(tsLayout, f["created_at"].(string))
		if err != nil {
			continue
		}
		if stateKinds[f["kind"].(string)] {
			continue
		}
		out = append(out, incidentFinding{id: f["id"].(int64), at: at, source: f["source"].(string), kind: f["kind"].(string), severity: f["severity"].(string),
			host: f["host"].(string), actor: f["actor"].(string), ip: f["ip"].(string), summary: f["summary"].(string),
			target: f["target"].(string), status: f["status"].(string), count: f["count"].(int64)})
	}
	return out, nil
}

// incidentKey picks who an secIncident is about
func incidentKey(f incidentFinding) (string, string) {
	switch {
	case f.ip != "":
		return "ip", f.ip
	case f.host != "" && f.source == "host":
		return "host", f.host
	case f.actor != "" && !strings.HasPrefix(f.actor, "role:"):
		return "account", f.actor
	case f.host != "":
		return "host", f.host
	case f.actor != "":
		return "role", strings.TrimPrefix(f.actor, "role:")
	}
	return "kind", f.kind
}

func groupIncidents(findings []incidentFinding) []*secIncident {
	var all []*secIncident
	open := map[string]*secIncident{}
	for _, f := range findings {
		by, value := incidentKey(f)
		key := by + ":" + value
		inc := open[key]
		if inc == nil || f.at.Sub(inc.lastAt) > incidentGap {
			inc = &secIncident{Key: key + "@" + f.at.Format(time.RFC3339), By: by, Value: value, firstAt: f.at, kinds: map[string]bool{}, Severity: "info"}
			open[key] = inc
			all = append(all, inc)
		}
		inc.lastAt = f.at
		inc.Findings = append(inc.Findings, f.id)
		inc.Count += f.count
		inc.kinds[f.kind] = true
		if severityRank[f.severity] > severityRank[inc.Severity] {
			inc.Severity = f.severity
		}
		if f.status == "open" {
			inc.Open = true
		}
		if f.host != "" && !slices.Contains(inc.Hosts, f.host) {
			inc.Hosts = append(inc.Hosts, f.host)
		}
		if f.actor != "" && f.ip != "" && !strings.HasPrefix(f.actor, "role:") && !slices.Contains(inc.Accounts, f.actor) && len(inc.Accounts) < 20 {
			inc.Accounts = append(inc.Accounts, f.actor)
		}
		if inc.Summary == "" || severityRank[f.severity] >= severityRank[inc.Severity] && f.summary != "" {
			inc.Summary = fallback(f.summary, inc.Summary)
		}
	}
	return all
}

type ipEvidence struct {
	at     time.Time
	probe  bool
	login  bool
	path   string
	status int
	who    string
}

// ipEvidence reads the requests that show an address got somewhere: a probe
// path answered 2xx, or a sign-in that went through
func (s *Server) ipEvidence(ctx context.Context, ips []string, tr timeRange) (map[string][]ipEvidence, error) {
	out := map[string][]ipEvidence{}
	st := &s.security().st
	probe, login := probeCond(s.q), st.loginCond(s.q)
	for i := 0; i < len(ips); i += 500 {
		chunk := ips[i:min(i+500, len(ips))]
		args := []any{tr.From.Format(tsLayout), tr.To.Add(incidentGap).Format(tsLayout)}
		for _, ip := range chunk {
			args = append(args, ip)
		}
		res, err := s.db.Query(ctx, `SELECT ip, created_at, CASE WHEN `+probe+` THEN 1 ELSE 0 END, path, status, COALESCE(`+loginAccountExpr(s.q)+`, '')
FROM api_logs WHERE created_at >= ? AND created_at < ? AND ip IN (`+strings.TrimSuffix(strings.Repeat("?, ", len(chunk)), ", ")+`)
  AND ((`+probe+` AND status >= 200 AND status < 300) OR (`+login+` AND status >= 200 AND status < 400))
ORDER BY created_at LIMIT 5000`, args...)
		if err != nil {
			return nil, dbErr(err)
		}
		for _, row := range res.Rows {
			at, err := time.Parse(tsLayout, toString(row[1]))
			if err != nil {
				continue
			}
			isProbe := toInt(row[2]) == 1
			out[toString(row[0])] = append(out[toString(row[0])], ipEvidence{at: at, probe: isProbe, login: !isProbe, path: toString(row[3]),
				status: int(toInt(row[4])), who: toString(row[5])})
		}
	}
	return out, nil
}

func (s *Server) incidents(ctx context.Context, tr timeRange) ([]*secIncident, map[string]any, error) {
	findings, err := s.loadIncidentFindings(ctx, tr)
	if err != nil {
		return nil, nil, err
	}
	all := groupIncidents(findings)
	var ips []string
	for _, inc := range all {
		if inc.By == "ip" && !slices.Contains(ips, inc.Value) {
			ips = append(ips, inc.Value)
		}
	}
	evidence, err := s.ipEvidence(ctx, ips, tr)
	if err != nil {
		return nil, nil, err
	}
	blocks, err := s.activeBlocks(ctx)
	if err != nil {
		return nil, nil, err
	}
	var out []*secIncident
	noise := map[string]any{"incidents": 0, "addresses": 0, "findings": 0}
	noiseIPs := map[string]bool{}
	for _, inc := range all {
		judge(inc, evidence[inc.Value], blocks)
		if inc.Outcome != "succeeded" && isNoise(inc) {
			noise["incidents"] = noise["incidents"].(int) + 1
			noise["findings"] = noise["findings"].(int) + len(inc.Findings)
			noiseIPs[inc.Value] = true
			continue
		}
		if inc.Outcome != "succeeded" && inc.Severity == "info" {
			continue
		}
		out = append(out, inc)
	}
	noise["addresses"] = len(noiseIPs)
	slices.SortStableFunc(out, func(a, b *secIncident) int {
		if d := outcomeRank[b.Outcome] - outcomeRank[a.Outcome]; d != 0 {
			return d
		}
		if a.Open != b.Open {
			if a.Open {
				return -1
			}
			return 1
		}
		if d := severityRank[b.Severity] - severityRank[a.Severity]; d != 0 {
			return d
		}
		return b.lastAt.Compare(a.lastAt)
	})
	if out == nil {
		out = []*secIncident{}
	}
	return out, noise, nil
}

var outcomeRank = map[string]int{"blocked": 0, "attempted": 1, "succeeded": 2}

func isNoise(inc *secIncident) bool {
	for k := range inc.kinds {
		if !noiseKinds[k] {
			return false
		}
	}
	return true
}

// judge sets the outcome, the reasons for it and what to do next
func judge(inc *secIncident, ev []ipEvidence, blocks []block) {
	inc.First, inc.Last = inc.firstAt.Format(tsLayout), inc.lastAt.Format(tsLayout)
	for k := range inc.kinds {
		inc.Kinds = append(inc.Kinds, k)
	}
	slices.Sort(inc.Kinds)
	inc.Outcome = "attempted"
	attacked := false
	for k := range inc.kinds {
		attacked = attacked || loginAttackKinds[k]
	}
	var probed []string
	for _, e := range ev {
		if e.at.Before(inc.firstAt.Add(-incidentGap)) || e.at.After(inc.lastAt.Add(incidentGap)) {
			continue
		}
		switch {
		case e.probe && !slices.Contains(probed, e.path) && len(probed) < 5:
			probed = append(probed, e.path)
			inc.Why = append(inc.Why, e.path+" answered "+strconv.Itoa(e.status))
			inc.Steps = append(inc.Steps, secStep{Text: "Remove or protect " + e.path + " and rotate any secret it held"})
		case e.login && attacked && !e.at.Before(inc.firstAt.Add(-loginLead)):
			who := fallback(e.who, "an account")
			if !slices.Contains(inc.Why, "signed in as "+who+" after the failed attempts") {
				inc.Why = append(inc.Why, "signed in as "+who+" after the failed attempts")
				if e.who != "" {
					inc.Steps = append(inc.Steps, secStep{Text: "End " + e.who + "'s sessions and reset the password", Action: "end_sessions", Target: e.who})
				}
			}
		}
	}
	if inc.kinds["ssh_login"] && (inc.kinds["ssh_bruteforce"] || inc.kinds["ssh_failed"]) {
		inc.Why = append(inc.Why, "an SSH login went through after failed ones")
		inc.Steps = append(inc.Steps, secStep{Text: "Check what was run on " + strings.Join(inc.Hosts, ", ") + ", rotate SSH keys and turn off password logins"})
	}
	for _, k := range inc.Kinds {
		if compromiseKinds[k] {
			inc.Why = append(inc.Why, kindReason(k))
		}
	}
	if len(inc.Why) > 0 {
		inc.Outcome = "succeeded"
	}
	if inc.By == "ip" {
		if a, err := netip.ParseAddr(inc.Value); err == nil {
			for i := range blocks {
				if blocks[i].covers(a) {
					inc.Block = &blocks[i]
					break
				}
			}
			if inc.Block != nil && inc.Outcome != "succeeded" {
				inc.Outcome = "blocked"
			}
			if inc.Block == nil && publicAddr(a) {
				inc.Steps = append(inc.Steps, secStep{Text: "Block " + inc.Value, Action: "block_ip", Target: inc.Value})
			}
		}
	}
	if inc.kinds["rate_limited"] && inc.Outcome == "attempted" && len(inc.kinds) == 1 {
		inc.Outcome = "blocked"
	}
	if inc.Why == nil {
		inc.Why = []string{}
	}
	if inc.Steps == nil {
		inc.Steps = []secStep{}
	}
	if inc.Hosts == nil {
		inc.Hosts = []string{}
	}
	if inc.Accounts == nil {
		inc.Accounts = []string{}
	}
}

func kindReason(kind string) string {
	switch kind {
	case "risky_account_change":
		return "a password, email or MFA change right after a risky sign-in"
	case "listed_ip_login":
		return "a sign-in went through from a listed address"
	case "impossible_travel":
		return "the same account signed in from places too far apart"
	case "session_spread":
		return "one session was used from many places"
	case "suspicious_process":
		return "a suspicious process ran on the host"
	case "ssh_key_added":
		return "a new SSH key was added"
	case "secret_in_logs":
		return "a secret reached the logs"
	}
	return kind
}
