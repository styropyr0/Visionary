// Package authbridge signs people in through an existing API instead of a
// separate user store. A JSON config describes the API's endpoints and where
// the token, user and role are in its responses, so Visionary can sit in front
// of any backend that already has authentication.
//
// Two flows are supported:
//   - "otp": request a code for an identifier (phone, email), then verify it.
//   - "password": send an identifier and password in one call (Login).
package authbridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// Modes. otp and password delegate to an existing API; shared is one team
// password checked by Visionary itself; none skips sign-in (local use only).
const (
	TypeOTP      = "otp"
	TypePassword = "password"
	TypeShared   = "shared"
	TypeNone     = "none"

	// AnyRole in AllowedRoles or VISIONARY_ROLE_MAP matches every user of the
	// API, whatever their role.
	AnyRole = "*"

	LocationNone     = "none"
	LocationOptional = "optional"
	LocationRequired = "required"
)

type Config struct {
	// Provider is shown on the sign-in screen: "Sign in with your <Provider> account".
	Provider string `json:"provider"`
	Type     string `json:"type"`
	BaseURL  string `json:"base_url"`
	// Timeout per API call, as a Go duration ("15s").
	Timeout string `json:"timeout,omitempty"`

	Identifier Identifier `json:"identifier"`
	// CodeLength and ResendSeconds apply to the otp flow.
	CodeLength    int `json:"code_length,omitempty"`
	ResendSeconds int `json:"resend_seconds,omitempty"`
	// Location asks the browser for the user's position at sign-in, for APIs
	// that expect it: "none" (default), "optional" or "required". Body values
	// "{{latitude}}" and "{{longitude}}" are sent as numbers.
	Location string `json:"location,omitempty"`

	// RequestCode sends the one-time code (otp only).
	RequestCode *Endpoint `json:"request_code,omitempty"`
	// Login signs in with identifier and password in one call (password
	// only). It is stored as Verify once loaded.
	Login *Endpoint `json:"login,omitempty"`
	// Verify exchanges the code for a token (otp). Password configs from
	// before Login may still use it.
	Verify Endpoint `json:"verify"`
	// Profile returns the signed-in user. Without it, user fields are read
	// from the Verify response and the role can't be re-checked later.
	Profile *Endpoint `json:"profile,omitempty"`
	Logout  *Endpoint `json:"logout,omitempty"`

	Token TokenConfig `json:"token"`
	// Headers are sent on every call, e.g. an API key the auth API requires.
	Headers      map[string]string `json:"headers,omitempty"`
	Fields       Fields            `json:"fields"`
	AllowedRoles []string          `json:"allowed_roles"`

	// SharedPassword is the team password in shared mode. It is only read
	// from VISIONARY_SHARED_PASSWORD, never from a config file.
	SharedPassword string `json:"-"`
}

type Identifier struct {
	Label       string `json:"label"`
	Placeholder string `json:"placeholder,omitempty"`
	// Input is the HTML input type: "tel", "email" or "text".
	Input string `json:"input,omitempty"`
	// Normalize is applied before validation: "digits", "lowercase" or "trim".
	Normalize string `json:"normalize,omitempty"`
	Pattern   string `json:"pattern,omitempty"`
	// PatternHint is shown when the pattern does not match.
	PatternHint string `json:"pattern_hint,omitempty"`
}

// Endpoint is one call. Body values may contain {{identifier}}, {{code}} and
// {{password}}; a value that is exactly one placeholder is replaced as is.
type Endpoint struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Body    map[string]any    `json:"body,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// TokenConfig says how the API expects the token back on later calls.
type TokenConfig struct {
	Header string `json:"header"`
	Prefix string `json:"prefix"`
}

// Fields are dotted paths into JSON responses, e.g. "data.token" or
// "user.roles.0". Token, ExpiresAt, UserID and SentTo are read from the
// Verify / RequestCode responses; Name, Handle and Role from Profile (or
// Verify when there is no Profile endpoint). Role may point at a string or an
// array of strings.
type Fields struct {
	SentTo       string `json:"sent_to,omitempty"`
	Token        string `json:"token"`
	ExpiresAt    string `json:"expires_at,omitempty"`
	UserID       string `json:"user_id,omitempty"`
	Name         string `json:"name,omitempty"`
	Handle       string `json:"handle,omitempty"`
	Role         string `json:"role"`
	ErrorMessage string `json:"error_message,omitempty"`
}

// Source says where the auth config comes from: an optional JSON file (Path)
// or JSON text (Inline), then VISIONARY_AUTH_* variables from Getenv on top.
// Environment variables alone are enough.
type Source struct {
	Path   string
	Inline string
	Getenv func(string) string
}

// Load reads and validates the auth config. There is no built-in default:
// every deployment chooses its mode.
func Load(src Source) (Config, error) {
	var cfg Config
	var raw []byte
	name := "VISIONARY_AUTH_CONFIG_JSON"
	switch {
	case src.Path != "" && src.Inline != "":
		return cfg, errors.New("set VISIONARY_AUTH_CONFIG or VISIONARY_AUTH_CONFIG_JSON, not both")
	case src.Path != "":
		var err error
		if raw, err = os.ReadFile(src.Path); err != nil {
			return cfg, fmt.Errorf("read auth config: %w", err)
		}
		name = src.Path
	case src.Inline != "":
		raw = []byte(src.Inline)
	}
	if raw != nil {
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&cfg); err != nil {
			return cfg, fmt.Errorf("parse auth config %s: %w", name, err)
		}
	}
	if src.Getenv != nil {
		if err := ApplyEnv(&cfg, src.Getenv); err != nil {
			return cfg, err
		}
	}
	return cfg, cfg.validate()
}

// Delegated reports whether sign-in goes through an external API.
func (c *Config) Delegated() bool { return c.Type == TypeOTP || c.Type == TypePassword }

func (c *Config) validate() error {
	c.Type = strings.ToLower(strings.TrimSpace(c.Type))
	switch c.Type {
	case "":
		return errors.New("choose a sign-in mode: set VISIONARY_AUTH_MODE to otp, password, shared or none (see the README)")
	case TypeShared:
		if len(c.SharedPassword) < 12 {
			return errors.New("shared mode needs VISIONARY_SHARED_PASSWORD of at least 12 characters")
		}
		if c.Provider == "" {
			c.Provider = "Visionary"
		}
		c.Location = LocationNone
		return nil
	case TypeNone:
		c.Provider = "Visionary"
		c.Location = LocationNone
		return nil
	case TypeOTP, TypePassword:
	default:
		return fmt.Errorf("unknown sign-in mode %q: use otp, password, shared or none", c.Type)
	}

	c.BaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	u, err := url.Parse(c.BaseURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return errors.New("VISIONARY_AUTH_BASE_URL must be an http(s) URL")
	}
	if c.Type == TypePassword {
		// One call signs in; a code endpoint left over from otp is unused.
		if c.Login != nil {
			c.Verify, c.Login = *c.Login, nil
		}
		c.RequestCode = nil
		if c.Verify.Path == "" {
			return errors.New("password mode needs VISIONARY_AUTH_LOGIN, your API's login endpoint (e.g. POST /auth/login)")
		}
	}
	if c.Type == TypeOTP {
		if c.Login != nil {
			return errors.New("VISIONARY_AUTH_LOGIN is for password mode; otp mode uses VISIONARY_AUTH_START and VISIONARY_AUTH_VERIFY")
		}
		if c.RequestCode == nil {
			return errors.New("otp mode needs VISIONARY_AUTH_START (the endpoint that sends the code)")
		}
		if c.CodeLength == 0 {
			c.CodeLength = 6
		}
		if c.CodeLength < 4 || c.CodeLength > 10 {
			return errors.New("VISIONARY_AUTH_CODE_LENGTH must be between 4 and 10")
		}
		if c.ResendSeconds == 0 {
			c.ResendSeconds = 30
		}
	}
	if c.Verify.Path == "" {
		return errors.New("set VISIONARY_AUTH_VERIFY, the endpoint that signs a user in")
	}
	if c.Fields.Token == "" {
		return errors.New("set VISIONARY_AUTH_FIELD_TOKEN, where the token is in the sign-in response (e.g. data.token)")
	}
	// VISIONARY_ROLE_MAP also names who may sign in (every role it maps).
	roleMap := strings.TrimSpace(os.Getenv("VISIONARY_ROLE_MAP"))
	if len(c.AllowedRoles) == 0 && roleMap == "" {
		return errors.New(`set VISIONARY_ALLOWED_ROLES or VISIONARY_ROLE_MAP to say who may sign in ("*" lets every user of the API in)`)
	}
	// Only "*" works without reading a role from the API.
	if c.Fields.Role == "" && !onlyAnyRole(c.AllowedRoles, roleMap) {
		return errors.New(`set VISIONARY_AUTH_FIELD_ROLE, where the user's role is in the API's response (e.g. data.role), or allow every user with VISIONARY_ALLOWED_ROLES=*`)
	}
	if c.Identifier.Label == "" {
		c.Identifier.Label = "Username"
	}
	if c.Identifier.Input == "" {
		c.Identifier.Input = "text"
	}
	if c.Identifier.Pattern != "" {
		if _, err := regexp.Compile(c.Identifier.Pattern); err != nil {
			return fmt.Errorf("identifier.pattern: %w", err)
		}
	}
	switch c.Location {
	case "":
		c.Location = LocationNone
	case LocationNone, LocationOptional, LocationRequired:
	default:
		return fmt.Errorf("location must be %q, %q or %q", LocationNone, LocationOptional, LocationRequired)
	}
	if c.Token.Header == "" {
		c.Token = TokenConfig{Header: "Authorization", Prefix: "Bearer "}
	}
	if c.Provider == "" {
		c.Provider = u.Host
	}
	if c.Timeout != "" {
		if _, err := time.ParseDuration(c.Timeout); err != nil {
			return fmt.Errorf("timeout: %w", err)
		}
	}
	for _, e := range []*Endpoint{c.RequestCode, &c.Verify, c.Profile, c.Logout} {
		if e != nil && e.Method == "" {
			e.Method = "POST"
			if e == c.Profile {
				e.Method = "GET"
			}
		}
	}
	return nil
}

// onlyAnyRole reports whether every allowed role and role-map entry is "*".
func onlyAnyRole(allowed []string, roleMap string) bool {
	names := append([]string{}, allowed...)
	for _, part := range strings.Split(roleMap, ",") {
		if name, _, _ := strings.Cut(part, "="); strings.TrimSpace(name) != "" {
			names = append(names, strings.TrimSpace(name))
		}
	}
	for _, n := range names {
		if n != AnyRole {
			return false
		}
	}
	return len(names) > 0
}

// Public is what the sign-in screen needs; it holds nothing secret.
type Public struct {
	Provider      string     `json:"provider"`
	Type          string     `json:"type"`
	Identifier    Identifier `json:"identifier"`
	CodeLength    int        `json:"code_length,omitempty"`
	ResendSeconds int        `json:"resend_seconds,omitempty"`
	Location      string     `json:"location"`
	AllowedRoles  []string   `json:"allowed_roles"`
}

func (c *Config) Public() Public {
	return Public{
		Provider:      c.Provider,
		Type:          c.Type,
		Identifier:    c.Identifier,
		CodeLength:    c.CodeLength,
		ResendSeconds: c.ResendSeconds,
		Location:      c.Location,
		// Never null: the sign-in screen lists them.
		AllowedRoles: append([]string{}, c.AllowedRoles...),
	}
}
