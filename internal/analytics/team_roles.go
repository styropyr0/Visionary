package analytics

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/styropyr0/visionary/internal/authbridge"
)

// role is what a user may do in Visionary. Each level includes the ones below.
type role int

const (
	roleNone   role = iota
	roleViewer      // dashboards, the analyst and read-only queries
	roleEditor      // also saved panels, dashboards, alerts and checks
	roleAdmin       // also settings: API keys, role mapping, audit log, channels
)

func (r role) String() string {
	switch r {
	case roleViewer:
		return "viewer"
	case roleEditor:
		return "editor"
	case roleAdmin:
		return "admin"
	}
	return ""
}

func parseRole(s string) role {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "viewer":
		return roleViewer
	case "editor":
		return roleEditor
	case "admin":
		return roleAdmin
	}
	return roleNone
}

// actionRoles maps the actions handlers ask can() about to the role needed.
var actionRoles = map[string]role{"view": roleViewer, "edit": roleEditor, "admin": roleAdmin}

// roleMapping turns the auth provider's roles into Visionary roles.
// VISIONARY_ROLE_MAP ("ADMIN=admin,ANALYST=viewer") names them explicitly;
// any VISIONARY_ALLOWED_ROLES entry the map does not mention is an admin, as
// before roles existed. The role "*" matches every user of the API, with or
// without a role ("ADMIN=admin,*=viewer").
type roleMapping struct {
	entries []roleEntry
}

type roleEntry struct {
	Provider string `json:"provider_role"`
	Role     string `json:"role"`
	// Source is "map" (VISIONARY_ROLE_MAP) or "allowed" (VISIONARY_ALLOWED_ROLES).
	Source string `json:"source"`
}

func newRoleMapping(raw string, allowed []string) (*roleMapping, error) {
	m := &roleMapping{}
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, value, ok := strings.Cut(part, "=")
		name = strings.TrimSpace(name)
		r := parseRole(value)
		if !ok || name == "" || r == roleNone {
			return nil, fmt.Errorf("role mapping entry %q must look like ROLE=viewer|editor|admin", part)
		}
		key := strings.ToUpper(name)
		if seen[key] {
			return nil, fmt.Errorf("the role mapping names %q twice", name)
		}
		seen[key] = true
		m.entries = append(m.entries, roleEntry{Provider: name, Role: r.String(), Source: "map"})
	}
	for _, a := range allowed {
		if a = strings.TrimSpace(a); a != "" && !seen[strings.ToUpper(a)] {
			seen[strings.ToUpper(a)] = true
			m.entries = append(m.entries, roleEntry{Provider: a, Role: roleAdmin.String(), Source: "allowed"})
		}
	}
	return m, nil
}

// resolve is the highest Visionary role any of the user's roles maps to.
func (m *roleMapping) resolve(roles []string) role {
	best := roleNone
	for _, r := range roles {
		for _, e := range m.entries {
			if strings.EqualFold(e.Provider, r) {
				best = max(best, parseRole(e.Role))
			}
		}
	}
	for _, e := range m.entries {
		if e.Provider == authbridge.AnyRole {
			best = max(best, parseRole(e.Role))
		}
	}
	return best
}

func (m *roleMapping) allowed(u *authbridge.User) bool { return m.resolve(u.Roles) != roleNone }

// roleOf is the session's effective role. Shared-password, local and API key
// sessions carry a fixed role; delegated sessions are mapped on every
// request, so a changed mapping applies at once.
func (s *Server) roleOf(sess *session) role {
	if sess == nil {
		return roleNone
	}
	if sess.Role != "" {
		return parseRole(sess.Role)
	}
	sg := s.signin()
	if sg.cfg.Type == authbridge.TypeShared {
		// Cookies issued before roles existed.
		return roleAdmin
	}
	return sg.roles.resolve(sess.Roles)
}

func sessionFrom(r *http.Request) *session {
	sess, _ := r.Context().Value(sessionKey{}).(*session)
	return sess
}

// can reports whether the signed-in user may perform action ("view", "edit"
// or "admin"). Every handler that changes state checks can(r, "edit") or
// can(r, "admin") and answers 403 when it is false.
func (s *Server) can(r *http.Request, action string) bool {
	need, ok := actionRoles[action]
	return ok && s.roleOf(sessionFrom(r)) >= need
}

// require answers 403 unless the user may perform action.
func (s *Server) require(w http.ResponseWriter, r *http.Request, action string) bool {
	if s.can(r, action) {
		return true
	}
	msg := "Your role can't make changes here; ask an editor or admin."
	if action == "admin" {
		msg = "Only admins can do this."
	}
	writeError(w, http.StatusForbidden, msg)
	return false
}
