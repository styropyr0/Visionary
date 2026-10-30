package analytics

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	sdk "github.com/styropyr0/visionary/sdks/go"
)

// What a detection rule reads. Each source names its table, the fields a
// rule can group or count by (as SQL expressions that never return NULL, so
// a group's value can be matched again with =), the filters its form offers,
// and the columns a matched event shows with.

type ruleSource struct {
	table  string
	label  string
	fields map[string]string
	// count is what one row adds: host rows stand for several occurrences.
	count string
	// sample is the columns of one matched event, for samples and timelines.
	sample string
}

var ruleSourceNames = []string{"requests", "logs", "events", "audit"}

// attrField is a field of a log line's attrs, as attrs.order_id.
var attrField = regexp.MustCompile(`^attrs\.[A-Za-z0-9_\-]{1,64}$`)

func (q sqlDialect) ruleSources() map[string]ruleSource {
	nn := func(expr string) string { return "COALESCE(" + expr + ", '')" }
	return map[string]ruleSource{
		"requests": {table: "api_logs", label: "Requests", count: "1",
			sample: "created_at, request_id, method, path, status, ip, " + nn(accountExpr(q)) + " AS account, user_agent",
			fields: map[string]string{
				"ip": nn("ip"), "user": nn(accountExpr(q)), "account": nn(loginAccountExpr(q)), "route": nn("COALESCE(route, path)"),
				"path": "path", "method": "method", "status": q.text("status"), "service": nn("service"), "user_agent": nn("user_agent"),
				"role": nn("user_role"), "version": nn("version"), "country": nn("ip"),
			}},
		"logs": {table: "app_logs", label: "App logs", count: "1",
			sample: "created_at, id, level, service, host, message, request_id, trace_id, user_id",
			fields: map[string]string{
				"service": nn("service"), "host": nn("host"), "container": nn("container"), "user": nn("user_id"), "session": nn("session_id"),
				"level": nn("level"), "logger": nn("logger"), "pattern": nn("pattern"), "request": nn("request_id"), "trace": nn("trace_id"),
				"source": nn("source"), "version": nn("version"),
			}},
		"events": {table: "security_events", label: "Security events", count: "count",
			sample: "created_at, id, source, kind, severity, host, actor, ip, target",
			fields: map[string]string{
				"ip": nn("ip"), "user": nn("actor"), "host": nn("host"), "kind": "kind", "target": nn("target"), "service": nn("service"),
				"source": "source", "country": nn("ip"),
			}},
		"audit": {table: "audit_log", label: "Visionary audit log", count: "1",
			sample: "created_at, id, action, user_name, role, target, ip",
			fields: map[string]string{
				"ip": "ip", "user": "user_name", "user_id": "user_id", "role": "role", "action": "action", "target": "target", "country": "ip",
			}},
	}
}

// ruleFilterKeys are the filters each source's form offers.
var ruleFilterKeys = map[string][]string{
	"requests": {"status", "method", "route", "path", "ip", "user", "role", "service", "user_agent", "login", "auth_failed"},
	"logs":     {"query"},
	"events":   {"kind", "source", "host", "user", "ip", "target", "min_severity"},
	"audit":    {"action", "user", "role", "ip", "target"},
}

// fieldExpr is the SQL for a group or count field, or "" when the source has
// no such field.
func (q sqlDialect) fieldExpr(src, field string) string {
	if src == "logs" && attrField.MatchString(field) {
		key := strings.TrimPrefix(field, "attrs.")
		switch q.d {
		case sdk.Postgres:
			return "COALESCE(CAST(attrs AS JSON) ->> " + q.sqlString(key) + ", '')"
		case sdk.MySQL:
			return "COALESCE(JSON_UNQUOTE(JSON_EXTRACT(attrs, " + q.sqlString(`$."`+key+`"`) + ")), '')"
		}
		return "COALESCE(json_extract(attrs, " + q.sqlString(`$."`+key+`"`) + "), '')"
	}
	return q.ruleSources()[src].fields[field]
}

// splitList is a comma list with blanks dropped.
func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// likeList matches expr against globs (* any run); a value without * or /
// matches anywhere. Values are bound.
func likeList(expr string, values []string, args *[]any) string {
	parts := make([]string, 0, len(values))
	for _, v := range values {
		pat := strings.ToLower(v)
		if strings.Contains(pat, "/") && !strings.Contains(pat, "*") {
			pat = wildcard(pat, false)
		} else {
			pat = wildcard(pat, true)
		}
		parts = append(parts, "LOWER("+expr+") LIKE ? ESCAPE '!'")
		*args = append(*args, pat)
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}

func inList(expr string, values []string, args *[]any) string {
	for _, v := range values {
		*args = append(*args, v)
	}
	return expr + " IN (" + placeholders(len(values)) + ")"
}

var statusItem = regexp.MustCompile(`^(>=|<=|>|<|=)?\s*([1-5])(\d\d|xx)$`)

// statusCond reads 401,403 or 4xx or >=500 or <400.
func statusCond(v string, args *[]any) (string, error) {
	var parts []string
	for _, it := range splitList(strings.ToLower(v)) {
		m := statusItem.FindStringSubmatch(it)
		if m == nil {
			return "", badInput("status takes codes, classes or comparisons: 401,403 or 4xx or >=500")
		}
		if m[3] == "xx" {
			if m[1] != "" {
				return "", badInput("a status class like 4xx takes no comparison")
			}
			lo, _ := strconv.Atoi(m[2])
			parts = append(parts, "(status >= ? AND status < ?)")
			*args = append(*args, lo*100, lo*100+100)
			continue
		}
		n, _ := strconv.Atoi(m[2] + m[3])
		parts = append(parts, "status "+fallback(m[1], "=")+" ?")
		*args = append(*args, n)
	}
	if len(parts) == 0 {
		return "", badInput("status is empty")
	}
	return "(" + strings.Join(parts, " OR ") + ")", nil
}

// filterSQL is a rule step's filter as a WHERE fragment with its bound args.
// tr bounds the request lookups a log search may make.
func (s *Server) filterSQL(src string, f map[string]string, tr timeRange) (string, []any, error) {
	q := s.q
	var conds []string
	var args []any
	add := func(c string) { conds = append(conds, c) }
	allowed := map[string]bool{}
	for _, k := range ruleFilterKeys[src] {
		allowed[k] = true
	}
	for k, v := range f {
		if strings.TrimSpace(v) == "" {
			continue
		}
		if !allowed[k] {
			return "", nil, badInput(fmt.Sprintf("%s has no filter %q", src, k))
		}
		if len(v) > 2000 {
			return "", nil, badInput("the " + k + " filter is too long")
		}
	}
	get := func(k string) string { return strings.TrimSpace(f[k]) }
	switch src {
	case "requests":
		if v := get("status"); v != "" {
			c, err := statusCond(v, &args)
			if err != nil {
				return "", nil, err
			}
			add(c)
		}
		if v := splitList(strings.ToUpper(get("method"))); len(v) > 0 {
			add(inList("method", v, &args))
		}
		if v := splitList(get("route")); len(v) > 0 {
			add(likeList("COALESCE(route, path)", v, &args))
		}
		if v := splitList(get("path")); len(v) > 0 {
			// Attacks hide in the query string as often as in the path.
			c := likeList("path", v, &args)
			add("(" + c + " OR " + likeList("COALESCE(query, '')", v, &args) + ")")
		}
		if v := splitList(get("ip")); len(v) > 0 {
			add(inList("ip", v, &args))
		}
		if v := splitList(get("user")); len(v) > 0 {
			add(inList(accountExpr(q), v, &args))
		}
		if v := splitList(get("role")); len(v) > 0 {
			add(inList("user_role", v, &args))
		}
		if v := splitList(get("service")); len(v) > 0 {
			add(inList("service", v, &args))
		}
		if v := splitList(get("user_agent")); len(v) > 0 {
			add(likeList("COALESCE(user_agent, '')", v, &args))
		}
		if get("login") == "yes" {
			add(s.security().st.loginCond(q))
		}
		switch get("auth_failed") {
		case "yes":
			add(authFailCond(q))
		case "no":
			add("status < 400")
		}
	case "logs":
		if v := get("query"); v != "" {
			cq, err := compileLogQueryIn(v, q, s.onD1(), &tr)
			if err != nil {
				return "", nil, badInput("search: " + err.Error())
			}
			add("(" + cq.where + ")")
			args = append(args, cq.args...)
		}
	case "events":
		// Signals from rules are never read by rules.
		add("source <> 'rule'")
		if v := splitList(get("kind")); len(v) > 0 {
			add(inList("kind", v, &args))
		}
		if v := splitList(get("source")); len(v) > 0 {
			add(inList("source", v, &args))
		}
		if v := splitList(get("host")); len(v) > 0 {
			add(inList("host", v, &args))
		}
		if v := splitList(get("user")); len(v) > 0 {
			add(inList("actor", v, &args))
		}
		if v := splitList(get("ip")); len(v) > 0 {
			add(inList("ip", v, &args))
		}
		if v := splitList(get("target")); len(v) > 0 {
			add(likeList("COALESCE(target, '')", v, &args))
		}
		if v := get("min_severity"); v != "" {
			levels := severitiesFrom(v)
			if levels == nil {
				return "", nil, badInput("minimum severity must be info, warning or critical")
			}
			add(inList("severity", levels, &args))
		}
	case "audit":
		if v := splitList(get("action")); len(v) > 0 {
			var parts []string
			for _, a := range v {
				if strings.HasSuffix(a, "*") {
					parts = append(parts, "action LIKE ? ESCAPE '!'")
					args = append(args, likeEscaper.Replace(strings.TrimSuffix(a, "*"))+"%")
				} else {
					parts = append(parts, "action = ?")
					args = append(args, a)
				}
			}
			add("(" + strings.Join(parts, " OR ") + ")")
		}
		if v := splitList(get("user")); len(v) > 0 {
			add(inList("user_name", v, &args))
		}
		if v := splitList(get("role")); len(v) > 0 {
			add(inList("role", v, &args))
		}
		if v := splitList(get("ip")); len(v) > 0 {
			add(inList("ip", v, &args))
		}
		if v := splitList(get("target")); len(v) > 0 {
			add(likeList("target", v, &args))
		}
	default:
		return "", nil, badInput("source must be requests, logs, events or audit")
	}
	if len(conds) == 0 {
		return "1=1", nil, nil
	}
	return strings.Join(conds, " AND "), args, nil
}

// spanArgs are a time range as the two bound created_at bounds.
func spanArgs(from, to time.Time) []any {
	return []any{from.UTC().Format(tsLayout), to.UTC().Format(tsLayout)}
}
