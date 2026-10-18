package analytics

import (
	"context"
	"strconv"
	"strings"

	"github.com/styropyr0/visionary/sdks/go/d1"
)

// Rows built from an export are written as multi-row INSERTs per table: on
// D1 as one batch call (100 params per statement, 50 statements per call),
// elsewhere as statements of up to otlpSQLMaxParams typed params.

const (
	otlpD1MaxParams  = 100
	otlpD1MaxBatch   = 50
	otlpSQLMaxParams = 900
)

type otlpTable struct {
	name string
	cols []string
}

var (
	otlpRequestsTable = &otlpTable{"api_logs", []string{
		"request_id", "created_at", "method", "path", "route", "query", "status", "duration_ms", "ip", "user_agent",
		"user_id", "user_handle", "user_role", "request_headers", "request_content_type", "request_body",
		"request_body_truncated", "request_size", "response_content_type", "response_body",
		"response_body_truncated", "response_size", "error_code", "error_key",
		"service", "version", "db_count", "db_ms", "http_count", "http_ms",
	}}
	otlpSpansTable = &otlpTable{"spans", []string{
		"created_at", "request_id", "service", "instance", "kind", "name", "target", "detail",
		"offset_ms", "duration_ms", "status", "error", "row_count",
	}}
	otlpErrorsTable = &otlpTable{"errors", []string{
		"created_at", "service", "instance", "version", "request_id", "method", "route", "path", "user_id",
		"type", "message", "stack", "fingerprint", "handled", "runtime",
	}}
	otlpReleasesTable = &otlpTable{"releases", []string{
		"created_at", "service", "instance", "version", "sdk", "sdk_version", "runtime",
	}}
)

// otlpRows collects rows per table; each row matches its table's columns.
type otlpRows struct {
	order  []*otlpTable
	tables map[*otlpTable][][]any
	// releases are candidate releases rows by service and version, added
	// by store when the database does not have them yet.
	releases map[string][]any
}

func (b *otlpRows) add(t *otlpTable, row []any) {
	if b.tables == nil {
		b.tables = map[*otlpTable][][]any{}
	}
	if _, ok := b.tables[t]; !ok {
		b.order = append(b.order, t)
	}
	b.tables[t] = append(b.tables[t], row)
}

func (b *otlpRows) empty() bool { return len(b.order) == 0 && len(b.releases) == 0 }

// write stores the rows: on D1 in as few batch calls as the limits allow,
// elsewhere in one transaction.
func (o *otlpState) write(ctx context.Context, b *otlpRows) error {
	if b.empty() {
		return nil
	}
	if o.d1 != nil {
		var stmts []d1.Statement
		for _, t := range b.order {
			for _, st := range otlpInserts(t, b.tables[t], otlpD1MaxParams, true) {
				stmts = append(stmts, st.Statement)
			}
		}
		for len(stmts) > 0 {
			n := min(len(stmts), otlpD1MaxBatch)
			if err := o.d1.Batch(ctx, stmts[:n]); err != nil {
				return err
			}
			stmts = stmts[n:]
		}
		return nil
	}
	if o.sql == nil {
		// No pool of our own: no transaction either.
		for _, t := range b.order {
			for _, st := range otlpInserts(t, b.tables[t], otlpSQLMaxParams, false) {
				if err := o.s.db.Exec(ctx, st.SQL, st.args...); err != nil {
					return err
				}
			}
		}
		return nil
	}
	// One transaction, so an export the exporter retries is never half
	// stored.
	tx, err := o.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	d := o.s.db.Dialect()
	for _, t := range b.order {
		for _, st := range otlpInserts(t, b.tables[t], otlpSQLMaxParams, false) {
			if _, err := tx.ExecContext(ctx, d.Rebind(st.SQL), st.args...); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// otlpStmt is one INSERT: string params for D1, typed args elsewhere.
type otlpStmt struct {
	d1.Statement
	args []any
}

// otlpInserts chunks rows into multi-row INSERTs of at most maxParams params.
// With stringParams (D1) every placeholder is NULLIF with an empty string and every value a
// string, as the SDK contract describes.
func otlpInserts(t *otlpTable, rows [][]any, maxParams int, stringParams bool) []otlpStmt {
	ph := "?"
	if stringParams {
		ph = "NULLIF(?, '')"
	}
	one := "(" + strings.TrimSuffix(strings.Repeat(ph+", ", len(t.cols)), ", ") + ")"
	head := "INSERT INTO " + t.name + " (" + strings.Join(t.cols, ", ") + ") VALUES "
	per := max(1, maxParams/len(t.cols))
	var out []otlpStmt
	for len(rows) > 0 {
		n := min(per, len(rows))
		st := otlpStmt{}
		st.SQL = head + strings.TrimSuffix(strings.Repeat(one+", ", n), ", ")
		for _, row := range rows[:n] {
			if stringParams {
				for _, v := range row {
					st.Params = append(st.Params, otlpParam(v))
				}
			} else {
				st.args = append(st.args, row...)
			}
		}
		out = append(out, st)
		rows = rows[n:]
	}
	return out
}

func otlpParam(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	}
	return ""
}
