package analytics

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// A parser for the PromQL subset Visionary evaluates. Written here rather
// than linking the Prometheus module, which would pull in most of Prometheus.

// promError is a query error with the byte offset it points at.
type promError struct {
	Pos int
	Msg string
}

func (e *promError) Error() string { return fmt.Sprintf("%s (at character %d)", e.Msg, e.Pos+1) }

func promErrorf(pos int, format string, args ...any) error {
	return &promError{Pos: pos, Msg: fmt.Sprintf(format, args...)}
}

type promExpr interface{ position() int }

type numberLit struct {
	pos int
	v   float64
}

type stringLit struct {
	pos int
	v   string
}

type labelMatcher struct {
	name, op, value string
	re              *regexp.Regexp
}

func (m labelMatcher) matches(v string) bool {
	switch m.op {
	case "=":
		return v == m.value
	case "!=":
		return v != m.value
	case "=~":
		return m.re.MatchString(v)
	case "!~":
		return !m.re.MatchString(v)
	}
	return false
}

type vectorSel struct {
	pos      int
	name     string
	matchers []labelMatcher
	offset   time.Duration
}

type matrixSel struct {
	pos int
	vs  *vectorSel
	rng time.Duration
}

type callExpr struct {
	pos  int
	fn   string
	args []promExpr
}

type aggExpr struct {
	pos      int
	op       string
	param    promExpr
	expr     promExpr
	grouping []string
	without  bool
}

type vectorMatching struct {
	on       bool
	labels   []string
	card     string // "one-to-one", "many-to-one", "one-to-many"
	include  []string
	explicit bool
}

type binaryExpr struct {
	pos        int
	op         string
	lhs, rhs   promExpr
	returnBool bool
	matching   vectorMatching
}

type unaryExpr struct {
	pos  int
	expr promExpr
}

type parenExpr struct {
	pos  int
	expr promExpr
}

func (e *numberLit) position() int  { return e.pos }
func (e *stringLit) position() int  { return e.pos }
func (e *vectorSel) position() int  { return e.pos }
func (e *matrixSel) position() int  { return e.pos }
func (e *callExpr) position() int   { return e.pos }
func (e *aggExpr) position() int    { return e.pos }
func (e *binaryExpr) position() int { return e.pos }
func (e *unaryExpr) position() int  { return e.pos }
func (e *parenExpr) position() int  { return e.pos }

// Tokens.
type promTokKind int

const (
	tEOF promTokKind = iota
	tIdent
	tNumber
	tDuration
	tString
	tOp // punctuation and operators
)

type promToken struct {
	kind promTokKind
	pos  int
	text string
	num  float64
	dur  time.Duration
}

var promAggOps = []string{"sum", "avg", "min", "max", "count", "topk", "bottomk", "quantile", "stddev", "stdvar", "group"}

var promFuncs = map[string]struct {
	args     []string // "v" instant vector, "m" range vector, "s" scalar
	optional int
}{
	"rate": {[]string{"m"}, 0}, "irate": {[]string{"m"}, 0}, "increase": {[]string{"m"}, 0},
	"delta": {[]string{"m"}, 0}, "idelta": {[]string{"m"}, 0}, "resets": {[]string{"m"}, 0}, "changes": {[]string{"m"}, 0},
	"avg_over_time": {[]string{"m"}, 0}, "min_over_time": {[]string{"m"}, 0}, "max_over_time": {[]string{"m"}, 0},
	"sum_over_time": {[]string{"m"}, 0}, "count_over_time": {[]string{"m"}, 0}, "last_over_time": {[]string{"m"}, 0},
	"present_over_time": {[]string{"m"}, 0}, "absent_over_time": {[]string{"m"}, 0},
	"abs": {[]string{"v"}, 0}, "ceil": {[]string{"v"}, 0}, "floor": {[]string{"v"}, 0}, "sqrt": {[]string{"v"}, 0},
	"exp": {[]string{"v"}, 0}, "ln": {[]string{"v"}, 0}, "log2": {[]string{"v"}, 0}, "log10": {[]string{"v"}, 0},
	"round": {[]string{"v", "s"}, 1}, "clamp_min": {[]string{"v", "s"}, 0}, "clamp_max": {[]string{"v", "s"}, 0},
	"clamp": {[]string{"v", "s", "s"}, 0}, "histogram_quantile": {[]string{"s", "v"}, 0},
	"scalar": {[]string{"v"}, 0}, "vector": {[]string{"s"}, 0}, "time": {nil, 0}, "absent": {[]string{"v"}, 0},
	"sort": {[]string{"v"}, 0}, "sort_desc": {[]string{"v"}, 0},
}

func lexProm(input string) ([]promToken, error) {
	var out []promToken
	i := 0
	for i < len(input) {
		c := input[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '#':
			for i < len(input) && input[i] != '\n' {
				i++
			}
		case c == '"' || c == '\'' || c == '`':
			s, n, err := lexString(input, i)
			if err != nil {
				return nil, err
			}
			out = append(out, promToken{kind: tString, pos: i, text: s})
			i += n
		case c >= '0' && c <= '9' || c == '.' && i+1 < len(input) && input[i+1] >= '0' && input[i+1] <= '9':
			t, n, err := lexNumber(input, i)
			if err != nil {
				return nil, err
			}
			out = append(out, t)
			i += n
		case c == '_' || c == ':' || c < utf8.RuneSelf && unicode.IsLetter(rune(c)):
			j := i
			for j < len(input) && (input[j] == '_' || input[j] == ':' || input[j] < utf8.RuneSelf && (unicode.IsLetter(rune(input[j])) || unicode.IsDigit(rune(input[j])))) {
				j++
			}
			word := input[i:j]
			if lw := strings.ToLower(word); lw == "inf" || lw == "nan" {
				v := math.Inf(1)
				if lw == "nan" {
					v = math.NaN()
				}
				out = append(out, promToken{kind: tNumber, pos: i, text: word, num: v})
			} else {
				out = append(out, promToken{kind: tIdent, pos: i, text: word})
			}
			i = j
		default:
			op := ""
			for _, o := range []string{"=~", "!~", "!=", "==", "<=", ">=", "(", ")", "{", "}", "[", "]", ",", "=", "<", ">", "+", "-", "*", "/", "%", "^", "@"} {
				if strings.HasPrefix(input[i:], o) {
					op = o
					break
				}
			}
			if op == "" {
				r, _ := utf8.DecodeRuneInString(input[i:])
				return nil, promErrorf(i, "unexpected character %q", r)
			}
			out = append(out, promToken{kind: tOp, pos: i, text: op})
			i += len(op)
		}
	}
	return append(out, promToken{kind: tEOF, pos: len(input)}), nil
}

func lexString(input string, start int) (string, int, error) {
	q := input[start]
	if q == '`' {
		end := strings.IndexByte(input[start+1:], '`')
		if end < 0 {
			return "", 0, promErrorf(start, "unterminated string")
		}
		return input[start+1 : start+1+end], end + 2, nil
	}
	var b strings.Builder
	for i := start + 1; i < len(input); i++ {
		c := input[i]
		switch {
		case c == q:
			return b.String(), i - start + 1, nil
		case c == '\n':
			return "", 0, promErrorf(start, "unterminated string")
		case c == '\\':
			if i+1 >= len(input) {
				return "", 0, promErrorf(i, "unterminated string")
			}
			i++
			switch e := input[i]; e {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '\\', '"', '\'', '`':
				b.WriteByte(e)
			case 'u':
				if i+4 >= len(input) {
					return "", 0, promErrorf(i, "invalid escape")
				}
				n, err := strconv.ParseUint(input[i+1:i+5], 16, 32)
				if err != nil {
					return "", 0, promErrorf(i, "invalid escape")
				}
				b.WriteRune(rune(n))
				i += 4
			default:
				return "", 0, promErrorf(i-1, "unknown escape \\%c", e)
			}
		default:
			b.WriteByte(c)
		}
	}
	return "", 0, promErrorf(start, "unterminated string")
}

var durationUnits = map[string]time.Duration{
	"ms": time.Millisecond, "s": time.Second, "m": time.Minute, "h": time.Hour,
	"d": 24 * time.Hour, "w": 7 * 24 * time.Hour, "y": 365 * 24 * time.Hour,
}

// lexNumber reads a number, or a duration like 5m or 1h30m.
func lexNumber(input string, start int) (promToken, int, error) {
	j := start
	isDigit := func(c byte) bool { return c >= '0' && c <= '9' }
	if strings.HasPrefix(input[j:], "0x") || strings.HasPrefix(input[j:], "0X") {
		j += 2
		for j < len(input) && strings.ContainsRune("0123456789abcdefABCDEF", rune(input[j])) {
			j++
		}
		v, err := strconv.ParseInt(input[start:j], 0, 64)
		if err != nil {
			return promToken{}, 0, promErrorf(start, "invalid number")
		}
		return promToken{kind: tNumber, pos: start, text: input[start:j], num: float64(v)}, j - start, nil
	}
	for j < len(input) && isDigit(input[j]) {
		j++
	}
	// A duration: digits followed by a unit, possibly repeated (1h30m).
	if j < len(input) && strings.ContainsRune("smhdwy", rune(input[j])) {
		var total time.Duration
		k := start
		for k < len(input) && isDigit(input[k]) {
			d := k
			for d < len(input) && isDigit(input[d]) {
				d++
			}
			u := d
			for u < len(input) && strings.ContainsRune("smhdwy", rune(input[u])) {
				u++
			}
			unit, ok := durationUnits[input[d:u]]
			if !ok {
				return promToken{}, 0, promErrorf(d, "unknown duration unit %q", input[d:u])
			}
			n, _ := strconv.ParseInt(input[k:d], 10, 64)
			total += time.Duration(n) * unit
			k = u
		}
		if k < len(input) && (input[k] == '_' || unicode.IsLetter(rune(input[k]))) {
			return promToken{}, 0, promErrorf(k, "invalid duration")
		}
		return promToken{kind: tDuration, pos: start, text: input[start:k], dur: total}, k - start, nil
	}
	if j < len(input) && input[j] == '.' {
		j++
		for j < len(input) && isDigit(input[j]) {
			j++
		}
	}
	if j < len(input) && (input[j] == 'e' || input[j] == 'E') {
		k := j + 1
		if k < len(input) && (input[k] == '+' || input[k] == '-') {
			k++
		}
		if k < len(input) && isDigit(input[k]) {
			for k < len(input) && isDigit(input[k]) {
				k++
			}
			j = k
		}
	}
	v, err := strconv.ParseFloat(input[start:j], 64)
	if err != nil {
		return promToken{}, 0, promErrorf(start, "invalid number")
	}
	return promToken{kind: tNumber, pos: start, text: input[start:j], num: v}, j - start, nil
}

type promParser struct {
	toks []promToken
	i    int
}

const maxPromQueryLen = 10000

// parsePromQL parses a query into its expression tree.
func parsePromQL(input string) (promExpr, error) {
	if strings.TrimSpace(input) == "" {
		return nil, promErrorf(0, "the query is empty")
	}
	if len(input) > maxPromQueryLen {
		return nil, promErrorf(0, "the query is longer than %d characters", maxPromQueryLen)
	}
	toks, err := lexProm(input)
	if err != nil {
		return nil, err
	}
	p := &promParser{toks: toks}
	e, err := p.expr(0)
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.kind != tEOF {
		return nil, promErrorf(t.pos, "unexpected %s", describeTok(t))
	}
	if err := checkTypes(e); err != nil {
		return nil, err
	}
	return e, nil
}

func describeTok(t promToken) string {
	switch t.kind {
	case tEOF:
		return "end of query"
	case tString:
		return "string " + strconv.Quote(t.text)
	}
	return strconv.Quote(t.text)
}

func (p *promParser) peek() promToken { return p.toks[p.i] }
func (p *promParser) next() promToken {
	t := p.toks[p.i]
	if t.kind != tEOF {
		p.i++
	}
	return t
}

func (p *promParser) isOp(text string) bool {
	t := p.peek()
	return t.kind == tOp && t.text == text
}

func (p *promParser) isWord(w string) bool {
	t := p.peek()
	return t.kind == tIdent && strings.EqualFold(t.text, w)
}

func (p *promParser) expect(text string) (promToken, error) {
	t := p.next()
	if t.kind != tOp || t.text != text {
		return t, promErrorf(t.pos, "expected %q, found %s", text, describeTok(t))
	}
	return t, nil
}

var binaryPrec = map[string]int{
	"or": 1, "and": 2, "unless": 2,
	"==": 3, "!=": 3, "<": 3, ">": 3, "<=": 3, ">=": 3,
	"+": 4, "-": 4, "*": 5, "/": 5, "%": 5, "atan2": 5, "^": 6,
}

func (p *promParser) binaryOp() (string, bool) {
	t := p.peek()
	switch t.kind {
	case tOp:
		_, ok := binaryPrec[t.text]
		return t.text, ok
	case tIdent:
		w := strings.ToLower(t.text)
		if w == "and" || w == "or" || w == "unless" || w == "atan2" {
			return w, true
		}
	}
	return "", false
}

func (p *promParser) expr(minPrec int) (promExpr, error) {
	lhs, err := p.unary()
	if err != nil {
		return nil, err
	}
	for {
		op, ok := p.binaryOp()
		if !ok || binaryPrec[op] < minPrec {
			return lhs, nil
		}
		opTok := p.next()
		be := &binaryExpr{pos: opTok.pos, op: op, lhs: lhs, matching: vectorMatching{card: "one-to-one"}}
		if p.isWord("bool") {
			bt := p.next()
			if !isComparison(op) {
				return nil, promErrorf(bt.pos, "bool only goes with comparison operators")
			}
			be.returnBool = true
		}
		if p.isWord("on") || p.isWord("ignoring") {
			be.matching.on = strings.EqualFold(p.next().text, "on")
			be.matching.explicit = true
			if be.matching.labels, err = p.labelList(); err != nil {
				return nil, err
			}
			if p.isWord("group_left") || p.isWord("group_right") {
				gt := p.next()
				if op == "and" || op == "or" || op == "unless" {
					return nil, promErrorf(gt.pos, "no grouping allowed for %s", op)
				}
				be.matching.card = "many-to-one"
				if strings.EqualFold(gt.text, "group_right") {
					be.matching.card = "one-to-many"
				}
				if p.isOp("(") {
					if be.matching.include, err = p.labelList(); err != nil {
						return nil, err
					}
				}
			}
		}
		next := binaryPrec[op] + 1
		if op == "^" {
			next = binaryPrec[op]
		}
		if be.rhs, err = p.expr(next); err != nil {
			return nil, err
		}
		lhs = be
	}
}

func isComparison(op string) bool {
	switch op {
	case "==", "!=", "<", ">", "<=", ">=":
		return true
	}
	return false
}

func (p *promParser) unary() (promExpr, error) {
	if p.isOp("-") || p.isOp("+") {
		t := p.next()
		e, err := p.expr(binaryPrec["^"])
		if err != nil {
			return nil, err
		}
		if t.text == "+" {
			return e, nil
		}
		if n, ok := e.(*numberLit); ok {
			return &numberLit{pos: t.pos, v: -n.v}, nil
		}
		return &unaryExpr{pos: t.pos, expr: e}, nil
	}
	e, err := p.primary()
	if err != nil {
		return nil, err
	}
	return p.postfix(e)
}

// postfix reads [range] and offset after a selector or parenthesised expression.
func (p *promParser) postfix(e promExpr) (promExpr, error) {
	if p.isOp("[") {
		lb := p.next()
		vs, ok := e.(*vectorSel)
		if !ok {
			return nil, promErrorf(lb.pos, "ranges only go after a metric selector; subqueries are not supported")
		}
		d := p.next()
		if d.kind != tDuration {
			return nil, promErrorf(d.pos, "expected a duration like 5m, found %s", describeTok(d))
		}
		if t := p.peek(); strings.HasPrefix(t.text, ":") {
			return nil, promErrorf(t.pos, "subqueries are not supported")
		}
		if _, err := p.expect("]"); err != nil {
			return nil, err
		}
		if d.dur <= 0 {
			return nil, promErrorf(d.pos, "the range must be positive")
		}
		e = &matrixSel{pos: vs.pos, vs: vs, rng: d.dur}
	}
	if p.isWord("offset") {
		ot := p.next()
		neg := false
		if p.isOp("-") {
			p.next()
			neg = true
		}
		d := p.next()
		if d.kind != tDuration {
			return nil, promErrorf(d.pos, "expected a duration after offset")
		}
		off := d.dur
		if neg {
			off = -off
		}
		switch x := e.(type) {
		case *vectorSel:
			x.offset = off
		case *matrixSel:
			x.vs.offset = off
		default:
			return nil, promErrorf(ot.pos, "offset only goes after a metric selector")
		}
	}
	if p.isOp("@") {
		return nil, promErrorf(p.peek().pos, "the @ modifier is not supported")
	}
	return e, nil
}

func (p *promParser) primary() (promExpr, error) {
	t := p.peek()
	switch t.kind {
	case tNumber:
		p.next()
		return &numberLit{pos: t.pos, v: t.num}, nil
	case tDuration:
		return nil, promErrorf(t.pos, "a duration is only allowed in a range or after offset")
	case tString:
		p.next()
		return &stringLit{pos: t.pos, v: t.text}, nil
	case tEOF:
		return nil, promErrorf(t.pos, "unexpected end of query")
	case tOp:
		switch t.text {
		case "(":
			p.next()
			e, err := p.expr(0)
			if err != nil {
				return nil, err
			}
			if _, err := p.expect(")"); err != nil {
				return nil, err
			}
			return &parenExpr{pos: t.pos, expr: e}, nil
		case "{":
			return p.selector("", t.pos)
		}
		return nil, promErrorf(t.pos, "unexpected %s", describeTok(t))
	}
	name := t.text
	p.next()
	lw := strings.ToLower(name)
	if slices.Contains(promAggOps, lw) && (p.isOp("(") || p.isWord("by") || p.isWord("without")) {
		return p.aggregate(lw, t.pos)
	}
	if p.isOp("(") {
		if _, ok := promFuncs[name]; !ok {
			return nil, promErrorf(t.pos, "unknown or unsupported function %q", name)
		}
		return p.call(name, t.pos)
	}
	switch lw {
	case "by", "without", "on", "ignoring", "group_left", "group_right", "bool", "offset", "and", "or", "unless":
		return nil, promErrorf(t.pos, "unexpected %q", name)
	}
	return p.selector(name, t.pos)
}

func (p *promParser) selector(name string, pos int) (promExpr, error) {
	vs := &vectorSel{pos: pos, name: name}
	if p.isOp("{") {
		p.next()
		for !p.isOp("}") {
			lt := p.next()
			if lt.kind != tIdent && lt.kind != tString {
				return nil, promErrorf(lt.pos, "expected a label name, found %s", describeTok(lt))
			}
			opTok := p.next()
			if opTok.kind != tOp || (opTok.text != "=" && opTok.text != "!=" && opTok.text != "=~" && opTok.text != "!~") {
				return nil, promErrorf(opTok.pos, "expected =, !=, =~ or !~ after the label %q", lt.text)
			}
			vt := p.next()
			if vt.kind != tString {
				return nil, promErrorf(vt.pos, "expected a quoted label value, found %s", describeTok(vt))
			}
			m := labelMatcher{name: lt.text, op: opTok.text, value: vt.text}
			if m.op == "=~" || m.op == "!~" {
				re, err := regexp.Compile("^(?:" + vt.text + ")$")
				if err != nil {
					return nil, promErrorf(vt.pos, "invalid regular expression: %v", err)
				}
				m.re = re
			}
			vs.matchers = append(vs.matchers, m)
			if p.isOp(",") {
				p.next()
				continue
			}
			if !p.isOp("}") {
				t := p.peek()
				return nil, promErrorf(t.pos, "expected , or } in the label matchers, found %s", describeTok(t))
			}
		}
		p.next()
	}
	if name != "" {
		vs.matchers = append([]labelMatcher{{name: "__name__", op: "=", value: name}}, vs.matchers...)
	}
	for _, m := range vs.matchers {
		if m.name == "__name__" && m.op == "=" {
			if vs.name != "" && vs.name != m.value {
				return nil, promErrorf(pos, "the metric name is given twice")
			}
			vs.name = m.value
		}
	}
	if !slices.ContainsFunc(vs.matchers, func(m labelMatcher) bool { return !m.matches("") }) {
		return nil, promErrorf(pos, "a selector needs a metric name or a label matcher that does not match the empty string")
	}
	return vs, nil
}

func (p *promParser) labelList() ([]string, error) {
	if _, err := p.expect("("); err != nil {
		return nil, err
	}
	var out []string
	for !p.isOp(")") {
		t := p.next()
		if t.kind != tIdent {
			return nil, promErrorf(t.pos, "expected a label name, found %s", describeTok(t))
		}
		out = append(out, t.text)
		if p.isOp(",") {
			p.next()
		} else if !p.isOp(")") {
			t := p.peek()
			return nil, promErrorf(t.pos, "expected , or ), found %s", describeTok(t))
		}
	}
	p.next()
	return out, nil
}

func (p *promParser) args() ([]promExpr, error) {
	if _, err := p.expect("("); err != nil {
		return nil, err
	}
	var out []promExpr
	for !p.isOp(")") {
		e, err := p.expr(0)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
		if p.isOp(",") {
			p.next()
		} else if !p.isOp(")") {
			t := p.peek()
			return nil, promErrorf(t.pos, "expected , or ), found %s", describeTok(t))
		}
	}
	p.next()
	return out, nil
}

func (p *promParser) call(name string, pos int) (promExpr, error) {
	args, err := p.args()
	if err != nil {
		return nil, err
	}
	spec := promFuncs[name]
	if len(args) > len(spec.args) || len(args) < len(spec.args)-spec.optional {
		return nil, promErrorf(pos, "%s takes %d argument(s), got %d", name, len(spec.args), len(args))
	}
	return &callExpr{pos: pos, fn: name, args: args}, nil
}

func (p *promParser) aggregate(op string, pos int) (promExpr, error) {
	ae := &aggExpr{pos: pos, op: op}
	var err error
	grouping := func() error {
		if p.isWord("by") || p.isWord("without") {
			ae.without = strings.EqualFold(p.next().text, "without")
			ae.grouping, err = p.labelList()
			return err
		}
		return nil
	}
	if err := grouping(); err != nil {
		return nil, err
	}
	args, err := p.args()
	if err != nil {
		return nil, err
	}
	if ae.grouping == nil && !ae.without {
		if err := grouping(); err != nil {
			return nil, err
		}
	}
	want := 1
	if op == "topk" || op == "bottomk" || op == "quantile" {
		want = 2
	}
	if len(args) != want {
		return nil, promErrorf(pos, "%s takes %d argument(s), got %d", op, want, len(args))
	}
	if want == 2 {
		ae.param = args[0]
	}
	ae.expr = args[want-1]
	return ae, nil
}

// Value types for checking.
const (
	typScalar = "scalar"
	typVector = "instant vector"
	typMatrix = "range vector"
	typString = "string"
)

func exprType(e promExpr) string {
	switch x := e.(type) {
	case *numberLit:
		return typScalar
	case *stringLit:
		return typString
	case *vectorSel, *aggExpr:
		return typVector
	case *matrixSel:
		return typMatrix
	case *parenExpr:
		return exprType(x.expr)
	case *unaryExpr:
		return exprType(x.expr)
	case *callExpr:
		switch x.fn {
		case "scalar", "time":
			return typScalar
		}
		return typVector
	case *binaryExpr:
		if exprType(x.lhs) == typScalar && exprType(x.rhs) == typScalar {
			return typScalar
		}
		return typVector
	}
	return ""
}

func article(t string) string {
	if t == typVector {
		return "an " + t
	}
	return "a " + t
}

// checkTypes checks argument and operand types, so evaluation can trust them.
func checkTypes(e promExpr) error {
	switch x := e.(type) {
	case *parenExpr:
		return checkTypes(x.expr)
	case *unaryExpr:
		if t := exprType(x.expr); t != typScalar && t != typVector {
			return promErrorf(x.pos, "unary minus needs a scalar or an instant vector, not %s", article(t))
		}
		return checkTypes(x.expr)
	case *callExpr:
		spec := promFuncs[x.fn]
		for i, a := range x.args {
			want := map[string]string{"v": typVector, "m": typMatrix, "s": typScalar}[spec.args[i]]
			if got := exprType(a); got != want {
				hint := ""
				if want == typMatrix && got == typVector {
					hint = ", e.g. " + x.fn + "(name[5m])"
				}
				return promErrorf(a.position(), "%s expects %s as argument %d, got %s%s", x.fn, article(want), i+1, article(got), hint)
			}
			if err := checkTypes(a); err != nil {
				return err
			}
		}
	case *aggExpr:
		if x.param != nil {
			if t := exprType(x.param); t != typScalar {
				return promErrorf(x.param.position(), "%s expects a scalar parameter, got %s", x.op, article(t))
			}
			if err := checkTypes(x.param); err != nil {
				return err
			}
		}
		if t := exprType(x.expr); t != typVector {
			return promErrorf(x.expr.position(), "%s expects an instant vector, got %s", x.op, article(t))
		}
		return checkTypes(x.expr)
	case *binaryExpr:
		lt, rt := exprType(x.lhs), exprType(x.rhs)
		for _, side := range []struct {
			t string
			e promExpr
		}{{lt, x.lhs}, {rt, x.rhs}} {
			if side.t != typScalar && side.t != typVector {
				return promErrorf(side.e.position(), "binary operators need scalars or instant vectors, not %s", article(side.t))
			}
		}
		if (x.op == "and" || x.op == "or" || x.op == "unless") && (lt != typVector || rt != typVector) {
			return promErrorf(x.pos, "%s needs instant vectors on both sides", x.op)
		}
		if isComparison(x.op) && lt == typScalar && rt == typScalar && !x.returnBool {
			return promErrorf(x.pos, "comparisons between scalars need bool")
		}
		if x.matching.explicit && (lt != typVector || rt != typVector) {
			return promErrorf(x.pos, "on and ignoring only go between two vectors")
		}
		if err := checkTypes(x.lhs); err != nil {
			return err
		}
		return checkTypes(x.rhs)
	}
	return nil
}
