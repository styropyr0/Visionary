package visionary

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// maxDetail caps a span's detail (a statement, a URL, a GraphQL document):
// long enough that the statement can still be planned with EXPLAIN.
const maxDetail = 8000

// Span kinds, for StartSpan.
const (
	KindDB     = "db"
	KindHTTP   = "http"
	KindCache  = "cache"
	KindQueue  = "queue"
	KindCustom = "custom"
	// KindGraphQL is one GraphQL operation; see GraphQLOperation.
	KindGraphQL = "graphql"
	// KindBackground is work a request started and did not wait for; see Go.
	KindBackground = "background"
)

var spanColumns = []string{
	"created_at", "request_id", "service", "instance", "kind", "name", "target", "detail",
	"offset_ms", "duration_ms", "status", "error", "row_count",
}

// dbSeen and httpSeen record whether any hook for that kind of call exists.
// Until one does, the request's db_* or http_* totals are NULL ("cannot
// see") rather than 0 ("made none").
var dbSeen, httpSeen atomic.Bool

func markKind(kind string) {
	switch kind {
	case KindDB:
		dbSeen.Store(true)
	case KindHTTP:
		httpSeen.Store(true)
	}
}

// spanRecord is one finished call. Statements are sanitized and named on the
// writer goroutine (see sqlName), keeping the request path cheap.
type spanRecord struct {
	start     time.Time
	requestID string
	inRequest bool
	offset    time.Duration
	kind      string
	name      string
	target    string
	detail    string
	// sql, when set, is the raw statement name and detail come from; prefix
	// is put before the derived name ("BATCH ", "PREPARE ").
	sql, prefix string
	duration    time.Duration
	failed      bool
	err         string
	rows        int64 // -1 when the driver did not say
	// nested marks a call made inside another recorded call (the prepare of
	// a query): it is a span row but its time is already in the totals.
	nested bool
}

// spanTotals are the per-request aggregates stored on the api_logs row.
type spanTotals struct {
	dbCount, httpCount int64
	dbTime, httpTime   time.Duration
}

// columns returns db_count, db_ms, http_count, http_ms; NULL for a kind the
// SDK has no hook for, or for all four when span capture is off.
func (t *spanTotals) columns() (dbCount, dbMs, httpCount, httpMs any) {
	if t == nil {
		return nil, nil, nil, nil
	}
	if dbSeen.Load() || t.dbCount > 0 {
		dbCount, dbMs = t.dbCount, millis(t.dbTime)
	}
	if httpSeen.Load() || t.httpCount > 0 {
		httpCount, httpMs = t.httpCount, millis(t.httpTime)
	}
	return
}

// trace collects the spans of one logged request. Handlers may start
// goroutines that make calls, so every method locks.
type trace struct {
	mu        sync.Mutex
	requestID string
	start     time.Time
	max       int
	closed    bool
	late      int // spans that ended after the request was logged
	totals    spanTotals
	spans     []*spanRecord
}

// add records s; false means the request has already been logged.
func (t *trace) add(s *spanRecord) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return false
	}
	switch {
	case s.nested:
	case s.kind == KindDB:
		t.totals.dbCount++
		t.totals.dbTime += s.duration
	case s.kind == KindHTTP:
		t.totals.httpCount++
		t.totals.httpTime += s.duration
	}
	// Calls beyond the cap still count in the totals.
	if len(t.spans) < t.max {
		t.spans = append(t.spans, s)
	}
	return true
}

// addLate counts a span that ended after the request was logged; false
// once VISIONARY_SPAN_MAX of them were kept.
func (t *trace) addLate() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.late >= t.max {
		return false
	}
	t.late++
	return true
}

// finish closes the trace and returns its totals and its spans in start
// order. Spans that end later are stored on their own.
func (t *trace) finish() (*spanTotals, []*spanRecord) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	totals := t.totals
	spans := t.spans
	t.spans = nil
	slices.SortStableFunc(spans, func(a, b *spanRecord) int { return a.start.Compare(b.start) })
	return &totals, spans
}

// activeSpan is a call in progress.
type activeSpan struct {
	agent *Agent
	trace *trace
	rec   spanRecord
	ended atomic.Bool
}

// beginSpan starts a span, or returns nil when nothing would record it: span
// capture off, no Agent, or the SDK's own writes.
func beginSpan(ctx context.Context, kind string) *activeSpan {
	if isQuiet(ctx) {
		return nil
	}
	ann, a := fromContext(ctx)
	if !a.spansOn() {
		return nil
	}
	s := &activeSpan{agent: a, rec: spanRecord{start: time.Now(), kind: kind, rows: -1}}
	if ann != nil && ann.trace != nil {
		s.trace = ann.trace
		s.rec.requestID = ann.trace.requestID
		s.rec.inRequest = true
		s.rec.offset = s.rec.start.Sub(ann.trace.start)
	}
	return s
}

// end finishes the span. The fields of s.rec that describe the call (name,
// sql, target, detail, rows) are set by the caller first.
func (s *activeSpan) end(err error) {
	if s == nil || !s.ended.CompareAndSwap(false, true) {
		return
	}
	s.rec.duration = time.Since(s.rec.start)
	if err != nil {
		s.fail(err.Error())
	}
	if s.trace != nil {
		if s.trace.add(&s.rec) {
			return
		}
		// Background work the request started: still its call, whatever it
		// took, up to the cap; past it only slow calls, like lone spans.
		if s.trace.addLate() {
			s.agent.writer.enqueue(&s.rec)
			return
		}
	}
	s.agent.storeLoneSpan(&s.rec)
}

func (s *activeSpan) fail(msg string) {
	s.rec.failed = true
	s.rec.err = truncateRunes(msg, 500)
}

// storeLoneSpan keeps a span made outside any logged request (or after its
// request was logged) when it is slow enough to matter.
func (a *Agent) storeLoneSpan(s *spanRecord) {
	if s.duration >= a.cfg.SpanThreshold {
		a.writer.enqueue(s)
	}
}

func (a *Agent) spansOn() bool { return a != nil && a.cfg.Spans && a.writer != nil }

// StartSpan times a call the handler makes, for kinds and clients the
// built-in hooks don't cover: a cache lookup, a queue publish, a slow
// computation. kind is KindDB, KindHTTP, KindCache, KindQueue or KindCustom;
// name should be low-cardinality ("GET session", "publish emails"). Call the
// returned function once, when the call is over:
//
//	end := visionary.StartSpan(ctx, visionary.KindCache, "GET session")
//	v, err := rdb.Get(ctx, key).Result()
//	end(err)
//
// Inside a request the span belongs to it; elsewhere it is stored only when
// it takes at least the span threshold. Safe from any goroutine.
func StartSpan(ctx context.Context, kind, name string) (end func(err error)) {
	switch kind {
	case KindDB, KindHTTP, KindCache, KindQueue, KindCustom, KindGraphQL, KindBackground:
	default:
		kind = KindCustom
	}
	markKind(kind)
	s := beginSpan(ctx, kind)
	if s == nil {
		return func(error) {}
	}
	s.rec.name = truncateRunes(name, 200)
	return s.end
}

// spanParams returns the row in spanColumns order.
func (w *writer) spanParams(s *spanRecord) []any {
	name, detail := s.name, s.detail
	if s.sql != "" {
		clean := sanitizeSQL(s.sql)
		if name == "" {
			name = s.prefix + sqlName(clean)
		}
		if detail == "" {
			detail = clean
		}
	}
	if name == "" {
		name = s.kind
	}
	status := "ok"
	if s.failed {
		status = "error"
	}
	var offset, rows any
	if s.inRequest {
		offset = millis(s.offset)
	}
	if s.rows >= 0 {
		rows = s.rows
	}
	return []any{
		s.start.UTC().Format(tsLayout),
		nullable(s.requestID),
		nullable(w.ident.service),
		w.ident.instance,
		s.kind,
		name,
		nullable(s.target),
		nullable(truncateRunes(detail, maxDetail)),
		offset,
		millis(s.duration),
		status,
		nullable(s.err),
		rows,
	}
}
