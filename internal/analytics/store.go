package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/styropyr0/visionary/sdk"
)

// Saved panels live next to api_logs in the same D1 database, so every viewer
// of the dashboard sees the same set.
const panelsSchema = `CREATE TABLE IF NOT EXISTS analytics_panels (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    title       TEXT    NOT NULL,
    description TEXT    NOT NULL DEFAULT '',
    prompt      TEXT    NOT NULL DEFAULT '',
    query       TEXT    NOT NULL,
    viz         TEXT    NOT NULL,
    created_at  TEXT    NOT NULL
)`

type Panel struct {
	ID          int64     `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Prompt      string    `json:"prompt"`
	Query       string    `json:"query"`
	Viz         Viz       `json:"viz"`
	CreatedAt   time.Time `json:"created_at"`
}

const (
	maxTitleLen  = 120
	maxDescLen   = 500
	maxPromptLen = 2000
)

// ensureSchema creates Visionary's own table and the two the SDK writes, so
// dashboards load (empty) before any service reports in.
func (s *Server) ensureSchema(ctx context.Context) error {
	for _, schema := range []string{panelsSchema, sdk.RequestsSchema, sdk.SystemSchema} {
		if err := s.db.Exec(ctx, schema); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) listPanels(ctx context.Context) ([]Panel, error) {
	rows, err := s.db.Query(ctx, `SELECT id, title, description, prompt, query, viz, created_at
FROM analytics_panels ORDER BY id`)
	if err != nil {
		return nil, err
	}
	panels := make([]Panel, 0, len(rows.Rows))
	for _, row := range rows.Rows {
		if len(row) != 7 {
			continue
		}
		p := Panel{
			ID:          toInt(row[0]),
			Title:       toString(row[1]),
			Description: toString(row[2]),
			Prompt:      toString(row[3]),
			Query:       toString(row[4]),
		}
		_ = json.Unmarshal([]byte(toString(row[5])), &p.Viz)
		p.CreatedAt, _ = time.Parse(tsLayout, toString(row[6]))
		panels = append(panels, p)
	}
	return panels, nil
}

func (s *Server) createPanel(ctx context.Context, p Panel) (*Panel, error) {
	p.Title = strings.TrimSpace(p.Title)
	if p.Title == "" || len(p.Title) > maxTitleLen {
		return nil, &queryError{fmt.Errorf("title must be 1–%d characters", maxTitleLen)}
	}
	if len(p.Description) > maxDescLen || len(p.Prompt) > maxPromptLen {
		return nil, &queryError{errors.New("description or prompt is too long")}
	}
	checked, err := validateReadOnly(p.Query)
	if err != nil {
		return nil, &queryError{err}
	}
	p.Query = checked
	vizJSON, err := json.Marshal(p.Viz)
	if err != nil {
		return nil, err
	}
	p.CreatedAt = time.Now().UTC()

	rows, err := s.db.Query(ctx, `INSERT INTO analytics_panels (title, description, prompt, query, viz, created_at)
VALUES (?, ?, ?, ?, ?, ?) RETURNING id`,
		p.Title, p.Description, p.Prompt, p.Query, string(vizJSON), p.CreatedAt.Format(tsLayout))
	if err != nil {
		return nil, err
	}
	if len(rows.Rows) == 1 && len(rows.Rows[0]) == 1 {
		p.ID = toInt(rows.Rows[0][0])
	}
	return &p, nil
}

func (s *Server) deletePanel(ctx context.Context, id int64) error {
	return s.db.Exec(ctx, `DELETE FROM analytics_panels WHERE id = ?`, strconv.FormatInt(id, 10))
}

// knownRoutes lists recent route patterns for the model's prompt and the logs
// filter, cached for ten minutes.
func (s *Server) knownRoutes(ctx context.Context) []string {
	s.routesMu.Lock()
	defer s.routesMu.Unlock()
	if time.Since(s.routesAt) < 10*time.Minute && s.routes != nil {
		return s.routes
	}
	since := time.Now().UTC().Add(-7 * 24 * time.Hour).Format(tsLayout)
	rows, err := s.db.Query(ctx, `SELECT route FROM api_logs
WHERE created_at >= ? AND route IS NOT NULL
GROUP BY route ORDER BY COUNT(*) DESC LIMIT 100`, since)
	if err != nil {
		s.logger.Warn("load known routes", "err", err)
		return s.routes
	}
	routes := make([]string, 0, len(rows.Rows))
	for _, row := range rows.Rows {
		if len(row) == 1 {
			if r := toString(row[0]); r != "" {
				routes = append(routes, r)
			}
		}
	}
	s.routes, s.routesAt = routes, time.Now()
	return routes
}

func toString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return fmt.Sprint(t)
	}
}

func toInt(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case string:
		n, _ := strconv.ParseInt(t, 10, 64)
		return n
	}
	return 0
}
