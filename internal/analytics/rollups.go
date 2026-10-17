package analytics

import (
	"context"
	"net/http"
)

// Pre-aggregated rollups (rollup_requests, rollup_system) for long ranges.

func (s *Server) rollupsSchema() []string          { return nil }
func (s *Server) rollupsRoutes(mux *http.ServeMux) {}
func (s *Server) rollupsStart(ctx context.Context) {}
func (s *Server) rollupsPrompt() string            { return "" }
