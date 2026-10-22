package visionary

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const requestIDHeader = "X-Request-ID"

type ctxKey struct{}

// annotations travel in the request context so code deeper in the stack (auth
// middleware, handlers, tracers, CaptureError) can describe the request for
// the log and attach calls and errors to it.
type annotations struct {
	agent *Agent
	// trace collects spans; nil when the request is not logged or spans are off.
	trace     *trace
	requestID string
	method    string
	path      string

	mu    sync.Mutex
	user  User
	route string
	// errs are errors captured during the request, written when it ends so
	// they carry its final route and user.
	errs []*errorRecord
	done bool
}

// fromContext returns the request's annotations (nil outside Middleware) and
// the Agent that records for ctx (the request's, else the active one).
func fromContext(ctx context.Context) (*annotations, *Agent) {
	if ann, ok := ctx.Value(ctxKey{}).(*annotations); ok {
		return ann, ann.agent
	}
	return nil, active.Load()
}

// SetUser attaches the signed-in user to the request being logged. Call it
// from auth middleware once the user is known. No-op outside Middleware.
func SetUser(ctx context.Context, u User) {
	if a, ok := ctx.Value(ctxKey{}).(*annotations); ok {
		a.mu.Lock()
		a.user = u
		a.mu.Unlock()
	}
}

// SetRoute records the route pattern for the request, for routers the Route
// option can't see. No-op outside Middleware.
func SetRoute(ctx context.Context, pattern string) {
	if a, ok := ctx.Value(ctxKey{}).(*annotations); ok {
		a.mu.Lock()
		a.route = pattern
		a.mu.Unlock()
	}
}

// httpCounters feed the system recorder's throughput columns.
type httpCounters struct {
	requests, errors, inFlight atomic.Int64
}

// Middleware logs each request, collects the database and HTTP calls it
// makes (see PgxTracer, WrapDriver, Transport and StartSpan), records panics
// and counts throughput. A nil Agent returns next unchanged.
//
// Mount it inside the router's panic recovery (chi's Recoverer, gin's
// Recovery): a panic is recorded in errors, the request is logged with status
// 500, and the panic continues to the recovery middleware, which answers as
// it always did. Mounted outside the recovery it never sees panics, only the
// 500 they become.
func (a *Agent) Middleware(next http.Handler) http.Handler {
	if a == nil || (a.writer == nil && a.system == nil) {
		return next
	}
	counters := a.counters()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counters.inFlight.Add(1)
		defer counters.inFlight.Add(-1)

		start := time.Now()
		ann := &annotations{agent: a, method: r.Method, path: r.URL.Path}
		logIt := a.cfg.Requests.Enabled && a.writer != nil && strings.HasPrefix(r.URL.Path, a.cfg.Requests.PathPrefix)
		if logIt {
			ann.requestID = r.Header.Get(requestIDHeader)
			if ann.requestID == "" || len(ann.requestID) > 128 {
				ann.requestID = newRequestID()
			}
			w.Header().Set(requestIDHeader, ann.requestID)
			if a.spansOn() {
				ann.trace = &trace{requestID: ann.requestID, start: start, max: a.cfg.SpanMax}
			}
		}
		r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, ann))
		rec := &captureWriter{ResponseWriter: w, status: http.StatusOK}

		var (
			headers    http.Header
			reqType    string
			reqCapture *captureReader
		)
		if logIt {
			headers = r.Header.Clone()
			reqType = r.Header.Get("Content-Type")
			rec.bodies, rec.max = a.cfg.Requests.CaptureBodies, a.cfg.Requests.MaxBodyBytes
			if rec.bodies && r.Body != nil && r.Body != http.NoBody && capturable(reqType) {
				reqCapture = &captureReader{ReadCloser: r.Body, max: rec.max}
				r.Body = reqCapture
			}
		}

		finish := func() {
			counters.observe(rec.status)
			ann.mu.Lock()
			ann.done = true
			errs := ann.errs
			ann.errs = nil
			ann.mu.Unlock()
			route := ""
			if logIt || len(errs) > 0 {
				route = a.route(r, ann)
				ann.mu.Lock()
				ann.route = route // for errors captured by goroutines still running
				ann.mu.Unlock()
			}
			if logIt {
				e := &entry{
					RequestID:           ann.requestID,
					StartedAt:           start,
					Method:              r.Method,
					Path:                r.URL.Path,
					Route:               route,
					Query:               r.URL.RawQuery,
					Status:              rec.status,
					Duration:            time.Since(start),
					IP:                  clientIP(r),
					UserAgent:           r.UserAgent(),
					RequestHeaders:      headers,
					RequestContentType:  reqType,
					RequestSize:         max(r.ContentLength, 0),
					ResponseContentType: rec.Header().Get("Content-Type"),
					ResponseBody:        rec.buf.Bytes(),
					ResponseTruncated:   rec.truncated,
					ResponseSize:        rec.size,
				}
				if reqCapture != nil {
					e.RequestBody = reqCapture.buf.Bytes()
					e.RequestTruncated = reqCapture.truncated
					e.RequestSize = max(e.RequestSize, reqCapture.size)
				}
				if ann.trace != nil {
					e.Totals, e.Spans = ann.trace.finish()
				}
				ann.mu.Lock()
				e.User = ann.user
				ann.mu.Unlock()
				a.writer.enqueue(e)
			}
			for _, e := range errs {
				a.enqueueError(e, ann, route)
			}
		}

		// A panic is recorded, logged as the 500 the recovery middleware
		// will send, and then continues unchanged.
		completed := false
		defer func() {
			if completed {
				return
			}
			v := recover()
			if v != nil && v != http.ErrAbortHandler {
				a.capturePanic(ann, v)
			}
			if !rec.wroteHeader {
				rec.status = http.StatusInternalServerError
			}
			finish()
			if v != nil {
				panic(v)
			}
		}()
		next.ServeHTTP(rec, r)
		completed = true
		finish()
	})
}

// route is the request's route pattern: SetRoute, else the Route option,
// else the Go 1.22+ ServeMux pattern.
func (a *Agent) route(r *http.Request, ann *annotations) string {
	ann.mu.Lock()
	route := ann.route
	ann.mu.Unlock()
	if route == "" && a.cfg.Requests.Route != nil {
		route = a.cfg.Requests.Route(r)
	}
	if route == "" && r.Pattern != "" {
		// ServeMux sets Pattern as "GET /users/{id}"; keep the path part.
		route = r.Pattern
		if _, path, ok := strings.Cut(r.Pattern, " "); ok {
			route = path
		}
	}
	return route
}

func (a *Agent) counters() *httpCounters {
	if a.system != nil {
		return &a.system.http
	}
	return &httpCounters{}
}

func (c *httpCounters) observe(status int) {
	c.requests.Add(1)
	if status >= 500 {
		c.errors.Add(1)
	}
}

func newRequestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// clientIP prefers the first X-Forwarded-For hop, as most deployments sit
// behind a proxy; it is informational only and never used for decisions.
func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		first, _, _ := strings.Cut(fwd, ",")
		return strings.TrimSpace(first)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// capturable skips binary and multipart payloads, which are useless as text
// and expensive to store.
func capturable(contentType string) bool {
	ct := strings.ToLower(contentType)
	switch {
	case ct == "":
		return true
	case strings.HasPrefix(ct, "multipart/"),
		strings.HasPrefix(ct, "image/"),
		strings.HasPrefix(ct, "video/"),
		strings.HasPrefix(ct, "audio/"),
		strings.HasPrefix(ct, "application/octet-stream"),
		strings.HasPrefix(ct, "application/zip"),
		strings.HasPrefix(ct, "application/pdf"):
		return false
	}
	return true
}

// captureReader tees up to max bytes of the request body as the handler reads it.
type captureReader struct {
	io.ReadCloser
	buf       bytes.Buffer
	max       int
	size      int64
	truncated bool
}

func (c *captureReader) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	if n > 0 {
		c.size += int64(n)
		c.truncated = capture(&c.buf, p[:n], c.max) || c.truncated
	}
	return n, err
}

// captureWriter records status, size and (when bodies are on) up to max bytes
// of the response body.
type captureWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	bodies      bool
	capturing   bool
	buf         bytes.Buffer
	max         int
	size        int64
	truncated   bool
}

func (c *captureWriter) WriteHeader(code int) {
	if !c.wroteHeader {
		c.status = code
		c.markWritten()
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *captureWriter) Write(b []byte) (int, error) {
	if !c.wroteHeader {
		c.markWritten()
	}
	n, err := c.ResponseWriter.Write(b)
	if n > 0 {
		c.size += int64(n)
		if c.capturing {
			c.truncated = capture(&c.buf, b[:n], c.max) || c.truncated
		}
	}
	return n, err
}

func (c *captureWriter) markWritten() {
	c.wroteHeader = true
	c.capturing = c.bodies && capturable(c.Header().Get("Content-Type"))
}

func (c *captureWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (c *captureWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// capture appends p to buf up to limit bytes and reports whether anything was cut.
func capture(buf *bytes.Buffer, p []byte, limit int) bool {
	room := limit - buf.Len()
	if room <= 0 {
		return true
	}
	if len(p) > room {
		buf.Write(p[:room])
		return true
	}
	buf.Write(p)
	return false
}
