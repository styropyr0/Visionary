package visionary

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"reflect"
)

// OpenDB opens a database/sql pool, like sql.Open, whose statements are
// recorded as db spans. driverName is a registered driver ("sqlite", "pgx",
// "mysql"); target labels the spans with the system and database, e.g.
// "sqlite/app" or "mysql/shop".
//
//	db, err := visionary.OpenDB("sqlite", "file:app.db", "sqlite/app")
func OpenDB(driverName, dsn, target string) (*sql.DB, error) {
	// sql.Open only looks the driver up; it does not connect.
	probe, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, err
	}
	d := probe.Driver()
	_ = probe.Close()
	c, err := WrapDriver(d, target).(driver.DriverContext).OpenConnector(dsn)
	if err != nil {
		return nil, err
	}
	return sql.OpenDB(c), nil
}

// WrapDriver returns d with every statement, transaction start and end
// recorded as a db span, for registering under a new name:
//
//	sql.Register("sqlite-traced", visionary.WrapDriver(&sqlite.Driver{}, "sqlite/app"))
//
// Prefer OpenDB when the driver is already registered, or WrapConnector
// for a driver.Connector.
func WrapDriver(d driver.Driver, target string) driver.Driver {
	dbSeen.Store(true)
	return &tracedDriver{d: d, target: target}
}

// WrapConnector is WrapDriver for drivers configured through a connector:
//
//	db := sql.OpenDB(visionary.WrapConnector(connector, "postgresql/shop"))
func WrapConnector(c driver.Connector, target string) driver.Connector {
	dbSeen.Store(true)
	return &tracedConnector{c: c, drv: &tracedDriver{d: c.Driver(), target: target}}
}

type tracedDriver struct {
	d      driver.Driver
	target string
}

func (d *tracedDriver) Open(name string) (driver.Conn, error) {
	c, err := d.d.Open(name)
	if err != nil {
		return nil, err
	}
	return &tracedConn{Conn: c, target: d.target}, nil
}

func (d *tracedDriver) OpenConnector(name string) (driver.Connector, error) {
	if dc, ok := d.d.(driver.DriverContext); ok {
		c, err := dc.OpenConnector(name)
		if err != nil {
			return nil, err
		}
		return &tracedConnector{c: c, drv: d}, nil
	}
	return &tracedConnector{c: dsnConnector{name: name, d: d.d}, drv: d}, nil
}

type tracedConnector struct {
	c   driver.Connector
	drv *tracedDriver
}

func (c *tracedConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.c.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &tracedConn{Conn: conn, target: c.drv.target}, nil
}

func (c *tracedConnector) Driver() driver.Driver { return c.drv }

// Close releases the wrapped connector's resources when it has any.
func (c *tracedConnector) Close() error {
	if cl, ok := c.c.(io.Closer); ok {
		return cl.Close()
	}
	return nil
}

// dsnConnector adapts a driver without DriverContext, as database/sql does.
type dsnConnector struct {
	name string
	d    driver.Driver
}

func (c dsnConnector) Connect(context.Context) (driver.Conn, error) { return c.d.Open(c.name) }
func (c dsnConnector) Driver() driver.Driver                        { return c.d }

// tracedConn forwards every optional interface database/sql looks for and
// answers the way database/sql would when the driver lacks one.
type tracedConn struct {
	driver.Conn
	target string
}

func (c *tracedConn) span(ctx context.Context, query string) *activeSpan {
	s := beginSpan(ctx, KindDB)
	if s != nil {
		s.rec.sql = query
		s.rec.target = c.target
	}
	return s
}

func (c *tracedConn) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}

func (c *tracedConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	var st driver.Stmt
	var err error
	if pc, ok := c.Conn.(driver.ConnPrepareContext); ok {
		st, err = pc.PrepareContext(ctx, query)
	} else {
		if err = ctx.Err(); err == nil {
			st, err = c.Conn.Prepare(query)
		}
	}
	if err != nil {
		return nil, err
	}
	return &tracedStmt{Stmt: st, conn: c, query: query}, nil
}

func (c *tracedConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *tracedConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	s := c.span(ctx, "BEGIN")
	var tx driver.Tx
	var err error
	if bc, ok := c.Conn.(driver.ConnBeginTx); ok {
		tx, err = bc.BeginTx(ctx, opts)
	} else if opts.Isolation != driver.IsolationLevel(sql.LevelDefault) || opts.ReadOnly {
		err = errors.New("visionary: driver does not support non-default transaction options")
	} else {
		tx, err = c.Conn.Begin() //nolint:staticcheck // fallback for old drivers, as database/sql does
	}
	s.end(err)
	if err != nil {
		return nil, err
	}
	return &tracedTx{Tx: tx, ctx: ctx, conn: c}, nil
}

func (c *tracedConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	ec, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip // database/sql prepares instead
	}
	s := c.span(ctx, query)
	res, err := ec.ExecContext(ctx, query, args)
	if errors.Is(err, driver.ErrSkip) {
		return nil, err // not executed: the prepared path records it
	}
	endExec(s, res, err)
	return res, err
}

func (c *tracedConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	qc, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	s := c.span(ctx, query)
	rows, err := qc.QueryContext(ctx, query, args)
	if errors.Is(err, driver.ErrSkip) {
		return nil, err
	}
	return wrapRows(s, rows, err)
}

func (c *tracedConn) Ping(ctx context.Context) error {
	if p, ok := c.Conn.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (c *tracedConn) ResetSession(ctx context.Context) error {
	if r, ok := c.Conn.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

func (c *tracedConn) IsValid() bool {
	if v, ok := c.Conn.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

func (c *tracedConn) CheckNamedValue(nv *driver.NamedValue) error {
	if ch, ok := c.Conn.(driver.NamedValueChecker); ok {
		return ch.CheckNamedValue(nv)
	}
	return driver.ErrSkip // database/sql's default conversion
}

type tracedTx struct {
	driver.Tx
	ctx  context.Context
	conn *tracedConn
}

func (t *tracedTx) Commit() error {
	s := t.conn.span(t.ctx, "COMMIT")
	err := t.Tx.Commit()
	s.end(err)
	return err
}

func (t *tracedTx) Rollback() error {
	s := t.conn.span(t.ctx, "ROLLBACK")
	err := t.Tx.Rollback()
	s.end(err)
	return err
}

type tracedStmt struct {
	driver.Stmt
	conn  *tracedConn
	query string
}

func (s *tracedStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	sp := s.conn.span(ctx, s.query)
	var res driver.Result
	var err error
	if ec, ok := s.Stmt.(driver.StmtExecContext); ok {
		res, err = ec.ExecContext(ctx, args)
	} else {
		var vals []driver.Value
		if vals, err = plainValues(args); err == nil {
			res, err = s.Stmt.Exec(vals) //nolint:staticcheck // fallback for old drivers
		}
	}
	endExec(sp, res, err)
	return res, err
}

func (s *tracedStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	sp := s.conn.span(ctx, s.query)
	var rows driver.Rows
	var err error
	if qc, ok := s.Stmt.(driver.StmtQueryContext); ok {
		rows, err = qc.QueryContext(ctx, args)
	} else {
		var vals []driver.Value
		if vals, err = plainValues(args); err == nil {
			rows, err = s.Stmt.Query(vals) //nolint:staticcheck // fallback for old drivers
		}
	}
	return wrapRows(sp, rows, err)
}

// CheckNamedValue keeps the statement's checker first and the connection's
// second, the order database/sql uses for an unwrapped driver.
func (s *tracedStmt) CheckNamedValue(nv *driver.NamedValue) error {
	if ch, ok := s.Stmt.(driver.NamedValueChecker); ok {
		return ch.CheckNamedValue(nv)
	}
	return s.conn.CheckNamedValue(nv)
}

func plainValues(args []driver.NamedValue) ([]driver.Value, error) {
	vals := make([]driver.Value, len(args))
	for i, a := range args {
		if a.Name != "" {
			return nil, errors.New("visionary: driver does not support named parameters")
		}
		vals[i] = a.Value
	}
	return vals, nil
}

func endExec(s *activeSpan, res driver.Result, err error) {
	if s == nil {
		return
	}
	if err == nil && res != nil {
		if n, rerr := res.RowsAffected(); rerr == nil {
			s.rec.rows = n
		}
	}
	s.end(err)
}

// wrapRows ends the span when the rows are closed, so the time spent reading
// results counts and row_count is the number of rows read.
func wrapRows(s *activeSpan, rows driver.Rows, err error) (driver.Rows, error) {
	if err != nil || s == nil {
		s.end(err)
		return rows, err
	}
	s.rec.rows = 0
	return &tracedRows{Rows: rows, span: s}, nil
}

type tracedRows struct {
	driver.Rows
	span *activeSpan
	err  error
}

func (r *tracedRows) Next(dest []driver.Value) error {
	err := r.Rows.Next(dest)
	switch {
	case err == nil:
		r.span.rec.rows++
	case err != io.EOF && r.err == nil:
		r.err = err
	}
	return err
}

func (r *tracedRows) Close() error {
	err := r.Rows.Close()
	if r.err == nil {
		r.err = err
	}
	r.span.end(r.err)
	return err
}

func (r *tracedRows) HasNextResultSet() bool {
	if n, ok := r.Rows.(driver.RowsNextResultSet); ok {
		return n.HasNextResultSet()
	}
	return false
}

func (r *tracedRows) NextResultSet() error {
	if n, ok := r.Rows.(driver.RowsNextResultSet); ok {
		return n.NextResultSet()
	}
	return io.EOF
}

func (r *tracedRows) ColumnTypeScanType(i int) reflect.Type {
	if c, ok := r.Rows.(driver.RowsColumnTypeScanType); ok {
		return c.ColumnTypeScanType(i)
	}
	return reflect.TypeFor[any]()
}

func (r *tracedRows) ColumnTypeDatabaseTypeName(i int) string {
	if c, ok := r.Rows.(driver.RowsColumnTypeDatabaseTypeName); ok {
		return c.ColumnTypeDatabaseTypeName(i)
	}
	return ""
}

func (r *tracedRows) ColumnTypeLength(i int) (int64, bool) {
	if c, ok := r.Rows.(driver.RowsColumnTypeLength); ok {
		return c.ColumnTypeLength(i)
	}
	return 0, false
}

func (r *tracedRows) ColumnTypeNullable(i int) (bool, bool) {
	if c, ok := r.Rows.(driver.RowsColumnTypeNullable); ok {
		return c.ColumnTypeNullable(i)
	}
	return false, false
}

func (r *tracedRows) ColumnTypePrecisionScale(i int) (int64, int64, bool) {
	if c, ok := r.Rows.(driver.RowsColumnTypePrecisionScale); ok {
		return c.ColumnTypePrecisionScale(i)
	}
	return 0, 0, false
}
