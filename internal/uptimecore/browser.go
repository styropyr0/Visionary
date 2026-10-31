package uptimecore

import (
	"context"
	"fmt"
	"time"
)

// Run retries once on failure like HTTP checks. An error means the check couldn't run at all
func (p *BrowserPool) Run(ctx context.Context, c Check, allowPrivate bool) (Result, BrowserRun, error) {
	res, run, err := p.PlayOnce(ctx, c, allowPrivate)
	if err != nil || res.OK || res.blocked || ctx.Err() != nil {
		return res, run, err
	}
	select {
	case <-ctx.Done():
		return res, run, nil
	case <-time.After(RetryDelay):
	}
	res2, run2, err := p.PlayOnce(ctx, c, allowPrivate)
	if err != nil {
		return res, run, nil
	}
	res2.Retried = true
	return res2, run2, nil
}

// A run that outlives its timeout by far has hung the browser,
// which is then killed so the next run starts a new one
func (p *BrowserPool) PlayOnce(ctx context.Context, c Check, allowPrivate bool) (Result, BrowserRun, error) {
	b, err := p.acquire(ctx)
	if err != nil {
		return Result{}, BrowserRun{}, err
	}
	defer p.release()
	var guard *guardProxy
	if !allowPrivate {
		if guard, err = startGuardProxy(); err != nil {
			return Result{}, BrowserRun{}, err
		}
		defer guard.close()
	}
	type out struct {
		res Result
		run BrowserRun
	}
	done := make(chan out, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				p.logger.Error("browser check panicked", "check_id", c.ID, "panic", fmt.Sprint(r))
				done <- out{res: Result{CreatedAt: now(), Error: "the browser run crashed"}}
			}
		}()
		r, b := runBrowserCheck(ctx, b, c, guard)
		done <- out{r, b}
	}()
	watchdog := time.Duration(c.TimeoutMs)*time.Millisecond + 45*time.Second
	select {
	case o := <-done:
		return o.res, o.run, nil
	case <-time.After(watchdog):
		p.logger.Warn("browser check hung; restarting the browser", "check_id", c.ID)
		p.kill()
		return Result{CreatedAt: now(), DurationMs: float64(watchdog.Milliseconds()), Error: "the browser stopped responding"},
			BrowserRun{FailedStep: -1, Steps: []StepResult{}, Console: []string{}, Network: []NetFailure{}}, nil
	}
}

func now() string { return time.Now().UTC().Format(TimeLayout) }
