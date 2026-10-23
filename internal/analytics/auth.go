package analytics

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
	"golang.org/x/time/rate"

	"github.com/styropyr0/visionary/internal/authbridge"
)

const sessionCookie = "visionary_session"

// recheckTimeout bounds a role check, so an API that hangs delays one request
// at most this long.
const recheckTimeout = 5 * time.Second

// session is what the cookie carries. It is encrypted, so the API token never
// reaches page scripts or logs.
type session struct {
	UserID string   `json:"u"`
	Name   string   `json:"n"`
	Handle string   `json:"h"`
	Roles  []string `json:"r"`
	// Role is set for sessions with a fixed role (shared password, local, API
	// key); delegated sessions are mapped from Roles on every request.
	Role    string `json:"o,omitempty"`
	Token   string `json:"t"`
	Expires int64  `json:"e"`
	// Issued is when the session started; an account's password changed
	// after it ends the session.
	Issued int64 `json:"i,omitempty"`
	// Checked is when the API last confirmed the token and role.
	Checked int64 `json:"c"`
}

func (s *session) expiresAt() time.Time { return time.Unix(s.Expires, 0) }
func (s *session) checkedAt() time.Time { return time.Unix(s.Checked, 0) }

type auth struct {
	// aead seals cookies. It starts with a random key and takes the stored
	// session secret once the settings are read (loadSessionSecret).
	aead atomic.Pointer[cipher.AEAD]
	// srv supplies Settings → Server → Sessions, read at each use.
	srv *Server

	checks singleflight.Group

	mu       sync.Mutex
	limiters map[string]*loginLimiter
	// signedOut holds sessions ended by logout until they would have expired,
	// so a copied cookie stops working at once on this instance. Other
	// instances reject it at their next role check, when the API refuses the
	// revoked token.
	signedOut map[string]time.Time
	// retryAt holds, per token, when to ask an unreachable API again; until
	// then the session is served from its cookie without waiting on it.
	retryAt map[string]time.Time
}

type loginLimiter struct {
	lim  *rate.Limiter
	seen time.Time
}

// newAuth starts with a random cookie key; setSecret replaces it with the
// stored one before the server takes requests.
func newAuth(srv *Server) (*auth, error) {
	a := &auth{
		srv:       srv,
		limiters:  make(map[string]*loginLimiter),
		signedOut: make(map[string]time.Time),
		retryAt:   make(map[string]time.Time),
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	return a, a.setSecret(secret)
}

// setSecret derives the cookie key from the session secret.
func (a *auth) setSecret(secret []byte) error {
	if len(secret) < 32 {
		return errors.New("the session secret must be at least 32 characters")
	}
	key := sha256.Sum256(append([]byte("visionary/session/v1:"), secret...))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	a.aead.Store(&aead)
	return nil
}

func (a *auth) ttl() time.Duration        { return a.srv.confDuration("session_ttl") }
func (a *auth) checkEvery() time.Duration { return a.srv.confDuration("role_check_interval") }
func (a *auth) trustProxy() bool          { return a.srv.confBool("trust_proxy") }

func (a *auth) seal(s *session) (string, error) {
	plain, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	aead := *a.aead.Load()
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := aead.Seal(nonce, nonce, plain, []byte(sessionCookie))
	return base64.RawURLEncoding.EncodeToString(out), nil
}

func (a *auth) open(value string) (*session, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	aead := *a.aead.Load()
	ns := aead.NonceSize()
	if err != nil || len(raw) < ns {
		return nil, false
	}
	plain, err := aead.Open(nil, raw[:ns], raw[ns:], []byte(sessionCookie))
	if err != nil {
		return nil, false
	}
	var s session
	if json.Unmarshal(plain, &s) != nil || s.Token == "" {
		return nil, false
	}
	return &s, true
}

// read returns the request's unexpired, not signed-out session.
func (a *auth) read(r *http.Request) (*session, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil, false
	}
	s, ok := a.open(c.Value)
	if !ok || time.Now().After(s.expiresAt()) || a.isSignedOut(s) {
		return nil, false
	}
	return s, true
}

func (a *auth) write(w http.ResponseWriter, r *http.Request, s *session) error {
	value, err := a.seal(s)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    value,
		Path:     "/",
		Expires:  s.expiresAt(),
		HttpOnly: true,
		Secure:   a.secure(r),
		SameSite: http.SameSiteStrictMode,
	})
	return nil
}

func (a *auth) clear(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   a.secure(r),
		SameSite: http.SameSiteStrictMode,
	})
}

// newSession starts a session for an allowed user. It ends with the API token
// or the session TTL, whichever comes first.
func (a *auth) newSession(res *authbridge.Result) *session {
	now := time.Now()
	exp := now.Add(a.ttl())
	if !res.ExpiresAt.IsZero() && res.ExpiresAt.Before(exp) {
		exp = res.ExpiresAt
	}
	return &session{
		UserID:  res.User.ID,
		Name:    res.User.Name,
		Handle:  res.User.Handle,
		Roles:   res.User.Roles,
		Token:   res.Token,
		Expires: exp.Unix(),
		Issued:  now.Unix(),
		Checked: now.Unix(),
	}
}

// checkShared compares the team password, then the viewer password when one
// is set, in constant time. It returns the role the password grants.
func (a *auth) checkShared(sg *signin, pw string) role {
	got := sha256.Sum256([]byte(pw))
	want := sha256.Sum256([]byte(sg.cfg.SharedPassword))
	admin := subtle.ConstantTimeCompare(want[:], got[:]) == 1
	viewer := false
	if sg.viewerPassword != "" {
		want = sha256.Sum256([]byte(sg.viewerPassword))
		viewer = subtle.ConstantTimeCompare(want[:], got[:]) == 1
	}
	switch {
	case admin:
		return roleAdmin
	case viewer:
		return roleViewer
	}
	return roleNone
}

func tokenKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// markSignedOut rejects s on this instance from now until it expires.
func (a *auth) markSignedOut(s *session) {
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	for k, exp := range a.signedOut {
		if now.After(exp) {
			delete(a.signedOut, k)
		}
	}
	a.signedOut[tokenKey(s.Token)] = s.expiresAt()
}

// waitingForAPI reports a token whose last check found the API unreachable,
// until its retry time comes.
func (a *auth) waitingForAPI(key string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	at, ok := a.retryAt[key]
	if ok && time.Now().After(at) {
		delete(a.retryAt, key)
		return false
	}
	return ok
}

func (a *auth) retryLater(key string, expires time.Time) {
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	for k, at := range a.retryAt {
		if now.After(at) {
			delete(a.retryAt, k)
		}
	}
	at := now.Add(a.checkEvery())
	if !expires.IsZero() && expires.Before(at) {
		at = expires
	}
	a.retryAt[key] = at
}

func (a *auth) isSignedOut(s *session) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, out := a.signedOut[tokenKey(s.Token)]
	return out
}

var (
	errNotAllowed   = errors.New("this account doesn't have access to Visionary")
	errSessionEnded = errors.New("your session has ended, sign in again")
)

// confirm re-checks the token and role with the API once every checkEvery.
// It returns the session to keep and whether it changed (so the cookie must
// be rewritten). Concurrent requests for one token share one API call. When
// the API has no profile endpoint, sessions simply run to expiry.
//
// The cookie itself is the proof of sign-in: it is sealed with the session
// secret and carries its expiry. The API is asked only whether the account
// still has access, so while it is down (unreachable, 5xx, rate limiting)
// signed-in users keep working until their session expires, and the check is
// retried every checkEvery instead of on every request. Only an answer from
// the API (token revoked, role removed) ends a session early.
func (a *auth) confirm(ctx context.Context, sg *signin, s *session) (*session, bool, error) {
	if sg.bridge == nil || !sg.bridge.CanRecheck() || time.Since(s.checkedAt()) < a.checkEvery() {
		return s, false, nil
	}
	key := tokenKey(s.Token)
	if a.waitingForAPI(key) {
		return s, false, nil
	}
	v, err, _ := a.checks.Do(key, func() (any, error) {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recheckTimeout)
		defer cancel()
		return sg.bridge.Recheck(cctx, s.Token)
	})
	if err != nil {
		if authbridge.IsUnavailable(err) {
			a.retryLater(key, s.expiresAt())
			return s, false, nil
		}
		return nil, false, errSessionEnded
	}
	u := v.(*authbridge.User)
	if !sg.roles.allowed(u) {
		return nil, false, errNotAllowed
	}
	next := *s
	next.Name, next.Handle, next.Roles, next.Checked = u.Name, u.Handle, u.Roles, time.Now().Unix()
	return &next, true, nil
}

// secure decides the cookie's Secure flag. "auto" follows the request scheme,
// including X-Forwarded-Proto when the proxy is trusted.
func (a *auth) secure(r *http.Request) bool {
	switch a.srv.conf("secure_cookie") {
	case "true":
		return true
	case "false":
		return false
	}
	if r.TLS != nil {
		return true
	}
	return a.trustProxy() && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// allowLogin rate-limits sign-in calls per client: 5 at once, then one every
// 12 seconds. The auth API applies its own limits on top.
func (a *auth) allowLogin(r *http.Request) bool {
	ip := a.clientIP(r)
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	for k, l := range a.limiters {
		if now.Sub(l.seen) > 30*time.Minute {
			delete(a.limiters, k)
		}
	}
	l, ok := a.limiters[ip]
	if !ok {
		l = &loginLimiter{lim: rate.NewLimiter(rate.Every(12*time.Second), 5)}
		a.limiters[ip] = l
	}
	l.seen = now
	return l.lim.Allow()
}

func (a *auth) clientIP(r *http.Request) string {
	if a.trustProxy() {
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			first, _, _ := strings.Cut(fwd, ",")
			return strings.TrimSpace(first)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
