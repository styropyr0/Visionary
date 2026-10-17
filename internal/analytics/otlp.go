package analytics

import "net/http"

// OpenTelemetry ingestion over OTLP/HTTP, mapped onto Visionary's tables.

func (s *Server) otlpRoutes(mux *http.ServeMux) {}
