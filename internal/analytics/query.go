package analytics

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/styropyr0/visionary/internal/db"
)

// tsLayout matches api_logs.created_at, so string comparison is time order and
// the created_at index applies.
const tsLayout = "2006-01-02T15:04:05.000Z"

// timeRange is the window a dashboard is looking at, with the time-series
// bucket size chosen for it.
type timeRange struct {
	From   time.Time
	To     time.Time
	Bucket int64 // seconds
}

var bucketSteps = []int64{60, 300, 600, 900, 1800, 3600, 7200, 10800, 21600, 43200, 86400}

// targetPoints keeps time series readable: about 60–120 buckets per chart.
const targetPoints = 90

func parseRange(q url.Values, maxDays int) (timeRange, error) {
	now := time.Now().UTC()
	from, to := now.Add(-24*time.Hour), now
	if v := q.Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return timeRange{}, errors.New("from must be an RFC 3339 timestamp")
		}
		from = t.UTC()
	}
	if v := q.Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return timeRange{}, errors.New("to must be an RFC 3339 timestamp")
		}
		to = t.UTC()
	}
	if !to.After(from) {
		return timeRange{}, errors.New("to must be after from")
	}
	if to.Sub(from) > time.Duration(maxDays)*24*time.Hour {
		return timeRange{}, fmt.Errorf("time range is limited to %d days", maxDays)
	}
	return timeRange{From: from, To: to, Bucket: pickBucket(to.Sub(from))}, nil
}

func pickBucket(span time.Duration) int64 {
	want := int64(span.Seconds()) / targetPoints
	for _, step := range bucketSteps {
		if step >= want {
			return step
		}
	}
	return bucketSteps[len(bucketSteps)-1]
}

// previous is the window of the same length right before r, for deltas.
func (r timeRange) previous() timeRange {
	span := r.To.Sub(r.From)
	return timeRange{From: r.From.Add(-span), To: r.From, Bucket: r.Bucket}
}

// render fills the {{from}}, {{to}}, {{bucket}} and {{t}} placeholders; {{t}}
// is created_at floored to the bucket, in epoch seconds, written for q. Values are
// generated here (timestamps and an integer), never taken from user input, so
// inlining them is safe; quoted placeholders are accepted too because models
// sometimes add the quotes themselves.
func (r timeRange) render(sql string, q sqlDialect) string {
	from := "'" + r.From.Format(tsLayout) + "'"
	to := "'" + r.To.Format(tsLayout) + "'"
	bucket := strconv.FormatInt(r.Bucket, 10)
	return strings.NewReplacer(
		"'{{from}}'", from, "'{{to}}'", to, "'{{bucket}}'", bucket,
		"{{t}}", q.bucketOf(bucket),
		"{{from}}", from, "{{to}}", to, "{{bucket}}", bucket,
	).Replace(sql)
}

// Result is what every data endpoint returns.
type Result struct {
	Columns    []string `json:"columns"`
	Rows       [][]any  `json:"rows"`
	Truncated  bool     `json:"truncated"`
	RowsRead   int64    `json:"rows_read"`
	DurationMs float64  `json:"duration_ms"`
	SQL        string   `json:"sql"`
	Bucket     int64    `json:"bucket"`
	From       string   `json:"from"`
	To         string   `json:"to"`
}

// runTemplate renders sql for r, wraps it in a row cap, and runs it. trusted
// skips validation for the package's own fixed queries.
func (s *Server) runTemplate(ctx context.Context, sql string, r timeRange, trusted bool) (*Result, error) {
	if !trusted {
		checked, err := validateReadOnly(sql)
		if err != nil {
			return nil, &queryError{err}
		}
		sql = checked
	}
	rendered := r.render(sql, s.q)
	// One extra row tells us the result was cut.
	capped := fmt.Sprintf("SELECT * FROM (\n%s\n) AS q LIMIT %d", rendered, s.cfg.MaxRows+1)

	var rows *db.Rows
	var err error
	if trusted {
		rows, err = s.db.Query(ctx, capped)
	} else {
		rows, err = s.db.QueryUntrusted(ctx, capped)
	}
	if err != nil {
		return nil, dbErr(err)
	}
	return newResult(rows, s.cfg.MaxRows, rendered, r), nil
}

func newResult(rows *db.Rows, maxRows int, sql string, r timeRange) *Result {
	res := &Result{
		Columns:    rows.Columns,
		Rows:       rows.Rows,
		RowsRead:   rows.RowsRead,
		DurationMs: rows.DurationMs,
		SQL:        sql,
		Bucket:     r.Bucket,
		From:       r.From.Format(time.RFC3339Nano),
		To:         r.To.Format(time.RFC3339Nano),
	}
	if len(res.Rows) > maxRows {
		res.Rows = res.Rows[:maxRows]
		res.Truncated = true
	}
	if res.Columns == nil {
		res.Columns = []string{}
	}
	return res
}

// queryError marks failures caused by the SQL itself (bad syntax, rejected by
// the guard). They are shown to the user and fed back to the model.
type queryError struct{ err error }

func (e *queryError) Error() string { return e.err.Error() }
func (e *queryError) Unwrap() error { return e.err }

// dbErr treats the database rejecting the statement as a query error the user
// and the model can act on; connection failures stay upstream errors.
func dbErr(err error) error {
	var qe *db.QueryError
	if errors.As(err, &qe) {
		return &queryError{qe.Err}
	}
	return err
}
