package visionary

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/styropyr0/visionary/internal/d1"
)

// stmt is one write with typed args (nil for NULL).
type stmt struct {
	sql  string
	args []any
}

// store is where rows go: D1 over REST, or any database/sql connection.
type store interface {
	dialect() Dialect
	// stringParams reports whether args must travel as strings (D1).
	stringParams() bool
	execScript(ctx context.Context, script string) error
	batch(ctx context.Context, stmts []stmt) error
	execChanges(ctx context.Context, query string, args ...any) (int64, error)
	// columns runs a query returning one name per row.
	columns(ctx context.Context, query string) ([]string, error)
}

type d1Store struct{ c *d1.Client }

func (s d1Store) dialect() Dialect   { return SQLite }
func (s d1Store) stringParams() bool { return true }

func (s d1Store) execScript(ctx context.Context, script string) error {
	return s.c.Exec(ctx, script)
}

func (s d1Store) batch(ctx context.Context, stmts []stmt) error {
	out := make([]d1.Statement, len(stmts))
	for i, st := range stmts {
		out[i] = d1.Statement{SQL: st.sql, Params: stringArgs(st.args)}
	}
	return s.c.Batch(ctx, out)
}

func (s d1Store) execChanges(ctx context.Context, query string, args ...any) (int64, error) {
	return s.c.ExecChanges(ctx, query, stringArgs(args)...)
}

func (s d1Store) columns(ctx context.Context, query string) ([]string, error) {
	r, err := s.c.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, row := range r.Rows {
		if len(row) > 0 {
			out = append(out, fmt.Sprint(row[0]))
		}
	}
	return out, nil
}

func stringArgs(args []any) []string {
	out := make([]string, len(args))
	for i, a := range args {
		switch v := a.(type) {
		case nil:
			out[i] = ""
		case string:
			out[i] = v
		case int:
			out[i] = strconv.Itoa(v)
		case int64:
			out[i] = strconv.FormatInt(v, 10)
		case float64:
			out[i] = strconv.FormatFloat(v, 'f', -1, 64)
		case bool:
			out[i] = map[bool]string{true: "1", false: "0"}[v]
		default:
			out[i] = fmt.Sprint(v)
		}
	}
	return out
}

type sqlStore struct {
	db *sql.DB
	d  Dialect
}

func (s sqlStore) dialect() Dialect   { return s.d }
func (s sqlStore) stringParams() bool { return false }

func (s sqlStore) execScript(ctx context.Context, script string) error {
	for _, q := range Statements(script) {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			// MySQL has no CREATE INDEX IF NOT EXISTS; an existing index is fine.
			if s.d == MySQL && strings.Contains(err.Error(), "1061") {
				continue
			}
			return err
		}
	}
	return nil
}

// batch writes all statements in one transaction.
func (s sqlStore) batch(ctx context.Context, stmts []stmt) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	prepared := map[string]*sql.Stmt{}
	for _, st := range stmts {
		ps, ok := prepared[st.sql]
		if !ok {
			if ps, err = tx.PrepareContext(ctx, s.d.Rebind(st.sql)); err != nil {
				return err
			}
			prepared[st.sql] = ps
		}
		if _, err := ps.ExecContext(ctx, st.args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s sqlStore) execChanges(ctx context.Context, query string, args ...any) (int64, error) {
	res, err := s.db.ExecContext(ctx, s.d.Rebind(query), args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s sqlStore) columns(ctx context.Context, query string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// OpenURL opens a database/sql connection from a URL: postgres://,
// postgresql://, mysql:// or sqlite:///path. The matching driver must be
// imported by the application:
//
//	postgres: _ "github.com/jackc/pgx/v5/stdlib"   (driver "pgx")
//	mysql:    _ "github.com/go-sql-driver/mysql"   (driver "mysql")
//	sqlite:   _ "modernc.org/sqlite"               (driver "sqlite")
func OpenURL(raw string) (*sql.DB, Dialect, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, "", fmt.Errorf("visionary: database URL: %w", err)
	}
	var driver, dsn string
	var d Dialect
	switch strings.ToLower(u.Scheme) {
	case "postgres", "postgresql":
		driver, dsn, d = "pgx", raw, Postgres
	case "mysql":
		driver, d = "mysql", MySQL
		dsn = mysqlDSN(u)
	case "sqlite", "sqlite3":
		driver, d = "sqlite", SQLite
		dsn = strings.TrimPrefix(strings.TrimPrefix(raw, u.Scheme+"://"), "/")
		if strings.HasPrefix(raw, u.Scheme+":///") && !strings.Contains(dsn, ":") {
			dsn = "/" + dsn // absolute path on Unix
		}
	default:
		return nil, "", fmt.Errorf("visionary: unsupported database URL scheme %q", u.Scheme)
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, "", fmt.Errorf("visionary: open %s (import the %q driver): %w", d, driver, err)
	}
	return db, d, nil
}

// mysqlDSN converts mysql://user:pass@host:port/db?params into the
// go-sql-driver form, in UTC.
func mysqlDSN(u *url.URL) string {
	pass, _ := u.User.Password()
	q := u.Query()
	if q.Get("time_zone") == "" {
		q.Set("time_zone", "'+00:00'")
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		host += ":3306"
	}
	return fmt.Sprintf("%s:%s@tcp(%s)%s?%s", u.User.Username(), pass, host, u.Path, q.Encode())
}

var errUnknownDialect = errors.New("visionary: Config.Dialect is required with Config.Database")
