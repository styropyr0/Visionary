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
// middleware, handlers) can describe the request for the log.
type annotations struct {
	mu    sync.Mutex
	user  User
	route string
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

// Middleware logs each request and counts throughput. Mount it outermost
// (outside panic recovery) so failures are logged with the status the client
// got. A nil Agent returns next unchanged.
func (a *Agent) Middleware(next http.Handler) http.Handler {
	if a == nil || (a.requests == nil && a.system == nil) {
		return next
	}
	counters := a.counters()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counters.inFlight.Add(1)
		defer counters.inFlight.Add(-1)

		logIt := a.requests != nil && strings.HasPrefix(r.URL.Path, a.cfg.Requests.PathPrefix)
		if !logIt {
			rec := &captureWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			counters.observe(rec.status)
			return
		}

		start := time.Now()
		requestID := r.Header.Get(requestIDHeader)
		if requestID == "" || len(requestID) > 128 {
			requestID = newRequestID()
		}
		w.Header().Set(requestIDHeader, requestID)

		ann := &annotations{}
		r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, ann))
		headers := r.Header.Clone()
		reqType := r.Header.Get("Content-Type")
		bodies := a.cfg.Requests.CaptureBodies
		maxBody := a.cfg.Requests.MaxBodyBytes

		var reqCapture *captureReader
		if bodies && r.Body != nil && r.Body != http.NoBody && capturable(reqType) {
			reqCapture = &captureReader{ReadCloser: r.Body, max: maxBody}
			r.Body = reqCapture
		}
		rec := &captureWriter{ResponseWriter: w, status: http.StatusOK, max: maxBody, bodies: bodies}

		next.ServeHTTP(rec, r)
		counters.observe(rec.status)

		e := &entry{
			RequestID:           requestID,
			StartedAt:           start,
			Method:              r.Method,
			Path:                r.URL.Path,
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
		ann.mu.Lock()
		e.User, e.Route = ann.user, ann.route
		ann.mu.Unlock()
		if e.Route == "" && a.cfg.Requests.Route != nil {
			e.Route = a.cfg.Requests.Route(r)
		}
		if e.Route == "" && r.Pattern != "" {
			// Go 1.22+ ServeMux sets Pattern on the request it routes,
			// as "GET /users/{id}"; keep the path part.
			e.Route = r.Pattern
			if _, path, ok := strings.Cut(r.Pattern, " "); ok {
				e.Route = path
			}
		}
		if reqCapture != nil {
			e.RequestBody = reqCapture.buf.Bytes()
			e.RequestTruncated = reqCapture.truncated
			e.RequestSize = max(e.RequestSize, reqCapture.size)
		}
		a.requests.log(e)
	})
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
