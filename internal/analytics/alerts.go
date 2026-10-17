package analytics

import (
	"context"
	"net/http"
)

// Alert rules, notification channels, anomaly detection, SLO burn alerts and
// digests.

func (s *Server) alertsSchema() []string          { return nil }
func (s *Server) alertsRoutes(mux *http.ServeMux) {}
func (s *Server) alertsStart(ctx context.Context) {}
func (s *Server) alertsPrompt() string            { return "" }
