package analytics

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
)

// Your own tables in Explore: when the application's tables share the
// database, an admin can let Explore and the analyst read some of them
// (Settings → Server → AI analyst → explore_tables), so a question can join
// logs and requests with users, orders or plans. Read-only like everything
// Explore runs; Visionary's tables that hold secrets never qualify.

var ownTables atomic.Pointer[map[string]bool]

// neverExplore are Visionary's tables with credentials, keys, accounts or
// who did what; naming them in the setting does nothing.
var neverExplore = map[string]bool{
	"VISIONARY_SETTINGS": true, "VISIONARY_API_KEYS": true, "VISIONARY_USERS": true, "ALERT_CHANNELS": true, "AUDIT_LOG": true,
}

var tableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// setOwnTables reads the comma-separated setting.
func setOwnTables(list string) {
	m := map[string]bool{}
	for _, t := range strings.Split(list, ",") {
		t = strings.ToUpper(strings.TrimSpace(t))
		if !tableName.MatchString(t) || neverExplore[t] || strings.HasPrefix(t, "VISIONARY_") {
			continue
		}
		bad := false
		for _, p := range deniedPrefixes {
			if strings.HasPrefix(t, p) {
				bad = true
			}
		}
		if !bad {
			m[t] = true
		}
	}
	ownTables.Store(&m)
}

// ownTable reports whether a table of the application may be read.
func ownTable(name string) bool {
	m := ownTables.Load()
	return m != nil && (*m)[name]
}

// ownTablesPrompt describes the application's tables Explore may read,
// with their columns, for the analyst.
func (s *Server) ownTablesPrompt(ctx context.Context) string {
	m := ownTables.Load()
	if m == nil || len(*m) == 0 {
		return ""
	}
	names := make([]string, 0, len(*m))
	for t := range *m {
		names = append(names, strings.ToLower(t))
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("Tables of the application itself, which you may read and join with the tables above (by user_id = the table's id, for example):")
	for _, t := range names {
		cols := []string{}
		if res, err := s.db.Query(ctx, s.q.d.ColumnsQuery(t)); err == nil {
			for _, row := range res.Rows {
				if len(row) > 0 {
					cols = append(cols, toString(row[0]))
				}
			}
		}
		if len(cols) == 0 {
			continue
		}
		b.WriteString("\n- " + t + "(" + strings.Join(cols, ", ") + ")")
	}
	return b.String()
}
