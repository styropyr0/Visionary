package analytics

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/styropyr0/visionary/sdks/go/logrules"
)

// Settings → Logs. The logs.* values are read by the SDKs too (every 30
// seconds), so a change reaches every service without a redeploy:
//
//	GET  /api/settings/logs                  everything, with the syslog receiver's state
//	PUT  /api/settings/logs                  admins: replace the settings
//	POST /api/settings/logs/overrides        editors: add a level override for a while
//	DELETE /api/settings/logs/overrides/{i}  editors: remove one
//	POST /api/settings/logs/drops            editors: add a drop rule
//	DELETE /api/settings/logs/drops/{i}      editors: remove one
//	POST /api/settings/logs/rules/test       try a parse rule on a sample line

func (s *Server) applogsSettingsRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/settings/logs", s.authed(s.handleLogSettings))
	mux.Handle("PUT /api/settings/logs", s.authed(s.handlePutLogSettings))
	mux.Handle("POST /api/settings/logs/overrides", s.authed(s.handleAddLogOverride))
	mux.Handle("DELETE /api/settings/logs/overrides/{i}", s.authed(s.handleDeleteLogOverride))
	mux.Handle("POST /api/settings/logs/drops", s.authed(s.handleAddLogDrop))
	mux.Handle("DELETE /api/settings/logs/drops/{i}", s.authed(s.handleDeleteLogDrop))
	mux.Handle("POST /api/settings/logs/rules/test", s.authed(s.handleTestParseRule))
}

// logSettingsView is Settings → Logs as the page edits it.
type logSettingsView struct {
	Enabled     bool                `json:"enabled"`
	Level       string              `json:"level"`
	Buffer      bool                `json:"buffer"`
	BufferLevel string              `json:"buffer_level"`
	SlowMs      int                 `json:"slow_ms"`
	Rate        int                 `json:"rate"`
	Overrides   []logrules.Override `json:"overrides"`
	Drops       []logrules.Drop     `json:"drops"`
	Server      logServerSettings   `json:"server"`
}

func (s *Server) logSettingsView() logSettingsView {
	l := s.logRules()
	v := logSettingsView{
		Enabled: l.Enabled, Level: logrules.Name(l.Level), Buffer: l.Buffer, BufferLevel: logrules.Name(l.BufferLevel),
		SlowMs: int(l.Slow / time.Millisecond), Rate: l.Rate,
		Overrides: append([]logrules.Override{}, l.Overrides...), Drops: append([]logrules.Drop{}, l.Drops...),
		Server: s.logServer(),
	}
	if v.Server.Access.HiddenServices == nil {
		v.Server.Access.HiddenServices = []string{}
	}
	if v.Server.Access.MaskedFields == nil {
		v.Server.Access.MaskedFields = []string{}
	}
	return v
}

func (s *Server) handleLogSettings(w http.ResponseWriter, r *http.Request) {
	addr, errText := s.syslogStatus()
	now := time.Now()
	v := s.logSettingsView()
	expired := make([]bool, len(v.Overrides))
	for i, o := range v.Overrides {
		if t, ok := logrules.ParseTime(o.Until); ok && o.Until != "" && now.After(t) {
			expired[i] = true
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"settings": v, "expired": expired,
		"syslog":    map[string]string{"listening": addr, "error": errText},
		"can_admin": s.can(r, "admin"), "can_edit": s.can(r, "edit"),
	})
}

// validLogSettings checks a view and returns the stored values.
func validLogSettings(v logSettingsView) (map[string]string, error) {
	if logrules.ParseLevel(v.Level) == 0 || logrules.ParseLevel(v.BufferLevel) == 0 {
		return nil, fmt.Errorf("levels are trace, debug, info, warn, error or fatal")
	}
	if v.SlowMs < 1 || v.SlowMs > 600000 {
		return nil, fmt.Errorf("the slow threshold is 1 ms to 10 minutes")
	}
	if v.Rate < 0 || v.Rate > 10_000_000 || v.Server.Rate < 0 || v.Server.Rate > 100_000_000 {
		return nil, fmt.Errorf("the caps are 0 (none) to 10 million lines a minute")
	}
	if len(v.Overrides) > 100 || len(v.Drops) > 200 || len(v.Server.Rules) > 50 {
		return nil, fmt.Errorf("at most 100 overrides, 200 drop rules and 50 parse rules")
	}
	for i := range v.Overrides {
		o := &v.Overrides[i]
		o.Service, o.Route, o.User = strings.TrimSpace(o.Service), strings.TrimSpace(o.Route), strings.TrimSpace(o.User)
		if logrules.ParseLevel(o.Level) == 0 {
			return nil, fmt.Errorf("override %d needs a level", i+1)
		}
		if o.Until != "" {
			if _, ok := logrules.ParseTime(o.Until); !ok {
				return nil, fmt.Errorf("override %d: until must be a timestamp", i+1)
			}
		}
	}
	for i := range v.Drops {
		d := &v.Drops[i]
		if d.Level != "" && logrules.ParseLevel(d.Level) == 0 {
			return nil, fmt.Errorf("drop rule %d has an unknown level", i+1)
		}
		if strings.TrimSpace(d.Service+d.Logger+d.Contains+d.Level) == "" {
			return nil, fmt.Errorf("drop rule %d matches every line; give it a service, logger, text or level", i+1)
		}
	}
	for i := range v.Server.Rules {
		r := v.Server.Rules[i]
		if err := r.compile(); err != nil {
			return nil, fmt.Errorf("parse rule %q: %v", r.Name, err)
		}
	}
	if a := strings.TrimSpace(v.Server.Syslog); a != "" {
		if _, port, ok := strings.Cut(a, ":"); !ok || port == "" {
			return nil, fmt.Errorf("the syslog address is host:port or :port, e.g. :5514")
		}
	}
	if v.Server.Redact == nil {
		v.Server.Redact = []string{}
	}
	if v.Server.Rules == nil {
		v.Server.Rules = []parseRule{}
	}
	ov, _ := json.Marshal(nonNil(v.Overrides))
	dr, _ := json.Marshal(nonNil(v.Drops))
	srv, _ := json.Marshal(v.Server)
	return map[string]string{
		"logs.enabled": strconv.FormatBool(v.Enabled), "logs.level": logrules.Name(logrules.ParseLevel(v.Level)),
		"logs.buffer": strconv.FormatBool(v.Buffer), "logs.buffer_level": logrules.Name(logrules.ParseLevel(v.BufferLevel)),
		"logs.slow_ms": strconv.Itoa(v.SlowMs), "logs.rate": strconv.Itoa(v.Rate),
		"logs.overrides": string(ov), "logs.drop": string(dr), settingLogServer: string(srv),
	}, nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// saveLogSettings stores v and applies it to this server at once.
func (s *Server) saveLogSettings(w http.ResponseWriter, r *http.Request, v logSettingsView, action string, detail any) bool {
	kv, err := validLogSettings(v)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return false
	}
	if err := s.writeSettings(r.Context(), kv); err != nil {
		s.fail(w, err)
		return false
	}
	if err := s.refreshLive(r.Context()); err != nil {
		s.logger.Warn("logs: reload settings", "err", err)
	}
	s.syslogReapply()
	s.audit(r, action, "", detail)
	return true
}

func (s *Server) handlePutLogSettings(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	var v logSettingsView
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if s.saveLogSettings(w, r, v, "logs.settings", map[string]any{"level": v.Level, "buffer": v.Buffer, "rate": v.Rate}) {
		s.handleLogSettings(w, r)
	}
}

// handleAddLogOverride changes the level for a service, route or user for
// a number of minutes: "debug for /checkout for 15 minutes".
func (s *Server) handleAddLogOverride(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "edit") {
		return
	}
	var body struct {
		logrules.Override
		Minutes int `json:"minutes"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Minutes < 1 || body.Minutes > 7*24*60 {
		writeError(w, http.StatusBadRequest, "an override lasts 1 minute to 7 days")
		return
	}
	o := body.Override
	o.Until = time.Now().UTC().Add(time.Duration(body.Minutes) * time.Minute).Format(tsLayout)
	v := s.logSettingsView()
	// Drop expired overrides while here.
	now := time.Now()
	kept := v.Overrides[:0]
	for _, x := range v.Overrides {
		if t, ok := logrules.ParseTime(x.Until); x.Until == "" || (ok && now.Before(t)) {
			kept = append(kept, x)
		}
	}
	v.Overrides = append(kept, o)
	if s.saveLogSettings(w, r, v, "logs.override_add", map[string]any{"service": o.Service, "route": o.Route, "user": o.User, "level": o.Level, "until": o.Until}) {
		s.handleLogSettings(w, r)
	}
}

func (s *Server) handleDeleteLogOverride(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "edit") {
		return
	}
	v := s.logSettingsView()
	i, err := strconv.Atoi(r.PathValue("i"))
	if err != nil || i < 0 || i >= len(v.Overrides) {
		writeError(w, http.StatusNotFound, "no such override")
		return
	}
	o := v.Overrides[i]
	v.Overrides = append(v.Overrides[:i], v.Overrides[i+1:]...)
	if s.saveLogSettings(w, r, v, "logs.override_remove", map[string]any{"service": o.Service, "route": o.Route, "user": o.User, "level": o.Level}) {
		s.handleLogSettings(w, r)
	}
}

func (s *Server) handleAddLogDrop(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "edit") {
		return
	}
	var d logrules.Drop
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&d); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	v := s.logSettingsView()
	v.Drops = append(v.Drops, d)
	if s.saveLogSettings(w, r, v, "logs.drop_add", d) {
		s.handleLogSettings(w, r)
	}
}

func (s *Server) handleDeleteLogDrop(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "edit") {
		return
	}
	v := s.logSettingsView()
	i, err := strconv.Atoi(r.PathValue("i"))
	if err != nil || i < 0 || i >= len(v.Drops) {
		writeError(w, http.StatusNotFound, "no such drop rule")
		return
	}
	d := v.Drops[i]
	v.Drops = append(v.Drops[:i], v.Drops[i+1:]...)
	if s.saveLogSettings(w, r, v, "logs.drop_remove", d) {
		s.handleLogSettings(w, r)
	}
}

// handleTestParseRule shows what a parse rule makes of a sample line.
func (s *Server) handleTestParseRule(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Pattern string `json:"pattern"`
		Sample  string `json:"sample"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	rule := parseRule{Name: "test", Pattern: body.Pattern}
	if err := rule.compile(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	l := &logLine{message: body.Sample, attrs: map[string]any{}}
	matched := applyParseRules(l, []parseRule{rule})
	delete(l.attrs, "parsed_by")
	out := map[string]any{"matched": matched, "fields": l.attrs, "message": l.message}
	if l.sev > 0 {
		out["level"] = logrules.Name(l.sev)
	}
	if !l.at.IsZero() {
		out["time"] = l.at.UTC().Format(tsLayout)
	}
	writeJSON(w, http.StatusOK, out)
}
