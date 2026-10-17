package analytics

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/styropyr0/visionary/sdks/go"
)

// rumWriter batches client events in memory and inserts them in the
// background, so a browser's beacon never waits on the database and many
// small batches become a few statements.
type rumWriter struct {
	s       *Server
	mu      sync.Mutex
	pending []clientRow
	wake    chan struct{}
	dropped int
}

const (
	rumQueueMax   = 20000
	rumFlushRows  = 500
	rumFlushEvery = 2 * time.Second
)

func newRUMWriter(s *Server) *rumWriter {
	return &rumWriter{s: s, wake: make(chan struct{}, 1)}
}

// enqueue adds a request's rows, or reports false when the queue is full
// (the SDK then retries once, later).
func (w *rumWriter) enqueue(rows []clientRow) bool {
	w.mu.Lock()
	if len(w.pending)+len(rows) > rumQueueMax {
		w.dropped += len(rows)
		w.mu.Unlock()
		return false
	}
	w.pending = append(w.pending, rows...)
	full := len(w.pending) >= rumFlushRows
	w.mu.Unlock()
	if full {
		select {
		case w.wake <- struct{}{}:
		default:
		}
	}
	return true
}

func (w *rumWriter) run(ctx context.Context) {
	t := time.NewTicker(rumFlushEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			// Drain what is queued, bounded so shutdown cannot hang.
			drain, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			w.flush(drain)
			cancel()
			return
		case <-t.C:
		case <-w.wake:
		}
		w.flush(ctx)
	}
}

func (w *rumWriter) flush(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			w.s.logger.Error("client_events writer panicked", "panic", r)
		}
	}()
	for {
		w.mu.Lock()
		n := min(len(w.pending), rumFlushRows)
		batch := w.pending[:n:n]
		w.pending = w.pending[n:]
		if len(w.pending) == 0 {
			w.pending = nil
		}
		dropped := w.dropped
		w.dropped = 0
		w.mu.Unlock()
		if dropped > 0 {
			w.s.logger.Warn("client_events queue full; events dropped", "dropped", dropped)
		}
		if n == 0 {
			return
		}
		w.write(ctx, batch)
	}
}

// write inserts a batch, trying three times with 1 s and 2 s pauses.
func (w *rumWriter) write(ctx context.Context, rows []clientRow) {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		if err = w.s.insertClientRows(ctx, rows); err == nil {
			return
		}
	}
	w.s.logger.Error("write client_events", "rows", len(rows), "err", err)
}

const clientColumns = `created_at, received_at, app, app_version, version, session_id, user_id, device, os, country,
	kind, name, url, method, status, duration_ms, value, message, stack, fingerprint, attrs`

const clientColumnCount = 21

// values is the row in clientColumns order: text, integer, float or nil.
func (r clientRow) values() []any {
	str := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	var status, dur, val any
	if r.Status != nil {
		status = *r.Status
	}
	if r.DurationMs != nil {
		dur = *r.DurationMs
	}
	if r.Value != nil {
		val = *r.Value
	}
	return []any{
		r.CreatedAt, r.ReceivedAt, r.App, str(r.AppVersion), str(r.Version), str(r.SessionID), str(r.UserID),
		str(r.Device), str(r.OS), str(r.Country), r.Kind, str(r.Name), str(r.URL), str(r.Method),
		status, dur, val, str(r.Message), str(r.Stack), str(r.Fingerprint), str(r.Attrs),
	}
}

func (s *Server) insertClientRows(ctx context.Context, rows []clientRow) error {
	if s.q.d == sdk.SQLite {
		return s.insertClientJSON(ctx, rows)
	}
	// One multi-row statement per 200 rows (4200 parameters).
	for len(rows) > 0 {
		n := min(len(rows), 200)
		var b strings.Builder
		b.WriteString("INSERT INTO client_events (" + clientColumns + ") VALUES ")
		args := make([]any, 0, n*clientColumnCount)
		for i, r := range rows[:n] {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString("(?" + strings.Repeat(", ?", clientColumnCount-1) + ")")
			args = append(args, r.values()...)
		}
		if err := s.db.Exec(ctx, b.String(), args...); err != nil {
			return err
		}
		rows = rows[n:]
	}
	return nil
}

// insertClientJSON writes SQLite and D1 rows with one statement per chunk:
// the rows travel as a single JSON array parameter that json_each unpacks.
// D1 caps a statement at 100 bound parameters (about four rows of 21
// columns) and only takes string parameters, so this avoids both limits,
// and JSON keeps the integers, floats and NULLs typed.
func (s *Server) insertClientJSON(ctx context.Context, rows []clientRow) error {
	var sel strings.Builder
	for i := 0; i < clientColumnCount; i++ {
		if i > 0 {
			sel.WriteString(", ")
		}
		sel.WriteString("json_extract(j.value, '$[" + strconv.Itoa(i) + "]')")
	}
	stmt := "INSERT INTO client_events (" + clientColumns + ")\nSELECT " + sel.String() + "\nFROM json_each(?) AS j"

	// Chunks of about 80 KB keep each statement well inside D1's limits.
	var chunk [][]any
	size := 0
	flush := func() error {
		if len(chunk) == 0 {
			return nil
		}
		payload, err := json.Marshal(chunk)
		if err != nil {
			return err
		}
		chunk, size = chunk[:0], 0
		return s.db.Exec(ctx, stmt, string(payload))
	}
	for _, r := range rows {
		v := r.values()
		chunk = append(chunk, v)
		size += r.approxSize()
		if size >= 80<<10 {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	return flush()
}

func (r clientRow) approxSize() int {
	return 200 + len(r.AppVersion) + len(r.Version) + len(r.SessionID) + len(r.UserID) + len(r.Device) + len(r.OS) +
		len(r.Name) + len(r.URL) + len(r.Message) + len(r.Stack) + len(r.Attrs)
}
