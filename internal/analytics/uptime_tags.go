package analytics

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	sdk "github.com/styropyr0/visionary/sdks/go"
)

// Tags (smoke, checkout, deploy) group checks for filtering and for CI runs.
// They live in uptime_check_tags, one row per tag.

const uptimeMaxTags = 20

var tagRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.:-]{0,39}$`)

func init() {
	scopedTables["UPTIME_CHECK_TAGS"] = scopeRule{by: byCheck}
}

func (s *Server) uptimeTagsSchema() []string {
	key, tag, ine := "INTEGER", "TEXT", "IF NOT EXISTS "
	tail := ""
	switch s.q.d {
	case sdk.Postgres:
		key = "BIGINT"
	case sdk.MySQL:
		key, tag, ine, tail = "BIGINT", "VARCHAR(64)", "", " CHARACTER SET utf8mb4"
	}
	return []string{`CREATE TABLE IF NOT EXISTS uptime_check_tags (
    check_id ` + key + ` NOT NULL,
    tag      ` + tag + ` NOT NULL,
    PRIMARY KEY (check_id, tag)
)` + tail + `;
CREATE INDEX ` + ine + `idx_uptime_check_tags_tag ON uptime_check_tags (tag)`}
}

// normalizeTags lowercases, checks and sorts tags, dropping repeats.
func normalizeTags(in []string) ([]string, error) {
	out := []string{}
	for _, t := range in {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || slices.Contains(out, t) {
			continue
		}
		if !tagRe.MatchString(t) {
			return nil, fmt.Errorf("tag %q: use up to 40 letters, digits, and _ . : -", t)
		}
		out = append(out, t)
	}
	if len(out) > uptimeMaxTags {
		return nil, fmt.Errorf("at most %d tags", uptimeMaxTags)
	}
	slices.Sort(out)
	return out, nil
}

func (s *Server) loadCheckTags(ctx context.Context, checks []UptimeCheck) error {
	for i := range checks {
		checks[i].Tags = []string{}
	}
	if len(checks) == 0 {
		return nil
	}
	q, args := `SELECT check_id, tag FROM uptime_check_tags`, []any{}
	if len(checks) == 1 {
		q, args = q+` WHERE check_id = ?`, []any{checks[0].ID}
	}
	rows, err := s.db.Query(ctx, q+` ORDER BY tag`, args...)
	if err != nil {
		if missingTable(err, "uptime_check_tags") {
			return nil
		}
		return err
	}
	byID := map[int64][]string{}
	for _, row := range rows.Rows {
		if len(row) == 2 {
			id := toInt(row[0])
			byID[id] = append(byID[id], toString(row[1]))
		}
	}
	for i := range checks {
		if t, ok := byID[checks[i].ID]; ok {
			checks[i].Tags = t
		}
	}
	return nil
}

func (s *Server) saveCheckTags(ctx context.Context, c UptimeCheck) error {
	if err := s.db.Exec(ctx, `DELETE FROM uptime_check_tags WHERE check_id = ?`, c.ID); err != nil {
		return err
	}
	if len(c.Tags) == 0 {
		return nil
	}
	marks := make([]string, len(c.Tags))
	args := make([]any, 0, 2*len(c.Tags))
	for i, t := range c.Tags {
		marks[i] = "(?, ?)"
		args = append(args, c.ID, t)
	}
	return s.db.Exec(ctx, `INSERT INTO uptime_check_tags (check_id, tag) VALUES `+strings.Join(marks, ", "), args...)
}

func (s *Server) deleteCheckTags(ctx context.Context, id int64) {
	if err := s.db.Exec(ctx, `DELETE FROM uptime_check_tags WHERE check_id = ?`, id); err != nil && !missingTable(err, "uptime_check_tags") {
		s.logger.Warn("check tags not deleted", "check_id", id, "err", err)
	}
}

func checksTagged(checks []UptimeCheck, tags []string, ids []int64) []UptimeCheck {
	var out []UptimeCheck
	for _, c := range checks {
		hit := slices.Contains(ids, c.ID)
		for _, t := range c.Tags {
			hit = hit || slices.Contains(tags, t)
		}
		if hit {
			out = append(out, c)
		}
	}
	return out
}
