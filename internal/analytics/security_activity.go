package analytics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// One timeline of what an address or a host did, across requests, security
// events, sudo after an SSH login from it, app and host logs and the audit log

type activityItem struct {
	At       string         `json:"at"`
	Type     string         `json:"type"`
	Kind     string         `json:"kind,omitempty"`
	Severity string         `json:"severity,omitempty"`
	Host     string         `json:"host,omitempty"`
	Actor    string         `json:"actor,omitempty"`
	IP       string         `json:"ip,omitempty"`
	Method   string         `json:"method,omitempty"`
	Path     string         `json:"path,omitempty"`
	Status   int            `json:"status,omitempty"`
	Text     string         `json:"text,omitempty"`
	Ref      int64          `json:"ref,omitempty"`
	Request  string         `json:"request_id,omitempty"`
	Count    int64          `json:"count,omitempty"`
	Detail   map[string]any `json:"detail,omitempty"`
	// Via says why an item without the address belongs here
	Via string `json:"via,omitempty"`
}

const activityMax = 400

func (s *Server) securityActivityRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/security/ip/{ip}/activity", s.authed(s.handleIPActivity))
	mux.Handle("GET /api/security/probed", s.authed(s.handleProbedPath))
	mux.Handle("GET /api/security/host-events", s.authed(s.handleHostEvents))
	mux.Handle("GET /api/security/incidents", s.authed(s.handleSecurityIncidents))
	mux.Handle("GET /api/security/posture", s.authed(s.handleSecurityPosture))
	mux.Handle("GET /api/security/needs", s.authed(s.handleSecurityNeeds))
}

func sortActivity(items []activityItem) []activityItem {
	slices.SortStableFunc(items, func(a, b activityItem) int { return strings.Compare(b.At, a.At) })
	return items[:min(len(items), activityMax)]
}

func (s *Server) ipActivity(ctx context.Context, ip string, tr timeRange) ([]activityItem, error) {
	from, to := tr.From.Format(tsLayout), tr.To.Format(tsLayout)
	var items []activityItem
	reqs, err := s.db.Query(ctx, `SELECT id, created_at, method, path, status, request_id, COALESCE(user_handle, login_account, ''), service
FROM api_logs WHERE created_at >= ? AND created_at < ? AND ip = ? ORDER BY created_at DESC LIMIT 200`, from, to, ip)
	if err != nil {
		return nil, dbErr(err)
	}
	var requestIDs []any
	for _, row := range reqs.Rows {
		items = append(items, activityItem{At: toString(row[1]), Type: "request", Ref: toInt(row[0]), Method: toString(row[2]), Path: toString(row[3]),
			Status: int(toInt(row[4])), Request: toString(row[5]), Actor: toString(row[6]), Host: toString(row[7]), IP: ip})
		if id := toString(row[5]); id != "" {
			requestIDs = append(requestIDs, id)
		}
	}
	events, err := s.securityEventItems(ctx, "ip = ?", []any{ip}, from, to, 200)
	if err != nil {
		return nil, err
	}
	items = append(items, events...)
	for _, e := range events {
		if e.Kind != "ssh_login" || e.Host == "" || e.Actor == "" {
			continue
		}
		at, err := time.Parse(tsLayout, e.At)
		if err != nil {
			continue
		}
		sudo, err := s.securityEventItems(ctx, "kind = 'sudo' AND host = ? AND actor = ?", []any{e.Host, e.Actor},
			e.At, at.Add(12*time.Hour).Format(tsLayout), 50)
		if err != nil {
			return nil, err
		}
		for i := range sudo {
			sudo[i].Via = "sudo by " + e.Actor + " after signing in over SSH from this address"
		}
		items = append(items, sudo...)
	}
	if len(requestIDs) > 0 {
		logs, err := s.logItems(ctx, `request_id IN (`+strings.TrimSuffix(strings.Repeat("?, ", len(requestIDs)), ", ")+`)`, requestIDs, from, to)
		if err != nil {
			return nil, err
		}
		items = append(items, logs...)
	}
	if ipText.MatchString(ip) && tr.To.Sub(tr.From) <= 7*24*time.Hour {
		logs, err := s.logItems(ctx, `source <> 'app' AND message LIKE ?`, []any{"%" + ip + "%"}, from, to)
		if err != nil {
			return nil, err
		}
		for i := range logs {
			logs[i].Via = "mentions this address"
		}
		items = append(items, logs...)
	}
	audit, err := s.db.Query(ctx, `SELECT id, created_at, user_name, action, target FROM audit_log WHERE created_at >= ? AND created_at < ? AND ip = ? ORDER BY id DESC LIMIT 100`, from, to, ip)
	if err == nil {
		for _, row := range audit.Rows {
			items = append(items, activityItem{At: toString(row[1]), Type: "audit", Ref: toInt(row[0]), Actor: toString(row[2]), Kind: toString(row[3]), Text: toString(row[4]), IP: ip})
		}
	}
	return sortActivity(dedupeActivity(items)), nil
}

// hostActivity is what happened on one host around a host finding
func (s *Server) hostActivity(ctx context.Context, host string, tr timeRange) ([]activityItem, error) {
	from, to := tr.From.Format(tsLayout), tr.To.Format(tsLayout)
	items, err := s.securityEventItems(ctx, "host = ?", []any{host}, from, to, 200)
	if err != nil {
		return nil, err
	}
	logs, err := s.logItems(ctx, `host = ? AND source <> 'app' AND severity >= 13`, []any{host}, from, to)
	if err != nil {
		return nil, err
	}
	return sortActivity(append(items, logs...)), nil
}

var ipText = regexp.MustCompile(`^[0-9a-fA-F:.]{2,45}$`)

func (s *Server) securityEventItems(ctx context.Context, cond string, args []any, from, to string, limit int) ([]activityItem, error) {
	res, err := s.db.Query(ctx, `SELECT id, created_at, source, kind, severity, host, actor, ip, target, detail, count
FROM security_events WHERE created_at >= ? AND created_at < ? AND `+cond+` ORDER BY created_at DESC LIMIT `+strconv.Itoa(limit),
		append([]any{from, to}, args...)...)
	if err != nil {
		return nil, dbErr(err)
	}
	items := make([]activityItem, 0, len(res.Rows))
	for _, row := range res.Rows {
		it := activityItem{At: toString(row[1]), Type: "event", Ref: toInt(row[0]), Kind: toString(row[3]), Severity: toString(row[4]),
			Host: toString(row[5]), Actor: toString(row[6]), IP: toString(row[7]), Text: toString(row[8]), Count: toInt(row[10])}
		if toString(row[2]) == "host" {
			it.Type = "host"
		}
		if d := toString(row[9]); d != "" {
			_ = json.Unmarshal([]byte(d), &it.Detail)
		}
		items = append(items, it)
	}
	return items, nil
}

func (s *Server) logItems(ctx context.Context, cond string, args []any, from, to string) ([]activityItem, error) {
	res, err := s.db.Query(ctx, `SELECT id, created_at, level, message, COALESCE(host, service, ''), request_id
FROM app_logs WHERE created_at >= ? AND created_at < ? AND `+cond+` ORDER BY created_at DESC LIMIT 100`, append([]any{from, to}, args...)...)
	if err != nil {
		return nil, dbErr(err)
	}
	items := make([]activityItem, 0, len(res.Rows))
	for _, row := range res.Rows {
		items = append(items, activityItem{At: toString(row[1]), Type: "log", Ref: toInt(row[0]), Severity: toString(row[2]),
			Text: clip(toString(row[3]), 500), Host: toString(row[4]), Request: toString(row[5])})
	}
	return items, nil
}

func dedupeActivity(items []activityItem) []activityItem {
	seen := map[string]bool{}
	out := items[:0]
	for _, it := range items {
		k := it.Type + "/" + strconv.FormatInt(it.Ref, 10)
		if it.Ref != 0 && seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, it)
	}
	return out
}

// handleIPActivity is the IP panel's timeline, what it asked for, and where it
// is blocked
func (s *Server) handleIPActivity(w http.ResponseWriter, r *http.Request) {
	ip := strings.TrimSpace(r.PathValue("ip"))
	a, err := netip.ParseAddr(ip)
	if err != nil {
		writeError(w, http.StatusBadRequest, "not an IP address")
		return
	}
	tr, err := parseRange(r.URL.Query(), s.maxRangeDays())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := r.Context()
	items, err := s.ipActivity(ctx, ip, tr)
	if err != nil {
		s.fail(w, err)
		return
	}
	routes, err := s.db.Query(ctx, `SELECT method, COALESCE(route, path) AS route, COUNT(*) AS requests,
  `+countIf("status >= 200 AND status < 300")+` AS ok, `+countIf("status >= 400 AND status < 500")+` AS client_errors,
  `+countIf("status >= 500")+` AS server_errors, MAX(created_at) AS last_seen
FROM api_logs WHERE created_at >= ? AND created_at < ? AND ip = ?
GROUP BY method, COALESCE(route, path) ORDER BY requests DESC LIMIT 25`, tr.From.Format(tsLayout), tr.To.Format(tsLayout), ip)
	if err != nil {
		s.fail(w, dbErr(err))
		return
	}
	verdict := activityVerdict(items)
	st := &s.security().st
	login := st.loginCond(s.q)
	signins, err := s.db.Query(ctx, `SELECT `+countIf(login+" AND "+authFailCond(s.q))+`, `+countIf(login+" AND status >= 200 AND status < 400")+`
FROM api_logs WHERE created_at >= ? AND created_at < ? AND ip = ?`, tr.From.Format(tsLayout), tr.To.Format(tsLayout), ip)
	if err != nil {
		s.fail(w, dbErr(err))
		return
	}
	if len(signins.Rows) == 1 {
		failed, ok := toInt(signins.Rows[0][0]), toInt(signins.Rows[0][1])
		verdict["failed_logins"], verdict["logins"] = failed, ok
		if failed >= 3 && ok > 0 {
			verdict["outcome"] = "succeeded"
		}
	}
	out := map[string]any{"ip": ip, "public": publicAddr(a), "items": items, "routes": newResult(routes, 25, "", tr), "verdict": verdict}
	blocked, err := s.blockedBy(ctx, ip)
	if err != nil {
		s.fail(w, err)
		return
	}
	out["block"] = blocked
	writeJSON(w, http.StatusOK, out)
}

// activityVerdict says in a line whether anything this address tried worked
func activityVerdict(items []activityItem) map[string]any {
	var probes, probeOK, fails, logins, sudo, events int
	for _, it := range items {
		switch it.Type {
		case "request":
			if isProbePath(it.Path) {
				probes++
				if it.Status >= 200 && it.Status < 300 {
					probeOK++
				}
			}
			if it.Status == 401 || it.Status == 403 {
				fails++
			}
		case "host", "event":
			events++
			switch it.Kind {
			case "ssh_login":
				logins++
			case "sudo":
				sudo++
			}
		}
	}
	v := map[string]any{"outcome": "attempted", "probes": probes, "probes_answered": probeOK, "refused": fails, "ssh_logins": logins, "sudo": sudo}
	switch {
	case logins > 0 || sudo > 0 || probeOK > 0:
		v["outcome"] = "succeeded"
	case probes == 0 && fails == 0 && events == 0:
		v["outcome"] = "quiet"
	}
	return v
}

// handleProbedPath lists the addresses that asked for one probe path
func (s *Server) handleProbedPath(w http.ResponseWriter, r *http.Request) {
	tr, err := parseRange(r.URL.Query(), s.maxRangeDays())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" || len(path) > 2048 {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	res, err := s.db.Query(r.Context(), `SELECT ip, COUNT(*) AS hits, `+countIf("status >= 200 AND status < 300")+` AS answered_2xx,
  MAX(status) AS last_status, MIN(created_at) AS first_seen, MAX(created_at) AS last_seen, MAX(user_agent) AS user_agent
FROM api_logs WHERE created_at >= ? AND created_at < ? AND path = ? AND ip IS NOT NULL AND ip <> ''
GROUP BY ip ORDER BY answered_2xx DESC, hits DESC LIMIT 200`, tr.From.Format(tsLayout), tr.To.Format(tsLayout), path)
	if err != nil {
		s.fail(w, dbErr(err))
		return
	}
	statuses, err := s.db.Query(r.Context(), `SELECT status, COUNT(*) AS requests FROM api_logs
WHERE created_at >= ? AND created_at < ? AND path = ? GROUP BY status ORDER BY requests DESC`, tr.From.Format(tsLayout), tr.To.Format(tsLayout), path)
	if err != nil {
		s.fail(w, dbErr(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": path, "ips": newResult(res, 200, "", tr), "statuses": newResult(statuses, 50, "", tr)})
}

const hostEventsPage = 200

// handleHostEvents pages through the agent's events with filters on host,
// kind, user and address
func (s *Server) handleHostEvents(w http.ResponseWriter, r *http.Request) {
	tr, err := parseRange(r.URL.Query(), s.maxRangeDays())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	q := r.URL.Query()
	where := []string{"source = 'host'"}
	var args []any
	for _, f := range []struct{ param, col string }{{"host", "host"}, {"kind", "kind"}, {"user", "actor"}, {"ip", "ip"}} {
		v := strings.TrimSpace(q.Get(f.param))
		if v == "" {
			continue
		}
		if !findingKey.MatchString(v) {
			writeError(w, http.StatusBadRequest, "invalid "+f.param)
			return
		}
		where, args = append(where, f.col+" = ?"), append(args, v)
	}
	if v := q.Get("before"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "before must be an id")
			return
		}
		where, args = append(where, "id < ?"), append(args, id)
	}
	items, err := s.securityEventItems(r.Context(), strings.Join(where, " AND "), args, tr.From.Format(tsLayout), tr.To.Format(tsLayout), hostEventsPage+1)
	if err != nil {
		s.fail(w, err)
		return
	}
	slices.SortStableFunc(items, func(a, b activityItem) int { return int(b.Ref - a.Ref) })
	more := len(items) > hostEventsPage
	kinds, err := s.db.Query(r.Context(), `SELECT kind, COUNT(*) FROM security_events WHERE source = 'host' AND created_at >= ? AND created_at < ? GROUP BY kind ORDER BY 2 DESC`,
		tr.From.Format(tsLayout), tr.To.Format(tsLayout))
	if err != nil {
		s.fail(w, dbErr(err))
		return
	}
	counts := map[string]int64{}
	for _, row := range kinds.Rows {
		counts[toString(row[0])] = toInt(row[1])
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": head(items, hostEventsPage), "more": more, "kinds": counts})
}

var probeRegexps = func() []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(probePaths))
	for _, p := range probePaths {
		var b strings.Builder
		b.WriteString("^")
		for i := 0; i < len(p); i++ {
			switch c := p[i]; {
			case c == '!' && i+1 < len(p):
				i++
				b.WriteString(regexp.QuoteMeta(string(p[i])))
			case c == '%':
				b.WriteString(".*")
			case c == '_':
				b.WriteString(".")
			default:
				b.WriteString(regexp.QuoteMeta(string(c)))
			}
		}
		b.WriteString("$")
		out = append(out, regexp.MustCompile(b.String()))
	}
	return out
}()

// isProbePath is probeCond for one path
func isProbePath(path string) bool {
	path = strings.ToLower(path)
	for _, re := range probeRegexps {
		if re.MatchString(path) {
			return true
		}
	}
	return false
}
