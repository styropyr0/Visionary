package visionary

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestSystemColumnsMatchSchema(t *testing.T) {
	for _, d := range []Dialect{SQLite, Postgres, MySQL} {
		ddl := d.SystemSchema()
		for _, c := range systemColumns {
			if !strings.Contains(ddl, "\n    "+c+" ") {
				t.Errorf("%v schema has no %s", d, c)
			}
		}
	}
}

func TestSystemRowsSplitOnD1(t *testing.T) {
	st := &d1FakeStore{}
	r := newRecorder(SystemConfig{Interval: 15e9}, identity{instance: "i"}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	row := make([]any, len(systemColumns))
	for i := range row {
		row[i] = float64(i)
	}
	r.buf = [][]any{row, row}
	r.flush(context.Background())
	if len(systemColumns) <= d1MaxParams {
		if len(st.written) != 2 {
			t.Fatalf("statements: %d", len(st.written))
		}
		return
	}
	if len(st.written) != 4 {
		t.Fatalf("statements: %d", len(st.written))
	}
	ins, upd := st.written[0], st.written[1]
	if len(ins.args) != d1MaxParams || strings.Count(ins.sql, "?") != d1MaxParams || !strings.HasPrefix(ins.sql, "INSERT INTO system_metrics") {
		t.Errorf("insert: %d args, %s", len(ins.args), ins.sql)
	}
	if len(upd.args) != len(systemColumns)-d1MaxParams || !strings.HasSuffix(upd.sql, "WHERE id = last_insert_rowid()") || upd.args[0] != float64(d1MaxParams) {
		t.Errorf("update: %d args, %s", len(upd.args), upd.sql)
	}

	sql := &fakeStore{}
	r = newRecorder(SystemConfig{Interval: 15e9}, identity{instance: "i"}, sql, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.buf = [][]any{row}
	r.flush(context.Background())
	if len(sql.written) != 1 || len(sql.written[0].args) != len(systemColumns) {
		t.Errorf("SQL insert: %+v", sql.written)
	}
}

func TestHostColumnsAreSystemColumns(t *testing.T) {
	all := map[string]bool{}
	for _, c := range systemColumns {
		all[c] = true
	}
	for _, c := range HostColumns {
		if !all[c] {
			t.Errorf("host column %s is not a system_metrics column", c)
		}
		if strings.HasPrefix(c, "container_") {
			t.Errorf("host column %s describes a container", c)
		}
	}
}
