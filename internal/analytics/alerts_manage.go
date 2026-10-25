package analytics

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Rule management beyond one rule at a time: the change history of a rule,
// bulk actions, exporting and importing rules and silences as JSON, the
// counts behind the sidebar badges, and Activity as CSV.

// ---------- History ----------

type ruleHistoryEntry struct {
	ID      int64      `json:"id"`
	RuleID  int64      `json:"rule_id"`
	At      string     `json:"at"`
	Actor   string     `json:"actor"`
	Action  string     `json:"action"` // create, update, delete, mute, unmute, enable, disable
	Changes []string   `json:"changes"`
	Rule    *alertRule `json:"rule"`
}

// recordHistory keeps what a change did to a rule: the fields that changed
// and the rule as it was afterwards (before, for a delete). Failures are
// logged; the change itself already happened.
func (s *Server) recordHistory(r *http.Request, action string, before, after *alertRule) {
	s.recordHistoryAs(r.Context(), actorName(r), action, before, after)
}

func (s *Server) recordHistoryAs(ctx context.Context, actor, action string, before, after *alertRule) {
	snap := after
	if snap == nil {
		snap = before
	}
	if snap == nil {
		return
	}
	changes := diffRules(before, after)
	if action == "update" && len(changes) == 0 {
		return
	}
	c, _ := json.Marshal(changes)
	b, _ := json.Marshal(snap)
	if _, err := s.db.InsertID(ctx, `INSERT INTO alert_rule_history (rule_id, at, actor, action, changes, rule) VALUES (?, ?, ?, ?, ?, ?)`,
		snap.ID, time.Now().UTC().Format(tsLayout), actor, action, string(c), string(b)); err != nil {
		s.logger.Warn("alerts: record rule history", "rule_id", snap.ID, "err", err)
	}
}

// diffRules names the settings that differ, in words people use.
func diffRules(a, b *alertRule) []string {
	if a == nil || b == nil {
		return []string{}
	}
	out := []string{}
	add := func(name string, changed bool) {
		if changed {
			out = append(out, name)
		}
	}
	add("name", a.Name != b.Name)
	add("type", a.Kind != b.Kind)
	add("severity", a.Severity != b.Severity)
	add("channels", mustJSON(a.Channels) != mustJSON(b.Channels))
	add("runbook", a.Runbook != b.Runbook)
	add("enabled", a.Enabled != b.Enabled)
	add("muted until", a.MutedUntil != b.MutedUntil)
	var sa, sb map[string]json.RawMessage
	_ = json.Unmarshal([]byte(mustJSON(a.Spec)), &sa)
	_ = json.Unmarshal([]byte(mustJSON(b.Spec)), &sb)
	keys := map[string]bool{}
	for k := range sa {
		keys[k] = true
	}
	for k := range sb {
		keys[k] = true
	}
	var spec []string
	for k := range keys {
		if string(sa[k]) != string(sb[k]) {
			spec = append(spec, strings.ReplaceAll(k, "_", " "))
		}
	}
	sort.Strings(spec)
	return append(out, spec...)
}

func (s *Server) handleRuleHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid rule id")
		return
	}
	res, err := s.db.Query(r.Context(), `SELECT id, rule_id, at, actor, action, changes, rule FROM alert_rule_history
WHERE rule_id = ? ORDER BY at DESC, id DESC LIMIT 100`, id)
	if err != nil {
		if missingTable(err, "alert_rule_history") {
			writeJSON(w, http.StatusOK, map[string]any{"history": []ruleHistoryEntry{}})
			return
		}
		s.fail(w, err)
		return
	}
	out := make([]ruleHistoryEntry, 0, len(res.Rows))
	for _, row := range res.Rows {
		if len(row) != 7 {
			continue
		}
		h := ruleHistoryEntry{ID: toInt(row[0]), RuleID: toInt(row[1]), At: toString(row[2]), Actor: toString(row[3]), Action: toString(row[4]), Changes: []string{}}
		_ = json.Unmarshal([]byte(toString(row[5])), &h.Changes)
		var rule alertRule
		if json.Unmarshal([]byte(toString(row[6])), &rule) == nil {
			h.Rule = &rule
		}
		out = append(out, h)
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": out})
}

// ---------- Bulk actions ----------

type bulkInput struct {
	IDs     []int64 `json:"ids"`
	Action  string  `json:"action"` // enable, disable, mute, unmute, delete
	Minutes int     `json:"minutes"`
}

func (s *Server) handleBulkRules(w http.ResponseWriter, r *http.Request) {
	if !s.can(r, "edit") {
		s.forbidden(w)
		return
	}
	var in bulkInput
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(in.IDs) == 0 || len(in.IDs) > 500 {
		writeError(w, http.StatusBadRequest, "pick 1 to 500 rules")
		return
	}
	switch in.Action {
	case "enable", "disable", "unmute", "delete":
	case "mute":
		if in.Minutes <= 0 || in.Minutes > 60*24*90 {
			writeError(w, http.StatusBadRequest, "mute for 1 minute to 90 days")
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "action must be enable, disable, mute, unmute or delete")
		return
	}
	ctx := r.Context()
	now := time.Now().UTC()
	changed := 0
	for _, id := range in.IDs {
		old, err := s.getRule(ctx, id)
		if err != nil {
			s.fail(w, err)
			return
		}
		if old == nil {
			continue
		}
		rule := *old
		switch in.Action {
		case "delete":
			if err := s.deleteRule(ctx, id); err != nil {
				s.fail(w, err)
				return
			}
			s.recordHistory(r, "delete", old, nil)
			changed++
			continue
		case "enable", "disable":
			rule.Enabled = in.Action == "enable"
		case "mute":
			rule.MutedUntil = now.Add(time.Duration(in.Minutes) * time.Minute).Format(tsLayout)
		case "unmute":
			rule.MutedUntil = ""
		}
		if mustJSON(rule) == mustJSON(*old) {
			continue
		}
		if err := s.updateRule(ctx, &rule); err != nil {
			s.fail(w, err)
			return
		}
		s.recordHistory(r, in.Action, old, &rule)
		changed++
	}
	s.alertEngine().invalidate(0)
	s.audit(r, "alert.rule.bulk_"+in.Action, strconv.Itoa(changed)+" rules", map[string]any{"ids": in.IDs, "minutes": in.Minutes})
	writeJSON(w, http.StatusOK, map[string]any{"changed": changed})
}

// ---------- Export and import ----------

// alertsExport is the file people move rules between installs with.
// Channels go by name (their URLs and keys stay behind); importing maps them
// onto channels of the same name.
type alertsExport struct {
	Version    int               `json:"version"`
	ExportedAt string            `json:"exported_at,omitempty"`
	Channels   []exportedChannel `json:"channels"`
	Rules      []exportedRule    `json:"rules"`
	Silences   []silenceInput    `json:"silences"`
}

type exportedChannel struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type exportedRule struct {
	Name       string         `json:"name"`
	Kind       string         `json:"kind"`
	Severity   string         `json:"severity"`
	Runbook    string         `json:"runbook"`
	Enabled    bool           `json:"enabled"`
	Channels   []string       `json:"channels"`
	Spec       ruleSpec       `json:"spec"`
	Escalation []exportedStep `json:"escalation,omitempty"`
	DependsOn  []string       `json:"depends_on,omitempty"`
}

type exportedStep struct {
	After    string   `json:"after"`
	Channels []string `json:"channels"`
}

func (s *Server) handleExportAlerts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rules, err := s.listRules(ctx, false)
	if err != nil {
		s.fail(w, err)
		return
	}
	chans, err := s.listChannels(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	silences, err := s.listSilences(ctx)
	if err != nil && !missingTable(err, "alert_silences") {
		s.fail(w, err)
		return
	}
	chanName, ruleName := map[int64]string{}, map[int64]string{}
	out := alertsExport{Version: 1, ExportedAt: time.Now().UTC().Format(time.RFC3339), Channels: []exportedChannel{}, Rules: []exportedRule{}, Silences: []silenceInput{}}
	for _, c := range chans {
		chanName[c.ID] = c.Name
		out.Channels = append(out.Channels, exportedChannel{Name: c.Name, Kind: c.Kind})
	}
	for _, rule := range rules {
		ruleName[rule.ID] = rule.Name
	}
	names := func(ids []int64, m map[int64]string) []string {
		out := []string{}
		for _, id := range ids {
			if n, ok := m[id]; ok {
				out = append(out, n)
			}
		}
		return out
	}
	for _, rule := range rules {
		e := exportedRule{Name: rule.Name, Kind: rule.Kind, Severity: rule.Severity, Runbook: rule.Runbook, Enabled: rule.Enabled,
			Channels: names(rule.Channels, chanName), Spec: rule.Spec}
		for _, st := range rule.Spec.Escalation {
			e.Escalation = append(e.Escalation, exportedStep{After: st.After, Channels: names(st.Channels, chanName)})
		}
		if deps := names(rule.Spec.DependsOn, ruleName); len(deps) > 0 {
			e.DependsOn = deps
		}
		e.Spec.Escalation, e.Spec.DependsOn = nil, nil
		out.Rules = append(out.Rules, e)
	}
	for _, sl := range silences {
		in := silenceInput{Name: sl.Name, Matchers: sl.Matchers, Schedule: sl.Schedule, Enabled: &sl.Enabled}
		// Rule ids differ between installs; a silence for named rules keeps
		// working only where those rules exist, so it is matched by name.
		in.Matchers.RuleIDs = nil
		if len(sl.Matchers.RuleIDs) > 0 {
			in.Matchers.Rules = names(sl.Matchers.RuleIDs, ruleName)
		}
		out.Silences = append(out.Silences, in)
	}
	w.Header().Set("Content-Disposition", `attachment; filename="visionary-alerts-`+time.Now().UTC().Format("2006-01-02")+`.json"`)
	writeJSON(w, http.StatusOK, out)
}

type importResult struct {
	Created  int      `json:"created"`
	Skipped  int      `json:"skipped"`
	Silences int      `json:"silences"`
	Warnings []string `json:"warnings"`
}

// handleImportAlerts adds the rules and silences of an export. Rules and
// silences whose name already exists are skipped, so importing the same file
// twice adds nothing.
func (s *Server) handleImportAlerts(w http.ResponseWriter, r *http.Request) {
	if !s.can(r, "edit") {
		s.forbidden(w)
		return
	}
	var in alertsExport
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	if err := dec.Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "not a Visionary alerts export (invalid JSON)")
		return
	}
	if in.Version != 1 {
		writeError(w, http.StatusBadRequest, "unsupported export version")
		return
	}
	if len(in.Rules) > 1000 || len(in.Silences) > 200 {
		writeError(w, http.StatusBadRequest, "at most 1000 rules and 200 silences per import")
		return
	}
	ctx := r.Context()
	res := importResult{Warnings: []string{}}
	rules, err := s.listRules(ctx, false)
	if err != nil {
		s.fail(w, err)
		return
	}
	chans, err := s.listChannels(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	chanID, ruleID := map[string]int64{}, map[string]int64{}
	for _, c := range chans {
		chanID[c.Name] = c.ID
	}
	for _, rule := range rules {
		ruleID[rule.Name] = rule.ID
	}
	ids := func(names []string, what string) []int64 {
		out := []int64{}
		for _, n := range names {
			if id, ok := chanID[n]; ok {
				out = append(out, id)
			} else {
				res.Warnings = append(res.Warnings, what+": no channel named "+strconv.Quote(n))
			}
		}
		return out
	}
	type pending struct {
		id   int64
		deps []string
	}
	var withDeps []pending
	for _, e := range in.Rules {
		if _, exists := ruleID[e.Name]; exists {
			res.Skipped++
			continue
		}
		enabled := e.Enabled
		spec := e.Spec
		spec.DependsOn = nil
		spec.Escalation = nil
		for _, st := range e.Escalation {
			if c := ids(st.Channels, e.Name+" escalation"); len(c) > 0 {
				spec.Escalation = append(spec.Escalation, escalationStep{After: st.After, Channels: c})
			}
		}
		rule, err := s.checkRule(ctx, ruleInput{Name: e.Name, Kind: e.Kind, Severity: e.Severity, Runbook: e.Runbook,
			Enabled: &enabled, Channels: ids(e.Channels, e.Name), Spec: spec})
		if err != nil {
			res.Warnings = append(res.Warnings, e.Name+": "+userMessage(err))
			continue
		}
		if err := s.insertRule(ctx, &rule); err != nil {
			s.fail(w, err)
			return
		}
		s.recordHistory(r, "create", nil, &rule)
		ruleID[rule.Name] = rule.ID
		res.Created++
		if len(e.DependsOn) > 0 {
			withDeps = append(withDeps, pending{rule.ID, e.DependsOn})
		}
	}
	// Dependencies name rules that may come later in the file.
	for _, p := range withDeps {
		rule, err := s.getRule(ctx, p.id)
		if err != nil || rule == nil {
			continue
		}
		for _, n := range p.deps {
			if id, ok := ruleID[n]; ok && id != rule.ID {
				rule.Spec.DependsOn = append(rule.Spec.DependsOn, id)
			} else {
				res.Warnings = append(res.Warnings, rule.Name+": depends on a missing rule "+strconv.Quote(n))
			}
		}
		if err := s.updateRule(ctx, rule); err != nil {
			s.fail(w, err)
			return
		}
	}
	existing, err := s.listSilences(ctx)
	if err != nil && !missingTable(err, "alert_silences") {
		s.fail(w, err)
		return
	}
	haveSilence := map[string]bool{}
	for _, sl := range existing {
		haveSilence[sl.Name] = true
	}
	for _, si := range in.Silences {
		if haveSilence[strings.TrimSpace(si.Name)] {
			continue
		}
		for _, n := range si.Matchers.Rules {
			if id, ok := ruleID[n]; ok {
				si.Matchers.RuleIDs = append(si.Matchers.RuleIDs, id)
			}
		}
		if len(si.Matchers.Rules) > 0 && len(si.Matchers.RuleIDs) == 0 {
			res.Warnings = append(res.Warnings, "silence "+si.Name+": none of its rules exist; skipped")
			continue
		}
		si.Matchers.Rules = nil
		sl := alertSilence{CreatedBy: actorName(r)}
		if err := si.apply(&sl); err != nil {
			res.Warnings = append(res.Warnings, "silence "+si.Name+": "+userMessage(err))
			continue
		}
		if err := s.saveSilence(ctx, &sl); err != nil {
			s.fail(w, err)
			return
		}
		res.Silences++
	}
	s.alertEngine().invalidate(0)
	s.alertEngine().invalidateSilences()
	s.audit(r, "alert.import", strconv.Itoa(res.Created)+" rules", map[string]any{"created": res.Created, "skipped": res.Skipped, "silences": res.Silences})
	writeJSON(w, http.StatusOK, res)
}

// ---------- Summary ----------

// handleAlertsSummary is what the sidebar badges show: rules firing, firing
// series nobody acknowledged, and uptime checks that are down.
func (s *Server) handleAlertsSummary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := map[string]any{"firing_rules": 0, "firing_series": 0, "unacked": 0, "checks_down": 0, "down": []string{}}
	rules, err := s.alertEngine().enabledRules(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	enabled := map[int64]bool{}
	for _, rule := range rules {
		enabled[rule.ID] = true
	}
	states, err := s.ruleStates(ctx, 0)
	if err != nil {
		s.fail(w, err)
		return
	}
	incidents, err := s.openIncidents(ctx, 0)
	if err != nil {
		s.fail(w, err)
		return
	}
	firingRules, series, unacked := 0, 0, 0
	for id, list := range states {
		if !enabled[id] {
			continue
		}
		any := false
		for _, st := range list {
			if st.State != "firing" {
				continue
			}
			any = true
			series++
			if inc := incidents[id][st.Key]; inc == nil || inc.AckedAt == "" {
				unacked++
			}
		}
		if any {
			firingRules++
		}
	}
	out["firing_rules"], out["firing_series"], out["unacked"] = firingRules, series, unacked
	down, err := s.checksDown(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	out["checks_down"], out["down"] = len(down), head(down, 5)
	writeJSON(w, http.StatusOK, out)
}

// checksDown names the enabled uptime checks whose latest result failed.
func (s *Server) checksDown(ctx context.Context) ([]string, error) {
	res, err := s.db.Query(ctx, `SELECT c.name FROM uptime_checks c
JOIN (SELECT check_id, MAX(created_at) AS latest FROM uptime_results WHERE created_at >= ? GROUP BY check_id) l ON l.check_id = c.id
JOIN uptime_results r ON r.check_id = l.check_id AND r.created_at = l.latest
WHERE c.enabled = 1 AND r.ok = 0
ORDER BY c.name`, time.Now().UTC().Add(-48*time.Hour).Format(tsLayout))
	if err != nil {
		if missingTable(err, "uptime_results") || missingTable(err, "uptime_checks") {
			return []string{}, nil
		}
		return nil, err
	}
	out := []string{}
	seen := map[string]bool{}
	for _, row := range res.Rows {
		if n := toString(row[0]); !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out, nil
}

// ---------- CSV ----------

func (s *Server) handleEventsCSV(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	tr, err := parseRange(q, s.maxRangeDays())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	f, err := eventFilterFrom(q, tr)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	f.MaxLimit, f.Limit = 10000, 10000
	events, err := s.listEvents(r.Context(), f)
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="alert-activity-`+time.Now().UTC().Format("2006-01-02")+`.csv"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"created_at", "rule_id", "rule_name", "kind", "severity", "state", "series_key", "value", "threshold", "message", "explanation", "notified", "suppressed"})
	num := func(v *float64) string {
		if v == nil {
			return ""
		}
		return fnum(*v)
	}
	for _, ev := range events {
		_ = cw.Write([]string{ev.CreatedAt, strconv.FormatInt(ev.RuleID, 10), csvCell(ev.RuleName), ev.Kind, ev.Severity, ev.State, csvCell(ev.SeriesKey),
			num(ev.Value), num(ev.Threshold), csvCell(ev.Message), csvCell(ev.Explanation), strconv.FormatBool(ev.Notified), csvCell(ev.Suppressed)})
	}
	cw.Flush()
}

// csvCell keeps a spreadsheet from running a cell as a formula.
func csvCell(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
}
