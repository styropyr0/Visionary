package analytics

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"
)

// An incident is one firing episode of a rule series, from the firing event
// to the resolved one. It carries what the series state does not: who
// acknowledged it, how far it has escalated, and whether its firing was
// held back (so its resolution is too).

type alertIncident struct {
	ID             int64  `json:"id"`
	RuleID         int64  `json:"rule_id"`
	SeriesKey      string `json:"series_key"`
	OpenedAt       string `json:"opened_at"`
	EventID        int64  `json:"event_id"`
	AckedBy        string `json:"acked_by,omitempty"`
	AckedAt        string `json:"acked_at,omitempty"`
	AckNote        string `json:"ack_note,omitempty"`
	EscalationStep int    `json:"escalation_step"`
	Silent         bool   `json:"silent"`
}

func (s *Server) openIncident(ctx context.Context, ruleID int64, key, at string, eventID int64, silent bool) error {
	// A series fires once per episode; a stale open row (a crash between the
	// firing and its resolution) is closed first.
	if err := s.resolveIncident(ctx, ruleID, key, at); err != nil {
		return err
	}
	_, err := s.db.InsertID(ctx, `INSERT INTO alert_incidents (rule_id, series_key, opened_at, resolved_at, event_id, acked_by, acked_at, ack_note, escalation_step, silent)
VALUES (?, ?, ?, '', ?, '', '', '', 0, ?)`, ruleID, key, at, eventID, boolInt(silent))
	return err
}

func (s *Server) resolveIncident(ctx context.Context, ruleID int64, key, at string) error {
	return s.db.Exec(ctx, `UPDATE alert_incidents SET resolved_at = ? WHERE rule_id = ? AND series_key = ? AND resolved_at = ''`, at, ruleID, key)
}

// openIncidents loads the unresolved incidents of a rule (every rule for 0),
// by rule id and series key.
func (s *Server) openIncidents(ctx context.Context, ruleID int64) (map[int64]map[string]*alertIncident, error) {
	sql := `SELECT id, rule_id, series_key, opened_at, event_id, acked_by, acked_at, ack_note, escalation_step, silent FROM alert_incidents WHERE resolved_at = ''`
	var args []any
	if ruleID > 0 {
		sql += ` AND rule_id = ?`
		args = append(args, ruleID)
	}
	res, err := s.db.Query(ctx, sql+` ORDER BY id`, args...)
	if err != nil {
		if missingTable(err, "alert_incidents") {
			return map[int64]map[string]*alertIncident{}, nil
		}
		return nil, err
	}
	out := map[int64]map[string]*alertIncident{}
	for _, row := range res.Rows {
		if len(row) != 10 {
			continue
		}
		inc := &alertIncident{ID: toInt(row[0]), RuleID: toInt(row[1]), SeriesKey: toString(row[2]), OpenedAt: toString(row[3]),
			EventID: toInt(row[4]), AckedBy: toString(row[5]), AckedAt: toString(row[6]), AckNote: toString(row[7]),
			EscalationStep: int(toInt(row[8])), Silent: toInt(row[9]) == 1}
		if out[inc.RuleID] == nil {
			out[inc.RuleID] = map[string]*alertIncident{}
		}
		out[inc.RuleID][inc.SeriesKey] = inc
	}
	return out, nil
}

func (s *Server) setEscalationStep(ctx context.Context, id int64, step int) error {
	return s.db.Exec(ctx, `UPDATE alert_incidents SET escalation_step = ? WHERE id = ?`, step, id)
}

// escalate sends the next due escalation step of an unacknowledged firing
// series, at most one step per evaluation.
func (e *alertEngine) escalate(ctx context.Context, r alertRule, st *seriesState, inc *alertIncident, now time.Time) {
	if inc == nil || inc.AckedAt != "" || inc.Silent || inc.EscalationStep >= len(r.Spec.Escalation) {
		return
	}
	opened, err := time.Parse(tsLayout, inc.OpenedAt)
	if err != nil {
		return
	}
	step := r.Spec.Escalation[inc.EscalationStep]
	after, _ := parseSpan(step.After)
	if now.Sub(opened) < after {
		return
	}
	next := inc.EscalationStep + 1
	if err := e.s.setEscalationStep(ctx, inc.ID, next); err != nil {
		e.s.logger.Warn("alerts: escalation step", "err", err)
		return
	}
	inc.EscalationStep = next
	line := fmt.Sprintf("Firing for %s without an acknowledgement (escalation step %d of %d). %s",
		humanDuration(now.Sub(opened)), next, len(r.Spec.Escalation), st.Message)
	n := e.s.followUpNotice(r, st.Key, st.Label, "escalation", line)
	ids := []int64{inc.EventID}
	go func() {
		defer e.recoverLog("escalate")
		ctx, cancel := context.WithTimeout(e.ctx, 60*time.Second)
		defer cancel()
		e.s.deliver(ctx, r.ID, ids, step.Channels, n)
	}()
}

// notifiedChannels are the channels that have heard of an incident: the
// rule's own and those of the escalation steps sent so far.
func notifiedChannels(r alertRule, inc *alertIncident) []int64 {
	seen := map[int64]bool{}
	var out []int64
	add := func(ids []int64) {
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	add(r.Channels)
	for i := 0; i < inc.EscalationStep && i < len(r.Spec.Escalation); i++ {
		add(r.Spec.Escalation[i].Channels)
	}
	return out
}

type ackInput struct {
	// SeriesKey picks one series; empty acknowledges every firing series of
	// the rule.
	SeriesKey *string `json:"series_key"`
	Note      string  `json:"note"`
}

// handleAckRule acknowledges a rule's firing series: reminders and
// escalations stop until it resolves, and the channels that were told hear
// who took it.
func (s *Server) handleAckRule(w http.ResponseWriter, r *http.Request) {
	s.ackRule(w, r, true)
}

func (s *Server) handleUnackRule(w http.ResponseWriter, r *http.Request) {
	s.ackRule(w, r, false)
}

func (s *Server) ackRule(w http.ResponseWriter, r *http.Request, ack bool) {
	if !s.can(r, "edit") {
		s.forbidden(w)
		return
	}
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid rule id")
		return
	}
	var in ackInput
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(in.Note) > 500 {
		writeError(w, http.StatusBadRequest, "note must be under 500 characters")
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
	open, err := s.openIncidents(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	var picked []*alertIncident
	for key, inc := range open[id] {
		if in.SeriesKey != nil && *in.SeriesKey != key {
			continue
		}
		if ack == (inc.AckedAt == "") {
			picked = append(picked, inc)
		}
	}
	sort.Slice(picked, func(i, j int) bool { return picked[i].SeriesKey < picked[j].SeriesKey })
	if len(picked) == 0 {
		msg := "nothing is firing to acknowledge"
		if !ack {
			msg = "nothing acknowledged to undo"
		}
		writeError(w, http.StatusConflict, msg)
		return
	}
	by, now := fallback(actorName(r), "someone"), time.Now().UTC().Format(tsLayout)
	for _, inc := range picked {
		if ack {
			err = s.db.Exec(r.Context(), `UPDATE alert_incidents SET acked_by = ?, acked_at = ?, ack_note = ? WHERE id = ?`, by, now, in.Note, inc.ID)
		} else {
			err = s.db.Exec(r.Context(), `UPDATE alert_incidents SET acked_by = '', acked_at = '', ack_note = '' WHERE id = ?`, inc.ID)
		}
		if err != nil {
			s.fail(w, err)
			return
		}
	}
	if ack {
		e := s.alertEngine()
		labels := map[string]string{}
		if states, err := s.ruleStates(r.Context(), rule.ID); err == nil {
			for _, st := range states[rule.ID] {
				labels[st.Key] = st.Label
			}
		}
		for _, inc := range picked {
			if inc.Silent {
				continue
			}
			line := "Acknowledged by " + by + "."
			if in.Note != "" {
				line += " " + in.Note
			}
			n := s.followUpNotice(*rule, inc.SeriesKey, labels[inc.SeriesKey], "ack", line)
			chans, ids := notifiedChannels(*rule, inc), []int64{inc.EventID}
			go func() {
				defer e.recoverLog("ack")
				ctx, cancel := context.WithTimeout(e.ctx, 60*time.Second)
				defer cancel()
				s.deliver(ctx, rule.ID, ids, chans, n)
			}()
		}
	}
	keys := make([]string, 0, len(picked))
	for _, inc := range picked {
		keys = append(keys, inc.SeriesKey)
	}
	action := "alert.rule.ack"
	if !ack {
		action = "alert.rule.unack"
	}
	s.audit(r, action, rule.Name, map[string]any{"id": id, "series": keys, "note": in.Note})
	writeJSON(w, http.StatusOK, map[string]any{"series": keys, "acked_by": map[bool]string{true: by}[ack]})
}
