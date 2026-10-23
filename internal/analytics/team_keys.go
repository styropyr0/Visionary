package analytics

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// API keys. A key is "vis_" plus 32 random bytes; only its SHA-256 is stored,
// with the first characters kept visible so people can tell keys apart.
// Scopes: "read" uses the HTTP API with viewer rights, "server" sends from
// SDKs and OTLP, "client" sends from browsers and mobile apps (public).

const (
	keyPrefix     = "vis_"
	keyVisibleLen = 12
	maxKeyName    = 80
	// keyCacheTTL bounds how long a revoked key keeps working on other
	// instances; revoking on this one takes effect at once.
	keyCacheTTL = 30 * time.Second
)

var keyScopes = []string{"read", "server", "client"}

type cachedKey struct {
	id      int64
	name    string
	scope   string
	expires time.Time // zero: never
}

// keyStore caches the active keys by hash, so ingest endpoints and API
// clients don't query the database on every request, and batches last-used
// times into one write a minute.
type keyStore struct {
	mu     sync.Mutex
	byHash map[string]cachedKey
	loaded time.Time
	used   map[int64]time.Time
	loads  singleflight.Group
}

func hashKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func newKeySecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return keyPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// lookupKey finds an active, unexpired key of scope.
func (s *Server) lookupKey(ctx context.Context, raw, scope string) (cachedKey, bool) {
	if !strings.HasPrefix(raw, keyPrefix) || len(raw) > 128 {
		return cachedKey{}, false
	}
	ks := &s.team().keys
	ks.mu.Lock()
	fresh := ks.byHash != nil && time.Since(ks.loaded) < keyCacheTTL
	ks.mu.Unlock()
	if !fresh {
		s.reloadKeys(ctx)
	}
	h := hashKey(raw)
	ks.mu.Lock()
	defer ks.mu.Unlock()
	k, ok := ks.byHash[h]
	if !ok || k.scope != scope || (!k.expires.IsZero() && time.Now().After(k.expires)) {
		return cachedKey{}, false
	}
	ks.used[k.id] = time.Now().UTC()
	return k, true
}

// reloadKeys refreshes the cache; concurrent callers share one query. On a
// database error the old set stays in use and the next call retries.
func (s *Server) reloadKeys(ctx context.Context) {
	ks := &s.team().keys
	_, _, _ = ks.loads.Do("keys", func() (any, error) {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		rows, err := s.db.Query(cctx, `SELECT id, name, scope, key_hash, expires_at FROM visionary_api_keys WHERE revoked_at IS NULL`)
		if err != nil {
			s.logger.Warn("load api keys", "err", err)
			return nil, err
		}
		next := make(map[string]cachedKey, len(rows.Rows))
		for _, row := range rows.Rows {
			if len(row) != 5 {
				continue
			}
			k := cachedKey{id: toInt(row[0]), name: toString(row[1]), scope: toString(row[2])}
			if v := toString(row[4]); v != "" {
				k.expires, _ = time.Parse(tsLayout, v)
			}
			next[toString(row[3])] = k
		}
		ks.mu.Lock()
		ks.byHash, ks.loaded = next, time.Now()
		ks.mu.Unlock()
		return nil, nil
	})
}

func (s *Server) invalidateKeys() {
	ks := &s.team().keys
	ks.mu.Lock()
	ks.loaded = time.Time{}
	ks.mu.Unlock()
}

// flushKeyUse writes the batched last-used times once a minute.
func (s *Server) flushKeyUse(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			s.writeKeyUse(context.Background())
			return
		case <-tick.C:
			s.writeKeyUse(ctx)
		}
	}
}

func (s *Server) writeKeyUse(ctx context.Context) {
	ks := &s.team().keys
	ks.mu.Lock()
	used := ks.used
	ks.used = map[int64]time.Time{}
	ks.mu.Unlock()
	for id, at := range used {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := s.db.Exec(cctx, `UPDATE visionary_api_keys SET last_used_at = ? WHERE id = ?`, at.Format(tsLayout), id)
		cancel()
		if err != nil {
			s.logger.Warn("record api key use", "err", err)
		}
	}
}

// pendingUse is the newest unwritten last-used time of a key.
func (s *Server) pendingUse(id int64) (time.Time, bool) {
	ks := &s.team().keys
	ks.mu.Lock()
	defer ks.mu.Unlock()
	t, ok := ks.used[id]
	return t, ok
}

// ingestKeyValid checks a key presented to an ingest endpoint. scope is
// "server" (SDKs and OTLP, VISIONARY_INGEST_KEYS) or "client" (browser and
// mobile apps, VISIONARY_CLIENT_KEYS); both lists are comma separated. Keys
// created in Settings with the same scope are accepted too.
func (s *Server) ingestKeyValid(key, scope string) bool {
	if key == "" {
		return false
	}
	env := "VISIONARY_INGEST_KEYS"
	if scope == "client" {
		env = "VISIONARY_CLIENT_KEYS"
	}
	for _, k := range strings.Split(s.conf(env), ",") {
		if k = strings.TrimSpace(k); k != "" && constantTimeEqual(k, key) {
			return true
		}
	}
	if scope != "server" && scope != "client" {
		return false
	}
	_, ok := s.lookupKey(context.Background(), key, scope)
	return ok
}

// bearerKey returns a Visionary API key from the Authorization header.
func bearerKey(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if len(h) < 7 || !strings.EqualFold(h[:7], "bearer ") {
		return "", false
	}
	key := strings.TrimSpace(h[7:])
	return key, strings.HasPrefix(key, keyPrefix)
}

// readPosts are the POST endpoints that only read: read keys may call them.
var readPosts = []string{"/api/query", "/api/analyst"}

// keyAuthed serves a request made with a read key as a viewer. Keys never
// write, whatever the handler would allow.
func (s *Server) keyAuthed(w http.ResponseWriter, r *http.Request, raw string, h http.HandlerFunc) {
	k, ok := s.lookupKey(r.Context(), raw, "read")
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid, expired or revoked API key")
		return
	}
	readOnly := r.Method == http.MethodGet || r.Method == http.MethodHead ||
		(r.Method == http.MethodPost && slices.Contains(readPosts, r.URL.Path))
	if !readOnly {
		writeError(w, http.StatusForbidden, "API keys are read-only")
		return
	}
	if needsJSON(r) && !isJSON(r) {
		writeError(w, http.StatusUnsupportedMediaType, "expected application/json")
		return
	}
	sess := &session{
		UserID:  "key:" + strconv.FormatInt(k.id, 10),
		Name:    "API key " + k.name,
		Roles:   []string{"api-key"},
		Role:    roleViewer.String(),
		Expires: time.Now().Add(time.Hour).Unix(),
	}
	h(w, r.WithContext(context.WithValue(r.Context(), sessionKey{}, sess)))
}

func (s *Server) handleListKeys(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT id, name, scope, prefix, created_by, created_at, expires_at, last_used_at, revoked_at
FROM visionary_api_keys ORDER BY id DESC`)
	if err != nil {
		s.fail(w, err)
		return
	}
	keys := make([]map[string]any, 0, len(rows.Rows))
	for _, row := range rows.Rows {
		if len(row) != 9 {
			continue
		}
		id := toInt(row[0])
		lastUsed := toString(row[7])
		if t, ok := s.pendingUse(id); ok {
			lastUsed = t.Format(tsLayout)
		}
		keys = append(keys, map[string]any{
			"id": id, "name": toString(row[1]), "scope": toString(row[2]), "prefix": toString(row[3]),
			"created_by": toString(row[4]), "created_at": toString(row[5]), "expires_at": nullable(row[6]),
			"last_used_at": nullable(lastUsed), "revoked_at": nullable(row[8]),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
}

func nullable(v any) any {
	if s := toString(v); s != "" {
		return s
	}
	return nil
}

func (s *Server) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	var body struct {
		Name        string `json:"name"`
		Scope       string `json:"scope"`
		ExpiresDays int    `json:"expires_days"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" || len(body.Name) > maxKeyName {
		writeError(w, http.StatusBadRequest, "name must be 1–"+strconv.Itoa(maxKeyName)+" characters")
		return
	}
	if !slices.Contains(keyScopes, body.Scope) {
		writeError(w, http.StatusBadRequest, "scope must be read, server or client")
		return
	}
	if body.ExpiresDays < 0 || body.ExpiresDays > 3650 {
		writeError(w, http.StatusBadRequest, "expiry must be 0 (never) to 3650 days")
		return
	}
	secret, err := newKeySecret()
	if err != nil {
		s.fail(w, err)
		return
	}
	now := time.Now().UTC()
	expires := ""
	if body.ExpiresDays > 0 {
		expires = now.AddDate(0, 0, body.ExpiresDays).Format(tsLayout)
	}
	sess := sessionFrom(r)
	by := fallback(sess.Name, sess.UserID)
	prefix := secret[:keyVisibleLen]
	id, err := s.db.InsertID(r.Context(), `INSERT INTO visionary_api_keys (name, scope, prefix, key_hash, created_by, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, NULLIF(?, ''))`, body.Name, body.Scope, prefix, hashKey(secret), by, now.Format(tsLayout), expires)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.invalidateKeys()
	s.audit(r, "api_key.create", strconv.FormatInt(id, 10), map[string]any{"name": body.Name, "scope": body.Scope, "prefix": prefix, "expires_at": nullable(expires)})
	writeJSON(w, http.StatusCreated, map[string]any{
		"key": map[string]any{
			"id": id, "name": body.Name, "scope": body.Scope, "prefix": prefix, "created_by": by,
			"created_at": now.Format(tsLayout), "expires_at": nullable(expires), "last_used_at": nil, "revoked_at": nil,
		},
		// Shown once; only the hash is kept.
		"secret": secret,
	})
}

func (s *Server) handleRevokeKey(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid key id")
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT name, revoked_at FROM visionary_api_keys WHERE id = ?`, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if len(rows.Rows) == 0 || len(rows.Rows[0]) != 2 {
		writeError(w, http.StatusNotFound, "key not found")
		return
	}
	if toString(rows.Rows[0][1]) != "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := s.db.Exec(r.Context(), `UPDATE visionary_api_keys SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
		time.Now().UTC().Format(tsLayout), id); err != nil {
		s.fail(w, err)
		return
	}
	s.invalidateKeys()
	s.audit(r, "api_key.revoke", strconv.FormatInt(id, 10), map[string]any{"name": toString(rows.Rows[0][0])})
	w.WriteHeader(http.StatusNoContent)
}
