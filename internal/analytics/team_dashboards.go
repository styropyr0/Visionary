package analytics

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Custom dashboards: saved and built-in panels on a 12-column grid, with
// instance, route and service variables, stored as one JSON layout per row.

type dashboard struct {
	ID          int64           `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Layout      dashboardLayout `json:"layout"`
	CreatedBy   string          `json:"created_by"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
}

type dashboardLayout struct {
	Panels    []dashPanel `json:"panels"`
	Variables []dashVar   `json:"variables"`
	// Time overrides the global range for this dashboard.
	Time *dashTime `json:"time,omitempty"`
}

// dashPanel is a saved panel (Ref is its id) or a built-in one (Ref is
// "overview/latency", "system/cpu" and so on), placed in grid units.
type dashPanel struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Ref   string `json:"ref"`
	Title string `json:"title,omitempty"`
	X     int    `json:"x"`
	Y     int    `json:"y"`
	W     int    `json:"w"`
	H     int    `json:"h"`
}

type dashVar struct {
	Name    string `json:"name"`
	Default string `json:"default,omitempty"`
}

type dashTime struct {
	Kind string `json:"kind"`
	Key  string `json:"key,omitempty"`
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

const (
	dashCols       = 12
	maxDashPanels  = 60
	maxDashNameLen = 120
	maxDashDescLen = 500
)

var (
	panelIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)
	rangeKeys      = []string{"15m", "1h", "6h", "24h", "7d", "30d"}
)

// Dashboard variables. Values are checked against a strict pattern per
// variable (no quotes, backslashes or semicolons), then written as a quoted
// literal; the rendered statement still goes through validateReadOnly.
var (
	dashVarPatterns = map[string]*regexp.Regexp{
		"instance": instanceName,
		"service":  regexp.MustCompile(`^[A-Za-z0-9._:/@-]{1,128}$`),
		"route":    regexp.MustCompile(`^[A-Za-z0-9/_.:{}*~@+$!,=<>()\[\] -]{1,256}$`),
	}
	// {{var.route}} is the value as a string literal ('' when unset);
	// {{filter.route}} is " AND route = '...'", or nothing when unset.
	dashPlaceholder = regexp.MustCompile(`\{\{\s*(var|filter)\.([A-Za-z_]+)\s*\}\}`)
)

func dashVarNames() []string { return []string{"instance", "route", "service"} }

// renderVariables substitutes dashboard variables into sql. A placeholder
// inside a string literal ('%{{var.route}}%') gets the bare value, which the
// patterns keep free of quotes and backslashes; anywhere else it becomes a
// quoted literal.
func renderVariables(sql string, values map[string]string) (string, error) {
	var out strings.Builder
	// A backslash in a literal ends it in SQLite and PostgreSQL but escapes
	// the next quote in MySQL, so after one the quoting is ambiguous and
	// placeholders are refused rather than guessed.
	inString, inIdent, ambiguous, last := false, false, false, 0
	scan := func(seg string) {
		for _, c := range seg {
			switch {
			case c == '\\' && inString:
				ambiguous = true
			case c == '\'' && !inIdent:
				inString = !inString
			case c == '"' && !inString:
				inIdent = !inIdent
			}
		}
	}
	for _, m := range dashPlaceholder.FindAllStringSubmatchIndex(sql, -1) {
		scan(sql[last:m[0]])
		out.WriteString(sql[last:m[0]])
		last = m[1]
		form, name := sql[m[2]:m[3]], strings.ToLower(sql[m[4]:m[5]])
		pattern, ok := dashVarPatterns[name]
		if !ok {
			return "", fmt.Errorf("unknown variable %q: use instance, route or service", name)
		}
		v := values[name]
		if v != "" && !pattern.MatchString(v) {
			return "", fmt.Errorf("invalid value for %s", name)
		}
		switch {
		case ambiguous:
			return "", errors.New("variables can't follow a string literal that contains a backslash")
		case inIdent:
			return "", fmt.Errorf("{{%s.%s}} can't be used inside a quoted name", form, name)
		case form == "filter" && inString:
			return "", fmt.Errorf("{{filter.%s}} can't be quoted", name)
		case form == "filter" && v != "":
			out.WriteString(" AND " + name + " = " + sqlString(v))
		case form == "filter":
		case inString:
			out.WriteString(v)
		default:
			out.WriteString(sqlString(v))
		}
	}
	out.WriteString(sql[last:])
	return out.String(), nil
}

// sqlString quotes a validated value. Doubling quotes is defense in depth:
// the patterns already exclude them.
func sqlString(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }

func variableValues(q map[string][]string) map[string]string {
	out := map[string]string{}
	for _, name := range dashVarNames() {
		if v := q["var."+name]; len(v) > 0 {
			out[name] = strings.TrimSpace(v[0])
		}
	}
	return out
}

func (s *Server) validateDashboard(d *dashboard) error {
	d.Name = strings.TrimSpace(d.Name)
	if d.Name == "" || len(d.Name) > maxDashNameLen {
		return fmt.Errorf("name must be 1–%d characters", maxDashNameLen)
	}
	if len(d.Description) > maxDashDescLen {
		return errors.New("description is too long")
	}
	l := &d.Layout
	if len(l.Panels) > maxDashPanels {
		return fmt.Errorf("a dashboard holds at most %d panels", maxDashPanels)
	}
	seen := map[string]bool{}
	for i := range l.Panels {
		p := &l.Panels[i]
		if !panelIDPattern.MatchString(p.ID) || seen[p.ID] {
			return errors.New("each panel needs a unique id")
		}
		seen[p.ID] = true
		switch p.Kind {
		case "saved":
			if _, err := strconv.ParseInt(p.Ref, 10, 64); err != nil {
				return errors.New("saved panel ref must be a panel id")
			}
		case "builtin":
			if !s.builtinExists(p.Ref) {
				return fmt.Errorf("unknown built-in panel %q", p.Ref)
			}
		default:
			return errors.New("panel kind must be saved or builtin")
		}
		if len(p.Title) > maxTitleLen {
			return errors.New("panel title is too long")
		}
		if p.W < 2 || p.W > dashCols || p.X < 0 || p.X+p.W > dashCols || p.H < 2 || p.H > 24 || p.Y < 0 || p.Y > 1000 {
			return errors.New("panel position is outside the grid")
		}
	}
	vars := map[string]bool{}
	for _, v := range l.Variables {
		pattern, ok := dashVarPatterns[v.Name]
		if !ok || vars[v.Name] {
			return errors.New("variables must be instance, route or service, each once")
		}
		vars[v.Name] = true
		if v.Default != "" && !pattern.MatchString(v.Default) {
			return fmt.Errorf("invalid default for %s", v.Name)
		}
	}
	if t := l.Time; t != nil {
		switch t.Kind {
		case "relative":
			if !slices.Contains(rangeKeys, t.Key) {
				return errors.New("unknown relative range")
			}
			t.From, t.To = "", ""
		case "absolute":
			from, err1 := time.Parse(time.RFC3339Nano, t.From)
			to, err2 := time.Parse(time.RFC3339Nano, t.To)
			if err1 != nil || err2 != nil || !to.After(from) {
				return errors.New("time range needs from before to, as RFC 3339")
			}
			t.Key = ""
		default:
			return errors.New("time kind must be relative or absolute")
		}
	}
	if l.Panels == nil {
		l.Panels = []dashPanel{}
	}
	if l.Variables == nil {
		l.Variables = []dashVar{}
	}
	return nil
}

func (s *Server) builtinExists(ref string) bool {
	group, name, ok := strings.Cut(ref, "/")
	if !ok {
		return false
	}
	var m map[string]string
	switch group {
	case "overview":
		m = s.overview
	case "performance":
		m = s.performance()
	case "system":
		m = s.system
	}
	_, ok = m[name]
	return ok
}

const dashColumns = `id, name, description, layout, created_by, created_at, updated_at`

func scanDashboard(row []any) (dashboard, bool) {
	if len(row) != 7 {
		return dashboard{}, false
	}
	d := dashboard{
		ID: toInt(row[0]), Name: toString(row[1]), Description: toString(row[2]),
		CreatedBy: toString(row[4]), CreatedAt: toString(row[5]), UpdatedAt: toString(row[6]),
	}
	if json.Unmarshal([]byte(toString(row[3])), &d.Layout) != nil || d.Layout.Panels == nil {
		d.Layout.Panels = []dashPanel{}
	}
	if d.Layout.Variables == nil {
		d.Layout.Variables = []dashVar{}
	}
	return d, true
}

func (s *Server) handleListDashboards(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), `SELECT `+dashColumns+` FROM dashboards ORDER BY name, id`)
	if err != nil {
		s.fail(w, err)
		return
	}
	list := make([]dashboard, 0, len(rows.Rows))
	for _, row := range rows.Rows {
		if d, ok := scanDashboard(row); ok {
			list = append(list, d)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"dashboards": list})
}

func (s *Server) loadDashboard(w http.ResponseWriter, r *http.Request) (dashboard, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid dashboard id")
		return dashboard{}, false
	}
	rows, err := s.db.Query(r.Context(), `SELECT `+dashColumns+` FROM dashboards WHERE id = ?`, id)
	if err != nil {
		s.fail(w, err)
		return dashboard{}, false
	}
	if len(rows.Rows) == 0 {
		writeError(w, http.StatusNotFound, "dashboard not found")
		return dashboard{}, false
	}
	d, ok := scanDashboard(rows.Rows[0])
	if !ok {
		writeError(w, http.StatusNotFound, "dashboard not found")
	}
	return d, ok
}

func (s *Server) handleGetDashboard(w http.ResponseWriter, r *http.Request) {
	if d, ok := s.loadDashboard(w, r); ok {
		writeJSON(w, http.StatusOK, d)
	}
}

type dashboardInput struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Layout      dashboardLayout `json:"layout"`
}

func (s *Server) readDashboard(w http.ResponseWriter, r *http.Request) (dashboard, string, bool) {
	var in dashboardInput
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return dashboard{}, "", false
	}
	d := dashboard{Name: in.Name, Description: strings.TrimSpace(in.Description), Layout: in.Layout}
	if err := s.validateDashboard(&d); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return dashboard{}, "", false
	}
	layout, err := json.Marshal(d.Layout)
	if err != nil {
		s.fail(w, err)
		return dashboard{}, "", false
	}
	return d, string(layout), true
}

func (s *Server) handleCreateDashboard(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "edit") {
		return
	}
	d, layout, ok := s.readDashboard(w, r)
	if !ok {
		return
	}
	sess := sessionFrom(r)
	now := time.Now().UTC().Format(tsLayout)
	d.CreatedBy, d.CreatedAt, d.UpdatedAt = fallback(sess.Name, sess.UserID), now, now
	id, err := s.db.InsertID(r.Context(), `INSERT INTO dashboards (name, description, layout, created_by, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)`, d.Name, d.Description, layout, d.CreatedBy, d.CreatedAt, d.UpdatedAt)
	if err != nil {
		s.fail(w, err)
		return
	}
	d.ID = id
	s.audit(r, "dashboard.create", strconv.FormatInt(id, 10), map[string]any{"name": d.Name, "panels": len(d.Layout.Panels)})
	writeJSON(w, http.StatusCreated, d)
}

func (s *Server) handleUpdateDashboard(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "edit") {
		return
	}
	cur, ok := s.loadDashboard(w, r)
	if !ok {
		return
	}
	d, layout, ok := s.readDashboard(w, r)
	if !ok {
		return
	}
	d.ID, d.CreatedBy, d.CreatedAt, d.UpdatedAt = cur.ID, cur.CreatedBy, cur.CreatedAt, time.Now().UTC().Format(tsLayout)
	if err := s.db.Exec(r.Context(), `UPDATE dashboards SET name = ?, description = ?, layout = ?, updated_at = ? WHERE id = ?`,
		d.Name, d.Description, layout, d.UpdatedAt, d.ID); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "dashboard.update", strconv.FormatInt(d.ID, 10), map[string]any{"name": d.Name, "panels": len(d.Layout.Panels)})
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleDeleteDashboard(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "edit") {
		return
	}
	d, ok := s.loadDashboard(w, r)
	if !ok {
		return
	}
	if err := s.db.Exec(r.Context(), `DELETE FROM dashboards WHERE id = ?`, d.ID); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, "dashboard.delete", strconv.FormatInt(d.ID, 10), map[string]any{"name": d.Name})
	w.WriteHeader(http.StatusNoContent)
}

// handlePanelData runs a saved panel with dashboard variables from the
// query string (var.instance, var.route, var.service).
func (s *Server) handlePanelData(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid panel id")
		return
	}
	q := r.URL.Query()
	tr, err := parseRange(q, s.maxRangeDays())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT query FROM analytics_panels WHERE id = ?`, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if len(rows.Rows) == 0 || len(rows.Rows[0]) != 1 {
		writeError(w, http.StatusNotFound, "panel not found")
		return
	}
	sql, err := renderVariables(toString(rows.Rows[0][0]), variableValues(q))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := s.runTemplate(r.Context(), sql, tr, false)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleVariableValues lists the values a variable can take in the range.
func (s *Server) handleVariableValues(w http.ResponseWriter, r *http.Request) {
	tr, err := parseRange(r.URL.Query(), s.maxRangeDays())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var sql string
	switch r.PathValue("name") {
	case "instance":
		sql = `SELECT instance FROM system_metrics WHERE created_at >= ? AND created_at < ? GROUP BY instance ORDER BY instance LIMIT 200`
	case "route":
		sql = `SELECT route FROM api_logs WHERE created_at >= ? AND created_at < ? AND route IS NOT NULL GROUP BY route ORDER BY COUNT(*) DESC LIMIT 200`
	case "service":
		sql = `SELECT service FROM api_logs WHERE created_at >= ? AND created_at < ? AND service IS NOT NULL GROUP BY service ORDER BY service LIMIT 200`
	default:
		writeError(w, http.StatusNotFound, "unknown variable")
		return
	}
	values := []string{}
	rows, err := s.db.Query(r.Context(), sql, tr.From.Format(tsLayout), tr.To.Format(tsLayout))
	if err != nil {
		if !missingTable(err, "system_metrics") {
			s.fail(w, err)
			return
		}
	} else {
		for _, row := range rows.Rows {
			if len(row) == 1 {
				if v := toString(row[0]); v != "" {
					values = append(values, v)
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"values": values})
}
