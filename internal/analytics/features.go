package analytics

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

// Features plug in here. Each one lives in its own files and fills in its
// hooks (schema, routes, background work, notes for the analyst), so features
// do not edit each other's code:
//
//	alerts.go    alert rules, notification channels, anomalies, SLO burn, digests
//	uptime.go    uptime checks and the public status page
//	telemetry.go spans, errors and releases sent by the SDKs
//	rollups.go   pre-aggregated rollups for long ranges
//	otlp.go      OpenTelemetry (OTLP/HTTP) ingestion
//	rum.go       browser and mobile ingestion
//	team.go      roles, API keys, audit log, dashboards
//	hosts.go     the host agent's reports and the Hosts view
//	security.go  security detectors, findings, the Security page and report
//	insights.go  queries, dependencies, endpoint breakdown, EXPLAIN, GraphQL, CPU profiles
//	settings.go  team-wide settings: profiling, which pages show
//	data_delete.go deleting collected data by kind, range and service
//	applogs.go   application logs: ingest, search, patterns, archive

// featureSchemas are the CREATE statements of every feature, run at startup
// after the core tables exist.
func (s *Server) featureSchemas() []string {
	var out []string
	for _, f := range [][]string{s.telemetrySchema(), s.alertsSchema(), s.uptimeSchema(), s.rollupsSchema(), s.rumSchema(), s.teamSchema(), s.hostsSchema(), s.insightsSchema(), s.settingsSchema(), s.accountsSchema(), s.securitySchema(), s.applogsSchema()} {
		out = append(out, f...)
	}
	return out
}

func (s *Server) featureRoutes(mux *http.ServeMux) {
	s.telemetryRoutes(mux)
	s.alertsRoutes(mux)
	s.uptimeRoutes(mux)
	s.rollupsRoutes(mux)
	s.otlpRoutes(mux)
	s.rumRoutes(mux)
	s.teamRoutes(mux)
	s.hostsRoutes(mux)
	s.securityRoutes(mux)
	s.insightsRoutes(mux)
	s.settingsRoutes(mux)
	s.dataRoutes(mux)
}

// Start launches the features' background work. It returns at once; the work
// stops when ctx ends.
func (s *Server) Start(ctx context.Context) {
	if s.schemaPending.Load() {
		go s.retrySchema(ctx)
	}
	s.liveStart(ctx)
	// After the settings load, before anything reads its restart-only ones.
	s.noteStartConfig()
	s.releasesStart()
	s.alertsStart(ctx)
	s.uptimeStart(ctx)
	s.rollupsStart(ctx)
	s.telemetryStart(ctx)
	s.rumStart(ctx)
	s.teamStart(ctx)
	s.otlpStart(ctx)
	s.hostsStart(ctx)
	s.securityStart(ctx)
	s.applogsStart(ctx)
}

// featurePrompt describes the features' tables to the analyst.
func (s *Server) featurePrompt() string {
	var parts []string
	for _, p := range []string{s.telemetryPrompt(), s.rumPrompt(), s.alertsPrompt(), s.uptimePrompt(), s.rollupsPrompt(), s.hostsPrompt(), s.securityPrompt(), s.applogsPrompt()} {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, "\n\n")
}

// constantTimeEqual compares secrets without leaking their length or content
// through timing.
func constantTimeEqual(a, b string) bool {
	x := sha256.Sum256([]byte(a))
	y := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(x[:], y[:]) == 1
}
