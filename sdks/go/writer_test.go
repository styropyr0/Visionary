package visionary

import (
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

type d1FakeStore struct{ fakeStore }

func (f *d1FakeStore) stringParams() bool { return true }

func TestWriterMultiRowInserts(t *testing.T) {
	st := &fakeStore{}
	w := newWriter(RequestsConfig{}, true, identity{service: "api", instance: "i"}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if w.cfg.BatchSize != defaultSQLBatch {
		t.Fatal("batch size", w.cfg.BatchSize)
	}
	var batch []any
	for range 1200 {
		batch = append(batch, &logRecord{at: time.Now(), sev: sevInfo, message: "hi"})
	}
	batch = append(batch, &spanRecord{kind: KindDB, name: "SELECT 1", start: time.Now()})
	w.write(batch)

	rows := 0
	for _, s := range st.written {
		if len(s.args) != s.rows*len(columnsOf(s.sql)) {
			t.Fatalf("%d args for %d rows of %s", len(s.args), s.rows, s.sql[:30])
		}
		if len(s.args) > maxParamsPerStatement {
			t.Fatal("too many parameters:", len(s.args))
		}
		rows += s.rows
	}
	// 21 log columns fit 1428 rows a statement: one for the logs, one for the span.
	if len(st.written) != 2 || rows != 1201 || w.written.Load() != 1201 {
		t.Fatalf("%d statements, %d rows, %d counted", len(st.written), rows, w.written.Load())
	}
}

func TestWriterD1OneRowPerStatement(t *testing.T) {
	st := &d1FakeStore{}
	w := newWriter(RequestsConfig{BatchSize: 500}, true, identity{instance: "i"}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if w.cfg.BatchSize != maxStatementsPerCall || w.flushers() != 1 {
		t.Fatal("D1 batch", w.cfg.BatchSize, w.flushers())
	}
	w.write([]any{&logRecord{at: time.Now(), message: "a"}, &logRecord{at: time.Now(), message: "b"}})
	if len(st.written) != 2 || !strings.Contains(st.written[0].sql, "NULLIF(?, '')") {
		t.Fatalf("%+v", st.written)
	}
}

func columnsOf(sql string) []string {
	open, close := strings.Index(sql, "("), strings.Index(sql, ")")
	return strings.Split(sql[open+1:close], ", ")
}
