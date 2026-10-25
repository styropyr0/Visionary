package analytics

import (
	"context"
	"net/http"
	"strings"
	"time"
)

type channelInput struct {
	Name          string   `json:"name"`
	Kind          string   `json:"kind"`
	URL           string   `json:"url"`
	Secret        string   `json:"secret"`
	ClearSecret   bool     `json:"clear_secret"`
	To            []string `json:"to"`
	ChatID        string   `json:"chat_id"`
	AccountSID    string   `json:"account_sid"`
	From          string   `json:"from"`
	Region        string   `json:"region"`
	DigestEnabled bool     `json:"digest_enabled"`
	DigestWeekday *int     `json:"digest_weekday"`
	DigestHour    *int     `json:"digest_hour"`
}

// apply copies the input onto c. An empty URL or secret keeps the stored
// one, because the client only ever sees masked values.
func (in channelInput) apply(c *alertChannel) error {
	c.Name = strings.TrimSpace(in.Name)
	if c.Name == "" || len(c.Name) > maxTitleLen {
		return badInput("name must be 1–120 characters")
	}
	if in.Kind != "" {
		if c.ID != 0 && in.Kind != c.Kind {
			// A Slack URL is no use to an email channel; make a new channel.
			return badInput("a channel's kind cannot change")
		}
		c.Kind = in.Kind
	}
	if !channelKinds[c.Kind] {
		return badInput("unknown channel kind")
	}
	if u := strings.TrimSpace(in.URL); u != "" {
		c.Config.URL = u
	}
	if in.ClearSecret {
		c.Config.Secret = ""
	}
	if sec := strings.TrimSpace(in.Secret); sec != "" {
		c.Config.Secret = sec
	}
	if !urlKinds[c.Kind] {
		c.Config.URL = ""
	}
	if !secretKinds[c.Kind] {
		c.Config.Secret = ""
	}
	c.Config.To = nil
	if c.Kind == "email" || c.Kind == "sms" {
		for _, to := range in.To {
			if to = strings.TrimSpace(to); to != "" {
				c.Config.To = append(c.Config.To, to)
			}
		}
	}
	// As with the URL, a field left empty keeps the stored value, so a
	// partial update (the digest settings) leaves the rest alone.
	keep := func(stored *string, in string, used bool) {
		if !used {
			*stored = ""
		} else if v := strings.TrimSpace(in); v != "" {
			*stored = v
		}
	}
	keep(&c.Config.ChatID, in.ChatID, c.Kind == "telegram")
	keep(&c.Config.AccountSID, in.AccountSID, c.Kind == "sms")
	keep(&c.Config.From, in.From, c.Kind == "sms")
	keep(&c.Config.Region, in.Region, c.Kind == "opsgenie")
	if err := validateChannelConfig(c.Kind, c.Config); err != nil {
		return badInput(err.Error())
	}
	if in.DigestEnabled && noDigestKinds[c.Kind] {
		return badInput("weekly digests go to chat, webhook or email channels, not paging or SMS")
	}
	c.DigestEnabled = in.DigestEnabled
	if in.DigestWeekday != nil {
		c.DigestWeekday = *in.DigestWeekday
	}
	if in.DigestHour != nil {
		c.DigestHour = *in.DigestHour
	}
	if c.DigestWeekday < 0 || c.DigestWeekday > 6 || c.DigestHour < 0 || c.DigestHour > 23 {
		return badInput("digest weekday must be 0–6 and hour 0–23")
	}
	return nil
}

func (s *Server) handleListChannels(w http.ResponseWriter, r *http.Request) {
	chans, err := s.listChannels(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]channelView, 0, len(chans))
	for _, c := range chans {
		out = append(out, c.view())
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": out})
}

func (s *Server) handleCreateChannel(w http.ResponseWriter, r *http.Request) {
	if !s.can(r, "edit") {
		s.forbidden(w)
		return
	}
	var in channelInput
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	c := alertChannel{DigestWeekday: 1, DigestHour: 9}
	if err := in.apply(&c); err != nil {
		s.fail(w, err)
		return
	}
	if err := s.saveChannel(r.Context(), &c); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "alert.channel.create", c.Name, map[string]any{"id": c.ID, "kind": c.Kind})
	writeJSON(w, http.StatusCreated, c.view())
}

func (s *Server) handleUpdateChannel(w http.ResponseWriter, r *http.Request) {
	if !s.can(r, "edit") {
		s.forbidden(w)
		return
	}
	c, ok := s.channelFromPath(w, r)
	if !ok {
		return
	}
	var in channelInput
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := in.apply(c); err != nil {
		s.fail(w, err)
		return
	}
	if err := s.saveChannel(r.Context(), c); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "alert.channel.update", c.Name, map[string]any{"id": c.ID, "kind": c.Kind, "digest_enabled": c.DigestEnabled,
		"url_changed": in.URL != "", "secret_changed": in.Secret != "" || in.ClearSecret})
	writeJSON(w, http.StatusOK, c.view())
}

func (s *Server) handleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	if !s.can(r, "edit") {
		s.forbidden(w)
		return
	}
	c, ok := s.channelFromPath(w, r)
	if !ok {
		return
	}
	if err := s.db.Exec(r.Context(), `DELETE FROM alert_channels WHERE id = ?`, c.ID); err != nil {
		s.fail(w, err)
		return
	}
	// Rules keep no reference to a channel that is gone.
	changed, err := s.dropChannelFromRules(r.Context(), c.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.alertEngine().invalidate(0)
	s.audit(r, "alert.channel.delete", c.Name, map[string]any{"id": c.ID, "rules_changed": changed})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) channelFromPath(w http.ResponseWriter, r *http.Request) (*alertChannel, bool) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid channel id")
		return nil, false
	}
	c, err := s.getChannel(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return nil, false
	}
	if c == nil {
		writeError(w, http.StatusNotFound, "channel not found")
		return nil, false
	}
	return c, true
}

// handleTestChannel sends a sample alert and reports the channel's answer.
// Sending is a change people can see, so it needs edit rights.
func (s *Server) handleTestChannel(w http.ResponseWriter, r *http.Request) {
	if !s.can(r, "edit") {
		s.forbidden(w)
		return
	}
	c, ok := s.channelFromPath(w, r)
	if !ok {
		return
	}
	n := notice{
		Event: "test", Title: "[TEST] Visionary notification", Severity: "info", At: time.Now().UTC(),
		Lines:  []string{"This is a test from Visionary. Alerts for rules that use the channel \"" + c.Name + "\" arrive here."},
		Fields: [][2]string{{"Channel", c.Name}, {"Kind", c.Kind}},
	}
	n.Payload = map[string]any{"event": "test", "channel": map[string]any{"id": c.ID, "name": c.Name}, "at": n.At.Format(tsLayout)}
	err := s.sendNotice(r.Context(), *c, n)
	s.audit(r, "alert.channel.test", c.Name, map[string]any{"id": c.ID, "ok": err == nil})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSendDigest(w http.ResponseWriter, r *http.Request) {
	if !s.can(r, "edit") {
		s.forbidden(w)
		return
	}
	c, ok := s.channelFromPath(w, r)
	if !ok {
		return
	}
	err := s.sendDigest(r.Context(), *c, time.Now().UTC())
	s.audit(r, "alert.digest.send", c.Name, map[string]any{"id": c.ID, "ok": err == nil})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": userMessage(dbErr(err))})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleDigestSummary asks the analyst for this week's summary, as the
// digest itself would.
func (s *Server) handleDigestSummary(w http.ResponseWriter, r *http.Request) {
	if s.llm() == nil {
		writeError(w, http.StatusNotFound, "no model is configured")
		return
	}
	d, err := s.digestData(r.Context(), time.Now().UTC())
	if err != nil {
		s.fail(w, err)
		return
	}
	text := s.digestSummary(r.Context(), d)
	if text == "" {
		writeError(w, http.StatusBadGateway, "the model did not return a summary; try again")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"summary": text})
}

// handleDigestEmail is this week's digest as the email renders it, without
// the summary (a model call), for the page to preview in a sandboxed frame.
func (s *Server) handleDigestEmail(w http.ResponseWriter, r *http.Request) {
	d, err := s.digestData(r.Context(), time.Now().UTC())
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	_, _ = w.Write([]byte(s.digestHTML(d, "")))
}

// handleDigestPreview shows what this week's digest would say.
func (s *Server) handleDigestPreview(w http.ResponseWriter, r *http.Request) {
	d, err := s.digestData(r.Context(), time.Now().UTC())
	if err != nil {
		s.fail(w, dbErr(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": d, "lines": digestLines(d), "time_zone": tzName(s.digestTZ()), "llm": s.llm() != nil})
}

// dropChannelFromRules removes a channel from every rule's channels and
// escalation steps, and reports how many rules changed.
func (s *Server) dropChannelFromRules(ctx context.Context, id int64) (int, error) {
	rules, err := s.listRules(ctx, false)
	if err != nil {
		return 0, err
	}
	without := func(ids []int64) ([]int64, bool) {
		out := make([]int64, 0, len(ids))
		for _, x := range ids {
			if x != id {
				out = append(out, x)
			}
		}
		return out, len(out) != len(ids)
	}
	n := 0
	for _, r := range rules {
		var dropped bool
		r.Channels, dropped = without(r.Channels)
		for i := range r.Spec.Escalation {
			var d bool
			r.Spec.Escalation[i].Channels, d = without(r.Spec.Escalation[i].Channels)
			dropped = dropped || d
		}
		if !dropped {
			continue
		}
		if err := s.updateRule(ctx, &r); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
