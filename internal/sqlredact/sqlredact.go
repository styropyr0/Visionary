// Package sqlredact makes statement text read from a database server safe to
// store: every literal becomes ?, the way spans store statements. The host
// agent runs it before sending and the server again before writing.
package sqlredact

import (
	"strings"
	"unicode/utf8"

	visionary "github.com/styropyr0/visionary/sdks/go"
)

// MaxLen is the longest statement kept, in bytes, after redaction.
const MaxLen = 2000

// Redact replaces the literals of q with ?, collapses IN lists and drops
// comments. engine is "postgres" or "mysql" (MariaDB too): their string
// syntax differs where the SDK sanitizer alone would let a value through.
func Redact(engine, q string) string {
	if len(q) > 4*MaxLen {
		q = q[:4*MaxLen]
	}
	q = marks(q, engine == "mysql")
	q = visionary.SanitizeSQL(q)
	if len(q) > MaxLen {
		q = q[:MaxLen]
		for !utf8.ValidString(q) {
			q = q[:len(q)-1]
		}
	}
	return strings.TrimSpace(q)
}

// marks turns the string literals the SDK sanitizer would misread into '?':
// MySQL's "double-quoted" strings and backslash escapes, and PostgreSQL's
// $tag$dollar-quoted$tag$ and E'escaped' strings. Comments and quoted
// identifiers are copied as they are, so a quote inside them starts nothing.
func marks(q string, mysql bool) string {
	var b strings.Builder
	b.Grow(len(q))
	for i := 0; i < len(q); {
		c := q[i]
		switch {
		case c == '-' && i+1 < len(q) && q[i+1] == '-':
			j := lineEnd(q, i)
			b.WriteString(q[i:j])
			i = j
		case c == '#' && mysql:
			// A MySQL comment the sanitizer does not know: dropped here.
			i = lineEnd(q, i)
			b.WriteByte(' ')
		case c == '/' && i+1 < len(q) && q[i+1] == '*':
			j := strings.Index(q[i+2:], "*/")
			if j < 0 {
				j = len(q)
			} else {
				j += i + 4
			}
			b.WriteString(q[i:j])
			i = j
		case c == '\'':
			escapes := mysql || (i > 0 && (q[i-1] == 'E' || q[i-1] == 'e') && (i < 2 || !wordByte(q[i-2])))
			i = stringEnd(q, i, '\'', escapes)
			b.WriteString("'?'")
		case c == '"' && mysql:
			i = stringEnd(q, i, '"', true)
			b.WriteString("'?'")
		case c == '"' || c == '`':
			j := strings.IndexByte(q[i+1:], c)
			if j < 0 {
				j = len(q)
			} else {
				j += i + 2
			}
			b.WriteString(q[i:j])
			i = j
		case c == '$' && !mysql && (i == 0 || !wordByte(q[i-1])):
			tag, ok := dollarTag(q[i:])
			if !ok {
				b.WriteByte(c)
				i++
				break
			}
			end := strings.Index(q[i+len(tag):], tag)
			if end < 0 {
				i = len(q)
			} else {
				i += len(tag) + end + len(tag)
			}
			b.WriteString("'?'")
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

func lineEnd(q string, i int) int {
	if j := strings.IndexByte(q[i:], '\n'); j >= 0 {
		return i + j
	}
	return len(q)
}

// stringEnd is the index after the literal opened by quote at i. A doubled
// quote is part of it, and with escapes a backslash escapes the next byte.
func stringEnd(q string, i int, quote byte, escapes bool) int {
	for j := i + 1; j < len(q); j++ {
		switch q[j] {
		case '\\':
			if escapes {
				j++
			}
		case quote:
			if j+1 < len(q) && q[j+1] == quote {
				j++
				continue
			}
			return j + 1
		}
	}
	return len(q)
}

// dollarTag is the opening $tag$ or $$ at the start of s.
func dollarTag(s string) (string, bool) {
	for j := 1; j < len(s); j++ {
		c := s[j]
		switch {
		case c == '$':
			return s[:j+1], true
		case c == '_' || c >= 0x80 || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
		case c >= '0' && c <= '9' && j > 1:
		default:
			return "", false
		}
	}
	return "", false
}

func wordByte(c byte) bool {
	return c == '_' || c == '$' || c >= 0x80 || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
