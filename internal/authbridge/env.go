package authbridge

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ApplyEnv overlays VISIONARY_AUTH_* variables on c, so a deployment can be
// configured with environment variables alone, or tweak a JSON config per
// environment. Unset variables leave c unchanged.
//
// Endpoints are written as "METHOD /path", bodies and headers as JSON.
func ApplyEnv(c *Config, getenv func(string) string) error {
	get := func(k string) string { return strings.TrimSpace(getenv(k)) }
	set := func(dst *string, k string) {
		if v := get(k); v != "" {
			*dst = v
		}
	}
	setInt := func(dst *int, k string) error {
		v := get(k)
		if v == "" {
			return nil
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("%s must be a number", k)
		}
		*dst = n
		return nil
	}

	set(&c.Type, "VISIONARY_AUTH_MODE")
	set(&c.Provider, "VISIONARY_AUTH_PROVIDER")
	set(&c.BaseURL, "VISIONARY_AUTH_BASE_URL")
	set(&c.Timeout, "VISIONARY_AUTH_TIMEOUT")
	set(&c.Location, "VISIONARY_AUTH_LOCATION")

	set(&c.Identifier.Label, "VISIONARY_AUTH_IDENTIFIER_LABEL")
	set(&c.Identifier.Placeholder, "VISIONARY_AUTH_IDENTIFIER_PLACEHOLDER")
	set(&c.Identifier.Input, "VISIONARY_AUTH_IDENTIFIER_INPUT")
	set(&c.Identifier.Normalize, "VISIONARY_AUTH_IDENTIFIER_NORMALIZE")
	set(&c.Identifier.Pattern, "VISIONARY_AUTH_IDENTIFIER_PATTERN")
	set(&c.Identifier.PatternHint, "VISIONARY_AUTH_IDENTIFIER_HINT")
	if err := setInt(&c.CodeLength, "VISIONARY_AUTH_CODE_LENGTH"); err != nil {
		return err
	}
	if err := setInt(&c.ResendSeconds, "VISIONARY_AUTH_RESEND_SECONDS"); err != nil {
		return err
	}

	for _, e := range []struct {
		dst  **Endpoint
		name string
	}{
		{&c.RequestCode, "VISIONARY_AUTH_START"},
		{nil, "VISIONARY_AUTH_VERIFY"},
		{&c.Profile, "VISIONARY_AUTH_PROFILE"},
		{&c.Logout, "VISIONARY_AUTH_LOGOUT"},
	} {
		target := e.dst
		if target == nil {
			v := &c.Verify
			target = &v
		}
		if err := applyEndpoint(target, e.name, get); err != nil {
			return err
		}
	}

	set(&c.Token.Header, "VISIONARY_AUTH_TOKEN_HEADER")
	if v, ok := lookupRaw(getenv, "VISIONARY_AUTH_TOKEN_PREFIX"); ok {
		// The prefix keeps its trailing space ("Bearer ").
		c.Token.Prefix = v
	}
	if v := get("VISIONARY_AUTH_HEADERS"); v != "" {
		if err := json.Unmarshal([]byte(v), &c.Headers); err != nil {
			return fmt.Errorf("VISIONARY_AUTH_HEADERS must be a JSON object: %w", err)
		}
	}

	set(&c.Fields.SentTo, "VISIONARY_AUTH_FIELD_SENT_TO")
	set(&c.Fields.Token, "VISIONARY_AUTH_FIELD_TOKEN")
	set(&c.Fields.ExpiresAt, "VISIONARY_AUTH_FIELD_EXPIRES_AT")
	set(&c.Fields.UserID, "VISIONARY_AUTH_FIELD_USER_ID")
	set(&c.Fields.Name, "VISIONARY_AUTH_FIELD_NAME")
	set(&c.Fields.Handle, "VISIONARY_AUTH_FIELD_HANDLE")
	set(&c.Fields.Role, "VISIONARY_AUTH_FIELD_ROLE")
	set(&c.Fields.ErrorMessage, "VISIONARY_AUTH_FIELD_ERROR")

	if v := get("VISIONARY_ALLOWED_ROLES"); v != "" {
		c.AllowedRoles = nil
		for _, r := range strings.Split(v, ",") {
			if r = strings.TrimSpace(r); r != "" {
				c.AllowedRoles = append(c.AllowedRoles, r)
			}
		}
	}
	set(&c.SharedPassword, "VISIONARY_SHARED_PASSWORD")
	return nil
}

// applyEndpoint reads "<NAME>" ("POST /path"), "<NAME>_BODY" (JSON object)
// and "<NAME>_HEADERS" (JSON object). Setting "<NAME>=off" removes an
// optional endpoint.
func applyEndpoint(dst **Endpoint, name string, get func(string) string) error {
	line, body, headers := get(name), get(name+"_BODY"), get(name+"_HEADERS")
	if line == "" && body == "" && headers == "" {
		return nil
	}
	if strings.EqualFold(line, "off") {
		*dst = nil
		return nil
	}
	if *dst == nil {
		*dst = &Endpoint{}
	}
	e := *dst
	if line != "" {
		method, path, ok := strings.Cut(line, " ")
		if !ok {
			method, path = "", line
		}
		e.Method, e.Path = strings.ToUpper(strings.TrimSpace(method)), strings.TrimSpace(path)
		if !strings.HasPrefix(e.Path, "/") {
			return fmt.Errorf(`%s must look like "POST /auth/login"`, name)
		}
	}
	if body != "" {
		e.Body = nil
		if err := json.Unmarshal([]byte(body), &e.Body); err != nil {
			return fmt.Errorf("%s_BODY must be a JSON object: %w", name, err)
		}
	}
	if headers != "" {
		e.Headers = nil
		if err := json.Unmarshal([]byte(headers), &e.Headers); err != nil {
			return fmt.Errorf("%s_HEADERS must be a JSON object: %w", name, err)
		}
	}
	return nil
}

// lookupRaw returns a variable without trimming, and whether it is set at
// all (an empty getenv result counts as unset).
func lookupRaw(getenv func(string) string, k string) (string, bool) {
	v := getenv(k)
	return v, v != ""
}
