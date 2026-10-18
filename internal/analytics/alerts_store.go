package analytics

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// alertRule is one rule as stored in alert_rules. Kind-specific settings live
// in Spec, kept as JSON so new options need no schema change.
type alertRule struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Kind       string    `json:"kind"` // threshold, anomaly, slo
	Severity   string    `json:"severity"`
	Channels   []int64   `json:"channels"`
	Runbook    string    `json:"runbook"`
	Enabled    bool      `json:"enabled"`
	MutedUntil string    `json:"muted_until"`
	Spec       ruleSpec  `json:"spec"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type ruleSpec struct {
	// Metric is a built-in metric id, or "custom" with SQL.
	Metric    string     `json:"metric"`
	SQL       string     `json:"sql,omitempty"`
	Scope     alertScope `json:"scope"`
	Op        string     `json:"op,omitempty"`
	Threshold float64    `json:"threshold"`
	Window    string     `json:"window"`
	For       string     `json:"for"`
	// Repeat re-sends a firing alert this often; empty never repeats.
	Repeat string `json:"repeat,omitempty"`
	// Anomaly rules.
	Sensitivity string `json:"sensitivity,omitempty"`
	Direction   string `json:"direction,omitempty"`
	// SLO rules: percent of requests that succeed within the Apdex threshold.
	Target float64 `json:"target,omitempty"`
}

type alertScope struct {
	Route    string `json:"route,omitempty"`
	Method   string `json:"method,omitempty"`
	Instance string `json:"instance,omitempty"`
	Service  string `json:"service,omitempty"`
	Check    string `json:"check,omitempty"`
	// security_events rules (security_alerts.go): finding kind, minimum
	// severity, source (api or host) and host.
	EventKind     string `json:"event_kind,omitempty"`
	EventSeverity string `json:"event_severity,omitempty"`
	Source        string `json:"source,omitempty"`
	Host          string `json:"host,omitempty"`
}

// seriesState is the state machine of one rule series (one per instance or
// uptime check for rules that fan out, otherwise one with an empty key).
type seriesState struct {
	Key          string       `json:"key"`
	Label        string       `json:"label"`
	State        string       `json:"state"` // ok, pending, firing, resolved
	Since        string       `json:"since"`
	PendingSince string       `json:"pending_since,omitempty"`
	NotifiedAt   string       `json:"notified_at,omitempty"`
	EvaluatedAt  string       `json:"evaluated_at,omitempty"`
	Value        *float64     `json:"value"`
	Threshold    *float64     `json:"threshold"`
	Message      string       `json:"message"`
	Recent       [][2]float64 `json:"recent"`
}

const ruleColumns = `id, name, kind, spec, severity, channels, runbook, enabled, muted_until, created_at, updated_at`

func (s *Server) listRules(ctx context.Context, onlyEnabled bool) ([]alertRule, error) {
	sql := `SELECT ` + ruleColumns + ` FROM alert_rules`
	if onlyEnabled {
		sql += ` WHERE enabled = 1`
	}
	res, err := s.db.Query(ctx, sql+` ORDER BY id`)
	if err != nil {
		return nil, err
	}
	out := make([]alertRule, 0, len(res.Rows))
	for _, row := range res.Rows {
		if r, ok := scanRule(row); ok {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *Server) getRule(ctx context.Context, id int64) (*alertRule, error) {
	res, err := s.db.Query(ctx, `SELECT `+ruleColumns+` FROM alert_rules WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	for _, row := range res.Rows {
		if r, ok := scanRule(row); ok {
			return &r, nil
		}
	}
	return nil, nil
}

func scanRule(row []any) (alertRule, bool) {
	if len(row) != 11 {
		return alertRule{}, false
	}
	r := alertRule{
		ID:         toInt(row[0]),
		Name:       toString(row[1]),
		Kind:       toString(row[2]),
		Severity:   toString(row[4]),
		Runbook:    toString(row[6]),
		Enabled:    toInt(row[7]) == 1,
		MutedUntil: toString(row[8]),
	}
	_ = json.Unmarshal([]byte(toString(row[3])), &r.Spec)
	_ = json.Unmarshal([]byte(toString(row[5])), &r.Channels)
	if r.Channels == nil {
		r.Channels = []int64{}
	}
	r.CreatedAt, _ = time.Parse(tsLayout, toString(row[9]))
	r.UpdatedAt, _ = time.Parse(tsLayout, toString(row[10]))
	return r, true
}

func (s *Server) insertRule(ctx context.Context, r *alertRule) error {
	spec, _ := json.Marshal(r.Spec)
	chans, _ := json.Marshal(r.Channels)
	now := time.Now().UTC()
	id, err := s.db.InsertID(ctx, `INSERT INTO alert_rules (name, kind, spec, severity, channels, runbook, enabled, muted_until, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Name, r.Kind, string(spec), r.Severity, string(chans), r.Runbook, boolInt(r.Enabled), r.MutedUntil,
		now.Format(tsLayout), now.Format(tsLayout))
	if err != nil {
		return err
	}
	r.ID, r.CreatedAt, r.UpdatedAt = id, now, now
	return nil
}

func (s *Server) updateRule(ctx context.Context, r *alertRule) error {
	spec, _ := json.Marshal(r.Spec)
	chans, _ := json.Marshal(r.Channels)
	r.UpdatedAt = time.Now().UTC()
	return s.db.Exec(ctx, `UPDATE alert_rules SET name = ?, kind = ?, spec = ?, severity = ?, channels = ?, runbook = ?,
  enabled = ?, muted_until = ?, updated_at = ? WHERE id = ?`,
		r.Name, r.Kind, string(spec), r.Severity, string(chans), r.Runbook, boolInt(r.Enabled), r.MutedUntil,
		r.UpdatedAt.Format(tsLayout), r.ID)
}

func (s *Server) deleteRule(ctx context.Context, id int64) error {
	if err := s.db.Exec(ctx, `DELETE FROM alert_state WHERE rule_id = ?`, id); err != nil {
		return err
	}
	return s.db.Exec(ctx, `DELETE FROM alert_rules WHERE id = ?`, id)
}

func (s *Server) clearRuleState(ctx context.Context, id int64) error {
	return s.db.Exec(ctx, `DELETE FROM alert_state WHERE rule_id = ?`, id)
}

// ruleStates loads every series state, keyed by rule id.
func (s *Server) ruleStates(ctx context.Context, ruleID int64) (map[int64][]*seriesState, error) {
	sql := `SELECT rule_id, series_key, label, state, state_since, pending_since, notified_at, evaluated_at, value, threshold, message, recent FROM alert_state`
	var args []any
	if ruleID > 0 {
		sql += ` WHERE rule_id = ?`
		args = append(args, ruleID)
	}
	res, err := s.db.Query(ctx, sql+` ORDER BY rule_id, series_key`, args...)
	if err != nil {
		return nil, err
	}
	out := map[int64][]*seriesState{}
	for _, row := range res.Rows {
		if len(row) != 12 {
			continue
		}
		st := &seriesState{
			Key:          toString(row[1]),
			Label:        toString(row[2]),
			State:        toString(row[3]),
			Since:        toString(row[4]),
			PendingSince: toString(row[5]),
			NotifiedAt:   toString(row[6]),
			EvaluatedAt:  toString(row[7]),
			Value:        cellPtr(row[8]),
			Threshold:    cellPtr(row[9]),
			Message:      toString(row[10]),
		}
		_ = json.Unmarshal([]byte(toString(row[11])), &st.Recent)
		id := toInt(row[0])
		out[id] = append(out[id], st)
	}
	return out, nil
}

func (s *Server) saveState(ctx context.Context, ruleID int64, st *seriesState) error {
	recent, _ := json.Marshal(st.Recent)
	sql, args := bindNulls(s.q.upsertState(), []any{
		ruleID, st.Key, st.Label, st.State, st.Since, st.PendingSince, st.NotifiedAt, st.EvaluatedAt,
		floatArg(st.Value), floatArg(st.Threshold), st.Message, string(recent),
	})
	return s.db.Exec(ctx, sql, args...)
}

func (s *Server) deleteState(ctx context.Context, ruleID int64, key string) error {
	return s.db.Exec(ctx, `DELETE FROM alert_state WHERE rule_id = ? AND series_key = ?`, ruleID, key)
}

// alertEvent is one row of alert_events: a series starting to fire or
// resolving.
type alertEvent struct {
	ID          int64    `json:"id"`
	CreatedAt   string   `json:"created_at"`
	RuleID      int64    `json:"rule_id"`
	RuleName    string   `json:"rule_name"`
	Kind        string   `json:"kind"`
	Severity    string   `json:"severity"`
	State       string   `json:"state"`
	SeriesKey   string   `json:"series_key"`
	Value       *float64 `json:"value"`
	Threshold   *float64 `json:"threshold"`
	Message     string   `json:"message"`
	Explanation string   `json:"explanation"`
	Notified    bool     `json:"notified"`
}

func (s *Server) insertEvent(ctx context.Context, ev *alertEvent) error {
	sql, args := bindNulls(`INSERT INTO alert_events (created_at, rule_id, rule_name, kind, severity, state, series_key, value, threshold, message, explanation, notified)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, []any{
		ev.CreatedAt, ev.RuleID, ev.RuleName, ev.Kind, ev.Severity, ev.State, ev.SeriesKey,
		floatArg(ev.Value), floatArg(ev.Threshold), ev.Message, ev.Explanation, boolInt(ev.Notified),
	})
	id, err := s.db.InsertID(ctx, sql, args...)
	if err != nil {
		return err
	}
	ev.ID = id
	return nil
}

func (s *Server) finishEvent(ctx context.Context, id int64, explanation string, notified bool) error {
	return s.db.Exec(ctx, `UPDATE alert_events SET explanation = ?, notified = ? WHERE id = ?`, explanation, boolInt(notified), id)
}

type eventFilter struct {
	From, To time.Time
	RuleID   int64
	Severity string
	State    string
	Limit    int
}

func (s *Server) listEvents(ctx context.Context, f eventFilter) ([]alertEvent, error) {
	where := []string{"created_at >= ?", "created_at < ?"}
	args := []any{f.From.Format(tsLayout), f.To.Format(tsLayout)}
	if f.RuleID > 0 {
		where = append(where, "rule_id = ?")
		args = append(args, f.RuleID)
	}
	if f.Severity != "" {
		where = append(where, "severity = ?")
		args = append(args, f.Severity)
	}
	if f.State != "" {
		where = append(where, "state = ?")
		args = append(args, f.State)
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 200
	}
	res, err := s.db.Query(ctx, `SELECT id, created_at, rule_id, rule_name, kind, severity, state, series_key, value, threshold, message, explanation, notified
FROM alert_events WHERE `+strings.Join(where, " AND ")+` ORDER BY created_at DESC, id DESC LIMIT `+strconv.Itoa(f.Limit), args...)
	if err != nil {
		return nil, err
	}
	out := make([]alertEvent, 0, len(res.Rows))
	for _, row := range res.Rows {
		if len(row) != 13 {
			continue
		}
		out = append(out, alertEvent{
			ID: toInt(row[0]), CreatedAt: toString(row[1]), RuleID: toInt(row[2]), RuleName: toString(row[3]),
			Kind: toString(row[4]), Severity: toString(row[5]), State: toString(row[6]), SeriesKey: toString(row[7]),
			Value: cellPtr(row[8]), Threshold: cellPtr(row[9]), Message: toString(row[10]),
			Explanation: toString(row[11]), Notified: toInt(row[12]) == 1,
		})
	}
	return out, nil
}

// bindNulls writes SQL NULL in place of the placeholders whose argument is
// nil. D1 sends parameters as strings, where nil would arrive as "".
func bindNulls(sql string, args []any) (string, []any) {
	var b strings.Builder
	kept := make([]any, 0, len(args))
	i := 0
	for _, c := range sql {
		if c == '?' && i < len(args) {
			if args[i] == nil {
				b.WriteString("NULL")
			} else {
				b.WriteRune(c)
				kept = append(kept, args[i])
			}
			i++
			continue
		}
		b.WriteRune(c)
	}
	return b.String(), kept
}

func floatArg(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

// cellFloat reads a numeric cell, including NUMERIC sent as text.
func cellFloat(v any) (float64, bool) {
	if f, ok := asFloat(v); ok {
		return f, true
	}
	if s, ok := v.(string); ok && s != "" {
		var f float64
		if err := json.Unmarshal([]byte(s), &f); err == nil {
			return f, true
		}
	}
	return 0, false
}

func cellPtr(v any) *float64 {
	if f, ok := cellFloat(v); ok {
		return &f
	}
	return nil
}

func ptr(f float64) *float64 { return &f }
