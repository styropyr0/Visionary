package analytics

import (
	"context"
	"net/http"
)

// Spans, errors and releases written by the SDKs (sdks/SPEC.md, "Telemetry").
// The tables are created with the core schema in ensureSchema.

func (s *Server) telemetrySchema() []string          { return nil }
func (s *Server) telemetryRoutes(mux *http.ServeMux) {}
func (s *Server) telemetryStart(ctx context.Context) {}
func (s *Server) telemetryPrompt() string            { return "" }
