package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/styropyr0/visionary/sdks/go"
	"strconv"
	"strings"
	"time"
)

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

// ensureSchema creates Visionary's own tables and the ones the SDKs write, so
// dashboards load (empty) before any service reports in. Every step runs even
// when an earlier one fails, so one bad statement never leaves later tables
// missing; the errors come back together.
func (s *Server) ensureSchema(ctx context.Context) error {
	var errs []error
	// Saved panels live next to api_logs in the same database, so every viewer
	// of the dashboard sees the same set.
	for _, schema := range []string{s.q.panelsSchema(), s.q.d.RequestsSchema(), s.q.d.SystemSchema(), s.q.d.TelemetrySchema()} {
		if err := s.db.ExecScript(ctx, schema); err != nil {
			errs = append(errs, err)
		}
	}
	if err := s.addColumns(ctx, "system_metrics", sdk.AddedSystemColumns); err != nil {
		errs = append(errs, fmt.Errorf("system_metrics columns: %w", err))
	}
	if err := s.addColumns(ctx, "api_logs", sdk.AddedRequestColumns); err != nil {
		errs = append(errs, fmt.Errorf("api_logs columns: %w", err))
	}
	for _, schema := range s.featureSchemas() {
		if err := s.db.ExecScript(ctx, schema); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// retrySchema runs ensureSchema again until it succeeds, for a database that
// was unreachable, over quota or too slow at startup. Tables then appear
// without a restart.
func (s *Server) retrySchema(ctx context.Context) {
	wait := time.Minute
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		sctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		err := s.ensureSchema(sctx)
		cancel()
		if err == nil {
			s.schemaPending.Store(false)
			s.logger.Info("tables created")
			return
		}
		if wait < 15*time.Minute {
			wait *= 2
		}
		s.logger.Warn("create tables failed; retrying", "err", err, "in", wait.String())
	}
}

// addColumns brings a table from an older SDK up to the current columns, so
// the views can read them before every service has upgraded.
func (s *Server) addColumns(ctx context.Context, table string, added []sdk.Column) error {
	d := s.q.d
	res, err := s.db.Query(ctx, d.ColumnsQuery(table))
	if err != nil {
		return err
	}
	var have []string
	for _, row := range res.Rows {
		if len(row) > 0 {
			have = append(have, toString(row[0]))
		}
	}
	for _, stmt := range d.MissingColumns(table, added, have) {
		if err := s.db.Exec(ctx, stmt); err != nil && !sdk.IsDuplicateColumn(err) {
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

	p.ID, err = s.db.InsertID(ctx, `INSERT INTO analytics_panels (title, description, prompt, query, viz, created_at)
VALUES (?, ?, ?, ?, ?, ?)`,
		p.Title, p.Description, p.Prompt, p.Query, string(vizJSON), p.CreatedAt.Format(tsLayout))
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Server) deletePanel(ctx context.Context, id int64) error {
	return s.db.Exec(ctx, `DELETE FROM analytics_panels WHERE id = ?`, id)
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
	case int64:
		return t
	case float64:
		return int64(t)
	case string:
		n, _ := strconv.ParseInt(t, 10, 64)
		return n
	}
	return 0
}

// asFloat reads a numeric cell: D1 returns JSON numbers, SQL drivers int64 or
// float64.
func asFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int64:
		return float64(t), true
	}
	return 0, false
}
