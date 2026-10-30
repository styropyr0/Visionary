package analytics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// scimKey stores a key of scope scim and returns it.
func scimKey(t *testing.T, s *Server, scope string) string {
	t.Helper()
	secret, _ := newKeySecret()
	if err := s.db.Exec(context.Background(), `INSERT INTO visionary_api_keys (name, scope, prefix, key_hash, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"idp", scope, secret[:keyVisibleLen], hashKey(secret), "test", nowTS()); err != nil {
		t.Fatal(err)
	}
	s.invalidateKeys()
	return secret
}

func scimCall(t *testing.T, h http.Handler, key, method, target string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	w := call(t, h, method, target, body, nil, "Authorization", "Bearer "+key, "Content-Type", scimContentType)
	out := map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

func TestSCIMUsersAndGroups(t *testing.T) {
	s := newSSOServer(t)
	f := newFakeOIDC(t)
	h := setupOIDC(t, s, f)
	if w := call(t, h, "GET", "/api/settings/sso", nil, nil); w.Code != 401 {
		t.Fatalf("settings should need sign-in now: %d", w.Code)
	}
	// SCIM's group mapping goes in with the providers; write it directly.
	st := s.ssoSetup()
	cfg := st.cfg
	cfg.SCIM = scimConfig{RoleMap: "Ops=editor,Admins=admin", DefaultRole: "viewer"}
	raw, _ := json.Marshal(cfg)
	if err := s.writeSettings(context.Background(), map[string]string{settingSSO: string(raw)}); err != nil {
		t.Fatal(err)
	}
	_ = s.refreshLive(context.Background())

	key := scimKey(t, s, "scim")
	read := scimKey(t, s, "read")
	if w, _ := scimCall(t, h, read, "GET", "/scim/v2/Users", nil); w.Code != 401 {
		t.Fatalf("a read key reached SCIM: %d", w.Code)
	}
	if w := call(t, h, "GET", "/scim/v2/Users", nil, nil); w.Code != 401 || !strings.Contains(w.Body.String(), scimErrorSchema) {
		t.Fatalf("no key: %d %s", w.Code, w.Body)
	}

	w, user := scimCall(t, h, key, "POST", "/scim/v2/Users", map[string]any{
		"schemas": []string{scimUserSchema}, "userName": "Dana@acme.com", "externalId": "00u1",
		"name": map[string]string{"givenName": "Dana", "familyName": "Ng"}, "emails": []map[string]any{{"value": "dana@acme.com", "primary": true}}, "active": true,
	})
	if w.Code != 201 || user["userName"] != "dana@acme.com" || user["active"] != true {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	id := user["id"].(string)
	if w, _ := scimCall(t, h, key, "POST", "/scim/v2/Users", map[string]any{"userName": "dana@acme.com"}); w.Code != 409 {
		t.Fatalf("duplicate: %d", w.Code)
	}
	_, list := scimCall(t, h, key, "GET", `/scim/v2/Users?filter=userName+eq+%22dana@acme.com%22`, nil)
	if list["totalResults"] != float64(1) {
		t.Fatalf("filter: %v", list)
	}
	if w, _ := scimCall(t, h, key, "GET", `/scim/v2/Users?filter=title+co+%22x%22`, nil); w.Code != 400 {
		t.Fatalf("unsupported filter: %d", w.Code)
	}

	// Groups set the role.
	w, group := scimCall(t, h, key, "POST", "/scim/v2/Groups", map[string]any{"displayName": "Ops", "members": []map[string]string{{"value": id}}})
	if w.Code != 201 {
		t.Fatalf("group: %d %s", w.Code, w.Body)
	}
	if a, _ := s.findAccount(context.Background(), "id", id); a.Role != "editor" {
		t.Fatalf("role from group: %q", a.Role)
	}

	// Dana signs in through the provider, which sends no groups: SCIM's role stands.
	f.claims = map[string]any{"email": "dana@acme.com", "email_verified": true, "groups": []string{"sales"}}
	w = oidcSignIn(t, h, f)
	sess := cookieNamed(w, sessionCookie)
	if sess == nil {
		t.Fatalf("sign-in: %s", ssoErrorOf(w))
	}
	if w := call(t, h, "GET", "/api/session", nil, []*http.Cookie{sess}); !strings.Contains(w.Body.String(), `"role":"editor"`) {
		t.Fatalf("session: %s", w.Body)
	}

	// Leaving the group drops to the default role; Entra-style PATCH deactivates.
	gid := group["id"].(string)
	w, _ = scimCall(t, h, key, "PATCH", "/scim/v2/Groups/"+gid, map[string]any{
		"schemas": []string{scimPatchSchema}, "Operations": []map[string]any{{"op": "remove", "path": `members[value eq "` + id + `"]`}},
	})
	if a, _ := s.findAccount(context.Background(), "id", id); w.Code != 200 || a.Role != "viewer" {
		t.Fatalf("leave group: %d %q", w.Code, a.Role)
	}
	w, user = scimCall(t, h, key, "PATCH", "/scim/v2/Users/"+id, map[string]any{
		"schemas": []string{scimPatchSchema}, "Operations": []map[string]any{{"op": "Replace", "value": map[string]any{"active": "False"}}},
	})
	if w.Code != 200 || user["active"] != false {
		t.Fatalf("deactivate: %d %s", w.Code, w.Body)
	}
	if w := call(t, h, "GET", "/api/session", nil, []*http.Cookie{sess}); w.Code != 401 {
		t.Fatalf("a deactivated user kept their session: %d", w.Code)
	}
	if msg := ssoErrorOf(oidcSignIn(t, h, f)); !strings.Contains(msg, "disabled") {
		t.Fatalf("deactivated sign-in: %q", msg)
	}

	// Path form, then delete.
	w, user = scimCall(t, h, key, "PATCH", "/scim/v2/Users/"+id, map[string]any{
		"Operations": []map[string]any{{"op": "replace", "path": "active", "value": true}, {"op": "replace", "path": "displayName", "value": "Dana N."}},
	})
	if w.Code != 200 || user["active"] != true || user["displayName"] != "Dana N." {
		t.Fatalf("patch path: %d %s", w.Code, w.Body)
	}
	if w, _ := scimCall(t, h, key, "DELETE", "/scim/v2/Users/"+id, nil); w.Code != 204 {
		t.Fatalf("delete: %d", w.Code)
	}
	if w, _ := scimCall(t, h, key, "GET", "/scim/v2/Users/"+id, nil); w.Code != 404 {
		t.Fatalf("deleted user: %d", w.Code)
	}
	_, g := scimCall(t, h, key, "GET", "/scim/v2/Groups/"+gid, nil)
	if m, _ := g["members"].([]any); len(m) != 0 {
		t.Fatalf("deleted user still in group: %v", g)
	}

	// Password accounts are out of SCIM's reach.
	if err := s.db.Exec(context.Background(), `INSERT INTO visionary_users (id, username, name, role, password_hash, password_at, disabled, created_by, created_at)
VALUES ('local1', 'admin', 'Admin', 'admin', 'x', ?, 0, 't', ?)`, nowTS(), nowTS()); err != nil {
		t.Fatal(err)
	}
	if w, _ := scimCall(t, h, key, "GET", "/scim/v2/Users/local1", nil); w.Code != 404 {
		t.Fatalf("local account visible: %d", w.Code)
	}

	for _, p := range []string{"/scim/v2/ServiceProviderConfig", "/scim/v2/Schemas", "/scim/v2/ResourceTypes"} {
		if w, _ := scimCall(t, h, key, "GET", p, nil); w.Code != 200 || w.Header().Get("Content-Type") != scimContentType {
			t.Errorf("%s: %d", p, w.Code)
		}
	}

	// No writer runs in tests, so the entries wait in the queue.
	seen := map[string]bool{}
	for len(s.team().audits) > 0 {
		seen[(<-s.team().audits).action] = true
	}
	for _, a := range []string{"settings.sso", "auth.sign_in", "auth.sign_in_refused", "scim.user_created", "scim.group_created", "scim.role_changed", "scim.user_deactivated", "scim.user_deleted"} {
		if !seen[a] {
			t.Errorf("no %s in the audit log", a)
		}
	}
}
