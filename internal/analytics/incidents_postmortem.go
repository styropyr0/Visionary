package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// The postmortem is written once an incident is over: a draft from the
// timeline and the traffic of the affected services, by the model when the
// AI analyst is on and from the data alone when it is off, then edited by
// people. Action items are their own rows so each can have an owner and be
// ticked off.

const (
	maxPostmortemField = 20000
	postmortemBudget   = 90 * time.Second
)

type postmortem struct {
	Summary   string `json:"summary"`
	Impact    string `json:"impact"`
	RootCause string `json:"root_cause"`
	Timeline  string `json:"timeline"`
	Lessons   string `json:"lessons"`
	// Drafted is "ai" or "data" for a draft nobody has edited since.
	Drafted   string `json:"drafted,omitempty"`
	UpdatedBy string `json:"updated_by,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

func parsePostmortem(raw string) *postmortem {
	if raw == "" {
		return nil
	}
	var p postmortem
	if json.Unmarshal([]byte(raw), &p) != nil {
		return nil
	}
	return &p
}

func (s *Server) savePostmortem(ctx context.Context, id int64, p postmortem) error {
	b, _ := json.Marshal(p)
	return s.db.Exec(ctx, `UPDATE incidents SET postmortem = ?, updated_at = ? WHERE id = ?`, string(b), nowTS(), id)
}

type incidentAction struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	Owner     string `json:"owner"`
	Done      bool   `json:"done"`
	CreatedAt string `json:"created_at"`
	DoneAt    string `json:"done_at,omitempty"`
}

func (s *Server) incidentActions(ctx context.Context, id int64) ([]incidentAction, error) {
	res, err := s.db.Query(ctx, `SELECT id, body, owner, done, created_at, done_at FROM incident_actions WHERE incident_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	out := make([]incidentAction, 0, len(res.Rows))
	for _, row := range res.Rows {
		out = append(out, incidentAction{toInt(row[0]), toString(row[1]), toString(row[2]), toInt(row[3]) == 1, toString(row[4]), toString(row[5])})
	}
	return out, nil
}

func (s *Server) addAction(ctx context.Context, id int64, body, owner string) error {
	return s.db.Exec(ctx, `INSERT INTO incident_actions (incident_id, body, owner, done, created_at, done_at) VALUES (?, ?, ?, 0, ?, '')`, id, body, owner, nowTS())
}

func (s *Server) incidentEvidence(ctx context.Context, in *declaredIncident) string {
	start, _ := time.Parse(tsLayout, in.StartedAt)
	end := time.Now().UTC()
	if t, err := time.Parse(tsLayout, in.ResolvedAt); err == nil {
		end = t
	}
	if end.Sub(start) < 5*time.Minute {
		end = start.Add(5 * time.Minute)
	}
	before := start.Add(-end.Sub(start))
	svc, svcArgs := "", []any{}
	if len(in.Services) > 0 {
		svc = ` AND service IN (` + placeholders(len(in.Services)) + `)`
		for _, sv := range in.Services {
			svcArgs = append(svcArgs, sv)
		}
	}
	var b strings.Builder
	traffic := func(title string, from, to time.Time) {
		res, err := s.db.Query(ctx, `SELECT service, COUNT(*), `+countIf("status >= 500")+`, `+s.q.round("AVG(duration_ms)", 0)+`
FROM api_logs WHERE created_at >= ? AND created_at < ?`+svc+` GROUP BY service ORDER BY 2 DESC LIMIT 10`,
			append([]any{from.Format(tsLayout), to.Format(tsLayout)}, svcArgs...)...)
		if err != nil || len(res.Rows) == 0 {
			return
		}
		b.WriteString(title + "\n")
		for _, row := range res.Rows {
			fmt.Fprintf(&b, "- %s: %d requests, %d answered 5xx, %v ms on average\n", fallback(toString(row[0]), "unknown"), toInt(row[1]), toInt(row[2]), toString(row[3]))
		}
	}
	traffic("Requests during the incident:", start, end)
	traffic("Requests over the same length of time before it:", before, start)
	res, err := s.db.Query(ctx, `SELECT type, COUNT(*), MIN(created_at) FROM errors WHERE created_at >= ? AND created_at < ?`+svc+` GROUP BY type ORDER BY 2 DESC LIMIT 8`,
		append([]any{start.Format(tsLayout), end.Format(tsLayout)}, svcArgs...)...)
	if err == nil && len(res.Rows) > 0 {
		b.WriteString("Errors raised during the incident:\n")
		for _, row := range res.Rows {
			fmt.Fprintf(&b, "- %s: %d, first at %s\n", toString(row[0]), toInt(row[1]), toString(row[2]))
		}
	}
	return b.String()
}

func timelineText(entries []incidentEntry) string {
	var b strings.Builder
	for _, e := range entries {
		if e.Kind == "link" || e.Kind == "edit" || e.Kind == "postmortem" {
			continue
		}
		at := e.At
		if t, err := time.Parse(tsLayout, e.At); err == nil {
			at = t.Format("Jan 2 15:04 UTC")
		}
		line := e.Body
		if e.Actor != "" && (e.Kind == "note" || e.Kind == "update") {
			line = e.Actor + ": " + line
		}
		fmt.Fprintf(&b, "- %s — %s\n", at, strings.ReplaceAll(line, "\n", " "))
	}
	return strings.TrimSpace(b.String())
}

func durationText(sec *int64) string {
	if sec == nil {
		return "not reached"
	}
	return humanDuration(time.Duration(*sec) * time.Second)
}

func dataDraft(in *declaredIncident, entries []incidentEntry, evidence string) postmortem {
	services := "no services named"
	if len(in.Services) > 0 {
		services = strings.Join(in.Services, ", ")
	}
	summary := fmt.Sprintf("%s incident affecting %s. Acknowledged after %s, mitigated after %s and resolved after %s.",
		severityLabel(in.Severity), services, durationText(in.TTA), durationText(in.TTM), durationText(in.TTR))
	if in.Commander != "" {
		summary += " " + in.Commander + " was the incident commander."
	}
	impact := in.Impact
	if evidence != "" {
		impact = strings.TrimSpace(impact + "\n\n" + evidence)
	}
	return postmortem{Summary: summary, Impact: impact, Timeline: timelineText(entries), Drafted: "data"}
}

func (s *Server) handleDraftPostmortem(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "edit") {
		return
	}
	in, ok := s.incidentFromPath(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	links, err := s.incidentLinks(ctx, in.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	entries, err := s.incidentTimeline(ctx, in, links)
	if err != nil {
		s.fail(w, err)
		return
	}
	evidence := s.incidentEvidence(ctx, in)
	p := dataDraft(in, entries, evidence)
	var note string
	var actions []incidentAction
	if c := s.llm(); c == nil {
		note = "The AI analyst is off, so this draft holds the facts and timeline only; write the root cause and lessons yourself, or add an OpenAI API key in Settings → Server for a written draft."
	} else if drafted, acts, err := s.modelPostmortem(ctx, c, in, entries, links, evidence); err != nil {
		s.logger.Info("incidents: postmortem draft from the model failed", "id", in.ID, "err", userMessage(err))
		note = "The model could not write a draft (" + userMessage(err) + "), so this one holds the facts and timeline only."
	} else {
		p, actions = drafted, acts
	}
	by := fallback(actorName(r), "someone")
	p.UpdatedBy, p.UpdatedAt = by, nowTS()
	if err := s.savePostmortem(ctx, in.ID, p); err != nil {
		s.fail(w, err)
		return
	}
	existing, err := s.incidentActions(ctx, in.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	for _, a := range actions {
		dup := false
		for _, e := range existing {
			dup = dup || strings.EqualFold(e.Body, a.Body)
		}
		if !dup {
			if err := s.addAction(ctx, in.ID, a.Body, a.Owner); err != nil {
				s.fail(w, err)
				return
			}
		}
	}
	if _, err := s.addIncidentEntry(ctx, in.ID, "postmortem", by, "Drafted the postmortem.", map[string]any{"drafted": p.Drafted}); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "incident.postmortem.draft", in.Title, map[string]any{"id": in.ID, "drafted": p.Drafted})
	all, err := s.incidentActions(ctx, in.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"postmortem": p, "actions": all, "note": note})
}

func (s *Server) modelPostmortem(ctx context.Context, c *llm, in *declaredIncident, entries []incidentEntry, links []incidentLink, evidence string) (postmortem, []incidentAction, error) {
	ctx, cancel := context.WithTimeout(ctx, postmortemBudget)
	defer cancel()
	prompt := `You write blameless incident postmortems for ` + s.conf("app_name") + `. You get an incident's facts, its timeline (UTC) and the traffic of the affected services.
Reply with one JSON object: {"summary": "", "impact": "", "root_cause": "", "timeline": "", "lessons": "", "actions": [{"text": "", "owner": ""}]}.
- summary: 2 to 4 sentences: what happened, how long, how it was fixed.
- impact: who and what was affected, with the numbers you were given.
- root_cause: the most likely cause the timeline and data support. Say "likely" where it is not certain; if nothing points anywhere, say what is still unknown.
- timeline: the key moments as Markdown bullets "- Jan 2 15:04 UTC — what happened", in order, at most 15.
- lessons: what went well and what did not, as short Markdown bullets.
- actions: up to 6 concrete follow-ups that would prevent or shorten a repeat. owner is a responder's name when one clearly fits, otherwise "".
- Plain sentences, no headings, no SQL. Blame no one.`
	var b strings.Builder
	fmt.Fprintf(&b, "Title: %s\nSeverity: %s\nStatus: %s\nServices: %s\nImpact as declared: %s\n", in.Title, severityLabel(in.Severity), in.Status, strings.Join(in.Services, ", "), in.Impact)
	fmt.Fprintf(&b, "Started: %s\nDeclared: %s\nResolved: %s\nTime to acknowledge: %s, to mitigate: %s, to resolve: %s\n",
		in.StartedAt, in.DeclaredAt, fallback(in.ResolvedAt, "not yet"), durationText(in.TTA), durationText(in.TTM), durationText(in.TTR))
	for _, r := range in.Roles {
		fmt.Fprintf(&b, "Role: %s is %s\n", r.User, r.Role)
	}
	for _, l := range links {
		fmt.Fprintf(&b, "Linked %s: %s\n", linkKindName(l.Kind), l.Label)
	}
	b.WriteString("\nTimeline:\n" + truncate(timelineText(entries), 12000) + "\n\n" + evidence)
	content, err := c.chatJSON(ctx, []chatMessage{{Role: "system", Content: prompt}, {Role: "user", Content: b.String()}})
	if err != nil {
		return postmortem{}, nil, err
	}
	var out struct {
		Summary   string `json:"summary"`
		Impact    string `json:"impact"`
		RootCause string `json:"root_cause"`
		Timeline  string `json:"timeline"`
		Lessons   string `json:"lessons"`
		Actions   []struct {
			Text  string `json:"text"`
			Owner string `json:"owner"`
		} `json:"actions"`
	}
	if err := json.Unmarshal([]byte(content), &out); err != nil {
		return postmortem{}, nil, fmt.Errorf("the model's answer was not readable")
	}
	p := postmortem{Summary: truncate(out.Summary, maxPostmortemField), Impact: truncate(out.Impact, maxPostmortemField), RootCause: truncate(out.RootCause, maxPostmortemField),
		Timeline: truncate(out.Timeline, maxPostmortemField), Lessons: truncate(out.Lessons, maxPostmortemField), Drafted: "ai"}
	var acts []incidentAction
	for _, a := range head(out.Actions, 6) {
		if t := strings.TrimSpace(a.Text); t != "" {
			acts = append(acts, incidentAction{Body: truncate(t, 500), Owner: truncate(strings.TrimSpace(a.Owner), 255)})
		}
	}
	return p, acts, nil
}

func (s *Server) handleSavePostmortem(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "edit") {
		return
	}
	in, ok := s.incidentFromPath(w, r)
	if !ok {
		return
	}
	var p postmortem
	if err := readJSON(r, &p); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	for _, f := range []string{p.Summary, p.Impact, p.RootCause, p.Timeline, p.Lessons} {
		if len(f) > maxPostmortemField {
			writeError(w, http.StatusBadRequest, "each part of the postmortem must be under 20000 characters")
			return
		}
	}
	by := fallback(actorName(r), "someone")
	p.Drafted, p.UpdatedBy, p.UpdatedAt = "", by, nowTS()
	if err := s.savePostmortem(r.Context(), in.ID, p); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "incident.postmortem.save", in.Title, map[string]any{"id": in.ID})
	writeJSON(w, http.StatusOK, p)
}

type actionInput struct {
	Body  *string `json:"body"`
	Owner *string `json:"owner"`
	Done  *bool   `json:"done"`
}

func (a actionInput) check() string {
	if a.Body != nil && (strings.TrimSpace(*a.Body) == "" || len(*a.Body) > 500) {
		return "an action item must be 1–500 characters"
	}
	if a.Owner != nil && len(*a.Owner) > 255 {
		return "the owner's name is too long"
	}
	return ""
}

func (s *Server) handleAddAction(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "edit") {
		return
	}
	in, ok := s.incidentFromPath(w, r)
	if !ok {
		return
	}
	var a actionInput
	if err := readJSON(r, &a); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if a.Body == nil {
		writeError(w, http.StatusBadRequest, "say what needs doing")
		return
	}
	if msg := a.check(); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	owner := ""
	if a.Owner != nil {
		owner = strings.TrimSpace(*a.Owner)
	}
	if err := s.addAction(r.Context(), in.ID, strings.TrimSpace(*a.Body), owner); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "incident.action.create", in.Title, map[string]any{"id": in.ID, "action": *a.Body, "owner": owner})
	s.writeActions(w, r, in.ID)
}

func (s *Server) writeActions(w http.ResponseWriter, r *http.Request, id int64) {
	list, err := s.incidentActions(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"actions": list})
}

func (s *Server) handleEditAction(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "edit") {
		return
	}
	in, ok := s.incidentFromPath(w, r)
	if !ok {
		return
	}
	aid, ok := incidentID(r, "aid")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid action id")
		return
	}
	var a actionInput
	if err := readJSON(r, &a); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if msg := a.check(); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	var sets []string
	var args []any
	if a.Body != nil {
		sets, args = append(sets, "body = ?"), append(args, strings.TrimSpace(*a.Body))
	}
	if a.Owner != nil {
		sets, args = append(sets, "owner = ?"), append(args, strings.TrimSpace(*a.Owner))
	}
	if a.Done != nil {
		doneAt := ""
		if *a.Done {
			doneAt = nowTS()
		}
		sets, args = append(sets, "done = ?", "done_at = ?"), append(args, boolInt(*a.Done), doneAt)
	}
	if len(sets) > 0 {
		if err := s.db.Exec(r.Context(), `UPDATE incident_actions SET `+strings.Join(sets, ", ")+` WHERE id = ? AND incident_id = ?`, append(args, aid, in.ID)...); err != nil {
			s.fail(w, err)
			return
		}
		s.audit(r, "incident.action.update", in.Title, map[string]any{"id": in.ID, "action": aid, "done": a.Done})
	}
	s.writeActions(w, r, in.ID)
}

func (s *Server) handleRemoveAction(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "edit") {
		return
	}
	in, ok := s.incidentFromPath(w, r)
	if !ok {
		return
	}
	aid, ok := incidentID(r, "aid")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid action id")
		return
	}
	if err := s.db.Exec(r.Context(), `DELETE FROM incident_actions WHERE id = ? AND incident_id = ?`, aid, in.ID); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "incident.action.delete", in.Title, map[string]any{"id": in.ID, "action": aid})
	s.writeActions(w, r, in.ID)
}

func postmortemMarkdown(in *declaredIncident, p *postmortem, actions []incidentAction, entries []incidentEntry) string {
	if p == nil {
		p = &postmortem{}
	}
	var b strings.Builder
	when := func(ts string) string {
		if t, err := time.Parse(tsLayout, ts); err == nil {
			return t.Format("2006-01-02 15:04 UTC")
		}
		return "—"
	}
	fmt.Fprintf(&b, "# Postmortem: %s\n\n", in.Title)
	fmt.Fprintf(&b, "| | |\n|---|---|\n| Severity | %s |\n| Status | %s |\n", severityLabel(in.Severity), incidentStatusLabels[in.Status])
	if len(in.Services) > 0 {
		fmt.Fprintf(&b, "| Services | %s |\n", strings.Join(in.Services, ", "))
	}
	fmt.Fprintf(&b, "| Started | %s |\n| Declared | %s |\n| Resolved | %s |\n", when(in.StartedAt), when(in.DeclaredAt), when(in.ResolvedAt))
	fmt.Fprintf(&b, "| Time to acknowledge | %s |\n| Time to mitigate | %s |\n| Time to resolve | %s |\n", durationText(in.TTA), durationText(in.TTM), durationText(in.TTR))
	for _, r := range in.Roles {
		fmt.Fprintf(&b, "| %s | %s |\n", strings.ToUpper(r.Role[:1])+r.Role[1:], r.User)
	}
	section := func(title, body, empty string) {
		fmt.Fprintf(&b, "\n## %s\n\n%s\n", title, fallback(strings.TrimSpace(body), empty))
	}
	section("Summary", p.Summary, "_Not written yet._")
	section("Impact", p.Impact, "_Not written yet._")
	section("Root cause", p.RootCause, "_Not written yet._")
	section("Timeline", fallback(p.Timeline, timelineText(entries)), "_No entries._")
	section("Lessons learned", p.Lessons, "_Not written yet._")
	b.WriteString("\n## Action items\n\n")
	if len(actions) == 0 {
		b.WriteString("_None yet._\n")
	}
	for _, a := range actions {
		box := " "
		if a.Done {
			box = "x"
		}
		owner := ""
		if a.Owner != "" {
			owner = " — " + a.Owner
		}
		fmt.Fprintf(&b, "- [%s] %s%s\n", box, a.Body, owner)
	}
	return b.String()
}

func (s *Server) handlePostmortemMarkdown(w http.ResponseWriter, r *http.Request) {
	in, ok := s.incidentFromPath(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	links, err := s.incidentLinks(ctx, in.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	entries, err := s.incidentTimeline(ctx, in, links)
	if err != nil {
		s.fail(w, err)
		return
	}
	actions, err := s.incidentActions(ctx, in.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="incident-%d-postmortem.md"`, in.ID))
	_, _ = w.Write([]byte(postmortemMarkdown(in, parsePostmortem(in.postmortem), actions, entries)))
}
