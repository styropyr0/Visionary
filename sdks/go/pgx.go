package visionary

import (
	"context"
	"strings"
	"sync/atomic"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// PgxTracer returns a pgx v5 tracer that records every query, batch,
// CopyFrom and explicit Prepare as a db span. Tracers already on the config
// keep working: pass them and they are called after this one.
//
//	cfg, _ := pgxpool.ParseConfig(url)
//	cfg.ConnConfig.Tracer = visionary.PgxTracer(cfg.ConnConfig.Tracer)
//	pool, _ := pgxpool.NewWithConfig(ctx, cfg)
//
// Use one tracer per pool: it labels spans with the pool's database.
func PgxTracer(next ...pgx.QueryTracer) pgx.QueryTracer {
	dbSeen.Store(true)
	t := &pgxTracer{}
	for _, n := range next {
		if n != nil {
			t.next = append(t.next, n)
		}
	}
	return t
}

type pgxTracer struct {
	next   []pgx.QueryTracer
	target atomic.Pointer[string]
}

type (
	pgxQueryKey   struct{}
	pgxBatchKey   struct{}
	pgxCopyKey    struct{}
	pgxPrepareKey struct{}
)

// batchSpan accumulates the queries of one SendBatch.
type batchSpan struct {
	span *activeSpan
	sqls []string
	rows int64
	err  error
}

func (t *pgxTracer) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if s := beginSpan(ctx, KindDB); s != nil {
		s.rec.sql = data.SQL
		ctx = context.WithValue(ctx, pgxQueryKey{}, s)
	}
	for _, n := range t.next {
		ctx = n.TraceQueryStart(ctx, conn, data)
	}
	return ctx
}

func (t *pgxTracer) TraceQueryEnd(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryEndData) {
	for _, n := range t.next {
		n.TraceQueryEnd(ctx, conn, data)
	}
	if s, ok := ctx.Value(pgxQueryKey{}).(*activeSpan); ok {
		s.rec.target = t.targetOf(conn)
		s.rec.rows = rowsOf(data.CommandTag)
		s.end(data.Err)
	}
}

// TraceBatchStart and friends make one span per batch, named after its first
// statement ("BATCH INSERT bookings"), with every statement in the detail.
func (t *pgxTracer) TraceBatchStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceBatchStartData) context.Context {
	if s := beginSpan(ctx, KindDB); s != nil {
		b := &batchSpan{span: s}
		if data.Batch != nil {
			for _, q := range data.Batch.QueuedQueries {
				b.sqls = append(b.sqls, q.SQL)
			}
		}
		ctx = context.WithValue(ctx, pgxBatchKey{}, b)
	}
	for _, n := range t.next {
		if bt, ok := n.(pgx.BatchTracer); ok {
			ctx = bt.TraceBatchStart(ctx, conn, data)
		}
	}
	return ctx
}

func (t *pgxTracer) TraceBatchQuery(ctx context.Context, conn *pgx.Conn, data pgx.TraceBatchQueryData) {
	for _, n := range t.next {
		if bt, ok := n.(pgx.BatchTracer); ok {
			bt.TraceBatchQuery(ctx, conn, data)
		}
	}
	// Queries of one batch are read in order on the caller's goroutine.
	if b, ok := ctx.Value(pgxBatchKey{}).(*batchSpan); ok {
		if r := rowsOf(data.CommandTag); r > 0 {
			b.rows += r
		}
		if b.err == nil && data.Err != nil {
			b.err = data.Err
		}
	}
}

func (t *pgxTracer) TraceBatchEnd(ctx context.Context, conn *pgx.Conn, data pgx.TraceBatchEndData) {
	for _, n := range t.next {
		if bt, ok := n.(pgx.BatchTracer); ok {
			bt.TraceBatchEnd(ctx, conn, data)
		}
	}
	b, ok := ctx.Value(pgxBatchKey{}).(*batchSpan)
	if !ok {
		return
	}
	s := b.span
	s.rec.target = t.targetOf(conn)
	s.rec.rows = b.rows
	s.rec.prefix = "BATCH "
	if len(b.sqls) > 0 {
		s.rec.sql = b.sqls[0]
		clean := make([]string, 0, len(b.sqls))
		size := 0
		for _, q := range b.sqls {
			if size > 2000 {
				break
			}
			c := sanitizeSQL(q)
			clean = append(clean, c)
			size += len(c) + 2
		}
		s.rec.detail = strings.Join(clean, "; ")
	} else {
		s.rec.name = "BATCH"
	}
	err := data.Err
	if err == nil {
		err = b.err
	}
	s.end(err)
}

func (t *pgxTracer) TraceCopyFromStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceCopyFromStartData) context.Context {
	if s := beginSpan(ctx, KindDB); s != nil {
		table := data.TableName.Sanitize()
		s.rec.name = "COPY " + strings.Trim(lastPart(data.TableName), `"`)
		s.rec.detail = "COPY " + table + " (" + strings.Join(data.ColumnNames, ", ") + ") FROM STDIN"
		ctx = context.WithValue(ctx, pgxCopyKey{}, s)
	}
	for _, n := range t.next {
		if ct, ok := n.(pgx.CopyFromTracer); ok {
			ctx = ct.TraceCopyFromStart(ctx, conn, data)
		}
	}
	return ctx
}

func (t *pgxTracer) TraceCopyFromEnd(ctx context.Context, conn *pgx.Conn, data pgx.TraceCopyFromEndData) {
	for _, n := range t.next {
		if ct, ok := n.(pgx.CopyFromTracer); ok {
			ct.TraceCopyFromEnd(ctx, conn, data)
		}
	}
	if s, ok := ctx.Value(pgxCopyKey{}).(*activeSpan); ok {
		s.rec.target = t.targetOf(conn)
		s.rec.rows = rowsOf(data.CommandTag)
		s.end(data.Err)
	}
}

// TracePrepareStart records explicit Prepare calls that reach the server;
// already prepared statements cost nothing and are skipped.
func (t *pgxTracer) TracePrepareStart(ctx context.Context, conn *pgx.Conn, data pgx.TracePrepareStartData) context.Context {
	if s := beginSpan(ctx, KindDB); s != nil {
		s.rec.sql = data.SQL
		s.rec.prefix = "PREPARE "
		ctx = context.WithValue(ctx, pgxPrepareKey{}, s)
	}
	for _, n := range t.next {
		if pt, ok := n.(pgx.PrepareTracer); ok {
			ctx = pt.TracePrepareStart(ctx, conn, data)
		}
	}
	return ctx
}

func (t *pgxTracer) TracePrepareEnd(ctx context.Context, conn *pgx.Conn, data pgx.TracePrepareEndData) {
	for _, n := range t.next {
		if pt, ok := n.(pgx.PrepareTracer); ok {
			pt.TracePrepareEnd(ctx, conn, data)
		}
	}
	if s, ok := ctx.Value(pgxPrepareKey{}).(*activeSpan); ok && !data.AlreadyPrepared {
		s.rec.target = t.targetOf(conn)
		s.end(data.Err)
	}
}

// TraceConnectStart learns the database name cheaply and forwards to chained
// tracers that watch connects.
func (t *pgxTracer) TraceConnectStart(ctx context.Context, data pgx.TraceConnectStartData) context.Context {
	if data.ConnConfig != nil && t.target.Load() == nil {
		t.setTarget(data.ConnConfig.Database)
	}
	for _, n := range t.next {
		if ct, ok := n.(pgx.ConnectTracer); ok {
			ctx = ct.TraceConnectStart(ctx, data)
		}
	}
	return ctx
}

func (t *pgxTracer) TraceConnectEnd(ctx context.Context, data pgx.TraceConnectEndData) {
	for _, n := range t.next {
		if ct, ok := n.(pgx.ConnectTracer); ok {
			ct.TraceConnectEnd(ctx, data)
		}
	}
}

// targetOf is "postgresql/<database>". Conn.Config copies the whole config,
// so the name is read once per tracer.
func (t *pgxTracer) targetOf(conn *pgx.Conn) string {
	if p := t.target.Load(); p != nil {
		return *p
	}
	if conn == nil {
		return "postgresql"
	}
	return t.setTarget(conn.Config().Database)
}

func (t *pgxTracer) setTarget(db string) string {
	target := "postgresql"
	if db != "" {
		target += "/" + db
	}
	t.target.Store(&target)
	return target
}

// rowsOf is the row count in a command tag ("SELECT 5", "UPDATE 2"), or -1
// when the tag has none ("BEGIN").
func rowsOf(tag pgconn.CommandTag) int64 {
	s := tag.String()
	if s == "" || !isDigit(s[len(s)-1]) {
		return -1
	}
	return tag.RowsAffected()
}

func lastPart(id pgx.Identifier) string {
	if len(id) == 0 {
		return ""
	}
	return id[len(id)-1]
}
