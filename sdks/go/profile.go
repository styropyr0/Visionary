package visionary

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"runtime/pprof"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// CPU profiles, switched on and off from the dashboard (Settings ->
// Profiling). The Visionary server keeps the switch in visionary_settings;
// the Agent reads it every 30 seconds on its own goroutine and, while it is
// on, samples the CPU with runtime/pprof for profile.duration_ms every
// profile.interval_s, skipping turns when no request arrived. Each profile is
// one profiles row of folded stacks plus the time each route's requests took
// while it ran (empty when none finished: quiet services and workers still
// get profiles). VISIONARY_PROFILE=false ignores the dashboard entirely.

const (
	profileMinSamples   = 20
	profileSettingsPoll = 30 * time.Second
	profileNowWindow    = 5 * time.Minute
	defaultProfileEvery = 300 * time.Second
	defaultProfileFor   = 10 * time.Second
)

var profileColumns = []string{"created_at", "service", "instance", "version", "sdk", "duration_ms", "samples", "routes", "folded"}

// profileSettings is what the dashboard asked for.
type profileSettings struct {
	enabled  bool
	services []string
	interval time.Duration
	duration time.Duration
	now      string // profile.now, raw
}

// parseProfileSettings reads (name, value) rows; anything missing or
// unreadable takes its default.
func parseProfileSettings(rows [][]string) profileSettings {
	s := profileSettings{interval: defaultProfileEvery, duration: defaultProfileFor}
	for _, r := range rows {
		if len(r) < 2 {
			continue
		}
		v := r[1]
		switch r[0] {
		case "profile.enabled":
			s.enabled, _ = strconv.ParseBool(v)
		case "profile.services":
			var list []string
			if json.Unmarshal([]byte(v), &list) == nil {
				s.services = list
			}
		case "profile.interval_s":
			if n, err := strconv.ParseFloat(v, 64); err == nil && n > 0 {
				s.interval = max(time.Duration(n*float64(time.Second)), time.Second)
			}
		case "profile.duration_ms":
			if n, err := strconv.ParseFloat(v, 64); err == nil && n > 0 {
				s.duration = min(max(time.Duration(n*float64(time.Millisecond)), 100*time.Millisecond), 5*time.Minute)
			}
		case "profile.now":
			s.now = v
		}
	}
	return s
}

// includes reports whether service takes part: every service when the list
// is empty, and a service with no name only then.
func (s profileSettings) includes(service string) bool {
	if len(s.services) == 0 {
		return true
	}
	return service != "" && slices.Contains(s.services, service)
}

var nowLayouts = []string{tsLayout, time.RFC3339Nano, "2006-01-02 15:04:05.000", "2006-01-02 15:04:05", "2006-01-02T15:04:05"}

// freshNow reports whether a profile.now value asks for a profile: newer than
// the SDK's start and less than 5 minutes old.
func freshNow(v string, started, now time.Time) bool {
	for _, l := range nowLayouts {
		if t, err := time.Parse(l, v); err == nil {
			return t.After(started) && now.Sub(t) < profileNowWindow
		}
	}
	return false
}

// profiler runs the schedule and keeps the request tally the profiles need.
type profiler struct {
	db        store
	ident     identity
	logger    *slog.Logger
	retention int
	insert    string
	namer     *frameNamer

	// started counts requests that reached Middleware; busy, while a
	// profile runs, sums the milliseconds of requests that finished.
	started    atomic.Int64
	collecting atomic.Bool
	mu         sync.Mutex
	busy       map[string]float64

	// Loop state, touched only by run.
	startedAt   time.Time
	settings    profileSettings
	on          bool
	oneOff      bool
	lastNow     string
	lastStarted int64
	next        time.Time
	readFailed  bool
	wrote       bool
	lastPurge   time.Time
	pollEvery   time.Duration
	tick        time.Duration

	stop chan struct{}
	done chan struct{}
	once sync.Once
}

func newProfiler(ident identity, db store, logger *slog.Logger, retentionDays int) *profiler {
	return &profiler{
		db: db, ident: ident, logger: logger, retention: retentionDays,
		insert:    insertSQL("profiles", profileColumns, db.stringParams()),
		namer:     newFrameNamer(),
		startedAt: time.Now(),
		pollEvery: profileSettingsPoll,
		tick:      time.Second,
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
}

// requestStarted is called by Middleware for every request.
func (p *profiler) requestStarted() { p.started.Add(1) }

// requestFinished adds a finished request's time to the running profile.
func (p *profiler) requestFinished(key string, d time.Duration) {
	if !p.collecting.Load() {
		return
	}
	p.mu.Lock()
	if p.busy != nil {
		p.busy[key] += float64(d) / float64(time.Millisecond)
	}
	p.mu.Unlock()
}

func (p *profiler) start(ctx context.Context) { go p.run(ctx) }

func (p *profiler) close(ctx context.Context) {
	p.once.Do(func() { close(p.stop) })
	select {
	case <-p.done:
	case <-ctx.Done():
	}
}

func (p *profiler) run(ctx context.Context) {
	defer close(p.done)
	ctx = quiet(ctx)
	p.poll(ctx, time.Now())
	nextPoll := time.Now().Add(p.pollEvery)
	t := time.NewTicker(p.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.stop:
			return
		case <-t.C:
		}
		now := time.Now()
		if !now.Before(nextPoll) {
			p.poll(ctx, now)
			nextPoll = now.Add(p.pollEvery)
		}
		p.turn(ctx, now)
	}
}

// poll reads the dashboard's settings. A failed read means off.
func (p *profiler) poll(ctx context.Context, now time.Time) {
	qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	rows, err := p.db.rows(qctx, `SELECT name, value FROM visionary_settings WHERE name LIKE 'profile.%'`)
	cancel()
	if err != nil {
		if !p.readFailed {
			p.readFailed = true
			p.logger.Info("visionary: CPU profiling settings unavailable; profiling stays off until the Visionary server creates visionary_settings", "err", err)
		}
		rows = nil
	}
	p.apply(parseProfileSettings(rows), now)
}

// apply takes new settings: logs a switch on or off, and arms a one-off
// profile when profile.now changed to a fresh value.
func (p *profiler) apply(s profileSettings, now time.Time) {
	prev := p.settings
	p.settings = s
	on := s.enabled && s.includes(p.ident.service)
	switch {
	case on && !p.on:
		p.next = now.Add(s.interval)
		p.logger.Info("visionary: CPU profiling on", "interval", s.interval, "duration", s.duration)
	case !on && p.on:
		p.logger.Info("visionary: CPU profiling off")
	case on && s.interval < prev.interval && now.Add(s.interval).Before(p.next):
		p.next = now.Add(s.interval)
	}
	p.on = on
	if s.now != "" && s.now != p.lastNow {
		p.lastNow = s.now
		if s.includes(p.ident.service) && freshNow(s.now, p.startedAt, now) {
			p.oneOff = true
		}
	}
}

// turn takes a profile when one is due.
func (p *profiler) turn(ctx context.Context, now time.Time) {
	switch {
	case p.oneOff:
		p.oneOff = false
		p.lastStarted = p.started.Load()
		p.capture(ctx, 1)
		p.next = time.Now().Add(p.settings.interval)
	case p.on && !now.Before(p.next):
		p.next = now.Add(p.settings.interval)
		if n := p.started.Load(); n != p.lastStarted {
			p.lastStarted = n
			p.capture(ctx, profileMinSamples)
		}
	}
	if p.wrote && p.retention > 0 && time.Since(p.lastPurge) > 24*time.Hour {
		p.lastPurge = time.Now()
		purge(ctx, p.db, p.logger, p.db.dialect().purgeSQL("profiles"), p.retention, "profiles")
	}
}

// capture samples the CPU for the configured duration and writes the
// profile when it has at least minSamples: 1 for "Profile now", which was
// asked for, profileMinSamples for the schedule.
func (p *profiler) capture(ctx context.Context, minSamples int) {
	var buf bytes.Buffer
	if err := pprof.StartCPUProfile(&buf); err != nil {
		return // the process already profiles itself
	}
	p.mu.Lock()
	p.busy = map[string]float64{}
	p.mu.Unlock()
	p.collecting.Store(true)
	began := time.Now()
	timer := time.NewTimer(p.settings.duration)
	interrupted := false
	select {
	case <-timer.C:
	case <-ctx.Done():
		interrupted = true
	case <-p.stop:
		interrupted = true
	}
	timer.Stop()
	pprof.StopCPUProfile()
	elapsed := time.Since(began)
	p.collecting.Store(false)
	p.mu.Lock()
	busy := p.busy
	p.busy = nil
	p.mu.Unlock()
	if interrupted {
		return
	}
	folded, samples, err := foldProfile(buf.Bytes(), p.namer)
	if err != nil {
		p.logger.Warn("visionary: reading a CPU profile failed", "err", err)
		return
	}
	if samples < minSamples {
		p.logger.Debug("visionary: CPU profile dropped, too few samples", "samples", samples, "min", minSamples)
		return
	}
	routes := make(map[string]int64, len(busy))
	for k, ms := range busy {
		routes[k] = max(1, int64(math.Round(ms)))
	}
	routesJSON, _ := json.Marshal(routes)
	wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	err = p.db.batch(wctx, []stmt{{sql: p.insert, args: []any{
		began.UTC().Format(tsLayout), nullable(p.ident.service), p.ident.instance, nullable(p.ident.version), "go",
		millis(elapsed), int64(samples), string(routesJSON), folded,
	}}})
	if err != nil {
		p.logger.Warn("visionary: writing a CPU profile failed", "err", err)
		return
	}
	if !p.wrote {
		p.wrote = true
		p.lastPurge = time.Now().Add(-23 * time.Hour) // first purge an hour from now
	}
}
