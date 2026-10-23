package analytics

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"

	sdk "github.com/styropyr0/visionary/sdks/go"

	"github.com/styropyr0/visionary/internal/authbridge"
)

// Accounts: a username and password per person, kept by Visionary itself
// (Settings → Sign-in, way in "Accounts"). Each account has its own role.
// Passwords are bcrypt hashes; sessions re-check the account within a minute,
// so disabling, deleting or demoting someone, or resetting their password,
// ends or changes their session without waiting for it to expire.

const (
	accountsPrefix     = "acct:"
	accountRecheck     = time.Minute
	minPasswordLen     = 12
	maxPasswordBytes   = 72 // bcrypt reads no further
	accountBcryptCost  = 12
	maxAccountNameLen  = 120
	accountsListLimit  = 500
	accountUsernameMax = 64
)

var usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._@+-]{2,63}$`)

// dummyHash is compared against when a username doesn't exist, so a wrong
// username takes as long as a wrong password.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("visionary-no-such-account"), accountBcryptCost)

type account struct {
	ID         string `json:"id"`
	Username   string `json:"username"`
	Name       string `json:"name"`
	Role       string `json:"role"`
	Disabled   bool   `json:"disabled"`
	CreatedBy  string `json:"created_by"`
	CreatedAt  string `json:"created_at"`
	LastSignIn string `json:"last_sign_in_at,omitempty"`
	PasswordAt string `json:"password_changed_at"`
	hash       string
}

func (s *Server) accountsSchema() []string {
	if s.q.d == sdk.MySQL {
		return []string{`CREATE TABLE IF NOT EXISTS visionary_users (
    id              VARCHAR(32) NOT NULL PRIMARY KEY,
    username        VARCHAR(64) NOT NULL UNIQUE,
    name            VARCHAR(255) NOT NULL,
    role            VARCHAR(16) NOT NULL,
    password_hash   VARCHAR(100) NOT NULL,
    password_at     VARCHAR(32) NOT NULL,
    disabled        INT NOT NULL DEFAULT 0,
    created_by      VARCHAR(255) NOT NULL,
    created_at      VARCHAR(32) NOT NULL,
    last_sign_in_at VARCHAR(32)
) CHARACTER SET utf8mb4`}
	}
	return []string{`CREATE TABLE IF NOT EXISTS visionary_users (
    id              TEXT NOT NULL PRIMARY KEY,
    username        TEXT NOT NULL UNIQUE,
    name            TEXT NOT NULL,
    role            TEXT NOT NULL,
    password_hash   TEXT NOT NULL,
    password_at     TEXT NOT NULL,
    disabled        INTEGER NOT NULL DEFAULT 0,
    created_by      TEXT NOT NULL,
    created_at      TEXT NOT NULL,
    last_sign_in_at TEXT
)`}
}

const accountColumns = `id, username, name, role, disabled, created_by, created_at, last_sign_in_at, password_at, password_hash`

func accountFrom(r []any) account {
	return account{
		ID: toString(r[0]), Username: toString(r[1]), Name: toString(r[2]), Role: toString(r[3]),
		Disabled: toInt(r[4]) != 0, CreatedBy: toString(r[5]), CreatedAt: toString(r[6]),
		LastSignIn: toString(r[7]), PasswordAt: toString(r[8]), hash: toString(r[9]),
	}
}

func (s *Server) listAccounts(ctx context.Context) ([]account, error) {
	res, err := s.db.Query(ctx, `SELECT `+accountColumns+` FROM visionary_users ORDER BY username LIMIT `+strconv.Itoa(accountsListLimit))
	if err != nil {
		return nil, dbErr(err)
	}
	out := make([]account, 0, len(res.Rows))
	for _, r := range res.Rows {
		out = append(out, accountFrom(r))
	}
	return out, nil
}

// findAccount looks an account up by id or username; nil when there is none.
func (s *Server) findAccount(ctx context.Context, column, value string) (*account, error) {
	res, err := s.db.Query(ctx, `SELECT `+accountColumns+` FROM visionary_users WHERE `+column+` = ?`, value)
	if err != nil {
		return nil, dbErr(err)
	}
	if len(res.Rows) == 0 {
		return nil, nil
	}
	a := accountFrom(res.Rows[0])
	return &a, nil
}

// enabledAdmins counts the accounts that can still administer Visionary.
func (s *Server) enabledAdmins(ctx context.Context) (int, error) {
	res, err := s.db.Query(ctx, `SELECT COUNT(*) FROM visionary_users WHERE role = 'admin' AND disabled = 0`)
	if err != nil {
		if missingTable(err, "visionary_users") {
			return 0, nil
		}
		return 0, dbErr(err)
	}
	if len(res.Rows) == 0 {
		return 0, nil
	}
	return int(toInt(res.Rows[0][0])), nil
}

func checkPassword(pw string) error {
	switch {
	case utf8.RuneCountInString(pw) < minPasswordLen:
		return errors.New("passwords are at least 12 characters")
	case len(pw) > maxPasswordBytes:
		return errors.New("passwords are at most 72 bytes")
	}
	return nil
}

func normalizeUsername(u string) (string, error) {
	u = strings.ToLower(strings.TrimSpace(u))
	if !usernamePattern.MatchString(u) {
		return "", errors.New("usernames are 3 to 64 characters: letters, digits and . _ @ + -")
	}
	return u, nil
}

func hashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), accountBcryptCost)
	return string(b), err
}

func newAccountID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---------- Sign-in ----------

// completeAccount checks a username and password. Unknown users, wrong
// passwords and disabled accounts all get the same answer.
func (s *Server) completeAccount(w http.ResponseWriter, r *http.Request, username, password string) {
	fail := func() {
		s.auditAs(r, nil, "auth.sign_in_failed", maskIdentifier(username), map[string]any{"mode": authbridge.TypeAccounts})
		writeError(w, http.StatusUnauthorized, "That username or password isn't right.")
	}
	u, err := normalizeUsername(username)
	if err != nil || password == "" || len(password) > 1024 {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		fail()
		return
	}
	a, err := s.findAccount(r.Context(), "username", u)
	if err != nil {
		s.fail(w, err)
		return
	}
	hash := dummyHash
	if a != nil {
		hash = []byte(a.hash)
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(password)) != nil || a == nil || a.Disabled {
		fail()
		return
	}
	token := make([]byte, 24)
	if _, err := rand.Read(token); err != nil {
		s.fail(w, err)
		return
	}
	sess := s.auth.newSession(&authbridge.Result{
		Token: accountsPrefix + hex.EncodeToString(token),
		User:  authbridge.User{ID: accountsPrefix + a.ID, Name: a.Name, Handle: a.Username, Roles: []string{a.Role}},
	})
	sess.Role = a.Role
	if err := s.auth.write(w, r, sess); err != nil {
		s.fail(w, err)
		return
	}
	_ = s.db.Exec(r.Context(), `UPDATE visionary_users SET last_sign_in_at = ? WHERE id = ?`, nowTS(), a.ID)
	s.auditAs(r, sess, "auth.sign_in", sess.UserID, map[string]any{"mode": authbridge.TypeAccounts, "role": a.Role})
	writeJSON(w, http.StatusOK, s.sessionView(sess))
}

// confirmAccount re-reads a signed-in account once a minute: deleted,
// disabled or re-passworded accounts are signed out; a new role or name
// applies.
func (s *Server) confirmAccount(ctx context.Context, sess *session) (*session, bool, error) {
	if time.Since(sess.checkedAt()) < accountRecheck {
		return sess, false, nil
	}
	id, ok := strings.CutPrefix(sess.UserID, accountsPrefix)
	if !ok {
		return nil, false, errSessionEnded
	}
	a, err := s.findAccount(ctx, "id", id)
	if err != nil {
		// The database is the one thing every page needs anyway; keep the
		// session and look again next minute.
		return sess, false, nil
	}
	if a == nil || a.Disabled || parseRole(a.Role) == roleNone {
		return nil, false, errNotAllowed
	}
	if changed, err := time.Parse(tsLayout, a.PasswordAt); err == nil && changed.Unix() > sess.Issued {
		return nil, false, errSessionEnded
	}
	next := *sess
	next.Role, next.Name, next.Handle, next.Roles, next.Checked = a.Role, a.Name, a.Username, []string{a.Role}, time.Now().Unix()
	return &next, true, nil
}

// ---------- Settings → Sign-in → Accounts ----------

func (s *Server) accountRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/team/accounts", s.authed(s.handleListAccounts))
	mux.Handle("POST /api/team/accounts", s.authed(s.handleCreateAccount))
	mux.Handle("PUT /api/team/accounts/{id}", s.authed(s.handleUpdateAccount))
	mux.Handle("DELETE /api/team/accounts/{id}", s.authed(s.handleDeleteAccount))
	mux.Handle("PUT /api/me/password", s.authed(s.handleOwnPassword))
}

func (s *Server) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	list, err := s.listAccounts(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": list})
}

func (s *Server) handleCreateAccount(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	var body struct {
		Username string `json:"username"`
		Name     string `json:"name"`
		Role     string `json:"role"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	u, err := normalizeUsername(body.Username)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	role := parseRole(body.Role)
	if role == roleNone {
		writeError(w, http.StatusBadRequest, "the role is viewer, editor or admin")
		return
	}
	if err := checkPassword(body.Password); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	name := clip(strings.TrimSpace(body.Name), maxAccountNameLen)
	if existing, err := s.findAccount(r.Context(), "username", u); err != nil {
		s.fail(w, err)
		return
	} else if existing != nil {
		writeError(w, http.StatusConflict, "there is already an account named "+u)
		return
	}
	hash, err := hashPassword(body.Password)
	if err != nil {
		s.fail(w, err)
		return
	}
	now := nowTS()
	by := sessionFrom(r)
	a := account{ID: newAccountID(), Username: u, Name: name, Role: role.String(), CreatedBy: by.Name, CreatedAt: now, PasswordAt: now}
	if err := s.db.Exec(r.Context(), `INSERT INTO visionary_users (id, username, name, role, password_hash, password_at, disabled, created_by, created_at)
VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?)`, a.ID, a.Username, a.Name, a.Role, hash, now, a.CreatedBy, now); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "team.account_created", a.Username, map[string]any{"role": a.Role})
	writeJSON(w, http.StatusCreated, a)
}

// handleUpdateAccount changes the name, role, disabled flag or password.
// The last enabled admin can't be demoted or disabled while accounts are
// how people sign in.
func (s *Server) handleUpdateAccount(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	var body struct {
		Name     *string `json:"name"`
		Role     *string `json:"role"`
		Disabled *bool   `json:"disabled"`
		Password *string `json:"password"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a, err := s.findAccount(r.Context(), "id", r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	if a == nil {
		writeError(w, http.StatusNotFound, "no such account")
		return
	}
	next := *a
	if body.Name != nil {
		next.Name = clip(strings.TrimSpace(*body.Name), maxAccountNameLen)
	}
	if body.Role != nil {
		role := parseRole(*body.Role)
		if role == roleNone {
			writeError(w, http.StatusBadRequest, "the role is viewer, editor or admin")
			return
		}
		next.Role = role.String()
	}
	if body.Disabled != nil {
		next.Disabled = *body.Disabled
	}
	wasAdmin := a.Role == "admin" && !a.Disabled
	if wasAdmin && (next.Role != "admin" || next.Disabled) {
		if ok, err := s.canLoseAdmin(r.Context()); err != nil {
			s.fail(w, err)
			return
		} else if !ok {
			writeError(w, http.StatusConflict, "this is the last admin account; make another admin first")
			return
		}
	}
	hash, passwordAt := a.hash, a.PasswordAt
	if body.Password != nil {
		if err := checkPassword(*body.Password); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if hash, err = hashPassword(*body.Password); err != nil {
			s.fail(w, err)
			return
		}
		// One second on, so sessions issued this second end too.
		passwordAt = time.Now().UTC().Add(time.Second).Format(tsLayout)
	}
	disabled := 0
	if next.Disabled {
		disabled = 1
	}
	if err := s.db.Exec(r.Context(), `UPDATE visionary_users SET name = ?, role = ?, disabled = ?, password_hash = ?, password_at = ? WHERE id = ?`,
		next.Name, next.Role, disabled, hash, passwordAt, a.ID); err != nil {
		s.fail(w, err)
		return
	}
	next.PasswordAt = passwordAt
	s.audit(r, "team.account_updated", a.Username, map[string]any{
		"role": next.Role, "disabled": next.Disabled, "password_reset": body.Password != nil,
	})
	writeJSON(w, http.StatusOK, next)
}

func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	a, err := s.findAccount(r.Context(), "id", r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	if a == nil {
		writeError(w, http.StatusNotFound, "no such account")
		return
	}
	if a.Role == "admin" && !a.Disabled {
		if ok, err := s.canLoseAdmin(r.Context()); err != nil {
			s.fail(w, err)
			return
		} else if !ok {
			writeError(w, http.StatusConflict, "this is the last admin account; make another admin first")
			return
		}
	}
	if err := s.db.Exec(r.Context(), `DELETE FROM visionary_users WHERE id = ?`, a.ID); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "team.account_deleted", a.Username, nil)
	w.WriteHeader(http.StatusNoContent)
}

// canLoseAdmin reports whether one enabled admin account can go. Only the
// accounts way in depends on them; with another way in they can all go.
func (s *Server) canLoseAdmin(ctx context.Context) (bool, error) {
	if s.signin().cfg.Type != authbridge.TypeAccounts {
		return true, nil
	}
	n, err := s.enabledAdmins(ctx)
	return n > 1, err
}

// handleOwnPassword lets someone signed in with an account change its
// password. Their other sessions end; this one is renewed.
func (s *Server) handleOwnPassword(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	id, ok := strings.CutPrefix(sess.UserID, accountsPrefix)
	if !ok {
		writeError(w, http.StatusBadRequest, "only Visionary accounts have a password to change here")
		return
	}
	var body struct {
		Current  string `json:"current"`
		Password string `json:"password"`
	}
	if !s.loginPreflight(w, r, &body) {
		return
	}
	a, err := s.findAccount(r.Context(), "id", id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if a == nil || bcrypt.CompareHashAndPassword([]byte(a.hash), []byte(body.Current)) != nil {
		writeError(w, http.StatusBadRequest, "Your current password isn't right.")
		return
	}
	if err := checkPassword(body.Password); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	hash, err := hashPassword(body.Password)
	if err != nil {
		s.fail(w, err)
		return
	}
	changed := time.Now().UTC()
	if err := s.db.Exec(r.Context(), `UPDATE visionary_users SET password_hash = ?, password_at = ? WHERE id = ?`,
		hash, changed.Format(tsLayout), a.ID); err != nil {
		s.fail(w, err)
		return
	}
	next := *sess
	next.Issued, next.Checked = changed.Unix()+1, changed.Unix()
	if err := s.auth.write(w, r, &next); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "auth.password_changed", a.Username, nil)
	w.WriteHeader(http.StatusNoContent)
}
