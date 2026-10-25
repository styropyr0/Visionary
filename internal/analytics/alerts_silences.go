package analytics

import (
	"context"
	"encoding/json"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Silences hold back notifications for matching alerts: a maintenance
// window (once, between two times) or quiet hours (weekly, on some days
// between two clock times in a zone). Rules keep evaluating and recording
// activity while silenced, as with a muted rule; the event says which
// silence held it back.

type alertSilence struct {
	ID        int64           `json:"id"`
	Name      string          `json:"name"`
	Matchers  silenceMatchers `json:"matchers"`
	Schedule  silenceSchedule `json:"schedule"`
	Enabled   bool            `json:"enabled"`
	CreatedBy string          `json:"created_by"`
	CreatedAt string          `json:"created_at"`
	UpdatedAt string          `json:"updated_at"`
}

// silenceMatchers are ANDed; an empty set matches every alert.
type silenceMatchers struct {
	RuleIDs    []int64           `json:"rule_ids,omitempty"`
	Severities []string          `json:"severities,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
	// Series is a series key (an instance, check, host or container), with *
	// as a wildcard: web-* matches web-1 and web-2.
	Series string `json:"series,omitempty"`
	// Rules names rules instead of RuleIDs, in exports only.
	Rules []string `json:"rules,omitempty"`
}

type silenceSchedule struct {
	Kind string `json:"kind"` // once or weekly
	// once
	StartsAt string `json:"starts_at,omitempty"` // RFC 3339
	EndsAt   string `json:"ends_at,omitempty"`
	// weekly: Days are 0 (Sunday) to 6; To before From spans midnight.
	Days []int  `json:"days,omitempty"`
	From string `json:"from,omitempty"` // HH:MM
	To   string `json:"to,omitempty"`
	TZ   string `json:"tz,omitempty"`
}

type silenceView struct {
	alertSilence
	// State is active, scheduled (a later window, or not this time of the
	// week) or ended.
	State string `json:"state"`
}

var clockTime = regexp.MustCompile(`^([01]\d|2[0-3]):([0-5]\d)$`)

func clockMinutes(v string) int {
	m := clockTime.FindStringSubmatch(v)
	if m == nil {
		return -1
	}
	h, _ := strconv.Atoi(m[1])
	mm, _ := strconv.Atoi(m[2])
	return h*60 + mm
}

// active reports whether the schedule covers t.
func (sc silenceSchedule) active(t time.Time) bool {
	switch sc.Kind {
	case "once":
		from, err1 := time.Parse(time.RFC3339, sc.StartsAt)
		to, err2 := time.Parse(time.RFC3339, sc.EndsAt)
		return err1 == nil && err2 == nil && !t.Before(from) && t.Before(to)
	case "weekly":
		loc := time.UTC
		if sc.TZ != "" {
			if l, err := time.LoadLocation(sc.TZ); err == nil {
				loc = l
			}
		}
		local := t.In(loc)
		day, m := int(local.Weekday()), local.Hour()*60+local.Minute()
		from, to := clockMinutes(sc.From), clockMinutes(sc.To)
		has := func(d int) bool {
			for _, x := range sc.Days {
				if x == (d+7)%7 {
					return true
				}
			}
			return false
		}
		if from < to {
			return has(day) && m >= from && m < to
		}
		// Overnight: from this day's evening into the next morning.
		return (has(day) && m >= from) || (has(day-1) && m < to)
	}
	return false
}

func (sc silenceSchedule) ended(t time.Time) bool {
	if sc.Kind != "once" {
		return false
	}
	to, err := time.Parse(time.RFC3339, sc.EndsAt)
	return err == nil && !t.Before(to)
}

func (m silenceMatchers) match(r alertRule, series string) bool {
	if len(m.RuleIDs) > 0 && !containsID(m.RuleIDs, r.ID) {
		return false
	}
	if len(m.Severities) > 0 {
		ok := false
		for _, s := range m.Severities {
			ok = ok || s == r.Severity
		}
		if !ok {
			return false
		}
	}
	for k, v := range m.Labels {
		if r.Spec.Labels[k] != v {
			return false
		}
	}
	if m.Series != "" {
		ok, err := path.Match(m.Series, series)
		if err != nil || !ok {
			return false
		}
	}
	return true
}

func containsID(ids []int64, id int64) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

func (in *alertSilence) check() error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > maxTitleLen {
		return badInput("name must be 1–120 characters")
	}
	m := &in.Matchers
	m.Rules = nil
	for _, s := range m.Severities {
		if !severities[s] {
			return badInput("severities must be info, warning or critical")
		}
	}
	if len(m.Labels) > 20 || len(m.RuleIDs) > 200 || len(m.Series) > 200 {
		return badInput("too many matchers")
	}
	if err := checkLabels(m.Labels); err != nil {
		return err
	}
	if _, err := path.Match(m.Series, ""); err != nil {
		return badInput("series pattern is not valid")
	}
	sc := &in.Schedule
	switch sc.Kind {
	case "once":
		from, err1 := time.Parse(time.RFC3339, sc.StartsAt)
		to, err2 := time.Parse(time.RFC3339, sc.EndsAt)
		if err1 != nil || err2 != nil {
			return badInput("start and end must be RFC 3339 times")
		}
		if !to.After(from) {
			return badInput("the window must end after it starts")
		}
		if to.Sub(from) > 90*24*time.Hour {
			return badInput("a window can last at most 90 days")
		}
		sc.StartsAt, sc.EndsAt = from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339)
		sc.Days, sc.From, sc.To, sc.TZ = nil, "", "", ""
	case "weekly":
		if len(sc.Days) == 0 || len(sc.Days) > 7 {
			return badInput("pick at least one day")
		}
		for _, d := range sc.Days {
			if d < 0 || d > 6 {
				return badInput("days are 0 (Sunday) to 6")
			}
		}
		if clockMinutes(sc.From) < 0 || clockMinutes(sc.To) < 0 || sc.From == sc.To {
			return badInput("from and to must be different HH:MM times")
		}
		if sc.TZ != "" {
			if _, err := time.LoadLocation(sc.TZ); err != nil {
				return badInput("unknown time zone " + sc.TZ)
			}
		}
		sc.StartsAt, sc.EndsAt = "", ""
	default:
		return badInput("schedule kind must be once or weekly")
	}
	return nil
}

var labelName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,62}$`)

func checkLabels(labels map[string]string) error {
	if len(labels) > 20 {
		return badInput("at most 20 labels")
	}
	for k, v := range labels {
		if !labelName.MatchString(k) {
			return badInput("label names start with a letter and use letters, digits, _ . or -")
		}
		if len(v) > 200 {
			return badInput("label values are limited to 200 characters")
		}
	}
	return nil
}

const silenceColumns = `id, name, matchers, schedule, enabled, created_by, created_at, updated_at`

func (s *Server) listSilences(ctx context.Context) ([]alertSilence, error) {
	res, err := s.db.Query(ctx, `SELECT `+silenceColumns+` FROM alert_silences ORDER BY id`)
	if err != nil {
		return nil, err
	}
	out := make([]alertSilence, 0, len(res.Rows))
	for _, row := range res.Rows {
		if len(row) != 8 {
			continue
		}
		sl := alertSilence{ID: toInt(row[0]), Name: toString(row[1]), Enabled: toInt(row[4]) == 1,
			CreatedBy: toString(row[5]), CreatedAt: toString(row[6]), UpdatedAt: toString(row[7])}
		_ = json.Unmarshal([]byte(toString(row[2])), &sl.Matchers)
		_ = json.Unmarshal([]byte(toString(row[3])), &sl.Schedule)
		out = append(out, sl)
	}
	return out, nil
}

func (s *Server) saveSilence(ctx context.Context, sl *alertSilence) error {
	m, _ := json.Marshal(sl.Matchers)
	sc, _ := json.Marshal(sl.Schedule)
	now := time.Now().UTC().Format(tsLayout)
	sl.UpdatedAt = now
	if sl.ID == 0 {
		sl.CreatedAt = now
		id, err := s.db.InsertID(ctx, `INSERT INTO alert_silences (name, matchers, schedule, enabled, created_by, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			sl.Name, string(m), string(sc), boolInt(sl.Enabled), sl.CreatedBy, now, now)
		sl.ID = id
		return err
	}
	return s.db.Exec(ctx, `UPDATE alert_silences SET name = ?, matchers = ?, schedule = ?, enabled = ?, updated_at = ? WHERE id = ?`,
		sl.Name, string(m), string(sc), boolInt(sl.Enabled), now, sl.ID)
}

// silencedBy names the enabled silence covering a rule series at t, or "".
func silencedBy(list []alertSilence, r alertRule, series string, t time.Time) string {
	for _, sl := range list {
		if sl.Enabled && sl.Schedule.active(t) && sl.Matchers.match(r, series) {
			return sl.Name
		}
	}
	return ""
}

func silenceState(sl alertSilence, now time.Time) string {
	switch {
	case !sl.Enabled:
		return "disabled"
	case sl.Schedule.active(now):
		return "active"
	case sl.Schedule.ended(now):
		return "ended"
	}
	return "scheduled"
}

func (s *Server) handleListSilences(w http.ResponseWriter, r *http.Request) {
	list, err := s.listSilences(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	now := time.Now().UTC()
	out := make([]silenceView, 0, len(list))
	for _, sl := range list {
		out = append(out, silenceView{alertSilence: sl, State: silenceState(sl, now)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"silences": out})
}

type silenceInput struct {
	Name     string          `json:"name"`
	Matchers silenceMatchers `json:"matchers"`
	Schedule silenceSchedule `json:"schedule"`
	Enabled  *bool           `json:"enabled"`
}

func (in silenceInput) apply(sl *alertSilence) error {
	sl.Name, sl.Matchers, sl.Schedule = in.Name, in.Matchers, in.Schedule
	sl.Enabled = in.Enabled == nil || *in.Enabled
	return sl.check()
}

func (s *Server) handleCreateSilence(w http.ResponseWriter, r *http.Request) {
	if !s.can(r, "edit") {
		s.forbidden(w)
		return
	}
	var in silenceInput
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sl := alertSilence{CreatedBy: actorName(r)}
	if err := in.apply(&sl); err != nil {
		s.fail(w, err)
		return
	}
	if err := s.saveSilence(r.Context(), &sl); err != nil {
		s.fail(w, err)
		return
	}
	s.alertEngine().invalidateSilences()
	s.audit(r, "alert.silence.create", sl.Name, map[string]any{"id": sl.ID, "schedule": sl.Schedule})
	writeJSON(w, http.StatusCreated, silenceView{alertSilence: sl, State: silenceState(sl, time.Now().UTC())})
}

func (s *Server) silenceFromPath(w http.ResponseWriter, r *http.Request) (*alertSilence, bool) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid silence id")
		return nil, false
	}
	list, err := s.listSilences(r.Context())
	if err != nil {
		s.fail(w, err)
		return nil, false
	}
	for _, sl := range list {
		if sl.ID == id {
			return &sl, true
		}
	}
	writeError(w, http.StatusNotFound, "silence not found")
	return nil, false
}

func (s *Server) handleUpdateSilence(w http.ResponseWriter, r *http.Request) {
	if !s.can(r, "edit") {
		s.forbidden(w)
		return
	}
	sl, ok := s.silenceFromPath(w, r)
	if !ok {
		return
	}
	var in silenceInput
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := in.apply(sl); err != nil {
		s.fail(w, err)
		return
	}
	if err := s.saveSilence(r.Context(), sl); err != nil {
		s.fail(w, err)
		return
	}
	s.alertEngine().invalidateSilences()
	s.audit(r, "alert.silence.update", sl.Name, map[string]any{"id": sl.ID, "enabled": sl.Enabled, "schedule": sl.Schedule})
	writeJSON(w, http.StatusOK, silenceView{alertSilence: *sl, State: silenceState(*sl, time.Now().UTC())})
}

func (s *Server) handleDeleteSilence(w http.ResponseWriter, r *http.Request) {
	if !s.can(r, "edit") {
		s.forbidden(w)
		return
	}
	sl, ok := s.silenceFromPath(w, r)
	if !ok {
		return
	}
	if err := s.db.Exec(r.Context(), `DELETE FROM alert_silences WHERE id = ?`, sl.ID); err != nil {
		s.fail(w, err)
		return
	}
	s.alertEngine().invalidateSilences()
	s.audit(r, "alert.silence.delete", sl.Name, map[string]any{"id": sl.ID})
	w.WriteHeader(http.StatusNoContent)
}

// actorName is who made a request, for history and acknowledgements.
func actorName(r *http.Request) string {
	sess := sessionFrom(r)
	if sess == nil {
		return ""
	}
	return clip(fallback(sess.Name, sess.Handle), 255)
}
