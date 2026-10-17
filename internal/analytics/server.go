package analytics

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/styropyr0/visionary/internal/authbridge"
	"github.com/styropyr0/visionary/internal/db"
)

// UserAgent identifies Visionary to the auth API and model provider.
const UserAgent = "Visionary/1.0"

type Server struct {
	cfg Config
	db  db.DB
	q   sqlDialect

	overview    map[string]string
	performance map[string]string
	system      map[string]string
	bridge      *authbridge.Bridge
	llm         *llm
	auth        *auth
	logger      *slog.Logger
	static      fs.FS

	routesMu sync.Mutex
	routes   []string
	routesAt time.Time
}

// New wires the server. static is the built frontend (index.html at its root).
func New(cfg Config, logger *slog.Logger, static fs.FS) (*Server, error) {
	conn, err := db.Open(db.Config{
		URL:          cfg.DatabaseURL,
		D1APIBase:    cfg.D1APIBase,
		D1AccountID:  cfg.D1AccountID,
		D1DatabaseID: cfg.D1DatabaseID,
		D1APIToken:   cfg.D1APIToken,
	})
	if err != nil {
		return nil, err
	}
	logger.Info("database", "backend", conn.Name())
	bridge := authbridge.New(cfg.Auth, UserAgent)
	a, ephemeral, err := newAuth(cfg, bridge)
	if err != nil {
		return nil, err
	}
	if ephemeral {
		logger.Warn("VISIONARY_SESSION_SECRET not set; sessions reset on every restart")
	}
	q := sqlDialect{conn.Dialect()}
	s := &Server{cfg: cfg, db: conn, q: q, overview: overviewPanels(q), performance: performancePanels(q, cfg.ApdexMs), system: systemPanels(q), bridge: bridge, llm: newLLM(cfg), auth: a, logger: logger, static: static}
	if s.llm == nil {
		logger.Warn("OPENAI_API_KEY not set; the analyst is disabled")
	}
	switch cfg.Auth.Type {
	case authbridge.TypeNone:
		logger.Warn("VISIONARY_AUTH_MODE=none: anyone who can reach this server can use it; never expose it publicly")
	case authbridge.TypeShared:
		logger.Info("sign-in with a shared team password")
	default:
		if !bridge.CanRecheck() {
			logger.Warn("no profile endpoint (VISIONARY_AUTH_PROFILE); roles are only checked at sign-in")
		}
		logger.Info("sign-in delegated", "provider", cfg.Auth.Provider, "mode", cfg.Auth.Type,
			"base_url", cfg.Auth.BaseURL, "allowed_roles", cfg.Auth.AllowedRoles, "location", cfg.Auth.Location)
	}
	return s, nil
}

// Init creates the saved-panels table.
func (s *Server) Init(ctx context.Context) error {
	return s.ensureSchema(ctx)
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/auth/config", s.handleAuthConfig)
	mux.HandleFunc("POST /api/auth/start", s.handleAuthStart)
	mux.HandleFunc("POST /api/auth/complete", s.handleAuthComplete)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.Handle("GET /api/session", s.authed(s.handleSession))
	mux.Handle("GET /api/meta", s.authed(s.handleMeta))
	mux.Handle("GET /api/overview/{panel}", s.authed(s.handleOverview))
	mux.Handle("GET /api/system/{panel}", s.authed(s.handleSystem))
	mux.Handle("GET /api/performance/{panel}", s.authed(s.handlePerformance))
	mux.Handle("POST /api/analyst", s.authed(s.handleAnalyst))
	mux.Handle("POST /api/query", s.authed(s.handleQuery))
	mux.Handle("GET /api/panels", s.authed(s.handleListPanels))
	mux.Handle("POST /api/panels", s.authed(s.handleCreatePanel))
	mux.Handle("DELETE /api/panels/{id}", s.authed(s.handleDeletePanel))
	mux.Handle("GET /api/logs", s.authed(s.handleLogs))
	mux.Handle("GET /api/logs/{id}", s.authed(s.handleLog))
	s.featureRoutes(mux)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	mux.Handle("/", s.spa())
	return securityHeaders(mux)
}

type sessionKey struct{}

// authed requires an admin session and re-confirms the role with the API
// every RoleCheckEvery. Writes also require a JSON body, which a cross-site
// form cannot send, on top of the SameSite=Strict cookie.
func (s *Server) authed(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Auth.Type == authbridge.TypeNone {
			if r.Method == http.MethodPost && !isJSON(r) {
				writeError(w, http.StatusUnsupportedMediaType, "expected application/json")
				return
			}
			h(w, r.WithContext(context.WithValue(r.Context(), sessionKey{}, localSession)))
			return
		}
		sess, ok := s.auth.read(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "sign in required")
			return
		}
		next, changed, err := s.auth.confirm(r.Context(), sess)
		if err != nil {
			if !errors.Is(err, errAPIUnreachable) {
				s.auth.clear(w, r)
			}
			s.logger.Info("session rejected", "user_id", sess.UserID, "reason", err.Error())
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		if changed {
			if err := s.auth.write(w, r, next); err != nil {
				s.fail(w, err)
				return
			}
		}
		if r.Method == http.MethodPost && !isJSON(r) {
			writeError(w, http.StatusUnsupportedMediaType, "expected application/json")
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), sessionKey{}, next)))
	})
}

// handleAuthConfig tells the sign-in screen which flow to render.
func (s *Server) handleAuthConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.cfg.Auth.Public())
}

// localSession stands in for a user when sign-in is off (mode none).
var localSession = &session{UserID: "local", Name: "Local user", Roles: []string{"local"}, Expires: 4102444800} // 2100-01-01

// completeShared checks the team password. Sessions carry a random token only
// so logout can end them; there is no API to re-check against.
func (s *Server) completeShared(w http.ResponseWriter, r *http.Request, password string) {
	if !s.auth.checkShared(password) {
		s.logger.Warn("shared password sign-in failed", "ip", s.auth.clientIP(r))
		writeError(w, http.StatusUnauthorized, "That password isn't right.")
		return
	}
	token := make([]byte, 24)
	if _, err := rand.Read(token); err != nil {
		s.fail(w, err)
		return
	}
	sess := s.auth.newSession(&authbridge.Result{
		Token: "shared:" + hex.EncodeToString(token),
		User:  authbridge.User{ID: "team", Name: "Team", Roles: []string{"team"}},
	})
	if err := s.auth.write(w, r, sess); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sessionView(sess))
}

// handleAuthStart sends the one-time code (otp flow).
func (s *Server) handleAuthStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Identifier string `json:"identifier"`
	}
	if !s.loginPreflight(w, r, &body) {
		return
	}
	id, ok := s.identifier(w, body.Identifier)
	if !ok {
		return
	}
	sentTo, err := s.bridge.Start(r.Context(), id)
	if err != nil {
		s.apiFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"sent_to": sentTo})
}

// handleAuthComplete signs in through the auth API and lets only users with
// an allowed role in. Anyone else's freshly issued API session is revoked
// straight away.
func (s *Server) handleAuthComplete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Identifier string   `json:"identifier"`
		Code       string   `json:"code"`
		Password   string   `json:"password"`
		Latitude   *float64 `json:"latitude"`
		Longitude  *float64 `json:"longitude"`
	}
	if !s.loginPreflight(w, r, &body) {
		return
	}
	switch s.cfg.Auth.Type {
	case authbridge.TypeShared:
		s.completeShared(w, r, body.Password)
		return
	case authbridge.TypeNone:
		writeJSON(w, http.StatusOK, sessionView(localSession))
		return
	}
	id, ok := s.identifier(w, body.Identifier)
	if !ok {
		return
	}
	var pos *authbridge.Position
	if body.Latitude != nil && body.Longitude != nil {
		if *body.Latitude < -90 || *body.Latitude > 90 || *body.Longitude < -180 || *body.Longitude > 180 {
			writeError(w, http.StatusBadRequest, "That location isn't valid.")
			return
		}
		pos = &authbridge.Position{Latitude: *body.Latitude, Longitude: *body.Longitude}
	}
	if pos == nil && s.cfg.Auth.Location == authbridge.LocationRequired {
		writeError(w, http.StatusBadRequest, "Allow location access to sign in: "+s.cfg.Auth.Provider+" needs it.")
		return
	}
	if s.cfg.Auth.Location == authbridge.LocationNone {
		pos = nil
	}
	secret := body.Password
	if s.cfg.Auth.Type == authbridge.TypeOTP {
		secret = body.Code
		if len(secret) != s.cfg.Auth.CodeLength || strings.Trim(secret, "0123456789") != "" {
			writeError(w, http.StatusBadRequest, "Enter the "+strconv.Itoa(s.cfg.Auth.CodeLength)+"-digit code.")
			return
		}
	} else if secret == "" || len(secret) > 1024 {
		writeError(w, http.StatusBadRequest, "Enter your password.")
		return
	}

	res, err := s.bridge.Complete(r.Context(), id, secret, r.UserAgent(), pos)
	if err != nil {
		if res != nil {
			s.revoke(res.Token)
		}
		s.apiFail(w, err)
		return
	}
	if !s.bridge.Allowed(&res.User) {
		s.revoke(res.Token)
		s.logger.Warn("sign-in refused: role not allowed", "user_id", res.User.ID, "roles", res.User.Roles)
		writeError(w, http.StatusForbidden, "This account doesn't have access to Visionary.")
		return
	}
	sess := s.auth.newSession(res)
	if err := s.auth.write(w, r, sess); err != nil {
		s.fail(w, err)
		return
	}
	s.logger.Info("sign-in", "user_id", sess.UserID, "roles", sess.Roles)
	writeJSON(w, http.StatusOK, sessionView(sess))
}

func (s *Server) identifier(w http.ResponseWriter, raw string) (string, bool) {
	id, err := s.bridge.Normalize(raw)
	if err != nil {
		hint := s.cfg.Auth.Identifier.PatternHint
		if hint == "" {
			hint = "Check the " + strings.ToLower(s.cfg.Auth.Identifier.Label) + "."
		}
		writeError(w, http.StatusBadRequest, hint)
		return "", false
	}
	return id, true
}

// loginPreflight applies the JSON and rate-limit checks shared by sign-in.
func (s *Server) loginPreflight(w http.ResponseWriter, r *http.Request, dst any) bool {
	if !isJSON(r) {
		writeError(w, http.StatusUnsupportedMediaType, "expected application/json")
		return false
	}
	if !s.auth.allowLogin(r) {
		writeError(w, http.StatusTooManyRequests, "Too many attempts. Try again in a minute.")
		return false
	}
	if err := readJSON(r, dst); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return false
	}
	return true
}

// apiFail passes the API's own message through for rejected requests (wrong
// code, unknown number, cooldown) and hides details of outages.
func (s *Server) apiFail(w http.ResponseWriter, err error) {
	var ae *authbridge.APIError
	if errors.As(err, &ae) && !ae.Unavailable() {
		status := ae.Status
		if status == http.StatusUnauthorized {
			// The API's 401 here means a bad code or password, not a missing
			// Visionary session.
			status = http.StatusBadRequest
		}
		writeError(w, status, ae.Message)
		return
	}
	s.logger.Error("auth api call failed", "err", err)
	writeError(w, http.StatusBadGateway, "Can't reach "+s.cfg.Auth.Provider+" right now. Try again shortly.")
}

// revoke logs a token out on the API without holding up the response.
func (s *Server) revoke(token string) {
	if token == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.bridge.Logout(ctx, token); err != nil {
			s.logger.Warn("revoke api session", "err", err)
		}
	}()
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if sess, ok := s.auth.read(r); ok {
		s.auth.markSignedOut(sess)
		s.revoke(sess.Token)
	}
	s.auth.clear(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	sess, _ := r.Context().Value(sessionKey{}).(*session)
	writeJSON(w, http.StatusOK, sessionView(sess))
}

func sessionView(s *session) map[string]any {
	return map[string]any{
		"user":       map[string]any{"id": s.UserID, "name": s.Name, "handle": s.Handle, "roles": s.Roles},
		"expires_at": s.expiresAt().UTC(),
	}
}

func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	model := ""
	if s.llm != nil {
		model = s.llm.model
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ask_enabled":    s.llm != nil,
		"app_name":       s.cfg.AppName,
		"model":          model,
		"routes":         s.knownRoutes(r.Context()),
		"max_rows":       s.cfg.MaxRows,
		"max_range_days": s.cfg.MaxRangeDays,
		"apdex_ms":       s.cfg.ApdexMs,
		"slo_target":     s.cfg.SLOTarget,
	})
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("panel")
	sql, ok := s.overview[name]
	if !ok {
		writeError(w, http.StatusNotFound, "unknown panel")
		return
	}
	tr, err := parseRange(r.URL.Query(), s.cfg.MaxRangeDays)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if name == "stats" {
		s.handleStats(w, r, sql, tr)
		return
	}
	res, err := s.runTemplate(r.Context(), sql, tr, true)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// errNoSystemMetrics is answered with 404 so the System view can explain how
// to turn the recorder on, instead of showing a SQL error.
const errNoSystemMetrics = "no system metrics yet: a Visionary SDK in your service writes them to system_metrics while VISIONARY_SYSTEM_METRICS is on (the default) and its database points here"

func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	sql, ok := s.system[r.PathValue("panel")]
	if !ok {
		writeError(w, http.StatusNotFound, "unknown panel")
		return
	}
	tr, err := parseRange(r.URL.Query(), s.cfg.MaxRangeDays)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	clause, err := instanceClause(r.URL.Query().Get("instance"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := s.runTemplate(r.Context(), strings.ReplaceAll(sql, "{{instance}}", clause), tr, true)
	if err != nil {
		if missingTable(err, "system_metrics") {
			writeError(w, http.StatusNotFound, errNoSystemMetrics)
			return
		}
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleStats returns the window and the one before it, for deltas.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request, sql string, tr timeRange) {
	var (
		wg              sync.WaitGroup
		cur, prev       *Result
		curErr, prevErr error
	)
	wg.Add(2)
	go func() { defer wg.Done(); cur, curErr = s.runTemplate(r.Context(), sql, tr, true) }()
	go func() { defer wg.Done(); prev, prevErr = s.runTemplate(r.Context(), sql, tr.previous(), true) }()
	wg.Wait()
	if curErr != nil {
		s.fail(w, curErr)
		return
	}
	if prevErr != nil {
		s.logger.Warn("previous-period stats failed", "err", prevErr)
	}
	writeJSON(w, http.StatusOK, map[string]any{"current": cur, "previous": prev})
}

func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Query string `json:"query"`
		From  string `json:"from"`
		To    string `json:"to"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tr, err := parseRange(rangeValues(body.From, body.To), s.cfg.MaxRangeDays)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := s.runTemplate(r.Context(), body.Query, tr, false)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleListPanels(w http.ResponseWriter, r *http.Request) {
	panels, err := s.listPanels(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"panels": panels})
}

func (s *Server) handleCreatePanel(w http.ResponseWriter, r *http.Request) {
	var p Panel
	if err := readJSON(r, &p); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	created, err := s.createPanel(r.Context(), p)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleDeletePanel(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid panel id")
		return
	}
	if err := s.deletePanel(r.Context(), id); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	tr, err := parseRange(r.URL.Query(), s.cfg.MaxRangeDays)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := s.listLogs(r.Context(), tr, r.URL.Query())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleLog(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid log id")
		return
	}
	res, err := s.getLog(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if len(res.Rows) == 0 {
		writeError(w, http.StatusNotFound, "log not found")
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// fail maps query errors to 400 with their message; anything else is logged
// and reported as a generic upstream failure.
func (s *Server) fail(w http.ResponseWriter, err error) {
	var qe *queryError
	if errors.As(err, &qe) {
		writeError(w, http.StatusBadRequest, qe.Error())
		return
	}
	if errors.Is(err, context.Canceled) {
		return
	}
	s.logger.Error("analytics request failed", "err", err)
	writeError(w, http.StatusBadGateway, "upstream request failed")
}

// spa serves built assets and falls back to index.html for client routes.
func (s *Server) spa() http.Handler {
	files := http.FileServerFS(s.static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name != "" {
			if f, err := s.static.Open(name); err == nil {
				st, statErr := f.Stat()
				f.Close()
				if statErr == nil && !st.IsDir() {
					if strings.HasPrefix(name, "assets/") {
						// Vite fingerprints asset names, so they never change.
						w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
					}
					files.ServeHTTP(w, r)
					return
				}
			}
		}
		index, err := fs.ReadFile(s.static, "index.html")
		if err != nil {
			http.Error(w, "frontend is not built; run npm run build in web/analytics", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(index)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	const csp = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data:; font-src 'self' data:; connect-src 'self'; " +
		"frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func rangeValues(from, to string) map[string][]string {
	return map[string][]string{"from": {from}, "to": {to}}
}

func isJSON(r *http.Request) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mt == "application/json"
}

func readJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 256<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return errors.New("invalid JSON body")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// missingTable reports a "table does not exist" error in any supported dialect.
func missingTable(err error, table string) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, table) &&
		(strings.Contains(msg, "no such table") || strings.Contains(msg, "does not exist") || strings.Contains(msg, "doesn't exist"))
}
