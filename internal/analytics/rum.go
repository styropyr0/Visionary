package analytics

import (
	"context"
	"net/http"
)

// Browser and mobile ingestion (client_events), sdks/SPEC.md "Client ingest API".

func (s *Server) rumSchema() []string          { return nil }
func (s *Server) rumRoutes(mux *http.ServeMux) {}
func (s *Server) rumStart(ctx context.Context) {}
func (s *Server) rumPrompt() string            { return "" }
