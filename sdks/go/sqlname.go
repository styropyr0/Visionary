package visionary

import (
	"regexp"
	"strings"
)

// sanitizeSQL makes a statement safe to store and stable to group by:
// string and numeric literals become ?, IN lists collapse to IN (?),
// comments are dropped and whitespace runs become one space. Placeholders
// ($1, ?) and quoted identifiers are kept as written.
func sanitizeSQL(q string) string {
	var b strings.Builder
	b.Grow(len(q))
	var prev byte = ' ' // last byte written, to tell 1 from t1
	space := false
	put := func(s string) {
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteString(s)
		prev = s[len(s)-1]
	}
	for i := 0; i < len(q); {
		c := q[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f':
			space = true
			i++
		// Comments are dropped: once lines are joined, a -- comment would
		// swallow the rest of the statement, and a quote inside one would
		// start a literal that never ends.
		case c == '-' && i+1 < len(q) && q[i+1] == '-':
			j := strings.IndexByte(q[i:], '\n')
			if j < 0 {
				j = len(q) - i
			}
			space = true
			i += j
		case c == '/' && i+1 < len(q) && q[i+1] == '*':
			j := strings.Index(q[i+2:], "*/")
			if j < 0 {
				i = len(q)
			} else {
				i += j + 4
			}
			space = true
		case c == '\'':
			// A string literal; '' inside is an escaped quote.
			j := i + 1
			for j < len(q) {
				if q[j] == '\'' {
					if j+1 < len(q) && q[j+1] == '\'' {
						j += 2
						continue
					}
					j++
					break
				}
				j++
			}
			put("?")
			i = j
		case c == '"' || c == '`':
			j := strings.IndexByte(q[i+1:], c)
			if j < 0 {
				put(q[i:])
				i = len(q)
				break
			}
			put(q[i : i+j+2])
			i += j + 2
		// A space still to be written separates the number from the word
		// before it ("LIMIT 20", "THEN 0.95"): it is a literal, not a name.
		case isDigit(c) && (space || !isWordByte(prev)):
			j := i + 1
			for j < len(q) && (isDigit(q[j]) || q[j] == '.' || q[j] == 'x' || q[j] == 'X' ||
				isHexLetter(q[j]) || ((q[j] == '+' || q[j] == '-') && (q[j-1] == 'e' || q[j-1] == 'E'))) {
				j++
			}
			put("?")
			i = j
		default:
			put(q[i : i+1])
			i++
		}
	}
	return inList.ReplaceAllString(b.String(), "$1 (?)")
}

var inList = regexp.MustCompile(`(?i)\b(IN)\s*\(\s*(?:\?|\$\d+)(?:\s*,\s*(?:\?|\$\d+))*\s*\)`)

func isDigit(c byte) bool     { return c >= '0' && c <= '9' }
func isHexLetter(c byte) bool { return (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') }

// isWordByte covers identifier bytes and placeholders: a digit after one of
// these is part of a name ($1, t2, col_3), not a literal.
func isWordByte(c byte) bool {
	return c == '_' || c == '$' || c == '@' || c == ':' || c >= 0x80 ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || isDigit(c)
}

// sqlName is the low-cardinality span name of a sanitized statement: its
// first keyword and main table ("SELECT users", "INSERT bookings"), or the
// keyword alone ("BEGIN").
func sqlName(q string) string {
	toks := sqlTokens(q)
	kw, at := "", -1
	for i, t := range toks {
		if t.word {
			kw, at = strings.ToUpper(t.text), i
			break
		}
	}
	if kw == "" {
		return "QUERY"
	}
	if kw == "WITH" {
		// The main statement is the first top-level verb after the CTEs.
		for i := at + 1; i < len(toks); i++ {
			if t := toks[i]; t.word && t.depth == 0 {
				switch v := strings.ToUpper(t.text); v {
				case "SELECT", "INSERT", "UPDATE", "DELETE", "MERGE":
					kw, at = v, i
				default:
					continue
				}
				break
			}
		}
	}
	table := ""
	switch kw {
	case "SELECT", "DELETE":
		table = wordAfter(toks, at, "FROM")
	case "INSERT", "REPLACE", "MERGE":
		table = wordAfter(toks, at, "INTO")
	case "UPDATE":
		for i := at + 1; i < len(toks); i++ {
			t := toks[i]
			if !t.word {
				break
			}
			switch strings.ToUpper(t.text) {
			case "ONLY", "OR", "ROLLBACK", "ABORT", "REPLACE", "FAIL", "IGNORE", "LOW_PRIORITY":
				continue
			}
			table = t.text
			break
		}
	}
	if table == "" {
		return kw
	}
	if i := strings.LastIndexByte(table, '.'); i >= 0 {
		table = table[i+1:]
	}
	return kw + " " + table
}

// wordAfter returns the word after the first keyword kw at the depth of
// toks[at], or "" when the next token is not a name (a subquery).
func wordAfter(toks []sqlToken, at int, kw string) string {
	depth := toks[at].depth
	for i := at + 1; i < len(toks)-1; i++ {
		if t := toks[i]; t.word && t.depth == depth && strings.EqualFold(t.text, kw) {
			if next := toks[i+1]; next.word {
				return next.text
			}
			return ""
		}
	}
	return ""
}

type sqlToken struct {
	text  string
	word  bool
	depth int
}

// sqlTokens splits q into names (with quotes removed, schema.table kept
// together) and single punctuation bytes, tracking parenthesis depth.
func sqlTokens(q string) []sqlToken {
	var out []sqlToken
	depth := 0
	for i := 0; i < len(q); {
		c := q[i]
		switch {
		case c == ' ':
			i++
		case c == '(':
			out = append(out, sqlToken{text: "(", depth: depth})
			depth++
			i++
		case c == ')':
			if depth > 0 {
				depth--
			}
			out = append(out, sqlToken{text: ")", depth: depth})
			i++
		case isQuote(c) || isWordStart(c):
			var name string
			name, i = readName(q, i)
			out = append(out, sqlToken{text: name, word: true, depth: depth})
		default:
			out = append(out, sqlToken{text: q[i : i+1], depth: depth})
			i++
		}
	}
	return out
}

// readName reads a name at q[i:], joining qualified parts (schema.table)
// and dropping identifier quotes.
func readName(q string, i int) (string, int) {
	var name strings.Builder
	for {
		if c := q[i]; isQuote(c) {
			closer := c
			if c == '[' {
				closer = ']'
			}
			j := strings.IndexByte(q[i+1:], closer)
			if j < 0 {
				name.WriteString(q[i+1:])
				return name.String(), len(q)
			}
			name.WriteString(q[i+1 : i+1+j])
			i += j + 2
		} else {
			j := i
			for j < len(q) && isWordByte(q[j]) {
				j++
			}
			name.WriteString(q[i:j])
			i = j
		}
		if i+1 < len(q) && q[i] == '.' && (isQuote(q[i+1]) || isWordStart(q[i+1])) {
			name.WriteByte('.')
			i++
			continue
		}
		return name.String(), i
	}
}

func isQuote(c byte) bool { return c == '"' || c == '`' || c == '[' }

func isWordStart(c byte) bool {
	return c == '_' || c >= 0x80 || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
