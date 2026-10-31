package visionary

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// maxStatementsPerCall is D1's per-batch limit, where every row is its own
// statement.
const maxStatementsPerCall = 50

// SQL backends get each table's rows as multi-row INSERTs in one transaction,
// kept under every backend's bound-parameter limit (SQLite's is 32766).
const (
	maxParamsPerStatement = 30000
	defaultSQLBatch       = 500
	maxSQLBatch           = 2000
	parallelFlushes       = 4
)

// writer is the background goroutine that batches every row the SDK
// produces besides system samples: request logs (with their spans),
// standalone spans and errors. Nothing on a request path ever waits for it.
type writer struct {
	cfg      RequestsConfig
	db       store
	ident    identity
	logger   *slog.Logger
	redact   *redactor
	requests bool // api_logs is written (and purged)
	// logsOnly marks the writer of app_logs, which purges only that table;
	// metricsOnly the writer of metrics.
	logsOnly    bool
	metricsOnly bool
	logSecrets  map[string]bool

	insertRequest, insertSpan, insertError, insertLog, insertMetric string

	// queue holds *entry, *spanRecord and *errorRecord values.
	queue   chan any
	dropped atomic.Int64
	// lost and written count rows since start, for WriterStats.
	lost    atomic.Int64
	written atomic.Int64
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once
}

func newWriter(cfg RequestsConfig, requests bool, ident identity, db store, logger *slog.Logger) *writer {
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 5000
	}
	if db.stringParams() {
		if cfg.BatchSize <= 0 || cfg.BatchSize > maxStatementsPerCall {
			cfg.BatchSize = maxStatementsPerCall
		}
	} else if cfg.BatchSize <= 0 || cfg.BatchSize > maxSQLBatch {
		cfg.BatchSize = defaultSQLBatch
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = 3 * time.Second
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = 8 << 10
	}
	if cfg.ErrorCodeField == "" {
		cfg.ErrorCodeField = "error.code"
	}
	if cfg.ErrorKeyField == "" {
		cfg.ErrorKeyField = "error.key"
	}
	sp := db.stringParams()
	return &writer{
		cfg:           cfg,
		db:            db,
		ident:         ident,
		logger:        logger,
		redact:        newRedactor(cfg.RedactHeaders, cfg.RedactFields, cfg.RedactRequestFields),
		requests:      requests,
		insertRequest: insertSQL("api_logs", requestColumns, sp),
		insertSpan:    insertSQL("spans", spanColumns, sp),
		insertError:   insertSQL("errors", errorColumns, sp),
		insertLog:     insertSQL("app_logs", logColumns, sp),
		insertMetric:  insertSQL("metrics", metricColumns, sp),
		queue:         make(chan any, cfg.QueueSize),
		stop:          make(chan struct{}),
		done:          make(chan struct{}),
	}
}

func (w *writer) start(ctx context.Context) {
	go w.run()
	if w.cfg.RetentionDays > 0 {
		go w.runRetention(ctx)
	}
	if w.requests && !w.logsOnly {
		w.logger.Info("visionary: logging requests", "path_prefix", w.cfg.PathPrefix, "bodies", w.cfg.CaptureBodies,
			"retention_days", w.cfg.RetentionDays)
	}
}

// enqueue adds a row without blocking; when the queue is full the row is
// dropped and counted rather than slowing the caller.
func (w *writer) enqueue(row any) {
	select {
	case w.queue <- row:
	default:
		w.dropped.Add(1)
		w.lost.Add(1)
	}
}

func (w *writer) close(ctx context.Context) {
	w.once.Do(func() { close(w.stop) })
	select {
	case <-w.done:
	case <-ctx.Done():
		w.logger.Warn("visionary: log flush timed out", "pending", len(w.queue))
	}
}

func (w *writer) run() {
	defer close(w.done)
	ticker := time.NewTicker(w.cfg.FlushInterval)
	defer ticker.Stop()

	work := make(chan []any)
	var flushers sync.WaitGroup
	for range w.flushers() {
		flushers.Add(1)
		go func() {
			defer flushers.Done()
			for b := range work {
				w.write(b)
			}
		}()
	}
	defer func() {
		close(work)
		flushers.Wait()
	}()

	batch := make([]any, 0, w.cfg.BatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		work <- batch
		batch = make([]any, 0, w.cfg.BatchSize)
	}
	for {
		select {
		case row := <-w.queue:
			batch = append(batch, row)
			if len(batch) >= w.cfg.BatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
			if n := w.dropped.Swap(0); n > 0 {
				w.logger.Warn("visionary: queue full; rows dropped", "dropped", n)
			}
		case <-w.stop:
			for {
				select {
				case row := <-w.queue:
					batch = append(batch, row)
					if len(batch) >= w.cfg.BatchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		}
	}
}

// flushers is how many batches may be written at once. SQLite and D1 take
// one writer at a time; PostgreSQL and MySQL insert in parallel.
func (w *writer) flushers() int {
	if w.db.stringParams() || w.db.dialect() == SQLite {
		return 1
	}
	return parallelFlushes
}

func (w *writer) write(batch []any) {
	ctx, cancel := context.WithTimeout(quiet(context.Background()), 60*time.Second)
	defer cancel()

	users := w.lookupUsers(ctx, batch)
	reqs := rowGroup{table: "api_logs", columns: requestColumns, insert: w.insertRequest}
	spans := rowGroup{table: "spans", columns: spanColumns, insert: w.insertSpan}
	errs := rowGroup{table: "errors", columns: errorColumns, insert: w.insertError}
	logs := rowGroup{table: "app_logs", columns: logColumns, insert: w.insertLog}
	metrics := rowGroup{table: "metrics", columns: metricColumns, insert: w.insertMetric}
	for _, row := range batch {
		switch r := row.(type) {
		case *entry:
			reqs.rows = append(reqs.rows, w.requestParams(r, users))
			for _, s := range r.Spans {
				spans.rows = append(spans.rows, w.spanParams(s))
			}
		case *spanRecord:
			spans.rows = append(spans.rows, w.spanParams(r))
		case *errorRecord:
			errs.rows = append(errs.rows, w.errorParams(r))
		case *logRecord:
			logs.rows = append(logs.rows, w.logParams(r))
		case *metricRecord:
			metrics.rows = append(metrics.rows, w.metricParams(r))
		}
	}
	d1 := w.db.stringParams()
	var stmts []stmt
	for _, g := range []*rowGroup{&reqs, &spans, &errs, &logs, &metrics} {
		stmts = g.statements(stmts, d1)
	}
	// D1 takes 50 statements a call, so a batch can need several calls, each
	// retried on its own.
	perCall := len(stmts)
	if d1 {
		perCall = maxStatementsPerCall
	}
	for len(stmts) > 0 {
		n := min(len(stmts), perCall)
		if !w.send(ctx, stmts[:n]) {
			lost := rowCount(stmts)
			w.logger.Error("visionary: batch write failed; rows dropped", "count", lost)
			w.lost.Add(lost)
			return
		}
		w.written.Add(rowCount(stmts[:n]))
		stmts = stmts[n:]
	}
}

type rowGroup struct {
	table   string
	columns []string
	// insert is the single-row statement D1 runs once per row.
	insert string
	rows   [][]any
}

// statements appends the group's inserts: one per row for D1, otherwise
// multi-row INSERTs as large as the parameter limit allows.
func (g *rowGroup) statements(out []stmt, d1 bool) []stmt {
	if d1 {
		for _, r := range g.rows {
			out = append(out, stmt{sql: g.insert, args: r, rows: 1})
		}
		return out
	}
	per := max(1, maxParamsPerStatement/len(g.columns))
	for rows := g.rows; len(rows) > 0; {
		n := min(len(rows), per)
		args := make([]any, 0, n*len(g.columns))
		for _, r := range rows[:n] {
			args = append(args, r...)
		}
		out = append(out, stmt{sql: insertRowsSQL(g.table, g.columns, n), args: args, rows: n})
		rows = rows[n:]
	}
	return out
}

func rowCount(stmts []stmt) int64 {
	var n int64
	for _, st := range stmts {
		n += int64(max(st.rows, 1))
	}
	return n
}

// send writes stmts, retrying up to 3 attempts in total.
func (w *writer) send(ctx context.Context, stmts []stmt) bool {
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		if err = w.db.batch(ctx, stmts); err == nil {
			return true
		}
		if attempt < 3 && !sleepCtx(ctx, time.Duration(attempt)*time.Second) {
			break
		}
	}
	w.logger.Error("visionary: write failed", "err", err)
	return false
}

// runRetention purges expired rows a minute after start, then daily.
func (w *writer) runRetention(ctx context.Context) {
	ctx = quiet(ctx)
	d := w.db.dialect()
	tables := []string{"spans", "errors"}
	if w.requests {
		tables = append(tables, "api_logs")
	}
	if w.logsOnly {
		tables = []string{"app_logs"}
	}
	if w.metricsOnly {
		tables = []string{"metrics"}
	}
	runDaily(ctx, func() {
		for _, t := range tables {
			purge(ctx, w.db, w.logger, d.purgeSQL(t), w.cfg.RetentionDays, t)
		}
	})
}

func runDaily(ctx context.Context, f func()) {
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			f()
			timer.Reset(24 * time.Hour)
		}
	}
}

// purge deletes in chunks so no single statement runs long or hits D1's limits.
func purge(ctx context.Context, db store, logger *slog.Logger, sql string, days int, table string) {
	cutoff := time.Now().UTC().AddDate(0, 0, -days).Format(tsLayout)
	var total int64
	for range 200 {
		reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		changes, err := db.execChanges(reqCtx, sql, cutoff)
		cancel()
		if err != nil {
			logger.Error("visionary: retention purge failed", "table", table, "deleted", total, "err", err)
			return
		}
		total += changes
		if changes < 5000 {
			break
		}
	}
	if total > 0 {
		logger.Info("visionary: retention purge", "table", table, "deleted", total, "cutoff", cutoff)
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
