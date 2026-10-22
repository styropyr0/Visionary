package analytics

import (
	"net/http"
	"strings"

	sdk "github.com/styropyr0/visionary/sdks/go"
)

// Insights: the Queries, Dependencies and Profiles pages, and the endpoint
// breakdown on Performance. Everything reads the spans and api_logs the SDKs
// already write, except:
//
//   - explain_requests: a query plan asked for from the dashboard. The server
//     inserts it 'pending'; an SDK with EXPLAIN turned on claims it
//     ('running'), runs EXPLAIN on the application's own database and writes
//     the plan back ('done' or 'failed'). Visionary never connects to the
//     application's database itself.
//   - profiles: sampled CPU profiles written by the SDKs, as folded stacks
//     ("frame;frame;frame count" per line), with the routes that were busy
//     while each was taken.
//
//	insights_queries.go   query catalogue, depth per route, regressions after a deploy
//	insights_breakdown.go dependencies (outbound hosts) and the endpoint breakdown
//	insights_explain.go   EXPLAIN requests, plan findings, plain-words summaries
//	insights_graphql.go   GraphQL operations, their depth and field count
//	insights_profiles.go  CPU profiles and flame graphs

func (s *Server) insightsSchema() []string { return []string{s.q.insightsTables()} }

func (s *Server) insightsRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/insights/queries", s.authed(s.handleQueryCatalogue))
	mux.Handle("GET /api/insights/depth", s.authed(s.handleQueryDepth))
	mux.Handle("GET /api/insights/regressions", s.authed(s.handleQueryRegressions))
	mux.Handle("GET /api/insights/dependencies", s.authed(s.handleDependencies))
	mux.Handle("GET /api/insights/breakdown", s.authed(s.handleBreakdown))
	mux.Handle("GET /api/insights/explain", s.authed(s.handleExplainGet))
	mux.Handle("POST /api/insights/explain", s.authed(s.handleExplainCreate))
	mux.Handle("POST /api/insights/explain/{id}/summary", s.authed(s.handleExplainSummary))
	mux.Handle("GET /api/insights/graphql", s.authed(s.handleGraphQL))
	mux.Handle("GET /api/insights/profiles", s.authed(s.handleProfiles))
	mux.Handle("GET /api/insights/profiles/flame", s.authed(s.handleFlame))
}

func (q sqlDialect) insightsTables() string {
	id, ts, key, big := "INTEGER PRIMARY KEY AUTOINCREMENT", "TEXT", "TEXT", "TEXT"
	tail, index := "", "CREATE INDEX IF NOT EXISTS"
	switch q.d {
	case sdk.Postgres:
		id = "BIGSERIAL PRIMARY KEY"
	case sdk.MySQL:
		id, ts, key, big = "BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY", "VARCHAR(32)", "VARCHAR(255)", "MEDIUMTEXT"
		tail, index = " CHARACTER SET utf8mb4", "CREATE INDEX" // duplicate-index errors are ignored
	}
	ddl := `CREATE TABLE IF NOT EXISTS explain_requests (
    id          {id},
    created_at  {ts} NOT NULL,
    target      {key} NOT NULL,
    statement   TEXT NOT NULL,
    status      {key} NOT NULL,
    claimed_by  TEXT,
    plan        {big},
    error       TEXT,
    finished_at {ts}
){tail};
{index} idx_explain_requests_status ON explain_requests (status, created_at);
CREATE TABLE IF NOT EXISTS profiles (
    id          {id},
    created_at  {ts} NOT NULL,
    service     {key},
    instance    TEXT NOT NULL,
    version     TEXT,
    sdk         TEXT,
    duration_ms DOUBLE PRECISION NOT NULL,
    samples     INTEGER NOT NULL,
    routes      TEXT,
    folded      {big} NOT NULL
){tail};
{index} idx_profiles_created_at ON profiles (created_at)`
	if q.d == sdk.SQLite {
		ddl = strings.ReplaceAll(ddl, "DOUBLE PRECISION", "REAL")
	} else if q.d == sdk.MySQL {
		ddl = strings.ReplaceAll(ddl, "DOUBLE PRECISION", "DOUBLE")
	}
	return strings.NewReplacer("{id}", id, "{ts}", ts, "{key}", key, "{big}", big, "{tail}", tail, "{index}", index).Replace(ddl)
}
