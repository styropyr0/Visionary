package visionary

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"math/rand/v2"
	"net/http"
	"strings"
)

const (
	traceparentHeader = "traceparent"
	tracestateHeader  = "tracestate"
)

type spanContext struct {
	traceID, spanID string
	flags           string
	state           string
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

func currentSpan(ctx context.Context) spanContext {
	sc, _ := ctx.Value(spanCtxKey{}).(spanContext)
	return sc
}

// All-zero ids mean "no parent"
func parseTraceparent(h string) (sc spanContext, ok bool) {
	parts := strings.Split(strings.TrimSpace(h), "-")
	if len(parts) < 4 || len(parts[0]) != 2 || parts[0] == "ff" || len(parts[1]) != 32 || len(parts[2]) != 16 || len(parts[3]) != 2 {
		return sc, false
	}
	// Version 00 has exactly four fields, later versions may append more
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

func requestSpan(h http.Header) (sc spanContext, parent string) {
	if in, ok := parseTraceparent(h.Get(traceparentHeader)); ok {
		in.state = h.Get(tracestateHeader)
		parent, in.spanID = in.spanID, newSpanID()
		return in, parent
	}
	return spanContext{traceID: newTraceID(), spanID: newSpanID(), flags: "01"}, ""
}

// A traceparent the caller set itself is left alone
func injectTrace(req *http.Request, sc spanContext) *http.Request {
	if !sc.valid() || req.Header.Get(traceparentHeader) != "" {
		return req
	}
	// A RoundTripper must not change the caller's request
	out := req.Clone(req.Context())
	out.Header.Set(traceparentHeader, sc.traceparent())
	if sc.state != "" && out.Header.Get(tracestateHeader) == "" {
		out.Header.Set(tracestateHeader, sc.state)
	}
	return out
}
