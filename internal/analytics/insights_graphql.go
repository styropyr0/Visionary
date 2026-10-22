package analytics

import (
	"net/http"
	"sort"

	"golang.org/x/sync/errgroup"
)

// GraphQL operations arrive as spans of kind 'graphql': name is the
// operation ("query GetUser"), detail its document with literal values
// removed. Depth and field count are measured here from the document, so the
// SDKs need no GraphQL library.

// Thresholds past which an operation is flagged as deep or wide.
const (
	graphqlDeep   = 8
	graphqlFields = 100
)

// handleGraphQL lists operations with their latency, errors, depth, field
// count and the database statements their requests ran most.
func (s *Server) handleGraphQL(w http.ResponseWriter, r *http.Request) {
	tr, ok := s.rangeOf(w, r)
	if !ok {
		return
	}
	var ops, caused *Result
	g, ctx := errgroup.WithContext(r.Context())
	g.Go(func() (err error) {
		ops, err = s.telemetryQuery(ctx, `
WITH sp AS (
  SELECT name, detail, duration_ms, status, request_id,
    ROW_NUMBER() OVER (PARTITION BY name ORDER BY duration_ms) AS rn,
    COUNT(*) OVER (PARTITION BY name) AS n
  FROM spans WHERE created_at >= {{from}} AND created_at < {{to}} AND kind = 'graphql'
)
SELECT name, COUNT(*) AS calls,
  `+s.q.round("AVG(duration_ms)", 2)+` AS avg_ms,
  `+pctOf("duration_ms", "0.50")+` AS p50,
  `+pctOf("duration_ms", "0.95")+` AS p95,
  MAX(duration_ms) AS max_ms,
  `+countIf("status = 'error'")+` AS errors,
  MAX(detail) AS document
FROM sp GROUP BY name ORDER BY calls DESC LIMIT 100`, tr)
		return
	})
	// The database statements each operation's requests ran: calls per
	// operation and their time.
	g.Go(func() (err error) {
		caused, err = s.telemetryQuery(ctx, `
SELECT op.name AS operation, db.name AS statement, COUNT(*) AS calls,
  COUNT(DISTINCT db.request_id) AS requests, `+s.q.round("SUM(db.duration_ms)", 1)+` AS total_ms
FROM spans op JOIN spans db ON db.request_id = op.request_id AND db.kind = 'db'
WHERE op.created_at >= {{from}} AND op.created_at < {{to}} AND op.kind = 'graphql'
  AND db.created_at >= {{from}} AND db.created_at < {{to}}
GROUP BY op.name, db.name`, tr)
		return
	})
	if err := g.Wait(); err != nil {
		s.fail(w, err)
		return
	}

	type stmt struct {
		Statement string `json:"statement"`
		Calls     int64  `json:"calls"`
		PerOp     any    `json:"per_operation"`
		TotalMs   any    `json:"total_ms"`
	}
	byOp := map[string][]stmt{}
	for _, m := range rowMaps(caused) {
		op := toString(m["operation"])
		byOp[op] = append(byOp[op], stmt{toString(m["statement"]), toInt(m["calls"]), nil, m["total_ms"]})
	}
	ni, ci, di := indexOf(ops.Columns, "name"), indexOf(ops.Columns, "calls"), indexOf(ops.Columns, "document")
	ops.Columns = append(ops.Columns, "depth", "fields", "flags", "queries_per_call", "statements")
	for i, row := range ops.Rows {
		depth, fields := graphqlShape(toString(row[di]))
		var flags []string
		if depth >= graphqlDeep {
			flags = append(flags, "deep")
		}
		if fields >= graphqlFields {
			flags = append(flags, "wide")
		}
		calls := max(toInt(row[ci]), 1)
		st := byOp[toString(row[ni])]
		var dbCalls int64
		for j := range st {
			dbCalls += st[j].Calls
			st[j].PerOp = float64(int(float64(st[j].Calls)/float64(calls)*10+0.5)) / 10
		}
		sort.Slice(st, func(a, b int) bool { return st[a].Calls > st[b].Calls })
		perCall := float64(int(float64(dbCalls)/float64(calls)*10+0.5)) / 10
		if perCall >= float64(nPlusOneLimit) {
			flags = append(flags, "n+1")
		}
		ops.Rows[i] = append(row, depth, fields, flags, perCall, head(st, 5))
	}
	writeJSON(w, http.StatusOK, ops)
}

// nPlusOneLimit matches nPlusOneCalls: this many queries per operation call
// usually means a resolver that queries once per item.
const nPlusOneLimit = 10

// graphqlShape measures an operation document: how deep its selections nest
// (with named fragments expanded) and how many fields it selects. Malformed
// input yields what was read before the problem.
func graphqlShape(doc string) (depth, fields int) {
	p := &gqlParser{toks: gqlTokens(doc), frags: map[string][]gqlSel{}}
	var ops [][]gqlSel
	for p.i < len(p.toks) {
		t := p.peek()
		switch {
		case t == "fragment":
			p.next()
			name := p.next()
			for p.i < len(p.toks) && p.peek() != "{" {
				p.next()
			}
			p.frags[name] = p.selectionSet()
		case t == "{":
			ops = append(ops, p.selectionSet())
		case t == "query" || t == "mutation" || t == "subscription":
			p.next()
			for p.i < len(p.toks) && p.peek() != "{" {
				if p.peek() == "(" {
					p.skipBalanced("(", ")")
					continue
				}
				p.next()
			}
			ops = append(ops, p.selectionSet())
		default:
			p.next()
		}
	}
	for _, sel := range ops {
		d, f := p.measure(sel, map[string]bool{})
		depth, fields = max(depth, d), fields+f
	}
	return depth, fields
}

// gqlSel is one selection: a field (with its own selections) or a spread of
// a named fragment.
type gqlSel struct {
	spread   string
	children []gqlSel
	field    bool
}

type gqlParser struct {
	toks  []string
	i     int
	frags map[string][]gqlSel
}

func (p *gqlParser) peek() string {
	if p.i < len(p.toks) {
		return p.toks[p.i]
	}
	return ""
}

func (p *gqlParser) next() string {
	t := p.peek()
	p.i++
	return t
}

func (p *gqlParser) skipBalanced(open, close string) {
	depth := 0
	for p.i < len(p.toks) {
		switch p.next() {
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return
			}
		}
	}
}

func (p *gqlParser) directives() {
	for p.peek() == "@" {
		p.next()
		p.next()
		if p.peek() == "(" {
			p.skipBalanced("(", ")")
		}
	}
}

func (p *gqlParser) selectionSet() []gqlSel {
	if p.peek() != "{" {
		return nil
	}
	p.next()
	var out []gqlSel
	for p.i < len(p.toks) && p.peek() != "}" {
		if p.peek() == "..." {
			p.next()
			switch p.peek() {
			case "on", "{", "@":
				if p.peek() == "on" {
					p.next()
					p.next()
				}
				p.directives()
				// An inline fragment: its fields belong to the parent level.
				out = append(out, gqlSel{children: p.selectionSet()})
			default:
				name := p.next()
				p.directives()
				out = append(out, gqlSel{spread: name})
			}
			continue
		}
		p.next() // field name or alias
		if p.peek() == ":" {
			p.next()
			p.next()
		}
		if p.peek() == "(" {
			p.skipBalanced("(", ")")
		}
		p.directives()
		out = append(out, gqlSel{field: true, children: p.selectionSet()})
	}
	p.next() // }
	return out
}

// measure answers the depth of a selection set and its field count, expanding
// named fragments once along each path.
func (p *gqlParser) measure(sels []gqlSel, seen map[string]bool) (depth, fields int) {
	for _, s := range sels {
		switch {
		case s.spread != "":
			if seen[s.spread] {
				continue
			}
			seen[s.spread] = true
			d, f := p.measure(p.frags[s.spread], seen)
			delete(seen, s.spread)
			depth, fields = max(depth, d), fields+f
		case !s.field: // inline fragment
			d, f := p.measure(s.children, seen)
			depth, fields = max(depth, d), fields+f
		default:
			d, f := p.measure(s.children, seen)
			depth, fields = max(depth, d+1), fields+f+1
		}
	}
	return depth, fields
}

// gqlTokens splits a GraphQL document into names, punctuators and "..." while
// dropping strings, numbers, commas and comments.
func gqlTokens(doc string) []string {
	var out []string
	rs := []rune(doc)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case c == '#':
			for i < len(rs) && rs[i] != '\n' {
				i++
			}
		case c == '"':
			if i+2 < len(rs) && rs[i+1] == '"' && rs[i+2] == '"' {
				i += 3
				for i+2 < len(rs) && !(rs[i] == '"' && rs[i+1] == '"' && rs[i+2] == '"') {
					i++
				}
				i += 2
				continue
			}
			for i++; i < len(rs) && rs[i] != '"'; i++ {
				if rs[i] == '\\' {
					i++
				}
			}
		case c == '.' && i+2 < len(rs) && rs[i+1] == '.' && rs[i+2] == '.':
			out = append(out, "...")
			i += 2
		case c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
			j := i
			for j < len(rs) && (rs[j] == '_' || rs[j] >= 'a' && rs[j] <= 'z' || rs[j] >= 'A' && rs[j] <= 'Z' || rs[j] >= '0' && rs[j] <= '9') {
				j++
			}
			out = append(out, string(rs[i:j]))
			i = j - 1
		case c == '{' || c == '}' || c == '(' || c == ')' || c == '[' || c == ']' || c == ':' || c == '@' || c == '$' || c == '!' || c == '=' || c == '|':
			out = append(out, string(c))
		}
	}
	return out
}
