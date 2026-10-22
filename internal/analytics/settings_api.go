package analytics

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Settings → Thresholds, Retention and Status page (admins), and each
// person's own preferences (anyone signed in).

func (s *Server) serverSettingsRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/settings/server", s.authed(s.handleServerSettings))
	mux.Handle("PUT /api/settings/thresholds", s.authed(s.handleThresholds))
	mux.Handle("PUT /api/settings/retention", s.authed(s.handleRetention))
	mux.Handle("GET /api/settings/retention/usage", s.authed(s.handleRetentionUsage))
	mux.Handle("PUT /api/settings/status-page", s.authed(s.handleStatusPageSettings))
	mux.Handle("GET /api/me/preferences", s.authed(s.handleGetPreferences))
	mux.Handle("PUT /api/me/preferences", s.authed(s.handlePutPreferences))
}

func (s *Server) handleServerSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"settings":        s.live(),
		"retention_kinds": retentionKinds,
		"can_admin":       s.can(r, "admin"),
	})
}

// afterChange makes a change take effect at once on this server; others
// pick it up within 30 seconds.
func (s *Server) afterChange(w http.ResponseWriter, r *http.Request) {
	if err := s.refreshLive(r.Context()); err != nil {
		s.fail(w, err)
		return
	}
	s.handleServerSettings(w, r)
}

func (s *Server) handleThresholds(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	var body struct {
		Apdex     apdexRule `json:"apdex"`
		SLOTarget float64   `json:"slo_target"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Apdex.DefaultMs < 1 || body.Apdex.DefaultMs > 600000 {
		writeError(w, http.StatusBadRequest, "the Apdex threshold is 1 ms to 10 minutes")
		return
	}
	if body.SLOTarget < 50 || body.SLOTarget >= 100 {
		writeError(w, http.StatusBadRequest, "the SLO target is at least 50% and below 100%")
		return
	}
	routes, err := validApdexRoutes(body.Apdex.Routes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	body.Apdex.Routes = routes
	rule, _ := json.Marshal(body.Apdex)
	if err := s.writeSettings(r.Context(), map[string]string{
		settingApdex:     string(rule),
		settingSLOTarget: strconv.FormatFloat(body.SLOTarget, 'f', -1, 64),
	}); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "settings.thresholds", "", map[string]any{"apdex_ms": body.Apdex.DefaultMs, "routes": len(routes), "slo_target": body.SLOTarget})
	s.afterChange(w, r)
}

func (s *Server) handleRetention(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	var body struct {
		Retention map[string]int `json:"retention"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	kv := map[string]string{}
	for k, days := range body.Retention {
		known := false
		for _, rk := range retentionKinds {
			known = known || rk.Key == k
		}
		switch {
		case !known:
			writeError(w, http.StatusBadRequest, "unknown kind of data: "+k)
			return
		case days < 0 || days > 3650:
			writeError(w, http.StatusBadRequest, "keep data for 0 (forever) to 3650 days")
			return
		case (k == "audit") && days > 0 && days < 30:
			writeError(w, http.StatusBadRequest, "the audit log is kept for at least 30 days")
			return
		}
		kv[settingRetention+k] = strconv.Itoa(days)
	}
	if len(kv) == 0 {
		writeError(w, http.StatusBadRequest, "nothing to change")
		return
	}
	if err := s.writeSettings(r.Context(), kv); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "settings.retention", "", body.Retention)
	s.afterChange(w, r)
}

func (s *Server) handleRetentionUsage(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"usage": s.retentionUsage(r.Context())})
}

func (s *Server) handleStatusPageSettings(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	var body struct {
		Enabled bool   `json:"enabled"`
		Title   string `json:"title"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	body.Title = strings.TrimSpace(body.Title)
	if len(body.Title) > 120 {
		writeError(w, http.StatusBadRequest, "the title is at most 120 characters")
		return
	}
	if err := s.writeSettings(r.Context(), map[string]string{
		settingStatusPage:  strconv.FormatBool(body.Enabled),
		settingStatusTitle: body.Title,
	}); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "settings.status_page", "", map[string]any{"enabled": body.Enabled, "title": body.Title})
	statusCache.Lock()
	delete(statusCache.page, s)
	statusCache.Unlock()
	s.afterChange(w, r)
}

// Preferences are one JSON object per person, in visionary_settings under
// "prefs.<user>". The dashboard owns their shape; the server only keeps them
// small.
func prefsKey(r *http.Request) string {
	sess := sessionFrom(r)
	id := "local"
	if sess != nil {
		switch {
		case sess.UserID != "":
			id = sess.UserID
		case sess.Handle != "":
			id = sess.Handle
		}
	}
	return "prefs." + id
}

func (s *Server) handleGetPreferences(w http.ResponseWriter, r *http.Request) {
	m, err := s.readSettings(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	prefs := json.RawMessage("{}")
	if v := m[prefsKey(r)][0]; v != "" && json.Valid([]byte(v)) {
		prefs = json.RawMessage(v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"preferences": prefs})
}

func (s *Server) handlePutPreferences(w http.ResponseWriter, r *http.Request) {
	var prefs map[string]any
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&prefs); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body (at most 16 KB)")
		return
	}
	b, _ := json.Marshal(prefs)
	if err := s.db.Exec(r.Context(), s.q.upsertSetting(), prefsKey(r), string(b), time.Now().UTC().Format(tsLayout)); err != nil {
		s.fail(w, dbErr(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"preferences": json.RawMessage(b)})
}
