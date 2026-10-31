package analytics

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/styropyr0/visionary/sdks/go"
)

// Declared incidents: a severity, a commander and responders, a status that
// moves from investigating to resolved, and a timeline that fills in. Alerts
// and checks stay as they are; an incident links to them.

var (
	incidentSeverities = []string{"sev1", "sev2", "sev3", "sev4"}
	incidentStatuses   = []string{"investigating", "identified", "monitoring", "resolved"}
	incidentRoles      = []string{"commander", "communications", "operations", "scribe", "responder"}
	incidentLinkKinds  = []string{"alert", "uptime", "error", "log", "request", "trace", "chart", "url"}
)

func init() {
	for _, t := range []string{"INCIDENTS", "INCIDENT_TIMELINE"} {
		allowedTables[t] = true
	}
}

func (s *Server) incidentsSchema() []string {
	id, key, ts := "INTEGER PRIMARY KEY AUTOINCREMENT", "TEXT", "TEXT"
	tail, index := "", "CREATE INDEX IF NOT EXISTS"
	switch s.q.d {
	case sdk.Postgres:
		id = "BIGSERIAL PRIMARY KEY"
	case sdk.MySQL:
		id, key, ts = "BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY", "VARCHAR(191)", "VARCHAR(32)"
		tail, index = " CHARACTER SET utf8mb4", "CREATE INDEX"
	}
	ddl := `CREATE TABLE IF NOT EXISTS incidents (
    id              {id},
    title           TEXT NOT NULL,
    severity        {key} NOT NULL,
    status          {key} NOT NULL,
    services        TEXT NOT NULL,
    impact          TEXT NOT NULL,
    source_kind     {key} NOT NULL DEFAULT '',
    source_ref      TEXT NOT NULL,
    on_status_page  INTEGER NOT NULL DEFAULT 0,
    created_by      TEXT NOT NULL,
    started_at      {ts} NOT NULL,
    declared_at     {ts} NOT NULL,
    acknowledged_at {ts} NOT NULL DEFAULT '',
    identified_at   {ts} NOT NULL DEFAULT '',
    mitigated_at    {ts} NOT NULL DEFAULT '',
    resolved_at     {ts} NOT NULL DEFAULT '',
    updated_at      {ts} NOT NULL,
    postmortem      TEXT
){tail};
{index} idx_incidents_declared ON incidents (declared_at);
CREATE TABLE IF NOT EXISTS incident_roles (
    id          {id},
    incident_id BIGINT NOT NULL,
    role        {key} NOT NULL,
    user_name   TEXT NOT NULL,
    assigned_by TEXT NOT NULL,
    assigned_at {ts} NOT NULL
){tail};
{index} idx_incident_roles_incident ON incident_roles (incident_id);
CREATE TABLE IF NOT EXISTS incident_timeline (
    id          {id},
    incident_id BIGINT NOT NULL,
    at          {ts} NOT NULL,
    kind        {key} NOT NULL,
    actor       TEXT NOT NULL,
    body        TEXT NOT NULL,
    detail      TEXT NOT NULL
){tail};
{index} idx_incident_timeline_incident ON incident_timeline (incident_id, at);
CREATE TABLE IF NOT EXISTS incident_links (
    id          {id},
    incident_id BIGINT NOT NULL,
    kind        {key} NOT NULL,
    ref         TEXT NOT NULL,
    label       TEXT NOT NULL,
    created_by  TEXT NOT NULL,
    created_at  {ts} NOT NULL
){tail};
{index} idx_incident_links_incident ON incident_links (incident_id);
CREATE TABLE IF NOT EXISTS incident_actions (
    id          {id},
    incident_id BIGINT NOT NULL,
    body        TEXT NOT NULL,
    owner       TEXT NOT NULL,
    done        INTEGER NOT NULL DEFAULT 0,
    created_at  {ts} NOT NULL,
    done_at     {ts} NOT NULL DEFAULT ''
){tail};
{index} idx_incident_actions_incident ON incident_actions (incident_id)`
	return []string{strings.NewReplacer("{id}", id, "{key}", key, "{ts}", ts, "{tail}", tail, "{index}", index).Replace(ddl)}
}

func (s *Server) incidentsRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/incidents", s.authed(s.handleListIncidents))
	mux.Handle("POST /api/incidents", s.authed(s.handleDeclareIncident))
	mux.Handle("GET /api/incidents/stats", s.authed(s.handleIncidentStats))
	mux.Handle("GET /api/incidents/people", s.authed(s.handleIncidentPeople))
	mux.Handle("GET /api/incidents/source", s.authed(s.handleIncidentSource))
	mux.Handle("GET /api/incidents/{id}", s.authed(s.handleGetIncident))
	mux.Handle("PUT /api/incidents/{id}", s.authed(s.handleEditIncident))
	mux.Handle("DELETE /api/incidents/{id}", s.authed(s.handleDeleteIncident))
	mux.Handle("POST /api/incidents/{id}/status", s.authed(s.handleIncidentStatus))
	mux.Handle("POST /api/incidents/{id}/roles", s.authed(s.handleAssignRole))
	mux.Handle("DELETE /api/incidents/{id}/roles/{rid}", s.authed(s.handleRemoveRole))
	mux.Handle("POST /api/incidents/{id}/notes", s.authed(s.handleIncidentNote))
	mux.Handle("POST /api/incidents/{id}/links", s.authed(s.handleAddIncidentLink))
	mux.Handle("DELETE /api/incidents/{id}/links/{lid}", s.authed(s.handleRemoveIncidentLink))
	mux.Handle("POST /api/incidents/{id}/updates", s.authed(s.handleIncidentUpdate))
	mux.Handle("POST /api/incidents/{id}/postmortem/draft", s.authed(s.handleDraftPostmortem))
	mux.Handle("PUT /api/incidents/{id}/postmortem", s.authed(s.handleSavePostmortem))
	mux.Handle("GET /api/incidents/{id}/postmortem.md", s.authed(s.handlePostmortemMarkdown))
	mux.Handle("POST /api/incidents/{id}/actions", s.authed(s.handleAddAction))
	mux.Handle("PUT /api/incidents/{id}/actions/{aid}", s.authed(s.handleEditAction))
	mux.Handle("DELETE /api/incidents/{id}/actions/{aid}", s.authed(s.handleRemoveAction))
}

func (s *Server) incidentsPrompt() string {
	return `Table incidents: incidents people declared in Visionary, one row each.
- id, title TEXT, severity TEXT ('sev1' worst .. 'sev4'), status TEXT ('investigating', 'identified', 'monitoring', 'resolved'), services TEXT (JSON list of affected services), impact TEXT
- started_at (when the impact began), declared_at, acknowledged_at (a commander took it), identified_at, mitigated_at (monitoring a fix), resolved_at: TEXT timestamps, '' until reached
- Time to acknowledge, mitigate and resolve are those columns minus started_at. Open incidents have resolved_at = ''.

Table incident_timeline: what happened during each incident, in order.
- id, incident_id (= incidents.id), at TEXT, kind TEXT ('declared', 'status', 'severity', 'role', 'note', 'update', 'link', 'edit', 'postmortem'), actor TEXT, body TEXT, detail TEXT (JSON)
- When asked why something broke during an incident, read its timeline and compare api_logs, errors and releases over started_at .. resolved_at for its services.`
}

// declaredIncident is one row of incidents with its roles; the TT* fields
// are seconds from started_at, nil until reached.
type declaredIncident struct {
	ID           int64          `json:"id"`
	Title        string         `json:"title"`
	Severity     string         `json:"severity"`
	Status       string         `json:"status"`
	Services     []string       `json:"services"`
	Impact       string         `json:"impact"`
	SourceKind   string         `json:"source_kind,omitempty"`
	SourceRef    string         `json:"source_ref,omitempty"`
	OnStatusPage bool           `json:"on_status_page"`
	CreatedBy    string         `json:"created_by"`
	StartedAt    string         `json:"started_at"`
	DeclaredAt   string         `json:"declared_at"`
	AckedAt      string         `json:"acknowledged_at,omitempty"`
	IdentifiedAt string         `json:"identified_at,omitempty"`
	MitigatedAt  string         `json:"mitigated_at,omitempty"`
	ResolvedAt   string         `json:"resolved_at,omitempty"`
	UpdatedAt    string         `json:"updated_at"`
	Commander    string         `json:"commander"`
	Roles        []incidentRole `json:"roles"`
	TTA          *int64         `json:"tta_s"`
	TTM          *int64         `json:"ttm_s"`
	TTR          *int64         `json:"ttr_s"`
	postmortem   string
}

type incidentRole struct {
	ID         int64  `json:"id"`
	Role       string `json:"role"`
	User       string `json:"user"`
	AssignedBy string `json:"assigned_by"`
	AssignedAt string `json:"assigned_at"`
}

const incidentColumns = `id, title, severity, status, services, impact, source_kind, source_ref, on_status_page, created_by,
started_at, declared_at, acknowledged_at, identified_at, mitigated_at, resolved_at, updated_at, postmortem`

func scanIncident(row []any) declaredIncident {
	in := declaredIncident{
		ID: toInt(row[0]), Title: toString(row[1]), Severity: toString(row[2]), Status: toString(row[3]),
		Impact: toString(row[5]), SourceKind: toString(row[6]), SourceRef: toString(row[7]), OnStatusPage: toInt(row[8]) == 1,
		CreatedBy: toString(row[9]), StartedAt: toString(row[10]), DeclaredAt: toString(row[11]), AckedAt: toString(row[12]),
		IdentifiedAt: toString(row[13]), MitigatedAt: toString(row[14]), ResolvedAt: toString(row[15]), UpdatedAt: toString(row[16]),
		postmortem: toString(row[17]), Roles: []incidentRole{},
	}
	if json.Unmarshal([]byte(toString(row[4])), &in.Services) != nil || in.Services == nil {
		in.Services = []string{}
	}
	in.TTA, in.TTM, in.TTR = sinceStart(in.StartedAt, in.AckedAt), sinceStart(in.StartedAt, in.MitigatedAt), sinceStart(in.StartedAt, in.ResolvedAt)
	return in
}

func sinceStart(start, at string) *int64 {
	if at == "" {
		return nil
	}
	a, err1 := time.Parse(tsLayout, start)
	b, err2 := time.Parse(tsLayout, at)
	if err1 != nil || err2 != nil {
		return nil
	}
	d := int64(max(b.Sub(a), 0).Seconds())
	return &d
}

func (s *Server) loadIncidents(ctx context.Context, where string, args []any, limit int) ([]declaredIncident, error) {
	res, err := s.db.Query(ctx, `SELECT `+incidentColumns+` FROM incidents WHERE `+where+` ORDER BY declared_at DESC, id DESC LIMIT `+strconv.Itoa(limit), args...)
	if err != nil {
		if missingTable(err, "incidents") {
			return []declaredIncident{}, nil
		}
		return nil, err
	}
	out := make([]declaredIncident, 0, len(res.Rows))
	byID := map[int64]int{}
	ids := make([]any, 0, len(res.Rows))
	for _, row := range res.Rows {
		if len(row) != 18 {
			continue
		}
		in := scanIncident(row)
		byID[in.ID] = len(out)
		ids = append(ids, in.ID)
		out = append(out, in)
	}
	for len(ids) > 0 {
		chunk := head(ids, maxInArgs)
		ids = ids[len(chunk):]
		roles, err := s.db.Query(ctx, `SELECT id, incident_id, role, user_name, assigned_by, assigned_at FROM incident_roles
WHERE incident_id IN (`+placeholders(len(chunk))+`) ORDER BY id`, chunk...)
		if err != nil {
			return nil, err
		}
		for _, row := range roles.Rows {
			i, ok := byID[toInt(row[1])]
			if !ok {
				continue
			}
			r := incidentRole{ID: toInt(row[0]), Role: toString(row[2]), User: toString(row[3]), AssignedBy: toString(row[4]), AssignedAt: toString(row[5])}
			out[i].Roles = append(out[i].Roles, r)
			if r.Role == "commander" {
				out[i].Commander = r.User
			}
		}
	}
	return out, nil
}

func (s *Server) getIncident(ctx context.Context, id int64) (*declaredIncident, error) {
	list, err := s.loadIncidents(ctx, "id = ?", []any{id}, 1)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

func (s *Server) activeIncidents(ctx context.Context) int {
	res, err := s.db.Query(ctx, `SELECT COUNT(*) FROM incidents WHERE resolved_at = ''`)
	if err != nil || len(res.Rows) == 0 {
		return 0
	}
	return int(toInt(res.Rows[0][0]))
}

func noticeSeverity(sev string) string {
	switch sev {
	case "sev1", "sev2":
		return "critical"
	case "sev3":
		return "warning"
	}
	return "info"
}

func severityLabel(sev string) string { return strings.ToUpper(sev) }

var incidentStatusLabels = map[string]string{"investigating": "Investigating", "identified": "Identified", "monitoring": "Monitoring", "resolved": "Resolved"}
