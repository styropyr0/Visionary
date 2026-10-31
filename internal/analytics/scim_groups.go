package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
)

// A person's role is the highest role any of their groups maps to, or the default role

type scimGroup struct {
	ID, DisplayName, ExternalID string
	Members                     []string
	CreatedAt, UpdatedAt        string
}

const maxGroupName = 255

func (s *Server) scimGroups(ctx context.Context) ([]scimGroup, error) {
	res, err := s.db.Query(ctx, `SELECT id, display_name, external_id, members, created_at, updated_at FROM visionary_scim_groups ORDER BY created_at, id`)
	if err != nil {
		return nil, dbErr(err)
	}
	out := make([]scimGroup, 0, len(res.Rows))
	for _, r := range res.Rows {
		g := scimGroup{ID: toString(r[0]), DisplayName: toString(r[1]), ExternalID: toString(r[2]), CreatedAt: toString(r[4]), UpdatedAt: toString(r[5])}
		_ = json.Unmarshal([]byte(toString(r[3])), &g.Members)
		out = append(out, g)
	}
	return out, nil
}

func findGroup(groups []scimGroup, id string) *scimGroup {
	for i := range groups {
		if groups[i].ID == id {
			return &groups[i]
		}
	}
	return nil
}

func (s *Server) scimManagesRoles() bool {
	c := s.ssoSetup().cfg.SCIM
	return c.RoleMap != "" || c.DefaultRole != ""
}

func (s *Server) scimRole(groupNames []string) role {
	c := s.ssoSetup().cfg.SCIM
	m, err := newRoleMapping(c.RoleMap, nil)
	if err != nil {
		return roleNone
	}
	return max(m.resolve(groupNames), parseRole(c.DefaultRole))
}

func (s *Server) scimApplyRoles(r *http.Request, groups []scimGroup, userIDs []string) {
	if !s.scimManagesRoles() {
		return
	}
	ctx := r.Context()
	for _, id := range userIDs {
		var names []string
		for _, g := range groups {
			if slices.Contains(g.Members, id) {
				names = append(names, g.DisplayName)
			}
		}
		a, err := s.scimUser(ctx, id)
		if err != nil || a == nil {
			continue
		}
		next := s.scimRole(names).String()
		if next == a.Role {
			continue
		}
		if err := s.db.Exec(ctx, `UPDATE visionary_users SET role = ?, source = ? WHERE id = ?`, next, scimSource, id); err != nil {
			s.logger.Warn("scim: setting a role failed", "user", a.Username, "err", err)
			continue
		}
		s.touchUser(id)
		s.audit(r, "scim.role_changed", a.Username, map[string]any{"from": a.Role, "to": next, "groups": names})
	}
}

func (s *Server) scimForget(ctx context.Context, userID string) error {
	groups, err := s.scimGroups(ctx)
	if err != nil {
		return err
	}
	for _, g := range groups {
		if i := slices.Index(g.Members, userID); i >= 0 {
			g.Members = slices.Delete(g.Members, i, i+1)
			if err := s.saveGroup(ctx, &g, false); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Server) saveGroup(ctx context.Context, g *scimGroup, insert bool) error {
	if g.Members == nil {
		g.Members = []string{}
	}
	members, _ := json.Marshal(g.Members)
	g.UpdatedAt = nowTS()
	defer s.dropWorkspaces()
	if insert {
		g.CreatedAt = g.UpdatedAt
		return dbErr(s.db.Exec(ctx, `INSERT INTO visionary_scim_groups (id, display_name, external_id, members, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
			g.ID, g.DisplayName, g.ExternalID, string(members), g.CreatedAt, g.UpdatedAt))
	}
	return dbErr(s.db.Exec(ctx, `UPDATE visionary_scim_groups SET display_name = ?, external_id = ?, members = ?, updated_at = ? WHERE id = ?`,
		g.DisplayName, g.ExternalID, string(members), g.UpdatedAt, g.ID))
}

func (s *Server) scimGroupView(g *scimGroup, names map[string]string) map[string]any {
	members := []map[string]string{}
	for _, id := range g.Members {
		members = append(members, map[string]string{"value": id, "display": names[id], "$ref": s.scimBase() + "/Users/" + id})
	}
	v := map[string]any{
		"schemas": []string{scimGroupSchema}, "id": g.ID, "displayName": g.DisplayName, "members": members,
		"meta": map[string]string{"resourceType": "Group", "created": scimTime(g.CreatedAt), "lastModified": scimTime(g.UpdatedAt),
			"location": s.scimBase() + "/Groups/" + g.ID},
	}
	if g.ExternalID != "" {
		v["externalId"] = g.ExternalID
	}
	return v
}

func (s *Server) scimNames(ctx context.Context) (map[string]string, error) {
	list, err := s.scimUsers(ctx, "", "")
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(list))
	for _, a := range list {
		out[a.ID] = a.Username
	}
	return out, nil
}

type scimMember struct {
	Value string `json:"value"`
}

func memberIDs(raw json.RawMessage, names map[string]string) ([]string, error) {
	var list []scimMember
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &list); err != nil {
			var one scimMember
			if json.Unmarshal(raw, &one) != nil {
				return nil, errors.New("members must be a list of {\"value\": user id}")
			}
			list = []scimMember{one}
		}
	}
	var out []string
	for _, m := range list {
		if _, ok := names[m.Value]; !ok {
			return nil, errors.New("no user with id " + m.Value)
		}
		if !slices.Contains(out, m.Value) {
			out = append(out, m.Value)
		}
	}
	return out, nil
}

func (s *Server) handleSCIMListGroups(w http.ResponseWriter, r *http.Request) {
	attr, value, err := scimFilter(r, "displayname", "externalid", "id")
	if err != nil {
		scimError(w, http.StatusBadRequest, "invalidFilter", err.Error())
		return
	}
	ctx := r.Context()
	groups, err := s.scimGroups(ctx)
	if err != nil {
		s.scimFail(w, err)
		return
	}
	names, err := s.scimNames(ctx)
	if err != nil {
		s.scimFail(w, err)
		return
	}
	var match []scimGroup
	for _, g := range groups {
		switch {
		case attr == "displayname" && !strings.EqualFold(g.DisplayName, value),
			attr == "externalid" && g.ExternalID != value,
			attr == "id" && g.ID != value:
			continue
		}
		match = append(match, g)
	}
	start, count := scimPage(r)
	excludeMembers := strings.Contains(r.URL.Query().Get("excludedAttributes"), "members")
	out := []any{}
	for i := start - 1; i < len(match) && len(out) < count; i++ {
		v := s.scimGroupView(&match[i], names)
		if excludeMembers {
			delete(v, "members")
		}
		out = append(out, v)
	}
	scimJSON(w, http.StatusOK, scimList(out, len(match), start))
}

func (s *Server) handleSCIMGetGroup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	groups, err := s.scimGroups(ctx)
	if err != nil {
		s.scimFail(w, err)
		return
	}
	g := findGroup(groups, r.PathValue("id"))
	if g == nil {
		scimError(w, http.StatusNotFound, "", "no such group")
		return
	}
	names, err := s.scimNames(ctx)
	if err != nil {
		s.scimFail(w, err)
		return
	}
	scimJSON(w, http.StatusOK, s.scimGroupView(g, names))
}

type scimGroupBody struct {
	DisplayName string          `json:"displayName"`
	ExternalID  string          `json:"externalId"`
	Members     json.RawMessage `json:"members"`
}

func (s *Server) handleSCIMCreateGroup(w http.ResponseWriter, r *http.Request) {
	var body scimGroupBody
	if err := readSCIM(r, &body); err != nil {
		scimError(w, http.StatusBadRequest, "invalidSyntax", err.Error())
		return
	}
	ctx := r.Context()
	groups, names, ok := s.scimGroupContext(w, r)
	if !ok {
		return
	}
	g := &scimGroup{ID: newAccountID(), DisplayName: clip(strings.TrimSpace(body.DisplayName), maxGroupName), ExternalID: body.ExternalID}
	if g.DisplayName == "" {
		scimError(w, http.StatusBadRequest, "invalidValue", "displayName is required")
		return
	}
	for _, o := range groups {
		if strings.EqualFold(o.DisplayName, g.DisplayName) {
			scimError(w, http.StatusConflict, "uniqueness", "there is already a group named "+g.DisplayName)
			return
		}
	}
	members, err := memberIDs(body.Members, names)
	if err != nil {
		scimError(w, http.StatusBadRequest, "invalidValue", err.Error())
		return
	}
	g.Members = members
	if err := s.saveGroup(ctx, g, true); err != nil {
		s.scimFail(w, err)
		return
	}
	s.audit(r, "scim.group_created", g.DisplayName, map[string]any{"members": len(g.Members)})
	s.scimApplyRoles(r, append(groups, *g), g.Members)
	w.Header().Set("Location", s.scimBase()+"/Groups/"+g.ID)
	scimJSON(w, http.StatusCreated, s.scimGroupView(g, names))
}

func (s *Server) scimGroupContext(w http.ResponseWriter, r *http.Request) ([]scimGroup, map[string]string, bool) {
	groups, err := s.scimGroups(r.Context())
	if err != nil {
		s.scimFail(w, err)
		return nil, nil, false
	}
	names, err := s.scimNames(r.Context())
	if err != nil {
		s.scimFail(w, err)
		return nil, nil, false
	}
	return groups, names, true
}

func (s *Server) handleSCIMReplaceGroup(w http.ResponseWriter, r *http.Request) {
	var body scimGroupBody
	if err := readSCIM(r, &body); err != nil {
		scimError(w, http.StatusBadRequest, "invalidSyntax", err.Error())
		return
	}
	s.scimChangeGroup(w, r, func(g *scimGroup, names map[string]string) error {
		if name := strings.TrimSpace(body.DisplayName); name != "" {
			g.DisplayName = clip(name, maxGroupName)
		}
		g.ExternalID = body.ExternalID
		members, err := memberIDs(body.Members, names)
		g.Members = members
		return err
	})
}

var memberFilter = regexp.MustCompile(`(?i)^members\[\s*value\s+eq\s+"([^"]*)"\s*\]$`)

func (s *Server) handleSCIMPatchGroup(w http.ResponseWriter, r *http.Request) {
	ops, err := readPatch(r)
	if err != nil {
		scimError(w, http.StatusBadRequest, "invalidSyntax", err.Error())
		return
	}
	s.scimChangeGroup(w, r, func(g *scimGroup, names map[string]string) error {
		for _, op := range ops {
			path := strings.TrimSpace(op.Path)
			lower := strings.ToLower(path)
			switch {
			case lower == "members":
				ids, err := memberIDs(op.Value, names)
				if err != nil {
					return err
				}
				switch op.Op {
				case "add":
					for _, id := range ids {
						if !slices.Contains(g.Members, id) {
							g.Members = append(g.Members, id)
						}
					}
				case "replace":
					g.Members = ids
				case "remove":
					if len(ids) == 0 {
						g.Members = nil
					}
					g.Members = slices.DeleteFunc(g.Members, func(m string) bool { return slices.Contains(ids, m) })
				}
			case memberFilter.MatchString(path) && op.Op == "remove":
				id := memberFilter.FindStringSubmatch(path)[1]
				g.Members = slices.DeleteFunc(g.Members, func(m string) bool { return m == id })
			case lower == "displayname":
				var name string
				if json.Unmarshal(op.Value, &name) != nil || strings.TrimSpace(name) == "" {
					return errors.New("displayName must be a non-empty string")
				}
				g.DisplayName = clip(strings.TrimSpace(name), maxGroupName)
			case lower == "externalid":
				var id string
				_ = json.Unmarshal(op.Value, &id)
				g.ExternalID = id
			case lower == "" && op.Op != "remove":
				var obj scimGroupBody
				if err := json.Unmarshal(op.Value, &obj); err != nil {
					return errors.New("an operation without a path needs an object value")
				}
				if name := strings.TrimSpace(obj.DisplayName); name != "" {
					g.DisplayName = clip(name, maxGroupName)
				}
				if obj.ExternalID != "" {
					g.ExternalID = obj.ExternalID
				}
				if len(obj.Members) > 0 {
					ids, err := memberIDs(obj.Members, names)
					if err != nil {
						return err
					}
					g.Members = ids
				}
			}
		}
		return nil
	})
}

func (s *Server) scimChangeGroup(w http.ResponseWriter, r *http.Request, change func(*scimGroup, map[string]string) error) {
	groups, names, ok := s.scimGroupContext(w, r)
	if !ok {
		return
	}
	g := findGroup(groups, r.PathValue("id"))
	if g == nil {
		scimError(w, http.StatusNotFound, "", "no such group")
		return
	}
	before := append([]string{}, g.Members...)
	oldName := g.DisplayName
	if err := change(g, names); err != nil {
		scimError(w, http.StatusBadRequest, "invalidValue", err.Error())
		return
	}
	if err := s.saveGroup(r.Context(), g, false); err != nil {
		s.scimFail(w, err)
		return
	}
	var added, removed []string
	for _, id := range g.Members {
		if !slices.Contains(before, id) {
			added = append(added, names[id])
		}
	}
	for _, id := range before {
		if !slices.Contains(g.Members, id) {
			removed = append(removed, names[id])
		}
	}
	detail := map[string]any{"added": added, "removed": removed}
	if oldName != g.DisplayName {
		detail["renamed_from"] = oldName
	}
	s.audit(r, "scim.group_updated", g.DisplayName, detail)
	affected := append(before, g.Members...)
	if oldName == g.DisplayName {
		affected = nil
		for _, id := range append(before, g.Members...) {
			if slices.Contains(before, id) != slices.Contains(g.Members, id) {
				affected = append(affected, id)
			}
		}
	}
	s.scimApplyRoles(r, groups, affected)
	scimJSON(w, http.StatusOK, s.scimGroupView(g, names))
}

func (s *Server) handleSCIMDeleteGroup(w http.ResponseWriter, r *http.Request) {
	groups, _, ok := s.scimGroupContext(w, r)
	if !ok {
		return
	}
	g := findGroup(groups, r.PathValue("id"))
	if g == nil {
		scimError(w, http.StatusNotFound, "", "no such group")
		return
	}
	if err := s.db.Exec(r.Context(), `DELETE FROM visionary_scim_groups WHERE id = ?`, g.ID); err != nil {
		s.scimFail(w, dbErr(err))
		return
	}
	s.audit(r, "scim.group_deleted", g.DisplayName, map[string]any{"members": len(g.Members)})
	rest := slices.DeleteFunc(slices.Clone(groups), func(o scimGroup) bool { return o.ID == g.ID })
	s.scimApplyRoles(r, rest, g.Members)
	w.WriteHeader(http.StatusNoContent)
}
