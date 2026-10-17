package analytics

import (
	"context"
	"net/http"
)

// Uptime checks (uptime_checks, uptime_results) and the public status page.

func (s *Server) uptimeSchema() []string          { return nil }
func (s *Server) uptimeRoutes(mux *http.ServeMux) {}
func (s *Server) uptimeStart(ctx context.Context) {}
func (s *Server) uptimePrompt() string            { return "" }
