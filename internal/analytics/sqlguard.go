package analytics

import (
	"errors"
	"fmt"
	"strings"
)

// Every query that is not built by this package (model output, edited SQL,
// saved panels) must pass validateReadOnly first. The database layer adds a
// read-only transaction and a timeout where the database supports them; this
// guard is the part that also holds on D1, whose token can write.

var deniedKeywords = map[string]bool{
	"INSERT": true, "UPDATE": true, "DELETE": true, "REPLACE": true, "UPSERT": true, "MERGE": true,
	"DROP": true, "ALTER": true, "CREATE": true, "TRUNCATE": true, "RENAME": true,
	"ATTACH": true, "DETACH": true, "PRAGMA": true, "VACUUM": true, "REINDEX": true,
	"ANALYZE": true, "BEGIN": true, "COMMIT": true, "ROLLBACK": true, "SAVEPOINT": true,
	"RELEASE": true, "TRANSACTION": true, "RETURNING": true, "LOAD_EXTENSION": true,
	// PostgreSQL and MySQL.
	"INTO": true, "OUTFILE": true, "DUMPFILE": true, "COPY": true, "SET": true, "RESET": true,
	"GRANT": true, "REVOKE": true, "CALL": true, "DO": true, "EXECUTE": true, "PREPARE": true,
	"DEALLOCATE": true, "LOCK": true, "UNLOCK": true, "HANDLER": true, "LOAD": true,
	"LISTEN": true, "NOTIFY": true, "SHOW": true,
	"SLEEP": true, "BENCHMARK": true, "LOAD_FILE": true, "GET_LOCK": true, "DBLINK": true,
	"SET_CONFIG": true, "LO_IMPORT": true, "LO_EXPORT": true, "QUERY_TO_XML": true,
}

// deniedPrefixes blocks internal tables and server functions (pg_sleep,
// pg_read_file, ...).
var deniedPrefixes = []string{"SQLITE_", "_CF_", "D1_", "PG_"}

// allowedTables are the only tables a query may read, so the analyst cannot
// wander into the application's own tables when they share a database.
var allowedTables = map[string]bool{"API_LOGS": true, "SYSTEM_METRICS": true, "ANALYTICS_PANELS": true}

// tableFunctions may stand in a FROM clause.
var tableFunctions = map[string]bool{"JSON_EACH": true, "JSON_TREE": true, "GENERATE_SERIES": true, "UNNEST": true}

// fromFunctions take FROM as part of their arguments: EXTRACT(EPOCH FROM x).
var fromFunctions = map[string]bool{"EXTRACT": true, "SUBSTRING": true, "SUBSTR": true, "TRIM": true, "OVERLAY": true, "POSITION": true}

const maxSQLLength = 20000

type tokKind int

const (
	tokWord  tokKind = iota // bare identifier or keyword, upper-cased
	tokName                 // quoted identifier, upper-cased
	tokPunct                // one character: ( ) , .
	tokOther                // literals, numbers, operators
)

type token struct {
	kind tokKind
	text string
}

// validateReadOnly accepts exactly one SELECT (or WITH ... SELECT) statement
// over Visionary's tables and returns it without trailing semicolons. It scans
// tokens outside string literals, so keywords inside data never trip it, and
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
	toks, err := tokenize(sql)
	if err != nil {
		return "", err
	}

	if len(toks) == 0 || toks[0].kind != tokWord || (toks[0].text != "SELECT" && toks[0].text != "WITH") {
		return "", errors.New("only SELECT queries are allowed")
	}
	for _, t := range toks {
		if t.kind == tokWord && deniedKeywords[t.text] {
			return "", fmt.Errorf("%s is not allowed in a read-only query", t.text)
		}
		if isIdent(t) {
			for _, p := range deniedPrefixes {
				if strings.HasPrefix(strings.TrimSpace(t.text), p) {
					return "", errors.New("internal tables and functions cannot be used")
				}
			}
		}
	}
	if err := checkTables(toks); err != nil {
		return "", err
	}
	return sql, nil
}

// checkTables requires every table reference (after FROM or JOIN, and each
// comma-separated item of a FROM list) to be an allowed table, a CTE defined in
// the query, a subquery or a table function.
func checkTables(toks []token) error {
	ctes := map[string]bool{}
	for i := 0; i+2 < len(toks); i++ {
		if !isIdent(toks[i]) {
			continue
		}
		// name AS (   or   name (cols) AS (
		j := i + 1
		if toks[j].text == "(" {
			for j < len(toks) && toks[j].text != ")" {
				j++
			}
			j++
		}
		if j+1 < len(toks) && toks[j].kind == tokWord && toks[j].text == "AS" && toks[j+1].text == "(" {
			ctes[toks[i].text] = true
		}
	}

	// parens records, per open paren, whether it is an EXTRACT-style call;
	// fromDepth holds the depth of each open FROM list, where a comma starts
	// another table reference.
	var parens []bool
	var fromDepth []int
	expectTable := false
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if expectTable {
			expectTable = false
			switch {
			case t.text == "(":
				// a subquery; scanned like the rest
			case isIdent(t):
				if i+1 < len(toks) && toks[i+1].text == "." {
					return errors.New("schema-qualified tables cannot be queried; use api_logs, system_metrics or analytics_panels")
				}
				if i+1 < len(toks) && toks[i+1].text == "(" {
					if !tableFunctions[t.text] {
						return fmt.Errorf("%s cannot be used as a table", strings.ToLower(t.text))
					}
				} else if !allowedTables[t.text] && !ctes[t.text] {
					return fmt.Errorf("unknown table %s: only api_logs, system_metrics and analytics_panels can be queried", strings.ToLower(t.text))
				}
			default:
				return errors.New("unexpected token after FROM")
			}
		}
		depth := len(parens)
		switch {
		case t.text == "(":
			parens = append(parens, i > 0 && toks[i-1].kind == tokWord && fromFunctions[toks[i-1].text])
		case t.text == ")":
			if depth > 0 {
				parens = parens[:depth-1]
			}
			for len(fromDepth) > 0 && fromDepth[len(fromDepth)-1] > len(parens) {
				fromDepth = fromDepth[:len(fromDepth)-1]
			}
		case t.text == "," && len(fromDepth) > 0 && fromDepth[len(fromDepth)-1] == depth:
			expectTable = true
		case t.kind == tokWord:
			switch t.text {
			case "FROM":
				if depth > 0 && parens[depth-1] {
					continue // EXTRACT(x FROM y)
				}
				if i >= 2 && toks[i-1].text == "DISTINCT" && (toks[i-2].text == "IS" || toks[i-2].text == "NOT") {
					continue // IS [NOT] DISTINCT FROM
				}
				expectTable = true
				fromDepth = append(fromDepth, depth)
			case "JOIN":
				expectTable = true
			case "WHERE", "GROUP", "HAVING", "ORDER", "LIMIT", "WINDOW", "UNION", "EXCEPT", "INTERSECT", "ON", "USING", "SELECT":
				for len(fromDepth) > 0 && fromDepth[len(fromDepth)-1] == depth {
					fromDepth = fromDepth[:len(fromDepth)-1]
				}
			}
		}
	}
	if expectTable {
		return errors.New("FROM needs a table")
	}
	return nil
}

func isIdent(t token) bool { return t.kind == tokWord || t.kind == tokName }

func tokenize(sql string) ([]token, error) {
	var toks []token
	if strings.Contains(sql, "\\") {
		// MySQL treats a backslash in a literal as an escape, so the database
		// and this scan could disagree about where a string ends.
		return nil, errors.New("backslashes are not allowed")
	}
	for i := 0; i < len(sql); {
		ch := sql[i]
		switch {
		case ch == '{' && strings.HasPrefix(sql[i:], "{{"):
			// A template placeholder ({{from}}, {{t}}) is one opaque token.
			end := strings.Index(sql[i:], "}}")
			if end < 0 {
				return nil, errors.New("unterminated {{placeholder}}")
			}
			toks = append(toks, token{tokOther, sql[i : i+end+2]})
			i += end + 2
		case ch == '\'':
			end, err := skipQuoted(sql, i, ch, ch)
			if err != nil {
				return nil, err
			}
			toks = append(toks, token{tokOther, sql[i:end]})
			i = end
		case ch == '"' || ch == '`' || ch == '[':
			closer := ch
			if ch == '[' {
				closer = ']'
			}
			end, err := skipQuoted(sql, i, ch, closer)
			if err != nil {
				return nil, err
			}
			toks = append(toks, token{tokName, strings.ToUpper(sql[i+1 : end-1])})
			i = end
		case ch == '-' && i+1 < len(sql) && sql[i+1] == '-',
			ch == '/' && i+1 < len(sql) && sql[i+1] == '*',
			ch == '#':
			return nil, errors.New("comments are not allowed")
		case ch == ';':
			return nil, errors.New("only one statement is allowed")
		case ch == '$' && i+1 < len(sql) && (sql[i+1] == '$' || isIdentStart(sql[i+1])):
			// PostgreSQL dollar quoting would hide text from the scan.
			return nil, errors.New("dollar-quoted strings are not allowed")
		case isIdentStart(ch):
			j := i + 1
			for j < len(sql) && isIdentPart(sql[j]) {
				j++
			}
			toks = append(toks, token{tokWord, strings.ToUpper(sql[i:j])})
			i = j
		case ch == '(' || ch == ')' || ch == ',' || ch == '.':
			// A dot between digits is a decimal point, not a qualifier.
			if ch == '.' && i > 0 && i+1 < len(sql) && isDigit(sql[i-1]) && isDigit(sql[i+1]) {
				i++
				continue
			}
			toks = append(toks, token{tokPunct, string(ch)})
			i++
		case ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r':
			i++
		default:
			toks = append(toks, token{tokOther, string(ch)})
			i++
		}
	}
	return toks, nil
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

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || isDigit(c) || c == '$'
}
