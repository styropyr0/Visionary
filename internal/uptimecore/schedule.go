package uptimecore

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"
)

// A check never runs twice at once, and when every worker is busy
// due checks wait for the next tick

const (
	tick   = time.Second
	reload = 30 * time.Second
)

type Scheduler struct {
	Load           func(ctx context.Context) ([]Check, error)
	Execute        func(ctx context.Context, c Check)
	Workers        int
	BrowserWorkers int
	// Without a browser the check waits rather than failing
	BrowserReady func() bool
	Logger       *slog.Logger

	wake    chan struct{}
	mu      sync.Mutex
	running map[int64]bool
}

// Fill its fields before Run
func NewScheduler() *Scheduler {
	return &Scheduler{wake: make(chan struct{}, 1), running: map[int64]bool{}}
}

func (u *Scheduler) Wake() {
	select {
	case u.wake <- struct{}{}:
	default:
	}
}

type scheduled struct {
	check Check
	next  time.Time
}

func (u *Scheduler) Run(ctx context.Context) {
	jobs := make(chan Check)
	browserJobs := make(chan Check)
	var wg sync.WaitGroup
	work := func(n int, ch chan Check) {
		for range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for c := range ch {
					u.execute(ctx, c)
				}
			}()
		}
	}
	work(u.Workers, jobs)
	work(max(1, u.BrowserWorkers), browserJobs)
	defer func() {
		close(jobs)
		close(browserJobs)
		wg.Wait()
	}()

	schedule := map[int64]*scheduled{}
	t := time.NewTicker(tick)
	defer t.Stop()
	var lastReload time.Time
	for {
		now := time.Now()
		if now.Sub(lastReload) >= reload {
			u.reload(ctx, schedule)
			lastReload = now
		}
		for _, e := range schedule {
			if now.Before(e.next) || u.isRunning(e.check.ID) {
				continue
			}
			ch := jobs
			if e.check.Kind == "browser" {
				if u.BrowserReady == nil || !u.BrowserReady() {
					e.next = now.Add(time.Minute)
					continue
				}
				ch = browserJobs
			}
			select {
			case ch <- e.check:
				u.setRunning(e.check.ID, true)
				e.next = now.Add(Jittered(e.check.IntervalS))
			default:
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-u.wake:
			lastReload = time.Time{}
		case <-t.C:
		}
	}
}

// Jittered is the interval ±5% (at most ±3s), so checks with the same
// interval drift apart instead of firing together forever
func Jittered(intervalS int) time.Duration {
	d := time.Duration(intervalS) * time.Second
	j := min(d/20, 3*time.Second)
	return d - j + time.Duration(rand.Int64N(int64(2*j)+1))
}

func (u *Scheduler) reload(ctx context.Context, schedule map[int64]*scheduled) {
	checks, err := u.Load(ctx)
	if err != nil {
		if ctx.Err() == nil && u.Logger != nil {
			u.Logger.Warn("uptime checks not loaded", "err", err)
		}
		return
	}
	seen := map[int64]bool{}
	for _, c := range checks {
		seen[c.ID] = true
		if e, ok := schedule[c.ID]; ok {
			if e.check.IntervalS != c.IntervalS {
				e.next = time.Now().Add(time.Duration(rand.Int64N(int64(5 * time.Second))))
			}
			e.check = c
			continue
		}
		spread := min(time.Duration(c.IntervalS)*time.Second/2, 15*time.Second)
		schedule[c.ID] = &scheduled{check: c, next: time.Now().Add(time.Duration(rand.Int64N(int64(spread))))}
	}
	for id := range schedule {
		if !seen[id] {
			delete(schedule, id)
		}
	}
}

func (u *Scheduler) isRunning(id int64) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.running[id]
}

func (u *Scheduler) setRunning(id int64, v bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if v {
		u.running[id] = true
	} else {
		delete(u.running, id)
	}
}

// Nothing a check does may stop the loop
func (u *Scheduler) execute(ctx context.Context, c Check) {
	defer u.setRunning(c.ID, false)
	defer func() {
		if p := recover(); p != nil && u.Logger != nil {
			u.Logger.Error("uptime check panicked", "check_id", c.ID, "panic", fmt.Sprint(p))
		}
	}()
	u.Execute(ctx, c)
}
