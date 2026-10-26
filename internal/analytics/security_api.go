package analytics

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func (s *Server) handleSecurityPanel(w http.ResponseWriter, r *http.Request) {
	sql, ok := s.security().panels[r.PathValue("panel")]
	if !ok {
		writeError(w, http.StatusNotFound, "unknown panel")
		return
	}
	tr, err := parseRange(r.URL.Query(), s.maxRangeDays())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := s.runTemplate(r.Context(), sql, tr, true)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

var (
	findingKind = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	findingKey  = regexp.MustCompile(`^[^\x00-\x1f]{1,255}$`)
)

const findingColumns = `id, created_at, source, kind, severity, host, service, actor, ip, target, detail, count, status, note, status_by, status_at`

// findingsPage is how many findings one request returns.
const findingsPage = 100

// handleSecurityFindings lists findings newest first. Filters (all exact,
// bound): source, kind, severity (that level and above), host, ip, actor,
// status (open for untriaged); before (an id) pages.
func (s *Server) handleSecurityFindings(w http.ResponseWriter, r *http.Request) {
	tr, err := parseRange(r.URL.Query(), s.maxRangeDays())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	q := r.URL.Query()
	where := []string{"created_at >= ?", "created_at < ?"}
	args := []any{tr.From.Format(tsLayout), tr.To.Format(tsLayout)}
	for _, f := range []struct {
		param string
		re    *regexp.Regexp
	}{{"source", findingKind}, {"kind", findingKind}, {"host", findingKey}, {"ip", findingKey}, {"actor", findingKey}} {
		v := strings.TrimSpace(q.Get(f.param))
		if v == "" {
			continue
		}
		if !f.re.MatchString(v) {
			writeError(w, http.StatusBadRequest, "invalid "+f.param)
			return
		}
		where, args = append(where, f.param+" = ?"), append(args, v)
	}
	if v := q.Get("severity"); v != "" {
		levels := severitiesFrom(v)
		if levels == nil {
			writeError(w, http.StatusBadRequest, "severity must be info, warning or critical")
			return
		}
		where = append(where, "severity IN ("+strings.TrimSuffix(strings.Repeat("?, ", len(levels)), ", ")+")")
		for _, l := range levels {
			args = append(args, l)
		}
	}
	switch v := q.Get("status"); v {
	case "":
	case "open":
		where = append(where, "(status IS NULL OR status = '')")
	case "acknowledged", "resolved", "false_positive":
		where, args = append(where, "status = ?"), append(args, v)
	default:
		writeError(w, http.StatusBadRequest, "status must be open, acknowledged, resolved or false_positive")
		return
	}
	if v := q.Get("before"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "before must be an id")
			return
		}
		where, args = append(where, "id < ?"), append(args, id)
	}
	res, err := s.db.Query(r.Context(), `SELECT `+findingColumns+` FROM security_events WHERE `+strings.Join(where, " AND ")+`
ORDER BY id DESC LIMIT `+strconv.Itoa(findingsPage+1), args...)
	if err != nil {
		s.fail(w, dbErr(err))
		return
	}
	list := make([]map[string]any, 0, len(res.Rows))
	for _, row := range res.Rows {
		if f := findingView(row); f != nil {
			list = append(list, f)
		}
	}
	more := len(list) > findingsPage
	writeJSON(w, http.StatusOK, map[string]any{"findings": head(list, findingsPage), "more": more})
}

// severitiesFrom is the level and the ones above it.
func severitiesFrom(v string) []string {
	switch v {
	case "info":
		return []string{"info", "warning", "critical"}
	case "warning":
		return []string{"warning", "critical"}
	case "critical":
		return []string{"critical"}
	}
	return nil
}

func findingView(row []any) map[string]any {
	if len(row) != 16 {
		return nil
	}
	var detail map[string]any
	if json.Unmarshal([]byte(toString(row[10])), &detail) != nil || detail == nil {
		detail = map[string]any{}
	}
	summary, _ := detail["summary"].(string)
	return map[string]any{
		"id": toInt(row[0]), "created_at": toString(row[1]), "source": toString(row[2]), "kind": toString(row[3]),
		"severity": toString(row[4]), "host": toString(row[5]), "service": toString(row[6]), "actor": toString(row[7]),
		"ip": toString(row[8]), "target": toString(row[9]), "detail": detail, "summary": summary, "count": toInt(row[11]),
		"status": fallback(toString(row[12]), "open"), "note": toString(row[13]), "status_by": toString(row[14]), "status_at": toString(row[15]),
	}
}

// handleSecurityFinding returns one finding with the requests around it: the
// same IP's (or account's, or role's) api_logs rows from a window before it was
// first seen to the end of its dedupe period.
func (s *Server) handleSecurityFinding(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid finding id")
		return
	}
	res, err := s.db.Query(r.Context(), `SELECT `+findingColumns+` FROM security_events WHERE id = ?`, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if len(res.Rows) == 0 {
		writeError(w, http.StatusNotFound, "finding not found")
		return
	}
	f := findingView(res.Rows[0])
	at, err := time.Parse(tsLayout, f["created_at"].(string))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "finding has an invalid time")
		return
	}
	st := s.security().st
	kind := f["kind"].(string)
	back := st.window
	if slowKinds[kind] {
		back = st.slowWindow
	}
	tr := timeRange{From: at.Add(-back - time.Minute), To: at.Add(st.dedupeFor(kind) + time.Minute)}
	out := map[string]any{"finding": f, "from": tr.From.Format(time.RFC3339Nano), "to": tr.To.Format(time.RFC3339Nano)}
	if f["source"] == "audit" {
		if err := s.findingAudit(r.Context(), out, f, tr); err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
		return
	}

	ip, actor := f["ip"].(string), f["actor"].(string)
	var cond string
	var args []any
	switch {
	case ip != "":
		cond, args = "ip = ?", []any{ip}
		out["match"] = "ip"
	case strings.HasPrefix(actor, "role:"):
		cond, args = "user_role = ?", []any{strings.TrimPrefix(actor, "role:")}
		out["match"] = "role"
	case actor != "" && f["source"] == "api":
		cond, args = "(user_handle = ? OR "+s.q.text("user_id")+" = ? OR login_account = ?)", []any{actor, actor, actor}
		out["match"] = "user"
	}
	if cond == "" {
		writeJSON(w, http.StatusOK, out)
		return
	}
	span := append([]any{tr.From.Format(tsLayout), tr.To.Format(tsLayout)}, args...)
	reqs, err := s.db.Query(r.Context(), `SELECT id, created_at, method, path, route, status, duration_ms, user_id, user_handle, login_account, user_agent, error_key
FROM api_logs WHERE created_at >= ? AND created_at < ? AND `+cond+`
ORDER BY created_at DESC LIMIT 100`, span...)
	if err != nil {
		s.fail(w, dbErr(err))
		return
	}
	out["requests"] = newResult(reqs, 100, "", tr)
	statuses, err := s.db.Query(r.Context(), `SELECT status, COUNT(*) AS requests FROM api_logs
WHERE created_at >= ? AND created_at < ? AND `+cond+` GROUP BY status ORDER BY requests DESC`, span...)
	if err != nil {
		s.fail(w, dbErr(err))
		return
	}
	out["statuses"] = newResult(statuses, 50, "", tr)
	writeJSON(w, http.StatusOK, out)
}

// sensitiveActions are the audit actions the Security page and digest show:
// sign-ins and their failures, API keys, notification channels and role
// mapping. Text ranges are prefix matches that need no LIKE escaping.
var sensitiveActions = []string{"auth.", "api_key.", "alert.channel.", "team."}

func sensitiveActionCond() string {
	parts := make([]string, len(sensitiveActions))
	for i, a := range sensitiveActions {
		parts[i] = "(action >= '" + a + "' AND action < '" + strings.TrimSuffix(a, ".") + "/')"
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}

// handleSecurityAudit lists sensitive actions from Visionary's own audit log
// over the range, read in place. Admins only, like the audit log itself.
func (s *Server) handleSecurityAudit(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	tr, err := parseRange(r.URL.Query(), s.maxRangeDays())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	entries, counts, err := s.sensitiveAudit(r.Context(), tr.From, tr.To, 200)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "counts": counts})
}

type auditCount struct {
	Action string `json:"action"`
	Count  int64  `json:"count"`
}

func (s *Server) sensitiveAudit(ctx context.Context, from, to time.Time, limit int) ([]map[string]any, []auditCount, error) {
	span := []any{from.UTC().Format(tsLayout), to.UTC().Format(tsLayout)}
	rows, err := s.db.Query(ctx, `SELECT id, created_at, user_id, user_name, role, action, target, detail, ip FROM audit_log
WHERE created_at >= ? AND created_at < ? AND `+sensitiveActionCond()+`
ORDER BY id DESC LIMIT `+strconv.Itoa(limit), span...)
	if err != nil {
		return nil, nil, err
	}
	entries := make([]map[string]any, 0, len(rows.Rows))
	for _, row := range rows.Rows {
		if len(row) != 9 {
			continue
		}
		var detail any
		if json.Unmarshal([]byte(toString(row[7])), &detail) != nil {
			detail = map[string]any{}
		}
		entries = append(entries, map[string]any{
			"id": toInt(row[0]), "created_at": toString(row[1]), "user_id": toString(row[2]), "user_name": toString(row[3]),
			"role": toString(row[4]), "action": toString(row[5]), "target": toString(row[6]), "detail": detail, "ip": toString(row[8]),
		})
	}
	res, err := s.db.Query(ctx, `SELECT action, COUNT(*) AS n FROM audit_log
WHERE created_at >= ? AND created_at < ? AND `+sensitiveActionCond()+` GROUP BY action ORDER BY n DESC`, span...)
	if err != nil {
		return nil, nil, err
	}
	counts := []auditCount{}
	for _, row := range res.Rows {
		if len(row) == 2 {
			counts = append(counts, auditCount{toString(row[0]), toInt(row[1])})
		}
	}
	return entries, counts, nil
}

// auditRoleMapping writes a team.role_mapping audit entry at startup when
// the sign-in mode or role mapping (set in the environment, so never changed
// through the API) differs from the last one recorded.
func (s *Server) auditRoleMapping(ctx context.Context) {
	sg := s.signin()
	cur, _ := json.Marshal(map[string]any{"mode": sg.cfg.Type, "mapping": sg.roles.entries})
	for attempt := 0; attempt < 10; attempt++ {
		res, err := s.db.Query(ctx, `SELECT detail FROM audit_log WHERE action = 'team.role_mapping' ORDER BY id DESC LIMIT 1`)
		if err == nil {
			var prev struct {
				Mode    string          `json:"mode"`
				Mapping json.RawMessage `json:"mapping"`
			}
			if len(res.Rows) == 1 {
				_ = json.Unmarshal([]byte(toString(res.Rows[0][0])), &prev)
				was, _ := json.Marshal(map[string]any{"mode": prev.Mode, "mapping": prev.Mapping})
				if string(was) == string(cur) {
					return
				}
			}
			detail := map[string]any{"mode": sg.cfg.Type, "mapping": sg.roles.entries}
			if len(res.Rows) == 1 {
				detail["previous"] = json.RawMessage(toString(res.Rows[0][0]))
			}
			b, _ := json.Marshal(detail)
			e := auditEntry{at: time.Now().UTC(), userID: "system", userName: "Visionary", action: "team.role_mapping",
				target: sg.cfg.Type, detailJSON: string(b)}
			select {
			case s.team().audits <- e:
			default:
			}
			return
		}
		if ctx.Err() != nil {
			return
		}
		if !missingTable(err, "audit_log") {
			s.logger.Warn("security: read role mapping audit", "err", err)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
	}
}

// maskIdentifier keeps enough of a sign-in identifier to recognise it in
// the audit log without storing the whole email or phone number.
func maskIdentifier(id string) string {
	r := []rune(strings.TrimSpace(id))
	switch {
	case len(r) == 0:
		return ""
	case len(r) <= 4:
		return string(r[:1]) + "•••"
	}
	if at := strings.IndexRune(string(r), '@'); at > 0 {
		local := []rune(string(r)[:at])
		return string(local[:1]) + "•••" + string(r)[at:]
	}
	return string(r[:2]) + "•••" + string(r[len(r)-2:])
}
