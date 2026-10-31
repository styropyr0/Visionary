package visionary

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"math/rand/v2"
	"net/http"
	"strings"
)

// W3C trace context. A request continues the trace in its traceparent header
// or starts one; it and every call it makes get their own span id, and
// outbound requests through Transport carry the trace on.

const (
	traceparentHeader = "traceparent"
	tracestateHeader  = "tracestate"
)

type spanContext struct {
	traceID, spanID string
	// flags is the traceparent trace-flags byte in hex ("01" sampled).
	flags string
	// state is the caller's tracestate, passed on unchanged.
	state string
}

func (sc spanContext) valid() bool { return sc.traceID != "" && sc.spanID != "" }

func (sc spanContext) traceparent() string {
	flags := sc.flags
	if flags == "" {
		flags = "01"
	}
	return "00-" + sc.traceID + "-" + sc.spanID + "-" + flags
}

type spanCtxKey struct{}

func withSpan(ctx context.Context, sc spanContext) context.Context {
	return context.WithValue(ctx, spanCtxKey{}, sc)
}

// currentSpan is the innermost span of ctx: a custom or background span,
// else the request's server span. Zero outside any trace.
func currentSpan(ctx context.Context) spanContext {
	sc, _ := ctx.Value(spanCtxKey{}).(spanContext)
	return sc
}

// parseTraceparent reads a traceparent header. ok is false for anything
// malformed, for version ff and for all-zero ids, which mean "no parent".
func parseTraceparent(h string) (sc spanContext, ok bool) {
	parts := strings.Split(strings.TrimSpace(h), "-")
	if len(parts) < 4 || len(parts[0]) != 2 || parts[0] == "ff" || len(parts[1]) != 32 || len(parts[2]) != 16 || len(parts[3]) != 2 {
		return sc, false
	}
	// Version 00 has exactly four fields; later versions may append more.
	if parts[0] == "00" && len(parts) != 4 {
		return sc, false
	}
	for _, p := range parts[:4] {
		if !isLowerHex(p) {
			return sc, false
		}
	}
	if strings.Trim(parts[1], "0") == "" || strings.Trim(parts[2], "0") == "" {
		return sc, false
	}
	return spanContext{traceID: parts[1], spanID: parts[2], flags: parts[3]}, true
}

func isLowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// newTraceID and newSpanID are random, never all zero. math/rand/v2's
// generator is seeded from the OS and cheap enough for every call.
func newTraceID() string {
	var b [16]byte
	for b == [16]byte{} {
		binary.LittleEndian.PutUint64(b[:8], rand.Uint64())
		binary.LittleEndian.PutUint64(b[8:], rand.Uint64())
	}
	return hex.EncodeToString(b[:])
}

func newSpanID() string {
	var b [8]byte
	for b == [8]byte{} {
		binary.LittleEndian.PutUint64(b[:], rand.Uint64())
	}
	return hex.EncodeToString(b[:])
}

// requestSpan is the server span of an incoming request: a child of the
// caller's span when traceparent names one, else the root of a new trace.
// parent is the caller's span id, or "".
func requestSpan(h http.Header) (sc spanContext, parent string) {
	if in, ok := parseTraceparent(h.Get(traceparentHeader)); ok {
		in.state = h.Get(tracestateHeader)
		parent, in.spanID = in.spanID, newSpanID()
		return in, parent
	}
	return spanContext{traceID: newTraceID(), spanID: newSpanID(), flags: "01"}, ""
}

// injectTrace sets traceparent (and tracestate) on an outbound request so
// the service it reaches continues the trace with sc as the parent. A
// traceparent the caller set itself is left alone.
func injectTrace(req *http.Request, sc spanContext) *http.Request {
	if !sc.valid() || req.Header.Get(traceparentHeader) != "" {
		return req
	}
	// A RoundTripper must not change the caller's request.
	out := req.Clone(req.Context())
	out.Header.Set(traceparentHeader, sc.traceparent())
	if sc.state != "" && out.Header.Get(tracestateHeader) == "" {
		out.Header.Set(tracestateHeader, sc.state)
	}
	return out
}
