package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Detection rules: the team's own detectors over requests, app logs, security
// events or the audit log. A rule writes what it finds as a finding with source
// 'rule' (a signal), so triage, suppressions, alerts and the report all apply.
//
//	security_rules.go          the rule, its checks, the store and the API
//	security_rules_sql.go      sources, fields and filters as SQL
//	security_rules_eval.go     the background job, each logic, signals
//	security_rules_preview.go  testing a rule against the last 24 hours
//	security_rules_library.go  the built-in templates and MITRE ATT&CK names

type detectionRule struct {
	ID          int64             `json:"id"`
	Name        string            `json:"name"`
	Kind        string            `json:"kind"`
	Description string            `json:"description,omitempty"`
	Enabled     bool              `json:"enabled"`
	Template    string            `json:"template,omitempty"`
	Severity    string            `json:"severity"`
	Tactic      string            `json:"tactic,omitempty"`
	Technique   string            `json:"technique,omitempty"`
	Source      string            `json:"source"`
	Filter      map[string]string `json:"filter,omitempty"`
	// Logic is threshold, new_value, sequence or anomaly.
	Logic     string   `json:"logic"`
	GroupBy   []string `json:"group_by,omitempty"`
	Window    string   `json:"window"`
	Threshold int      `json:"threshold"`
	// Distinct counts different values of a field instead of rows.
	Distinct string `json:"distinct,omitempty"`
	// Field and Learn: a new_value rule flags a Field value its group has not
	// had in the Learn period before.
	Field string `json:"field,omitempty"`
	Learn string `json:"learn,omitempty"`
	// Then is a sequence's second step: Threshold matches of Filter, then one
	// of Then, by the same group within Window.
	Then map[string]string `json:"then,omitempty"`
	// Factor: an anomaly is a group at Factor times its usual count.
	Factor    float64           `json:"factor,omitempty"`
	Suppress  []ruleSuppression `json:"suppress,omitempty"`
	Channels  []int64           `json:"channels,omitempty"`
	Runbook   string            `json:"runbook,omitempty"`
	CreatedAt string            `json:"created_at,omitempty"`
	CreatedBy string            `json:"created_by,omitempty"`
	UpdatedAt string            `json:"updated_at,omitempty"`
}

// ruleSuppression skips a rule's signals whose group (or new value) has
// Field = Value, until Until when set.
type ruleSuppression struct {
	Field string `json:"field"`
	Value string `json:"value"`
	Until string `json:"until,omitempty"`
	Note  string `json:"note,omitempty"`
	By    string `json:"by,omitempty"`
}

const (
	maxRules         = 100
	maxRuleWindow    = 24 * time.Hour
	maxRuleLearn     = 30 * 24 * time.Hour
	maxRuleSuppress  = 100
	defaultRuleLearn = 7 * 24 * time.Hour
)

var (
	ruleLogics    = map[string]bool{"threshold": true, "new_value": true, "sequence": true, "anomaly": true}
	ruleTactic    = regexp.MustCompile(`^TA\d{4}$`)
	ruleTechnique = regexp.MustCompile(`^T\d{4}(\.\d{3})?$`)
	ruleSlug      = regexp.MustCompile(`[^a-z0-9]+`)
)

func (r *detectionRule) window() time.Duration {
	d, err := parseSpan(r.Window)
	if err != nil || d < time.Minute {
		return 10 * time.Minute
	}
	return min(d, maxRuleWindow)
}

func (r *detectionRule) learn() time.Duration {
	d, err := parseSpan(r.Learn)
	if err != nil || d < time.Hour {
		return defaultRuleLearn
	}
	return min(d, maxRuleLearn)
}

// every is how often a rule runs: a fifth of its window, between one and ten
// minutes, so long windows are not re-read every minute.
func (r *detectionRule) every() time.Duration {
	return min(max(r.window()/5, time.Minute), 10*time.Minute)
}

// dedupe is how long one group's signal keeps growing before a new one opens.
func (r *detectionRule) dedupe() time.Duration { return max(r.window(), 10*time.Minute) }

func (s *Server) checkDetectionRule(r *detectionRule) error {
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" || len(r.Name) > 120 {
		return badInput("a rule needs a name of up to 120 characters")
	}
	r.Description = clip(strings.TrimSpace(r.Description), 1000)
	r.Runbook = clip(strings.TrimSpace(r.Runbook), 500)
	r.Severity = fallback(r.Severity, "warning")
	if severityRank[r.Severity] == 0 {
		return badInput("severity must be info, warning or critical")
	}
	r.Tactic, r.Technique = strings.ToUpper(strings.TrimSpace(r.Tactic)), strings.ToUpper(strings.TrimSpace(r.Technique))
	if r.Tactic != "" && !ruleTactic.MatchString(r.Tactic) {
		return badInput("the MITRE ATT&CK tactic is an id like TA0006")
	}
	if r.Technique != "" && !ruleTechnique.MatchString(r.Technique) {
		return badInput("the MITRE ATT&CK technique is an id like T1110 or T1110.004")
	}
	srcs := s.q.ruleSources()
	if _, ok := srcs[r.Source]; !ok {
		return badInput("source must be requests, logs, events or audit")
	}
	if !ruleLogics[r.Logic] {
		return badInput("logic must be threshold, new_value, sequence or anomaly")
	}
	if d, err := parseSpan(r.Window); err != nil || d < time.Minute || d > maxRuleWindow {
		return badInput("the window is a duration from 1m to 24h, like 10m")
	}
	if r.Learn != "" {
		if d, err := parseSpan(r.Learn); err != nil || d < time.Hour || d > maxRuleLearn {
			return badInput("learning is a duration from 1h to 30d, like 7d")
		}
	}
	if len(r.GroupBy) > 3 {
		return badInput("group by up to three fields")
	}
	field := func(f, what string) error {
		if s.q.fieldExpr(r.Source, f) == "" {
			return badInput(fmt.Sprintf("%s has no field %q to %s", srcs[r.Source].label, f, what))
		}
		if f == "country" && what != "find new values of" {
			return badInput("country can only be the field a new-value rule watches")
		}
		return nil
	}
	seen := map[string]bool{}
	groups := r.GroupBy[:0]
	for _, g := range r.GroupBy {
		if g = strings.TrimSpace(g); g == "" || seen[g] {
			continue
		}
		if err := field(g, "group by"); err != nil {
			return err
		}
		seen[g] = true
		groups = append(groups, g)
	}
	r.GroupBy = groups
	if r.Distinct != "" {
		if err := field(r.Distinct, "count"); err != nil {
			return err
		}
	}
	r.Filter, r.Then = tidyFilter(r.Filter), tidyFilter(r.Then)
	tr := timeRange{From: time.Now().Add(-time.Hour), To: time.Now()}
	if _, _, err := s.filterSQL(r.Source, r.Filter, tr); err != nil {
		return err
	}
	switch r.Logic {
	case "threshold":
		if r.Threshold < 1 {
			return badInput("the threshold is a count of 1 or more")
		}
	case "new_value":
		if r.Field == "" {
			return badInput("a new-value rule needs the field to watch")
		}
		if err := field(r.Field, "find new values of"); err != nil {
			return err
		}
		if seen[r.Field] {
			return badInput("the field to watch can't also be a group")
		}
		r.Distinct = ""
	case "sequence":
		if r.Threshold < 1 {
			r.Threshold = 1
		}
		if len(r.Then) == 0 {
			return badInput("a sequence needs a second step: what happens after")
		}
		if _, _, err := s.filterSQL(r.Source, r.Then, tr); err != nil {
			return err
		}
		if len(r.GroupBy) == 0 {
			return badInput("a sequence needs a field to group by, so both steps are by the same IP, user or host")
		}
		r.Distinct = ""
	case "anomaly":
		if r.Factor == 0 {
			r.Factor = 3
		}
		if r.Factor < 1.5 || r.Factor > 100 {
			return badInput("the anomaly factor is between 1.5 and 100")
		}
		if r.Threshold < 1 {
			r.Threshold = 1
		}
	}
	if r.Logic != "sequence" {
		r.Then = nil
	}
	if r.Logic != "new_value" {
		r.Field, r.Learn = "", ""
	}
	if r.Logic != "anomaly" {
		r.Factor = 0
	}
	if len(r.Suppress) > maxRuleSuppress {
		return badInput(fmt.Sprintf("a rule keeps up to %d suppressions", maxRuleSuppress))
	}
	for i := range r.Suppress {
		sp := &r.Suppress[i]
		sp.Field, sp.Value = strings.TrimSpace(sp.Field), strings.TrimSpace(sp.Value)
		if sp.Field == "" || sp.Value == "" || len(sp.Value) > 255 {
			return badInput("a suppression needs a field and a value")
		}
		if sp.Field != r.Field && !seen[sp.Field] {
			return badInput(fmt.Sprintf("suppressions match the rule's group fields; %q is not one", sp.Field))
		}
		if sp.Until != "" {
			t, err := time.Parse(time.RFC3339, sp.Until)
			if err != nil {
				return badInput("a suppression ends at an RFC 3339 time")
			}
			sp.Until = t.UTC().Format(tsLayout)
		}
		sp.Note = clip(strings.TrimSpace(sp.Note), 200)
	}
	if len(r.Channels) > 20 {
		return badInput("notify up to 20 channels")
	}
	return nil
}

func tidyFilter(f map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range f {
		if v = strings.TrimSpace(v); v != "" {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (r *detectionRule) suppressedBy(values map[string]string, now time.Time) *ruleSuppression {
	ts := now.UTC().Format(tsLayout)
	for i, sp := range r.Suppress {
		if sp.Until != "" && sp.Until <= ts {
			continue
		}
		if v, ok := values[sp.Field]; ok && strings.EqualFold(v, sp.Value) {
			return &r.Suppress[i]
		}
	}
	return nil
}

func (s *Server) listDetectionRules(ctx context.Context, onlyEnabled bool) ([]detectionRule, error) {
	where := ""
	if onlyEnabled {
		where = " WHERE enabled = 1"
	}
	res, err := s.db.Query(ctx, `SELECT id, name, kind, enabled, spec, created_at, created_by, updated_at FROM security_rules`+where+` ORDER BY id LIMIT `+strconv.Itoa(maxRules))
	if err != nil {
		return nil, err
	}
	out := make([]detectionRule, 0, len(res.Rows))
	for _, row := range res.Rows {
		if r, ok := scanDetectionRule(row); ok {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *Server) getDetectionRule(ctx context.Context, id int64) (*detectionRule, error) {
	res, err := s.db.Query(ctx, `SELECT id, name, kind, enabled, spec, created_at, created_by, updated_at FROM security_rules WHERE id = ?`, id)
	if err != nil || len(res.Rows) == 0 {
		return nil, err
	}
	r, ok := scanDetectionRule(res.Rows[0])
	if !ok {
		return nil, errors.New("unreadable rule")
	}
	return &r, nil
}

func scanDetectionRule(row []any) (detectionRule, bool) {
	if len(row) != 8 {
		return detectionRule{}, false
	}
	var r detectionRule
	if json.Unmarshal([]byte(toString(row[4])), &r) != nil {
		return detectionRule{}, false
	}
	r.ID, r.Name, r.Kind, r.Enabled = toInt(row[0]), toString(row[1]), toString(row[2]), toInt(row[3]) == 1
	r.CreatedAt, r.CreatedBy, r.UpdatedAt = toString(row[5]), toString(row[6]), toString(row[7])
	return r, true
}

func (r *detectionRule) specJSON() string {
	c := *r
	c.ID, c.CreatedAt, c.CreatedBy, c.UpdatedAt = 0, "", "", ""
	b, _ := json.Marshal(c)
	return string(b)
}

// ruleKind names a new rule's signals: rule_ and its name, unique, kept
// when the rule is renamed so its history stays together.
func (s *Server) ruleKind(ctx context.Context, name string) (string, error) {
	base := strings.Trim(ruleSlug.ReplaceAllString(strings.ToLower(name), "_"), "_")
	base = "rule_" + strings.TrimRight(clip(fallback(base, "custom"), 40), "_")
	res, err := s.db.Query(ctx, `SELECT kind FROM security_rules WHERE kind = ? OR kind LIKE ? ESCAPE '!'`, base, likeEscaper.Replace(base)+"!_%")
	if err != nil {
		return "", err
	}
	taken := map[string]bool{}
	for _, row := range res.Rows {
		taken[toString(row[0])] = true
	}
	kind := base
	for i := 2; taken[kind]; i++ {
		kind = base + "_" + strconv.Itoa(i)
	}
	return kind, nil
}

func (s *Server) insertDetectionRule(ctx context.Context, r *detectionRule, by string) error {
	kind, err := s.ruleKind(ctx, r.Name)
	if err != nil {
		return err
	}
	now := nowTS()
	r.Kind, r.CreatedAt, r.CreatedBy, r.UpdatedAt = kind, now, by, now
	id, err := s.db.InsertID(ctx, `INSERT INTO security_rules (name, kind, enabled, template, spec, created_at, created_by, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Name, r.Kind, boolInt(r.Enabled), r.Template, r.specJSON(), now, by, now)
	r.ID = id
	return err
}

func (s *Server) updateDetectionRule(ctx context.Context, r *detectionRule) error {
	r.UpdatedAt = nowTS()
	return s.db.Exec(ctx, `UPDATE security_rules SET name = ?, enabled = ?, spec = ?, updated_at = ? WHERE id = ?`,
		r.Name, boolInt(r.Enabled), r.specJSON(), r.UpdatedAt, r.ID)
}

func (s *Server) securityRulesRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/security/rules", s.authed(s.handleListDetectionRules))
	mux.Handle("POST /api/security/rules", s.authed(s.handleCreateDetectionRule))
	mux.Handle("POST /api/security/rules/test", s.authed(s.handleTestDetectionRule))
	mux.Handle("PUT /api/security/rules/{id}", s.authed(s.handleUpdateDetectionRule))
	mux.Handle("PATCH /api/security/rules/{id}", s.authed(s.handlePatchDetectionRule))
	mux.Handle("DELETE /api/security/rules/{id}", s.authed(s.handleDeleteDetectionRule))
	mux.Handle("POST /api/security/rules/{id}/suppress", s.authed(s.handleSuppressDetectionRule))
}

type detectionRuleView struct {
	detectionRule
	Signals    int64  `json:"signals"`
	LastSignal string `json:"last_signal,omitempty"`
	LastRun    string `json:"last_run,omitempty"`
	Error      string `json:"error,omitempty"`
	Every      string `json:"every"`
}

func (s *Server) detectionRuleViews(ctx context.Context, rules []detectionRule) ([]detectionRuleView, error) {
	res, err := s.db.Query(ctx, `SELECT kind, COUNT(*), MAX(created_at) FROM security_events WHERE source = 'rule' AND created_at >= ? GROUP BY kind`,
		time.Now().UTC().Add(-7*24*time.Hour).Format(tsLayout))
	if err != nil {
		return nil, err
	}
	stats := map[string][2]string{}
	counts := map[string]int64{}
	for _, row := range res.Rows {
		if len(row) == 3 {
			counts[toString(row[0])] = toInt(row[1])
			stats[toString(row[0])] = [2]string{"", toString(row[2])}
		}
	}
	rs := s.ruleRuns()
	out := make([]detectionRuleView, 0, len(rules))
	for _, r := range rules {
		v := detectionRuleView{detectionRule: r, Signals: counts[r.Kind], LastSignal: stats[r.Kind][1], Every: fmtSpan(r.every())}
		rs.mu.Lock()
		if run := rs.runs[r.ID]; run != nil {
			if !run.lastRun.IsZero() {
				v.LastRun = run.lastRun.Format(tsLayout)
			}
			v.Error = run.lastErr
		}
		rs.mu.Unlock()
		out = append(out, v)
	}
	return out, nil
}

func (s *Server) handleListDetectionRules(w http.ResponseWriter, r *http.Request) {
	rules, err := s.listDetectionRules(r.Context(), false)
	if err != nil {
		s.fail(w, err)
		return
	}
	views, err := s.detectionRuleViews(r.Context(), rules)
	if err != nil {
		s.fail(w, err)
		return
	}
	used := map[string]int64{}
	for _, v := range rules {
		if v.Template != "" {
			used[v.Template] = v.ID
		}
	}
	type templateView struct {
		ruleTemplate
		RuleID int64 `json:"rule_id,omitempty"`
	}
	tpls := make([]templateView, 0, len(ruleTemplates))
	for _, t := range ruleTemplates {
		tpls = append(tpls, templateView{t, used[t.ID]})
	}
	fields := map[string][]string{}
	for name, src := range s.q.ruleSources() {
		for f := range src.fields {
			fields[name] = append(fields[name], f)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rules": views, "templates": tpls, "tactics": mitreTactics, "techniques": mitreTechniques,
		"fields": fields, "filters": ruleFilterKeys, "geo": s.geoEnabled(), "can_edit": s.can(r, "edit"),
	})
}

func sessionName(r *http.Request) string {
	if sess := sessionFrom(r); sess != nil {
		return fallback(sess.Name, sess.Handle)
	}
	return ""
}

func (s *Server) handleCreateDetectionRule(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "edit") || !s.requireAllServices(w, r) {
		return
	}
	var body struct {
		detectionRule
		From string `json:"from_template"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rule := body.detectionRule
	if body.From != "" {
		t, ok := templateByID(body.From)
		if !ok {
			writeError(w, http.StatusBadRequest, "unknown template")
			return
		}
		rule = t.rule()
		rule.Enabled = true
		rule.Channels = body.Channels
	}
	rules, err := s.listDetectionRules(r.Context(), false)
	if err != nil {
		s.fail(w, err)
		return
	}
	if len(rules) >= maxRules {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("Visionary keeps up to %d detection rules", maxRules))
		return
	}
	if err := s.checkDetectionRule(&rule); err != nil {
		s.fail(w, err)
		return
	}
	if err := s.insertDetectionRule(r.Context(), &rule, sessionName(r)); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "security.rule_created", rule.Name, map[string]any{"id": rule.ID, "kind": rule.Kind, "template": rule.Template})
	writeJSON(w, http.StatusOK, rule)
}

func (s *Server) ruleFromPath(w http.ResponseWriter, r *http.Request) *detectionRule {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid rule id")
		return nil
	}
	rule, err := s.getDetectionRule(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return nil
	}
	if rule == nil {
		writeError(w, http.StatusNotFound, "rule not found")
	}
	return rule
}

func (s *Server) handleUpdateDetectionRule(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "edit") || !s.requireAllServices(w, r) {
		return
	}
	old := s.ruleFromPath(w, r)
	if old == nil {
		return
	}
	var rule detectionRule
	if err := readJSON(r, &rule); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rule.ID, rule.Kind, rule.Template, rule.CreatedAt, rule.CreatedBy = old.ID, old.Kind, old.Template, old.CreatedAt, old.CreatedBy
	if err := s.checkDetectionRule(&rule); err != nil {
		s.fail(w, err)
		return
	}
	if err := s.updateDetectionRule(r.Context(), &rule); err != nil {
		s.fail(w, err)
		return
	}
	if old.Source != rule.Source || old.Field != rule.Field || strings.Join(old.GroupBy, ",") != strings.Join(rule.GroupBy, ",") || old.Logic != rule.Logic {
		s.forgetRule(r.Context(), rule.ID)
	}
	s.audit(r, "security.rule_updated", rule.Name, map[string]any{"id": rule.ID, "enabled": rule.Enabled})
	writeJSON(w, http.StatusOK, rule)
}

func (s *Server) handlePatchDetectionRule(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "edit") || !s.requireAllServices(w, r) {
		return
	}
	rule := s.ruleFromPath(w, r)
	if rule == nil {
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := readJSON(r, &body); err != nil || body.Enabled == nil {
		writeError(w, http.StatusBadRequest, "send {\"enabled\": true} or false")
		return
	}
	rule.Enabled = *body.Enabled
	if err := s.updateDetectionRule(r.Context(), rule); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "security.rule_updated", rule.Name, map[string]any{"id": rule.ID, "enabled": rule.Enabled})
	writeJSON(w, http.StatusOK, rule)
}

func (s *Server) handleDeleteDetectionRule(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "edit") || !s.requireAllServices(w, r) {
		return
	}
	rule := s.ruleFromPath(w, r)
	if rule == nil {
		return
	}
	if err := s.db.Exec(r.Context(), `DELETE FROM security_rules WHERE id = ?`, rule.ID); err != nil {
		s.fail(w, err)
		return
	}
	s.forgetRule(r.Context(), rule.ID)
	s.audit(r, "security.rule_deleted", rule.Name, map[string]any{"id": rule.ID, "kind": rule.Kind})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSuppressDetectionRule(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "edit") || !s.requireAllServices(w, r) {
		return
	}
	rule := s.ruleFromPath(w, r)
	if rule == nil {
		return
	}
	var body struct {
		Field string `json:"field"`
		Value string `json:"value"`
		Days  int    `json:"days"`
		Note  string `json:"note"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sp := ruleSuppression{Field: body.Field, Value: body.Value, Note: body.Note, By: sessionName(r)}
	if body.Days > 0 {
		sp.Until = time.Now().UTC().Add(time.Duration(min(body.Days, 3650)) * 24 * time.Hour).Format(time.RFC3339)
	}
	for i, old := range rule.Suppress {
		if old.Field == sp.Field && strings.EqualFold(old.Value, sp.Value) {
			rule.Suppress = append(rule.Suppress[:i], rule.Suppress[i+1:]...)
			break
		}
	}
	rule.Suppress = append(rule.Suppress, sp)
	if err := s.checkDetectionRule(rule); err != nil {
		s.fail(w, err)
		return
	}
	if err := s.updateDetectionRule(r.Context(), rule); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "security.rule_suppressed", rule.Name, map[string]any{"id": rule.ID, "field": sp.Field, "until": sp.Until})
	writeJSON(w, http.StatusOK, rule)
}
