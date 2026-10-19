package analytics

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (s *Server) alertsRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/alerts/meta", s.authed(s.handleAlertsMeta))
	mux.Handle("GET /api/alerts/rules", s.authed(s.handleListRules))
	mux.Handle("POST /api/alerts/rules", s.authed(s.handleCreateRule))
	mux.Handle("PUT /api/alerts/rules/{id}", s.authed(s.handleUpdateRule))
	mux.Handle("DELETE /api/alerts/rules/{id}", s.authed(s.handleDeleteRule))
	mux.Handle("POST /api/alerts/rules/{id}/mute", s.authed(s.handleMuteRule))
	mux.Handle("POST /api/alerts/rules/{id}/evaluate", s.authed(s.handleEvaluateRule))
	mux.Handle("POST /api/alerts/preview", s.authed(s.handlePreviewRule))
	mux.Handle("GET /api/alerts/events", s.authed(s.handleListEvents))
	mux.Handle("GET /api/alerts/slos", s.authed(s.handleListSLOs))
	mux.Handle("GET /api/alerts/channels", s.authed(s.handleListChannels))
	mux.Handle("POST /api/alerts/channels", s.authed(s.handleCreateChannel))
	mux.Handle("PUT /api/alerts/channels/{id}", s.authed(s.handleUpdateChannel))
	mux.Handle("DELETE /api/alerts/channels/{id}", s.authed(s.handleDeleteChannel))
	mux.Handle("POST /api/alerts/channels/{id}/test", s.authed(s.handleTestChannel))
	mux.Handle("POST /api/alerts/channels/{id}/digest", s.authed(s.handleSendDigest))
	mux.Handle("GET /api/alerts/digest", s.authed(s.handleDigestPreview))
}

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

func (s *Server) forbidden(w http.ResponseWriter) {
	writeError(w, http.StatusForbidden, "your role cannot change alerts")
}

func (s *Server) handleAlertsMeta(w http.ResponseWriter, r *http.Request) {
	smtp, ok := smtpFromEnv()
	instances, _ := s.alertEngine().knownInstances(r.Context(), time.Now().UTC())
	names, services := []string{}, []string{}
	seen := map[string]bool{}
	for _, in := range instances {
		names = append(names, in.Instance)
		if in.Service != "" && !seen[in.Service] {
			seen[in.Service] = true
			services = append(services, in.Service)
		}
	}
	tz := digestTZ()
	writeJSON(w, http.StatusOK, map[string]any{
		"metrics":         s.allMetrics(),
		"apdex_ms":        s.cfg.ApdexMs,
		"slo_target":      s.cfg.SLOTarget,
		"llm":             s.llm != nil,
		"smtp_configured": ok,
		"smtp_from":       map[bool]string{true: smtp.From}[ok],
		"digest_tz":       tzName(tz),
		// Lets the page show each digest's time in the viewer's own zone.
		"digest_offset_min": tzOffsetMinutes(tz),
		"interval_s":      int(s.alertEngine().interval.Seconds()),
		"routes":          s.knownRoutes(r.Context()),
		"instances":       names,
		"services":        services,
		"can_edit":        s.can(r, "edit"),
	})
}

// ruleView is a rule with its live status for the list.
type ruleView struct {
	alertRule
	Status ruleStatus `json:"status"`
}

type ruleStatus struct {
	State       string         `json:"state"` // ok, pending, firing, resolved, or "" before the first evaluation
	Since       string         `json:"since,omitempty"`
	Value       *float64       `json:"value"`
	Threshold   *float64       `json:"threshold"`
	Message     string         `json:"message"`
	EvaluatedAt string         `json:"evaluated_at,omitempty"`
	Error       string         `json:"error,omitempty"`
	Firing      int            `json:"firing"`
	Series      []*seriesState `json:"series"`
	Recent      [][2]float64   `json:"recent"`
}

var stateRank = map[string]int{"firing": 4, "pending": 3, "resolved": 2, "ok": 1}

func (s *Server) ruleViews(ctx context.Context, rules []alertRule) ([]ruleView, error) {
	states, err := s.ruleStates(ctx, 0)
	if err != nil {
		return nil, err
	}
	e := s.alertEngine()
	out := make([]ruleView, 0, len(rules))
	for _, rule := range rules {
		v := ruleView{alertRule: rule, Status: ruleStatus{Series: states[rule.ID], Error: e.errFor(rule.ID), Recent: [][2]float64{}}}
		if v.Status.Series == nil {
			v.Status.Series = []*seriesState{}
		}
		var worst *seriesState
		for _, st := range v.Status.Series {
			if st.State == "firing" {
				v.Status.Firing++
			}
			if worst == nil || stateRank[st.State] > stateRank[worst.State] {
				worst = st
			}
		}
		if worst != nil {
			v.Status.State, v.Status.Since, v.Status.Value, v.Status.Threshold = worst.State, worst.Since, worst.Value, worst.Threshold
			v.Status.Message, v.Status.EvaluatedAt = worst.Message, worst.EvaluatedAt
			if worst.Recent != nil {
				v.Status.Recent = worst.Recent
			}
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Server) handleListRules(w http.ResponseWriter, r *http.Request) {
	rules, err := s.listRules(r.Context(), false)
	if err != nil {
		s.fail(w, err)
		return
	}
	views, err := s.ruleViews(r.Context(), rules)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": views})
}

type ruleInput struct {
	Name     string   `json:"name"`
	Kind     string   `json:"kind"`
	Severity string   `json:"severity"`
	Channels []int64  `json:"channels"`
	Runbook  string   `json:"runbook"`
	Enabled  *bool    `json:"enabled"`
	Spec     ruleSpec `json:"spec"`
}

var (
	severities = map[string]bool{"info": true, "warning": true, "critical": true}
	operators  = map[string]bool{">": true, "<": true, ">=": true, "<=": true}
)

// checkRule validates and normalizes a rule. Errors are for the person
// editing it.
func (s *Server) checkRule(ctx context.Context, in ruleInput) (alertRule, error) {
	rule := alertRule{
		Name: strings.TrimSpace(in.Name), Kind: fallback(in.Kind, "threshold"), Severity: fallback(in.Severity, "warning"),
		Channels: in.Channels, Runbook: strings.TrimSpace(in.Runbook), Enabled: in.Enabled == nil || *in.Enabled, Spec: in.Spec,
	}
	if rule.Name == "" || len(rule.Name) > maxTitleLen {
		return rule, badInput("name must be 1–120 characters")
	}
	if !severities[rule.Severity] {
		return rule, badInput("severity must be info, warning or critical")
	}
	if len(rule.Runbook) > 2000 {
		return rule, badInput("runbook note must be under 2000 characters")
	}
	if rule.Channels == nil {
		rule.Channels = []int64{}
	}
	if len(rule.Channels) > 0 {
		chans, err := s.listChannels(ctx)
		if err != nil {
			return rule, err
		}
		have := map[int64]bool{}
		for _, c := range chans {
			have[c.ID] = true
		}
		for _, id := range rule.Channels {
			if !have[id] {
				return rule, badInput("unknown channel " + strconv.FormatInt(id, 10))
			}
		}
	}
	sp := &rule.Spec
	if err := checkScope(sp.Scope); err != nil {
		return rule, err
	}
	for _, d := range []struct{ name, v string }{{"window", sp.Window}, {"for", sp.For}, {"repeat", sp.Repeat}} {
		if _, err := parseSpan(d.v); err != nil {
			return rule, badInput(d.name + ": " + err.Error())
		}
	}
	if f, _ := parseSpan(sp.For); f > 24*time.Hour {
		return rule, badInput(`"for" can be at most 24h`)
	}
	if rp, _ := parseSpan(sp.Repeat); rp != 0 && (rp < 5*time.Minute || rp > 7*24*time.Hour) {
		return rule, badInput("repeat must be between 5m and 7d, or empty")
	}

	switch rule.Kind {
	case "threshold":
		if !operators[sp.Op] {
			return rule, badInput("condition must be >, <, >= or <=")
		}
		if w, _ := parseSpan(sp.Window); w != 0 && (w < time.Minute || w > 24*time.Hour) {
			return rule, badInput("window must be between 1m and 24h")
		}
		sp.Window = fallback(sp.Window, "5m")
		if sp.Metric == "custom" {
			checked, err := validateReadOnly(sp.SQL)
			if err != nil {
				return rule, &queryError{err}
			}
			sp.SQL = checked
			sp.Scope = alertScope{}
		} else if _, ok := s.metricByID(sp.Metric); !ok {
			return rule, badInput("pick a metric")
		} else {
			sp.SQL = ""
		}
		sp.Sensitivity, sp.Direction, sp.Target = "", "", 0
	case "anomaly":
		m, ok := s.metricByID(sp.Metric)
		if !ok || !m.Anomaly {
			return rule, badInput("anomaly rules watch request rate, p95 latency, 5xx rate or host CPU")
		}
		sp.Sensitivity = fallback(sp.Sensitivity, "medium")
		if _, ok := sensitivityScore[sp.Sensitivity]; !ok {
			return rule, badInput("sensitivity must be low, medium or high")
		}
		switch sp.Direction {
		case "", "up", "down", "both":
		default:
			return rule, badInput("direction must be up, down or both")
		}
		sp.Window = fallback(sp.Window, "15m")
		if w, _ := parseSpan(sp.Window); w < 5*time.Minute || w > 2*time.Hour {
			return rule, badInput("anomaly window must be between 5m and 2h")
		}
		sp.SQL, sp.Op, sp.Threshold, sp.Target = "", "", 0, 0
	case "slo":
		if sp.Target == 0 {
			sp.Target = s.cfg.SLOTarget
		}
		if sp.Target < 50 || sp.Target >= 100 {
			return rule, badInput("SLO target must be at least 50% and below 100%")
		}
		sp.Metric, sp.SQL, sp.Op, sp.Threshold, sp.Window, sp.Sensitivity, sp.Direction = "slo", "", "", 0, "", "", ""
		sp.Scope.Instance, sp.Scope.Check = "", ""
	default:
		return rule, badInput("kind must be threshold, anomaly or slo")
	}
	return rule, nil
}

func checkScope(sc alertScope) error {
	if sc.Method != "" && !methodName.MatchString(sc.Method) {
		return badInput("method must be an HTTP method such as GET")
	}
	if sc.Instance != "" && !instanceName.MatchString(sc.Instance) {
		return badInput("invalid instance name")
	}
	if sc.Container != "" && !instanceName.MatchString(sc.Container) {
		return badInput("invalid container name")
	}
	if len(sc.Route) > 512 || len(sc.Service) > 128 || len(sc.Check) > 128 {
		return badInput("scope value is too long")
	}
	return checkSecurityScope(sc)
}

func (s *Server) handleCreateRule(w http.ResponseWriter, r *http.Request) {
	if !s.can(r, "edit") {
		s.forbidden(w)
		return
	}
	var in ruleInput
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rule, err := s.checkRule(r.Context(), in)
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.insertRule(r.Context(), &rule); err != nil {
		s.fail(w, err)
		return
	}
	s.alertEngine().invalidate(rule.ID)
	s.audit(r, "alert.rule.create", rule.Name, map[string]any{"id": rule.ID, "kind": rule.Kind, "metric": rule.Spec.Metric})
	writeJSON(w, http.StatusCreated, rule)
}

// badInput is a validation error, answered with 400 and its message.
func badInput(msg string) error { return &queryError{errors.New(msg)} }

func (s *Server) handleUpdateRule(w http.ResponseWriter, r *http.Request) {
	if !s.can(r, "edit") {
		s.forbidden(w)
		return
	}
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid rule id")
		return
	}
	old, err := s.getRule(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if old == nil {
		writeError(w, http.StatusNotFound, "rule not found")
		return
	}
	var in ruleInput
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rule, err := s.checkRule(r.Context(), in)
	if err != nil {
		s.fail(w, err)
		return
	}
	rule.ID, rule.CreatedAt, rule.MutedUntil = id, old.CreatedAt, old.MutedUntil
	if err := s.updateRule(r.Context(), &rule); err != nil {
		s.fail(w, err)
		return
	}
	// A changed condition starts from a clean state rather than inheriting
	// readings of something else; renames and channel edits keep it.
	if mustJSON(old.Spec) != mustJSON(rule.Spec) || old.Kind != rule.Kind {
		if err := s.clearRuleState(r.Context(), id); err != nil {
			s.fail(w, err)
			return
		}
	}
	s.alertEngine().invalidate(id)
	s.audit(r, "alert.rule.update", rule.Name, map[string]any{"id": id, "kind": rule.Kind, "metric": rule.Spec.Metric, "enabled": rule.Enabled})
	writeJSON(w, http.StatusOK, rule)
}

func (s *Server) handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	if !s.can(r, "edit") {
		s.forbidden(w)
		return
	}
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid rule id")
		return
	}
	old, err := s.getRule(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if old == nil {
		writeError(w, http.StatusNotFound, "rule not found")
		return
	}
	if err := s.deleteRule(r.Context(), id); err != nil {
		s.fail(w, err)
		return
	}
	s.alertEngine().invalidate(0)
	s.audit(r, "alert.rule.delete", old.Name, map[string]any{"id": id})
	w.WriteHeader(http.StatusNoContent)
}

// handleMuteRule snoozes a rule until a time (or for a number of minutes);
// an empty body unmutes it.
func (s *Server) handleMuteRule(w http.ResponseWriter, r *http.Request) {
	if !s.can(r, "edit") {
		s.forbidden(w)
		return
	}
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid rule id")
		return
	}
	var body struct {
		Until   string `json:"until"`
		Minutes int    `json:"minutes"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rule, err := s.getRule(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if rule == nil {
		writeError(w, http.StatusNotFound, "rule not found")
		return
	}
	until := ""
	switch {
	case body.Minutes > 0:
		if body.Minutes > 60*24*90 {
			writeError(w, http.StatusBadRequest, "mute for at most 90 days")
			return
		}
		until = time.Now().UTC().Add(time.Duration(body.Minutes) * time.Minute).Format(tsLayout)
	case body.Until != "":
		t, err := time.Parse(time.RFC3339Nano, body.Until)
		if err != nil || !t.After(time.Now()) {
			writeError(w, http.StatusBadRequest, "until must be a future RFC 3339 time")
			return
		}
		until = t.UTC().Format(tsLayout)
	}
	if err := s.db.Exec(r.Context(), `UPDATE alert_rules SET muted_until = ?, updated_at = ? WHERE id = ?`,
		until, time.Now().UTC().Format(tsLayout), id); err != nil {
		s.fail(w, err)
		return
	}
	s.alertEngine().invalidate(0)
	action := "alert.rule.mute"
	if until == "" {
		action = "alert.rule.unmute"
	}
	s.audit(r, action, rule.Name, map[string]any{"id": id, "until": until})
	writeJSON(w, http.StatusOK, map[string]string{"muted_until": until})
}

// handleEvaluateRule evaluates a rule now, as the scheduler would.
func (s *Server) handleEvaluateRule(w http.ResponseWriter, r *http.Request) {
	if !s.can(r, "edit") {
		s.forbidden(w)
		return
	}
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid rule id")
		return
	}
	rule, err := s.getRule(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if rule == nil {
		writeError(w, http.StatusNotFound, "rule not found")
		return
	}
	if err := s.alertEngine().evaluate(r.Context(), *rule, time.Now().UTC()); err != nil {
		s.fail(w, err)
		return
	}
	views, err := s.ruleViews(r.Context(), []alertRule{*rule})
	if err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "alert.rule.evaluate", rule.Name, map[string]any{"id": id})
	writeJSON(w, http.StatusOK, views[0])
}

func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	tr, err := parseRange(q, s.cfg.MaxRangeDays)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	f := eventFilter{From: tr.From, To: tr.To, Severity: q.Get("severity"), State: q.Get("state")}
	f.RuleID, _ = strconv.ParseInt(q.Get("rule_id"), 10, 64)
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	if f.Severity != "" && !severities[f.Severity] || f.State != "" && f.State != "firing" && f.State != "resolved" {
		writeError(w, http.StatusBadRequest, "invalid filter")
		return
	}
	events, err := s.listEvents(r.Context(), f)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

func (s *Server) handleListSLOs(w http.ResponseWriter, r *http.Request) {
	rules, err := s.listRules(r.Context(), false)
	if err != nil {
		s.fail(w, err)
		return
	}
	var slos []alertRule
	for _, rule := range rules {
		if rule.Kind == "slo" {
			slos = append(slos, rule)
		}
	}
	views, err := s.ruleViews(r.Context(), slos)
	if err != nil {
		s.fail(w, err)
		return
	}
	type item struct {
		Rule   ruleView   `json:"rule"`
		Status *sloStatus `json:"status"`
		Error  string     `json:"error,omitempty"`
	}
	out := make([]item, 0, len(views))
	now := time.Now().UTC()
	for _, v := range views {
		st, err := s.sloStatus(r.Context(), v.alertRule, now)
		it := item{Rule: v, Status: st}
		if err != nil {
			it.Error = userMessage(dbErr(err))
		}
		out = append(out, it)
	}
	writeJSON(w, http.StatusOK, map[string]any{"slos": out, "apdex_ms": s.cfg.ApdexMs})
}
