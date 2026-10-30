package analytics

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
	"time"
)

// Features plug in here. Each one lives in its own files and fills in its
// hooks (schema, routes, background work, notes for the analyst), so features
// do not edit each other's code:
//
//	alerts.go    alert rules, notification channels, anomalies, SLO burn, digests
//	uptime.go    uptime checks and the public status page
//	telemetry.go spans, errors and releases sent by the SDKs
//	rollups.go   pre-aggregated rollups for long ranges
//	otlp.go      OpenTelemetry ingestion over OTLP/HTTP and OTLP/gRPC
//	rum.go       browser and mobile ingestion
//	team.go      roles, API keys, audit log, dashboards
//	hosts.go     the host agent's reports and the Hosts view
//	security.go  security detectors, findings, the Security page and report
//	insights.go  queries, dependencies, endpoint breakdown, EXPLAIN, GraphQL, CPU profiles
//	settings.go  team-wide settings: profiling, which pages show
//	data_delete.go deleting collected data by kind, range and service
//	applogs.go   application logs: ingest, search, patterns, archive
//	traces.go    distributed traces across services, trace search, the service map
//	incidents.go declared incidents: roles, timeline, status updates, postmortems
//	metrics.go   custom metrics: SDK rows, OTLP, Prometheus remote_write, charts,
//	             PromQL and the Prometheus HTTP API (promql*.go)
//	sso.go       single sign-on (OIDC, SAML) and SCIM provisioning
//	ci.go        CI pipelines, jobs and tests: GitHub Actions webhook, JUnit, flaky tests
//	replay.go    session replay recorded by the browser SDK
//	leases.go    several replicas on one database: which one runs each job
//	workspaces.go workspaces: people scoped to sets of services
//	config_*.go  configuration as one YAML or JSON file: export, dry run, import

// featureSchemas are the CREATE statements of every feature, run at startup
// after the core tables exist.
func (s *Server) featureSchemas() []string {
	var out []string
	for _, f := range [][]string{s.telemetrySchema(), s.alertsSchema(), s.uptimeSchema(), s.uptimeBrowserSchema(), s.rollupsSchema(), s.rumSchema(), s.teamSchema(), s.hostsSchema(), s.insightsSchema(), s.settingsSchema(), s.accountsSchema(), s.securitySchema(), s.applogsSchema(), s.tracesSchema(), s.incidentsSchema(), s.metricsSchema(), s.scimSchema(), s.ciSchema(), s.replaySchema(), s.q.leasesSchema(), s.workspacesSchema()} {
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
	s.applogsRoutes(mux)
	s.tracesRoutes(mux)
	s.incidentsRoutes(mux)
	s.metricsRoutes(mux)
	s.ssoRoutes(mux)
	s.ciRoutes(mux)
	s.replayRoutes(mux)
	s.replicasRoutes(mux)
	s.workspacesRoutes(mux)
	s.configRoutes(mux)
	s.partitionRoutes(mux)
}

// Start launches the features' background work. It returns after the first
// lease round; the work stops when ctx ends. Work that must run on one
// replica only registers with leased and starts when this replica holds it.
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
	s.tracesStart(ctx)
	s.replayStart(ctx)
	s.pullSignOuts(ctx)
	s.partitionsStart(ctx)
	s.leasesStart(ctx)
}

// featurePrompt describes the features' tables to the analyst.
func (s *Server) featurePrompt() string {
	var parts []string
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, p := range []string{s.telemetryPrompt(), s.rumPrompt(), s.alertsPrompt(), s.uptimePrompt(), s.rollupsPrompt(), s.hostsPrompt(), s.securityPrompt(), s.applogsPrompt(), s.tracesPrompt(), s.incidentsPrompt(), s.metricsPrompt(), s.ciPrompt(), s.ownTablesPrompt(ctx)} {
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
