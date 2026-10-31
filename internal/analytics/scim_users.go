package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
)

// SCIM Users. Only people from single sign-on or SCIM are visible here;
// accounts with a password are Visionary's own and SCIM can't see or change
// them.

func (s *Server) scimUsers(ctx context.Context, attr, value string) ([]account, error) {
	where, args := "source <> ''", []any{}
	switch attr {
	case "username":
		where, args = where+" AND username = ?", append(args, strings.ToLower(strings.TrimSpace(value)))
	case "externalid":
		where, args = where+" AND external_id = ?", append(args, value)
	case "id":
		where, args = where+" AND id = ?", append(args, value)
	case "emails.value", "emails":
		where, args = where+" AND email = ?", append(args, strings.ToLower(strings.TrimSpace(value)))
	}
	res, err := s.db.Query(ctx, `SELECT `+accountColumns+` FROM visionary_users WHERE `+where+` ORDER BY created_at, id`, args...)
	if err != nil {
		return nil, dbErr(err)
	}
	out := make([]account, 0, len(res.Rows))
	for _, r := range res.Rows {
		out = append(out, accountFrom(r))
	}
	return out, nil
}

func (s *Server) scimUser(ctx context.Context, id string) (*account, error) {
	list, err := s.scimUsers(ctx, "id", id)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

func (s *Server) scimUserView(a *account, groups []scimGroup) map[string]any {
	member := []map[string]string{}
	for _, g := range groups {
		if slices.Contains(g.Members, a.ID) {
			member = append(member, map[string]string{"value": g.ID, "display": g.DisplayName, "$ref": s.scimBase() + "/Groups/" + g.ID})
		}
	}
	v := map[string]any{
		"schemas": []string{scimUserSchema}, "id": a.ID, "userName": a.Username,
		"name":        map[string]string{"formatted": a.Name},
		"displayName": a.Name, "active": !a.Disabled, "groups": member,
		"meta": map[string]string{"resourceType": "User", "created": scimTime(a.CreatedAt), "lastModified": scimTime(a.CreatedAt),
			"location": s.scimBase() + "/Users/" + a.ID},
	}
	if a.ExternalID != "" {
		v["externalId"] = a.ExternalID
	}
	if a.Email != "" {
		v["emails"] = []map[string]any{{"value": a.Email, "type": "work", "primary": true}}
	}
	return v
}

type scimUserState struct {
	userName, externalID, email string
	displayName, formatted      string
	givenName, familyName       string
	active                      bool
}

func stateOf(a *account) *scimUserState {
	return &scimUserState{userName: a.Username, externalID: a.ExternalID, email: a.Email, displayName: a.Name, active: !a.Disabled}
}

func (u *scimUserState) name() string {
	for _, n := range []string{u.displayName, u.formatted, strings.TrimSpace(u.givenName + " " + u.familyName)} {
		if n = strings.TrimSpace(n); n != "" {
			return n
		}
	}
	return ""
}

// set applies one attribute by its path, lowercased. Attributes Visionary
// doesn't keep (title, locale, enterprise extensions) are ignored. A nil
// value clears it.
func (u *scimUserState) set(path string, v json.RawMessage) error {
	str := func() string {
		var s string
		if v != nil {
			_ = json.Unmarshal(v, &s)
		}
		return strings.TrimSpace(s)
	}
	switch {
	case path == "active":
		if v == nil {
			return nil
		}
		b, err := scimBool(v)
		if err != nil {
			return err
		}
		u.active = b
	case path == "username":
		u.userName = str()
	case path == "externalid":
		u.externalID = str()
	case path == "displayname":
		u.displayName = str()
	case path == "name.formatted":
		u.formatted = str()
	case path == "name.givenname":
		u.givenName = str()
	case path == "name.familyname":
		u.familyName = str()
	case path == "name":
		var n struct {
			Formatted  string `json:"formatted"`
			GivenName  string `json:"givenName"`
			FamilyName string `json:"familyName"`
		}
		if v != nil {
			_ = json.Unmarshal(v, &n)
		}
		u.formatted, u.givenName, u.familyName = n.Formatted, n.GivenName, n.FamilyName
	case path == "emails":
		var list []struct {
			Value   string `json:"value"`
			Primary any    `json:"primary"`
		}
		if v != nil {
			_ = json.Unmarshal(v, &list)
		}
		u.email = ""
		for i, e := range list {
			if i == 0 || e.Primary == true || e.Primary == "true" || e.Primary == "True" {
				u.email = strings.ToLower(strings.TrimSpace(e.Value))
			}
		}
	case strings.HasPrefix(path, "emails[") && strings.HasSuffix(path, "].value"):
		u.email = strings.ToLower(str())
	}
	return nil
}

func (u *scimUserState) apply(ops []patchOp) error {
	for _, op := range ops {
		path := strings.ToLower(strings.TrimSpace(op.Path))
		if op.Op == "remove" {
			if path == "" {
				return errors.New("remove needs a path")
			}
			if err := u.set(path, nil); err != nil {
				return err
			}
			continue
		}
		if path != "" {
			if err := u.set(path, op.Value); err != nil {
				return err
			}
			continue
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(op.Value, &obj); err != nil {
			return errors.New("an operation without a path needs an object value")
		}
		for k, v := range obj {
			if err := u.set(strings.ToLower(k), v); err != nil {
				return err
			}
		}
	}
	return nil
}

func scimStateFromBody(r *http.Request) (*scimUserState, error) {
	var obj map[string]json.RawMessage
	if err := readSCIM(r, &obj); err != nil {
		return nil, err
	}
	u := &scimUserState{active: true}
	for k, v := range obj {
		if err := u.set(strings.ToLower(k), v); err != nil {
			return nil, err
		}
	}
	return u, nil
}

func (u *scimUserState) check() error {
	name, err := normalizeUsername(u.userName)
	if err != nil {
		return errors.New("userName: " + err.Error())
	}
	u.userName = name
	if u.email == "" && strings.Contains(name, "@") {
		u.email = name
	}
	return nil
}

func (s *Server) handleSCIMListUsers(w http.ResponseWriter, r *http.Request) {
	attr, value, err := scimFilter(r, "username", "externalid", "id", "emails.value", "emails")
	if err != nil {
		scimError(w, http.StatusBadRequest, "invalidFilter", err.Error())
		return
	}
	list, err := s.scimUsers(r.Context(), attr, value)
	if err != nil {
		s.scimFail(w, err)
		return
	}
	groups, err := s.scimGroups(r.Context())
	if err != nil {
		s.scimFail(w, err)
		return
	}
	start, count := scimPage(r)
	out := []any{}
	for i := start - 1; i < len(list) && len(out) < count; i++ {
		out = append(out, s.scimUserView(&list[i], groups))
	}
	scimJSON(w, http.StatusOK, scimList(out, len(list), start))
}

func (s *Server) handleSCIMGetUser(w http.ResponseWriter, r *http.Request) {
	a, err := s.scimUser(r.Context(), r.PathValue("id"))
	if err != nil {
		s.scimFail(w, err)
		return
	}
	if a == nil {
		scimError(w, http.StatusNotFound, "", "no such user")
		return
	}
	groups, err := s.scimGroups(r.Context())
	if err != nil {
		s.scimFail(w, err)
		return
	}
	scimJSON(w, http.StatusOK, s.scimUserView(a, groups))
}

func (s *Server) handleSCIMCreateUser(w http.ResponseWriter, r *http.Request) {
	u, err := scimStateFromBody(r)
	if err == nil {
		err = u.check()
	}
	if err != nil {
		scimError(w, http.StatusBadRequest, "invalidValue", err.Error())
		return
	}
	ctx := r.Context()
	if existing, err := s.findAccount(ctx, "username", u.userName); err != nil {
		s.scimFail(w, err)
		return
	} else if existing != nil {
		scimError(w, http.StatusConflict, "uniqueness", "there is already a user named "+u.userName)
		return
	}
	now := nowTS()
	a := &account{ID: newAccountID(), Username: u.userName, Name: clip(fallback(u.name(), u.userName), maxAccountNameLen), Email: u.email,
		ExternalID: u.externalID, Disabled: !u.active, Source: scimSource, CreatedBy: sessionFrom(r).Name, CreatedAt: now, PasswordAt: now}
	if s.scimManagesRoles() {
		a.Role = s.scimRole(nil).String()
	}
	if err := s.db.Exec(ctx, `INSERT INTO visionary_users (id, username, name, role, password_hash, password_at, disabled, created_by, created_at, source, email, external_id)
VALUES (?, ?, ?, ?, '', ?, ?, ?, ?, ?, ?, ?)`, a.ID, a.Username, a.Name, a.Role, now, boolInt(a.Disabled), a.CreatedBy, now, scimSource, a.Email, a.ExternalID); err != nil {
		s.scimFail(w, dbErr(err))
		return
	}
	s.audit(r, "scim.user_created", a.Username, map[string]any{"role": a.Role, "active": u.active, "external_id": a.ExternalID})
	w.Header().Set("Location", s.scimBase()+"/Users/"+a.ID)
	scimJSON(w, http.StatusCreated, s.scimUserView(a, nil))
}

func (s *Server) handleSCIMReplaceUser(w http.ResponseWriter, r *http.Request) {
	u, err := scimStateFromBody(r)
	if err != nil {
		scimError(w, http.StatusBadRequest, "invalidValue", err.Error())
		return
	}
	s.scimSaveUser(w, r, func(*scimUserState) (*scimUserState, error) { return u, nil })
}

func (s *Server) handleSCIMPatchUser(w http.ResponseWriter, r *http.Request) {
	ops, err := readPatch(r)
	if err != nil {
		scimError(w, http.StatusBadRequest, "invalidSyntax", err.Error())
		return
	}
	s.scimSaveUser(w, r, func(u *scimUserState) (*scimUserState, error) { return u, u.apply(ops) })
}

// scimSaveUser changes a user with change, which gets the current state.
// Turning someone inactive signs them out: at once on this server, within a
// minute on the others.
func (s *Server) scimSaveUser(w http.ResponseWriter, r *http.Request, change func(*scimUserState) (*scimUserState, error)) {
	ctx := r.Context()
	a, err := s.scimUser(ctx, r.PathValue("id"))
	if err != nil {
		s.scimFail(w, err)
		return
	}
	if a == nil {
		scimError(w, http.StatusNotFound, "", "no such user")
		return
	}
	u, err := change(stateOf(a))
	if err == nil {
		err = u.check()
	}
	if err != nil {
		scimError(w, http.StatusBadRequest, "invalidValue", err.Error())
		return
	}
	if u.userName != a.Username {
		if other, err := s.findAccount(ctx, "username", u.userName); err != nil {
			s.scimFail(w, err)
			return
		} else if other != nil {
			scimError(w, http.StatusConflict, "uniqueness", "there is already a user named "+u.userName)
			return
		}
	}
	wasActive := !a.Disabled
	next := *a
	next.Username, next.Name, next.Email, next.ExternalID, next.Disabled, next.Source =
		u.userName, clip(fallback(u.name(), a.Name), maxAccountNameLen), u.email, u.externalID, !u.active, scimSource
	if err := s.db.Exec(ctx, `UPDATE visionary_users SET username = ?, name = ?, email = ?, external_id = ?, disabled = ?, source = ? WHERE id = ?`,
		next.Username, next.Name, next.Email, next.ExternalID, boolInt(next.Disabled), scimSource, a.ID); err != nil {
		s.scimFail(w, dbErr(err))
		return
	}
	s.touchUser(a.ID)
	action := "scim.user_updated"
	switch {
	case wasActive && !u.active:
		action = "scim.user_deactivated"
	case !wasActive && u.active:
		action = "scim.user_reactivated"
	}
	detail := map[string]any{"active": u.active}
	if a.Username != next.Username {
		detail["renamed_from"] = a.Username
	}
	s.audit(r, action, next.Username, detail)
	groups, err := s.scimGroups(ctx)
	if err != nil {
		s.scimFail(w, err)
		return
	}
	scimJSON(w, http.StatusOK, s.scimUserView(&next, groups))
}

func (s *Server) handleSCIMDeleteUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	a, err := s.scimUser(ctx, r.PathValue("id"))
	if err != nil {
		s.scimFail(w, err)
		return
	}
	if a == nil {
		scimError(w, http.StatusNotFound, "", "no such user")
		return
	}
	if err := s.db.Exec(ctx, `DELETE FROM visionary_users WHERE id = ?`, a.ID); err != nil {
		s.scimFail(w, dbErr(err))
		return
	}
	s.touchUser(a.ID)
	if err := s.scimForget(ctx, a.ID); err != nil {
		s.logger.Warn("scim: removing a deleted user from groups failed", "err", err)
	}
	s.audit(r, "scim.user_deleted", a.Username, nil)
	w.WriteHeader(http.StatusNoContent)
}
