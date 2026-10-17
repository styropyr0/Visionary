package visionary

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"reflect"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

var errorColumns = []string{
	"created_at", "service", "instance", "version", "request_id", "method", "route", "path", "user_id",
	"type", "message", "stack", "fingerprint", "handled", "runtime",
}

const (
	errorsPerMinute    = 20
	maxStackBytes      = 16 << 10
	maxErrorsInRequest = 100
	fingerprintFrames  = 5
)

// errorRecord is one captured error, linked to its request when it has one.
type errorRecord struct {
	at                                     time.Time
	requestID, method, route, path, userID string
	typ, message, stack, fingerprint       string
	handled                                bool
}

// CaptureError records a handled error: one the application recovered from
// but wants to see, with a stack trace of where it was captured. Inside a
// request (any context derived from it) it links to that request. The type
// is the innermost error of the Unwrap chain, e.g. *pgconn.PgError.
//
//	if err := h.store.Save(ctx, trip); err != nil {
//		visionary.CaptureError(ctx, err)
//		return writeError(w, err)
//	}
//
// Safe from any goroutine; a no-op when error capture is off or no Agent
// runs. The same error is stored at most 20 times a minute.
func CaptureError(ctx context.Context, err error) {
	if err == nil {
		return
	}
	ann, a := fromContext(ctx)
	if !a.errorsOn() {
		return
	}
	if rec := a.newError(rootType(err), err.Error(), true); rec != nil {
		a.addError(ann, rec)
	}
}

// capturePanic records a panic that reached Middleware. It runs in the
// deferred function, while the panicking frames are still on the stack.
func (a *Agent) capturePanic(ann *annotations, v any) {
	if !a.errorsOn() {
		return
	}
	typ, msg := fmt.Sprintf("%T", v), fmt.Sprint(v)
	if err, ok := v.(error); ok {
		typ, msg = rootType(err), err.Error()
	}
	if rec := a.newError(typ, msg, false); rec != nil {
		a.addError(ann, rec)
	}
}

func (a *Agent) errorsOn() bool { return a != nil && a.cfg.Errors && a.writer != nil }

// newError builds the record from the current goroutine's stack, or returns
// nil when this fingerprint is over its rate limit.
func (a *Agent) newError(typ, msg string, handled bool) *errorRecord {
	pcs := make([]uintptr, 64)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs)])
	var all []runtime.Frame
	for {
		f, more := frames.Next()
		if !isSDKFrame(f) {
			all = append(all, f)
		}
		if !more {
			break
		}
	}
	fp := fingerprint(typ, msg, all)
	if !a.limiter.allow(fp, time.Now()) {
		return nil
	}
	return &errorRecord{
		at:          time.Now(),
		typ:         typ,
		message:     truncateRunes(msg, 1000),
		stack:       truncate(cleanStack(debug.Stack()), maxStackBytes),
		fingerprint: fp,
		handled:     handled,
	}
}

// addError holds an error until its request ends, so it carries the final
// route and user; outside a request (or after it) it is queued at once.
func (a *Agent) addError(ann *annotations, rec *errorRecord) {
	if ann != nil {
		ann.mu.Lock()
		if !ann.done {
			if len(ann.errs) < maxErrorsInRequest {
				ann.errs = append(ann.errs, rec)
			}
			ann.mu.Unlock()
			return
		}
		ann.mu.Unlock()
	}
	a.enqueueError(rec, ann, "")
}

func (a *Agent) enqueueError(rec *errorRecord, ann *annotations, route string) {
	if ann != nil {
		rec.requestID, rec.method, rec.path = ann.requestID, ann.method, ann.path
		ann.mu.Lock()
		rec.userID = ann.user.ID
		if route == "" {
			route = ann.route
		}
		ann.mu.Unlock()
		rec.route = route
	}
	a.writer.enqueue(rec)
}

// errorParams returns the row in errorColumns order.
func (w *writer) errorParams(e *errorRecord) []any {
	return []any{
		e.at.UTC().Format(tsLayout),
		nullable(w.ident.service),
		w.ident.instance,
		nullable(w.ident.version),
		nullable(e.requestID),
		nullable(e.method),
		nullable(e.route),
		nullable(e.path),
		nullable(e.userID),
		e.typ,
		nullable(e.message),
		nullable(e.stack),
		e.fingerprint,
		boolInt(e.handled),
		runtime.Version(),
	}
}

// rootType names the innermost error of err's Unwrap chain.
func rootType(err error) string {
	for {
		inner := errors.Unwrap(err)
		if inner == nil {
			return fmt.Sprintf("%T", err)
		}
		err = inner
	}
}

var digitRun = regexp.MustCompile(`[0-9]+`)

// fingerprint groups occurrences of the same error: the first 16 hex
// characters of SHA-1 over the type and the top application frames
// (function@file, no line numbers, so edits elsewhere in a file keep it). With
// no frames it falls back to the message with numbers masked.
func fingerprint(typ, msg string, frames []runtime.Frame) string {
	key := typ + "\n" + digitRun.ReplaceAllString(msg, "0")
	if top := topFrames(frames); len(top) > 0 {
		key = typ + "\n" + strings.Join(top, "\n")
	}
	sum := sha1.Sum([]byte(key))
	return hex.EncodeToString(sum[:])[:16]
}

// topFrames returns up to five application frames as function@file, or the
// top five innermost frames of any kind (the SDK's own excepted) when none
// are the application's. Frames arrive innermost first.
func topFrames(frames []runtime.Frame) []string {
	var app, rest []string
	for _, f := range frames {
		if f.Function == "" {
			continue
		}
		key := f.Function + "@" + path.Base(f.File)
		if isAppFrame(f) {
			app = append(app, key)
			if len(app) == fingerprintFrames {
				break
			}
		} else if len(rest) < fingerprintFrames {
			rest = append(rest, key)
		}
	}
	if len(app) > 0 {
		return app
	}
	return rest
}

var (
	sdkPackage = reflect.TypeFor[Agent]().PkgPath()
	mainModule = func() string {
		if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Path != "command-line-arguments" {
			return bi.Main.Path
		}
		return ""
	}()
	goroot = strings.ReplaceAll(runtime.GOROOT(), "\\", "/")
)

// sdkDir is this package's source directory. Inlined SDK code (Middleware's
// closures inlined into main show up as main.main.(*Agent).Middleware.func2)
// keeps the SDK's file, so frames are told apart by file as well as name.
var sdkDir = func() string {
	_, file, _, _ := runtime.Caller(0)
	return path.Dir(strings.ReplaceAll(file, "\\", "/")) + "/"
}()

func isSDKFrame(f runtime.Frame) bool {
	return strings.HasPrefix(f.Function, sdkPackage+".") || isSDKFile(f.File)
}

func isSDKFile(file string) bool {
	file = strings.ReplaceAll(file, "\\", "/")
	return strings.HasPrefix(file, sdkDir) && !strings.Contains(file[len(sdkDir):], "/")
}

// isAppFrame tells the application's own code from the standard library and
// dependencies: by the main module's path when the binary carries build info,
// else by where the source file lives.
func isAppFrame(f runtime.Frame) bool {
	pkg := funcPackage(f.Function)
	switch {
	case isSDKFrame(f):
		return false
	case pkg == "main":
		return true
	case mainModule != "":
		return pkg == mainModule || strings.HasPrefix(pkg, mainModule+"/")
	}
	file := strings.ReplaceAll(f.File, "\\", "/")
	return !(goroot != "" && strings.HasPrefix(file, goroot+"/")) && !strings.HasPrefix(file, "$GOROOT/") &&
		!strings.Contains(file, "/pkg/mod/") && !strings.Contains(file, "/vendor/")
}

// funcPackage is the import path of a function name such as
// "example.com/app/handlers.(*Trips).Get".
func funcPackage(fn string) string {
	slash := strings.LastIndexByte(fn, '/')
	if dot := strings.IndexByte(fn[slash+1:], '.'); dot >= 0 {
		return fn[:slash+1+dot]
	}
	return fn
}

// cleanStack removes runtime/debug, the panic call and SDK frames from a
// debug.Stack trace, so it starts where the application failed.
func cleanStack(stack []byte) string {
	lines := bytes.Split(bytes.TrimRight(stack, "\n"), []byte("\n"))
	var b strings.Builder
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		hasFile := i+1 < len(lines) && bytes.HasPrefix(lines[i+1], []byte("\t"))
		if hasFile && (bytes.HasPrefix(line, []byte("runtime/debug.")) || bytes.HasPrefix(line, []byte("panic(")) ||
			bytes.HasPrefix(line, []byte(sdkPackage+".")) || isSDKFile(stackFile(lines[i+1]))) {
			i++ // and its file line
			continue
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// stackFile is the path in a traceback file line (tab, /src/app/x.go:12 +0x1d).
func stackFile(line []byte) string {
	f := string(bytes.TrimPrefix(line, []byte("\t")))
	if i := strings.LastIndexByte(f, ':'); i > 0 {
		f = f[:i]
	}
	return f
}

// errLimiter allows a fixed number of rows per fingerprint per minute.
type errLimiter struct {
	mu   sync.Mutex
	seen map[string]*errWindow
}

type errWindow struct {
	start time.Time
	n     int
}

func (l *errLimiter) allow(fp string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen == nil {
		l.seen = map[string]*errWindow{}
	}
	w := l.seen[fp]
	if w == nil || now.Sub(w.start) >= time.Minute {
		if len(l.seen) >= 10000 {
			for k, v := range l.seen {
				if now.Sub(v.start) >= time.Minute {
					delete(l.seen, k)
				}
			}
		}
		l.seen[fp] = &errWindow{start: now, n: 1}
		return true
	}
	if w.n >= errorsPerMinute {
		return false
	}
	w.n++
	return true
}
