package analytics

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	sdk "github.com/styropyr0/visionary/sdks/go"

	"github.com/styropyr0/visionary/internal/db"
)

// A request scoped to workspaces carries a *wsScope in its context, and
// scopedDB rewrites every statement run with that context: each table that
// holds data of services is read through a subquery that keeps only the
// allowed services' rows. Every read goes through s.db, so no handler has to
// remember to filter. Rows without a service are left out, so only people
// who see every service see them.

type scopeKey struct{}

type wsScope struct {
	// patterns are the service names and globs ("payments-*") the request may read.
	patterns []string
	// ids are the workspaces in effect, which decide the alerts, uptime
	// checks, dashboards and saved views shown.
	ids []int64
	// home is where alerts, checks, dashboards and views created now belong.
	home int64
	// role is what the workspaces let the person do; roleNone keeps their own.
	role role
	d    sdk.Dialect

	once  sync.Once
	preds map[string]string
}

func scopeFrom(ctx context.Context) *wsScope {
	sc, _ := ctx.Value(scopeKey{}).(*wsScope)
	return sc
}

func withScope(ctx context.Context, sc *wsScope) context.Context {
	return context.WithValue(ctx, scopeKey{}, sc)
}

// key tells scopes apart in caches; it is empty without one.
func (sc *wsScope) key() string {
	if sc == nil {
		return ""
	}
	return strings.Join(sc.patterns, "\n") + "\x00" + fmt.Sprint(sc.ids)
}

// unscoped drops a scope, for reads that decide the scope itself.
func unscoped(ctx context.Context) context.Context {
	return context.WithValue(ctx, scopeKey{}, (*wsScope)(nil))
}

type scopeBy int

const (
	byService   scopeBy = iota // the table's own service column
	byInstance                 // rollup_system: instances that reported an allowed service
	byHost                     // host tables: hosts whose inventory names an allowed service
	byIncident                 // incident details: incidents that affect an allowed service
	byRule                     // alert history: rules of the workspaces
	byCheck                    // uptime results: checks of the workspaces
	byTarget                   // EXPLAIN plans: databases an allowed service queries
	byReplay                   // replay chunks: sessions of an allowed app
	ownedOnly                  // alert rules and uptime checks: the workspaces' own
	ownedShared                // dashboards and saved views: the workspaces' own and unassigned ones
	affected                   // incidents: their affected services
	hidden                     // nothing; the rows can't be tied to a service
)

type scopeRule struct {
	by  scopeBy
	col string
}

// scopedTables are the tables read through a filter in a scoped request, by
// upper-case name. Any other table is shared metadata (settings, channels,
// silences, saved panels) or admin-only.
var scopedTables = map[string]scopeRule{
	"API_LOGS": {byService, "service"}, "SPANS": {byService, "service"}, "ERRORS": {byService, "service"},
	"RELEASES": {byService, "service"}, "SYSTEM_METRICS": {byService, "service"}, "APP_LOGS": {byService, "service"},
	"METRICS": {byService, "service"}, "PROFILES": {byService, "service"}, "LOG_PATTERNS": {byService, "service"},
	"CI_PIPELINES": {byService, "service"}, "CI_JOBS": {byService, "service"}, "CI_TESTS": {byService, "service"},
	"CI_FLAKY_TESTS":  {byService, "service"},
	"ROLLUP_REQUESTS": {byService, "service"}, "HOST_INVENTORY": {byService, "service"},
	"SECURITY_EVENTS": {byService, "service"}, "CLIENT_EVENTS": {byService, "app"},
	"REPLAY_SESSIONS": {byService, "app"}, "REPLAY_CHUNKS": {by: byReplay},
	"ROLLUP_SYSTEM":  {by: byInstance},
	"HOST_PROCESSES": {by: byHost}, "HOST_MOUNTS": {by: byHost}, "HOST_UNITS": {by: byHost},
	"HOST_CONTAINERS": {by: byHost}, "CONTAINER_METRICS": {by: byHost}, "CONTAINER_EVENTS": {by: byHost},
	"INCIDENTS":      {by: affected},
	"INCIDENT_ROLES": {by: byIncident}, "INCIDENT_TIMELINE": {by: byIncident}, "INCIDENT_LINKS": {by: byIncident},
	"INCIDENT_ACTIONS": {by: byIncident},
	"ALERT_RULES":      {by: ownedOnly}, "UPTIME_CHECKS": {by: ownedOnly},
	"ALERT_STATE": {by: byRule}, "ALERT_EVENTS": {by: byRule}, "ALERT_INCIDENTS": {by: byRule},
	"ALERT_DELIVERIES": {by: byRule}, "ALERT_RULE_HISTORY": {by: byRule},
	"UPTIME_RESULTS": {by: byCheck}, "UPTIME_BROWSER_CHECKS": {by: byCheck}, "UPTIME_BROWSER_RESULTS": {by: byCheck},
	"DASHBOARDS": {by: ownedShared}, "LOG_VIEWS": {by: ownedShared},
	"EXPLAIN_REQUESTS": {by: byTarget},
	"LOG_ARCHIVES":     {by: hidden}, "SECURITY_KNOWN": {by: hidden},
	// Detection rules span every service; their signals are security_events.
	"SECURITY_RULES": {by: hidden},
}

// ownedTables get the workspace of whoever creates a row in them.
var ownedTables = map[string]bool{"ALERT_RULES": true, "UPTIME_CHECKS": true, "DASHBOARDS": true, "LOG_VIEWS": true}

// sharedInScope are the unfiltered tables people in a workspace may still
// name in their own SQL.
var sharedInScope = map[string]bool{"ANALYTICS_PANELS": true}

// pred is the filter for table (upper case), and whether the table has one.
func (sc *wsScope) pred(table string) (string, bool) {
	sc.once.Do(sc.buildPreds)
	p, ok := sc.preds[table]
	return p, ok
}

func (sc *wsScope) buildPreds() {
	sc.preds = make(map[string]string, len(scopedTables))
	owned := "1 = 0"
	shared := "workspace_id IS NULL"
	if len(sc.ids) > 0 {
		list := make([]string, len(sc.ids))
		for i, id := range sc.ids {
			list[i] = strconv.FormatInt(id, 10)
		}
		owned = "workspace_id IN (" + strings.Join(list, ", ") + ")"
		shared = "(workspace_id IS NULL OR " + owned + ")"
	}
	for table, r := range scopedTables {
		var p string
		switch r.by {
		case byService:
			p = sc.serviceMatch(r.col)
		case byInstance:
			p = "instance IN (SELECT instance FROM system_metrics WHERE " + sc.serviceMatch("service") + ")"
		case byHost:
			p = "host IN (SELECT host FROM host_inventory WHERE " + sc.serviceMatch("service") + ")"
		case affected:
			p = sc.affectedMatch()
		case byIncident:
			p = "incident_id IN (SELECT id FROM incidents WHERE " + sc.affectedMatch() + ")"
		case ownedOnly:
			p = owned
		case ownedShared:
			p = shared
		case byRule:
			p = "rule_id IN (SELECT id FROM alert_rules WHERE " + owned + ")"
		case byCheck:
			p = "check_id IN (SELECT id FROM uptime_checks WHERE " + owned + ")"
		case byReplay:
			p = "session_id IN (SELECT session_id FROM replay_sessions WHERE " + sc.serviceMatch("app") + ")"
		case byTarget:
			p = "target IN (SELECT target FROM spans WHERE " + sc.serviceMatch("service") + ")"
		default:
			p = "1 = 0"
		}
		sc.preds[table] = p
	}
}

// binary makes a MySQL comparison case-sensitive, as it is elsewhere.
func (sc *wsScope) binary(col string) string {
	if sc.d == sdk.MySQL {
		return col + " COLLATE utf8mb4_bin"
	}
	return col
}

// like matches col against a glob whose only wildcard is *.
func (sc *wsScope) like(col, glob string) string {
	if sc.d == sdk.SQLite {
		return col + " GLOB '" + glob + "'"
	}
	pat := strings.NewReplacer("_", "!_", "*", "%").Replace(glob)
	return sc.binary(col) + " LIKE '" + pat + "' ESCAPE '!'"
}

// serviceMatch keeps rows whose col is an allowed service. Patterns are
// checked against servicePattern when saved, so they quote safely.
func (sc *wsScope) serviceMatch(col string) string {
	var exact, parts []string
	for _, p := range sc.patterns {
		switch {
		case p == "*":
			parts = append(parts, "("+col+" IS NOT NULL AND "+col+" <> '')")
		case strings.Contains(p, "*"):
			parts = append(parts, sc.like(col, p))
		default:
			exact = append(exact, "'"+p+"'")
		}
	}
	if len(exact) > 0 {
		parts = append(parts, sc.binary(col)+" IN ("+strings.Join(exact, ", ")+")")
	}
	if len(parts) == 0 {
		return "1 = 0"
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}

// affectedMatch keeps incidents naming an allowed service. services is a
// JSON list of names, which can't hold quotes, so a quote marks where each
// name starts and ends.
func (sc *wsScope) affectedMatch() string {
	var parts []string
	for _, p := range sc.patterns {
		if p == "*" {
			parts = append(parts, "services <> '[]'")
			continue
		}
		core := strings.Trim(p, "*")
		glob := "*" + core + "*"
		if !strings.HasPrefix(p, "*") {
			glob = "*\"" + strings.TrimPrefix(glob, "*")
		}
		if !strings.HasSuffix(p, "*") {
			glob = strings.TrimSuffix(glob, "*") + "\"*"
		}
		parts = append(parts, sc.like("services", glob))
	}
	if len(parts) == 0 {
		return "1 = 0"
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}

// rewrite reads every scoped table of a SELECT through its filter. untrusted
// is SQL from a person or the model, which may name only scoped tables, the
// shared ones and its own CTEs.
func (sc *wsScope) rewrite(sql string, untrusted bool) (string, error) {
	toks, err := scanTokens(sql, !untrusted)
	if err != nil {
		return "", err
	}
	refs, err := tableRefs(toks)
	if err != nil {
		return "", err
	}
	var ctes map[string]bool
	if untrusted {
		ctes = cteNames(toks)
	}
	var b strings.Builder
	last := 0
	for _, i := range refs {
		t := toks[i]
		next := ""
		if i+1 < len(toks) {
			next = toks[i+1].text
		}
		switch {
		case next == "(":
			continue // a table function
		case next == ".":
			if i+2 < len(toks) {
				if _, ok := scopedTables[toks[i+2].text]; ok || untrusted {
					return "", fmt.Errorf("%s can't be read with a schema name inside a workspace", strings.ToLower(toks[i+2].text))
				}
			}
			continue
		case i >= 2 && toks[i-2].text == "DELETE":
			continue
		}
		p, ok := sc.pred(t.text)
		if !ok {
			if untrusted && !ctes[t.text] && !sharedInScope[t.text] {
				return "", fmt.Errorf("%s can't be read inside a workspace", strings.ToLower(t.text))
			}
			continue
		}
		name := sql[t.pos:t.end]
		b.WriteString(sql[last:t.pos])
		b.WriteString("(SELECT * FROM " + name + " WHERE " + p + ")")
		if !hasAlias(toks, i) {
			b.WriteString(" " + name)
		}
		last = t.end
	}
	b.WriteString(sql[last:])
	return b.String(), nil
}

// aliasStops are words that can follow a table reference without being its alias.
var aliasStops = map[string]bool{
	"WHERE": true, "GROUP": true, "ORDER": true, "LIMIT": true, "OFFSET": true, "HAVING": true, "WINDOW": true,
	"UNION": true, "EXCEPT": true, "INTERSECT": true, "JOIN": true, "INNER": true, "LEFT": true, "RIGHT": true,
	"FULL": true, "CROSS": true, "NATURAL": true, "OUTER": true, "ON": true, "USING": true, "FOR": true,
	"FETCH": true, "SET": true, "VALUES": true, "RETURNING": true, "LATERAL": true,
}

func hasAlias(toks []token, i int) bool {
	if i+1 >= len(toks) {
		return false
	}
	t := toks[i+1]
	return t.kind == tokName || t.kind == tokWord && !aliasStops[t.text]
}

// cteNames are the names a WITH clause defines: WITH [RECURSIVE] name
// [(cols)] AS [[NOT] MATERIALIZED] (...), repeated after commas.
func cteNames(toks []token) map[string]bool {
	out := map[string]bool{}
	for i := range toks {
		if toks[i].kind != tokWord || toks[i].text != "WITH" {
			continue
		}
		j := i + 1
		if j < len(toks) && toks[j].text == "RECURSIVE" {
			j++
		}
		for j < len(toks) && isIdent(toks[j]) {
			name := toks[j].text
			j++
			if j < len(toks) && toks[j].text == "(" {
				j = closeParen(toks, j) + 1
			}
			if j >= len(toks) || toks[j].text != "AS" {
				break
			}
			j++
			for j < len(toks) && (toks[j].text == "NOT" || toks[j].text == "MATERIALIZED") {
				j++
			}
			if j >= len(toks) || toks[j].text != "(" {
				break
			}
			out[name] = true
			j = closeParen(toks, j) + 1
			if j >= len(toks) || toks[j].text != "," {
				break
			}
			j++
		}
	}
	return out
}

// closeParen is the index of the parenthesis closing the one at open.
func closeParen(toks []token, open int) int {
	depth := 0
	for k := open; k < len(toks); k++ {
		switch toks[k].text {
		case "(":
			depth++
		case ")":
			if depth--; depth == 0 {
				return k
			}
		}
	}
	return len(toks)
}

// restrictWrite limits an UPDATE or DELETE of a scoped table to the rows the
// scope can see, so nobody changes what they can't read.
func (sc *wsScope) restrictWrite(sql string) (string, error) {
	toks, err := scanTokens(sql, true)
	if err != nil {
		return "", err
	}
	target := -1
	switch {
	case len(toks) > 1 && toks[0].text == "UPDATE":
		target = 1
	case len(toks) > 2 && toks[0].text == "DELETE" && toks[1].text == "FROM":
		target = 2
	}
	if target < 0 {
		return sql, nil
	}
	p, ok := sc.pred(toks[target].text)
	if !ok {
		return sql, nil
	}
	depth, where, cut := 0, -1, len(sql)
loop:
	for i := target + 1; i < len(toks); i++ {
		switch t := toks[i]; {
		case t.text == "(":
			depth++
		case t.text == ")":
			depth--
		case depth > 0 || t.kind != tokWord:
		case t.text == "WHERE" && where < 0:
			where = i
		case t.text == "ORDER" || t.text == "LIMIT" || t.text == "RETURNING":
			cut = t.pos
			break loop
		}
	}
	if where < 0 {
		return strings.TrimRight(sql[:cut], " \n\t") + " WHERE " + p + " " + sql[cut:], nil
	}
	cond := strings.TrimSpace(sql[toks[where].end:cut])
	return sql[:toks[where].end] + " (" + cond + ") AND " + p + " " + sql[cut:], nil
}

// ownedInsert is the owned table an INSERT writes to, or "".
func ownedInsert(sql string) string {
	toks, err := scanTokens(sql, true)
	if err != nil || len(toks) < 3 || toks[0].text != "INSERT" || toks[1].text != "INTO" || !ownedTables[toks[2].text] {
		return ""
	}
	return strings.ToLower(toks[2].text)
}

// scopedDB applies the request's workspace scope to every statement.
type scopedDB struct{ db.DB }

func scopeFailed(err error) error {
	return &db.QueryError{Err: fmt.Errorf("workspace filter: %w", err)}
}

func (d scopedDB) Query(ctx context.Context, query string, args ...any) (*db.Rows, error) {
	if sc := scopeFrom(ctx); sc != nil {
		q, err := sc.rewrite(query, false)
		if err != nil {
			return nil, scopeFailed(err)
		}
		query = q
	}
	return d.DB.Query(ctx, query, args...)
}

func (d scopedDB) QueryUntrusted(ctx context.Context, query string) (*db.Rows, error) {
	if sc := scopeFrom(ctx); sc != nil {
		q, err := sc.rewrite(query, true)
		if err != nil {
			return nil, scopeFailed(err)
		}
		query = q
	}
	return d.DB.QueryUntrusted(ctx, query)
}

func (d scopedDB) Exec(ctx context.Context, query string, args ...any) error {
	if sc := scopeFrom(ctx); sc != nil {
		q, err := sc.restrictWrite(query)
		if err != nil {
			return scopeFailed(err)
		}
		query = q
	}
	return d.DB.Exec(ctx, query, args...)
}

func (d scopedDB) InsertID(ctx context.Context, query string, args ...any) (int64, error) {
	id, err := d.DB.InsertID(ctx, query, args...)
	if sc := scopeFrom(ctx); err == nil && sc != nil && sc.home > 0 {
		if table := ownedInsert(query); table != "" {
			err = d.DB.Exec(ctx, `UPDATE `+table+` SET workspace_id = ? WHERE id = ?`, sc.home, id)
		}
	}
	return id, err
}
