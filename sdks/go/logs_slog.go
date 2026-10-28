package visionary

import (
	"bytes"
	"context"
	"io"
	"log"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"time"
)

// LogHandler wraps an slog.Handler so every record also goes to Visionary's
// app_logs, linked to the request when it was logged with the request's
// context (slog.InfoContext(r.Context(), ...)). next still gets exactly what
// it got before, at its own level; Visionary sees lower levels too, so the
// buffer can keep a failed request's debug lines.
//
//	slog.SetDefault(slog.New(visionary.LogHandler(slog.NewJSONHandler(os.Stdout, nil))))
//
// next may be nil to send records only to Visionary. Records are captured by
// the Agent that handles the request in ctx, else the most recently started
// one; before any Agent starts they only reach next.
func LogHandler(next slog.Handler) slog.Handler {
	return &slogHandler{next: next}
}

type slogHandler struct {
	next   slog.Handler
	attrs  []slog.Attr // from WithAttrs, already prefixed by their groups
	groups string      // "a.b." for WithGroup("a").WithGroup("b")
}

func (h *slogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	if h.next != nil && h.next.Enabled(ctx, level) {
		return true
	}
	_, a := fromContext(ctx)
	return a != nil && a.logs != nil && a.logs.settings().enabled
}

func (h *slogHandler) Handle(ctx context.Context, r slog.Record) error {
	var err error
	if h.next != nil && h.next.Enabled(ctx, r.Level) {
		err = h.next.Handle(ctx, r)
	}
	if _, a := fromContext(ctx); a != nil && a.logs != nil {
		a.logs.capture(ctx, h.record(r))
	}
	return err
}

func (h *slogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	c := *h
	if h.next != nil {
		c.next = h.next.WithAttrs(attrs)
	}
	c.attrs = append(slices0(h.attrs), prefixAttrs(h.groups, attrs)...)
	return &c
}

func (h *slogHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	c := *h
	if h.next != nil {
		c.next = h.next.WithGroup(name)
	}
	c.groups = h.groups + name + "."
	return &c
}

func slices0(a []slog.Attr) []slog.Attr { return append([]slog.Attr(nil), a...) }

func prefixAttrs(prefix string, attrs []slog.Attr) []slog.Attr {
	if prefix == "" {
		return attrs
	}
	out := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		out[i] = slog.Attr{Key: prefix + a.Key, Value: a.Value}
	}
	return out
}

// slogSev maps slog's levels (Debug -4, Info 0, Warn 4, Error 8) and the
// steps between them onto the stored severities.
func slogSev(l slog.Level) int {
	switch {
	case l < slog.LevelDebug:
		return sevTrace
	case l < slog.LevelInfo:
		return sevDebug
	case l < slog.LevelWarn:
		return sevInfo
	case l < slog.LevelError:
		return sevWarn
	case l < slog.LevelError+4:
		return sevError
	}
	return sevFatal
}

func (h *slogHandler) record(r slog.Record) *logRecord {
	rec := &logRecord{at: r.Time, sev: slogSev(r.Level), message: r.Message}
	if rec.at.IsZero() {
		rec.at = time.Now()
	}
	attrs := map[string]any{}
	for _, a := range h.attrs {
		addAttr(attrs, "", a)
	}
	r.Attrs(func(a slog.Attr) bool {
		addAttr(attrs, h.groups, a)
		return true
	})
	rec.take(attrs)
	if r.PC != 0 {
		f, _ := runtime.CallersFrames([]uintptr{r.PC}).Next()
		rec.file, rec.line, rec.fn = codeFile(f.File, f.Function), f.Line, shortFunc(f.Function)
	}
	return rec
}

// addAttr flattens a (possibly grouped) attribute into dotted keys.
func addAttr(out map[string]any, prefix string, a slog.Attr) {
	v := a.Value.Resolve()
	if v.Kind() == slog.KindGroup {
		p := prefix
		if a.Key != "" {
			p += a.Key + "."
		}
		for _, g := range v.Group() {
			addAttr(out, p, g)
		}
		return
	}
	if a.Key == "" {
		return
	}
	out[prefix+a.Key] = attrValue(v)
}

func attrValue(v slog.Value) any {
	switch v.Kind() {
	case slog.KindString:
		return v.String()
	case slog.KindInt64:
		return v.Int64()
	case slog.KindUint64:
		return v.Uint64()
	case slog.KindFloat64:
		return v.Float64()
	case slog.KindBool:
		return v.Bool()
	case slog.KindDuration:
		return v.Duration().String()
	case slog.KindTime:
		return v.Time().UTC().Format(time.RFC3339Nano)
	}
	if err, ok := v.Any().(error); ok {
		return err.Error()
	}
	return v.String()
}

// take moves the attributes that have columns of their own (request_id,
// trace_id, span_id, user_id, session_id, logger) out of attrs.
func (r *logRecord) take(attrs map[string]any) {
	for _, k := range []string{"request_id", "trace_id", "span_id", "user_id", "session_id", "logger"} {
		v, ok := attrs[k]
		if !ok {
			continue
		}
		s, ok := v.(string)
		if !ok {
			continue
		}
		switch k {
		case "request_id":
			r.requestID = s
		case "trace_id":
			r.traceID = s
		case "span_id":
			r.spanID = s
		case "user_id":
			r.userID = s
		case "session_id":
			r.sessionID = s
		case "logger":
			r.logger = s
		}
		delete(attrs, k)
	}
	if len(attrs) > 0 {
		r.attrs = attrs
	}
}

// CaptureStdLog copies every line written through the standard log package
// (log.Printf, log.Println, and slog's default handler, which writes through
// it) to Visionary, without changing what the application prints. Lines
// carry no request context; a level is read from the text when it states one
// ("ERROR ...", "[warn]", "level=debug"), else they are info.
//
// Call it after Start. It returns a function that puts the previous writer
// back.
func (a *Agent) CaptureStdLog() (restore func()) {
	if a == nil || a.logs == nil {
		return func() {}
	}
	prev := log.Writer()
	log.SetOutput(io.MultiWriter(prev, &stdLogWriter{a: a, flags: log.Flags(), prefix: log.Prefix()}))
	return func() { log.SetOutput(prev) }
}

// stdLogWriter turns log package output into records, one per line.
type stdLogWriter struct {
	a      *Agent
	flags  int
	prefix string
	mu     sync.Mutex
	rest   []byte // a line not yet ended
}

// Write gets one log call's output at a time, ending in a newline; a call
// that prints several lines (a stack trace) stays one record.
func (w *stdLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.rest = append(w.rest, p...)
	var out []byte
	if i := bytes.LastIndexByte(w.rest, '\n'); i >= 0 {
		out = append([]byte(nil), w.rest[:i]...)
		w.rest = w.rest[i+1:]
	} else if len(w.rest) > maxLogMessage {
		out, w.rest = w.rest, nil
	}
	w.mu.Unlock()
	if out != nil {
		w.line(string(out))
	}
	return len(p), nil
}

func (w *stdLogWriter) line(s string) {
	s = strings.TrimRight(s, "\r\n")
	s = strings.TrimPrefix(s, w.prefix)
	s = stripStdHeader(s, w.flags)
	if strings.TrimSpace(s) == "" {
		return
	}
	rec := &logRecord{at: time.Now(), sev: sevInfo, message: s}
	if sev := textLevel(s); sev > 0 {
		rec.sev = sev
		// slog's default handler prints "INFO msg k=v" through log: the
		// level has its own column.
		if word, rest, ok := strings.Cut(s, " "); ok && strings.TrimSpace(rest) != "" {
			w := strings.ToLower(strings.TrimSuffix(word, ":"))
			if word == strings.ToUpper(word) && (w == levelName(sev) || w == "warning") {
				rec.message = rest
			}
		}
	}
	if f, ok := callerOutside("log", "log/slog", "io"); ok {
		rec.file, rec.line, rec.fn = codeFile(f.File, f.Function), f.Line, shortFunc(f.Function)
	}
	w.a.logs.capture(context.Background(), rec)
}

// stripStdHeader removes the date, time and file the log flags put before
// the message: "2026/10/28 09:30:12.123456 main.go:42: ".
func stripStdHeader(s string, flags int) string {
	if flags&(log.Ldate|log.Ltime|log.Lmicroseconds) != 0 {
		n := 0
		if flags&log.Ldate != 0 {
			n++
		}
		if flags&(log.Ltime|log.Lmicroseconds) != 0 {
			n++
		}
		for range n {
			if i := strings.IndexByte(s, ' '); i >= 0 {
				s = s[i+1:]
			}
		}
	}
	if flags&(log.Lshortfile|log.Llongfile) != 0 {
		if i := strings.Index(s, ": "); i >= 0 {
			s = s[i+2:]
		}
	}
	return s
}

// callerOutside finds the first frame of the application above the log
// machinery: outside the SDK, the runtime and the given packages.
func callerOutside(pkgs ...string) (runtime.Frame, bool) {
	pcs := make([]uintptr, 24)
	n := runtime.Callers(3, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	for {
		f, more := frames.Next()
		pkg := funcPackage(f.Function)
		if !isSDKFrame(f) && pkg != "runtime" && !containsStr(pkgs, pkg) {
			return f, f.Function != ""
		}
		if !more {
			return runtime.Frame{}, false
		}
	}
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

var logNamer = sync.OnceValue(newFrameNamer)

// codeFile names a source file the way CPU profiles do: relative to the
// project for the application's own code, lib:/std: for the rest.
func codeFile(path, fn string) string {
	return logNamer().file(path, fn)
}

// shortFunc drops the import path: "github.com/a/b/pkg.(*T).Method" →
// "pkg.(*T).Method".
func shortFunc(fn string) string {
	if i := strings.LastIndexByte(fn, '/'); i >= 0 {
		return fn[i+1:]
	}
	return fn
}
