package analytics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	maxIncidentTitle = 200
	maxIncidentText  = 4000
	maxIncidentList  = 500
)

func incidentID(r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	return id, err == nil && id > 0
}

func (s *Server) incidentFromPath(w http.ResponseWriter, r *http.Request) (*declaredIncident, bool) {
	id, ok := incidentID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid incident id")
		return nil, false
	}
	in, err := s.getIncident(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return nil, false
	}
	if in == nil {
		writeError(w, http.StatusNotFound, "incident not found")
		return nil, false
	}
	return in, true
}

func (s *Server) addIncidentEntry(ctx context.Context, id int64, kind, actor, body string, detail map[string]any) (int64, error) {
	if detail == nil {
		detail = map[string]any{}
	}
	b, _ := json.Marshal(detail)
	now := nowTS()
	eid, err := s.db.InsertID(ctx, `INSERT INTO incident_timeline (incident_id, at, kind, actor, body, detail) VALUES (?, ?, ?, ?, ?, ?)`,
		id, now, kind, actor, body, string(b))
	if err != nil {
		return 0, err
	}
	return eid, s.db.Exec(ctx, `UPDATE incidents SET updated_at = ? WHERE id = ?`, now, id)
}

func cleanServices(list []string) ([]string, string) {
	out := []string{}
	for _, sv := range list {
		if sv = strings.TrimSpace(sv); sv != "" && !slices.Contains(out, sv) {
			if len(sv) > 120 {
				return nil, "service names must be under 120 characters"
			}
			out = append(out, sv)
		}
	}
	if len(out) > 20 {
		return nil, "name at most 20 affected services"
	}
	return out, ""
}

func parseStart(v string) (string, bool) {
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		t, err = time.Parse(tsLayout, v)
	}
	if err != nil {
		return "", false
	}
	if now := time.Now().UTC(); t.After(now) {
		t = now
	}
	return t.UTC().Format(tsLayout), true
}

func (s *Server) handleListIncidents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from := time.Now().UTC().AddDate(0, 0, -90)
	if v := q.Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "from must be an RFC 3339 timestamp")
			return
		}
		from = t.UTC()
	}
	where, args := []string{"(resolved_at = '' OR declared_at >= ?)"}, []any{from.Format(tsLayout)}
	if v := q.Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "to must be an RFC 3339 timestamp")
			return
		}
		where, args = append(where, "declared_at < ?"), append(args, t.UTC().Format(tsLayout))
	}
	switch q.Get("status") {
	case "active":
		where = append(where, "resolved_at = ''")
	case "resolved":
		where = append(where, "resolved_at <> ''")
	}
	if sev := q.Get("severity"); sev != "" {
		if !slices.Contains(incidentSeverities, sev) {
			writeError(w, http.StatusBadRequest, "unknown severity")
			return
		}
		where, args = append(where, "severity = ?"), append(args, sev)
	}
	list, err := s.loadIncidents(r.Context(), strings.Join(where, " AND "), args, maxIncidentList)
	if err != nil {
		s.fail(w, err)
		return
	}
	service, text, person := q.Get("service"), strings.ToLower(strings.TrimSpace(q.Get("q"))), q.Get("person")
	out := list[:0]
	for _, in := range list {
		if service != "" && !slices.Contains(in.Services, service) {
			continue
		}
		if text != "" && !strings.Contains(strings.ToLower(in.Title+" "+in.Impact), text) {
			continue
		}
		if person != "" && !slices.ContainsFunc(in.Roles, func(r incidentRole) bool { return r.User == person }) {
			continue
		}
		out = append(out, in)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.ResolvedAt == "") != (b.ResolvedAt == "") {
			return a.ResolvedAt == ""
		}
		if a.ResolvedAt == "" && a.Severity != b.Severity {
			return a.Severity < b.Severity
		}
		return a.DeclaredAt > b.DeclaredAt
	})
	writeJSON(w, http.StatusOK, map[string]any{"incidents": out, "can_edit": s.can(r, "edit")})
}

func (s *Server) handleIncidentStats(w http.ResponseWriter, r *http.Request) {
	tr, err := parseRange(r.URL.Query(), 3650)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	list, err := s.loadIncidents(r.Context(), "declared_at >= ? AND declared_at < ?", []any{tr.From.UTC().Format(tsLayout), tr.To.UTC().Format(tsLayout)}, 5000)
	if err != nil {
		s.fail(w, err)
		return
	}
	open, err := s.loadIncidents(r.Context(), "resolved_at = ''", nil, maxIncidentList)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := incidentStats(list, tr.From.UTC(), tr.To.UTC())
	unassigned := 0
	for _, in := range open {
		if in.Commander == "" {
			unassigned++
		}
	}
	out["open"], out["unassigned"] = len(open), unassigned
	writeJSON(w, http.StatusOK, out)
}

type incidentBucket struct {
	At     string         `json:"at"`
	Counts map[string]int `json:"counts"`
}

func incidentStats(list []declaredIncident, from, to time.Time) map[string]any {
	step := 24 * time.Hour
	switch span := to.Sub(from); {
	case span <= 48*time.Hour:
		step = time.Hour
	case span > 62*24*time.Hour:
		step = 7 * 24 * time.Hour
	}
	start := from.Truncate(min(step, 24*time.Hour))
	var buckets []incidentBucket
	for t := start; t.Before(to); t = t.Add(step) {
		buckets = append(buckets, incidentBucket{At: t.Format(tsLayout), Counts: map[string]int{}})
	}
	bySev := map[string]int{}
	var sums [3]int64
	var ns [3]int64
	active := 0
	for _, in := range list {
		bySev[in.Severity]++
		if in.ResolvedAt == "" {
			active++
		}
		if d, err := time.Parse(tsLayout, in.DeclaredAt); err == nil && len(buckets) > 0 {
			if i := int(d.Sub(start) / step); i >= 0 && i < len(buckets) {
				buckets[i].Counts[in.Severity]++
			}
		}
		for i, v := range []*int64{in.TTA, in.TTM, in.TTR} {
			if v != nil {
				sums[i] += *v
				ns[i]++
			}
		}
	}
	mean := func(i int) *int64 {
		if ns[i] == 0 {
			return nil
		}
		v := sums[i] / ns[i]
		return &v
	}
	if buckets == nil {
		buckets = []incidentBucket{}
	}
	return map[string]any{
		"total": len(list), "active": active, "by_severity": bySev, "step_s": int64(step.Seconds()), "buckets": buckets,
		"mtta_s": mean(0), "mttm_s": mean(1), "mttr_s": mean(2),
	}
}

type declareInput struct {
	Title        string   `json:"title"`
	Severity     string   `json:"severity"`
	Services     []string `json:"services"`
	Impact       string   `json:"impact"`
	StartedAt    string   `json:"started_at"`
	SourceKind   string   `json:"source_kind"`
	SourceRef    string   `json:"source_ref"`
	Commander    string   `json:"commander"`
	OnStatusPage bool     `json:"on_status_page"`
	Channels     []int64  `json:"channels"`
}

func (s *Server) handleDeclareIncident(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "edit") {
		return
	}
	var in declareInput
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	in.Title, in.Impact, in.Commander = strings.TrimSpace(in.Title), strings.TrimSpace(in.Impact), strings.TrimSpace(in.Commander)
	switch {
	case in.Title == "" || len(in.Title) > maxIncidentTitle:
		writeError(w, http.StatusBadRequest, "the title must be 1–200 characters")
		return
	case !slices.Contains(incidentSeverities, in.Severity):
		writeError(w, http.StatusBadRequest, "severity must be sev1, sev2, sev3 or sev4")
		return
	case len(in.Impact) > maxIncidentText:
		writeError(w, http.StatusBadRequest, "the impact summary must be under 4000 characters")
		return
	case len(in.Commander) > 255:
		writeError(w, http.StatusBadRequest, "the commander's name is too long")
		return
	}
	services, msg := cleanServices(in.Services)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	ctx := r.Context()
	var src *incidentSource
	if in.SourceKind != "" {
		var err error
		if src, err = s.resolveIncidentSource(ctx, in.SourceKind, in.SourceRef); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	now := nowTS()
	started := now
	if in.StartedAt != "" {
		var ok bool
		if started, ok = parseStart(in.StartedAt); !ok {
			writeError(w, http.StatusBadRequest, "started_at must be an RFC 3339 timestamp")
			return
		}
	} else if src != nil && src.StartedAt != "" {
		started = src.StartedAt
	}
	by := fallback(actorName(r), "someone")
	svc, _ := json.Marshal(services)
	acked := ""
	if in.Commander != "" {
		acked = now
	}
	kind, ref := "", ""
	if src != nil {
		kind, ref = src.Kind, src.Ref
	}
	id, err := s.db.InsertID(ctx, `INSERT INTO incidents (title, severity, status, services, impact, source_kind, source_ref, on_status_page, created_by,
started_at, declared_at, acknowledged_at, identified_at, mitigated_at, resolved_at, updated_at, postmortem)
VALUES (?, ?, 'investigating', ?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', '', ?, '')`,
		in.Title, in.Severity, string(svc), in.Impact, kind, ref, boolInt(in.OnStatusPage), by, started, now, acked, now)
	if err != nil {
		s.fail(w, err)
		return
	}
	body := "Declared " + severityLabel(in.Severity) + " by " + by + "."
	if _, err := s.addIncidentEntry(ctx, id, "declared", by, body, map[string]any{"severity": in.Severity, "services": services}); err != nil {
		s.fail(w, err)
		return
	}
	if src != nil {
		if err := s.linkIncident(ctx, id, src.Kind, src.Ref, src.Label, by); err != nil {
			s.fail(w, err)
			return
		}
	}
	if in.Commander != "" {
		if err := s.assignRole(ctx, id, "commander", in.Commander, by); err != nil {
			s.fail(w, err)
			return
		}
	}
	if in.OnStatusPage {
		dropStatusCache(s)
	}
	inc, err := s.getIncident(ctx, id)
	if err != nil || inc == nil {
		s.fail(w, err)
		return
	}
	if len(in.Channels) > 0 {
		line := in.Impact
		if line == "" {
			line = "A new incident was declared."
		}
		if _, err := s.postIncidentUpdate(ctx, inc, by, line, in.Channels, false); err != nil {
			s.logger.Warn("incidents: notify on declare", "err", err)
		}
	}
	s.audit(r, "incident.declare", inc.Title, map[string]any{"id": id, "severity": in.Severity, "source": kind})
	writeJSON(w, http.StatusCreated, inc)
}

func (s *Server) handleGetIncident(w http.ResponseWriter, r *http.Request) {
	in, ok := s.incidentFromPath(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	links, err := s.incidentLinks(ctx, in.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	timeline, err := s.incidentTimeline(ctx, in, links)
	if err != nil {
		s.fail(w, err)
		return
	}
	actions, err := s.incidentActions(ctx, in.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"incident": in, "links": links, "timeline": timeline, "actions": actions, "postmortem": parsePostmortem(in.postmortem),
		"can_edit": s.can(r, "edit"), "can_admin": s.can(r, "admin"), "llm": s.llm() != nil, "status_page": s.statusEnabled(),
	})
}

type editIncidentInput struct {
	Title        *string   `json:"title"`
	Severity     *string   `json:"severity"`
	Services     *[]string `json:"services"`
	Impact       *string   `json:"impact"`
	StartedAt    *string   `json:"started_at"`
	OnStatusPage *bool     `json:"on_status_page"`
}

func (s *Server) handleEditIncident(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "edit") {
		return
	}
	in, ok := s.incidentFromPath(w, r)
	if !ok {
		return
	}
	var e editIncidentInput
	if err := readJSON(r, &e); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var sets []string
	var args []any
	var changed []string
	by := fallback(actorName(r), "someone")
	ctx := r.Context()
	if e.Title != nil && strings.TrimSpace(*e.Title) != in.Title {
		t := strings.TrimSpace(*e.Title)
		if t == "" || len(t) > maxIncidentTitle {
			writeError(w, http.StatusBadRequest, "the title must be 1–200 characters")
			return
		}
		sets, args, changed = append(sets, "title = ?"), append(args, t), append(changed, "title")
	}
	if e.Impact != nil && strings.TrimSpace(*e.Impact) != in.Impact {
		t := strings.TrimSpace(*e.Impact)
		if len(t) > maxIncidentText {
			writeError(w, http.StatusBadRequest, "the impact summary must be under 4000 characters")
			return
		}
		sets, args, changed = append(sets, "impact = ?"), append(args, t), append(changed, "impact")
	}
	if e.Services != nil {
		list, msg := cleanServices(*e.Services)
		if msg != "" {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		if !slices.Equal(list, in.Services) {
			b, _ := json.Marshal(list)
			sets, args, changed = append(sets, "services = ?"), append(args, string(b)), append(changed, "affected services")
		}
	}
	if e.StartedAt != nil {
		t, ok := parseStart(*e.StartedAt)
		if !ok {
			writeError(w, http.StatusBadRequest, "started_at must be an RFC 3339 timestamp")
			return
		}
		if t != in.StartedAt {
			sets, args, changed = append(sets, "started_at = ?"), append(args, t), append(changed, "start time")
		}
	}
	if e.OnStatusPage != nil && *e.OnStatusPage != in.OnStatusPage {
		sets, args = append(sets, "on_status_page = ?"), append(args, boolInt(*e.OnStatusPage))
		changed = append(changed, map[bool]string{true: "shown on the status page", false: "hidden from the status page"}[*e.OnStatusPage])
	}
	sevChanged := e.Severity != nil && *e.Severity != in.Severity
	if sevChanged {
		if !slices.Contains(incidentSeverities, *e.Severity) {
			writeError(w, http.StatusBadRequest, "severity must be sev1, sev2, sev3 or sev4")
			return
		}
		sets, args = append(sets, "severity = ?"), append(args, *e.Severity)
	}
	if len(sets) > 0 {
		if err := s.db.Exec(ctx, `UPDATE incidents SET `+strings.Join(sets, ", ")+` WHERE id = ?`, append(args, in.ID)...); err != nil {
			s.fail(w, err)
			return
		}
	}
	if sevChanged {
		body := "Severity changed from " + severityLabel(in.Severity) + " to " + severityLabel(*e.Severity) + "."
		if _, err := s.addIncidentEntry(ctx, in.ID, "severity", by, body, map[string]any{"from": in.Severity, "to": *e.Severity}); err != nil {
			s.fail(w, err)
			return
		}
	}
	if len(changed) > 0 {
		if _, err := s.addIncidentEntry(ctx, in.ID, "edit", by, "Changed the "+strings.Join(changed, ", ")+".", nil); err != nil {
			s.fail(w, err)
			return
		}
	}
	if e.OnStatusPage != nil || in.OnStatusPage {
		dropStatusCache(s)
	}
	out, err := s.getIncident(ctx, in.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	if sevChanged || len(changed) > 0 {
		s.audit(r, "incident.update", out.Title, map[string]any{"id": in.ID, "changed": changed, "severity": out.Severity})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeleteIncident(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	in, ok := s.incidentFromPath(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	for _, t := range []string{"incident_timeline", "incident_roles", "incident_links", "incident_actions"} {
		if err := s.db.Exec(ctx, `DELETE FROM `+t+` WHERE incident_id = ?`, in.ID); err != nil {
			s.fail(w, err)
			return
		}
	}
	if err := s.db.Exec(ctx, `DELETE FROM incidents WHERE id = ?`, in.ID); err != nil {
		s.fail(w, err)
		return
	}
	if in.OnStatusPage {
		dropStatusCache(s)
	}
	s.audit(r, "incident.delete", in.Title, map[string]any{"id": in.ID})
	w.WriteHeader(http.StatusNoContent)
}

// A status change also counts as acknowledging, and reopening clears the resolution
func (s *Server) setIncidentStatus(ctx context.Context, in *declaredIncident, status, by, note string) error {
	now := nowTS()
	sets, args := []string{"status = ?"}, []any{status}
	stamp := func(col, cur string) {
		if cur == "" {
			sets, args = append(sets, col+" = ?"), append(args, now)
		}
	}
	stamp("acknowledged_at", in.AckedAt)
	switch status {
	case "identified":
		stamp("identified_at", in.IdentifiedAt)
	case "monitoring":
		stamp("mitigated_at", in.MitigatedAt)
	case "resolved":
		stamp("mitigated_at", in.MitigatedAt)
		stamp("resolved_at", in.ResolvedAt)
	}
	if status != "resolved" && in.ResolvedAt != "" {
		sets = append(sets, "resolved_at = ''")
	}
	if err := s.db.Exec(ctx, `UPDATE incidents SET `+strings.Join(sets, ", ")+` WHERE id = ?`, append(args, in.ID)...); err != nil {
		return err
	}
	body := incidentStatusLabels[in.Status] + " → " + incidentStatusLabels[status] + "."
	if in.ResolvedAt != "" && status != "resolved" {
		body = "Reopened: " + body
	}
	_, err := s.addIncidentEntry(ctx, in.ID, "status", by, body, map[string]any{"from": in.Status, "to": status, "note": note})
	if in.OnStatusPage {
		dropStatusCache(s)
	}
	return err
}

func (s *Server) handleIncidentStatus(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "edit") {
		return
	}
	in, ok := s.incidentFromPath(w, r)
	if !ok {
		return
	}
	var body struct {
		Status string `json:"status"`
		Note   string `json:"note"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !slices.Contains(incidentStatuses, body.Status) {
		writeError(w, http.StatusBadRequest, "status must be investigating, identified, monitoring or resolved")
		return
	}
	if len(body.Note) > maxIncidentText {
		writeError(w, http.StatusBadRequest, "the note must be under 4000 characters")
		return
	}
	if body.Status == in.Status {
		writeError(w, http.StatusConflict, "the incident is already "+body.Status)
		return
	}
	by := fallback(actorName(r), "someone")
	if err := s.setIncidentStatus(r.Context(), in, body.Status, by, strings.TrimSpace(body.Note)); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "incident.status", in.Title, map[string]any{"id": in.ID, "from": in.Status, "to": body.Status})
	s.writeIncident(w, r, in.ID)
}

func (s *Server) writeIncident(w http.ResponseWriter, r *http.Request, id int64) {
	out, err := s.getIncident(r.Context(), id)
	if err != nil || out == nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) assignRole(ctx context.Context, id int64, role, user, by string) error {
	now := nowTS()
	if role == "commander" {
		if err := s.db.Exec(ctx, `DELETE FROM incident_roles WHERE incident_id = ? AND role = 'commander'`, id); err != nil {
			return err
		}
		if err := s.db.Exec(ctx, `UPDATE incidents SET acknowledged_at = ? WHERE id = ? AND acknowledged_at = ''`, now, id); err != nil {
			return err
		}
	} else if err := s.db.Exec(ctx, `DELETE FROM incident_roles WHERE incident_id = ? AND role = ? AND user_name = ?`, id, role, user); err != nil {
		return err
	}
	if err := s.db.Exec(ctx, `INSERT INTO incident_roles (incident_id, role, user_name, assigned_by, assigned_at) VALUES (?, ?, ?, ?, ?)`, id, role, user, by, now); err != nil {
		return err
	}
	body := user + " is now " + roleName(role) + "."
	if user == by {
		body = user + " took the " + roleName(role) + " role."
	}
	_, err := s.addIncidentEntry(ctx, id, "role", by, body, map[string]any{"role": role, "user": user})
	return err
}

func roleName(role string) string {
	if role == "commander" {
		return "incident commander"
	}
	return role + " lead"
}

func (s *Server) handleAssignRole(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "edit") {
		return
	}
	in, ok := s.incidentFromPath(w, r)
	if !ok {
		return
	}
	var body struct {
		Role string `json:"role"`
		User string `json:"user"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	body.User = strings.TrimSpace(body.User)
	by := fallback(actorName(r), "someone")
	if body.User == "" {
		body.User = by
	}
	if !slices.Contains(incidentRoles, body.Role) {
		writeError(w, http.StatusBadRequest, "role must be one of "+strings.Join(incidentRoles, ", "))
		return
	}
	if len(body.User) > 255 {
		writeError(w, http.StatusBadRequest, "the name is too long")
		return
	}
	if err := s.assignRole(r.Context(), in.ID, body.Role, body.User, by); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "incident.role", in.Title, map[string]any{"id": in.ID, "role": body.Role, "user": body.User})
	s.writeIncident(w, r, in.ID)
}

func (s *Server) handleRemoveRole(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "edit") {
		return
	}
	in, ok := s.incidentFromPath(w, r)
	if !ok {
		return
	}
	rid, ok := incidentID(r, "rid")
	idx := slices.IndexFunc(in.Roles, func(x incidentRole) bool { return x.ID == rid })
	if !ok || idx < 0 {
		writeError(w, http.StatusNotFound, "role not found")
		return
	}
	role := in.Roles[idx]
	ctx := r.Context()
	if err := s.db.Exec(ctx, `DELETE FROM incident_roles WHERE id = ? AND incident_id = ?`, rid, in.ID); err != nil {
		s.fail(w, err)
		return
	}
	by := fallback(actorName(r), "someone")
	if _, err := s.addIncidentEntry(ctx, in.ID, "role", by, role.User+" is no longer "+roleName(role.Role)+".", map[string]any{"role": role.Role, "user": role.User, "removed": true}); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "incident.role.remove", in.Title, map[string]any{"id": in.ID, "role": role.Role, "user": role.User})
	s.writeIncident(w, r, in.ID)
}

func validLink(v string) bool {
	if v == "" || strings.HasPrefix(v, "#/") {
		return true
	}
	u, err := url.Parse(v)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

func (s *Server) handleIncidentNote(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "edit") {
		return
	}
	in, ok := s.incidentFromPath(w, r)
	if !ok {
		return
	}
	var body struct {
		Body string `json:"body"`
		Link string `json:"link"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	body.Body, body.Link = strings.TrimSpace(body.Body), strings.TrimSpace(body.Link)
	if body.Body == "" || len(body.Body) > maxIncidentText {
		writeError(w, http.StatusBadRequest, "the note must be 1–4000 characters")
		return
	}
	if len(body.Link) > 2000 || !validLink(body.Link) {
		writeError(w, http.StatusBadRequest, "the link must be an http(s) address or a dashboard link starting with #/")
		return
	}
	var detail map[string]any
	if body.Link != "" {
		detail = map[string]any{"link": body.Link}
	}
	by := fallback(actorName(r), "someone")
	if _, err := s.addIncidentEntry(r.Context(), in.ID, "note", by, body.Body, detail); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "incident.note", in.Title, map[string]any{"id": in.ID})
	w.WriteHeader(http.StatusNoContent)
}

type incidentLink struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"`
	Ref       string `json:"ref"`
	Label     string `json:"label"`
	CreatedBy string `json:"created_by"`
	CreatedAt string `json:"created_at"`
}

func (s *Server) incidentLinks(ctx context.Context, id int64) ([]incidentLink, error) {
	res, err := s.db.Query(ctx, `SELECT id, kind, ref, label, created_by, created_at FROM incident_links WHERE incident_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	out := make([]incidentLink, 0, len(res.Rows))
	for _, row := range res.Rows {
		out = append(out, incidentLink{toInt(row[0]), toString(row[1]), toString(row[2]), toString(row[3]), toString(row[4]), toString(row[5])})
	}
	return out, nil
}

func (s *Server) linkIncident(ctx context.Context, id int64, kind, ref, label, by string) error {
	if err := s.db.Exec(ctx, `INSERT INTO incident_links (incident_id, kind, ref, label, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, kind, ref, label, by, nowTS()); err != nil {
		return err
	}
	_, err := s.addIncidentEntry(ctx, id, "link", by, "Linked "+linkKindName(kind)+": "+label+".", map[string]any{"kind": kind, "ref": ref})
	return err
}

func linkKindName(kind string) string {
	switch kind {
	case "alert":
		return "alert rule"
	case "uptime":
		return "uptime check"
	case "error":
		return "error issue"
	case "log":
		return "log line"
	case "url":
		return "page"
	}
	return kind
}

func (s *Server) handleAddIncidentLink(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "edit") {
		return
	}
	in, ok := s.incidentFromPath(w, r)
	if !ok {
		return
	}
	var body struct {
		Kind  string `json:"kind"`
		Ref   string `json:"ref"`
		Label string `json:"label"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	body.Ref, body.Label = strings.TrimSpace(body.Ref), strings.TrimSpace(body.Label)
	if !slices.Contains(incidentLinkKinds, body.Kind) {
		writeError(w, http.StatusBadRequest, "kind must be one of "+strings.Join(incidentLinkKinds, ", "))
		return
	}
	if body.Ref == "" || len(body.Ref) > 2000 || len(body.Label) > 255 {
		writeError(w, http.StatusBadRequest, "give what to link, under 2000 characters, and a label under 255")
		return
	}
	ctx := r.Context()
	switch body.Kind {
	case "alert", "uptime", "error", "log":
		src, err := s.resolveIncidentSource(ctx, body.Kind, body.Ref)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		body.Ref, body.Label = src.Ref, fallback(body.Label, src.Label)
	case "chart", "url":
		if !validLink(body.Ref) {
			writeError(w, http.StatusBadRequest, "the link must be an http(s) address or a dashboard link starting with #/")
			return
		}
	}
	by := fallback(actorName(r), "someone")
	if err := s.linkIncident(ctx, in.ID, body.Kind, body.Ref, fallback(body.Label, body.Ref), by); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "incident.link", in.Title, map[string]any{"id": in.ID, "kind": body.Kind, "ref": body.Ref})
	links, err := s.incidentLinks(ctx, in.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"links": links})
}

func (s *Server) handleRemoveIncidentLink(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "edit") {
		return
	}
	in, ok := s.incidentFromPath(w, r)
	if !ok {
		return
	}
	lid, ok := incidentID(r, "lid")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid link id")
		return
	}
	if err := s.db.Exec(r.Context(), `DELETE FROM incident_links WHERE id = ? AND incident_id = ?`, lid, in.ID); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "incident.link.remove", in.Title, map[string]any{"id": in.ID, "link": lid})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleIncidentUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "edit") {
		return
	}
	in, ok := s.incidentFromPath(w, r)
	if !ok {
		return
	}
	var body struct {
		Body       string  `json:"body"`
		Status     string  `json:"status"`
		Channels   []int64 `json:"channels"`
		StatusPage bool    `json:"status_page"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	body.Body = strings.TrimSpace(body.Body)
	if body.Body == "" || len(body.Body) > 2000 {
		writeError(w, http.StatusBadRequest, "the update must be 1–2000 characters")
		return
	}
	if body.Status != "" && !slices.Contains(incidentStatuses, body.Status) {
		writeError(w, http.StatusBadRequest, "status must be investigating, identified, monitoring or resolved")
		return
	}
	if len(body.Channels) == 0 && !body.StatusPage {
		writeError(w, http.StatusBadRequest, "pick at least one channel or the status page")
		return
	}
	ctx := r.Context()
	by := fallback(actorName(r), "someone")
	if body.Status != "" && body.Status != in.Status {
		if err := s.setIncidentStatus(ctx, in, body.Status, by, ""); err != nil {
			s.fail(w, err)
			return
		}
	}
	if body.StatusPage && !in.OnStatusPage {
		if err := s.db.Exec(ctx, `UPDATE incidents SET on_status_page = 1 WHERE id = ?`, in.ID); err != nil {
			s.fail(w, err)
			return
		}
	}
	cur, err := s.getIncident(ctx, in.ID)
	if err != nil || cur == nil {
		s.fail(w, err)
		return
	}
	sent, err := s.postIncidentUpdate(ctx, cur, by, body.Body, body.Channels, body.StatusPage)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "incident.post_update", in.Title, map[string]any{"id": in.ID, "channels": sent, "status_page": body.StatusPage, "status": cur.Status})
	writeJSON(w, http.StatusOK, map[string]any{"incident": cur, "channels": sent})
}

func (s *Server) handleIncidentPeople(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "edit") {
		return
	}
	ctx := r.Context()
	seen := map[string]bool{}
	people := []string{}
	add := func(n string) {
		if n = strings.TrimSpace(n); n != "" && !seen[strings.ToLower(n)] {
			seen[strings.ToLower(n)] = true
			people = append(people, n)
		}
	}
	add(actorName(r))
	if accts, err := s.listAccounts(ctx); err == nil {
		for _, a := range accts {
			if !a.Disabled {
				add(fallback(a.Name, a.Username))
			}
		}
	}
	since := time.Now().UTC().AddDate(0, 0, -90).Format(tsLayout)
	if res, err := s.db.Query(ctx, `SELECT DISTINCT user_name FROM audit_log WHERE created_at >= ? AND user_name <> '' LIMIT 500`, since); err == nil {
		for _, row := range res.Rows {
			add(toString(row[0]))
		}
	}
	if res, err := s.db.Query(ctx, `SELECT DISTINCT user_name FROM incident_roles LIMIT 500`); err == nil {
		for _, row := range res.Rows {
			add(toString(row[0]))
		}
	}
	sort.Slice(people, func(i, j int) bool { return strings.ToLower(people[i]) < strings.ToLower(people[j]) })
	writeJSON(w, http.StatusOK, map[string]any{"people": people, "me": actorName(r)})
}

func (s *Server) handleIncidentSource(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	src, err := s.resolveIncidentSource(r.Context(), q.Get("kind"), q.Get("ref"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, src)
}
