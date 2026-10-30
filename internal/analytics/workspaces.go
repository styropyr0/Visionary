package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	sdk "github.com/styropyr0/visionary/sdks/go"
)

// Workspaces scope people to sets of services. A workspace is a name, the
// services it covers (exact names or globs) and its members, each with a
// role there. With no workspaces everyone sees everything, as before. Global
// admins always see everything; anyone else who belongs to a workspace sees
// only its services, and can switch between their workspaces. Alerts,
// uptime checks, dashboards and saved views can belong to one. The
// filtering itself is in workspaces_scope.go.

const (
	workspaceCookie   = "visionary_workspace"
	settingUnassigned = "workspaces.unassigned"
	workspaceCacheFor = 15 * time.Second
	maxWorkspaceName  = 80
	maxWorkspaceRules = 100
	maxMembers        = 500
)

// servicePattern is a service name, optionally with * at either end. Names
// can't hold quotes, commas or backslashes, so they go into SQL as they are.
var servicePattern = regexp.MustCompile(`^\*$|^\*?[A-Za-z0-9._:/@+ -]{1,128}\*?$`)

type workspace struct {
	ID        int64      `json:"id"`
	Name      string     `json:"name"`
	Services  []string   `json:"services"`
	Members   []wsMember `json:"members"`
	CreatedBy string     `json:"created_by"`
	CreatedAt string     `json:"created_at"`
	UpdatedAt string     `json:"updated_at"`
}

// wsMember is a person ("user", by account id or delegated user id), an
// identity provider or SCIM group ("group", by name) or an API key ("key",
// by id).
type wsMember struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Role string `json:"role"`
}

var workspaceAddedColumns = map[string][]sdk.Column{
	"alert_rules":     {{Name: "workspace_id", Kind: "int"}},
	"uptime_checks":   {{Name: "workspace_id", Kind: "int"}},
	"dashboards":      {{Name: "workspace_id", Kind: "int"}},
	"log_views":       {{Name: "workspace_id", Kind: "int"}},
	"visionary_users": {{Name: "sso_groups", Kind: "text"}},
}

func (s *Server) workspacesSchema() []string {
	id, key, ts, tail := "INTEGER PRIMARY KEY AUTOINCREMENT", "TEXT", "TEXT", ""
	switch s.q.d {
	case sdk.Postgres:
		id = "BIGSERIAL PRIMARY KEY"
	case sdk.MySQL:
		id, key, ts, tail = "BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY", "VARCHAR(191)", "VARCHAR(32)", " CHARACTER SET utf8mb4"
	}
	return []string{`CREATE TABLE IF NOT EXISTS workspaces (
    id         ` + id + `,
    name       ` + key + ` NOT NULL,
    services   TEXT NOT NULL,
    members    TEXT NOT NULL,
    created_by TEXT NOT NULL,
    created_at ` + ts + ` NOT NULL,
    updated_at ` + ts + ` NOT NULL
)` + tail}
}

func (s *Server) workspacesRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/workspaces", s.authed(s.handleListWorkspaces))
	mux.Handle("POST /api/workspaces", s.authed(s.handleCreateWorkspace))
	mux.Handle("PUT /api/workspaces/{id}", s.authed(s.handleUpdateWorkspace))
	mux.Handle("DELETE /api/workspaces/{id}", s.authed(s.handleDeleteWorkspace))
	mux.Handle("GET /api/workspaces/services", s.authed(s.handleWorkspaceServices))
	mux.Handle("PUT /api/workspaces/unassigned", s.authed(s.handleWorkspaceUnassigned))
	mux.Handle("GET /api/workspaces/items", s.authed(s.handleWorkspaceItems))
	mux.Handle("PUT /api/workspaces/items", s.authed(s.handleAssignWorkspaceItem))
}

// ---------- Cache ----------

type wsSnapshot struct {
	list []workspace
	// groups are each account's identity provider and SCIM groups.
	groups map[string][]string
	// rules maps alert rules to their workspace, so they are evaluated
	// over its services.
	rules map[int64]int64
	// unassigned is "none" when people outside every workspace see nothing.
	unassigned string
	at         time.Time
}

type wsState struct {
	mu    sync.Mutex
	snap  *wsSnapshot
	loads singleflight.Group
}

var wsStates sync.Map // *Server -> *wsState

func (s *Server) workspaceState() *wsState {
	v, _ := wsStates.LoadOrStore(s, &wsState{})
	return v.(*wsState)
}

// dropWorkspaces makes the next request read workspaces again.
func (s *Server) dropWorkspaces() {
	st := s.workspaceState()
	st.mu.Lock()
	st.snap = nil
	st.mu.Unlock()
}

// workspaceSnap returns the workspaces, read at most every workspaceCacheFor.
// If reading fails the last copy stays in use; with none, the error is
// returned so requests fail rather than show everything.
func (s *Server) workspaceSnap(ctx context.Context) (*wsSnapshot, error) {
	st := s.workspaceState()
	st.mu.Lock()
	snap := st.snap
	st.mu.Unlock()
	if snap != nil && time.Since(snap.at) < workspaceCacheFor {
		return snap, nil
	}
	v, err, _ := st.loads.Do("ws", func() (any, error) {
		lctx, cancel := context.WithTimeout(unscoped(context.WithoutCancel(ctx)), 5*time.Second)
		defer cancel()
		return s.loadWorkspaces(lctx)
	})
	if err != nil {
		if snap != nil {
			return snap, nil
		}
		return nil, err
	}
	next := v.(*wsSnapshot)
	st.mu.Lock()
	st.snap = next
	st.mu.Unlock()
	return next, nil
}

func (s *Server) loadWorkspaces(ctx context.Context) (*wsSnapshot, error) {
	snap := &wsSnapshot{at: time.Now()}
	list, err := s.listWorkspaces(ctx)
	if err != nil {
		if missingTable(err, "workspaces") {
			return snap, nil
		}
		return nil, err
	}
	snap.list = list
	if len(list) == 0 {
		return snap, nil
	}
	if res, err := s.db.Query(ctx, `SELECT value FROM visionary_settings WHERE name = ?`, settingUnassigned); err == nil && len(res.Rows) > 0 {
		snap.unassigned = toString(res.Rows[0][0])
	}
	snap.rules = map[int64]int64{}
	if res, err := s.db.Query(ctx, `SELECT id, workspace_id FROM alert_rules WHERE workspace_id IS NOT NULL`); err == nil {
		for _, row := range res.Rows {
			snap.rules[toInt(row[0])] = toInt(row[1])
		}
	}
	snap.groups = map[string][]string{}
	res, err := s.db.Query(ctx, `SELECT id, sso_groups FROM visionary_users WHERE sso_groups IS NOT NULL AND sso_groups <> ''`)
	if err != nil {
		return nil, err
	}
	for _, row := range res.Rows {
		var g []string
		if json.Unmarshal([]byte(toString(row[1])), &g) == nil {
			snap.groups[toString(row[0])] = g
		}
	}
	if groups, err := s.scimGroups(ctx); err == nil {
		for _, g := range groups {
			for _, m := range g.Members {
				snap.groups[m] = append(snap.groups[m], g.DisplayName)
			}
		}
	} else if !missingTable(err, "visionary_scim_groups") {
		return nil, err
	}
	return snap, nil
}

func (s *Server) listWorkspaces(ctx context.Context) ([]workspace, error) {
	res, err := s.db.Query(ctx, `SELECT id, name, services, members, created_by, created_at, updated_at FROM workspaces ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	out := make([]workspace, 0, len(res.Rows))
	for _, row := range res.Rows {
		if len(row) != 7 {
			continue
		}
		ws := workspace{ID: toInt(row[0]), Name: toString(row[1]), CreatedBy: toString(row[4]), CreatedAt: toString(row[5]), UpdatedAt: toString(row[6])}
		_ = json.Unmarshal([]byte(toString(row[2])), &ws.Services)
		_ = json.Unmarshal([]byte(toString(row[3])), &ws.Members)
		if ws.Services == nil {
			ws.Services = []string{}
		}
		if ws.Members == nil {
			ws.Members = []wsMember{}
		}
		out = append(out, ws)
	}
	return out, nil
}

// ---------- Who sees what ----------

type wsAccess struct {
	ws   *workspace
	role role
}

// memberships are the workspaces sess belongs to, with the best role any of
// its memberships gives there.
func memberships(snap *wsSnapshot, sess *session) []wsAccess {
	if sess == nil {
		return nil
	}
	id := sess.UserID
	account := ""
	if v, ok := strings.CutPrefix(id, accountsPrefix); ok {
		account = v
	} else if v, ok := strings.CutPrefix(id, ssoPrefix); ok {
		account = v
	}
	key, isKey := strings.CutPrefix(id, "key:")
	groups := snap.groups[account]
	if account == "" && !isKey && sess.Role == "" {
		// Delegated sign-in: the provider's roles act as groups.
		groups = sess.Roles
	}
	var out []wsAccess
	for i := range snap.list {
		ws := &snap.list[i]
		best := roleNone
		for _, m := range ws.Members {
			hit := false
			switch m.Kind {
			case "user":
				hit = !isKey && (m.ID == id || account != "" && m.ID == account)
			case "key":
				hit = isKey && m.ID == key
			case "group":
				hit = slices.ContainsFunc(groups, func(g string) bool { return strings.EqualFold(g, m.ID) })
			}
			if hit {
				best = max(best, parseRole(m.Role))
			}
		}
		if best != roleNone {
			out = append(out, wsAccess{ws, best})
		}
	}
	return out
}

// available are the workspaces sess may switch to: all of them for admins.
func (s *Server) available(snap *wsSnapshot, sess *session) []wsAccess {
	if s.roleOf(sess) >= roleAdmin {
		out := make([]wsAccess, len(snap.list))
		for i := range snap.list {
			out[i] = wsAccess{&snap.list[i], roleAdmin}
		}
		return out
	}
	return memberships(snap, sess)
}

func selectedWorkspace(r *http.Request) int64 {
	if c, err := r.Cookie(workspaceCookie); err == nil {
		if id, err := strconv.ParseInt(c.Value, 10, 64); err == nil {
			return id
		}
	}
	return 0
}

// scopeFor is the scope of a request, or nil when it sees every service.
func (s *Server) scopeFor(r *http.Request, sess *session) (*wsScope, error) {
	snap, err := s.workspaceSnap(r.Context())
	if err != nil {
		return nil, err
	}
	if len(snap.list) == 0 {
		return nil, nil
	}
	admin := s.roleOf(sess) >= roleAdmin
	mine := s.available(snap, sess)
	active, narrowed := mine, false
	if want := selectedWorkspace(r); want > 0 {
		for _, a := range mine {
			if a.ws.ID == want {
				active, narrowed = []wsAccess{a}, true
			}
		}
	}
	if !narrowed && (admin || len(mine) == 0 && snap.unassigned != "none") {
		return nil, nil
	}
	sc := &wsScope{d: s.q.d, patterns: []string{}}
	seen := map[string]bool{}
	for _, a := range active {
		sc.ids = append(sc.ids, a.ws.ID)
		for _, p := range a.ws.Services {
			if !seen[p] {
				seen[p] = true
				sc.patterns = append(sc.patterns, p)
			}
		}
		if !admin {
			sc.role = max(sc.role, a.role)
		}
	}
	for _, a := range active {
		if len(active) == 1 || a.role >= roleEditor {
			sc.home = a.ws.ID
			break
		}
	}
	return sc, nil
}

// ruleScope evaluates an alert rule that belongs to a workspace over that
// workspace's services only.
func (s *Server) ruleScope(ctx context.Context, ruleID int64) context.Context {
	snap, err := s.workspaceSnap(ctx)
	if err != nil {
		return ctx
	}
	wid, ok := snap.rules[ruleID]
	if !ok {
		return ctx
	}
	for _, ws := range snap.list {
		if ws.ID == wid {
			return withScope(ctx, &wsScope{d: s.q.d, patterns: ws.Services, ids: []int64{wid}, home: wid})
		}
	}
	return ctx
}

// serveAs runs h as sess, within the request's workspace scope.
func (s *Server) serveAs(w http.ResponseWriter, r *http.Request, sess *session, h http.HandlerFunc) {
	ctx := context.WithValue(r.Context(), sessionKey{}, sess)
	sc, err := s.scopeFor(r, sess)
	if err != nil {
		s.logger.Error("workspaces: reading them failed", "err", err)
		writeError(w, http.StatusServiceUnavailable, "Workspaces couldn't be read; try again shortly.")
		return
	}
	if sc != nil {
		ctx = withScope(ctx, sc)
	}
	h(w, r.WithContext(ctx))
}

// requestRole is the role in effect: inside workspaces, the role they give,
// though never admin of the whole server.
func (s *Server) requestRole(r *http.Request) role {
	rl := s.roleOf(sessionFrom(r))
	if sc := scopeFrom(r.Context()); sc != nil && sc.role != roleNone {
		rl = min(sc.role, roleEditor)
	}
	return rl
}

// workspaceView is the session's part about workspaces: where it can switch
// and which one is active (0: all of them).
func (s *Server) workspaceView(r *http.Request) map[string]any {
	out := map[string]any{"workspaces": []any{}, "workspace": 0, "scoped": scopeFrom(r.Context()) != nil}
	snap, err := s.workspaceSnap(r.Context())
	if err != nil || len(snap.list) == 0 {
		return out
	}
	list := []map[string]any{}
	want := selectedWorkspace(r)
	for _, a := range s.available(snap, sessionFrom(r)) {
		list = append(list, map[string]any{"id": a.ws.ID, "name": a.ws.Name, "role": a.role.String()})
		if a.ws.ID == want {
			out["workspace"] = want
		}
	}
	out["workspaces"] = list
	return out
}

// ---------- API ----------

func (s *Server) handleListWorkspaces(w http.ResponseWriter, r *http.Request) {
	snap, err := s.workspaceSnap(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	sess := sessionFrom(r)
	admin := s.roleOf(sess) >= roleAdmin
	list := []workspace{}
	roles := map[int64]string{}
	for _, a := range s.available(snap, sess) {
		ws := *a.ws
		if !admin && a.role < roleAdmin {
			ws.Members = nil
		}
		list = append(list, ws)
		roles[ws.ID] = a.role.String()
	}
	unassigned := snap.unassigned
	if unassigned != "none" {
		unassigned = "all"
	}
	writeJSON(w, http.StatusOK, map[string]any{"workspaces": list, "roles": roles, "unassigned": unassigned, "admin": admin})
}

type workspaceBody struct {
	Name     string     `json:"name"`
	Services []string   `json:"services"`
	Members  []wsMember `json:"members"`
}

// clean checks a workspace as sent and puts it in a stable form.
func (b *workspaceBody) clean() error {
	b.Name = strings.TrimSpace(b.Name)
	if b.Name == "" || len(b.Name) > maxWorkspaceName {
		return errors.New("give the workspace a name of at most 80 characters")
	}
	services := []string{}
	for _, p := range b.Services {
		p = strings.TrimSpace(p)
		if p == "" || slices.Contains(services, p) {
			continue
		}
		if !servicePattern.MatchString(p) || strings.Contains(strings.Trim(p, "*"), "*") {
			return errors.New(p + " isn't a service name or pattern: use letters, digits and . _ - : / @ +, with * only at the start or end")
		}
		services = append(services, p)
	}
	if len(services) > maxWorkspaceRules {
		return errors.New("a workspace can list at most 100 services or patterns")
	}
	b.Services = services
	members := []wsMember{}
	seen := map[string]bool{}
	for _, m := range b.Members {
		m.ID = strings.TrimSpace(m.ID)
		if m.Kind != "user" && m.Kind != "group" && m.Kind != "key" {
			return errors.New("a member is a user, a group or an API key")
		}
		if m.ID == "" || len(m.ID) > 255 {
			return errors.New("every member needs a name or id")
		}
		if parseRole(m.Role) == roleNone {
			return errors.New("a member's role is viewer, editor or admin")
		}
		m.Role = parseRole(m.Role).String()
		k := m.Kind + "\x00" + strings.ToLower(m.ID)
		if seen[k] {
			continue
		}
		seen[k] = true
		members = append(members, m)
	}
	if len(members) > maxMembers {
		return errors.New("a workspace can have at most 500 members")
	}
	b.Members = members
	return nil
}

func (s *Server) handleCreateWorkspace(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	var body workspaceBody
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := body.clean(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	svc, _ := json.Marshal(body.Services)
	mem, _ := json.Marshal(body.Members)
	now := nowTS()
	sess := sessionFrom(r)
	id, err := s.db.InsertID(unscoped(r.Context()), `INSERT INTO workspaces (name, services, members, created_by, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		body.Name, string(svc), string(mem), sess.Name, now, now)
	if err != nil {
		s.fail(w, dbErr(err))
		return
	}
	s.dropWorkspaces()
	s.audit(r, "workspace.create", strconv.FormatInt(id, 10), map[string]any{"name": body.Name, "services": body.Services, "members": len(body.Members)})
	writeJSON(w, http.StatusCreated, workspace{ID: id, Name: body.Name, Services: body.Services, Members: body.Members, CreatedBy: sess.Name, CreatedAt: now, UpdatedAt: now})
}

// findWorkspace reads one workspace afresh, for changes.
func (s *Server) findWorkspace(ctx context.Context, id int64) (*workspace, error) {
	list, err := s.listWorkspaces(unscoped(ctx))
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].ID == id {
			return &list[i], nil
		}
	}
	return nil, nil
}

// handleUpdateWorkspace lets admins change anything and a workspace's own
// admins change its members.
func (s *Server) handleUpdateWorkspace(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var body workspaceBody
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ws, err := s.findWorkspace(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if ws == nil {
		writeError(w, http.StatusNotFound, "workspace not found")
		return
	}
	sess := sessionFrom(r)
	if s.roleOf(sess) < roleAdmin {
		snap, err := s.workspaceSnap(r.Context())
		if err != nil {
			s.fail(w, err)
			return
		}
		own := slices.ContainsFunc(memberships(snap, sess), func(a wsAccess) bool { return a.ws.ID == id && a.role >= roleAdmin })
		if !own {
			writeError(w, http.StatusForbidden, "Only admins, and this workspace's admins, can change it.")
			return
		}
		body.Name, body.Services = ws.Name, ws.Services
	}
	if err := body.clean(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	svc, _ := json.Marshal(body.Services)
	mem, _ := json.Marshal(body.Members)
	if err := s.db.Exec(unscoped(r.Context()), `UPDATE workspaces SET name = ?, services = ?, members = ?, updated_at = ? WHERE id = ?`,
		body.Name, string(svc), string(mem), nowTS(), id); err != nil {
		s.fail(w, dbErr(err))
		return
	}
	s.dropWorkspaces()
	s.audit(r, "workspace.update", strconv.FormatInt(id, 10), map[string]any{
		"name": body.Name, "services": body.Services, "members": memberChanges(ws.Members, body.Members),
	})
	w.WriteHeader(http.StatusNoContent)
}

// memberChanges lists who was added, removed or given another role.
func memberChanges(before, after []wsMember) []string {
	key := func(m wsMember) string { return m.Kind + " " + m.ID }
	old := map[string]string{}
	for _, m := range before {
		old[key(m)] = m.Role
	}
	var out []string
	for _, m := range after {
		was, ok := old[key(m)]
		switch {
		case !ok:
			out = append(out, "added "+key(m)+" as "+m.Role)
		case was != m.Role:
			out = append(out, key(m)+" "+was+" → "+m.Role)
		}
		delete(old, key(m))
	}
	for k := range old {
		out = append(out, "removed "+k)
	}
	sort.Strings(out)
	return out
}

func (s *Server) handleDeleteWorkspace(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	ctx := unscoped(r.Context())
	ws, err := s.findWorkspace(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if ws == nil {
		writeError(w, http.StatusNotFound, "workspace not found")
		return
	}
	// What belonged to it becomes unassigned rather than lost.
	for table := range ownedTables {
		if err := s.db.Exec(ctx, `UPDATE `+strings.ToLower(table)+` SET workspace_id = NULL WHERE workspace_id = ?`, id); err != nil {
			s.fail(w, dbErr(err))
			return
		}
	}
	if err := s.db.Exec(ctx, `DELETE FROM workspaces WHERE id = ?`, id); err != nil {
		s.fail(w, dbErr(err))
		return
	}
	s.dropWorkspaces()
	s.audit(r, "workspace.delete", strconv.FormatInt(id, 10), map[string]any{"name": ws.Name})
	w.WriteHeader(http.StatusNoContent)
}

// serviceSources are where service names are found for the picker.
var serviceSources = []string{"api_logs", "app_logs", "spans", "errors", "system_metrics", "metrics", "releases"}

// handleWorkspaceServices lists the services seen in the last 30 days, so
// admins can see what a pattern matches as they type it.
func (s *Server) handleWorkspaceServices(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	since := time.Now().UTC().Add(-30 * 24 * time.Hour).Format(tsLayout)
	set := map[string]bool{}
	for _, t := range serviceSources {
		res, err := s.db.Query(r.Context(), `SELECT DISTINCT service FROM `+t+` WHERE created_at >= ? AND service IS NOT NULL LIMIT 1000`, since)
		if err != nil {
			continue
		}
		for _, row := range res.Rows {
			if v := toString(row[0]); v != "" {
				set[v] = true
			}
		}
	}
	if res, err := s.db.Query(r.Context(), `SELECT DISTINCT app FROM client_events WHERE created_at >= ? LIMIT 1000`, since); err == nil {
		for _, row := range res.Rows {
			if v := toString(row[0]); v != "" {
				set[v] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	writeJSON(w, http.StatusOK, map[string]any{"services": out})
}

func (s *Server) handleWorkspaceUnassigned(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	var body struct {
		Mode string `json:"mode"`
	}
	if err := readJSON(r, &body); err != nil || (body.Mode != "all" && body.Mode != "none") {
		writeError(w, http.StatusBadRequest, "mode is all or none")
		return
	}
	if err := s.writeSettings(unscoped(r.Context()), map[string]string{settingUnassigned: body.Mode}); err != nil {
		s.fail(w, err)
		return
	}
	s.dropWorkspaces()
	s.audit(r, "workspace.unassigned", body.Mode, nil)
	w.WriteHeader(http.StatusNoContent)
}

// workspaceItems are what can belong to a workspace, by the name the API uses.
var workspaceItems = map[string]string{"alert": "alert_rules", "check": "uptime_checks", "dashboard": "dashboards", "view": "log_views"}

func (s *Server) handleWorkspaceItems(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	ctx := unscoped(r.Context())
	out := map[string]any{}
	for kind, table := range workspaceItems {
		res, err := s.db.Query(ctx, `SELECT id, name, workspace_id FROM `+table+` ORDER BY name, id`)
		if err != nil {
			s.fail(w, err)
			return
		}
		items := make([]map[string]any, 0, len(res.Rows))
		for _, row := range res.Rows {
			items = append(items, map[string]any{"id": toInt(row[0]), "name": toString(row[1]), "workspace_id": nullable(row[2])})
		}
		out[kind] = items
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAssignWorkspaceItem(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	var body struct {
		Kind        string `json:"kind"`
		ID          int64  `json:"id"`
		WorkspaceID *int64 `json:"workspace_id"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	table, ok := workspaceItems[body.Kind]
	if !ok {
		writeError(w, http.StatusBadRequest, "kind is alert, check, dashboard or view")
		return
	}
	ctx := unscoped(r.Context())
	var target any
	if body.WorkspaceID != nil {
		ws, err := s.findWorkspace(ctx, *body.WorkspaceID)
		if err != nil {
			s.fail(w, err)
			return
		}
		if ws == nil {
			writeError(w, http.StatusNotFound, "workspace not found")
			return
		}
		target = ws.ID
	}
	if err := s.db.Exec(ctx, `UPDATE `+table+` SET workspace_id = ? WHERE id = ?`, target, body.ID); err != nil {
		s.fail(w, dbErr(err))
		return
	}
	if body.Kind == "alert" {
		s.dropWorkspaces()
	}
	s.audit(r, "workspace.assign", body.Kind+" "+strconv.FormatInt(body.ID, 10), map[string]any{"workspace_id": target})
	w.WriteHeader(http.StatusNoContent)
}

// rememberGroups keeps the groups an identity provider sent at sign-in, which
// can make the person a member of workspaces.
func (s *Server) rememberGroups(ctx context.Context, accountID string, groups []string) {
	b, _ := json.Marshal(groups)
	if err := s.db.Exec(unscoped(ctx), `UPDATE visionary_users SET sso_groups = ? WHERE id = ?`, string(b), accountID); err != nil {
		s.logger.Warn("workspaces: saving sign-in groups failed", "err", err)
		return
	}
	s.dropWorkspaces()
}
