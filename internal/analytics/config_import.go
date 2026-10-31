package analytics

import (
	"encoding/json"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Importing a configuration document. A dry run reports what would be
// created, updated or left unchanged without writing anything; applying
// makes those changes. Nothing is deleted: something missing from the file
// is left alone.

type configChange struct {
	Type    string   `json:"type"`
	Name    string   `json:"name"`
	Action  string   `json:"action"` // create, update, unchanged or skip
	Fields  []string `json:"fields,omitempty"`
	Warning string   `json:"warning,omitempty"`
}

type configImport struct {
	s       *Server
	r       *http.Request
	apply   bool
	st      *configState
	changes []configChange
	// Names of what exists, or was created by this import. A dry run maps
	// what it would create to 0.
	chanID, ruleID, checkID, panelID map[string]int64
	// rules made by this import that depend on rules made after them
	laterDeps map[int64][]string
}

func (s *Server) handleConfigImport(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") || !s.requireAllServices(w, r) {
		return
	}
	var body struct {
		Document string `json:"document"`
		DryRun   bool   `json:"dry_run"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "expected {document, dry_run}; the file may be over 8 MB")
		return
	}
	doc, err := parseConfigDoc(body.Document)
	if err != nil {
		s.fail(w, err)
		return
	}
	if len(doc.Channels) > 500 || len(doc.Rules) > 2000 || len(doc.Silences) > 500 || len(doc.UptimeChecks) > uptimeMaxChecks ||
		len(doc.SavedPanels) > 2000 || len(doc.Dashboards) > 500 || len(doc.LogViews) > 500 {
		writeError(w, http.StatusBadRequest, "the file holds more than one install can")
		return
	}
	st, err := s.loadConfigState(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	ci := &configImport{s: s, r: r, apply: !body.DryRun, st: st, changes: []configChange{},
		chanID: map[string]int64{}, ruleID: map[string]int64{}, checkID: map[string]int64{}, panelID: map[string]int64{},
		laterDeps: map[int64][]string{}}
	for _, c := range st.channels {
		ci.chanID[c.Name] = c.ID
	}
	for _, rule := range st.rules {
		if _, dup := ci.ruleID[rule.Name]; !dup && !isOwnCheckRule(rule) {
			ci.ruleID[rule.Name] = rule.ID
		}
	}
	for _, c := range st.checks {
		if _, dup := ci.checkID[c.Name]; !dup {
			ci.checkID[c.Name] = c.ID
		}
	}
	for _, p := range st.panels {
		if _, dup := ci.panelID[p.Title]; !dup {
			ci.panelID[p.Title] = p.ID
		}
	}
	// Each part may refer to the ones before it.
	steps := []func(configDoc) error{ci.channels, ci.checks, ci.rules, ci.silences, ci.panels, ci.dashboards, ci.logViews, ci.pages}
	for _, step := range steps {
		if err := step(doc); err != nil {
			s.fail(w, err)
			return
		}
	}
	counts := map[string]int{"create": 0, "update": 0, "unchanged": 0, "skip": 0}
	for _, c := range ci.changes {
		counts[c.Action]++
	}
	if ci.apply {
		s.alertEngine().invalidate(0)
		s.alertEngine().invalidateSilences()
		wakeUptime(s)
		s.audit(r, "config.import", "", counts)
	}
	writeJSON(w, http.StatusOK, map[string]any{"dry_run": body.DryRun, "changes": ci.changes, "counts": counts})
}

func (ci *configImport) note(typ, name string, fields []string) {
	action := "unchanged"
	switch {
	case fields == nil:
		action = "create"
	case len(fields) > 0:
		action = "update"
	}
	ci.changes = append(ci.changes, configChange{Type: typ, Name: name, Action: action, Fields: fields})
}

func (ci *configImport) skip(typ, name, why string) {
	ci.changes = append(ci.changes, configChange{Type: typ, Name: name, Action: "skip", Warning: why})
}

// warn adds a warning to the last change noted.
func (ci *configImport) warn(msg string) {
	if n := len(ci.changes); n > 0 {
		c := &ci.changes[n-1]
		c.Warning = strings.TrimPrefix(c.Warning+"; "+msg, "; ")
	}
}

// resolve maps names to ids. Names that do not exist are dropped with a
// warning; names a dry run would create keep their place in names but get
// no id.
func resolve(names []string, ids map[string]int64, what string) (out []int64, kept []string, missing []string) {
	out, kept = []int64{}, []string{}
	for _, n := range names {
		id, ok := ids[n]
		switch {
		case !ok:
			missing = append(missing, what+" "+strconv.Quote(n))
		case id > 0:
			out = append(out, id)
			kept = append(kept, n)
		default:
			kept = append(kept, n)
		}
	}
	return out, kept, missing
}

// diffFields names the fields that differ between two forms of the same
// thing, one level into nested objects (spec.window). Empty and missing
// values count as equal.
func diffFields(a, b any) []string {
	var ma, mb map[string]any
	_ = json.Unmarshal([]byte(mustJSON(a)), &ma)
	_ = json.Unmarshal([]byte(mustJSON(b)), &mb)
	out := []string{}
	diffMaps("", ma, mb, &out)
	sort.Strings(out)
	return out
}

func diffMaps(prefix string, a, b map[string]any, out *[]string) {
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	for k := range keys {
		x, y := a[k], b[k]
		if isEmptyJSON(x) && isEmptyJSON(y) || mustJSON(x) == mustJSON(y) {
			continue
		}
		xm, ok1 := x.(map[string]any)
		ym, ok2 := y.(map[string]any)
		if prefix == "" && (ok1 || x == nil) && (ok2 || y == nil) {
			diffMaps(k+".", xm, ym, out)
			continue
		}
		*out = append(*out, prefix+k)
	}
}

func isEmptyJSON(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case bool:
		return !t
	case float64:
		return t == 0
	case []any:
		return len(t) == 0
	case map[string]any:
		for _, x := range t {
			if !isEmptyJSON(x) {
				return false
			}
		}
		return true
	}
	return false
}

func orTrue(b *bool) bool { return b == nil || *b }

func (ci *configImport) channels(doc configDoc) error {
	ctx := ci.r.Context()
	for _, in := range doc.Channels {
		name := strings.TrimSpace(in.Name)
		wd, hr := in.DigestWeekday, in.DigestHour
		input := channelInput{Name: name, Kind: in.Kind, URL: in.URL, Secret: in.Secret, To: in.To, ChatID: in.ChatID,
			AccountSID: in.AccountSID, From: in.From, Region: in.Region, DigestEnabled: in.DigestEnabled, DigestWeekday: &wd, DigestHour: &hr}
		var cur *alertChannel
		for i := range ci.st.channels {
			if ci.st.channels[i].Name == name {
				cur = &ci.st.channels[i]
				break
			}
		}
		if cur == nil {
			c := alertChannel{}
			if err := input.apply(&c); err != nil {
				why := userMessage(err)
				if urlKinds[in.Kind] && in.URL == "" || secretKinds[in.Kind] && in.Secret == "" {
					why += " (URLs and keys are left out of exports: add url or secret to the file, or create the channel first)"
				}
				ci.skip("channel", name, why)
				continue
			}
			ci.note("channel", name, nil)
			ci.chanID[name] = 0
			if ci.apply {
				if err := ci.s.saveChannel(ctx, &c); err != nil {
					return err
				}
				ci.chanID[name] = c.ID
				ci.s.audit(ci.r, "alert.channel.create", c.Name, map[string]any{"id": c.ID, "kind": c.Kind, "import": true})
			}
			continue
		}
		if in.Kind != "" && in.Kind != cur.Kind {
			ci.skip("channel", name, "a channel's kind cannot change")
			continue
		}
		next := *cur
		next.Config.To = slices.Clone(cur.Config.To)
		if err := input.apply(&next); err != nil {
			ci.skip("channel", name, userMessage(err))
			continue
		}
		fields := diffFields(channelForm(*cur), channelForm(next))
		if next.Config.URL != cur.Config.URL {
			fields = append(fields, "url")
		}
		if next.Config.Secret != cur.Config.Secret {
			fields = append(fields, "secret")
		}
		ci.note("channel", name, fields)
		if ci.apply && len(fields) > 0 {
			if err := ci.s.saveChannel(ctx, &next); err != nil {
				return err
			}
			ci.s.audit(ci.r, "alert.channel.update", next.Name, map[string]any{"id": next.ID, "kind": next.Kind, "import": true})
		}
	}
	return nil
}

func (ci *configImport) checks(doc configDoc) error {
	ctx := ci.r.Context()
	total := len(ci.st.checks)
	for _, in := range doc.UptimeChecks {
		c := UptimeCheck{Name: in.Name, Kind: in.Kind, URL: in.URL, Method: in.Method, ExpectStatus: in.ExpectStatus,
			BodyContains: in.BodyContains, Headers: in.Headers, IntervalS: in.IntervalS, TimeoutMs: in.TimeoutMs, DegradedMs: in.DegradedMs,
			FollowRedirects: orTrue(in.FollowRedirects), Enabled: orTrue(in.Enabled), OnStatusPage: in.OnStatusPage,
			StatusShowURL: in.StatusShowURL, Steps: in.Steps, Secrets: in.Secrets, FinalScreenshot: in.FinalScreenshot, Tags: in.Tags, Playwright: in.Playwright}
		name := strings.TrimSpace(in.Name)
		var cur *UptimeCheck
		for i := range ci.st.checks {
			if ci.st.checks[i].Name == name {
				cur = &ci.st.checks[i]
				break
			}
		}
		if cur != nil {
			c.ID, c.CreatedAt = cur.ID, cur.CreatedAt
		}
		if err := c.normalize(cur); err != nil {
			why := err.Error()
			if strings.Contains(why, "again") {
				why += " (secrets are left out of exports: put the value in the file)"
			}
			ci.skip("uptime_check", name, why)
			continue
		}
		var alert *checkAlertInput
		var alertForm *configCheckAlert
		var missing []string
		if a := in.Alert; a != nil {
			if a.Mode != "custom" && a.Mode != "off" && a.Mode != "default" {
				ci.skip("uptime_check", name, "alert mode must be custom, off or default")
				continue
			}
			ids, kept, miss := resolve(a.Channels, ci.chanID, "no channel")
			missing = miss
			alert = &checkAlertInput{Mode: a.Mode, Channels: ids, Failures: a.Failures, Severity: a.Severity}
			if a.Mode != "default" {
				alertForm = &configCheckAlert{Mode: a.Mode, Channels: kept, Failures: max(a.Failures, 0), Severity: fallback(a.Severity, "critical")}
				if alertForm.Failures == 0 {
					alertForm.Failures = 2
				}
			}
		}
		if cur == nil {
			if total >= uptimeMaxChecks {
				ci.skip("uptime_check", name, "at most "+strconv.Itoa(uptimeMaxChecks)+" checks")
				continue
			}
			total++
			ci.note("uptime_check", name, nil)
			ci.checkID[name] = 0
		} else {
			fields := checkChanges(*cur, c)
			if fields == nil {
				fields = []string{}
			}
			before := ci.st.checkConfig(*cur).Alert
			if alert != nil && mustJSON(before) != mustJSON(alertForm) {
				fields = append(fields, "alert")
			} else {
				alert = nil
			}
			ci.note("uptime_check", name, fields)
			if len(fields) == 0 {
				continue
			}
		}
		for _, m := range missing {
			ci.warn(m)
		}
		if !ci.apply {
			continue
		}
		created := c.ID == 0
		if err := ci.s.saveUptimeCheck(ctx, &c); err != nil {
			return err
		}
		ci.checkID[name] = c.ID
		action := "uptime.update"
		if created {
			action = "uptime.create"
		}
		ci.s.audit(ci.r, action, c.Name, map[string]any{"id": c.ID, "url": redactURL(c.URL), "import": true})
		if alert != nil {
			if err := ci.s.setCheckAlert(ci.r, c, *alert); err != nil {
				ci.warn("alert: " + userMessage(err))
			}
		}
	}
	return nil
}

// ruleFrom builds the rule a file entry describes, with what it refers to
// resolved, and the form it is compared in.
func (ci *configImport) ruleFrom(in configRule, inFile map[string]bool) (alertRule, configRule, []string, error) {
	name := strings.TrimSpace(in.Name)
	chans, chanNames, missing := resolve(in.Channels, ci.chanID, "no channel")
	spec := in.Spec
	spec.Escalation, spec.DependsOn, spec.Scope.Check = nil, nil, ""
	form := configRule{Name: name, Kind: in.Kind, Severity: in.Severity, Runbook: in.Runbook, Channels: chanNames}
	for _, step := range in.Escalation {
		ids, kept, miss := resolve(step.Channels, ci.chanID, "no channel")
		missing = append(missing, miss...)
		if len(kept) > 0 {
			form.Escalation = append(form.Escalation, exportedStep{After: step.After, Channels: kept})
		}
		if len(ids) > 0 {
			spec.Escalation = append(spec.Escalation, escalationStep{After: step.After, Channels: ids})
		}
	}
	if in.Check != "" {
		id, ok := ci.checkID[in.Check]
		if !ok {
			return alertRule{}, form, missing, badInput("no uptime check named " + strconv.Quote(in.Check))
		}
		form.Check = in.Check
		if id > 0 {
			spec.Scope.Check = strconv.FormatInt(id, 10)
		}
	}
	for _, d := range in.DependsOn {
		if d == name {
			continue
		}
		if id, ok := ci.ruleID[d]; ok || inFile[d] {
			form.DependsOn = append(form.DependsOn, d)
			if id > 0 {
				spec.DependsOn = append(spec.DependsOn, id)
			}
		} else {
			missing = append(missing, "no rule "+strconv.Quote(d)+" to depend on")
		}
	}
	enabled := orTrue(in.Enabled)
	rule, err := ci.s.checkRule(ci.r.Context(), ruleInput{Name: name, Kind: in.Kind, Severity: in.Severity, Runbook: in.Runbook,
		Enabled: &enabled, Channels: chans, Spec: spec})
	if err != nil {
		return rule, form, missing, err
	}
	form.Kind, form.Severity, form.Runbook, form.Enabled, form.Spec = rule.Kind, rule.Severity, rule.Runbook, &enabled, rule.Spec
	form.Spec.Escalation, form.Spec.DependsOn, form.Spec.Scope.Check = nil, nil, ""
	return rule, form, missing, nil
}

func (ci *configImport) rules(doc configDoc) error {
	ctx := ci.r.Context()
	inFile := map[string]bool{}
	for _, in := range doc.Rules {
		inFile[strings.TrimSpace(in.Name)] = true
	}
	byName := map[string]*alertRule{}
	for i := range ci.st.rules {
		if r := &ci.st.rules[i]; byName[r.Name] == nil && !isOwnCheckRule(*r) {
			byName[r.Name] = r
		}
	}
	for _, in := range doc.Rules {
		rule, form, missing, err := ci.ruleFrom(in, inFile)
		if err != nil {
			ci.skip("rule", form.Name, userMessage(err))
			continue
		}
		cur := byName[rule.Name]
		if cur == nil {
			ci.note("rule", rule.Name, nil)
			ci.ruleID[rule.Name] = 0
		} else {
			ci.note("rule", rule.Name, diffFields(ci.st.ruleConfig(*cur), form))
		}
		for _, m := range missing {
			ci.warn(m)
		}
		if !ci.apply {
			continue
		}
		if cur == nil {
			if err := ci.s.insertRule(ctx, &rule); err != nil {
				return err
			}
			ci.s.recordHistory(ci.r, "create", nil, &rule)
			ci.ruleID[rule.Name] = rule.ID
		} else if changed := ci.changes[len(ci.changes)-1].Fields; len(changed) > 0 {
			rule.ID, rule.CreatedAt, rule.MutedUntil = cur.ID, cur.CreatedAt, cur.MutedUntil
			if err := ci.s.updateRule(ctx, &rule); err != nil {
				return err
			}
			if cur.Spec.conditionKey() != rule.Spec.conditionKey() || cur.Kind != rule.Kind {
				if err := ci.s.clearRuleState(ctx, rule.ID); err != nil {
					return err
				}
			}
			ci.s.recordHistory(ci.r, "update", cur, &rule)
		}
		if rule.ID > 0 && len(rule.Spec.DependsOn) < len(form.DependsOn) {
			ci.laterDeps[rule.ID] = form.DependsOn
		}
	}
	// Dependencies on rules that came later in the file.
	for id, deps := range ci.laterDeps {
		rule, err := ci.s.getRule(ctx, id)
		if err != nil || rule == nil {
			continue
		}
		before := *rule
		rule.Spec.DependsOn = nil
		for _, d := range deps {
			if dep := ci.ruleID[d]; dep > 0 && dep != id {
				rule.Spec.DependsOn = append(rule.Spec.DependsOn, dep)
			}
		}
		if err := ci.s.updateRule(ctx, rule); err != nil {
			return err
		}
		ci.s.recordHistory(ci.r, "update", &before, rule)
	}
	if ci.apply {
		ci.s.audit(ci.r, "alert.import", "", map[string]any{"rules": len(doc.Rules), "config": true})
	}
	return nil
}

func (ci *configImport) silences(doc configDoc) error {
	ctx := ci.r.Context()
	for _, in := range doc.Silences {
		ids, kept, missing := resolve(in.Matchers.Rules, ci.ruleID, "no rule")
		name := strings.TrimSpace(in.Name)
		if len(in.Matchers.Rules) > 0 && len(kept) == 0 {
			ci.skip("silence", name, "none of its rules exist")
			continue
		}
		si := in
		si.Matchers.RuleIDs, si.Matchers.Rules = ids, nil
		sl := alertSilence{CreatedBy: actorName(ci.r)}
		if err := si.apply(&sl); err != nil {
			ci.skip("silence", name, userMessage(err))
			continue
		}
		enabled := sl.Enabled
		form := silenceInput{Name: sl.Name, Matchers: sl.Matchers, Schedule: sl.Schedule, Enabled: &enabled}
		form.Matchers.RuleIDs = nil
		if len(kept) > 0 {
			form.Matchers.Rules = kept
		}
		var cur *alertSilence
		for i := range ci.st.silences {
			if ci.st.silences[i].Name == sl.Name {
				cur = &ci.st.silences[i]
				break
			}
		}
		var fields []string
		if cur != nil {
			fields = diffFields(ci.st.silenceConfig(*cur), form)
		}
		ci.note("silence", sl.Name, fields)
		for _, m := range missing {
			ci.warn(m)
		}
		if !ci.apply || cur != nil && len(fields) == 0 {
			continue
		}
		if cur != nil {
			sl.ID, sl.CreatedBy, sl.CreatedAt = cur.ID, cur.CreatedBy, cur.CreatedAt
		}
		if err := ci.s.saveSilence(ctx, &sl); err != nil {
			return err
		}
	}
	return nil
}

func (ci *configImport) panels(doc configDoc) error {
	ctx := ci.r.Context()
	for _, in := range doc.SavedPanels {
		title := strings.TrimSpace(in.Title)
		checked, err := validateReadOnly(in.Query)
		if err != nil || title == "" || len(title) > maxTitleLen || len(in.Description) > maxDescLen || len(in.Prompt) > maxPromptLen {
			why := "title must be 1–120 characters, with a short description and prompt"
			if err != nil {
				why = "query: " + err.Error()
			}
			ci.skip("saved_panel", title, why)
			continue
		}
		form := configPanel{Title: title, Description: in.Description, Prompt: in.Prompt, Query: checked, Viz: in.Viz}
		var cur *Panel
		for i := range ci.st.panels {
			if ci.st.panels[i].Title == title {
				cur = &ci.st.panels[i]
				break
			}
		}
		if cur == nil {
			ci.note("saved_panel", title, nil)
			ci.panelID[title] = 0
			if ci.apply {
				p, err := ci.s.createPanel(ctx, Panel{Title: title, Description: in.Description, Prompt: in.Prompt, Query: checked, Viz: in.Viz})
				if err != nil {
					return err
				}
				ci.panelID[title] = p.ID
			}
			continue
		}
		fields := diffFields(configPanel{Title: cur.Title, Description: cur.Description, Prompt: cur.Prompt, Query: cur.Query, Viz: cur.Viz}, form)
		ci.note("saved_panel", title, fields)
		if ci.apply && len(fields) > 0 {
			if err := ci.s.db.Exec(ctx, `UPDATE analytics_panels SET description = ?, prompt = ?, query = ?, viz = ? WHERE id = ?`,
				in.Description, in.Prompt, checked, mustJSON(in.Viz), cur.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (ci *configImport) dashboards(doc configDoc) error {
	ctx := ci.r.Context()
	for _, in := range doc.Dashboards {
		d := dashboard{Name: in.Name, Description: strings.TrimSpace(in.Description), Layout: in.Layout}
		titles := map[string]string{}
		var missing []string
		d.Layout.Panels = nil
		for _, p := range in.Layout.Panels {
			if p.Kind == "saved" {
				id, ok := ci.panelID[p.Ref]
				if !ok {
					missing = append(missing, "no saved panel titled "+strconv.Quote(p.Ref))
					continue
				}
				titles[p.ID] = p.Ref
				p.Ref = strconv.FormatInt(id, 10)
			}
			d.Layout.Panels = append(d.Layout.Panels, p)
		}
		if err := ci.s.validateDashboard(&d); err != nil {
			ci.skip("dashboard", strings.TrimSpace(in.Name), err.Error())
			continue
		}
		form := configDashboard{Name: d.Name, Description: d.Description, Layout: d.Layout}
		form.Layout.Panels = slices.Clone(d.Layout.Panels)
		for i, p := range form.Layout.Panels {
			if t, ok := titles[p.ID]; ok {
				form.Layout.Panels[i].Ref = t
			}
		}
		var cur *dashboard
		for i := range ci.st.dashes {
			if ci.st.dashes[i].Name == d.Name {
				cur = &ci.st.dashes[i]
				break
			}
		}
		var fields []string
		if cur != nil {
			fields = diffFields(ci.st.dashboardConfig(*cur), form)
		}
		ci.note("dashboard", d.Name, fields)
		for _, m := range missing {
			ci.warn(m)
		}
		if !ci.apply || cur != nil && len(fields) == 0 {
			continue
		}
		layout := mustJSON(d.Layout)
		now := time.Now().UTC().Format(tsLayout)
		if cur == nil {
			id, err := ci.s.db.InsertID(ctx, `INSERT INTO dashboards (name, description, layout, created_by, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)`, d.Name, d.Description, layout, actorName(ci.r), now, now)
			if err != nil {
				return err
			}
			ci.s.audit(ci.r, "dashboard.create", strconv.FormatInt(id, 10), map[string]any{"name": d.Name, "import": true})
			continue
		}
		if err := ci.s.db.Exec(ctx, `UPDATE dashboards SET description = ?, layout = ?, updated_at = ? WHERE id = ?`,
			d.Description, layout, now, cur.ID); err != nil {
			return err
		}
		ci.s.audit(ci.r, "dashboard.update", strconv.FormatInt(cur.ID, 10), map[string]any{"name": d.Name, "import": true})
	}
	return nil
}

func (ci *configImport) logViews(doc configDoc) error {
	ctx := ci.r.Context()
	for _, in := range doc.LogViews {
		name, query := strings.TrimSpace(in.Name), strings.TrimSpace(in.Query)
		if name == "" || len(name) > 120 || len(in.Columns) > 30 {
			ci.skip("log_view", name, "a saved search needs a name of up to 120 characters and at most 30 columns")
			continue
		}
		if _, err := compileLogQuery(query, ci.s.q, ci.s.onD1()); err != nil {
			ci.skip("log_view", name, "query: "+err.Error())
			continue
		}
		cols := in.Columns
		if cols == nil {
			cols = []string{}
		}
		form := configLogView{Name: name, Query: query, Columns: cols}
		var cur *logView
		for i := range ci.st.views {
			if ci.st.views[i].Name == name {
				cur = &ci.st.views[i]
				break
			}
		}
		var fields []string
		if cur != nil {
			fields = diffFields(configLogView{Name: cur.Name, Query: cur.Query, Columns: cur.Columns}, form)
		}
		ci.note("log_view", name, fields)
		if !ci.apply || cur != nil && len(fields) == 0 {
			continue
		}
		if cur == nil {
			if _, err := ci.s.db.InsertID(ctx, `INSERT INTO log_views (name, query, columns, created_by, created_at) VALUES (?, ?, ?, ?, ?)`,
				name, query, mustJSON(cols), actorName(ci.r), time.Now().UTC().Format(tsLayout)); err != nil {
				return err
			}
			continue
		}
		if err := ci.s.db.Exec(ctx, `UPDATE log_views SET query = ?, columns = ? WHERE id = ?`, query, mustJSON(cols), cur.ID); err != nil {
			return err
		}
	}
	return nil
}

func (ci *configImport) pages(doc configDoc) error {
	if doc.Pages == nil {
		return nil
	}
	hidden := []string{}
	for _, p := range doc.Pages.Hidden {
		if !slices.Contains(hideablePages, p) {
			ci.skip("pages", "Pages", "unknown page "+strconv.Quote(p))
			return nil
		}
		if !slices.Contains(hidden, p) {
			hidden = append(hidden, p)
		}
	}
	a, b := slices.Clone(ci.st.hidden), slices.Clone(hidden)
	sort.Strings(a)
	sort.Strings(b)
	fields := []string{}
	if !slices.Equal(a, b) {
		fields = append(fields, "hidden")
	}
	ci.note("pages", "Pages", fields)
	if !ci.apply || len(fields) == 0 {
		return nil
	}
	if err := ci.s.writeSettings(ci.r.Context(), map[string]string{settingHiddenPages: mustJSON(hidden)}); err != nil {
		return err
	}
	ci.s.audit(ci.r, "pages.update", "", map[string]any{"hidden": hidden, "import": true})
	return nil
}
