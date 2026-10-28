package visionary

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Application logs (app_logs): lines the service logs through slog (see
// LogHandler) or the standard log package (see CaptureStdLog), linked to the
// request whose context they were logged with.
//
// What is kept follows the dashboard (Settings → Logs), read from
// visionary_settings every 30 seconds like the profiling switch:
//
//   - logs.level: lines below it are not stored...
//   - ...except inside a request that failed (5xx, a panic, a captured
//     error) or was slow (logs.slow_ms): there the lines down to
//     logs.buffer_level that were held back are written too, so every
//     failure comes with its debug lines (logs.buffer).
//   - logs.overrides lower or raise the level for a service, a route or a
//     user until a time, without a redeploy.
//   - logs.drop drops matching lines; logs.rate caps lines per minute.
//
// Lines below the level are held in the request until it ends and then
// written or dropped; nothing on a request path waits for the database.

// Severities as stored in app_logs.severity, with their level names.
const (
	sevTrace = 1 + iota
	sevDebug
	sevInfo
	sevWarn
	sevError
	sevFatal
)

var levelNames = [...]string{"", "trace", "debug", "info", "warn", "error", "fatal"}

func levelName(sev int) string {
	if sev < sevTrace || sev > sevFatal {
		return "info"
	}
	return levelNames[sev]
}

// ParseLevel reads a level name the way every source spells them: trace,
// debug, info, notice, warn, warning, error, err, critical, fatal, panic...
// Unknown names are 0.
func ParseLevel(s string) int {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "trace", "trc", "finest", "finer", "verbose", "vrb":
		return sevTrace
	case "debug", "dbg", "fine", "config":
		return sevDebug
	case "info", "inf", "information", "informational", "notice", "log":
		return sevInfo
	case "warn", "warning", "wrn":
		return sevWarn
	case "error", "err", "eror", "severe":
		return sevError
	case "fatal", "critical", "crit", "alert", "emerg", "emergency", "panic", "ftl":
		return sevFatal
	}
	return 0
}

const (
	maxLogMessage = 8 << 10
	maxLogAttrs   = 16 << 10
	// maxHeldLogs bounds the lines one request holds back; past it, the
	// oldest held lines are dropped.
	maxHeldLogs = 500
	logSettingsPoll = 30 * time.Second
)

// logHost is the machine's name, for the host column.
var logHost = hostname()

var logColumns = []string{
	"created_at", "service", "instance", "version", "source", "host", "container", "level", "severity", "logger",
	"message", "attrs", "request_id", "trace_id", "span_id", "user_id", "session_id", "code_file", "code_line",
	"code_func", "size",
}

// logRecord is one captured line. Redaction and encoding happen on the
// writer goroutine.
type logRecord struct {
	at        time.Time
	sev       int
	logger    string
	message   string
	attrs     map[string]any
	requestID string
	traceID   string
	spanID    string
	userID    string
	sessionID string
	file      string
	line      int
	fn        string
}

// logOverride changes the level for part of the traffic until a time.
type logOverride struct {
	Service string `json:"service"`
	Route   string `json:"route"`
	User    string `json:"user"`
	Level   string `json:"level"`
	Until   string `json:"until"`

	sev   int
	until time.Time
}

// logDrop drops the lines it matches; empty fields match anything, and Level
// (when set) drops only lines at or below it.
type logDrop struct {
	Service  string `json:"service"`
	Logger   string `json:"logger"`
	Contains string `json:"contains"`
	Level    string `json:"level"`

	sev int
}

// logSettings is what the dashboard asked for.
type logSettings struct {
	enabled     bool
	level       int
	buffer      bool
	bufferLevel int
	slow        time.Duration
	rate        int
	overrides   []logOverride
	drops       []logDrop
}

func defaultLogSettings(level int) *logSettings {
	if level == 0 {
		level = sevInfo
	}
	return &logSettings{enabled: true, level: level, buffer: true, bufferLevel: sevDebug, slow: time.Second, rate: 6000}
}

// parseLogSettings reads (name, value) rows of logs.*; anything missing or
// unreadable keeps its default.
func parseLogSettings(rows [][]string, base int) *logSettings {
	s := defaultLogSettings(base)
	for _, r := range rows {
		if len(r) < 2 {
			continue
		}
		v := strings.TrimSpace(r[1])
		switch r[0] {
		case "logs.enabled":
			if b, err := strconv.ParseBool(v); err == nil {
				s.enabled = b
			}
		case "logs.level":
			if n := ParseLevel(v); n > 0 {
				s.level = n
			}
		case "logs.buffer":
			if b, err := strconv.ParseBool(v); err == nil {
				s.buffer = b
			}
		case "logs.buffer_level":
			if n := ParseLevel(v); n > 0 {
				s.bufferLevel = n
			}
		case "logs.slow_ms":
			if n, err := strconv.ParseFloat(v, 64); err == nil && n > 0 {
				s.slow = time.Duration(n * float64(time.Millisecond))
			}
		case "logs.rate":
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				s.rate = n
			}
		case "logs.overrides":
			var list []logOverride
			if json.Unmarshal([]byte(v), &list) == nil {
				for _, o := range list {
					if o.sev = ParseLevel(o.Level); o.sev == 0 {
						continue
					}
					if o.Until != "" {
						t, ok := parseTime(o.Until)
						if !ok {
							continue
						}
						o.until = t
					}
					s.overrides = append(s.overrides, o)
				}
			}
		case "logs.drop":
			var list []logDrop
			if json.Unmarshal([]byte(v), &list) == nil {
				for _, d := range list {
					d.sev = ParseLevel(d.Level)
					if d.Service == "" && d.Logger == "" && d.Contains == "" && d.sev == 0 {
						continue
					}
					s.drops = append(s.drops, d)
				}
			}
		}
	}
	return s
}

func parseTime(v string) (time.Time, bool) {
	for _, l := range nowLayouts {
		if t, err := time.Parse(l, v); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// active reports whether o applies now to service.
func (o *logOverride) active(service string, now time.Time) bool {
	if !o.until.IsZero() && now.After(o.until) {
		return false
	}
	return o.Service == "" || o.Service == service
}

// matches reports whether o applies to a request on route by user. An
// override with neither applies to everything in its service.
func (o *logOverride) matches(route, user string) bool {
	if o.Route != "" && !routeMatches(o.Route, route) {
		return false
	}
	if o.User != "" && o.User != user {
		return false
	}
	return true
}

// routeMatches compares a route pattern, allowing a trailing * for prefixes.
func routeMatches(pattern, route string) bool {
	if p, ok := strings.CutSuffix(pattern, "*"); ok {
		return strings.HasPrefix(route, p)
	}
	return pattern == route
}

// floor is the lowest level kept outright for a line outside any request,
// or once its request is known: the base level, then service-wide
// overrides, then the most specific matching override (a user beats a
// route). Later overrides win among equals.
func (s *logSettings) floor(service, route, user string, now time.Time) int {
	level, rank := s.level, 0
	for i := range s.overrides {
		o := &s.overrides[i]
		if !o.active(service, now) || !o.matches(route, user) {
			continue
		}
		r := 1
		if o.Route != "" {
			r = 2
		}
		if o.User != "" {
			r = 3
		}
		if r >= rank {
			level, rank = o.sev, r
		}
	}
	return level
}

// holdBelow is the level under which a line logged in a request is held
// until the request ends: everything a route or user override could keep,
// and everything the buffer might keep.
func (s *logSettings) holdBelow(service string, now time.Time) int {
	level := s.level
	for i := range s.overrides {
		o := &s.overrides[i]
		if o.active(service, now) && o.Route == "" && o.User == "" {
			level = o.sev
		}
	}
	for i := range s.overrides {
		o := &s.overrides[i]
		if o.active(service, now) && (o.Route != "" || o.User != "") {
			level = max(level, o.sev)
		}
	}
	return level
}

func (s *logSettings) dropped(service string, r *logRecord) bool {
	for _, d := range s.drops {
		if d.Service != "" && d.Service != service {
			continue
		}
		if d.Logger != "" && d.Logger != r.logger {
			continue
		}
		if d.sev != 0 && r.sev > d.sev {
			continue
		}
		if d.Contains != "" && !strings.Contains(r.message, d.Contains) {
			continue
		}
		return true
	}
	return false
}

// heldLogs are a request's lines below its level, kept until it ends.
type heldLogs struct {
	lines   []*logRecord
	dropped int
}

// logger is the Agent's log pipeline.
type logger struct {
	a      *Agent
	w      *writer
	base   int
	set    atomic.Pointer[logSettings]

	// The per-minute cap: lines counted in the current minute, and those
	// dropped by it, reported once the minute ends.
	rateMu     sync.Mutex
	rateMinute int64
	rateCount  int
	rateDrop   int

	stop chan struct{}
	done chan struct{}
	once sync.Once
}

func newLogger(a *Agent, w *writer, base int) *logger {
	l := &logger{a: a, w: w, base: base, stop: make(chan struct{}), done: make(chan struct{})}
	l.set.Store(defaultLogSettings(base))
	return l
}

// secretKeys are the attribute names whose values are never stored.
func secretKeys(extra []string) map[string]bool {
	m := map[string]bool{}
	for _, f := range append(append([]string{}, defaultRedactFields...), extra...) {
		if f = strings.ToLower(strings.TrimSpace(f)); f != "" {
			m[f] = true
		}
	}
	return m
}

func (l *logger) settings() *logSettings { return l.set.Load() }

// start polls the dashboard's settings until ctx ends or close.
func (l *logger) start(ctx context.Context) {
	go func() {
		defer close(l.done)
		failed := false
		t := time.NewTimer(0)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-l.stop:
				return
			case <-t.C:
			}
			qctx, cancel := context.WithTimeout(quiet(ctx), 15*time.Second)
			rows, err := l.a.db.rows(qctx, "SELECT name, value FROM visionary_settings WHERE name LIKE 'logs.%'")
			cancel()
			switch {
			case err != nil:
				// No server yet, or the table is missing: keep what we had.
				if !failed {
					l.a.cfg.Logger.Debug("visionary: log settings unavailable; using defaults", "err", err)
					failed = true
				}
			default:
				failed = false
				l.set.Store(parseLogSettings(rows, l.base))
			}
			t.Reset(logSettingsPoll)
		}
	}()
}

func (l *logger) close() {
	l.once.Do(func() { close(l.stop) })
}

// capture takes one line logged with ctx. It never blocks.
func (l *logger) capture(ctx context.Context, r *logRecord) {
	s := l.settings()
	// The SDK's own messages are left out: a write failure logged here
	// would otherwise be queued behind the write that failed.
	if !s.enabled || isQuiet(ctx) || strings.HasPrefix(r.message, "visionary: ") {
		return
	}
	service := l.a.ident.service
	if s.dropped(service, r) {
		return
	}
	ann, _ := ctx.Value(ctxKey{}).(*annotations)
	if ann == nil {
		if r.sev >= s.floor(service, "", "", r.at) {
			l.emit(r)
		}
		return
	}
	ann.mu.Lock()
	if r.requestID == "" {
		r.requestID = ann.requestID
	}
	if r.traceID == "" {
		r.traceID = ann.traceID
	}
	if r.userID == "" {
		r.userID = ann.user.ID
	}
	if ann.done {
		// Background work the request left running: its route and user are
		// known now.
		route, user := ann.route, ann.user.ID
		ann.mu.Unlock()
		if r.sev >= s.floor(service, route, user, r.at) {
			l.emit(r)
		}
		return
	}
	if r.sev >= s.holdBelow(service, r.at) {
		ann.mu.Unlock()
		l.emit(r)
		return
	}
	if ann.logs == nil {
		ann.logs = &heldLogs{}
	}
	h := ann.logs
	if len(h.lines) >= maxHeldLogs {
		h.lines = slices.Delete(h.lines, 0, 1)
		h.dropped++
	}
	h.lines = append(h.lines, r)
	ann.mu.Unlock()
}

// finish decides a request's held lines once it has ended: kept when they
// reach the level for its route and user, or, when the request failed or
// was slow, the buffer's level.
func (l *logger) finish(ann *annotations, route string, failed bool, took time.Duration) {
	ann.mu.Lock()
	h := ann.logs
	ann.logs = nil
	user := ann.user.ID
	ann.mu.Unlock()
	if h == nil || len(h.lines) == 0 {
		return
	}
	s := l.settings()
	now := time.Now()
	floor := s.floor(l.a.ident.service, route, user, now)
	keepAll := s.buffer && (failed || (s.slow > 0 && took >= s.slow))
	for _, r := range h.lines {
		if r.userID == "" {
			r.userID = user
		}
		if r.sev >= floor || (keepAll && r.sev >= s.bufferLevel) {
			l.emit(r)
		}
	}
}

// emit queues a line, within the per-minute cap. Errors and fatals pass the
// cap up to ten times over, so a flood of info lines never hides them.
func (l *logger) emit(r *logRecord) {
	s := l.settings()
	if s.rate > 0 {
		minute := r.at.Unix() / 60
		l.rateMu.Lock()
		if minute != l.rateMinute {
			if l.rateDrop > 0 {
				drop := l.rateDrop
				l.rateMu.Unlock()
				l.w.enqueue(&logRecord{at: time.Now(), sev: sevWarn, logger: "visionary",
					message: fmt.Sprintf("visionary: %d log lines dropped over the cap of %d a minute (Settings → Logs)", drop, s.rate)})
				l.rateMu.Lock()
			}
			l.rateMinute, l.rateCount, l.rateDrop = minute, 0, 0
		}
		l.rateCount++
		over := l.rateCount > s.rate && (r.sev < sevError || l.rateCount > 10*s.rate)
		if over {
			l.rateDrop++
		}
		l.rateMu.Unlock()
		if over {
			return
		}
	}
	l.w.enqueue(r)
}

// logParams returns the row in logColumns order.
func (w *writer) logParams(r *logRecord) []any {
	msg := truncate(scrubSecrets(r.message), maxLogMessage)
	attrs := ""
	if len(r.attrs) > 0 {
		attrs = encodeAttrs(r.attrs, w.logSecrets)
	}
	var line any
	if r.line > 0 {
		line = r.line
	}
	return []any{
		r.at.UTC().Format(tsLayout),
		nullable(w.ident.service),
		nullable(w.ident.instance),
		nullable(w.ident.version),
		"go",
		nullable(logHost),
		nil,
		levelName(r.sev),
		r.sev,
		nullable(r.logger),
		msg,
		nullable(attrs),
		nullable(r.requestID),
		nullable(r.traceID),
		nullable(r.spanID),
		nullable(r.userID),
		nullable(r.sessionID),
		nullable(r.file),
		line,
		nullable(r.fn),
		len(msg) + len(attrs),
	}
}

// encodeAttrs writes attrs as one flat JSON object, secrets redacted, at
// most maxLogAttrs bytes: long values are cut first, then keys dropped.
func encodeAttrs(attrs map[string]any, secret map[string]bool) string {
	out := make(map[string]any, len(attrs))
	for k, v := range attrs {
		if secret[strings.ToLower(lastKey(k))] {
			out[k] = redacted
			continue
		}
		if s, ok := v.(string); ok {
			v = scrubSecrets(s)
		}
		out[k] = v
	}
	b, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	if len(b) <= maxLogAttrs {
		return string(b)
	}
	for k, v := range out {
		if s, ok := v.(string); ok && len(s) > 1024 {
			out[k] = truncate(s, 1024) + "…"
		}
	}
	if b, err = json.Marshal(out); err == nil && len(b) <= maxLogAttrs {
		return string(b)
	}
	keys := make([]string, 0, len(out))
	for k := range out {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	kept := map[string]any{"_truncated": true}
	for _, k := range keys {
		kept[k] = out[k]
		if b, _ := json.Marshal(kept); len(b) > maxLogAttrs {
			delete(kept, k)
			break
		}
	}
	b, _ = json.Marshal(kept)
	return string(b)
}

func lastKey(k string) string {
	if i := strings.LastIndexByte(k, '.'); i >= 0 {
		return k[i+1:]
	}
	return k
}

// Secrets in free text, replaced before a line is stored. Every SDK uses
// these same patterns (see SPEC.md, Logs).
var secretPatterns = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(-----END [A-Z ]*PRIVATE KEY-----|$)`), "[REDACTED:private-key]"},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}`), "[REDACTED:jwt]"},
	{regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`), "$1 [REDACTED]"},
	{regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`), "[REDACTED:aws-key]"},
	{regexp.MustCompile(`(?i)\b(password|passwd|pwd|secret|token|api[_-]?key|access[_-]?token|refresh[_-]?token|client[_-]?secret)\b(["']?\s*[=:]\s*)("[^"]*"|'[^']*'|[^\s,;&"']+)`), "$1$2[REDACTED]"},
}

// scrubSecrets replaces credentials in s.
func scrubSecrets(s string) string {
	for _, p := range secretPatterns {
		if p.re.MatchString(s) {
			s = p.re.ReplaceAllString(s, p.with)
		}
	}
	return s
}

// textLevel finds the level a plain-text line states at its start, the
// way loggers print it: "ERROR ...", "[warn] ...", "error: ...",
// "2026-10-28 09:30:12 WARN ...", or a level=/lvl= field anywhere in the
// first 200 bytes. A bare lowercase word ("error rate is low") is not a
// level. 0 when the line states none.
func textLevel(s string) int {
	head := s[:min(len(s), 200)]
	if m := levelFieldRE.FindStringSubmatch(head); m != nil {
		if n := ParseLevel(m[1]); n > 0 {
			return n
		}
	}
	fields := strings.Fields(head)
	for i, f := range fields {
		if i > 3 {
			break
		}
		if timestampRE.MatchString(f) {
			continue
		}
		word := strings.Trim(f, "[]<>():|")
		n := ParseLevel(word)
		if n == 0 {
			return 0
		}
		marked := strings.HasPrefix(f, "[") || strings.HasPrefix(f, "<") || strings.HasPrefix(f, "(") ||
			strings.HasSuffix(f, ":") || strings.HasSuffix(f, "]") || strings.HasSuffix(f, "|")
		if marked || word == strings.ToUpper(word) {
			return n
		}
		return 0
	}
	return 0
}

var (
	levelFieldRE = regexp.MustCompile(`(?i)\b(?:level|lvl|severity)=["']?([a-z]+)`)
	// timestampRE matches a date, a time or both, as loggers print them.
	timestampRE = regexp.MustCompile(`^[\[(]?[0-9][0-9/:.,\-TZ+]*[\])]?$`)
)
