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

	"github.com/styropyr0/visionary/sdks/go/logrules"
)

// LogHandler wraps next so every record also goes to Visionary, linked to the
// request when logged with its context. next still gets exactly what it did
// before; it may be nil to send records only to Visionary.
//
//	slog.SetDefault(slog.New(visionary.LogHandler(slog.NewJSONHandler(os.Stdout, nil))))
func LogHandler(next slog.Handler) slog.Handler {
	return &slogHandler{next: next}
}

type slogHandler struct {
	next   slog.Handler
	attrs  []slog.Attr
	groups string // "a.b." for WithGroup("a").WithGroup("b")
}

func (h *slogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	if h.next != nil && h.next.Enabled(ctx, level) {
		return true
	}
	_, a := fromContext(ctx)
	return a != nil && a.logs != nil && a.logs.settings().Enabled
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

// slog levels are Debug -4, Info 0, Warn 4, Error 8, with steps between.
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

// take moves attributes that have their own columns (request_id, user_id...) out of attrs.
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

// CaptureStdLog copies lines written through the standard log package to
// Visionary without changing what is printed. Call it after Start; the
// returned func puts the previous writer back.
func (a *Agent) CaptureStdLog() (restore func()) {
	if a == nil || a.logs == nil {
		return func() {}
	}
	prev := log.Writer()
	log.SetOutput(io.MultiWriter(prev, &stdLogWriter{a: a, flags: log.Flags(), prefix: log.Prefix()}))
	return func() { log.SetOutput(prev) }
}

type stdLogWriter struct {
	a      *Agent
	flags  int
	prefix string
	mu     sync.Mutex
	rest   []byte
}

// A log call that prints several lines (a stack trace) stays one record.
func (w *stdLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.rest = append(w.rest, p...)
	var out []byte
	if i := bytes.LastIndexByte(w.rest, '\n'); i >= 0 {
		out = append([]byte(nil), w.rest[:i]...)
		w.rest = w.rest[i+1:]
	} else if len(w.rest) > logrules.MaxMessage {
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
	if sev := logrules.TextLevel(s); sev > 0 {
		// slog's default handler prints "INFO msg k=v" through log.
		rec.sev, rec.message = sev, logrules.StripLevelWord(s, sev)
	}
	if f, ok := callerOutside("log", "log/slog", "io"); ok {
		rec.file, rec.line, rec.fn = codeFile(f.File, f.Function), f.Line, shortFunc(f.Function)
	}
	w.a.logs.capture(context.Background(), rec)
}

// stripStdHeader removes the log flags' prefix: "2026/10/28 09:30:12.123456 main.go:42: ".
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

// callerOutside skips frames in the SDK, the runtime and the given packages.
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

// codeFile names files like CPU profiles do: project-relative, else lib:/std:.
func codeFile(path, fn string) string {
	return logNamer().file(path, fn)
}

func shortFunc(fn string) string {
	if i := strings.LastIndexByte(fn, '/'); i >= 0 {
		return fn[i+1:]
	}
	return fn
}
