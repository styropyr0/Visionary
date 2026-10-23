package analytics

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	sdk "github.com/styropyr0/visionary/sdks/go"
)

// The rollup job aggregates completed buckets. Each (table, grain) keeps one
// contiguous aggregated span [low, high) in rollup_state:
//
//   - every minute the 1m span moves forward to the current minute, and the
//     minutes that just turned lateWindow old are aggregated once more, to
//     pick up rows that arrived late;
//   - 1h rows are built once per hour from final 1m rows (or from the raw
//     table where the 1m rows have expired);
//   - history is backfilled newest first, in small chunks with pauses, until
//     the oldest raw row or the retention limit.
//
// Builds upsert a bucket range and then sweep rows of that range the build
// did not write, so re-running any range is safe.

const (
	lateWindow   = 10 * time.Minute
	tickEvery    = time.Minute
	backfillTime = 40 * time.Second // per tick, so the forward step keeps its pace
)

// spanDone marks a span backfilled down to the oldest raw row: anything
// earlier has no data, so it counts as covered.
const spanDone = "1970-01-01T00:00:00.000Z"

type rollupSpan struct{ low, high string }

func (sp rollupSpan) empty() bool { return sp.high == "" }

// covers reports whether [from, to) was aggregated.
func (sp rollupSpan) covers(from, to time.Time) bool {
	return !sp.empty() && sp.low <= from.Format(tsLayout) && to.Format(tsLayout) <= sp.high
}

type rollupKind struct {
	name  string
	table func(rt *rollupRuntime) rollupTable
}

// minute and hour are how long 1m and 1h rows are kept (Settings →
// Retention); 0 there keeps them, for practical purposes, forever.
func (rt *rollupRuntime) minute() time.Duration {
	return rollupKeep(rt.s.retentionDays("rollup_minute"))
}
func (rt *rollupRuntime) hour() time.Duration { return rollupKeep(rt.s.retentionDays("rollup_hour")) }

func rollupKeep(days int) time.Duration {
	if days <= 0 {
		days = 100 * 365
	}
	return time.Duration(days) * 24 * time.Hour
}

type rollupRuntime struct {
	s       *Server
	minRows time.Duration // shortest range read from rollups, per kind
	d1      bool

	mu       sync.Mutex
	spans    map[string]rollupSpan
	routes   []string
	routesAt time.Time
	sweptAt  time.Time
}

// rollupRuntimes holds the job state of each running server.
var rollupRuntimes sync.Map // *Server -> *rollupRuntime

func (s *Server) rollupRuntime() *rollupRuntime {
	if v, ok := rollupRuntimes.Load(s); ok {
		return v.(*rollupRuntime)
	}
	return nil
}

func (rt *rollupRuntime) kinds() []rollupKind {
	return []rollupKind{
		{"requests", func(rt *rollupRuntime) rollupTable { return requestsTable(rt.s.apdex(), rt.knownRoutes()) }},
		{"system", func(*rollupRuntime) rollupTable { return systemTable() }},
	}
}

func spanName(kind string, grain int64) string { return fmt.Sprintf("%s:%d", kind, grain) }

func (rt *rollupRuntime) span(kind string, grain int64) rollupSpan {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.spans[spanName(kind, grain)]
}

func (rt *rollupRuntime) knownRoutes() []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.routes
}

func (rt *rollupRuntime) setSpan(ctx context.Context, kind string, grain int64, sp rollupSpan) error {
	name := spanName(kind, grain)
	rt.mu.Lock()
	rt.spans[name] = sp
	rt.mu.Unlock()
	q := rt.s.q
	stmt := `INSERT INTO rollup_state (name, low_at, high_at, updated_at) VALUES (?, ?, ?, ?)
ON CONFLICT (name) DO UPDATE SET low_at = excluded.low_at, high_at = excluded.high_at, updated_at = excluded.updated_at`
	if q.d == sdk.MySQL {
		stmt = `INSERT INTO rollup_state (name, low_at, high_at, updated_at) VALUES (?, ?, ?, ?)
ON DUPLICATE KEY UPDATE low_at = VALUES(low_at), high_at = VALUES(high_at), updated_at = VALUES(updated_at)`
	}
	return rt.s.db.Exec(ctx, stmt, name, sp.low, sp.high, time.Now().UTC().Format(tsLayout))
}

func (rt *rollupRuntime) loadSpans(ctx context.Context) error {
	rows, err := rt.s.db.Query(ctx, "SELECT name, low_at, high_at FROM "+rollupStateTable)
	if err != nil {
		return err
	}
	spans := map[string]rollupSpan{}
	for _, r := range rows.Rows {
		if len(r) == 3 {
			spans[toString(r[0])] = rollupSpan{low: toString(r[1]), high: toString(r[2])}
		}
	}
	rt.mu.Lock()
	rt.spans = spans
	rt.mu.Unlock()
	return nil
}

func (s *Server) rollupsStart(ctx context.Context) {
	if !s.confBool("VISIONARY_ROLLUPS") {
		s.logger.Info("rollups disabled in Settings → Server; long ranges read raw rows")
		return
	}
	rt := &rollupRuntime{
		s:     s,
		d1:    strings.Contains(s.db.Name(), "D1"),
		spans: map[string]rollupSpan{},
	}
	rollupRuntimes.Store(s, rt)
	go func() {
		defer rollupRuntimes.Delete(s)
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			started := time.Now()
			rt.tick(ctx)
			timer.Reset(max(tickEvery-time.Since(started), 5*time.Second))
		}
	}()
}

// tick runs one round. A failure only skips the rest of the round: the spans
// record what is done, so the next round resumes there.
func (rt *rollupRuntime) tick(ctx context.Context) {
	defer func() {
		if p := recover(); p != nil {
			rt.s.logger.Error("rollup job panicked", "panic", fmt.Sprint(p))
		}
	}()
	if err := rt.loadSpans(ctx); err != nil {
		if ctx.Err() == nil {
			rt.s.logger.Warn("rollups: read state", "err", err)
		}
		return
	}
	rt.refreshRoutes(ctx)
	now := time.Now().UTC()
	for _, k := range rt.kinds() {
		if err := rt.forward(ctx, k, now); err != nil {
			rt.warn(ctx, "forward", k.name, err)
		}
	}
	rt.retention(ctx, now)
	deadline := time.Now().Add(backfillTime)
	for _, k := range rt.kinds() {
		if err := rt.backfill(ctx, k, now, deadline); err != nil {
			rt.warn(ctx, "backfill", k.name, err)
		}
	}
}

func (rt *rollupRuntime) warn(ctx context.Context, step, kind string, err error) {
	if ctx.Err() != nil || missingTable(err, "system_metrics") || missingTable(err, "api_logs") {
		return
	}
	rt.s.logger.Warn("rollups: "+step, "table", kind, "err", err)
}

// refreshRoutes loads the routes kept by name in rollup_requests: the 200
// busiest over the last week, refreshed daily.
func (rt *rollupRuntime) refreshRoutes(ctx context.Context) {
	rt.mu.Lock()
	fresh := time.Since(rt.routesAt) < 24*time.Hour
	rt.mu.Unlock()
	if fresh {
		return
	}
	since := time.Now().UTC().Add(-7 * 24 * time.Hour).Format(tsLayout)
	rows, err := rt.s.db.Query(ctx, `SELECT COALESCE(route, path) AS r FROM api_logs
WHERE created_at >= ? GROUP BY COALESCE(route, path) ORDER BY COUNT(*) DESC LIMIT 200`, since)
	if err != nil {
		rt.warn(ctx, "load routes", "requests", err)
		return
	}
	routes := make([]string, 0, len(rows.Rows))
	for _, r := range rows.Rows {
		if len(r) == 1 && safeRoute(toString(r[0])) {
			routes = append(routes, toString(r[0]))
		}
	}
	rt.mu.Lock()
	rt.routes, rt.routesAt = routes, time.Now()
	rt.mu.Unlock()
}

// build aggregates [from, to) of grain, from the raw table (fromGrain 0) or
// from rows of fromGrain, then sweeps keys the build no longer produced.
func (rt *rollupRuntime) build(ctx context.Context, t rollupTable, grain, fromGrain int64, from, to time.Time) error {
	if !from.Before(to) {
		return nil
	}
	stamp := time.Now().UTC().Format(tsLayout)
	a, b := from.Format(tsLayout), to.Format(tsLayout)
	if err := rt.s.db.Exec(ctx, rt.s.q.aggregateSQL(t, grain, fromGrain), stamp, a, b); err != nil {
		return err
	}
	return rt.s.db.Exec(ctx, sweepSQL(t, grain), a, b, stamp)
}

func (rt *rollupRuntime) pause(ctx context.Context) {
	d := 200 * time.Millisecond
	if rt.d1 {
		d = time.Second
	}
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// chunk is how much history one build covers. D1 gets small chunks: it bills
// rows read and caps statement time.
func (rt *rollupRuntime) chunk(grain, fromGrain int64) time.Duration {
	switch {
	case grain == 60:
		if rt.d1 {
			return 2 * time.Hour
		}
		return 12 * time.Hour
	case fromGrain == 60:
		if rt.d1 {
			return 24 * time.Hour
		}
		return 7 * 24 * time.Hour
	}
	if rt.d1 {
		return 6 * time.Hour
	}
	return 2 * 24 * time.Hour
}

func parseTS(v string) time.Time {
	t, err := time.Parse(tsLayout, v)
	if err != nil {
		return time.Time{}
	}
	return t
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// forward brings the 1m span up to the current minute and the 1h span up to
// the last hour whose minutes are final.
func (rt *rollupRuntime) forward(ctx context.Context, k rollupKind, now time.Time) error {
	t := k.table(rt)
	end := now.Truncate(time.Minute)
	final := end.Add(-lateWindow)
	cutoff := now.Add(-rt.minute()).Truncate(time.Minute)

	sp := rt.span(k.name, 60)
	old := parseTS(sp.high)
	if sp.empty() || old.Before(cutoff) {
		// First run, or down for longer than 1m rows are kept: start a new
		// span; the hour rollups fill the gap from raw rows.
		old = maxTime(final, cutoff)
		sp = rollupSpan{low: old.Format(tsLayout), high: old.Format(tsLayout)}
	}
	for c := old; c.Before(end); {
		next := minTime(c.Add(rt.chunk(60, 0)), end)
		if err := rt.build(ctx, t, 60, 0, c, next); err != nil {
			return err
		}
		sp.high = next.Format(tsLayout)
		if err := rt.setSpan(ctx, k.name, 60, sp); err != nil {
			return err
		}
		c = next
		if c.Before(end) {
			rt.pause(ctx)
		}
	}
	// Minutes that just became final and were built before (not just now).
	redo := maxTime(old.Add(-lateWindow), parseTS(sp.low))
	if err := rt.build(ctx, t, 60, 0, redo, minTime(final, old)); err != nil {
		return err
	}

	hp := rt.span(k.name, 3600)
	target := final.Truncate(time.Hour)
	start := parseTS(hp.high)
	if hp.empty() {
		start = target
		hp = rollupSpan{low: target.Format(tsLayout), high: target.Format(tsLayout)}
		if err := rt.setSpan(ctx, k.name, 3600, hp); err != nil {
			return err
		}
	}
	for c := start; c.Before(target); {
		next, err := rt.buildHours(ctx, k, t, c, target)
		if err != nil {
			return err
		}
		hp.high = next.Format(tsLayout)
		if err := rt.setSpan(ctx, k.name, 3600, hp); err != nil {
			return err
		}
		c = next
	}
	return nil
}

// buildHours builds one chunk of hours starting at from, no later than
// limit, from final 1m rows when they cover it and from raw rows otherwise.
// It returns where the chunk ended.
func (rt *rollupRuntime) buildHours(ctx context.Context, k rollupKind, t rollupTable, from, limit time.Time) (time.Time, error) {
	minutes := rt.span(k.name, 60)
	finalEnd := parseTS(minutes.high).Add(-lateWindow)
	fromGrain := int64(0)
	to := minTime(from.Add(rt.chunk(3600, 0)), limit)
	if !minutes.empty() && minutes.low <= from.Format(tsLayout) && from.Before(finalEnd) {
		fromGrain = 60
		to = minTime(minTime(from.Add(rt.chunk(3600, 60)), limit), finalEnd.Truncate(time.Hour))
		if !to.After(from) {
			fromGrain, to = 0, minTime(from.Add(time.Hour), limit)
		}
	}
	if err := rt.build(ctx, t, 3600, fromGrain, from, to); err != nil {
		return from, err
	}
	return to, nil
}

// backfill extends spans backwards until the deadline: 1m rows first (the
// cheap source for the hours after them), then 1h rows.
func (rt *rollupRuntime) backfill(ctx context.Context, k rollupKind, now, deadline time.Time) error {
	oldest, err := rt.oldestRaw(ctx, k)
	if err != nil {
		return err
	}
	t := k.table(rt)
	for _, grain := range []int64{60, 3600} {
		unit, keep := time.Minute, rt.minute()
		if grain == 3600 {
			unit, keep = time.Hour, rt.hour()
		}
		sp := rt.span(k.name, grain)
		if sp.empty() || sp.low == spanDone {
			continue
		}
		if oldest.IsZero() {
			// No raw rows at all: nothing to backfill.
			sp.low = spanDone
			if err := rt.setSpan(ctx, k.name, grain, sp); err != nil {
				return err
			}
			continue
		}
		limit, done := now.Add(-keep).Truncate(unit), false
		if o := oldest.Truncate(unit); !o.Before(limit) {
			limit, done = o, true
		}
		low := parseTS(sp.low)
		for low.After(limit) {
			if time.Now().After(deadline) || ctx.Err() != nil {
				return nil
			}
			var from time.Time
			if grain == 60 {
				from = maxTime(low.Add(-rt.chunk(60, 0)), limit)
				err = rt.build(ctx, t, 60, 0, from, low)
			} else {
				from, err = rt.buildHoursBack(ctx, k, t, low, limit)
			}
			if err != nil {
				return err
			}
			low = from
			sp.low = low.Format(tsLayout)
			if err := rt.setSpan(ctx, k.name, grain, sp); err != nil {
				return err
			}
			rt.pause(ctx)
		}
		if done && !low.After(limit) {
			sp.low = spanDone
			if err := rt.setSpan(ctx, k.name, grain, sp); err != nil {
				return err
			}
		}
	}
	return nil
}

// buildHoursBack builds the chunk of hours ending at to, no earlier than
// limit, and returns where it starts.
func (rt *rollupRuntime) buildHoursBack(ctx context.Context, k rollupKind, t rollupTable, to, limit time.Time) (time.Time, error) {
	minutes := rt.span(k.name, 60)
	if !minutes.empty() && minutes.low <= to.Add(-time.Hour).Format(tsLayout) {
		from := maxTime(maxTime(to.Add(-rt.chunk(3600, 60)), limit), parseTS(minutes.low).Add(time.Hour-1).Truncate(time.Hour))
		if minutes.low == spanDone {
			from = maxTime(to.Add(-rt.chunk(3600, 60)), limit)
		}
		if from.Before(to) {
			return from, rt.build(ctx, t, 3600, 60, from, to)
		}
	}
	from := maxTime(to.Add(-rt.chunk(3600, 0)), limit)
	return from, rt.build(ctx, t, 3600, 0, from, to)
}

func (rt *rollupRuntime) oldestRaw(ctx context.Context, k rollupKind) (time.Time, error) {
	source := "api_logs"
	if k.name == "system" {
		source = "system_metrics"
	}
	rows, err := rt.s.db.Query(ctx, "SELECT MIN(created_at) FROM "+source)
	if err != nil {
		return time.Time{}, err
	}
	if len(rows.Rows) == 1 && len(rows.Rows[0]) == 1 {
		return parseTS(toString(rows.Rows[0][0])), nil
	}
	return time.Time{}, nil
}

// retention drops expired rollup rows, hourly.
func (rt *rollupRuntime) retention(ctx context.Context, now time.Time) {
	if time.Since(rt.sweptAt) < time.Hour {
		return
	}
	for _, k := range rt.kinds() {
		table := "rollup_requests"
		if k.name == "system" {
			table = "rollup_system"
		}
		for _, g := range []struct {
			grain int64
			keep  time.Duration
			unit  time.Duration
		}{{60, rt.minute(), time.Minute}, {3600, rt.hour(), time.Hour}} {
			cutoff := now.Add(-g.keep).Truncate(g.unit).Format(tsLayout)
			sp := rt.span(k.name, g.grain)
			if !sp.empty() && sp.low < cutoff {
				sp.low = cutoff
				if err := rt.setSpan(ctx, k.name, g.grain, sp); err != nil {
					rt.warn(ctx, "retention", k.name, err)
					return
				}
			}
			if err := rt.s.db.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE bucket_s = %d AND created_at < ?", table, g.grain), cutoff); err != nil {
				rt.warn(ctx, "retention", k.name, err)
				return
			}
		}
	}
	rt.sweptAt = time.Now()
}
