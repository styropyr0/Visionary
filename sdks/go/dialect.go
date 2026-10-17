package visionary

import (
	"embed"
	"strconv"
	"strings"
)

// Dialect is the SQL flavour of the database the tables live in.
type Dialect string

const (
	// SQLite also covers Cloudflare D1, which speaks SQLite.
	SQLite   Dialect = "sqlite"
	Postgres Dialect = "postgres"
	MySQL    Dialect = "mysql"
)

//go:embed schema
var schemas embed.FS

// RequestsSchema returns the CREATE statements for api_logs in d.
func (d Dialect) RequestsSchema() string { return d.schema("requests.sql") }

// SystemSchema returns the CREATE statements for system_metrics in d.
func (d Dialect) SystemSchema() string { return d.schema("system.sql") }

func (d Dialect) schema(name string) string {
	b, err := schemas.ReadFile("schema/" + string(d.normalize()) + "/" + name)
	if err != nil {
		panic("visionary: missing schema " + name + " for " + string(d))
	}
	return string(b)
}

func (d Dialect) normalize() Dialect {
	switch d {
	case Postgres, MySQL:
		return d
	}
	return SQLite
}

// Rebind turns "?" placeholders into "$1, $2, ..." for PostgreSQL; other
// dialects use "?" as written. Question marks inside quoted strings are left
// alone.
func (d Dialect) Rebind(sql string) string {
	if d != Postgres || !strings.Contains(sql, "?") {
		return sql
	}
	var b strings.Builder
	b.Grow(len(sql) + 16)
	n := 0
	var quote byte
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '?':
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// Statements splits a schema script into statements.
func Statements(script string) []string {
	var out []string
	for _, s := range strings.Split(script, ";") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// purgeSQL deletes up to 5000 rows older than the cutoff parameter. MySQL
// rejects LIMIT inside IN (subquery) but allows DELETE ... LIMIT; the others
// are the other way round.
func (d Dialect) purgeSQL(table string) string {
	if d == MySQL {
		return "DELETE FROM " + table + " WHERE created_at < ? ORDER BY id LIMIT 5000"
	}
	return "DELETE FROM " + table + " WHERE id IN (SELECT id FROM " + table + " WHERE created_at < ? LIMIT 5000)"
}

// insertSQL builds an INSERT for columns. D1 takes string params only, so
// there each placeholder becomes NULLIF(?, <empty string>) and nulls travel
// as empty strings.
func insertSQL(table string, columns []string, stringParams bool) string {
	ph := "?"
	if stringParams {
		ph = "NULLIF(?, '')"
	}
	marks := make([]string, len(columns))
	for i := range marks {
		marks[i] = ph
	}
	return "INSERT INTO " + table + " (" + strings.Join(columns, ", ") + ") VALUES (" + strings.Join(marks, ", ") + ")"
}
