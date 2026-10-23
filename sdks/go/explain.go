package visionary

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Query plans on demand. Visionary never connects to the application's
// database: when someone asks for a statement's plan on the dashboard, the
// server queues it in explain_requests, and an Agent given an Explainer for
// that database claims it, runs EXPLAIN (never EXPLAIN ANALYZE: the statement
// is planned, not executed) and writes the plan back.
//
//	cfg.Explain = []*visionary.Explainer{visionary.PgxExplainer(pool)}
//
// Statements are stored with their values replaced by ?, so the plan is the
// generic one (PostgreSQL 16+ plans without values; older versions and MySQL
// plan with NULLs, which the plan says). VISIONARY_EXPLAIN=false turns it off.

// Explainer plans statements on one database. Target must match the target
// its spans carry ("postgresql/shop"), which is how requests find it.
type Explainer struct {
	Target string
	run    func(ctx context.Context, statement string) (string, error)
}

// PgxExplainer plans statements on a pgx pool. Its target is
// "postgresql/<database>", as PgxTracer names it.
func PgxExplainer(pool *pgxpool.Pool) *Explainer {
	target := "postgresql"
	if db := pool.Config().ConnConfig.Database; db != "" {
		target += "/" + db
	}
	return &Explainer{Target: target, run: func(ctx context.Context, statement string) (string, error) {
		c, err := pool.Acquire(ctx)
		if err != nil {
			return "", err
		}
		defer c.Release()
		// The raw protocol: pgx would otherwise try to bind $1 itself.
		exec := func(q string) ([]string, error) {
			results, err := c.Conn().PgConn().Exec(ctx, q).ReadAll()
			if err != nil {
				return nil, err
			}
			var lines []string
			for _, r := range results {
				for _, row := range r.Rows {
					if len(row) > 0 {
						lines = append(lines, string(row[0]))
					}
				}
			}
			return lines, nil
		}
		return explainPostgres(statement, exec)
	}}
}

// SQLExplainer plans statements on a database/sql pool. target is the one
// given to OpenDB or WrapDriver.
func SQLExplainer(db *sql.DB, d Dialect, target string) *Explainer {
	return &Explainer{Target: target, run: func(ctx context.Context, statement string) (string, error) {
		switch d.normalize() {
		case Postgres:
			return explainPostgres(statement, func(q string) ([]string, error) { return queryLines(ctx, db, q) })
		case MySQL:
			stmt, err := explainable(statement)
			if err != nil {
				return "", err
			}
			out, err := queryTable(ctx, db, "EXPLAIN "+withNulls(stmt))
			if err != nil {
				return "", err
			}
			return nullNote + out, nil
		default:
			stmt, err := explainable(statement)
			if err != nil {
				return "", err
			}
			return explainSQLite(ctx, db, stmt)
		}
	}}
}

const (
	nullNote   = "-- Planned with values set to NULL, so it is approximate.\n"
	numberNote = "-- Planned with values used in arithmetic set to 1, so it is approximate.\n"
)

var (
	firstWord    = regexp.MustCompile(`^\s*\(*\s*([A-Za-z]+)`)
	dollarParam  = regexp.MustCompile(`\$(\d+)`)
	typedLiteral = regexp.MustCompile(`(?i)\b(INTERVAL|DATE|TIMESTAMPTZ|TIMESTAMP|TIME)\s+\?`)
	// A removed value is a number when arithmetic, a CASE branch, LIMIT,
	// BETWEEN or a numeric function's first argument surrounds it.
	numberBefore = regexp.MustCompile(`(?i)(?:[-+*/%]|\b(?:THEN|ELSE|LIMIT|OFFSET|BETWEEN)|\b(?:chr|least|greatest|round|power|mod|abs)\(|\bBETWEEN\s+\S+\s+AND)\s*$`)
	numberAfter  = regexp.MustCompile(`^\s*[-+*/%]`)
)

// typedConstants stand in for a typed literal's removed value: INTERVAL '30
// days' is stored as INTERVAL ?, and a parameter cannot go there.
var typedConstants = map[string]string{
	"INTERVAL": "INTERVAL '1 day'", "DATE": "DATE '2000-01-01'", "TIMESTAMP": "TIMESTAMP '2000-01-01'",
	"TIMESTAMPTZ": "TIMESTAMPTZ '2000-01-01'", "TIME": "TIME '00:00'",
}

// explainable accepts one statement that EXPLAIN can plan.
func explainable(statement string) (string, error) {
	s := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(statement), ";"))
	if s == "" || strings.Contains(s, ";") {
		return "", errors.New("one statement only")
	}
	m := firstWord.FindStringSubmatch(s)
	if m == nil {
		return "", errors.New("not a statement")
	}
	switch strings.ToUpper(m[1]) {
	case "SELECT", "WITH", "INSERT", "UPDATE", "DELETE", "VALUES", "TABLE":
		return typedLiteral.ReplaceAllStringFunc(s, func(lit string) string {
			return typedConstants[strings.ToUpper(typedLiteral.FindStringSubmatch(lit)[1])]
		}), nil
	}
	return "", fmt.Errorf("%s statements have no plan", strings.ToUpper(m[1]))
}

// numbered turns each ? (a value the SDK removed) into the next free $n, so
// PostgreSQL sees parameters it can plan around.
func numbered(s string) string {
	next := 0
	for _, m := range dollarParam.FindAllStringSubmatch(s, -1) {
		if n, _ := strconv.Atoi(m[1]); n > next {
			next = n
		}
	}
	var b strings.Builder
	for _, r := range s {
		if r == '?' {
			next++
			b.WriteString("$" + strconv.Itoa(next))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// numbersGuessed is numbered, except that a ? that can only be a number
// becomes 1: PostgreSQL cannot choose an operator for -$1 or $1 * x when
// nothing says $1 is a number. 1, not 0, so chr(?) and x / ? stay valid.
func numbersGuessed(s string) string {
	parts := strings.Split(s, "?")
	var b strings.Builder
	for i, p := range parts[:len(parts)-1] {
		b.WriteString(p)
		if numberBefore.MatchString(b.String()) || numberAfter.MatchString(parts[i+1]) {
			b.WriteString("1")
		} else {
			b.WriteString("?")
		}
	}
	b.WriteString(parts[len(parts)-1])
	return numbered(b.String())
}

func withNulls(s string) string {
	return dollarParam.ReplaceAllString(strings.ReplaceAll(s, "?", "NULL"), "NULL")
}

// explainPostgres tries, in order: a generic plan with every removed value as
// a parameter; a generic plan keeping the application's own $n (which have
// real types) with removed values as NULL, for when a parameter's type cannot
// be guessed (GREATEST(?, 0.5)); the generic plan again with values that can
// only be numbers set to 1, for arithmetic on them (-?, ? * x); and, before
// PostgreSQL 16, a plan with NULLs.
func explainPostgres(statement string, exec func(string) ([]string, error)) (string, error) {
	stmt, err := explainable(statement)
	if err != nil {
		return "", err
	}
	lines, first := exec("EXPLAIN (GENERIC_PLAN) " + numbered(stmt))
	if first == nil {
		return strings.Join(lines, "\n"), nil
	}
	for _, try := range []struct{ note, q string }{
		{nullNote, "EXPLAIN (GENERIC_PLAN) " + strings.ReplaceAll(stmt, "?", "NULL")},
		{numberNote, "EXPLAIN (GENERIC_PLAN) " + numbersGuessed(stmt)},
		{nullNote, "EXPLAIN " + withNulls(stmt)},
	} {
		if lines, err := exec(try.q); err == nil {
			return try.note + strings.Join(lines, "\n"), nil
		}
	}
	return "", planError(first)
}

// planError says plainly why a stored statement could not be planned.
func planError(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "end of input"):
		return fmt.Errorf("the stored statement looks cut short (spans keep up to %d characters; older SDK versions kept 2000), so it cannot be planned", maxDetail)
	case strings.Contains(msg, "cannot be matched"), strings.Contains(msg, "could not determine"), strings.Contains(msg, "operator does not exist"), strings.Contains(msg, "is not unique"):
		return fmt.Errorf("the statement's values were removed when it was stored, and the database cannot tell their types here, so it cannot be planned (%s)", msg)
	}
	return err
}

func queryLines(ctx context.Context, db *sql.DB, q string) ([]string, error) {
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return nil, err
		}
		out = append(out, line)
	}
	return out, rows.Err()
}

// queryTable renders a result as "column: value" blocks, one per row
// (MySQL's EXPLAIN is a table).
func queryTable(ctx context.Context, db *sql.DB, q string) (string, error) {
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for rows.Next() {
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return "", err
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		for i, c := range cols {
			v := "NULL"
			if vals[i].Valid {
				v = vals[i].String
			}
			fmt.Fprintf(&b, "%s: %s\n", c, v)
		}
	}
	return b.String(), rows.Err()
}

// explainSQLite renders EXPLAIN QUERY PLAN as an indented tree. Unbound
// parameters are fine in SQLite.
func explainSQLite(ctx context.Context, db *sql.DB, stmt string) (string, error) {
	rows, err := db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+stmt)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	depth := map[int64]int{}
	var b strings.Builder
	for rows.Next() {
		var id, parent, unused int64
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			return "", err
		}
		d := 0
		if parent != 0 {
			d = depth[parent] + 1
		}
		depth[id] = d
		b.WriteString(strings.Repeat("  ", d) + detail + "\n")
	}
	return b.String(), rows.Err()
}

const explainEvery = 10 * time.Second

// explainLoop claims queued plan requests for the databases this Agent has an
// Explainer for, until ctx ends.
func (a *Agent) explainLoop(ctx context.Context) {
	byTarget := map[string]*Explainer{}
	for _, e := range a.cfg.Explain {
		if e != nil {
			byTarget[e.Target] = e
		}
	}
	if len(byTarget) == 0 {
		return
	}
	d := a.db.dialect()
	t := time.NewTicker(explainEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		qctx, cancel := context.WithTimeout(quiet(ctx), 5*time.Second)
		pending, err := a.db.rows(qctx, d.Rebind(`SELECT id, target, statement FROM explain_requests WHERE status = 'pending' ORDER BY id LIMIT 5`))
		cancel()
		if err != nil {
			continue // the table appears once the Visionary server has started
		}
		for _, p := range pending {
			if len(p) < 3 {
				continue
			}
			e := byTarget[p[1]]
			id, perr := strconv.ParseInt(p[0], 10, 64)
			if e == nil || perr != nil {
				continue
			}
			a.explainOne(ctx, d, e, id, p[2])
		}
	}
}

func (a *Agent) explainOne(ctx context.Context, d Dialect, e *Explainer, id int64, statement string) {
	ctx = quiet(ctx)
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	n, err := a.db.execChanges(cctx, d.Rebind(`UPDATE explain_requests SET status = 'running', claimed_by = ? WHERE id = ? AND status = 'pending'`), a.ident.instance, id)
	cancel()
	if err != nil || n != 1 {
		return // another instance took it
	}
	rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	plan, err := e.run(rctx, statement)
	cancel()
	status, msg := "done", ""
	if err != nil {
		status, msg, plan = "failed", truncateRunes(err.Error(), 500), ""
	}
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := a.db.execChanges(wctx, d.Rebind(`UPDATE explain_requests SET status = ?, plan = ?, error = ?, finished_at = ? WHERE id = ?`),
		status, plan, msg, time.Now().UTC().Format(tsLayout), id); err != nil {
		a.cfg.Logger.Warn("visionary: writing a query plan failed", "err", err)
	}
}
