package analytics

import (
	"context"
	"sort"
	"strconv"
	"sync"
	"time"

	sdk "github.com/styropyr0/visionary/sdks/go"
)

// Several servers (replicas) can run on one database behind a load balancer.
// Work that must happen once, such as alert checks, deliveries, digests,
// uptime probes, rollups and retention, runs under a lease in
// visionary_leases: one replica holds each lease, renews it every 10 seconds
// and stops that work as soon as it can't. A lease expires 30 seconds after
// its last renewal by the database's clock, so a replica that dies hands its
// work over within 40 seconds, and one that shuts down hands it
// over at once. Leases spread across replicas: one holding more than its
// share gives one up now and then for another to take.

const (
	leaseTTL   = 30 * time.Second
	leaseRenew = 10 * time.Second
)

type leaseJob struct {
	name string
	run  func(context.Context)
	// cancel is set while run is running; stopping once it has been asked to end.
	cancel   context.CancelFunc
	stopping bool
	shed     bool
	since    time.Time
}

type leaseState struct {
	mu      sync.Mutex
	jobs    []*leaseJob
	wg      sync.WaitGroup
	okAt    time.Time // when the last renewal that worked was sent
	live    int
	shedAt  time.Time
	stopped chan struct{}
}

var leaseStates sync.Map // *Server -> *leaseState

func (s *Server) leases() *leaseState {
	v, _ := leaseStates.LoadOrStore(s, &leaseState{})
	return v.(*leaseState)
}

// leased runs fn on whichever replica holds the lease name, from the first
// lease round in Start. fn must return when its context ends.
func (s *Server) leased(name string, fn func(context.Context)) {
	ls := s.leases()
	ls.mu.Lock()
	ls.jobs = append(ls.jobs, &leaseJob{name: name, run: fn})
	ls.mu.Unlock()
}

// dbNowMs is the database's clock in epoch milliseconds, so replicas whose
// clocks disagree still agree on when a lease ends.
func (q sqlDialect) dbNowMs() string {
	switch q.d {
	case sdk.Postgres:
		return "CAST(EXTRACT(EPOCH FROM clock_timestamp()) * 1000 AS BIGINT)"
	case sdk.MySQL:
		return "CAST(UNIX_TIMESTAMP(NOW(3)) * 1000 AS SIGNED)"
	}
	return "CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER)"
}

func (q sqlDialect) leasesSchema() []string {
	key, text := "TEXT", "TEXT"
	if q.d == sdk.MySQL {
		key, text = "VARCHAR(191)", "VARCHAR(191)"
	}
	return []string{
		`CREATE TABLE IF NOT EXISTS visionary_leases (
    name        ` + key + ` NOT NULL PRIMARY KEY,
    holder      ` + text + ` NOT NULL,
    acquired_ms BIGINT NOT NULL,
    expires_ms  BIGINT NOT NULL
)`,
		`CREATE TABLE IF NOT EXISTS visionary_replicas (
    id           ` + key + ` NOT NULL PRIMARY KEY,
    version      ` + text + ` NOT NULL,
    addr         ` + text + ` NOT NULL,
    started_at   ` + text + ` NOT NULL,
    heartbeat_ms BIGINT NOT NULL
)`,
		`CREATE TABLE IF NOT EXISTS visionary_changes (
    name       ` + key + ` NOT NULL PRIMARY KEY,
    changed_ms BIGINT NOT NULL
)`,
		`CREATE TABLE IF NOT EXISTS visionary_once (
    name       ` + key + ` NOT NULL PRIMARY KEY,
    holder     ` + text + ` NOT NULL,
    expires_ms BIGINT NOT NULL
)`,
	}
}

// insertIgnore adds a row unless its key is taken.
func (q sqlDialect) insertIgnore(table, cols, values string) string {
	if q.d == sdk.MySQL {
		return `INSERT IGNORE INTO ` + table + ` (` + cols + `) VALUES (` + values + `)`
	}
	return `INSERT INTO ` + table + ` (` + cols + `) VALUES (` + values + `) ON CONFLICT DO NOTHING`
}

// upsertRow adds a row or overwrites the named columns of the one with its key.
func (q sqlDialect) upsertRow(table, key, cols, values string, update []string) string {
	stmt := `INSERT INTO ` + table + ` (` + cols + `) VALUES (` + values + `)`
	set := ""
	for i, c := range update {
		if i > 0 {
			set += ", "
		}
		if q.d == sdk.MySQL {
			set += c + " = VALUES(" + c + ")"
		} else {
			set += c + " = excluded." + c
		}
	}
	if q.d == sdk.MySQL {
		return stmt + ` ON DUPLICATE KEY UPDATE ` + set
	}
	return stmt + ` ON CONFLICT (` + key + `) DO UPDATE SET ` + set
}

type leaseRow struct {
	holder              string
	acquired, expiresMs int64
}

func (s *Server) readLeases(ctx context.Context) (map[string]leaseRow, error) {
	res, err := s.db.Query(ctx, `SELECT name, holder, acquired_ms, expires_ms FROM visionary_leases`)
	if err != nil {
		return nil, err
	}
	out := map[string]leaseRow{}
	for _, r := range res.Rows {
		if len(r) == 4 {
			out[toString(r[0])] = leaseRow{holder: toString(r[1]), acquired: toInt(r[2]), expiresMs: toInt(r[3])}
		}
	}
	return out, nil
}

// tryLease takes name if nobody holds it. Whether it worked shows in the
// next read: the update is atomic, so of replicas trying together one wins.
func (s *Server) tryLease(ctx context.Context, name string, exists bool) error {
	now := s.q.dbNowMs()
	if !exists {
		if err := s.db.Exec(ctx, s.q.insertIgnore("visionary_leases", "name, holder, acquired_ms, expires_ms", "?, '', 0, 0"), name); err != nil {
			return err
		}
	}
	// holder is set last: MySQL evaluates SET left to right with new values.
	return s.db.Exec(ctx, `UPDATE visionary_leases SET acquired_ms = `+now+`, expires_ms = `+now+` + `+ms(leaseTTL)+`, holder = ?
WHERE name = ? AND (holder = '' OR expires_ms < `+now+`)`, s.replicaID(), name)
}

func (s *Server) renewLeases(ctx context.Context) error {
	return s.db.Exec(ctx, `UPDATE visionary_leases SET expires_ms = `+s.q.dbNowMs()+` + `+ms(leaseTTL)+` WHERE holder = ?`, s.replicaID())
}

// releaseLease hands name over; expires_ms keeps when, so the replica giving
// it up can leave it to others for a while.
func (s *Server) releaseLease(ctx context.Context, name string) error {
	return s.db.Exec(ctx, `UPDATE visionary_leases SET expires_ms = `+s.q.dbNowMs()+`, holder = '' WHERE name = ? AND holder = ?`, name, s.replicaID())
}

func ms(d time.Duration) string { return strconv.FormatInt(d.Milliseconds(), 10) }

// leaseRound renews what this replica holds, takes free leases up to its
// share, gives one up when it holds too many, and starts or stops the jobs
// to match.
func (s *Server) leaseRound(ctx context.Context) {
	ls := s.leases()
	sent := time.Now()
	nowMs, live, hbErr := s.heartbeat(ctx)
	renewErr := s.renewLeases(ctx)
	rows, err := s.readLeases(ctx)
	if err == nil && hbErr == nil && renewErr == nil {
		ls.mu.Lock()
		ls.okAt = sent
		ls.live = live
		ls.mu.Unlock()
	}
	if err != nil || hbErr != nil || renewErr != nil {
		ls.mu.Lock()
		lost := time.Since(ls.okAt) > leaseTTL-leaseRenew
		ls.mu.Unlock()
		if lost {
			// Others may take the leases before this replica can tell.
			s.stopJobs(func(*leaseJob) bool { return true })
		}
		if ctx.Err() == nil && !missingTable(firstErr(err, hbErr, renewErr), "visionary_") {
			s.logger.Warn("leases: renew failed", "err", firstErr(err, hbErr, renewErr))
		}
		return
	}

	id := s.replicaID()
	ls.mu.Lock()
	jobs := append([]*leaseJob(nil), ls.jobs...)
	ls.mu.Unlock()
	fair := (len(jobs) + max(live, 1) - 1) / max(live, 1)
	heldBy := func(r leaseRow) bool { return r.holder == id && r.expiresMs > nowMs }
	held := 0
	for _, j := range jobs {
		if heldBy(rows[j.name]) {
			held++
		}
	}
	tried := false
	for _, j := range jobs {
		r, exists := rows[j.name]
		if heldBy(r) || (r.holder != "" && r.expiresMs > nowMs) {
			continue
		}
		// A lease another replica just gave up is left to those below their share.
		if held >= fair && exists && nowMs-r.expiresMs < 2*leaseRenew.Milliseconds() {
			continue
		}
		if err := s.tryLease(ctx, j.name, exists); err != nil {
			s.logger.Warn("leases: take", "lease", j.name, "err", err)
			continue
		}
		tried = true
		held++
	}
	if tried {
		if rows, err = s.readLeases(ctx); err != nil {
			return
		}
	}

	s.stopJobs(func(j *leaseJob) bool { return !heldBy(rows[j.name]) })
	ls.mu.Lock()
	defer ls.mu.Unlock()
	var mine []*leaseJob
	for _, j := range ls.jobs {
		if !heldBy(rows[j.name]) {
			continue
		}
		mine = append(mine, j)
		if j.cancel == nil && !j.shed && ctx.Err() == nil {
			s.startJob(ctx, j)
		}
	}
	if live > 1 && len(mine) > fair && time.Since(ls.shedAt) > 2*leaseRenew {
		// The most recently started job gives way, so long runs aren't cut.
		sort.Slice(mine, func(a, b int) bool { return mine[a].since.After(mine[b].since) })
		for _, j := range mine {
			if j.cancel != nil && !j.stopping {
				ls.shedAt = time.Now()
				j.shed, j.stopping = true, true
				j.cancel()
				s.logger.Info("leases: handing over to balance replicas", "lease", j.name)
				break
			}
		}
	}
}

// startJob runs j until its context ends; ls.mu is held.
func (s *Server) startJob(ctx context.Context, j *leaseJob) {
	ls := s.leases()
	jctx, cancel := context.WithCancel(ctx)
	j.cancel, j.stopping, j.since = cancel, false, time.Now()
	s.logger.Info("leases: running", "lease", j.name)
	ls.wg.Add(1)
	go func() {
		defer ls.wg.Done()
		defer cancel()
		j.run(jctx)
		ls.mu.Lock()
		shed := j.shed
		j.cancel, j.stopping = nil, false
		ls.mu.Unlock()
		if shed && ctx.Err() == nil {
			rctx, rcancel := context.WithTimeout(ctx, 10*time.Second)
			if err := s.releaseLease(rctx, j.name); err != nil {
				s.logger.Warn("leases: hand over", "lease", j.name, "err", err)
			}
			rcancel()
			ls.mu.Lock()
			j.shed = false
			ls.mu.Unlock()
		}
	}()
}

// stopJobs asks the running jobs that match to end, without waiting.
func (s *Server) stopJobs(match func(*leaseJob) bool) {
	ls := s.leases()
	ls.mu.Lock()
	defer ls.mu.Unlock()
	for _, j := range ls.jobs {
		if j.cancel != nil && !j.stopping && match(j) {
			j.stopping = true
			j.cancel()
			s.logger.Info("leases: lost; stopping", "lease", j.name)
		}
	}
}

// leasesStart takes the first leases before returning, so a lone replica
// starts its jobs at once, then keeps them renewed until ctx ends.
func (s *Server) leasesStart(ctx context.Context) {
	ls := s.leases()
	ls.mu.Lock()
	ls.stopped = make(chan struct{})
	ls.mu.Unlock()
	s.leaseRound(ctx)
	go func() {
		defer close(ls.stopped)
		tick := time.NewTicker(leaseRenew)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				s.leasesStop()
				return
			case <-tick.C:
				s.leaseRound(ctx)
				s.followChanges(ctx)
			}
		}
	}()
}

// leasesStop waits briefly for the jobs to end, then gives up this replica's
// leases and its place in the list, so others take over without waiting for
// the leases to expire.
func (s *Server) leasesStop() {
	ls := s.leases()
	done := make(chan struct{})
	go func() { ls.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		s.logger.Warn("leases: jobs still running at shutdown; leaving their leases to expire")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.db.Exec(ctx, `UPDATE visionary_leases SET expires_ms = `+s.q.dbNowMs()+`, holder = '' WHERE holder = ?`, s.replicaID()); err != nil {
		s.logger.Warn("leases: release at shutdown", "err", err)
	}
	_ = s.db.Exec(ctx, `DELETE FROM visionary_replicas WHERE id = ?`, s.replicaID())
}

// Wait blocks until the background work has stopped and its leases are
// released after Start's context ended, or until ctx ends.
func (s *Server) Wait(ctx context.Context) {
	ls := s.leases()
	ls.mu.Lock()
	stopped := ls.stopped
	ls.mu.Unlock()
	if stopped == nil {
		return
	}
	select {
	case <-stopped:
	case <-ctx.Done():
	}
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}
