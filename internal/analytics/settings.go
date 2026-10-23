package analytics

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	sdk "github.com/styropyr0/visionary/sdks/go"
)

// Team-wide settings changed from the dashboard, kept in visionary_settings
// (name, value). The SDKs read the profile.* rows to know whether to take CPU
// profiles (see sdks/SPEC.md, "CPU profiles"); ui.* rows shape the dashboard.

const (
	settingProfileEnabled  = "profile.enabled"
	settingProfileServices = "profile.services"
	settingProfileInterval = "profile.interval_s"
	settingProfileDuration = "profile.duration_ms"
	settingProfileNow      = "profile.now"
	settingHiddenPages     = "ui.hidden_pages"
)

// The pages that can be hidden from the sidebar; Settings always shows, so
// nobody can lock the team out of undoing it.
var hideablePages = []string{"overview", "performance", "queries", "dependencies", "profiles", "system", "containers", "errors", "frontend", "uptime", "security", "explore", "logs", "dashboards", "saved", "alerts"}

func (s *Server) settingsSchema() []string { return []string{s.q.settingsTable()} }

func (s *Server) settingsRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/settings", s.authed(s.handleSettings))
	mux.Handle("PUT /api/settings/profile", s.authed(s.handleProfileSettings))
	mux.Handle("POST /api/settings/profile/now", s.authed(s.handleProfileNow))
	mux.Handle("GET /api/settings/profile/services", s.authed(s.handleProfileServices))
	mux.Handle("PUT /api/settings/pages", s.authed(s.handlePageSettings))
	mux.Handle("GET /api/settings/config", s.authed(s.handleConfigSettings))
	mux.Handle("PUT /api/settings/config", s.authed(s.handlePutConfig))
	mux.Handle("GET /api/settings/signin", s.authed(s.handleSigninSettings))
	mux.Handle("PUT /api/settings/signin", s.authed(s.handlePutSignin))
	s.serverSettingsRoutes(mux)
}

func (q sqlDialect) settingsTable() string {
	name, ts := "TEXT", "TEXT"
	if q.d == sdk.MySQL {
		name, ts = "VARCHAR(191)", "VARCHAR(32)"
	}
	return `CREATE TABLE IF NOT EXISTS visionary_settings (
    name       ` + name + ` NOT NULL PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at ` + ts + ` NOT NULL
)`
}

func (q sqlDialect) upsertSetting() string {
	const ins = `INSERT INTO visionary_settings (name, value, updated_at) VALUES (?, ?, ?)`
	if q.d == sdk.MySQL {
		return ins + ` ON DUPLICATE KEY UPDATE value = VALUES(value), updated_at = VALUES(updated_at)`
	}
	return ins + ` ON CONFLICT (name) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`
}

// insertSettingIfAbsent stores a value only when the name has none, so
// servers starting together keep the first one.
func (q sqlDialect) insertSettingIfAbsent() string {
	if q.d == sdk.MySQL {
		return `INSERT IGNORE INTO visionary_settings (name, value, updated_at) VALUES (?, ?, ?)`
	}
	return `INSERT INTO visionary_settings (name, value, updated_at) VALUES (?, ?, ?) ON CONFLICT (name) DO NOTHING`
}

func nowTS() string { return time.Now().UTC().Format(tsLayout) }

type profileSettings struct {
	Enabled    bool     `json:"enabled"`
	Services   []string `json:"services"`
	IntervalS  int      `json:"interval_s"`
	DurationMs int      `json:"duration_ms"`
	Now        string   `json:"now,omitempty"`
	UpdatedAt  string   `json:"updated_at,omitempty"`
}

type settingsView struct {
	Profile     profileSettings `json:"profile"`
	HiddenPages []string        `json:"hidden_pages"`
	CanAdmin    bool            `json:"can_admin"`
	CanEdit     bool            `json:"can_edit"`
}

func (s *Server) readSettings(ctx context.Context) (map[string][2]string, error) {
	rows, err := s.db.Query(ctx, `SELECT name, value, updated_at FROM visionary_settings`)
	if err != nil {
		return nil, dbErr(err)
	}
	out := map[string][2]string{}
	for _, r := range rows.Rows {
		out[toString(r[0])] = [2]string{toString(r[1]), toString(r[2])}
	}
	return out, nil
}

func (s *Server) loadSettings(ctx context.Context) (settingsView, error) {
	m, err := s.readSettings(ctx)
	if err != nil {
		return settingsView{}, err
	}
	p := profileSettings{IntervalS: 300, DurationMs: 10000, Services: []string{}}
	p.Enabled = m[settingProfileEnabled][0] == "true"
	if v := m[settingProfileServices][0]; v != "" {
		_ = json.Unmarshal([]byte(v), &p.Services)
	}
	if n, err := strconv.Atoi(m[settingProfileInterval][0]); err == nil && n > 0 {
		p.IntervalS = n
	}
	if n, err := strconv.Atoi(m[settingProfileDuration][0]); err == nil && n > 0 {
		p.DurationMs = n
	}
	p.Now = m[settingProfileNow][0]
	for _, k := range []string{settingProfileEnabled, settingProfileServices, settingProfileInterval, settingProfileDuration} {
		if at := m[k][1]; at > p.UpdatedAt {
			p.UpdatedAt = at
		}
	}
	hidden := []string{}
	if v := m[settingHiddenPages][0]; v != "" {
		_ = json.Unmarshal([]byte(v), &hidden)
	}
	return settingsView{Profile: p, HiddenPages: hidden}, nil
}

func (s *Server) writeSettings(ctx context.Context, kv map[string]string) error {
	now := time.Now().UTC().Format(tsLayout)
	for k, v := range kv {
		if err := s.db.Exec(ctx, s.q.upsertSetting(), k, v, now); err != nil {
			return dbErr(err)
		}
	}
	return nil
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	v, err := s.loadSettings(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	v.CanAdmin, v.CanEdit = s.can(r, "admin"), s.can(r, "edit")
	writeJSON(w, http.StatusOK, v)
}

// handleProfileSettings turns profiling on or off and sets its pace. The
// SDKs pick the change up within about 30 seconds.
func (s *Server) handleProfileSettings(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	var body profileSettings
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	switch {
	case body.IntervalS < 60 || body.IntervalS > 86400:
		writeError(w, http.StatusBadRequest, "the interval must be between 1 minute and 1 day")
		return
	case body.DurationMs < 1000 || body.DurationMs > 60000:
		writeError(w, http.StatusBadRequest, "each profile must sample for 1 to 60 seconds")
		return
	case body.DurationMs >= body.IntervalS*1000:
		writeError(w, http.StatusBadRequest, "each profile must be shorter than the interval")
		return
	}
	services := []string{}
	for _, sv := range body.Services {
		if sv = strings.TrimSpace(sv); sv != "" && !slices.Contains(services, sv) {
			services = append(services, sv)
		}
	}
	list, _ := json.Marshal(services)
	if err := s.writeSettings(r.Context(), map[string]string{
		settingProfileEnabled:  strconv.FormatBool(body.Enabled),
		settingProfileServices: string(list),
		settingProfileInterval: strconv.Itoa(body.IntervalS),
		settingProfileDuration: strconv.Itoa(body.DurationMs),
	}); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "profiling.update", "", map[string]any{"enabled": body.Enabled, "services": services, "interval_s": body.IntervalS, "duration_ms": body.DurationMs})
	s.handleSettings(w, r)
}

// handleProfileNow asks every SDK that takes part to take one profile at
// once, whether or not profiling is on.
func (s *Server) handleProfileNow(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "edit") {
		return
	}
	if err := s.writeSettings(r.Context(), map[string]string{settingProfileNow: time.Now().UTC().Format(tsLayout)}); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "profiling.now", "", nil)
	s.handleSettings(w, r)
}

// handleProfileServices lists the services that reported in the last day,
// with their SDKs and their latest profile, for choosing which to profile.
func (s *Server) handleProfileServices(w http.ResponseWriter, r *http.Request) {
	since := time.Now().UTC().Add(-24 * time.Hour).Format(tsLayout)
	ctx := r.Context()
	type svc struct {
		Service     string   `json:"service"`
		SDKs        []string `json:"sdks"`
		Instances   int64    `json:"instances"`
		LastSeen    string   `json:"last_seen"`
		LastProfile string   `json:"last_profile,omitempty"`
		Profiles    int64    `json:"profiles"`
	}
	rows, err := s.db.Query(ctx, `SELECT COALESCE(service, ''), COALESCE(sdk, ''), COUNT(DISTINCT instance), MAX(created_at)
FROM system_metrics WHERE created_at >= ? AND (sdk IS NULL OR sdk <> 'agent')
GROUP BY COALESCE(service, ''), COALESCE(sdk, '')`, since)
	if err != nil {
		s.fail(w, dbErr(err))
		return
	}
	by := map[string]*svc{}
	var order []string
	for _, row := range rows.Rows {
		name := toString(row[0])
		v := by[name]
		if v == nil {
			v = &svc{Service: name, SDKs: []string{}}
			by[name] = v
			order = append(order, name)
		}
		if k := toString(row[1]); k != "" && !slices.Contains(v.SDKs, k) {
			v.SDKs = append(v.SDKs, k)
		}
		v.Instances += toInt(row[2])
		if at := toString(row[3]); at > v.LastSeen {
			v.LastSeen = at
		}
	}
	prof, err := s.db.Query(ctx, `SELECT COALESCE(service, ''), COUNT(*), MAX(created_at) FROM profiles WHERE created_at >= ? GROUP BY COALESCE(service, '')`,
		time.Now().UTC().Add(-7*24*time.Hour).Format(tsLayout))
	if err == nil {
		for _, row := range prof.Rows {
			name := toString(row[0])
			v := by[name]
			if v == nil {
				v = &svc{Service: name, SDKs: []string{}}
				by[name] = v
				order = append(order, name)
			}
			v.Profiles, v.LastProfile = toInt(row[1]), toString(row[2])
		}
	}
	slices.Sort(order)
	out := make([]*svc, 0, len(order))
	for _, n := range order {
		out = append(out, by[n])
	}
	writeJSON(w, http.StatusOK, map[string]any{"services": out})
}

func (s *Server) handlePageSettings(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	var body struct {
		Hidden []string `json:"hidden_pages"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	hidden := []string{}
	for _, p := range body.Hidden {
		if !slices.Contains(hideablePages, p) {
			writeError(w, http.StatusBadRequest, "unknown page: "+p)
			return
		}
		if !slices.Contains(hidden, p) {
			hidden = append(hidden, p)
		}
	}
	list, _ := json.Marshal(hidden)
	if err := s.writeSettings(r.Context(), map[string]string{settingHiddenPages: string(list)}); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "pages.update", "", map[string]any{"hidden": hidden})
	s.handleSettings(w, r)
}
