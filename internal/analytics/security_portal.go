package analytics

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
	"rsc.io/qr"

	"github.com/styropyr0/visionary/internal/agentapi"
)

// Protecting Visionary itself:
//
//   - portal_allow_ips: the dashboard and its API answer only these
//     addresses. Ingest (SDKs, OpenTelemetry, the host agent, browser
//     events), the health check and the public status page stay open, and
//     the server's own machine is always allowed. Refusals are audited (at
//     most once per address every 10 minutes), which detectPortal turns into
//     portal_ip_blocked findings.
//   - Two-step sign-in for Visionary accounts: a TOTP authenticator app
//     (RFC 6238, 30 s, 6 digits) plus ten one-time recovery codes. With
//     portal_require_2fa on, an account without it can only set it up.

// portalOpenPaths are never behind the allow list.
func portalOpen(path string) bool {
	switch {
	case path == agentapi.Path, path == "/api/healthz", strings.HasPrefix(path, "/v1/"), strings.HasPrefix(path, "/api/ingest/"), path == "/loki/api/v1/push",
		path == "/status", path == "/status.json", path == "/status.js":
		return true
	}
	return false
}

type portalState struct {
	mu        sync.Mutex
	conf      string
	allow     []netip.Prefix
	blockedAt map[string]time.Time // last audit of a refused address
	lastStep  map[string]int64     // last TOTP step used per account, against replay
}

var portalStates sync.Map // *Server -> *portalState

func (s *Server) portal() *portalState {
	v, _ := portalStates.LoadOrStore(s, &portalState{blockedAt: map[string]time.Time{}, lastStep: map[string]int64{}})
	return v.(*portalState)
}

// allowList is the parsed setting; nil allows everyone.
func (s *Server) allowList() []netip.Prefix {
	conf := s.conf("portal_allow_ips")
	p := s.portal()
	p.mu.Lock()
	defer p.mu.Unlock()
	if conf != p.conf {
		p.conf, p.allow = conf, nil
		for _, v := range strings.Split(conf, ",") {
			if pr, err := parseIPOrPrefix(v); err == nil && strings.TrimSpace(v) != "" {
				p.allow = append(p.allow, pr)
			}
		}
	}
	return p.allow
}

// portalGuard refuses dashboard requests from addresses not on the allow
// list.
func (s *Server) portalGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allow := s.allowList()
		if len(allow) == 0 || portalOpen(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		ip := s.auth.clientIP(r)
		a, err := netip.ParseAddr(ip)
		if err == nil {
			a = a.Unmap()
			if a.IsLoopback() {
				next.ServeHTTP(w, r)
				return
			}
			for _, p := range allow {
				if p.Contains(a) {
					next.ServeHTTP(w, r)
					return
				}
			}
		}
		p := s.portal()
		p.mu.Lock()
		audit := time.Since(p.blockedAt[ip]) > 10*time.Minute
		if audit {
			p.blockedAt[ip] = time.Now()
			if len(p.blockedAt) > 10000 {
				clear(p.blockedAt)
			}
		}
		p.mu.Unlock()
		if audit {
			s.auditAs(r, nil, "auth.ip_blocked", ip, map[string]any{"path": clip(r.URL.Path, 200)})
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeError(w, http.StatusForbidden, "Visionary doesn't accept requests from this address.")
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("Visionary doesn't accept requests from this address (" + ip + ").\n"))
	})
}

// ---------- TOTP ----------

const (
	totpStep   = 30
	totpDigits = 6
	totpIssuer = "Visionary"
)

func totpAt(secret []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	m := hmac.New(sha1.New, secret)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, v%1000000)
}

// totpCheck returns the step the code matches (one step either side is
// allowed for clock drift), or -1.
func totpCheck(secret []byte, code string, now time.Time) int64 {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != totpDigits {
		return -1
	}
	step := now.Unix() / totpStep
	for d := int64(-1); d <= 1; d++ {
		if hmac.Equal([]byte(totpAt(secret, step+d)), []byte(code)) {
			return step + d
		}
	}
	return -1
}

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// sealSecret encrypts a TOTP secret with the session key, so the database
// alone can't produce codes.
func (s *Server) sealSecret(secret []byte) (string, error) {
	aead := *s.auth.aead.Load()
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(aead.Seal(nonce, nonce, secret, []byte("visionary/totp"))), nil
}

func (s *Server) openSecret(sealed string) ([]byte, error) {
	raw, err := base64.RawURLEncoding.DecodeString(sealed)
	aead := *s.auth.aead.Load()
	if err != nil || len(raw) < aead.NonceSize() {
		return nil, errors.New("unreadable two-step secret")
	}
	return aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], []byte("visionary/totp"))
}

func hashRecovery(code string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))))
	return hex.EncodeToString(sum[:])
}

// checkSecondFactor verifies an authenticator code or uses up a recovery
// code. It reports whether the code was good.
func (s *Server) checkSecondFactor(ctx context.Context, a *account, code string) (bool, error) {
	secret, err := s.openSecret(a.totpSecret)
	if err != nil {
		return false, err
	}
	if step := totpCheck(secret, code, time.Now()); step >= 0 {
		p := s.portal()
		p.mu.Lock()
		defer p.mu.Unlock()
		if step <= p.lastStep[a.ID] {
			return false, nil // already used
		}
		p.lastStep[a.ID] = step
		return true, nil
	}
	var hashes []string
	_ = json.Unmarshal([]byte(a.totpRecovery), &hashes)
	h := hashRecovery(code)
	for i, v := range hashes {
		if hmac.Equal([]byte(v), []byte(h)) {
			rest, _ := json.Marshal(append(hashes[:i:i], hashes[i+1:]...))
			return true, s.db.Exec(ctx, `UPDATE visionary_users SET totp_recovery = ? WHERE id = ?`, string(rest), a.ID)
		}
	}
	return false, nil
}

func (s *Server) twoStepRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/me/two-step", s.authed(s.handleTwoStep))
	mux.Handle("POST /api/me/two-step/setup", s.authed(s.handleTwoStepSetup))
	mux.Handle("POST /api/me/two-step/enable", s.authed(s.handleTwoStepEnable))
	mux.Handle("POST /api/me/two-step/disable", s.authed(s.handleTwoStepDisable))
	mux.Handle("POST /api/team/accounts/{id}/two-step/reset", s.authed(s.handleTwoStepReset))
}

// twoStepAllowed are the routes an account that must set up two-step
// sign-in can still reach.
func twoStepAllowed(path string) bool {
	return strings.HasPrefix(path, "/api/me/two-step") || path == "/api/session" || path == "/api/logout" || path == "/api/meta"
}

// ownAccount is the signed-in Visionary account, or an answer saying why
// there is none.
func (s *Server) ownAccount(w http.ResponseWriter, r *http.Request) *account {
	sess := sessionFrom(r)
	id, ok := strings.CutPrefix(sess.UserID, accountsPrefix)
	if !ok {
		writeError(w, http.StatusBadRequest, "Two-step sign-in is for Visionary accounts; your sign-in goes through your own API.")
		return nil
	}
	a, err := s.findAccount(r.Context(), "id", id)
	if err != nil {
		s.fail(w, err)
		return nil
	}
	if a == nil {
		writeError(w, http.StatusNotFound, "account not found")
	}
	return a
}

func (s *Server) handleTwoStep(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	_, local := strings.CutPrefix(sess.UserID, accountsPrefix)
	out := map[string]any{"available": local, "required": s.confBool("portal_require_2fa"), "enabled": false}
	if local {
		if a := s.ownAccount(w, r); a != nil {
			var codes []string
			_ = json.Unmarshal([]byte(a.totpRecovery), &codes)
			out["enabled"], out["recovery_left"] = a.TwoStep, len(codes)
		} else {
			return
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleTwoStepSetup makes a new secret (not yet in force) and returns it as
// text and a QR code for the authenticator app.
func (s *Server) handleTwoStepSetup(w http.ResponseWriter, r *http.Request) {
	a := s.ownAccount(w, r)
	if a == nil {
		return
	}
	if a.TwoStep {
		writeError(w, http.StatusConflict, "Two-step sign-in is already on; turn it off first to start over.")
		return
	}
	secret := make([]byte, 20)
	if _, err := rand.Read(secret); err != nil {
		s.fail(w, err)
		return
	}
	sealed, err := s.sealSecret(secret)
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.db.Exec(r.Context(), `UPDATE visionary_users SET totp_secret = ?, totp_enabled = 0 WHERE id = ?`, sealed, a.ID); err != nil {
		s.fail(w, err)
		return
	}
	text := b32.EncodeToString(secret)
	uri := "otpauth://totp/" + url.PathEscape(totpIssuer+":"+a.Username) + "?" + url.Values{
		"secret": {text}, "issuer": {totpIssuer}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"},
	}.Encode()
	out := map[string]any{"secret": text, "uri": uri}
	if code, err := qr.Encode(uri, qr.M); err == nil {
		code.Scale = 6
		out["qr"] = "data:image/png;base64," + base64.StdEncoding.EncodeToString(code.PNG())
	}
	writeJSON(w, http.StatusOK, out)
}

// handleTwoStepEnable turns it on once a code from the app matches, and
// returns the recovery codes (shown this once).
func (s *Server) handleTwoStepEnable(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if !s.loginPreflight(w, r, &body) {
		return
	}
	a := s.ownAccount(w, r)
	if a == nil {
		return
	}
	if a.totpSecret == "" {
		writeError(w, http.StatusBadRequest, "Start the setup first.")
		return
	}
	secret, err := s.openSecret(a.totpSecret)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Start the setup again.")
		return
	}
	if totpCheck(secret, body.Code, time.Now()) < 0 {
		writeError(w, http.StatusBadRequest, "That code isn't right. Check the time on your phone and try the newest code.")
		return
	}
	codes := make([]string, 10)
	hashes := make([]string, 10)
	for i := range codes {
		b := make([]byte, 5)
		if _, err := rand.Read(b); err != nil {
			s.fail(w, err)
			return
		}
		c := strings.ToLower(b32.EncodeToString(b))
		codes[i] = c[:4] + "-" + c[4:]
		hashes[i] = hashRecovery(codes[i])
	}
	stored, _ := json.Marshal(hashes)
	if err := s.db.Exec(r.Context(), `UPDATE visionary_users SET totp_enabled = 1, totp_recovery = ? WHERE id = ?`, string(stored), a.ID); err != nil {
		s.fail(w, err)
		return
	}
	next := *sessionFrom(r)
	next.NoTwoStep = false
	if err := s.auth.write(w, r, &next); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "auth.two_step_enabled", a.Username, nil)
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "recovery_codes": codes})
}

// handleTwoStepDisable turns it off after the account's password.
func (s *Server) handleTwoStepDisable(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if !s.loginPreflight(w, r, &body) {
		return
	}
	a := s.ownAccount(w, r)
	if a == nil {
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(a.hash), []byte(body.Password)) != nil {
		writeError(w, http.StatusBadRequest, "Your password isn't right.")
		return
	}
	if err := s.db.Exec(r.Context(), `UPDATE visionary_users SET totp_enabled = 0, totp_secret = NULL, totp_recovery = NULL WHERE id = ?`, a.ID); err != nil {
		s.fail(w, err)
		return
	}
	next := *sessionFrom(r)
	next.NoTwoStep = true
	if err := s.auth.write(w, r, &next); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "auth.two_step_disabled", a.Username, nil)
	writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
}

// handleTwoStepReset lets an admin turn it off for someone who lost their
// phone and recovery codes.
func (s *Server) handleTwoStepReset(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	a, err := s.findAccount(r.Context(), "id", r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	if a == nil {
		writeError(w, http.StatusNotFound, "account not found")
		return
	}
	if err := s.db.Exec(r.Context(), `UPDATE visionary_users SET totp_enabled = 0, totp_secret = NULL, totp_recovery = NULL WHERE id = ?`, a.ID); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "auth.two_step_reset", a.Username, nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
