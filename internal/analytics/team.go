package analytics

import (
	"context"
	"net/http"
	"runtime"
	"runtime/debug"
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
	audits        chan auditEntry
	keys          keyStore
	retentionDays int
}

var teamStates sync.Map // *Server -> *teamState

func (s *Server) team() *teamState {
	if v, ok := teamStates.Load(s); ok {
		return v.(*teamState)
	}
	v, _ := teamStates.LoadOrStore(s, &teamState{
		audits:        make(chan auditEntry, auditQueue),
		keys:          keyStore{used: map[int64]time.Time{}},
		retentionDays: getInt("VISIONARY_AUDIT_RETENTION_DAYS", 365),
	})
	return v.(*teamState)
}

func (s *Server) teamRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/team/members", s.authed(s.handleMembers))
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
	go s.auditRetention(ctx)
	go s.flushKeyUse(ctx)
}

// handleMembers shows the role mapping in effect and the current user; admins
// also see recent sign-ins from the audit log.
func (s *Server) handleMembers(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{
		"mode":            s.cfg.Auth.Type,
		"provider":        s.cfg.Auth.Provider,
		"mapping":         s.auth.roles.entries,
		"viewer_password": s.cfg.Auth.Type == authbridge.TypeShared && s.auth.viewerPassword != "",
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

func countList(env string) int {
	n := 0
	for _, k := range strings.Split(getEnv(env, ""), ",") {
		if strings.TrimSpace(k) != "" {
			n++
		}
	}
	return n
}

// handleAbout reports the version, database and which features are set up.
func (s *Server) handleAbout(w http.ResponseWriter, r *http.Request) {
	version := "dev"
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		version = bi.Main.Version
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
	model := "Set OPENAI_API_KEY to turn it on."
	if s.llm != nil {
		model = s.llm.model
	}
	sessions := "VISIONARY_SESSION_SECRET is not set: everyone is signed out on restart."
	if s.cfg.SessionSecret != "" {
		sessions = "Sessions survive restarts."
	}
	serverEnv, clientEnv := countList("VISIONARY_INGEST_KEYS"), countList("VISIONARY_CLIENT_KEYS")
	features := []featureStatus{
		{"AI analyst", s.llm != nil, model},
		{"Sign-in", s.cfg.Auth.Type != authbridge.TypeNone, s.cfg.Auth.Type + " (" + s.cfg.Auth.Provider + ")"},
		{"Persistent sessions", s.cfg.SessionSecret != "", sessions},
		{"Server ingest keys", serverEnv+dbKeys["server"] > 0, n(serverEnv) + " from the environment, " + n(dbKeys["server"]) + " created here"},
		{"Client ingest keys", clientEnv+dbKeys["client"] > 0, n(clientEnv) + " from the environment, " + n(dbKeys["client"]) + " created here"},
		{"Read API keys", dbKeys["read"] > 0, n(dbKeys["read"]) + " active"},
		{"Audit log", true, "Kept for " + n(s.team().retentionDays) + " days"},
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":    version,
		"go_version": runtime.Version(),
		"database":   s.db.Name(),
		"auth_mode":  s.cfg.Auth.Type,
		"features":   features,
	})
}
