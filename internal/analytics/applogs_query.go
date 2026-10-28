package analytics

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"modernc.org/sqlite"

	sdk "github.com/styropyr0/visionary/sdks/go"
	"github.com/styropyr0/visionary/sdks/go/logrules"
)

// The Logs page's search language, compiled to SQL with every value a bound
// parameter:
//
//	timeout                    message contains "timeout" (any case)
//	"connection refused"       message contains the phrase
//	level:error                a column equals a value
//	level:warn+  level:>=warn  that level or worse
//	service:api*               a prefix (or *suffix, *middle*)
//	status:>=500               a comparison, on columns or fields
//	order_id:42  attrs.k:v     a field of the line (any name that is not a column)
//	host:*                     the column or field is set
//	-healthz  NOT level:debug  negation
//	a OR b   (a OR b) c        alternatives; terms side by side must all match
//	/time(d|o)ut/  file:/x/    a regular expression (not on Cloudflare D1)

// logColumns are the fields that are columns of app_logs, by the names the
// search accepts.
var logColumns = map[string]string{
	"id": "id", "service": "service", "source": "source", "host": "host", "container": "container",
	"logger": "logger", "instance": "instance", "version": "version", "release": "version",
	"request_id": "request_id", "request": "request_id", "trace_id": "trace_id", "trace": "trace_id",
	"span_id": "span_id", "span": "span_id", "user_id": "user_id", "user": "user_id",
	"session_id": "session_id", "session": "session_id", "pattern": "pattern",
	"message": "message", "msg": "message", "code_file": "code_file", "file": "code_file",
	"code_line": "code_line", "line": "code_line", "code_func": "code_func", "func": "code_func",
	"severity": "severity", "level": "severity",
}

// numericColumns compare as numbers.
var numericColumns = map[string]bool{"id": true, "code_line": true, "severity": true}

// logQuery is a compiled search.
type logQuery struct {
	where string
	args  []any
	// terms are the free-text words and phrases, for highlighting.
	terms []string
}

type logQueryError struct{ msg string }

func (e *logQueryError) Error() string { return e.msg }

// token kinds of the search language.
type qtok struct {
	kind  byte // 'w' word, 'p' phrase, 'r' regex, '(' ')', '-' not, 'O' or, 'A' and
	text  string
	field string
	op    string // ":", ":>", ":>=", ":<", ":<="
}

// lexQuery splits a search into tokens. A field is a word ending in ":"
// right before its value.
func lexQuery(s string) ([]qtok, error) {
	var out []qtok
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case unicode.IsSpace(rune(c)):
			i++
		case c == '(' || c == ')':
			out = append(out, qtok{kind: c})
			i++
		case c == '-' && i+1 < len(s) && !unicode.IsSpace(rune(s[i+1])) && (i == 0 || unicode.IsSpace(rune(s[i-1])) || s[i-1] == '('):
			out = append(out, qtok{kind: '-'})
			i++
		default:
			field, op := "", ""
			if m := fieldPrefix.FindStringSubmatch(s[i:]); m != nil {
				field, op = m[1], m[2]
				i += len(m[0])
				if i >= len(s) || unicode.IsSpace(rune(s[i])) {
					return nil, &logQueryError{fmt.Sprintf("%s%s needs a value", field, op)}
				}
			}
			t, n, err := lexValue(s[i:])
			if err != nil {
				return nil, err
			}
			i += n
			t.field, t.op = field, op
			if field == "" && t.kind == 'w' {
				switch t.text {
				case "OR", "||":
					t = qtok{kind: 'O'}
				case "AND", "&&":
					t = qtok{kind: 'A'}
				case "NOT":
					t = qtok{kind: '-'}
				}
			}
			out = append(out, t)
		}
	}
	return out, nil
}

var fieldPrefix = regexp.MustCompile(`^([A-Za-z_@][A-Za-z0-9_.@\-]*)(:>=|:<=|:>|:<|:)`)

// lexValue reads a phrase, a regex or a word.
func lexValue(s string) (qtok, int, error) {
	switch s[0] {
	case '"':
		var b strings.Builder
		for i := 1; i < len(s); i++ {
			switch s[i] {
			case '\\':
				if i+1 < len(s) {
					i++
					b.WriteByte(s[i])
				}
			case '"':
				return qtok{kind: 'p', text: b.String()}, i + 1, nil
			default:
				b.WriteByte(s[i])
			}
		}
		return qtok{}, 0, &logQueryError{"a quoted phrase is missing its closing \""}
	case '/':
		// /…/ is a regular expression when the closing slash ends the term;
		// otherwise it is a path such as /orders/* or /pay.
		for i := 1; i < len(s); i++ {
			if s[i] == '\\' {
				i++
				continue
			}
			if s[i] == '/' {
				if i+1 < len(s) && !unicode.IsSpace(rune(s[i+1])) && s[i+1] != ')' {
					break
				}
				re := s[1:i]
				if _, err := regexp.Compile(re); err != nil {
					return qtok{}, 0, &logQueryError{"not a valid regular expression: " + re}
				}
				return qtok{kind: 'r', text: re}, i + 1, nil
			}
		}
	}
	n := 0
	for n < len(s) && !unicode.IsSpace(rune(s[n])) && s[n] != '(' && s[n] != ')' {
		n++
	}
	return qtok{kind: 'w', text: s[:n]}, n, nil
}

// queryParser builds the WHERE clause.
type queryParser struct {
	toks  []qtok
	pos   int
	d     sqlDialect
	args  []any
	terms []string
	depth int
	// noRegex is set on Cloudflare D1, where SQLite has no REGEXP.
	noRegex bool
	// from and to bound the requests route: and status: look up; empty
	// leaves them unbounded (alert rules add their window themselves).
	from, to string
}

const maxQueryTerms = 60

// compileLogQuery turns a search into a condition on app_logs; an empty
// search is "1=1". noRegex refuses regular expressions (Cloudflare D1).
func compileLogQuery(q string, d sqlDialect, noRegex bool) (*logQuery, error) {
	return compileLogQueryIn(q, d, noRegex, nil)
}

// compileLogQueryIn is compileLogQuery with the range the search covers,
// which bounds the request lookups of route: and status:.
func compileLogQueryIn(q string, d sqlDialect, noRegex bool, tr *timeRange) (*logQuery, error) {
	if len(q) > 2000 {
		return nil, &logQueryError{"the search is longer than 2000 characters"}
	}
	toks, err := lexQuery(q)
	if err != nil {
		return nil, err
	}
	if len(toks) > maxQueryTerms {
		return nil, &logQueryError{fmt.Sprintf("the search has more than %d terms", maxQueryTerms)}
	}
	if len(toks) == 0 {
		return &logQuery{where: "1=1"}, nil
	}
	p := &queryParser{toks: toks, d: d, noRegex: noRegex}
	if tr != nil {
		p.from, p.to = tr.From.Add(-joinLead).Format(tsLayout), tr.To.Format(tsLayout)
	}
	where, err := p.or()
	if err != nil {
		return nil, err
	}
	if p.pos < len(p.toks) {
		return nil, &logQueryError{"unexpected ) in the search"}
	}
	return &logQuery{where: where, args: p.args, terms: p.terms}, nil
}

func (p *queryParser) peek() *qtok {
	if p.pos < len(p.toks) {
		return &p.toks[p.pos]
	}
	return nil
}

func (p *queryParser) or() (string, error) {
	left, err := p.and()
	if err != nil {
		return "", err
	}
	parts := []string{left}
	for t := p.peek(); t != nil && t.kind == 'O'; t = p.peek() {
		p.pos++
		right, err := p.and()
		if err != nil {
			return "", err
		}
		parts = append(parts, right)
	}
	if len(parts) == 1 {
		return left, nil
	}
	return "(" + strings.Join(parts, " OR ") + ")", nil
}

func (p *queryParser) and() (string, error) {
	var parts []string
	for {
		t := p.peek()
		if t == nil || t.kind == 'O' || t.kind == ')' {
			break
		}
		if t.kind == 'A' {
			p.pos++
			continue
		}
		c, err := p.unary()
		if err != nil {
			return "", err
		}
		parts = append(parts, c)
	}
	switch len(parts) {
	case 0:
		return "", &logQueryError{"OR needs a term on both sides"}
	case 1:
		return parts[0], nil
	}
	return "(" + strings.Join(parts, " AND ") + ")", nil
}

func (p *queryParser) unary() (string, error) {
	t := p.peek()
	if t.kind == '-' {
		p.pos++
		if p.peek() == nil {
			return "", &logQueryError{"- or NOT needs a term after it"}
		}
		c, err := p.unary()
		if err != nil {
			return "", err
		}
		// NULL fields count as not matching, so NOT keeps them.
		return "NOT COALESCE(" + c + ", 1=0)", nil
	}
	if t.kind == '(' {
		p.pos++
		if p.depth++; p.depth > 10 {
			return "", &logQueryError{"the search is nested too deeply"}
		}
		c, err := p.or()
		p.depth--
		if err != nil {
			return "", err
		}
		if n := p.peek(); n == nil || n.kind != ')' {
			return "", &logQueryError{"a ( is missing its )"}
		}
		p.pos++
		return c, nil
	}
	if t.kind == ')' {
		return "", &logQueryError{"unexpected ) in the search"}
	}
	p.pos++
	return p.term(t)
}

func (p *queryParser) arg(v any) string {
	p.args = append(p.args, v)
	return "?"
}

// like is a case-insensitive pattern match.
func (p *queryParser) like(expr, pattern string) string {
	op := "LIKE"
	if p.d.d == sdk.Postgres {
		op = "ILIKE"
	}
	return expr + " " + op + " " + p.arg(pattern) + " ESCAPE '!'"
}

func (p *queryParser) regex(expr, re string) (string, error) {
	switch p.d.d {
	case sdk.Postgres:
		return expr + " ~* " + p.arg(re), nil
	case sdk.MySQL:
		return "REGEXP_LIKE(" + expr + ", " + p.arg(re) + ", 'i')", nil
	}
	if p.noRegex || !sqliteRegexp() {
		return "", &logQueryError{"regular expressions aren't available on this database"}
	}
	return expr + " REGEXP " + p.arg("(?i)"+re), nil
}

// attr is the SQL for a field of attrs.
func (p *queryParser) attr(key string) string {
	path := `$."` + strings.ReplaceAll(key, `"`, ``) + `"`
	switch p.d.d {
	case sdk.Postgres:
		return "(CAST(attrs AS JSON) ->> " + p.arg(key) + ")"
	case sdk.MySQL:
		return "JSON_UNQUOTE(JSON_EXTRACT(attrs, " + p.arg(path) + "))"
	}
	return "json_extract(attrs, " + p.arg(path) + ")"
}

func (p *queryParser) num(expr string) string {
	switch p.d.d {
	case sdk.Postgres:
		return "CAST(NULLIF(" + expr + ", '') AS DOUBLE PRECISION)"
	case sdk.MySQL:
		return "CAST(" + expr + " AS DOUBLE)"
	}
	return "CAST(" + expr + " AS REAL)"
}

func (p *queryParser) term(t *qtok) (string, error) {
	if t.field == "" {
		switch t.kind {
		case 'r':
			return p.regex("message", t.text)
		case 'p':
			p.terms = append(p.terms, t.text)
			return p.like("message", "%"+likeEscaper.Replace(t.text)+"%"), nil
		}
		if t.text == "*" {
			return "1=1", nil
		}
		p.terms = append(p.terms, strings.Trim(t.text, "*"))
		return p.like("message", wildcard(t.text, true)), nil
	}
	field := strings.ToLower(t.field)
	field = strings.TrimPrefix(field, "@")
	if field == "level" || field == "severity" {
		return p.level(t)
	}
	if field == "route" || field == "status" || field == "method" {
		return p.requestField(field, t)
	}
	col, isCol := logColumns[field]
	expr, key := col, ""
	if !isCol {
		key = strings.TrimPrefix(t.field, "attrs.")
		if key == "" || len(key) > 200 {
			return "", &logQueryError{"field names are 1 to 200 characters"}
		}
		expr = p.attr(key)
	}
	if t.kind == 'w' && t.text == "*" && t.op == ":" {
		return expr + " IS NOT NULL", nil
	}
	if t.op != ":" {
		f, err := strconv.ParseFloat(t.text, 64)
		if err != nil {
			return "", &logQueryError{fmt.Sprintf("%s%s needs a number", t.field, t.op)}
		}
		op := strings.TrimPrefix(t.op, ":")
		if isCol && !numericColumns[col] {
			return "", &logQueryError{fmt.Sprintf("%s isn't a number; compare numbers like status:>=500", t.field)}
		}
		if !isCol {
			expr = p.num(expr)
		}
		return expr + " " + op + " " + p.arg(f), nil
	}
	switch t.kind {
	case 'r':
		return p.regex(expr, t.text)
	case 'p':
		if col == "message" {
			return p.like(expr, "%"+likeEscaper.Replace(t.text)+"%"), nil
		}
		return expr + " = " + p.arg(t.text), nil
	}
	if strings.Contains(t.text, "*") {
		return p.like(expr, wildcard(t.text, false)), nil
	}
	if col == "message" {
		return p.like(expr, "%"+likeEscaper.Replace(t.text)+"%"), nil
	}
	if isCol && numericColumns[col] {
		n, err := strconv.ParseInt(t.text, 10, 64)
		if err != nil {
			return "", &logQueryError{fmt.Sprintf("%s takes a whole number", t.field)}
		}
		return expr + " = " + p.arg(n), nil
	}
	if !isCol {
		// Fields keep their JSON type: "42" and 42 both match 42.
		if f, err := strconv.ParseFloat(t.text, 64); err == nil {
			// The field appears twice, so its path is bound twice, in order.
			text := expr + " = " + p.arg(t.text)
			return "(" + text + " OR " + p.num(p.attr(key)) + " = " + p.arg(f) + ")", nil
		}
		if b, err := strconv.ParseBool(t.text); err == nil && (t.text == "true" || t.text == "false") {
			if p.d.d == sdk.SQLite {
				return expr + " = " + p.arg(map[bool]int{true: 1, false: 0}[b]), nil
			}
		}
	}
	return expr + " = " + p.arg(t.text), nil
}

// requestField matches lines by the request they were logged in:
// route:/orders/{id}, route:/admin/*, status:>=500, method:POST.
func (p *queryParser) requestField(field string, t *qtok) (string, error) {
	// The range comes first in the SQL, so its arguments are bound first.
	window := ""
	if p.from != "" {
		window = "created_at >= " + p.arg(p.from) + " AND created_at < " + p.arg(p.to) + " AND "
	}
	var cond string
	switch field {
	case "status":
		op := strings.TrimPrefix(t.op, ":")
		if op == "" {
			op = "="
		}
		text := strings.ToLower(t.text)
		if len(text) == 3 && strings.HasSuffix(text, "xx") && text[0] >= '1' && text[0] <= '5' {
			lo := int(text[0]-'0') * 100
			cond = "status >= " + p.arg(lo) + " AND status < " + p.arg(lo+100)
			break
		}
		n, err := strconv.Atoi(t.text)
		if err != nil || n < 100 || n > 599 {
			return "", &logQueryError{"status takes a code like 500 or a class like 5xx"}
		}
		cond = "status " + op + " " + p.arg(n)
	case "method":
		cond = "method = " + p.arg(strings.ToUpper(t.text))
	default:
		if t.op != ":" {
			return "", &logQueryError{"route takes a route pattern, e.g. route:/orders/{id}"}
		}
		if strings.Contains(t.text, "*") {
			cond = "route LIKE " + p.arg(wildcard(t.text, false)) + " ESCAPE '!'"
		} else {
			cond = "route = " + p.arg(t.text)
		}
	}
	return "request_id IN (SELECT request_id FROM api_logs WHERE " + window + cond + ")", nil
}

// level reads level:error, level:warn+, level:>=warn and severity numbers.
func (p *queryParser) level(t *qtok) (string, error) {
	text, op := strings.ToLower(t.text), strings.TrimPrefix(t.op, ":")
	if strings.HasSuffix(text, "+") {
		text, op = strings.TrimSuffix(text, "+"), ">="
	}
	if op == "" {
		op = "="
	}
	sev := logrules.ParseLevel(text)
	if sev == 0 {
		if n, err := strconv.Atoi(text); err == nil && n >= 1 && n <= 6 {
			sev = n
		}
	}
	if sev == 0 {
		return "", &logQueryError{"level is one of trace, debug, info, warn, error, fatal (add + for that level or worse)"}
	}
	return "severity " + op + " " + p.arg(sev), nil
}

// wildcard turns * into %, escaping the rest; contains wraps a bare word.
func wildcard(s string, contains bool) string {
	parts := strings.Split(s, "*")
	for i := range parts {
		parts[i] = likeEscaper.Replace(parts[i])
	}
	out := strings.Join(parts, "%")
	if contains && !strings.Contains(s, "*") {
		out = "%" + out + "%"
	}
	return out
}

// sqliteRegexp registers REGEXP with the SQLite driver once and reports
// whether that worked.
var sqliteRegexp = sync.OnceValue(func() bool {
	var mu sync.Mutex
	cache := map[string]*regexp.Regexp{}
	err := sqlite.RegisterDeterministicScalarFunction("regexp", 2, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		pat, _ := args[0].(string)
		var text string
		switch v := args[1].(type) {
		case string:
			text = v
		case []byte:
			text = string(v)
		case nil:
			return nil, nil
		default:
			text = fmt.Sprint(v)
		}
		mu.Lock()
		re, ok := cache[pat]
		if !ok {
			var err error
			if re, err = regexp.Compile(pat); err != nil {
				mu.Unlock()
				return nil, errors.New("invalid regular expression")
			}
			if len(cache) > 200 {
				clear(cache)
			}
			cache[pat] = re
		}
		mu.Unlock()
		if re.MatchString(text) {
			return int64(1), nil
		}
		return int64(0), nil
	})
	return err == nil
})
