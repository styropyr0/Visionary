package analytics

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
	"time"
)

// The engine evaluates every enabled rule once per interval, each at its own
// offset within the interval so a long rule list does not hit the database
// all at once. Each rule series runs a small state machine:
//
//	ok ──breach──▶ pending ──held for "for"──▶ firing ──clear──▶ resolved
//
// Firing and resolving notify the rule's channels and add an alert_events
// row; a firing series is re-sent every "repeat" until it clears. Muted rules
// keep evaluating and recording events but send nothing.

const (
	maxConcurrentEvals = 4
	evalTimeout        = 45 * time.Second
	recentPoints       = 60
	rulesCacheFor      = 30 * time.Second
)

type alertEngine struct {
	s        *Server
	interval time.Duration
	sem      chan struct{}
	ctx      context.Context

	mu          sync.Mutex
	next        map[int64]time.Time
	running     map[int64]bool
	lastErr     map[int64]string
	rules       []alertRule
	rulesAt     time.Time
	instances   []instanceSeen
	instancesAt time.Time
	budgets     map[string]cachedBudget
}

type cachedBudget struct {
	total, bad int64
	at         time.Time
}

// Server has no field for the engine (server.go belongs to the core), so each
// server's engine is kept here.
var alertEngines sync.Map

func (s *Server) alertEngine() *alertEngine {
	if e, ok := alertEngines.Load(s); ok {
		return e.(*alertEngine)
	}
	interval := s.confDuration("alerts_interval")
	if interval < 10*time.Second {
		interval = 10 * time.Second
	}
	e := &alertEngine{
		s: s, interval: interval, sem: make(chan struct{}, maxConcurrentEvals), ctx: context.Background(),
		next: map[int64]time.Time{}, running: map[int64]bool{}, lastErr: map[int64]string{}, budgets: map[string]cachedBudget{},
	}
	actual, _ := alertEngines.LoadOrStore(s, e)
	return actual.(*alertEngine)
}

func (s *Server) alertsStart(ctx context.Context) {
	e := s.alertEngine()
	e.ctx = ctx
	go e.loop(ctx)
	go s.digestLoop(ctx)
}

func (e *alertEngine) loop(ctx context.Context) {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			e.schedule(ctx, now)
		}
	}
}

// schedule starts the rules that are due.
func (e *alertEngine) schedule(ctx context.Context, now time.Time) {
	rules, err := e.enabledRules(ctx)
	if err != nil {
		e.s.logger.Warn("alerts: load rules", "err", err)
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	live := map[int64]bool{}
	for _, r := range rules {
		live[r.ID] = true
		next, seen := e.next[r.ID]
		if !seen {
			e.next[r.ID] = now.Add(e.stagger(r.ID))
			continue
		}
		if now.Before(next) || e.running[r.ID] {
			continue
		}
		e.next[r.ID] = now.Add(e.interval)
		e.running[r.ID] = true
		go e.run(ctx, r)
	}
	for id := range e.next {
		if !live[id] {
			delete(e.next, id)
		}
	}
}

func (e *alertEngine) stagger(id int64) time.Duration {
	secs := int64(e.interval / time.Second)
	return time.Duration((id*7919)%secs) * time.Second
}

func (e *alertEngine) run(ctx context.Context, r alertRule) {
	defer func() {
		if p := recover(); p != nil {
			e.s.logger.Error("alerts: evaluation panicked", "rule_id", r.ID, "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
		}
		e.mu.Lock()
		e.running[r.ID] = false
		e.mu.Unlock()
	}()
	select {
	case e.sem <- struct{}{}:
		defer func() { <-e.sem }()
	case <-ctx.Done():
		return
	}
	if err := e.evaluate(ctx, r, time.Now().UTC()); err != nil && ctx.Err() == nil {
		e.s.logger.Warn("alerts: evaluation failed", "rule_id", r.ID, "rule", r.Name, "err", err)
	}
}

func (e *alertEngine) enabledRules(ctx context.Context) ([]alertRule, error) {
	e.mu.Lock()
	if time.Since(e.rulesAt) < rulesCacheFor && e.rules != nil {
		r := e.rules
		e.mu.Unlock()
		return r, nil
	}
	e.mu.Unlock()
	rules, err := e.s.listRules(ctx, true)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	e.rules, e.rulesAt = rules, time.Now()
	e.mu.Unlock()
	return rules, nil
}

// invalidate makes the next tick reload rules; changed rules are evaluated
// right away.
func (e *alertEngine) invalidate(changed int64) {
	e.mu.Lock()
	e.rulesAt = time.Time{}
	if changed > 0 {
		e.next[changed] = time.Now()
		delete(e.lastErr, changed)
	}
	e.mu.Unlock()
}

func (e *alertEngine) setErr(id int64, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err == nil {
		delete(e.lastErr, id)
	} else {
		e.lastErr[id] = userMessage(err)
	}
}

func (e *alertEngine) errFor(id int64) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastErr[id]
}

// evaluate observes a rule, advances each series' state and sends what the
// transitions call for.
func (e *alertEngine) evaluate(parent context.Context, r alertRule, now time.Time) error {
	ctx, cancel := context.WithTimeout(parent, evalTimeout)
	defer cancel()
	s := e.s
	obs, err := s.observe(ctx, r, now)
	e.setErr(r.ID, err)
	if err != nil {
		return err
	}
	states, err := s.ruleStates(ctx, r.ID)
	if err != nil {
		return err
	}
	byKey := map[string]*seriesState{}
	for _, st := range states[r.ID] {
		byKey[st.Key] = st
	}
	seen := map[string]bool{}
	forDur, _ := parseSpan(r.Spec.For)
	repeat, _ := parseSpan(r.Spec.Repeat)
	muted := r.MutedUntil != "" && r.MutedUntil > now.Format(tsLayout)

	handle := func(st *seriesState, ob observation) error {
		tr := advance(st, ob, now, forDur, repeat)
		if err := s.saveState(ctx, r.ID, st); err != nil {
			return err
		}
		switch tr {
		case "firing", "resolved":
			ev := &alertEvent{
				CreatedAt: now.Format(tsLayout), RuleID: r.ID, RuleName: r.Name, Kind: r.Kind, Severity: r.Severity,
				State: tr, SeriesKey: st.Key, Value: ob.Value, Threshold: ob.Threshold, Message: ob.Message,
			}
			if err := s.insertEvent(ctx, ev); err != nil {
				return err
			}
			if !muted {
				e.dispatch(r, ev, now)
			}
		case "reminder":
			if !muted {
				ev := &alertEvent{CreatedAt: now.Format(tsLayout), RuleID: r.ID, RuleName: r.Name, Kind: r.Kind, Severity: r.Severity,
					State: "firing", SeriesKey: st.Key, Value: ob.Value, Threshold: ob.Threshold, Message: ob.Message}
				e.remind(r, ev, st.Since)
			}
		}
		return nil
	}

	for _, ob := range obs {
		seen[ob.Key] = true
		st := byKey[ob.Key]
		if st == nil {
			st = &seriesState{Key: ob.Key, State: "ok", Since: now.Format(tsLayout)}
		}
		if err := handle(st, ob); err != nil {
			return err
		}
	}
	// Series that stopped reporting (an instance that went away from a CPU
	// rule, say) resolve once; settled ones are dropped.
	for key, st := range byKey {
		if seen[key] {
			continue
		}
		if st.State == "firing" || st.State == "pending" {
			if err := handle(st, observation{Key: key, Label: st.Label, Message: fallback(st.Label, "This series") + " stopped reporting."}); err != nil {
				return err
			}
			continue
		}
		if err := s.deleteState(ctx, r.ID, key); err != nil {
			return err
		}
	}
	return nil
}

// advance moves a series through the state machine and reports the
// transition worth telling someone about: firing, resolved, reminder or "".
func advance(st *seriesState, ob observation, now time.Time, forDur, repeat time.Duration) string {
	ts := now.Format(tsLayout)
	st.EvaluatedAt, st.Value, st.Threshold, st.Message = ts, ob.Value, ob.Threshold, ob.Message
	if ob.Label != "" {
		st.Label = ob.Label
	}
	if ob.Value != nil {
		st.Recent = append(st.Recent, [2]float64{float64(now.Unix()), *ob.Value})
		if len(st.Recent) > recentPoints {
			st.Recent = st.Recent[len(st.Recent)-recentPoints:]
		}
	}
	set := func(state string) {
		st.State, st.Since = state, ts
	}
	switch st.State {
	case "firing":
		if !ob.Breach {
			set("resolved")
			st.PendingSince = ""
			return "resolved"
		}
		if repeat > 0 && st.NotifiedAt != "" {
			if last, err := time.Parse(tsLayout, st.NotifiedAt); err == nil && now.Sub(last) >= repeat {
				st.NotifiedAt = ts
				return "reminder"
			}
		}
		return ""
	case "pending":
		if !ob.Breach {
			set("ok")
			st.PendingSince = ""
			return ""
		}
		if since, err := time.Parse(tsLayout, st.PendingSince); err != nil || now.Sub(since) >= forDur {
			set("firing")
			st.NotifiedAt = ts
			return "firing"
		}
		return ""
	default: // ok, resolved
		if !ob.Breach {
			return ""
		}
		st.PendingSince = ts
		if forDur <= 0 {
			set("firing")
			st.NotifiedAt = ts
			return "firing"
		}
		set("pending")
		return ""
	}
}

// dispatch explains (when a model is configured) and sends a transition in
// the background, so slow channels never hold up evaluation. The explanation
// has its own 20s budget; the notification goes out when it is done or that
// budget runs out, whichever is first.
func (e *alertEngine) dispatch(r alertRule, ev *alertEvent, at time.Time) {
	go func() {
		defer e.recoverLog("dispatch")
		ctx, cancel := context.WithTimeout(e.ctx, 90*time.Second)
		defer cancel()
		s := e.s
		// Security findings name IPs, which never go to the model; their
		// message already says what happened.
		if ev.State == "firing" && s.llm() != nil && r.Spec.Metric != "security_events" {
			ev.Explanation = s.explainAlert(ctx, r, ev, at)
		}
		sent := s.sendToChannels(ctx, r.Channels, s.alertNotice(r, ev, ""))
		if err := s.finishEvent(ctx, ev.ID, ev.Explanation, sent); err != nil {
			s.logger.Warn("alerts: update event", "err", err)
		}
	}()
}

func (e *alertEngine) remind(r alertRule, ev *alertEvent, since string) {
	go func() {
		defer e.recoverLog("remind")
		ctx, cancel := context.WithTimeout(e.ctx, 60*time.Second)
		defer cancel()
		e.s.sendToChannels(ctx, r.Channels, e.s.alertNotice(r, ev, since))
	}()
}

func (e *alertEngine) recoverLog(what string) {
	if p := recover(); p != nil {
		e.s.logger.Error("alerts: "+what+" panicked", "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
	}
}

// knownInstances lists instances that reported in the last day, refreshed
// every ten minutes.
func (e *alertEngine) knownInstances(ctx context.Context, now time.Time) ([]instanceSeen, error) {
	e.mu.Lock()
	if time.Since(e.instancesAt) < 10*time.Minute && e.instances != nil {
		in := e.instances
		e.mu.Unlock()
		return in, nil
	}
	e.mu.Unlock()
	res, err := e.s.db.Query(ctx, `SELECT instance, MAX(service) FROM system_metrics WHERE created_at >= ? GROUP BY instance`,
		now.Add(-24*time.Hour).Format(tsLayout))
	if err != nil {
		return nil, err
	}
	out := make([]instanceSeen, 0, len(res.Rows))
	for _, row := range res.Rows {
		if len(row) == 2 {
			out = append(out, instanceSeen{Instance: toString(row[0]), Service: toString(row[1])})
		}
	}
	// An empty list is not cached, so the first instance is seen at once.
	if len(out) > 0 {
		e.mu.Lock()
		e.instances, e.instancesAt = out, time.Now()
		e.mu.Unlock()
	}
	return out, nil
}

// sloBudget returns the 30-day totals for an SLO scope, cached five minutes.
func (e *alertEngine) sloBudget(ctx context.Context, r alertRule, now time.Time, scope string, sargs []any) (int64, int64, string, error) {
	key := fmt.Sprint(r.Spec.Scope, r.Spec.Target)
	e.mu.Lock()
	c, ok := e.budgets[key]
	e.mu.Unlock()
	if ok && now.Sub(c.at) < 5*time.Minute {
		return c.total, c.bad, c.at.Format(tsLayout), nil
	}
	total, bad, err := e.s.sloBudgetQuery(ctx, now, scope, sargs)
	if err != nil {
		return 0, 0, "", err
	}
	if total > 0 {
		e.mu.Lock()
		e.budgets[key] = cachedBudget{total: total, bad: bad, at: now}
		e.mu.Unlock()
	}
	return total, bad, now.Format(tsLayout), nil
}

// digestTZ is where weekly digests are scheduled: the zone in Settings →
// Server, else the server's zone.
func (s *Server) digestTZ() *time.Location {
	if name := s.conf("digest_tz"); name != "" {
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
	}
	return time.Local
}

// tzName names a zone for people; the server's own zone is "Local" to Go.
func tzName(loc *time.Location) string {
	if loc != time.Local {
		return loc.String()
	}
	return "server time (" + time.Now().In(loc).Format("UTC-07:00") + ")"
}
