package visionary

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/styropyr0/visionary/sdks/go/ratelimit"
)

// Rate limits from Security → Rate limits: polled from visionary_settings,
// enforced in Middleware (request phase) and RateLimited (user phase), with
// what was limited written to rate_limit_events. Counters are per process.

const rateLimitPoll = 15 * time.Second

var rateLimitColumns = []string{"created_at", "service", "instance", "rule_id", "rule_name", "outcome", "key_value", "ip",
	"country", "method", "path", "user_id", "hits"}

func (a *Agent) rateLimitLoop(ctx context.Context) {
	defer close(a.rlDone)
	ctx = quiet(ctx)
	version, badVersion, writeFailed := "", "", false
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			a.flushRateLimits(context.WithoutCancel(ctx), &writeFailed)
			return
		case <-a.rlStop:
			a.flushRateLimits(ctx, &writeFailed)
			return
		case <-t.C:
		}
		qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		rows, err := a.db.rows(qctx, "SELECT value FROM visionary_settings WHERE name = '"+ratelimit.SettingVersion+"'")
		switch {
		case err != nil:
			// keep the rules in force
		case len(rows) == 0 || len(rows[0]) == 0:
			if version != "" {
				a.rl.Clear()
			}
			version = ""
		case rows[0][0] != version:
			next := rows[0][0]
			cfg, cerr := a.db.rows(qctx, "SELECT value FROM visionary_settings WHERE name = '"+ratelimit.SettingConfig+"'")
			if cerr != nil {
				break
			}
			raw := ""
			if len(cfg) > 0 && len(cfg[0]) > 0 {
				raw = cfg[0][0]
			}
			parsed, perr := ratelimit.Parse([]byte(raw))
			if raw != "" && perr != nil {
				if badVersion != next {
					badVersion = next
					a.cfg.Logger.Warn("visionary: rate limit rules are not valid JSON; keeping the previous ones", "err", perr)
				}
				break
			}
			a.rl.Load(parsed)
			version = next
		}
		cancel()
		a.rl.Sweep(time.Now())
		a.flushRateLimits(ctx, &writeFailed)
		t.Reset(rateLimitPoll)
	}
}

func (a *Agent) flushRateLimits(ctx context.Context, failed *bool) {
	events := a.rl.Drain()
	if len(events) == 0 {
		return
	}
	g := &rowGroup{table: "rate_limit_events", columns: rateLimitColumns, insert: insertSQL("rate_limit_events", rateLimitColumns, a.db.stringParams())}
	for _, e := range events {
		key := nullable(e.KeyValue)
		g.rows = append(g.rows, []any{e.CreatedAt.UTC().Format(tsLayout), nullable(a.ident.service), a.ident.instance, e.RuleID, e.RuleName,
			e.Outcome, key, nullable(e.IP), nullable(e.Country), nullable(e.Method), nullable(e.Path), nullable(e.UserID), e.Hits})
	}
	stmts := g.statements(nil, a.db.stringParams())
	wctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for len(stmts) > 0 {
		n := min(len(stmts), 50)
		if err := a.db.batch(wctx, stmts[:n]); err != nil {
			if !*failed {
				*failed = true
				a.cfg.Logger.Info("visionary: could not record rate-limited requests (the Visionary server creates rate_limit_events)", "err", err)
			}
			return
		}
		stmts = stmts[n:]
	}
}

// limitRequest is the request as the limiter sees it.
func limitRequest(r *http.Request) *ratelimit.Request {
	var query map[string][]string
	return &ratelimit.Request{
		Method:   r.Method,
		Path:     r.URL.Path,
		RemoteIP: clientIP(r),
		Header:   func(name string) string { return r.Header.Get(name) },
		Query: func(name string) string {
			if query == nil {
				query = r.URL.Query()
			}
			if v := query[name]; len(v) > 0 {
				return v[0]
			}
			return ""
		},
	}
}

// checkRateLimit runs the request phase and answers when it denies.
func (a *Agent) checkRateLimit(w http.ResponseWriter, r *http.Request, ann *annotations) bool {
	if !a.rl.Active() {
		return false
	}
	ann.limitReq = limitRequest(r)
	ann.limit = a.rl.CheckRequest(ann.limitReq, time.Now())
	if d := ann.limit.Denied(); d != nil {
		writeDenial(w, d)
		return true
	}
	return false
}

func writeDenial(w http.ResponseWriter, d *ratelimit.Denial) {
	h := w.Header()
	h.Set("Content-Type", d.ContentType)
	if d.RetryAfter > 0 {
		h.Set("Retry-After", strconv.Itoa(d.RetryAfter))
	}
	w.WriteHeader(d.Status)
	_, _ = w.Write(d.Body)
}

// RateLimited checks the rate-limit rules that need the user (by user,
// role or an attribute such as the plan) once SetUser has run, and answers
// the request when one denies it. Call it after your auth code:
//
//	visionary.SetUser(ctx, visionary.User{ID: id, Role: role})
//	if visionary.RateLimited(w, r, map[string]string{"plan": plan}) {
//		return
//	}
//
// Only the first call per request counts; it returns false outside Middleware.
func RateLimited(w http.ResponseWriter, r *http.Request, attrs map[string]string) bool {
	ann, ok := r.Context().Value(ctxKey{}).(*annotations)
	if !ok || ann.limit == nil || ann.agent == nil {
		return false
	}
	ann.mu.Lock()
	user, answered := ann.user, ann.limitAnswered
	ann.mu.Unlock()
	if answered {
		return true
	}
	d := ann.agent.rl.CheckUser(ann.limit, ann.limitReq, ratelimit.User{ID: user.ID, Role: user.Role}, attrs, time.Now())
	if d == nil {
		return false
	}
	ann.mu.Lock()
	ann.limitAnswered = true
	ann.mu.Unlock()
	writeDenial(w, d)
	return true
}

// RateLimitUsers wraps a handler with RateLimited; mount it after the
// middleware that calls SetUser.
func RateLimitUsers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if RateLimited(w, r, nil) {
			return
		}
		next.ServeHTTP(w, r)
	})
}

func newLimiter(cfg Config) *ratelimit.Limiter {
	if cfg.RateLimitOff {
		return nil
	}
	l := ratelimit.New(cfg.Service)
	l.SetRedactHeaders(append(append([]string{}, defaultRedactHeaders...), cfg.Requests.RedactHeaders...))
	return l
}
