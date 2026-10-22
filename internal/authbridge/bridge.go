package authbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Bridge struct {
	cfg     Config
	http    *http.Client
	pattern *regexp.Regexp
	ua      string
}

func New(cfg Config, userAgent string) *Bridge {
	timeout := 15 * time.Second
	if d, err := time.ParseDuration(cfg.Timeout); err == nil && d > 0 {
		timeout = d
	}
	b := &Bridge{cfg: cfg, http: &http.Client{Timeout: timeout}, ua: userAgent}
	if cfg.Identifier.Pattern != "" {
		b.pattern = regexp.MustCompile(cfg.Identifier.Pattern)
	}
	return b
}

func (b *Bridge) Config() *Config { return &b.cfg }

// User is who signed in, as the API describes them.
type User struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Handle string   `json:"handle"`
	Roles  []string `json:"roles"`
}

// Allowed reports whether any of the user's roles is an allowed role, or
// every role is allowed ("*"). Comparison ignores case.
func (b *Bridge) Allowed(u *User) bool {
	if slices.Contains(b.cfg.AllowedRoles, AnyRole) {
		return true
	}
	for _, r := range u.Roles {
		if slices.ContainsFunc(b.cfg.AllowedRoles, func(a string) bool { return strings.EqualFold(a, r) }) {
			return true
		}
	}
	return false
}

type Result struct {
	Token     string
	ExpiresAt time.Time
	User      User
}

// APIError is the API rejecting a call (Status set) or failing to answer
// (Status 0 or 5xx).
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	if e.Status == 0 {
		return "auth api unreachable: " + e.Message
	}
	return fmt.Sprintf("auth api %d: %s", e.Status, e.Message)
}

// Unavailable is an API that could not answer: unreachable, timing out, rate
// limiting or failing. It says nothing about the token.
func (e *APIError) Unavailable() bool {
	return e.Status == 0 || e.Status == http.StatusRequestTimeout || e.Status == http.StatusTooManyRequests || e.Status >= 500
}

func IsUnavailable(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Unavailable()
}

// ErrInvalidIdentifier means the identifier failed the configured pattern.
var ErrInvalidIdentifier = errors.New("invalid identifier")

// Normalize applies the identifier rules and checks the pattern.
func (b *Bridge) Normalize(id string) (string, error) {
	id = strings.TrimSpace(id)
	switch b.cfg.Identifier.Normalize {
	case "digits":
		id = strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, id)
	case "lowercase":
		id = strings.ToLower(id)
	}
	if id == "" || len(id) > 254 || (b.pattern != nil && !b.pattern.MatchString(id)) {
		return "", ErrInvalidIdentifier
	}
	return id, nil
}

// Start sends a one-time code (otp flow) and returns where it went.
func (b *Bridge) Start(ctx context.Context, identifier string) (string, error) {
	if b.cfg.Type != TypeOTP {
		return "", errors.New("this sign-in flow has no code step")
	}
	resp, err := b.call(ctx, b.cfg.RequestCode, vars{identifier: identifier}, "", "")
	if err != nil {
		return "", err
	}
	return str(lookup(resp, b.cfg.Fields.SentTo)), nil
}

// Position is the browser's location, sent when the config asks for it.
type Position struct {
	Latitude  float64
	Longitude float64
}

// Complete exchanges the code or password for a token and loads the user.
// pos may be nil when location is not required.
func (b *Bridge) Complete(ctx context.Context, identifier, secret, clientUA string, pos *Position) (*Result, error) {
	v := vars{identifier: identifier, pos: pos}
	if b.cfg.Type == TypeOTP {
		v.code = secret
	} else {
		v.password = secret
	}
	resp, err := b.call(ctx, &b.cfg.Verify, v, "", clientUA)
	if err != nil {
		return nil, err
	}
	token := str(lookup(resp, b.cfg.Fields.Token))
	if token == "" {
		return nil, &APIError{Message: "sign-in response had no token at " + b.cfg.Fields.Token}
	}
	res := &Result{Token: token}
	if exp := str(lookup(resp, b.cfg.Fields.ExpiresAt)); exp != "" {
		res.ExpiresAt, _ = time.Parse(time.RFC3339Nano, exp)
	}
	userSrc := resp
	if b.cfg.Profile != nil {
		if userSrc, err = b.call(ctx, b.cfg.Profile, vars{}, token, clientUA); err != nil {
			return res, err
		}
	}
	res.User = b.user(userSrc)
	if res.User.ID == "" {
		res.User.ID = str(lookup(resp, b.cfg.Fields.UserID))
	}
	return res, nil
}

// ErrNoProfile means the role cannot be re-checked: no profile endpoint.
var ErrNoProfile = errors.New("no profile endpoint configured")

// Recheck loads the token's user again, so revoked tokens and removed roles
// take effect during a session.
func (b *Bridge) Recheck(ctx context.Context, token string) (*User, error) {
	if b.cfg.Profile == nil {
		return nil, ErrNoProfile
	}
	resp, err := b.call(ctx, b.cfg.Profile, vars{}, token, "")
	if err != nil {
		return nil, err
	}
	u := b.user(resp)
	return &u, nil
}

// Logout revokes token on the API, when a logout endpoint is configured.
func (b *Bridge) Logout(ctx context.Context, token string) error {
	if b.cfg.Logout == nil {
		return nil
	}
	_, err := b.call(ctx, b.cfg.Logout, vars{}, token, "")
	return err
}

// CanRecheck reports whether sessions can be re-validated against the API.
func (b *Bridge) CanRecheck() bool { return b.cfg.Profile != nil }

func (b *Bridge) user(resp any) User {
	u := User{
		Name:   str(lookup(resp, b.cfg.Fields.Name)),
		Handle: str(lookup(resp, b.cfg.Fields.Handle)),
		ID:     str(lookup(resp, b.cfg.Fields.UserID)),
	}
	switch r := lookup(resp, b.cfg.Fields.Role).(type) {
	case string:
		u.Roles = []string{r}
	case []any:
		for _, x := range r {
			if s := str(x); s != "" {
				u.Roles = append(u.Roles, s)
			}
		}
	}
	return u
}

type vars struct {
	identifier, code, password string
	pos                        *Position
}

func (v vars) apply(s string) string {
	lat, lng := "", ""
	if v.pos != nil {
		lat = strconv.FormatFloat(v.pos.Latitude, 'f', -1, 64)
		lng = strconv.FormatFloat(v.pos.Longitude, 'f', -1, 64)
	}
	return strings.NewReplacer(
		"{{identifier}}", v.identifier, "{{code}}", v.code, "{{password}}", v.password,
		"{{latitude}}", lat, "{{longitude}}", lng,
	).Replace(s)
}

// render substitutes placeholders through the body, keeping JSON types. A
// value that is exactly "{{latitude}}" or "{{longitude}}" becomes a number,
// or null when no position was given.
func (v vars) render(x any) any {
	switch t := x.(type) {
	case string:
		switch t {
		case "{{latitude}}", "{{longitude}}":
			if v.pos == nil {
				return nil
			}
			if t == "{{latitude}}" {
				return v.pos.Latitude
			}
			return v.pos.Longitude
		}
		return v.apply(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = v.render(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = v.render(val)
		}
		return out
	default:
		return x
	}
}

func (b *Bridge) call(ctx context.Context, e *Endpoint, v vars, token, clientUA string) (any, error) {
	var body io.Reader
	if len(e.Body) > 0 {
		payload, err := json.Marshal(v.render(map[string]any(e.Body)))
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, e.Method, b.cfg.BaseURL+e.Path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	ua := b.ua
	if clientUA != "" {
		ua += " (" + clientUA + ")"
	}
	req.Header.Set("User-Agent", ua)
	for k, val := range b.cfg.Headers {
		req.Header.Set(k, v.apply(val))
	}
	for k, val := range e.Headers {
		req.Header.Set(k, v.apply(val))
	}
	if token != "" {
		req.Header.Set(b.cfg.Token.Header, b.cfg.Token.Prefix+token)
	}

	resp, err := b.http.Do(req)
	if err != nil {
		return nil, &APIError{Message: err.Error()}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, &APIError{Message: "read response: " + err.Error()}
	}
	var parsed any
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &parsed); err != nil {
			status := 0
			if resp.StatusCode >= 400 && resp.StatusCode < 500 {
				status = resp.StatusCode
			}
			return nil, &APIError{Status: status, Message: fmt.Sprintf("unreadable response (HTTP %d)", resp.StatusCode)}
		}
	}
	if resp.StatusCode >= 300 || isFailureEnvelope(parsed) {
		msg := str(lookup(parsed, b.cfg.Fields.ErrorMessage))
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		status := resp.StatusCode
		if status < 300 {
			status = http.StatusBadRequest
		}
		return nil, &APIError{Status: status, Message: msg}
	}
	return parsed, nil
}

// isFailureEnvelope catches APIs that answer 200 with {"success": false}.
func isFailureEnvelope(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	s, ok := m["success"].(bool)
	return ok && !s
}

// lookup follows a dotted path; numeric segments index arrays.
func lookup(v any, path string) any {
	if path == "" {
		return nil
	}
	for _, seg := range strings.Split(path, ".") {
		switch t := v.(type) {
		case map[string]any:
			v = t[seg]
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(t) {
				return nil
			}
			v = t[i]
		default:
			return nil
		}
	}
	return v
}

func str(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		return fmt.Sprint(t)
	}
}
