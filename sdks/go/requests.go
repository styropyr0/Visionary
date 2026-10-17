package visionary

import (
	"context"
	"log/slog"
	"math"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

const tsLayout = "2006-01-02T15:04:05.000Z"

// requestColumns is the insert order; params builds rows in it.
var requestColumns = []string{
	"request_id", "created_at", "method", "path", "route", "query", "status", "duration_ms", "ip", "user_agent",
	"user_id", "user_handle", "user_role", "request_headers", "request_content_type", "request_body",
	"request_body_truncated", "request_size", "response_content_type", "response_body",
	"response_body_truncated", "response_size", "error_code", "error_key",
}

// entry is the raw capture of one request. Redaction and user lookup happen
// on the writer goroutine, off the request path.
type entry struct {
	RequestID           string
	StartedAt           time.Time
	Method              string
	Path                string
	Route               string
	Query               string
	Status              int
	Duration            time.Duration
	IP                  string
	UserAgent           string
	User                User
	RequestHeaders      http.Header
	RequestContentType  string
	RequestBody         []byte
	RequestTruncated    bool
	RequestSize         int64
	ResponseContentType string
	ResponseBody        []byte
	ResponseTruncated   bool
	ResponseSize        int64
}

type requestWriter struct {
	cfg     RequestsConfig
	db      store
	insert  string
	logger  *slog.Logger
	redact  *redactor
	queue   chan *entry
	dropped atomic.Int64
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once
}

func newRequestWriter(cfg RequestsConfig, db store, logger *slog.Logger) *requestWriter {
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
	return &requestWriter{
		cfg:    cfg,
		db:     db,
		insert: insertSQL("api_logs", requestColumns, db.stringParams()),
		logger: logger,
		redact: newRedactor(cfg.RedactHeaders, cfg.RedactFields, cfg.RedactRequestFields),
		queue:  make(chan *entry, cfg.QueueSize),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
}

func (w *requestWriter) start(ctx context.Context) {
	go w.run()
	if w.cfg.RetentionDays > 0 {
		go w.runRetention(ctx)
	}
	w.logger.Info("visionary: logging requests", "path_prefix", w.cfg.PathPrefix, "bodies", w.cfg.CaptureBodies,
		"retention_days", w.cfg.RetentionDays)
}

// log enqueues e without blocking; when the queue is full the entry is
// dropped and counted rather than slowing the request.
func (w *requestWriter) log(e *entry) {
	select {
	case w.queue <- e:
	default:
		w.dropped.Add(1)
	}
}

func (w *requestWriter) close(ctx context.Context) {
	w.once.Do(func() { close(w.stop) })
	select {
	case <-w.done:
	case <-ctx.Done():
		w.logger.Warn("visionary: request log flush timed out", "pending", len(w.queue))
	}
}

func (w *requestWriter) run() {
	defer close(w.done)
	ticker := time.NewTicker(w.cfg.FlushInterval)
	defer ticker.Stop()

	batch := make([]*entry, 0, w.cfg.BatchSize)
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
		case e := <-w.queue:
			batch = append(batch, e)
			if len(batch) >= w.cfg.BatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
			if n := w.dropped.Swap(0); n > 0 {
				w.logger.Warn("visionary: request queue full; entries dropped", "dropped", n)
			}
		case <-w.stop:
			for {
				select {
				case e := <-w.queue:
					batch = append(batch, e)
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

func (w *requestWriter) write(batch []*entry) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	users := w.lookupUsers(ctx, batch)
	stmts := make([]stmt, 0, len(batch))
	for _, e := range batch {
		u := e.User
		if full, ok := users[u.ID]; ok {
			if u.Handle == "" {
				u.Handle = full.Handle
			}
			if u.Role == "" {
				u.Role = full.Role
			}
		}
		stmts = append(stmts, stmt{sql: w.insert, args: w.params(e, u)})
	}

	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		if err = w.db.batch(ctx, stmts); err == nil {
			return
		}
		if attempt < 3 && !sleepCtx(ctx, time.Duration(attempt)*time.Second) {
			break
		}
	}
	w.logger.Error("visionary: request batch write failed; entries dropped", "count", len(batch), "err", err)
}

// lookupUsers resolves each distinct user missing a handle or role once.
func (w *requestWriter) lookupUsers(ctx context.Context, batch []*entry) map[string]User {
	out := map[string]User{}
	if w.cfg.LookupUser == nil {
		return out
	}
	for _, e := range batch {
		id := e.User.ID
		if id == "" || (e.User.Handle != "" && e.User.Role != "") {
			continue
		}
		if _, seen := out[id]; seen {
			continue
		}
		if u, ok := w.cfg.LookupUser(ctx, id); ok {
			out[id] = u
		} else {
			out[id] = User{}
		}
	}
	return out
}

// params returns the row in requestColumns order, typed, with nil for NULL.
func (w *requestWriter) params(e *entry, u User) []any {
	var errCode, errKey string
	if e.Status >= 400 {
		errCode, errKey = errorFields(e.ResponseBody, w.cfg.ErrorCodeField, w.cfg.ErrorKeyField)
	}
	reqBody, respBody := "", ""
	if w.cfg.CaptureBodies {
		reqBody = w.redact.requestBody(e.RequestBody, e.RequestContentType)
		respBody = w.redact.responseBody(e.ResponseBody)
	}
	return []any{
		e.RequestID,
		e.StartedAt.UTC().Format(tsLayout),
		e.Method,
		e.Path,
		nullable(e.Route),
		nullable(e.Query),
		e.Status,
		math.Round(float64(e.Duration.Microseconds())) / 1000,
		nullable(e.IP),
		nullable(e.UserAgent),
		nullable(u.ID),
		nullable(u.Handle),
		nullable(u.Role),
		nullable(w.redact.headers(e.RequestHeaders)),
		nullable(e.RequestContentType),
		nullable(reqBody),
		boolInt(e.RequestTruncated),
		e.RequestSize,
		nullable(e.ResponseContentType),
		nullable(respBody),
		boolInt(e.ResponseTruncated),
		e.ResponseSize,
		nullable(errCode),
		nullable(errKey),
	}
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// runRetention purges expired rows a minute after start, then daily.
func (w *requestWriter) runRetention(ctx context.Context) {
	sql := w.db.dialect().purgeSQL("api_logs")
	runDaily(ctx, func() { purge(ctx, w.db, w.logger, sql, w.cfg.RetentionDays, "api_logs") })
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

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
