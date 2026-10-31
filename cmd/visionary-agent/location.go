package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/styropyr0/visionary/internal/agentapi"
	"github.com/styropyr0/visionary/internal/uptimecore"
)

const (
	locationBatchEvery = 5 * time.Second
	locationBatchMax   = 100
	locationQueueMax   = 5000
)

type locationRunner struct {
	cfg      config
	log      *slog.Logger
	client   *http.Client
	instance string
	browser  *uptimecore.BrowserPool
	sched    *uptimecore.Scheduler

	mu           sync.Mutex
	checks       []uptimecore.Check
	serverAllows bool
	active       bool
	queue        []agentapi.LocationResult
	flushNow     chan struct{}
}

func newLocationRunner(cfg config, logger *slog.Logger) *locationRunner {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	uptimecore.GuardHint = " (set VISIONARY_AGENT_ALLOW_PRIVATE=true on this location's runner to allow it)"
	l := &locationRunner{cfg: cfg, log: logger, client: &http.Client{Timeout: 30 * time.Second}, instance: hex.EncodeToString(b),
		flushNow: make(chan struct{}, 1),
		browser: uptimecore.NewBrowserPool(cfg.browserWorkers, func() string { return "" },
			"Install Chrome or Chromium on this machine, or point VISIONARY_CHROME at its executable.", logger)}
	l.sched = uptimecore.NewScheduler()
	l.sched.Load, l.sched.Execute = l.load, l.execute
	l.sched.Workers, l.sched.BrowserWorkers = 8, cfg.browserWorkers
	l.sched.BrowserReady, l.sched.Logger = l.browser.Available, logger
	return l
}

func runLocation(ctx context.Context, cfg config, logger *slog.Logger) error {
	l := newLocationRunner(cfg, logger)
	defer l.browser.Close()
	logger.Info("visionary-agent started as an uptime location", "version", version, "location", cfg.location, "server", cfg.url,
		"browser", l.browser.Available(), "allow_private", fallbackStr(cfg.allowPrivate, "as the server"))
	done := make(chan struct{})
	go func() {
		defer close(done)
		l.sched.Run(ctx)
	}()
	go l.send(ctx)
	l.poll(ctx)
	<-done
	fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	l.flush(fctx)
	return nil
}

func (l *locationRunner) allowPrivate() bool {
	if l.cfg.allowPrivate != "" {
		return l.cfg.allowPrivate == "true"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.serverAllows
}

func (l *locationRunner) load(context.Context) ([]uptimecore.Check, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.checks), nil
}

func (l *locationRunner) execute(ctx context.Context, c uptimecore.Check) {
	out := agentapi.LocationResult{CheckID: c.ID}
	switch c.Kind {
	case "http", "api", "tcp", "dns", "ping", "ssl":
		out.Result = uptimecore.ProbeWithRetry(ctx, c, l.allowPrivate)
		if out.Result.Skip != "" {
			return
		}
	case "browser":
		res, run, err := l.browser.Run(ctx, c, l.allowPrivate())
		if err != nil {
			return
		}
		out.Result, out.Screenshot, out.FinalScreenshot, out.StepShots = res, run.Screenshot, run.Final, run.StepShots
		out.Browser = &run
	default:
		return
	}
	if ctx.Err() != nil {
		return
	}
	l.mu.Lock()
	l.queue = append(l.queue, out)
	if over := len(l.queue) - locationQueueMax; over > 0 {
		l.queue = l.queue[over:]
	}
	full := len(l.queue) >= locationBatchMax
	l.mu.Unlock()
	if full {
		select {
		case l.flushNow <- struct{}{}:
		default:
		}
	}
}

func (l *locationRunner) poll(ctx context.Context) {
	wait := time.Duration(0)
	var lastMsg string
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = 15 * time.Second
		kinds := []string{"http", "api", "tcp", "dns", "ssl"}
		if uptimecore.PingStatus().Available {
			kinds = append(kinds, "ping")
		}
		st := l.browser.Status()
		if st.Available {
			kinds = append(kinds, "browser")
		}
		var resp agentapi.LocationChecks
		status, msg, err := l.post(ctx, agentapi.LocationPollPath, agentapi.LocationPoll{Location: l.cfg.location, Instance: l.instance,
			Version: version, Host: l.cfg.host, Browser: st.Available, BrowserVersion: st.Version, Kinds: kinds}, &resp)
		switch {
		case err != nil:
			l.log.Warn("could not reach the server; will retry", "err", err)
			continue
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			l.log.Error("the server refused this location's key", "status", status, "error", msg)
			wait = time.Minute
			continue
		case status >= 300:
			l.log.Warn("poll failed; will retry", "status", status, "error", msg)
			continue
		}
		if resp.PollS >= 5 {
			wait = time.Duration(resp.PollS) * time.Second
		}
		var checks []uptimecore.Check
		for _, c := range resp.Checks {
			if slices.Contains(kinds, c.Kind) {
				checks = append(checks, c)
			}
		}
		l.mu.Lock()
		changed := l.active != resp.Active || !sameChecks(l.checks, checks)
		l.checks, l.active, l.serverAllows = checks, resp.Active, resp.AllowPrivate
		l.mu.Unlock()
		if resp.Message != lastMsg && resp.Message != "" {
			l.log.Info(resp.Message)
		}
		lastMsg = resp.Message
		if changed {
			l.log.Info("checks for this location", "active", resp.Active, "checks", len(checks), "skipped", len(resp.Checks)-len(checks))
			l.sched.Wake()
		}
	}
}

func sameChecks(a, b []uptimecore.Check) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return bytes.Equal(ja, jb)
}

func (l *locationRunner) send(ctx context.Context) {
	t := time.NewTicker(locationBatchEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-l.flushNow:
		}
		l.flush(ctx)
	}
}

// A batch the server refuses for good (409 or another 4xx) is dropped,
// anything else waits for the next round
func (l *locationRunner) flush(ctx context.Context) {
	for {
		l.mu.Lock()
		n := min(len(l.queue), locationBatchMax)
		batch := slices.Clone(l.queue[:n])
		l.mu.Unlock()
		if n == 0 {
			return
		}
		status, msg, err := l.post(ctx, agentapi.LocationResultsPath, agentapi.LocationResults{Location: l.cfg.location, Instance: l.instance, Results: batch}, nil)
		switch {
		case err != nil:
			l.log.Warn("results not delivered; will retry", "err", err, "queued", n)
			return
		case status >= 500 || status == http.StatusTooManyRequests:
			l.log.Warn("results not delivered; will retry", "status", status, "error", msg)
			return
		case status >= 400:
			l.log.Error("server refused results; dropping them", "status", status, "error", msg, "results", n)
		}
		l.mu.Lock()
		l.queue = l.queue[min(n, len(l.queue)):]
		l.mu.Unlock()
	}
}

func (l *locationRunner) post(ctx context.Context, path string, body, out any) (int, string, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.cfg.url+path, bytes.NewReader(raw))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+l.cfg.key)
	req.Header.Set("User-Agent", "visionary-agent/"+version)
	resp, err := l.client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return 0, "", err
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) != nil || e.Error == "" {
			e.Error = http.StatusText(resp.StatusCode)
		}
		return resp.StatusCode, e.Error, nil
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, "", fmt.Errorf("unreadable answer: %w", err)
		}
	}
	return resp.StatusCode, "", nil
}
