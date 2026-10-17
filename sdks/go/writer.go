package visionary

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// maxStatementsPerCall is D1's per-batch limit; SQL backends use it too, so
// one slow transaction never holds a large lock.
const maxStatementsPerCall = 50

// writer is the one background goroutine that batches every row the SDK
// produces besides system samples: request logs (with their spans),
// standalone spans and errors. Nothing on a request path ever waits for it.
type writer struct {
	cfg      RequestsConfig
	db       store
	ident    identity
	logger   *slog.Logger
	redact   *redactor
	requests bool // api_logs is written (and purged)

	insertRequest, insertSpan, insertError string

	// queue holds *entry, *spanRecord and *errorRecord values.
	queue   chan any
	dropped atomic.Int64
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once
}

func newWriter(cfg RequestsConfig, requests bool, ident identity, db store, logger *slog.Logger) *writer {
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 5000
	}
	if cfg.BatchSize <= 0 || cfg.BatchSize > 50 {
		cfg.BatchSize = 50
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
	if w.requests {
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

	batch := make([]any, 0, w.cfg.BatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		w.write(batch)
		clear(batch)
		batch = batch[:0]
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

func (w *writer) write(batch []any) {
	ctx, cancel := context.WithTimeout(quiet(context.Background()), 60*time.Second)
	defer cancel()

	users := w.lookupUsers(ctx, batch)
	stmts := make([]stmt, 0, len(batch))
	for _, row := range batch {
		switch r := row.(type) {
		case *entry:
			stmts = append(stmts, stmt{sql: w.insertRequest, args: w.requestParams(r, users)})
			for _, s := range r.Spans {
				stmts = append(stmts, stmt{sql: w.insertSpan, args: w.spanParams(s)})
			}
		case *spanRecord:
			stmts = append(stmts, stmt{sql: w.insertSpan, args: w.spanParams(r)})
		case *errorRecord:
			stmts = append(stmts, stmt{sql: w.insertError, args: w.errorParams(r)})
		}
	}
	// A request's spans can push a batch past one call's limit; send it in
	// chunks, each retried on its own.
	for len(stmts) > 0 {
		n := min(len(stmts), maxStatementsPerCall)
		if !w.send(ctx, stmts[:n]) {
			w.logger.Error("visionary: batch write failed; rows dropped", "count", len(stmts))
			return
		}
		stmts = stmts[n:]
	}
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
