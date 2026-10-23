package analytics

import (
	"context"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/styropyr0/visionary/internal/authbridge"
)

// Roles, API keys, the audit log and dashboards. Roles live in
// team_roles.go, keys in team_keys.go, the audit log in team_audit.go and
// dashboards in team_dashboards.go.

// teamState is the feature's in-memory state. It hangs off the server through
// a side table so the shared Server struct stays as it is.
type teamState struct {
	audits chan auditEntry
	keys   keyStore
}

var teamStates sync.Map // *Server -> *teamState

func (s *Server) team() *teamState {
	if v, ok := teamStates.Load(s); ok {
		return v.(*teamState)
	}
	v, _ := teamStates.LoadOrStore(s, &teamState{
		audits: make(chan auditEntry, auditQueue),
		keys:   keyStore{used: map[int64]time.Time{}},
	})
	return v.(*teamState)
}

func (s *Server) teamRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/team/members", s.authed(s.handleMembers))
	s.accountRoutes(mux)
	mux.Handle("GET /api/team/about", s.authed(s.handleAbout))
	mux.Handle("GET /api/team/audit", s.authed(s.handleAudit))
	mux.Handle("GET /api/team/keys", s.authed(s.handleListKeys))
	mux.Handle("POST /api/team/keys", s.authed(s.handleCreateKey))
	mux.Handle("DELETE /api/team/keys/{id}", s.authed(s.handleRevokeKey))

	mux.Handle("GET /api/dashboards", s.authed(s.handleListDashboards))
	mux.Handle("POST /api/dashboards", s.authed(s.handleCreateDashboard))
	mux.Handle("GET /api/dashboards/{id}", s.authed(s.handleGetDashboard))
	mux.Handle("PUT /api/dashboards/{id}", s.authed(s.handleUpdateDashboard))
	mux.Handle("DELETE /api/dashboards/{id}", s.authed(s.handleDeleteDashboard))
	mux.Handle("GET /api/dashboards/panels/{id}/data", s.authed(s.handlePanelData))
	mux.Handle("GET /api/dashboards/variables/{name}", s.authed(s.handleVariableValues))
}

func (s *Server) teamStart(ctx context.Context) {
	go s.auditWriter(ctx)
	go s.flushKeyUse(ctx)
}

// handleMembers shows the role mapping in effect and the current user; admins
// also see recent sign-ins from the audit log.
func (s *Server) handleMembers(w http.ResponseWriter, r *http.Request) {
	sg := s.signin()
	out := map[string]any{
		"mode":            sg.cfg.Type,
		"open":            sg.open,
		"provider":        sg.cfg.Provider,
		"mapping":         sg.roles.entries,
		"viewer_password": sg.cfg.Type == authbridge.TypeShared && sg.viewerPassword != "",
		"me":              s.sessionView(sessionFrom(r)),
	}
	if s.can(r, "admin") {
		rows, err := s.db.Query(r.Context(), `SELECT created_at, user_id, user_name, role, action, ip FROM audit_log
WHERE action >= 'auth.' AND action < 'auth/' ORDER BY id DESC LIMIT 50`)
		if err != nil {
			s.fail(w, err)
			return
		}
		signIns := make([]map[string]any, 0, len(rows.Rows))
		for _, row := range rows.Rows {
			if len(row) == 6 {
				signIns = append(signIns, map[string]any{
					"created_at": toString(row[0]), "user_id": toString(row[1]), "user_name": toString(row[2]),
					"role": toString(row[3]), "action": toString(row[4]), "ip": toString(row[5]),
				})
			}
		}
		out["sign_ins"] = signIns
	}
	writeJSON(w, http.StatusOK, out)
}

type featureStatus struct {
	Name   string `json:"name"`
	On     bool   `json:"on"`
	Detail string `json:"detail"`
}

// connectedService is one instance that sent system metrics this week, with
// the SDK and language runtime that wrote them (empty from older SDKs).
type connectedService struct {
	Service  string `json:"service"`
	Instance string `json:"instance"`
	SDK      string `json:"sdk"`
	Runtime  string `json:"runtime"`
	LastSeen string `json:"last_seen"`
}

func (s *Server) connectedServices(ctx context.Context) []connectedService {
	out := []connectedService{}
	res, err := s.db.Query(ctx, `SELECT MAX(service), instance, MAX(sdk), MAX(runtime), MAX(created_at) AS last_seen
FROM system_metrics WHERE created_at >= ? GROUP BY instance ORDER BY last_seen DESC LIMIT 50`,
		time.Now().UTC().Add(-7*24*time.Hour).Format(tsLayout))
	if err != nil {
		return out // no system_metrics table yet
	}
	for _, row := range res.Rows {
		if len(row) == 5 {
			out = append(out, connectedService{toString(row[0]), toString(row[1]), toString(row[2]), toString(row[3]), toString(row[4])})
		}
	}
	return out
}

func (s *Server) countList(env string) int {
	n := 0
	for _, k := range strings.Split(s.conf(env), ",") {
		if strings.TrimSpace(k) != "" {
			n++
		}
	}
	return n
}

// handleAbout reports the version, database and which features are set up.
func (s *Server) handleAbout(w http.ResponseWriter, r *http.Request) {
	version, commit := buildInfo()
	if version == "" {
		version = "dev"
		if len(commit) >= 7 {
			version = commit[:7]
		}
	}
	dbKeys := map[string]int{}
	if rows, err := s.db.Query(r.Context(), `SELECT scope, COUNT(*) FROM visionary_api_keys WHERE revoked_at IS NULL GROUP BY scope`); err == nil {
		for _, row := range rows.Rows {
			if len(row) == 2 {
				dbKeys[toString(row[0])] = int(toInt(row[1]))
			}
		}
	}
	n := strconv.Itoa
	model := "Add an OpenAI API key in Settings → Server to turn it on."
	if c := s.llm(); c != nil {
		model = c.modelName()
	}
	sg := s.signin()
	signIn := sg.cfg.Type + " (" + sg.cfg.Provider + ")"
	switch {
	case sg.open == openDisabled:
		signIn = "Off: VISIONARY_ENABLE_AUTH=false"
	case sg.open == openUnconfigured:
		signIn = "Not set up: anyone with the URL is an admin"
	case sg.broken != nil:
		signIn = "Broken: " + sg.broken.Error()
	}
	serverList, clientList := s.countList("VISIONARY_INGEST_KEYS"), s.countList("VISIONARY_CLIENT_KEYS")
	features := []featureStatus{
		{"AI analyst", s.llm() != nil, model},
		{"Sign-in", sg.open == "" && sg.broken == nil, signIn},
		{"Server ingest keys", serverList+dbKeys["server"] > 0, n(serverList) + " in Settings → Server, " + n(dbKeys["server"]) + " under API keys"},
		{"Client ingest keys", clientList+dbKeys["client"] > 0, n(clientList) + " in Settings → Server, " + n(dbKeys["client"]) + " under API keys"},
		{"Read API keys", dbKeys["read"] > 0, n(dbKeys["read"]) + " active"},
		{"Audit log", true, "Kept for " + n(s.retentionDays("audit")) + " days"},
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":    version,
		"go_version": runtime.Version(),
		"services":   s.connectedServices(r.Context()),
		"database":   s.db.Name(),
		"auth_mode":  s.signin().cfg.Type,
		"features":   features,
	})
}
