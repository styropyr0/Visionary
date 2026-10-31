package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	sdk "github.com/styropyr0/visionary/sdks/go"
)

// SCIM 2.0 (RFC 7643, 7644): an identity provider creates, changes and removes
// people and their groups with an API key of scope scim. Managed people live in
// visionary_users with source scim and sign in through single sign-on; groups
// live in visionary_scim_groups.

const (
	scimSource      = "scim"
	scimContentType = "application/scim+json"
	scimUserSchema  = "urn:ietf:params:scim:schemas:core:2.0:User"
	scimGroupSchema = "urn:ietf:params:scim:schemas:core:2.0:Group"
	scimListSchema  = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
	scimPatchSchema = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
	scimErrorSchema = "urn:ietf:params:scim:api:messages:2.0:Error"
	scimMaxCount    = 200
	scimMaxBody     = 1 << 20
)

func (s *Server) scimSchema() []string {
	id, name, ts, tail := "TEXT", "TEXT", "TEXT", ""
	if s.q.d == sdk.MySQL {
		id, name, ts, tail = "VARCHAR(32)", "VARCHAR(255)", "VARCHAR(32)", " CHARACTER SET utf8mb4"
	}
	return []string{`CREATE TABLE IF NOT EXISTS visionary_scim_groups (
    id           ` + id + ` NOT NULL PRIMARY KEY,
    display_name ` + name + ` NOT NULL,
    external_id  TEXT,
    members      TEXT NOT NULL,
    created_at   ` + ts + ` NOT NULL,
    updated_at   ` + ts + ` NOT NULL
)` + tail}
}

func (s *Server) scimRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /scim/v2/ServiceProviderConfig", s.scimAuthed(s.handleSCIMConfig))
	mux.HandleFunc("GET /scim/v2/ResourceTypes", s.scimAuthed(s.handleSCIMResourceTypes))
	mux.HandleFunc("GET /scim/v2/Schemas", s.scimAuthed(s.handleSCIMSchemas))
	mux.HandleFunc("GET /scim/v2/Users", s.scimAuthed(s.handleSCIMListUsers))
	mux.HandleFunc("POST /scim/v2/Users", s.scimAuthed(s.handleSCIMCreateUser))
	mux.HandleFunc("GET /scim/v2/Users/{id}", s.scimAuthed(s.handleSCIMGetUser))
	mux.HandleFunc("PUT /scim/v2/Users/{id}", s.scimAuthed(s.handleSCIMReplaceUser))
	mux.HandleFunc("PATCH /scim/v2/Users/{id}", s.scimAuthed(s.handleSCIMPatchUser))
	mux.HandleFunc("DELETE /scim/v2/Users/{id}", s.scimAuthed(s.handleSCIMDeleteUser))
	mux.HandleFunc("GET /scim/v2/Groups", s.scimAuthed(s.handleSCIMListGroups))
	mux.HandleFunc("POST /scim/v2/Groups", s.scimAuthed(s.handleSCIMCreateGroup))
	mux.HandleFunc("GET /scim/v2/Groups/{id}", s.scimAuthed(s.handleSCIMGetGroup))
	mux.HandleFunc("PUT /scim/v2/Groups/{id}", s.scimAuthed(s.handleSCIMReplaceGroup))
	mux.HandleFunc("PATCH /scim/v2/Groups/{id}", s.scimAuthed(s.handleSCIMPatchGroup))
	mux.HandleFunc("DELETE /scim/v2/Groups/{id}", s.scimAuthed(s.handleSCIMDeleteGroup))
	mux.HandleFunc("/scim/v2/", func(w http.ResponseWriter, _ *http.Request) {
		scimError(w, http.StatusNotFound, "", "not found")
	})
}

// scimAuthed accepts a key of scope scim. The key stands in as the user in
// the audit log.
func (s *Server) scimAuthed(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, ok := bearerKey(r)
		if !ok {
			scimError(w, http.StatusUnauthorized, "", "send a SCIM key from Settings → API keys as Authorization: Bearer vis_…")
			return
		}
		k, ok := s.lookupKey(r.Context(), raw, "scim")
		if !ok {
			scimError(w, http.StatusUnauthorized, "", "invalid, expired or revoked SCIM key")
			return
		}
		if needsJSON(r) {
			mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || (mt != scimContentType && mt != "application/json") {
				scimError(w, http.StatusUnsupportedMediaType, "", "expected application/scim+json")
				return
			}
		}
		sess := &session{UserID: "key:" + strconv.FormatInt(k.id, 10), Name: "SCIM key " + k.name, Roles: []string{"scim"}}
		h(w, r.WithContext(context.WithValue(r.Context(), sessionKey{}, sess)))
	}
}

func scimJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", scimContentType)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// scimError answers in SCIM's error shape. scimType is one of RFC 7644's
// (invalidValue, uniqueness, invalidFilter, ...) or empty.
func scimError(w http.ResponseWriter, status int, scimType, detail string) {
	body := map[string]any{"schemas": []string{scimErrorSchema}, "status": strconv.Itoa(status), "detail": sentence(detail)}
	if scimType != "" {
		body["scimType"] = scimType
	}
	scimJSON(w, status, body)
}

func (s *Server) scimFail(w http.ResponseWriter, err error) {
	s.logger.Error("scim request failed", "err", err)
	scimError(w, http.StatusInternalServerError, "", "the request couldn't be completed; try again")
}

func readSCIM(r *http.Request, dst any) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, scimMaxBody))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, dst); err != nil {
		return errors.New("the body isn't valid JSON")
	}
	return nil
}

func (s *Server) scimBase() string {
	return strings.TrimRight(s.ssoSetup().cfg.PublicURL, "/") + "/scim/v2"
}

func scimList(resources []any, total, start int) map[string]any {
	return map[string]any{
		"schemas": []string{scimListSchema}, "totalResults": total, "startIndex": start,
		"itemsPerPage": len(resources), "Resources": resources,
	}
}

func scimPage(r *http.Request) (start, count int) {
	start, count = 1, 100
	if n, err := strconv.Atoi(r.URL.Query().Get("startIndex")); err == nil && n > 1 {
		start = n
	}
	if n, err := strconv.Atoi(r.URL.Query().Get("count")); err == nil && n >= 0 {
		count = min(n, scimMaxCount)
	}
	return start, count
}

var scimFilterPattern = regexp.MustCompile(`^\s*([A-Za-z.]+)\s+(?i:eq)\s+"((?:[^"\\]|\\.)*)"\s*$`)

// scimFilter reads the one filter identity providers send: attribute eq
// "value". It returns the attribute in lowercase.
func scimFilter(r *http.Request, allowed ...string) (attr, value string, err error) {
	f := r.URL.Query().Get("filter")
	if strings.TrimSpace(f) == "" {
		return "", "", nil
	}
	m := scimFilterPattern.FindStringSubmatch(f)
	if m == nil {
		return "", "", fmt.Errorf(`only filters of the form attribute eq "value" are supported`)
	}
	attr = strings.ToLower(m[1])
	if !slices.Contains(allowed, attr) {
		return "", "", fmt.Errorf("filtering on %s isn't supported; use %s", m[1], strings.Join(allowed, ", "))
	}
	value, _ = strconv.Unquote(`"` + m[2] + `"`)
	return attr, value, nil
}

// patchOp is one operation of a PATCH. Op is matched without case: Entra
// sends "Replace".
type patchOp struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value"`
}

func readPatch(r *http.Request) ([]patchOp, error) {
	var body struct {
		Schemas    []string  `json:"schemas"`
		Operations []patchOp `json:"Operations"`
	}
	if err := readSCIM(r, &body); err != nil {
		return nil, err
	}
	if len(body.Operations) == 0 {
		return nil, errors.New("a PATCH needs Operations")
	}
	for i := range body.Operations {
		op := strings.ToLower(body.Operations[i].Op)
		if op != "add" && op != "replace" && op != "remove" {
			return nil, fmt.Errorf("unknown op %q: use add, replace or remove", body.Operations[i].Op)
		}
		body.Operations[i].Op = op
	}
	return body.Operations, nil
}

// scimBool reads true/false, also as the strings "True" and "False".
func scimBool(raw json.RawMessage) (bool, error) {
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return b, nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if v, err := strconv.ParseBool(strings.TrimSpace(s)); err == nil {
			return v, nil
		}
	}
	return false, errors.New("active must be true or false")
}

func scimTime(ts string) string {
	t, err := time.Parse(tsLayout, ts)
	if err != nil {
		return ts
	}
	return t.UTC().Format(time.RFC3339)
}

func (s *Server) handleSCIMConfig(w http.ResponseWriter, _ *http.Request) {
	scimJSON(w, http.StatusOK, map[string]any{
		"schemas":          []string{"urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"},
		"documentationUri": "",
		"patch":            map[string]bool{"supported": true},
		"bulk":             map[string]any{"supported": false, "maxOperations": 0, "maxPayloadSize": 0},
		"filter":           map[string]any{"supported": true, "maxResults": scimMaxCount},
		"changePassword":   map[string]bool{"supported": false},
		"sort":             map[string]bool{"supported": false},
		"etag":             map[string]bool{"supported": false},
		"authenticationSchemes": []map[string]any{{
			"type": "oauthbearertoken", "name": "Bearer token", "primary": true,
			"description": "An API key of scope scim from Settings → API keys.",
		}},
		"meta": map[string]string{"resourceType": "ServiceProviderConfig", "location": s.scimBase() + "/ServiceProviderConfig"},
	})
}

func (s *Server) handleSCIMResourceTypes(w http.ResponseWriter, _ *http.Request) {
	base := s.scimBase()
	types := []any{
		map[string]any{"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:ResourceType"}, "id": "User", "name": "User",
			"endpoint": "/Users", "schema": scimUserSchema, "meta": map[string]string{"resourceType": "ResourceType", "location": base + "/ResourceTypes/User"}},
		map[string]any{"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:ResourceType"}, "id": "Group", "name": "Group",
			"endpoint": "/Groups", "schema": scimGroupSchema, "meta": map[string]string{"resourceType": "ResourceType", "location": base + "/ResourceTypes/Group"}},
	}
	scimJSON(w, http.StatusOK, scimList(types, len(types), 1))
}

func scimAttr(name, typ string, required bool, extra map[string]any) map[string]any {
	a := map[string]any{"name": name, "type": typ, "multiValued": false, "required": required, "caseExact": false,
		"mutability": "readWrite", "returned": "default", "uniqueness": "none"}
	for k, v := range extra {
		a[k] = v
	}
	return a
}

func (s *Server) handleSCIMSchemas(w http.ResponseWriter, _ *http.Request) {
	base := s.scimBase()
	member := []any{scimAttr("value", "string", false, nil), scimAttr("display", "string", false, nil)}
	user := map[string]any{
		"id": scimUserSchema, "name": "User", "description": "Someone who signs in to Visionary through single sign-on",
		"attributes": []any{
			scimAttr("userName", "string", true, map[string]any{"uniqueness": "server"}),
			scimAttr("name", "complex", false, map[string]any{"subAttributes": []any{
				scimAttr("formatted", "string", false, nil), scimAttr("givenName", "string", false, nil), scimAttr("familyName", "string", false, nil)}}),
			scimAttr("displayName", "string", false, nil),
			scimAttr("emails", "complex", false, map[string]any{"multiValued": true, "subAttributes": []any{
				scimAttr("value", "string", false, nil), scimAttr("type", "string", false, nil), scimAttr("primary", "boolean", false, nil)}}),
			scimAttr("active", "boolean", false, nil),
			scimAttr("groups", "complex", false, map[string]any{"multiValued": true, "mutability": "readOnly", "subAttributes": member}),
		},
		"meta": map[string]string{"resourceType": "Schema", "location": base + "/Schemas/" + scimUserSchema},
	}
	group := map[string]any{
		"id": scimGroupSchema, "name": "Group", "description": "A group whose members get the role it maps to",
		"attributes": []any{
			scimAttr("displayName", "string", true, nil),
			scimAttr("members", "complex", false, map[string]any{"multiValued": true, "subAttributes": member}),
		},
		"meta": map[string]string{"resourceType": "Schema", "location": base + "/Schemas/" + scimGroupSchema},
	}
	schemas := []any{user, group}
	scimJSON(w, http.StatusOK, scimList(schemas, len(schemas), 1))
}
