package ratelimit

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Outcomes recorded in rate_limit_events.
const (
	Limited    = "limited"
	Blocked    = "blocked"
	WouldLimit = "would_limit"
	WouldBlock = "would_block"
)

type propKind int

const (
	propIP propKind = iota
	propCountry
	propMethod
	propPath
	propHeader
	propQuery
	propGlobal
	propUser
	propRole
	propAttr
)

type prop struct {
	kind propKind
	name string // header (canonical lower case), query or attribute name
}

func (p prop) userPhase() bool { return p.kind == propUser || p.kind == propRole || p.kind == propAttr }

func parseProp(s string, key bool) (prop, bool) {
	if name, ok := strings.CutPrefix(s, "header:"); ok && name != "" {
		return prop{kind: propHeader, name: strings.ToLower(name)}, true
	}
	if name, ok := strings.CutPrefix(s, "query:"); ok && name != "" {
		return prop{kind: propQuery, name: name}, true
	}
	if name, ok := strings.CutPrefix(s, "attr:"); ok && name != "" {
		return prop{kind: propAttr, name: name}, true
	}
	switch s {
	case "ip":
		return prop{kind: propIP}, true
	case "country":
		return prop{kind: propCountry}, true
	case "method":
		return prop{kind: propMethod}, true
	case "path":
		return prop{kind: propPath}, true
	case "global":
		return prop{kind: propGlobal}, true
	case "user":
		return prop{kind: propUser}, true
	case "role":
		return prop{kind: propRole}, true
	}
	return prop{}, false
}

const (
	algSliding = iota
	algFixed
	algBucket
)

type cond struct {
	p      prop
	values []string
}

type rule struct {
	id, name    string
	monitor     bool
	action      string
	methods     []string
	paths       []string
	hasIPs      bool
	prefixes    []netip.Prefix
	countries   []string
	match       []cond
	key         prop
	limit       float64
	window      float64
	alg         int
	burst       float64
	limitBy     *prop
	limits      map[string]float64
	status      int
	body        string
	contentType string
	userPhase   bool
	counters    *counterSet
}

// shape is what a rule's counters depend on; a change resets them.
func (r *rule) shape() string {
	return r.key.name + "|" + strconv.Itoa(int(r.key.kind)) + "|" + strconv.FormatFloat(r.window, 'g', -1, 64) + "|" + strconv.Itoa(r.alg) + "|" + strconv.FormatFloat(r.burst, 'g', -1, 64)
}

type ruleSet struct {
	ipHeader       string
	countryHeaders []string
	request, user  []*rule
	byID           map[string]*rule
}

// compile turns a config into rules for one service; unusable rules are dropped.
func compile(c Config, service string) *ruleSet {
	rs := &ruleSet{ipHeader: strings.TrimSpace(c.IPHeader), byID: map[string]*rule{}}
	for _, h := range c.CountryHeaders {
		if h = strings.TrimSpace(h); h != "" {
			rs.countryHeaders = append(rs.countryHeaders, h)
		}
	}
	if len(rs.countryHeaders) == 0 {
		rs.countryHeaders = DefaultCountryHeaders
	}
	if !c.On() {
		return rs
	}
	for _, src := range c.Rules {
		r, ok := compileRule(src)
		if !ok || !src.On() || rs.byID[r.id] != nil || !servesService(src.Services, service) {
			continue
		}
		rs.byID[r.id] = r
		if r.userPhase {
			rs.user = append(rs.user, r)
		} else {
			rs.request = append(rs.request, r)
		}
	}
	return rs
}

func servesService(list []string, service string) bool {
	if len(list) == 0 {
		return true
	}
	if service == "" {
		return false
	}
	for _, s := range list {
		if s == service {
			return true
		}
	}
	return false
}

func compileRule(src Rule) (*rule, bool) {
	if !ValidID(src.ID) {
		return nil, false
	}
	r := &rule{id: src.ID, name: src.Name, action: src.Action, status: src.Status, body: src.Body, contentType: src.ContentType}
	if r.name == "" {
		r.name = r.id
	}
	switch src.Mode {
	case "", "enforce":
	case "monitor":
		r.monitor = true
	default:
		return nil, false
	}
	switch src.Action {
	case "limit", "block", "allow":
	default:
		return nil, false
	}
	for _, m := range src.Methods {
		r.methods = append(r.methods, strings.ToUpper(strings.TrimSpace(m)))
	}
	r.paths = src.Paths
	r.hasIPs = len(src.IPs) > 0
	for _, s := range src.IPs {
		if p, ok := parsePrefix(s); ok {
			r.prefixes = append(r.prefixes, p)
		}
	}
	for _, c := range src.Countries {
		r.countries = append(r.countries, strings.ToUpper(strings.TrimSpace(c)))
	}
	for name, values := range src.Match {
		p, ok := parseProp(name, false)
		if !ok || p.kind == propGlobal {
			return nil, false
		}
		r.match = append(r.match, cond{p: p, values: values})
		r.userPhase = r.userPhase || p.userPhase()
	}
	if src.Action != "limit" {
		return r, true
	}
	key := src.Key
	if key == "" {
		key = "ip"
	}
	k, ok := parseProp(key, true)
	if !ok {
		return nil, false
	}
	r.key = k
	r.userPhase = r.userPhase || k.userPhase()
	if src.WindowS < 1 || src.WindowS > MaxWindow {
		return nil, false
	}
	r.window, r.limit, r.burst = src.WindowS, src.Limit, src.Burst
	switch src.Algorithm {
	case "", "sliding":
		r.alg = algSliding
	case "fixed":
		r.alg = algFixed
	case "token_bucket":
		r.alg = algBucket
	default:
		return nil, false
	}
	if src.LimitBy != "" {
		p, ok := parseProp(src.LimitBy, false)
		if !ok || p.kind == propGlobal {
			return nil, false
		}
		r.limitBy = &p
		r.limits = src.Limits
		r.userPhase = r.userPhase || p.userPhase()
	}
	return r, true
}

// Request is what the limiter sees of a request. Header and Query return a
// header's or query parameter's first value ("" when absent).
type Request struct {
	Method   string
	Path     string
	RemoteIP string
	Header   func(name string) string
	Query    func(name string) string
}

// User is the request's user, known in the user phase.
type User struct{ ID, Role string }

// Denial is the answer for a request that is limited or blocked.
type Denial struct {
	Status      int
	RuleID      string
	RetryAfter  int // seconds; 0 for block
	Body        []byte
	ContentType string
}

// State carries one request between the two phases.
type State struct {
	set         *ruleSet
	ip, country string
	allowed     bool
	userDone    bool
	denial      *Denial
}

// Denied is the answer either phase gave, or nil.
func (s *State) Denied() *Denial {
	if s == nil {
		return nil
	}
	return s.denial
}

// Event is one rate_limit_events row.
type Event struct {
	CreatedAt                     time.Time
	RuleID, RuleName, Outcome     string
	KeyValue, IP, Country, Method string
	Path, UserID                  string
	Hits                          int64
}

// Limiter evaluates the rules in force and keeps the counters. Safe for
// concurrent use; a nil *Limiter lets everything through.
type Limiter struct {
	service string
	set     atomic.Pointer[ruleSet]

	loadMu sync.Mutex
	shapes map[string]string

	redactMu sync.RWMutex
	redact   map[string]bool

	aggMu sync.Mutex
	agg   map[string]*Event
	order []string

	// trace, when set, sees every recorded outcome (tests).
	trace func(ruleID, outcome string)
}

// New returns a limiter for a service (VISIONARY_SERVICE) with no rules.
func New(service string) *Limiter {
	l := &Limiter{service: service, shapes: map[string]string{}, agg: map[string]*Event{}}
	l.SetRedactHeaders([]string{"Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie", "X-Api-Key", "X-Auth-Token"})
	return l
}

// SetRedactHeaders names the headers whose values are never recorded as keys.
func (l *Limiter) SetRedactHeaders(names []string) {
	m := map[string]bool{}
	for _, n := range names {
		m[strings.ToLower(strings.TrimSpace(n))] = true
	}
	l.redactMu.Lock()
	l.redact = m
	l.redactMu.Unlock()
}

// Load puts a parsed config in force, carrying over the counters of rules
// whose key, window, algorithm and burst did not change.
func (l *Limiter) Load(c Config) {
	if l == nil {
		return
	}
	l.loadMu.Lock()
	defer l.loadMu.Unlock()
	next := compile(c, l.service)
	old := l.set.Load()
	shapes := map[string]string{}
	for id, r := range next.byID {
		shapes[id] = r.shape()
		if old != nil && l.shapes[id] == shapes[id] && old.byID[id] != nil {
			r.counters = old.byID[id].counters
		} else {
			r.counters = newCounterSet()
		}
	}
	l.shapes = shapes
	l.set.Store(next)
}

// Clear removes every rule.
func (l *Limiter) Clear() { l.Load(Config{}) }

// Active reports whether any rule is in force.
func (l *Limiter) Active() bool {
	if l == nil {
		return false
	}
	s := l.set.Load()
	return s != nil && len(s.byID) > 0
}

// CheckRequest runs the request phase. The State goes to CheckUser later.
func (l *Limiter) CheckRequest(req *Request, now time.Time) (st *State) {
	st = &State{}
	if l == nil {
		return st
	}
	set := l.set.Load()
	if set == nil || len(set.byID) == 0 {
		return st
	}
	defer func() {
		if recover() != nil {
			st = &State{} // fail open
		}
	}()
	st.set = set
	st.ip = clientIP(req, set.ipHeader)
	st.country = country(req, set.countryHeaders)
	l.run(set.request, st, req, nil, nil, now)
	return st
}

// CheckUser runs the user phase once per request; later calls repeat the
// first outcome.
func (l *Limiter) CheckUser(st *State, req *Request, user User, attrs map[string]string, now time.Time) (d *Denial) {
	if l == nil || st == nil || st.set == nil {
		return nil
	}
	if st.userDone || st.allowed || st.denial != nil {
		return st.denial
	}
	st.userDone = true
	if len(st.set.user) == 0 {
		return nil
	}
	defer func() {
		if recover() != nil {
			d = nil
		}
	}()
	l.run(st.set.user, st, req, &user, attrs, now)
	return st.denial
}

func (l *Limiter) run(rules []*rule, st *State, req *Request, user *User, attrs map[string]string, now time.Time) {
	t := float64(now.UnixNano()) / 1e9
	for _, r := range rules {
		if !l.matches(r, st, req, user, attrs) {
			continue
		}
		switch r.action {
		case "allow":
			if !r.monitor {
				st.allowed = true
				return
			}
		case "block":
			if r.monitor {
				l.record(r, WouldBlock, "", st, req, user, now)
				continue
			}
			l.record(r, Blocked, "", st, req, user, now)
			st.denial = r.deny(0)
			return
		case "limit":
			key := value(r.key, st, req, user, attrs)
			if key == "" {
				continue
			}
			limit := r.limit
			if r.limitBy != nil {
				if n, ok := r.limits[value(*r.limitBy, st, req, user, attrs)]; ok {
					limit = n
				}
			}
			if limit < 0 {
				continue
			}
			retry, over := r.hit(key, limit, t)
			if !over {
				continue
			}
			if r.monitor {
				l.record(r, WouldLimit, key, st, req, user, now)
				continue
			}
			l.record(r, Limited, key, st, req, user, now)
			st.denial = r.deny(retry)
			return
		}
	}
}

func (l *Limiter) matches(r *rule, st *State, req *Request, user *User, attrs map[string]string) bool {
	if len(r.methods) > 0 && !contains(r.methods, strings.ToUpper(req.Method)) {
		return false
	}
	if len(r.paths) > 0 {
		ok := false
		for _, g := range r.paths {
			if Glob(g, req.Path) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if r.hasIPs && !inPrefixes(r.prefixes, st.ip) {
		return false
	}
	if len(r.countries) > 0 && (st.country == "" || !contains(r.countries, st.country)) {
		return false
	}
	for _, c := range r.match {
		v := value(c.p, st, req, user, attrs)
		if len(c.values) == 0 {
			if v == "" {
				return false
			}
		} else if !contains(c.values, v) {
			return false
		}
	}
	return true
}

func value(p prop, st *State, req *Request, user *User, attrs map[string]string) string {
	switch p.kind {
	case propIP:
		return st.ip
	case propCountry:
		return st.country
	case propMethod:
		return strings.ToUpper(req.Method)
	case propPath:
		return req.Path
	case propHeader:
		if req.Header != nil {
			return req.Header(p.name)
		}
	case propQuery:
		if req.Query != nil {
			return req.Query(p.name)
		}
	case propGlobal:
		return "*"
	case propUser:
		if user != nil {
			return user.ID
		}
	case propRole:
		if user != nil {
			return user.Role
		}
	case propAttr:
		return attrs[p.name]
	}
	return ""
}

func (r *rule) deny(retry int) *Denial {
	d := &Denial{Status: r.status, RuleID: r.id, RetryAfter: retry}
	if d.Status == 0 {
		d.Status = 429
		if r.action == "block" {
			d.Status = 403
		}
	}
	switch {
	case r.body != "":
		d.Body = []byte(strings.ReplaceAll(r.body, "{retry_after}", strconv.Itoa(retry)))
		d.ContentType = "text/plain; charset=utf-8"
		if b := strings.TrimLeft(r.body, " \t\r\n"); strings.HasPrefix(b, "{") || strings.HasPrefix(b, "[") {
			d.ContentType = "application/json"
		}
	case r.action == "block":
		d.Body = []byte(`{"error":{"code":"blocked","key":"` + r.id + `","message":"Forbidden"}}`)
		d.ContentType = "application/json"
	default:
		d.Body = []byte(`{"error":{"code":"rate_limited","key":"` + r.id + `","message":"Too many requests"}}`)
		d.ContentType = "application/json"
	}
	if r.contentType != "" {
		d.ContentType = r.contentType
	}
	return d
}

// counter is one key's state: (win, curr, prev) for fixed and sliding
// windows, (tokens, last) for the token bucket.
type counter struct {
	win          int64
	curr, prev   float64
	tokens, last float64
}

type counterSet struct {
	mu sync.Mutex
	m  map[string]*counter
}

func newCounterSet() *counterSet { return &counterSet{m: map[string]*counter{}} }

// hit takes one request on key's counter and reports whether it is over,
// with the seconds to wait.
func (r *rule) hit(key string, limit, t float64) (int, bool) {
	W := r.window
	if limit == 0 {
		return int(math.Ceil(W)), true
	}
	cs := r.counters
	cs.mu.Lock()
	defer cs.mu.Unlock()
	c := cs.m[key]
	if c == nil {
		if len(cs.m) >= MaxCounters {
			r.sweepLocked(t)
			if len(cs.m) >= MaxCounters {
				cs.m = map[string]*counter{}
			}
		}
		c = &counter{win: math.MinInt64, tokens: r.capacity(limit), last: t}
		cs.m[key] = c
	}
	if r.alg == algBucket {
		C, R := r.capacity(limit), limit/W
		if t > c.last {
			c.tokens = math.Min(C, c.tokens+(t-c.last)*R)
			c.last = t
		}
		if c.tokens < 1 {
			return max(1, int(math.Ceil((1-c.tokens)/R))), true
		}
		c.tokens--
		return 0, false
	}
	w := int64(math.Floor(t / W))
	switch {
	case c.win == math.MinInt64:
		c.prev, c.curr = 0, 0
	case w == c.win+1:
		c.prev, c.curr = c.curr, 0
	case w > c.win+1:
		c.prev, c.curr = 0, 0
	}
	c.win = max(c.win, w)
	over := false
	if r.alg == algFixed {
		over = c.curr >= limit
	} else {
		e := t - float64(w)*W
		over = c.prev*(W-e)/W+c.curr+1 > limit
	}
	if over {
		return max(1, int(math.Ceil(float64(w+1)*W-t))), true
	}
	c.curr++
	return 0, false
}

func (r *rule) capacity(limit float64) float64 {
	if r.burst > 0 {
		return r.burst
	}
	return limit
}

// sweepLocked drops idle counters; the caller holds the set's lock.
func (r *rule) sweepLocked(t float64) {
	w := int64(math.Floor(t / r.window))
	for k, c := range r.counters.m {
		if r.alg == algBucket {
			// the limit may differ per key; the static one is close enough to call it idle
			C := r.capacity(r.limit)
			if r.limit <= 0 || c.tokens+(t-c.last)*(r.limit/r.window) >= C {
				delete(r.counters.m, k)
			}
		} else if c.win < w-1 {
			delete(r.counters.m, k)
		}
	}
}

// Sweep drops idle counters of every rule; the SDK calls it at each poll.
func (l *Limiter) Sweep(now time.Time) {
	if l == nil {
		return
	}
	set := l.set.Load()
	if set == nil {
		return
	}
	t := float64(now.UnixNano()) / 1e9
	for _, r := range set.byID {
		if r.counters == nil {
			continue
		}
		r.counters.mu.Lock()
		r.sweepLocked(t)
		r.counters.mu.Unlock()
	}
}

func (l *Limiter) record(r *rule, outcome, key string, st *State, req *Request, user *User, now time.Time) {
	if l.trace != nil {
		l.trace(r.id, outcome)
	}
	if key != "" && r.key.kind == propHeader {
		l.redactMu.RLock()
		secret := l.redact[r.key.name]
		l.redactMu.RUnlock()
		if secret {
			sum := sha256.Sum256([]byte(key))
			key = "sha256:" + hex.EncodeToString(sum[:])[:12]
		}
	}
	key = clip(key, 200)
	ip := st.ip
	l.aggMu.Lock()
	defer l.aggMu.Unlock()
	id := r.id + "\x00" + outcome + "\x00" + key + "\x00" + ip
	e := l.agg[id]
	if e == nil && len(l.agg) >= MaxAggregates {
		key, ip = "(other)", ""
		id = r.id + "\x00" + outcome + "\x00" + key + "\x00"
		e = l.agg[id]
	}
	if e == nil {
		e = &Event{CreatedAt: now, RuleID: r.id, RuleName: r.name, Outcome: outcome, KeyValue: key, IP: ip,
			Country: st.country, Method: strings.ToUpper(req.Method), Path: clip(req.Path, 300)}
		if user != nil {
			e.UserID = user.ID
		}
		l.agg[id] = e
		l.order = append(l.order, id)
	}
	e.Hits++
}

// Drain returns the outcomes recorded since the last call, oldest first.
func (l *Limiter) Drain() []Event {
	if l == nil {
		return nil
	}
	l.aggMu.Lock()
	defer l.aggMu.Unlock()
	out := make([]Event, 0, len(l.order))
	for _, id := range l.order {
		out = append(out, *l.agg[id])
	}
	l.agg, l.order = map[string]*Event{}, nil
	return out
}

func clientIP(req *Request, header string) string {
	if header != "" && req.Header != nil {
		if v := req.Header(strings.ToLower(header)); v != "" {
			first, _, _ := strings.Cut(v, ",")
			if first = strings.TrimSpace(first); first != "" {
				return first
			}
		}
	}
	return req.RemoteIP
}

func country(req *Request, headers []string) string {
	if req.Header == nil {
		return ""
	}
	for _, h := range headers {
		if v := strings.TrimSpace(req.Header(strings.ToLower(h))); v != "" {
			return strings.ToUpper(v)
		}
	}
	return ""
}

func inPrefixes(list []netip.Prefix, ip string) bool {
	a, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return false
	}
	a = a.Unmap().WithZone("")
	for _, p := range list {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Glob matches a whole path against a pattern where * is any run of characters.
func Glob(pattern, s string) bool {
	p, i := 0, 0
	star, mark := -1, 0
	for i < len(s) {
		switch {
		case p < len(pattern) && pattern[p] == '*':
			star, mark = p, i
			p++
		case p < len(pattern) && pattern[p] == s[i]:
			p++
			i++
		case star >= 0:
			p = star + 1
			mark++
			i = mark
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
