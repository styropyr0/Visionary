package analytics

import (
	"errors"
	"fmt"
	"strings"
)

// Statements reach D1 with a token that can write, so every query that is not
// built by this package (LLM output, edited SQL, saved panels) must pass
// validateReadOnly first.

var deniedKeywords = map[string]bool{
	"INSERT": true, "UPDATE": true, "DELETE": true, "REPLACE": true, "UPSERT": true,
	"DROP": true, "ALTER": true, "CREATE": true, "TRUNCATE": true, "RENAME": true,
	"ATTACH": true, "DETACH": true, "PRAGMA": true, "VACUUM": true, "REINDEX": true,
	"ANALYZE": true, "BEGIN": true, "COMMIT": true, "ROLLBACK": true, "SAVEPOINT": true,
	"RELEASE": true, "TRANSACTION": true, "RETURNING": true, "LOAD_EXTENSION": true,
}

// deniedPrefixes blocks SQLite and Cloudflare internal tables.
var deniedPrefixes = []string{"SQLITE_", "_CF_", "D1_"}

const maxSQLLength = 20000

// validateReadOnly accepts exactly one SELECT (or WITH ... SELECT) statement and
// returns it without trailing semicolons. It scans tokens outside string
// literals and quoted identifiers, so keywords inside data never trip it, and
// rejects comments outright so nothing can hide from the scan.
func validateReadOnly(sql string) (string, error) {
	sql = strings.TrimSpace(sql)
	for strings.HasSuffix(sql, ";") {
		sql = strings.TrimSpace(strings.TrimSuffix(sql, ";"))
	}
	if sql == "" {
		return "", errors.New("query is empty")
	}
	if len(sql) > maxSQLLength {
		return "", fmt.Errorf("query is longer than %d characters", maxSQLLength)
	}

	// words are bare tokens; names also include quoted identifiers, which can
	// name a table but never act as a keyword.
	var words, names []string
	for i := 0; i < len(sql); {
		ch := sql[i]
		switch {
		case ch == '\'':
			end, err := skipQuoted(sql, i, ch, ch)
			if err != nil {
				return "", err
			}
			i = end
		case ch == '"' || ch == '`' || ch == '[':
			closer := ch
			if ch == '[' {
				closer = ']'
			}
			end, err := skipQuoted(sql, i, ch, closer)
			if err != nil {
				return "", err
			}
			names = append(names, strings.ToUpper(sql[i+1:end-1]))
			i = end
		case ch == '-' && i+1 < len(sql) && sql[i+1] == '-',
			ch == '/' && i+1 < len(sql) && sql[i+1] == '*':
			return "", errors.New("comments are not allowed")
		case ch == ';':
			return "", errors.New("only one statement is allowed")
		case isIdentStart(ch):
			j := i + 1
			for j < len(sql) && isIdentPart(sql[j]) {
				j++
			}
			words = append(words, strings.ToUpper(sql[i:j]))
			names = append(names, words[len(words)-1])
			i = j
		default:
			i++
		}
	}

	if len(words) == 0 || (words[0] != "SELECT" && words[0] != "WITH") {
		return "", errors.New("only SELECT queries are allowed")
	}
	for _, w := range words {
		if deniedKeywords[w] {
			return "", fmt.Errorf("%s is not allowed in a read-only query", w)
		}
	}
	for _, n := range names {
		for _, p := range deniedPrefixes {
			if strings.HasPrefix(strings.TrimSpace(n), p) {
				return "", errors.New("internal tables cannot be queried")
			}
		}
	}
	return sql, nil
}

// skipQuoted returns the index just past the literal that opens at start. A
// doubled closing quote is an escaped quote, as in SQL.
func skipQuoted(s string, start int, open, close byte) (int, error) {
	for i := start + 1; i < len(s); i++ {
		if s[i] != close {
			continue
		}
		if open == close && i+1 < len(s) && s[i+1] == close {
			i++
			continue
		}
		return i + 1, nil
	}
	return 0, errors.New("unterminated quoted text")
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9') || c == '$'
}
