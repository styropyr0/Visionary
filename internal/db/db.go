// Package db is the server's access to the tables, in Cloudflare D1 or any
// supported SQL database (PostgreSQL, MySQL, SQLite).
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	"modernc.org/sqlite"

	"github.com/styropyr0/visionary/internal/d1"
	sdk "github.com/styropyr0/visionary/sdks/go"
)

// untrustedTimeout bounds queries written by people or the model.
const untrustedTimeout = 30 * time.Second

// Rows is a result in column order. RowsRead is -1 when the database does
// not report it.
type Rows struct {
	Columns    []string
	Rows       [][]any
	RowsRead   int64
	DurationMs float64
}

type DB interface {
	Dialect() sdk.Dialect
	// Name describes the backend for people ("PostgreSQL", "Cloudflare D1").
	Name() string
	// Query runs a statement the server built. "?" placeholders.
	Query(ctx context.Context, query string, args ...any) (*Rows, error)
	// QueryUntrusted runs SQL from a person or the model: read-only and
	// time-limited wherever the database can enforce it.
	QueryUntrusted(ctx context.Context, query string) (*Rows, error)
	Exec(ctx context.Context, query string, args ...any) error
	ExecScript(ctx context.Context, script string) error
	// InsertID runs an INSERT into a table with an "id" key and returns it.
	InsertID(ctx context.Context, query string, args ...any) (int64, error)
	Close() error
}

// QueryError is the database rejecting a statement (syntax, unknown column,
// timeout), as opposed to the database being unreachable. Its message is
// safe to show and to feed back to the model.
type QueryError struct{ Err error }

func (e *QueryError) Error() string { return e.Err.Error() }
func (e *QueryError) Unwrap() error { return e.Err }

// Config chooses the backend: URL (postgres://, mysql://, sqlite:///path,
// d1://) or, when it is empty or d1://, the D1 credentials.
type Config struct {
	URL                                              string
	D1APIBase, D1AccountID, D1DatabaseID, D1APIToken string
}

func Open(cfg Config) (DB, error) {
	url := strings.TrimSpace(cfg.URL)
	if url == "" || strings.HasPrefix(strings.ToLower(url), "d1:") {
		c := d1.New(cfg.D1APIBase, cfg.D1AccountID, cfg.D1DatabaseID, cfg.D1APIToken)
		if c == nil {
			return nil, errors.New("set VISIONARY_DATABASE_URL (postgres://, mysql://, sqlite:///path) or the D1_ACCOUNT_ID, D1_DATABASE_ID and D1_API_TOKEN variables")
		}
		return d1DB{c: c}, nil
	}
	conn, d, err := sdk.OpenURL(url)
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := conn.PingContext(pingCtx); err != nil {
		conn.Close()
		return nil, fmt.Errorf("connect to %s: %w", d, err)
	}
	if d == sdk.SQLite {
		// One writer at a time; readers wait instead of failing with SQLITE_BUSY.
		_, _ = conn.Exec("PRAGMA busy_timeout = 5000")
	}
	return sqlDB{db: conn, d: d}, nil
}

// --- Cloudflare D1 ---

type d1DB struct{ c *d1.Client }

func (d d1DB) Dialect() sdk.Dialect { return sdk.SQLite }
func (d d1DB) Name() string         { return "Cloudflare D1 (SQLite)" }
func (d d1DB) Close() error         { return nil }

func (d d1DB) Query(ctx context.Context, query string, args ...any) (*Rows, error) {
	r, err := d.c.Query(ctx, query, stringArgs(args)...)
	if err != nil {
		return nil, d1Err(err)
	}
	return &Rows{Columns: r.Columns, Rows: r.Rows, RowsRead: r.RowsRead, DurationMs: r.DurationMs}, nil
}

// QueryUntrusted relies on the read-only guard: D1 has no read-only mode.
func (d d1DB) QueryUntrusted(ctx context.Context, query string) (*Rows, error) {
	ctx, cancel := context.WithTimeout(ctx, untrustedTimeout)
	defer cancel()
	return d.Query(ctx, query)
}

func (d d1DB) Exec(ctx context.Context, query string, args ...any) error {
	return d1Err(d.c.Exec(ctx, query, stringArgs(args)...))
}

func (d d1DB) ExecScript(ctx context.Context, script string) error {
	return d1Err(d.c.Exec(ctx, script))
}

func (d d1DB) InsertID(ctx context.Context, query string, args ...any) (int64, error) {
	r, err := d.Query(ctx, query+" RETURNING id", args...)
	if err != nil {
		return 0, err
	}
	if len(r.Rows) != 1 || len(r.Rows[0]) != 1 {
		return 0, errors.New("insert returned no id")
	}
	return toInt64(r.Rows[0][0]), nil
}

func d1Err(err error) error {
	var se *d1.StatusError
	if errors.As(err, &se) && se.Status < 500 {
		return &QueryError{se}
	}
	return err
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
		default:
			out[i] = fmt.Sprint(v)
		}
	}
	return out
}

// --- database/sql backends ---

type sqlDB struct {
	db *sql.DB
	d  sdk.Dialect
}

func (s sqlDB) Dialect() sdk.Dialect { return s.d }
func (s sqlDB) Close() error         { return s.db.Close() }

func (s sqlDB) Name() string {
	switch s.d {
	case sdk.Postgres:
		return "PostgreSQL"
	case sdk.MySQL:
		return "MySQL"
	}
	return "SQLite"
}

func (s sqlDB) Query(ctx context.Context, query string, args ...any) (*Rows, error) {
	start := time.Now()
	rows, err := s.db.QueryContext(ctx, s.d.Rebind(query), args...)
	if err != nil {
		return nil, s.classify(err)
	}
	return scan(rows, start, s)
}

func (s sqlDB) QueryUntrusted(ctx context.Context, query string) (*Rows, error) {
	ctx, cancel := context.WithTimeout(ctx, untrustedTimeout+5*time.Second)
	defer cancel()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	start := time.Now()

	switch s.d {
	case sdk.SQLite:
		// No read-only transactions; query_only rejects writes on this connection.
		if _, err := conn.ExecContext(ctx, "PRAGMA query_only = 1"); err != nil {
			return nil, err
		}
		defer conn.ExecContext(context.WithoutCancel(ctx), "PRAGMA query_only = 0")
		rows, err := conn.QueryContext(ctx, query)
		if err != nil {
			return nil, s.classify(err)
		}
		return scan(rows, start, s)
	case sdk.MySQL:
		ms := strconv.FormatInt(untrustedTimeout.Milliseconds(), 10)
		if _, err := conn.ExecContext(ctx, "SET SESSION MAX_EXECUTION_TIME = "+ms); err != nil {
			return nil, err
		}
	}

	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if s.d == sdk.Postgres {
		ms := strconv.FormatInt(untrustedTimeout.Milliseconds(), 10)
		if _, err := tx.ExecContext(ctx, "SET LOCAL statement_timeout = "+ms); err != nil {
			return nil, err
		}
	}
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return nil, s.classify(err)
	}
	return scan(rows, start, s)
}

func (s sqlDB) Exec(ctx context.Context, query string, args ...any) error {
	_, err := s.db.ExecContext(ctx, s.d.Rebind(query), args...)
	return s.classify(err)
}

func (s sqlDB) ExecScript(ctx context.Context, script string) error {
	for _, q := range sdk.Statements(script) {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			var me *mysql.MySQLError
			if errors.As(err, &me) && me.Number == 1061 {
				continue // index already exists; MySQL lacks IF NOT EXISTS here
			}
			return err
		}
	}
	return nil
}

func (s sqlDB) InsertID(ctx context.Context, query string, args ...any) (int64, error) {
	if s.d == sdk.MySQL {
		res, err := s.db.ExecContext(ctx, query, args...)
		if err != nil {
			return 0, s.classify(err)
		}
		return res.LastInsertId()
	}
	var id int64
	err := s.db.QueryRowContext(ctx, s.d.Rebind(query+" RETURNING id"), args...).Scan(&id)
	return id, s.classify(err)
}

// classify marks errors the database raised about the statement itself.
func (s sqlDB) classify(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &QueryError{fmt.Errorf("the query took longer than %s", untrustedTimeout)}
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		// Class 08 is a connection problem; everything else is about the query.
		if strings.HasPrefix(pg.Code, "08") {
			return err
		}
		if pg.Code == "57014" {
			return &QueryError{fmt.Errorf("the query took longer than %s", untrustedTimeout)}
		}
		return &QueryError{errors.New(pg.Message)}
	}
	var my *mysql.MySQLError
	if errors.As(err, &my) {
		return &QueryError{errors.New(my.Message)}
	}
	var lite *sqlite.Error
	if errors.As(err, &lite) {
		return &QueryError{lite}
	}
	return err
}

func scan(rows *sql.Rows, start time.Time, s sqlDB) (*Rows, error) {
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	types, err := rows.ColumnTypes()
	if err != nil {
		return nil, err
	}
	kinds := make([]kind, len(cols))
	for i, t := range types {
		kinds[i] = kindOf(t.DatabaseTypeName())
	}
	out := &Rows{Columns: cols, Rows: [][]any{}, RowsRead: -1}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		for i, v := range vals {
			vals[i] = normalize(v, kinds[i])
		}
		out.Rows = append(out.Rows, vals)
	}
	if err := rows.Err(); err != nil {
		return nil, s.classify(err)
	}
	out.DurationMs = float64(time.Since(start).Microseconds()) / 1000
	return out, nil
}

type kind int

const (
	kindOther kind = iota
	kindInt
	kindFloat
)

// kindOf maps a driver's column type name to how values should be decoded.
// MySQL sends every value as text outside prepared statements, and pgx hands
// NUMERIC over as a string, so the declared type decides.
func kindOf(name string) kind {
	switch strings.ToUpper(name) {
	case "INT", "INT2", "INT4", "INT8", "INTEGER", "BIGINT", "SMALLINT", "TINYINT", "MEDIUMINT",
		"UNSIGNED INT", "UNSIGNED BIGINT", "UNSIGNED SMALLINT", "UNSIGNED TINYINT", "UNSIGNED MEDIUMINT", "YEAR":
		return kindInt
	case "FLOAT", "FLOAT4", "FLOAT8", "DOUBLE", "DOUBLE PRECISION", "REAL", "NUMERIC", "DECIMAL":
		return kindFloat
	}
	return kindOther
}

// normalize turns driver values into JSON-friendly ones.
func normalize(v any, k kind) any {
	if b, ok := v.([]byte); ok {
		v = string(b)
	}
	switch t := v.(type) {
	case string:
		switch k {
		case kindInt:
			if n, err := strconv.ParseInt(t, 10, 64); err == nil {
				return n
			}
			fallthrough
		case kindFloat:
			if f, err := strconv.ParseFloat(t, 64); err == nil {
				return f
			}
		}
		return t
	case time.Time:
		return t.UTC().Format("2006-01-02T15:04:05.000Z")
	case int32:
		return int64(t)
	case float32:
		return float64(t)
	}
	return v
}

func toInt64(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case float64:
		return int64(t)
	case string:
		n, _ := strconv.ParseInt(t, 10, 64)
		return n
	}
	return 0
}
