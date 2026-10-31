package analytics

import (
	"context"
	"net/http"
	"regexp"
	"strings"
)

// The columns themselves are added to older tables in ensureSchema, before the feature schemas run
func (s *Server) tracesSchema() []string {
	return []string{s.q.d.TraceIndex("api_logs"), s.q.d.TraceIndex("spans")}
}

func (s *Server) tracesStart(ctx context.Context) {}

func (s *Server) tracesRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/traces", s.authed(s.handleTraceSearch))
	mux.Handle("GET /api/traces/map", s.authed(s.handleServiceMap))
	mux.Handle("GET /api/traces/{trace_id}", s.authed(s.handleTrace))
}

func (s *Server) tracesPrompt() string {
	return `Distributed traces: api_logs and spans rows that share trace_id belong to one trace across services (W3C trace context, 32 hex characters). Rows from older SDKs have NULL trace columns.
- api_logs.trace_id, api_logs.span_id (the request's own server span), api_logs.parent_span_id (the caller's span, NULL when the request started the trace).
- spans.trace_id, spans.span_id, spans.parent_span_id (the request's span_id, or the custom or background span it ran inside).
- A request one service made to another: the caller's spans row (kind 'http') has span_id = the callee's api_logs.parent_span_id, with the same trace_id. Join on both columns. Calls between services are those pairs where the two service values differ.
- A trace's root request has parent_span_id NULL (or the earliest request when the caller was not traced). Indexed: trace_id on both tables.
- app_logs.trace_id and app_logs.span_id link log lines to the trace and the span they were logged in.`
}

var traceIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

func inMarks(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

// Keeps each statement within D1's 100 bound params
func chunks(ids []string, n int) [][]string {
	var out [][]string
	for len(ids) > n {
		out = append(out, ids[:n])
		ids = ids[n:]
	}
	if len(ids) > 0 {
		out = append(out, ids)
	}
	return out
}

func anys(ids []string) []any {
	out := make([]any, len(ids))
	for i, v := range ids {
		out[i] = v
	}
	return out
}
