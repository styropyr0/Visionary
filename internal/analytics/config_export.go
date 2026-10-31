package analytics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Configuration as a file: alert rules, channels, silences, uptime checks,
// saved panels, dashboards, saved log searches and the hidden pages, written
// as one YAML or JSON document that another install imports. Everything is
// matched by name, since ids differ between installs, and secrets (channel
// URLs and keys, secret headers, browser secrets) stay behind.

const configVersion = 1

type configDoc struct {
	Kind         string            `json:"kind"`
	Version      int               `json:"version"`
	ExportedAt   string            `json:"exported_at,omitempty"`
	Channels     []configChannel   `json:"channels"`
	Rules        []configRule      `json:"rules"`
	Silences     []silenceInput    `json:"silences"`
	UptimeChecks []configCheck     `json:"uptime_checks"`
	SavedPanels  []configPanel     `json:"saved_panels"`
	Dashboards   []configDashboard `json:"dashboards"`
	LogViews     []configLogView   `json:"log_views"`
	Pages        *configPages      `json:"pages,omitempty"`
}

// configChannel leaves the URL and secret out of exports; a file may carry
// them to create a channel that needs them.
type configChannel struct {
	Name          string   `json:"name"`
	Kind          string   `json:"kind"`
	URL           string   `json:"url,omitempty"`
	Secret        string   `json:"secret,omitempty"`
	To            []string `json:"to,omitempty"`
	ChatID        string   `json:"chat_id,omitempty"`
	AccountSID    string   `json:"account_sid,omitempty"`
	From          string   `json:"from,omitempty"`
	Region        string   `json:"region,omitempty"`
	DigestEnabled bool     `json:"digest_enabled"`
	DigestWeekday int      `json:"digest_weekday"`
	DigestHour    int      `json:"digest_hour"`
}

// configRule names its channels, the rules it depends on and the uptime
// check it watches (Check) instead of using their ids.
type configRule struct {
	Name       string         `json:"name"`
	Kind       string         `json:"kind"`
	Severity   string         `json:"severity"`
	Runbook    string         `json:"runbook,omitempty"`
	Enabled    *bool          `json:"enabled,omitempty"`
	Channels   []string       `json:"channels"`
	Check      string         `json:"check,omitempty"`
	Spec       ruleSpec       `json:"spec"`
	Escalation []exportedStep `json:"escalation,omitempty"`
	DependsOn  []string       `json:"depends_on,omitempty"`
}

type configCheck struct {
	Name            string            `json:"name"`
	Kind            string            `json:"kind"`
	URL             string            `json:"url,omitempty"`
	Method          string            `json:"method,omitempty"`
	ExpectStatus    string            `json:"expect_status,omitempty"`
	BodyContains    string            `json:"body_contains,omitempty"`
	Headers         map[string]string `json:"headers,omitempty"`
	IntervalS       int               `json:"interval_s"`
	TimeoutMs       int               `json:"timeout_ms"`
	DegradedMs      int               `json:"degraded_ms"`
	FollowRedirects *bool             `json:"follow_redirects,omitempty"`
	Enabled         *bool             `json:"enabled,omitempty"`
	OnStatusPage    bool              `json:"on_status_page"`
	StatusShowURL   bool              `json:"status_show_url"`
	Steps           []BrowserStep     `json:"steps,omitempty"`
	Secrets         map[string]string `json:"secrets,omitempty"`
	FinalScreenshot bool              `json:"final_screenshot,omitempty"`
	Tags            []string          `json:"tags,omitempty"`
	Alert           *configCheckAlert `json:"alert,omitempty"`
}

// configCheckAlert is a check's own alert (mode custom or off); checks
// alerted by the rule for every check have none.
type configCheckAlert struct {
	Mode     string   `json:"mode"`
	Channels []string `json:"channels"`
	Failures int      `json:"failures"`
	Severity string   `json:"severity,omitempty"`
}

type configPanel struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Prompt      string `json:"prompt,omitempty"`
	Query       string `json:"query"`
	Viz         Viz    `json:"viz"`
}

// configDashboard's saved panels are referred to by their title.
type configDashboard struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Layout      dashboardLayout `json:"layout"`
}

type configLogView struct {
	Name    string   `json:"name"`
	Query   string   `json:"query"`
	Columns []string `json:"columns"`
}

type configPages struct {
	Hidden []string `json:"hidden"`
}

func (s *Server) configRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/config/export", s.authed(s.handleConfigExport))
	mux.Handle("POST /api/config/import", s.authed(s.handleConfigImport))
}

// configState is everything the document covers as it is stored now, with
// lookups both ways between ids and names.
type configState struct {
	channels []alertChannel
	rules    []alertRule
	silences []alertSilence
	checks   []UptimeCheck
	panels   []Panel
	dashes   []dashboard
	views    []logView
	hidden   []string

	chanName, ruleName, checkName, panelTitle map[int64]string
	alerts                                    map[int64]checkAlertView
}

func (s *Server) loadConfigState(ctx context.Context) (*configState, error) {
	st := &configState{}
	var err error
	if st.channels, err = s.listChannels(ctx); err != nil {
		return nil, err
	}
	if st.rules, err = s.listRules(ctx, false); err != nil {
		return nil, err
	}
	if st.silences, err = s.listSilences(ctx); err != nil && !missingTable(err, "alert_silences") {
		return nil, err
	}
	if st.checks, err = s.listUptimeChecks(ctx); err != nil {
		return nil, err
	}
	if st.panels, err = s.listPanels(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT `+dashColumns+` FROM dashboards ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	for _, row := range rows.Rows {
		if d, ok := scanDashboard(row); ok {
			st.dashes = append(st.dashes, d)
		}
	}
	rows, err = s.db.Query(ctx, `SELECT id, name, query, columns, created_by, created_at FROM log_views ORDER BY name`)
	if err != nil && !missingTable(err, "log_views") {
		return nil, err
	}
	if err == nil {
		for _, row := range rows.Rows {
			v := logView{ID: toInt(row[0]), Name: toString(row[1]), Query: toString(row[2]), Columns: []string{}}
			_ = json.Unmarshal([]byte(toString(row[3])), &v.Columns)
			st.views = append(st.views, v)
		}
	}
	set, err := s.loadSettings(ctx)
	if err != nil {
		return nil, err
	}
	st.hidden = set.HiddenPages
	if st.alerts, err = s.uptimeAlertViews(ctx, st.checks); err != nil {
		return nil, err
	}
	st.index()
	return st, nil
}

func (st *configState) index() {
	st.chanName, st.ruleName, st.checkName, st.panelTitle = map[int64]string{}, map[int64]string{}, map[int64]string{}, map[int64]string{}
	for _, c := range st.channels {
		st.chanName[c.ID] = c.Name
	}
	for _, r := range st.rules {
		st.ruleName[r.ID] = r.Name
	}
	for _, c := range st.checks {
		st.checkName[c.ID] = c.Name
	}
	for _, p := range st.panels {
		st.panelTitle[p.ID] = p.Title
	}
}

func idNames(ids []int64, m map[int64]string) []string {
	out := []string{}
	for _, id := range ids {
		if n, ok := m[id]; ok {
			out = append(out, n)
		}
	}
	return out
}

func channelForm(c alertChannel) configChannel {
	return configChannel{Name: c.Name, Kind: c.Kind, To: c.Config.To, ChatID: c.Config.ChatID, AccountSID: c.Config.AccountSID,
		From: c.Config.From, Region: c.Config.Region, DigestEnabled: c.DigestEnabled, DigestWeekday: c.DigestWeekday, DigestHour: c.DigestHour}
}

func (st *configState) ruleConfig(r alertRule) configRule {
	enabled := r.Enabled
	out := configRule{Name: r.Name, Kind: r.Kind, Severity: r.Severity, Runbook: r.Runbook, Enabled: &enabled,
		Channels: idNames(r.Channels, st.chanName), Spec: r.Spec}
	for _, step := range r.Spec.Escalation {
		out.Escalation = append(out.Escalation, exportedStep{After: step.After, Channels: idNames(step.Channels, st.chanName)})
	}
	if deps := idNames(r.Spec.DependsOn, st.ruleName); len(deps) > 0 {
		out.DependsOn = deps
	}
	if id, err := strconv.ParseInt(r.Spec.Scope.Check, 10, 64); err == nil {
		out.Check = st.checkName[id]
	}
	out.Spec.Escalation, out.Spec.DependsOn, out.Spec.Scope.Check = nil, nil, ""
	return out
}

func (st *configState) silenceConfig(sl alertSilence) silenceInput {
	enabled := sl.Enabled
	out := silenceInput{Name: sl.Name, Matchers: sl.Matchers, Schedule: sl.Schedule, Enabled: &enabled}
	out.Matchers.RuleIDs = nil
	if len(sl.Matchers.RuleIDs) > 0 {
		out.Matchers.Rules = idNames(sl.Matchers.RuleIDs, st.ruleName)
	}
	return out
}

// checkConfig is the check with its secrets masked.
func (st *configState) checkConfig(c UptimeCheck) configCheck {
	m := c.masked()
	follow, enabled := m.FollowRedirects, m.Enabled
	out := configCheck{Name: m.Name, Kind: m.Kind, URL: m.URL, Method: m.Method, ExpectStatus: m.ExpectStatus, BodyContains: m.BodyContains,
		Headers: m.Headers, IntervalS: m.IntervalS, TimeoutMs: m.TimeoutMs, DegradedMs: m.DegradedMs, FollowRedirects: &follow,
		Enabled: &enabled, OnStatusPage: m.OnStatusPage, StatusShowURL: m.StatusShowURL, Steps: m.Steps, Secrets: m.Secrets,
		FinalScreenshot: m.FinalScreenshot, Tags: m.Tags}
	if len(out.Tags) == 0 {
		out.Tags = nil
	}
	if len(out.Headers) == 0 {
		out.Headers = nil
	}
	if m.Kind == "browser" {
		// The start URL is the first step's; the rest does not apply.
		out.URL, out.Method, out.ExpectStatus, out.FollowRedirects = "", "", "", nil
	}
	if a, ok := st.alerts[c.ID]; ok && (a.Mode == "custom" || a.Mode == "off") {
		out.Alert = &configCheckAlert{Mode: a.Mode, Channels: idNames(a.ChannelIDs, st.chanName), Failures: a.Failures, Severity: a.Severity}
	}
	return out
}

func (st *configState) dashboardConfig(d dashboard) configDashboard {
	out := configDashboard{Name: d.Name, Description: d.Description, Layout: d.Layout}
	out.Layout.Panels = make([]dashPanel, 0, len(d.Layout.Panels))
	for _, p := range d.Layout.Panels {
		if p.Kind == "saved" {
			id, _ := strconv.ParseInt(p.Ref, 10, 64)
			title, ok := st.panelTitle[id]
			if !ok {
				continue
			}
			p.Ref = title
		}
		out.Layout.Panels = append(out.Layout.Panels, p)
	}
	return out
}

// isOwnCheckRule reports the rule a check's alert settings make; it travels
// with the check instead.
func isOwnCheckRule(r alertRule) bool { return isOwnUptimeRule(r, "") }

func (st *configState) document() configDoc {
	doc := configDoc{Kind: "visionary-config", Version: configVersion, ExportedAt: time.Now().UTC().Format(time.RFC3339),
		Channels: []configChannel{}, Rules: []configRule{}, Silences: []silenceInput{}, UptimeChecks: []configCheck{},
		SavedPanels: []configPanel{}, Dashboards: []configDashboard{}, LogViews: []configLogView{}, Pages: &configPages{Hidden: st.hidden}}
	for _, c := range st.channels {
		doc.Channels = append(doc.Channels, channelForm(c))
	}
	for _, r := range st.rules {
		if !isOwnCheckRule(r) {
			doc.Rules = append(doc.Rules, st.ruleConfig(r))
		}
	}
	for _, sl := range st.silences {
		doc.Silences = append(doc.Silences, st.silenceConfig(sl))
	}
	for _, c := range st.checks {
		doc.UptimeChecks = append(doc.UptimeChecks, st.checkConfig(c))
	}
	for _, p := range st.panels {
		doc.SavedPanels = append(doc.SavedPanels, configPanel{Title: p.Title, Description: p.Description, Prompt: p.Prompt, Query: p.Query, Viz: p.Viz})
	}
	for _, d := range st.dashes {
		doc.Dashboards = append(doc.Dashboards, st.dashboardConfig(d))
	}
	for _, v := range st.views {
		doc.LogViews = append(doc.LogViews, configLogView{Name: v.Name, Query: v.Query, Columns: v.Columns})
	}
	return doc
}

func (s *Server) handleConfigExport(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") || !s.requireAllServices(w, r) {
		return
	}
	st, err := s.loadConfigState(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	doc := st.document()
	name := "visionary-config-" + time.Now().UTC().Format("2006-01-02")
	s.audit(r, "config.export", "", map[string]any{"rules": len(doc.Rules), "checks": len(doc.UptimeChecks), "dashboards": len(doc.Dashboards)})
	if r.URL.Query().Get("format") == "json" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.json"`)
		writeJSON(w, http.StatusOK, doc)
		return
	}
	out, err := toYAML(doc)
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.yaml"`)
	_, _ = w.Write(out)
}

// toYAML writes v as YAML with its fields in struct order, which reads
// better in a review than yaml's sorted map keys.
func toYAML(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	node, err := yamlNode(dec)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(node); err != nil {
		return nil, err
	}
	return buf.Bytes(), enc.Close()
}

func yamlNode(dec *json.Decoder) (*yaml.Node, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		kind, end := yaml.MappingNode, json.Delim('}')
		if t == '[' {
			kind, end = yaml.SequenceNode, ']'
		}
		n := &yaml.Node{Kind: kind}
		for dec.More() {
			if kind == yaml.MappingNode {
				k, err := dec.Token()
				if err != nil {
					return nil, err
				}
				n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k.(string)})
			}
			child, err := yamlNode(dec)
			if err != nil {
				return nil, err
			}
			n.Content = append(n.Content, child)
		}
		if tok, err := dec.Token(); err != nil || tok != end {
			return nil, errors.New("unbalanced JSON")
		}
		return n, nil
	case string:
		n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: t}
		if strings.Contains(t, "\n") {
			n.Style = yaml.LiteralStyle
		}
		return n, nil
	case json.Number:
		return &yaml.Node{Kind: yaml.ScalarNode, Value: t.String()}, nil
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(t)}, nil
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}, nil
}

// parseConfigDoc reads a document in YAML or JSON (JSON is YAML too), and
// refuses unknown fields so a typo is not silently ignored.
func parseConfigDoc(text string) (configDoc, error) {
	var doc configDoc
	var generic any
	if err := yaml.Unmarshal([]byte(text), &generic); err != nil {
		return doc, badInput("not valid YAML or JSON: " + err.Error())
	}
	raw, err := json.Marshal(generic)
	if err != nil {
		return doc, badInput("the file has values JSON cannot hold")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		return doc, badInput("not a Visionary configuration file: " + err.Error())
	}
	if doc.Kind != "visionary-config" {
		return doc, badInput(`not a Visionary configuration file (kind should be "visionary-config")`)
	}
	if doc.Version != configVersion {
		return doc, badInput("unsupported configuration version " + strconv.Itoa(doc.Version))
	}
	return doc, nil
}
