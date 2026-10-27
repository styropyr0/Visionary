package analytics

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"time"
)

// Uptime checks and alert rules. A check is alerted on by:
//
//   - its own uptime_failing rule (scoped to the check), set from the check's
//     form; a disabled one means "never alert for this check", or
//   - the default rule, an uptime_failing rule for every check, created with
//     the first check. It leaves out checks that have a rule of their own, so
//     no check is alerted twice.
//
// The check list and form show which applies, so a check nobody would hear
// about says so.

const defaultUptimeRuleName = "Uptime check down"

type checkAlertView struct {
	// Mode is custom (its own rule), off (its own rule, disabled), default
	// (the rule for every check) or none (nothing alerts on it).
	Mode     string   `json:"mode"`
	RuleID   int64    `json:"rule_id,omitempty"`
	RuleName string   `json:"rule_name,omitempty"`
	Channels []string `json:"channels"`
	// ChannelIDs are the rule's channels, to prefill the form.
	ChannelIDs []int64 `json:"channel_ids"`
	Failures   int     `json:"failures"`
	Severity   string  `json:"severity,omitempty"`
	Firing     bool    `json:"firing"`
}

type checkAlertInput struct {
	Mode     string  `json:"mode"`
	Channels []int64 `json:"channels"`
	Failures int     `json:"failures"`
	Severity string  `json:"severity"`
}

func isOwnUptimeRule(r alertRule, checkID string) bool {
	return r.Kind == "threshold" && r.Spec.Metric == "uptime_failing" && r.Spec.Scope.Check != "" && (checkID == "" || r.Spec.Scope.Check == checkID)
}

func isDefaultUptimeRule(r alertRule) bool {
	return r.Kind == "threshold" && r.Spec.Metric == "uptime_failing" && r.Spec.Scope.Check == ""
}

// checksWithOwnRule lists the checks a rule of their own covers, enabled or
// not.
func (s *Server) checksWithOwnRule(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	rules, err := s.listRules(ctx, false)
	if err != nil {
		return out
	}
	for _, r := range rules {
		if isOwnUptimeRule(r, "") {
			out[r.Spec.Scope.Check] = true
		}
	}
	return out
}

// uptimeAlertViews says how each check is alerted on.
func (s *Server) uptimeAlertViews(ctx context.Context, checks []UptimeCheck) (map[int64]checkAlertView, error) {
	rules, err := s.listRules(ctx, false)
	if err != nil {
		return nil, err
	}
	chans, err := s.listChannels(ctx)
	if err != nil {
		return nil, err
	}
	states, err := s.ruleStates(ctx, 0)
	if err != nil {
		return nil, err
	}
	names := map[int64]string{}
	for _, c := range chans {
		names[c.ID] = c.Name
	}
	view := func(r alertRule, mode, key string) checkAlertView {
		v := checkAlertView{Mode: mode, RuleID: r.ID, RuleName: r.Name, Channels: []string{}, ChannelIDs: r.Channels,
			Failures: int(r.Spec.Threshold), Severity: r.Severity}
		for _, id := range r.Channels {
			if n, ok := names[id]; ok {
				v.Channels = append(v.Channels, n)
			}
		}
		for _, st := range states[r.ID] {
			if st.Key == key && st.State == "firing" {
				v.Firing = true
			}
		}
		return v
	}
	var def *alertRule
	for i := range rules {
		if isDefaultUptimeRule(rules[i]) && rules[i].Enabled {
			def = &rules[i]
			break
		}
	}
	out := make(map[int64]checkAlertView, len(checks))
	for _, c := range checks {
		id := strconv.FormatInt(c.ID, 10)
		v := checkAlertView{Mode: "none", Channels: []string{}, ChannelIDs: []int64{}}
		for _, r := range rules {
			if isOwnUptimeRule(r, id) {
				mode := "custom"
				if !r.Enabled {
					mode = "off"
				}
				v = view(r, mode, id)
				break
			}
		}
		if v.Mode == "none" && def != nil {
			v = view(*def, "default", id)
		}
		out[c.ID] = v
	}
	return out, nil
}

// ensureDefaultUptimeRule creates the rule for every check unless one exists,
// sending to the first channel. It reports the rule it made, if any.
func (s *Server) ensureDefaultUptimeRule(r *http.Request) (*alertRule, error) {
	ctx := r.Context()
	rules, err := s.listRules(ctx, false)
	if err != nil {
		return nil, err
	}
	for _, rule := range rules {
		if isDefaultUptimeRule(rule) {
			return nil, nil
		}
	}
	chans, err := s.listChannels(ctx)
	if err != nil {
		return nil, err
	}
	rule := alertRule{Name: defaultUptimeRuleName, Kind: "threshold", Severity: "critical", Channels: []int64{}, Enabled: true,
		Runbook: "Created with the first uptime check. A check with its own alert settings (in its form) is left out of this rule.",
		Spec:    ruleSpec{Metric: "uptime_failing", Op: ">=", Threshold: 2, Window: "30m", For: "0s"}}
	if len(chans) > 0 {
		rule.Channels = []int64{chans[0].ID}
	}
	if err := s.insertRule(ctx, &rule); err != nil {
		return nil, err
	}
	s.alertEngine().invalidate(rule.ID)
	s.recordHistory(r, "create", nil, &rule)
	s.audit(r, "alert.rule.create", rule.Name, map[string]any{"id": rule.ID, "kind": rule.Kind, "metric": rule.Spec.Metric, "default": true})
	return &rule, nil
}

// useDefaultUptimeRule makes the rule for every check one that alerts
// someone: created when missing, turned back on when switched off, and given
// the channels and failures asked for. A rule without channels gets the
// first one, since choosing it means someone should hear.
func (s *Server) useDefaultUptimeRule(r *http.Request, in checkAlertInput) error {
	ctx := r.Context()
	if in.Failures != 0 && (in.Failures < 1 || in.Failures > 20) {
		return badInput("alert after 1 to 20 failed checks in a row")
	}
	if _, err := s.ensureDefaultUptimeRule(r); err != nil {
		return err
	}
	rules, err := s.listRules(ctx, false)
	if err != nil {
		return err
	}
	var def *alertRule
	for i := range rules {
		// An enabled one wins when there are several.
		if isDefaultUptimeRule(rules[i]) && (def == nil || !def.Enabled && rules[i].Enabled) {
			def = &rules[i]
		}
	}
	if def == nil {
		return nil
	}
	chans, err := s.listChannels(ctx)
	if err != nil {
		return err
	}
	old, rule := *def, *def
	rule.Enabled = true
	if len(in.Channels) > 0 {
		for _, id := range in.Channels {
			if !slices.ContainsFunc(chans, func(c alertChannel) bool { return c.ID == id }) {
				return badInput("unknown channel " + strconv.FormatInt(id, 10))
			}
		}
		rule.Channels = in.Channels
	} else if len(rule.Channels) == 0 && len(chans) > 0 {
		rule.Channels = []int64{chans[0].ID}
	}
	if in.Failures != 0 {
		rule.Spec.Threshold = float64(in.Failures)
	}
	if rule.Enabled == old.Enabled && slices.Equal(rule.Channels, old.Channels) && rule.Spec.Threshold == old.Spec.Threshold {
		return nil
	}
	if err := s.updateRule(ctx, &rule); err != nil {
		return err
	}
	s.alertEngine().invalidate(rule.ID)
	s.recordHistory(r, "update", &old, &rule)
	s.audit(r, "alert.rule.update", rule.Name, map[string]any{"id": rule.ID, "default": true})
	return nil
}

// setCheckAlert applies a check's alert settings.
func (s *Server) setCheckAlert(r *http.Request, c UptimeCheck, in checkAlertInput) error {
	ctx := r.Context()
	id := strconv.FormatInt(c.ID, 10)
	rules, err := s.listRules(ctx, false)
	if err != nil {
		return err
	}
	var own *alertRule
	for i := range rules {
		if isOwnUptimeRule(rules[i], id) {
			if own == nil {
				own = &rules[i]
			}
		}
	}
	switch in.Mode {
	case "default":
		for _, rule := range rules {
			if isOwnUptimeRule(rule, id) {
				if err := s.deleteRule(ctx, rule.ID); err != nil {
					return err
				}
				old := rule
				s.recordHistory(r, "delete", &old, nil)
			}
		}
		s.alertEngine().invalidate(0)
		return s.useDefaultUptimeRule(r, in)
	case "custom", "off":
	default:
		return badInput("alert mode must be default, custom or off")
	}
	if in.Failures == 0 {
		in.Failures = 2
	}
	if in.Failures < 1 || in.Failures > 20 {
		return badInput("alert after 1 to 20 failed checks in a row")
	}
	enabled := in.Mode == "custom"
	input := ruleInput{Name: "Uptime: " + c.Name + " is down", Kind: "threshold", Severity: fallback(in.Severity, "critical"),
		Channels: in.Channels, Enabled: &enabled,
		Spec: ruleSpec{Metric: "uptime_failing", Op: ">=", Threshold: float64(in.Failures), Window: "30m", For: "0s", Scope: alertScope{Check: id}}}
	if own != nil {
		// Settings made elsewhere (a runbook, a repeat, labels) are kept.
		input.Runbook, input.Spec = own.Runbook, own.Spec
		input.Spec.Threshold, input.Spec.Op, input.Spec.Scope.Check = float64(in.Failures), ">=", id
		if in.Channels == nil {
			input.Channels = own.Channels
		}
	}
	rule, err := s.checkRule(ctx, input)
	if err != nil {
		return err
	}
	if own == nil {
		if err := s.insertRule(ctx, &rule); err != nil {
			return err
		}
		s.recordHistory(r, "create", nil, &rule)
	} else {
		rule.ID, rule.CreatedAt, rule.MutedUntil = own.ID, own.CreatedAt, own.MutedUntil
		if err := s.updateRule(ctx, &rule); err != nil {
			return err
		}
		s.recordHistory(r, "update", own, &rule)
	}
	s.alertEngine().invalidate(rule.ID)
	s.audit(r, "uptime.alert", c.Name, map[string]any{"id": c.ID, "mode": in.Mode, "rule_id": rule.ID})
	return nil
}

// dropCheckRules deletes the rules of a deleted check.
func (s *Server) dropCheckRules(r *http.Request, checkID int64) {
	ctx := r.Context()
	rules, err := s.listRules(ctx, false)
	if err != nil {
		s.logger.Warn("uptime: load rules", "err", err)
		return
	}
	for _, rule := range rules {
		if isOwnUptimeRule(rule, strconv.FormatInt(checkID, 10)) {
			if err := s.deleteRule(ctx, rule.ID); err != nil {
				s.logger.Warn("uptime: delete check rule", "rule_id", rule.ID, "err", err)
				continue
			}
			old := rule
			s.recordHistory(r, "delete", &old, nil)
		}
	}
	s.alertEngine().invalidate(0)
}

func (s *Server) handleUptimeAlertGet(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadCheck(w, r)
	if !ok {
		return
	}
	views, err := s.uptimeAlertViews(r.Context(), []UptimeCheck{c})
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"alert": views[c.ID]})
}

func (s *Server) handleUptimeAlertSet(w http.ResponseWriter, r *http.Request) {
	if !s.can(r, "edit") {
		writeError(w, http.StatusForbidden, "you do not have permission to change checks")
		return
	}
	c, ok := s.loadCheck(w, r)
	if !ok {
		return
	}
	var in checkAlertInput
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.setCheckAlert(r, c, in); err != nil {
		s.fail(w, err)
		return
	}
	views, err := s.uptimeAlertViews(r.Context(), []UptimeCheck{c})
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"alert": views[c.ID], "at": time.Now().UTC().Format(tsLayout)})
}
