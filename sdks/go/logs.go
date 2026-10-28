package visionary

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/styropyr0/visionary/sdks/go/logrules"
)

// Lines below the level are held in the request until it ends, then written
// or dropped, so a failed request keeps its debug lines. Nothing on a request
// path waits for the database.

// Severities as stored in app_logs.severity.
const (
	sevTrace = logrules.Trace
	sevDebug = logrules.Debug
	sevInfo  = logrules.Info
	sevWarn  = logrules.Warn
	sevError = logrules.Error
	sevFatal = logrules.Fatal
)

// ParseLevel reads a level name as a severity, 1 trace to 6 fatal; 0 when unknown.
func ParseLevel(s string) int { return logrules.ParseLevel(s) }

const (
	// Past maxHeldLogs a request drops its oldest held lines.
	maxHeldLogs     = 500
	logSettingsPoll = 30 * time.Second
)

var logHost = hostname()

var logColumns = []string{
	"created_at", "service", "instance", "version", "source", "host", "container", "level", "severity", "logger",
	"message", "attrs", "request_id", "trace_id", "span_id", "user_id", "session_id", "code_file", "code_line",
	"code_func", "size",
}

// Redaction and encoding happen on the writer goroutine.
type logRecord struct {
	at        time.Time
	sev       int
	logger    string
	message   string
	attrs     map[string]any
	requestID string
	traceID   string
	spanID    string
	userID    string
	sessionID string
	file      string
	line      int
	fn        string
}

type heldLogs struct {
	lines   []*logRecord
	dropped int
}

type logger struct {
	a    *Agent
	w    *writer
	base int
	set  atomic.Pointer[logrules.Settings]

	// Per-minute cap; drops are reported once the minute ends.
	rateMu     sync.Mutex
	rateMinute int64
	rateCount  int
	rateDrop   int

	stop chan struct{}
	done chan struct{}
	once sync.Once
}

func newLogger(a *Agent, w *writer, base int) *logger {
	l := &logger{a: a, w: w, base: base, stop: make(chan struct{}), done: make(chan struct{})}
	l.set.Store(logrules.Defaults(base))
	return l
}

func (l *logger) settings() *logrules.Settings { return l.set.Load() }

func (l *logger) start(ctx context.Context) {
	go func() {
		defer close(l.done)
		failed := false
		t := time.NewTimer(0)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-l.stop:
				return
			case <-t.C:
			}
			qctx, cancel := context.WithTimeout(quiet(ctx), 15*time.Second)
			rows, err := l.a.db.rows(qctx, "SELECT name, value FROM visionary_settings WHERE name LIKE 'logs.%'")
			cancel()
			switch {
			case err != nil:
				// No server or table yet; keep the old settings.
				if !failed {
					l.a.cfg.Logger.Debug("visionary: log settings unavailable; using defaults", "err", err)
					failed = true
				}
			default:
				failed = false
				l.set.Store(logrules.Parse(rows, l.base))
			}
			t.Reset(logSettingsPoll)
		}
	}()
}

func (l *logger) close() {
	l.once.Do(func() { close(l.stop) })
}

// capture never blocks.
func (l *logger) capture(ctx context.Context, r *logRecord) {
	s := l.settings()
	// Skip the SDK's own messages, or a write failure would queue behind the failed write.
	if !s.Enabled || isQuiet(ctx) || strings.HasPrefix(r.message, "visionary: ") {
		return
	}
	service := l.a.ident.service
	if s.Dropped(service, r.logger, r.message, r.sev) {
		return
	}
	ann, _ := ctx.Value(ctxKey{}).(*annotations)
	if ann == nil {
		if r.sev >= s.Floor(service, "", "", r.at) {
			l.emit(r)
		}
		return
	}
	ann.mu.Lock()
	if r.requestID == "" {
		r.requestID = ann.requestID
	}
	if r.traceID == "" {
		r.traceID = ann.traceID
	}
	if r.userID == "" {
		r.userID = ann.user.ID
	}
	if ann.done {
		// Background work the request left running; its route and user are known now.
		route, user := ann.route, ann.user.ID
		ann.mu.Unlock()
		if r.sev >= s.Floor(service, route, user, r.at) {
			l.emit(r)
		}
		return
	}
	if r.sev >= s.HoldBelow(service, r.at) {
		ann.mu.Unlock()
		l.emit(r)
		return
	}
	if ann.logs == nil {
		ann.logs = &heldLogs{}
	}
	h := ann.logs
	if len(h.lines) >= maxHeldLogs {
		h.lines = slices.Delete(h.lines, 0, 1)
		h.dropped++
	}
	h.lines = append(h.lines, r)
	ann.mu.Unlock()
}

// finish keeps held lines at the route/user level, or at the buffer level when
// the request failed or was slow.
func (l *logger) finish(ann *annotations, route string, failed bool, took time.Duration) {
	ann.mu.Lock()
	h := ann.logs
	ann.logs = nil
	user := ann.user.ID
	ann.mu.Unlock()
	if h == nil || len(h.lines) == 0 {
		return
	}
	s := l.settings()
	floor := s.Floor(l.a.ident.service, route, user, time.Now())
	keepAll := s.Buffer && (failed || (s.Slow > 0 && took >= s.Slow))
	for _, r := range h.lines {
		if r.userID == "" {
			r.userID = user
		}
		if r.sev >= floor || (keepAll && r.sev >= s.BufferLevel) {
			l.emit(r)
		}
	}
}

// Errors and fatals get ten times the per-minute cap so an info flood never hides them.
func (l *logger) emit(r *logRecord) {
	s := l.settings()
	if s.Rate > 0 {
		minute := r.at.Unix() / 60
		l.rateMu.Lock()
		if minute != l.rateMinute {
			if l.rateDrop > 0 {
				drop := l.rateDrop
				l.rateMu.Unlock()
				l.w.enqueue(&logRecord{at: time.Now(), sev: sevWarn, logger: "visionary",
					message: fmt.Sprintf("visionary: %d log lines dropped over the cap of %d a minute (Settings → Logs)", drop, s.Rate)})
				l.rateMu.Lock()
			}
			l.rateMinute, l.rateCount, l.rateDrop = minute, 0, 0
		}
		l.rateCount++
		over := l.rateCount > s.Rate && (r.sev < sevError || l.rateCount > 10*s.Rate)
		if over {
			l.rateDrop++
		}
		l.rateMu.Unlock()
		if over {
			return
		}
	}
	l.w.enqueue(r)
}

// logParams returns the row in logColumns order.
func (w *writer) logParams(r *logRecord) []any {
	msg := logrules.Truncate(logrules.Scrub(r.message), logrules.MaxMessage)
	attrs := ""
	if len(r.attrs) > 0 {
		attrs = logrules.EncodeAttrs(r.attrs, w.logSecrets)
	}
	var line any
	if r.line > 0 {
		line = r.line
	}
	return []any{
		r.at.UTC().Format(tsLayout),
		nullable(w.ident.service),
		nullable(w.ident.instance),
		nullable(w.ident.version),
		"go",
		nullable(logHost),
		nil,
		logrules.Name(r.sev),
		r.sev,
		nullable(r.logger),
		msg,
		nullable(attrs),
		nullable(r.requestID),
		nullable(r.traceID),
		nullable(r.spanID),
		nullable(r.userID),
		nullable(r.sessionID),
		nullable(r.file),
		line,
		nullable(r.fn),
		len(msg) + len(attrs),
	}
}
