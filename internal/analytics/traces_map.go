package analytics

import (
	"net/http"
	"sort"

	"golang.org/x/sync/errgroup"
)

// mapNode is a service, or a database, host, cache or queue the services
// call that reports nothing itself.
type mapNode struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	Name     string   `json:"name"`
	Requests int64    `json:"requests"`
	Errors   int64    `json:"errors"`
	P95      *float64 `json:"p95"`
}

// mapEdge is calls from one node to another in the range.
type mapEdge struct {
	Source    string   `json:"source"`
	Target    string   `json:"target"`
	Kind      string   `json:"kind"`
	Calls     int64    `json:"calls"`
	PerMin    float64  `json:"per_min"`
	Errors    int64    `json:"errors"`
	ErrorRate float64  `json:"error_rate"`
	P95       *float64 `json:"p95"`
}

// p95Of is the 95th percentile of duration_ms per group, by rank (no
// dialect has to support percentile functions).
func p95Of(partition string) string {
	return `ROW_NUMBER() OVER (PARTITION BY ` + partition + ` ORDER BY duration_ms) AS rn,
    COUNT(*) OVER (PARTITION BY ` + partition + `) AS n`
}

const p95Pick = `MIN(CASE WHEN rn >= 0.95 * n THEN duration_ms END)`

// handleServiceMap answers the services as nodes and the calls between them
// as edges: a request whose parent span is in another service is one call
// from that service to it; database, HTTP, cache and queue calls that no
// traced request answered go to the dependency they name.
func (s *Server) handleServiceMap(w http.ResponseWriter, r *http.Request) {
	tr, ok := s.rangeOf(w, r)
	if !ok {
		return
	}
	var services, internal, external *Result
	g, ctx := errgroup.WithContext(r.Context())
	g.Go(func() (err error) {
		services, err = s.telemetryQuery(ctx, `
WITH r AS (
  SELECT COALESCE(service, '') AS service, duration_ms, status,
    `+p95Of("COALESCE(service, '')")+`
  FROM api_logs WHERE created_at >= {{from}} AND created_at < {{to}}
)
SELECT service, COUNT(*) AS requests, `+countIf("status >= 500")+` AS errors, `+p95Pick+` AS p95
FROM r GROUP BY service`, tr)
		return
	})
	g.Go(func() (err error) {
		// The caller's span measures what the caller waited for; a request
		// whose parent is the caller's own request (OpenTelemetry without
		// client spans) has only the callee's time.
		internal, err = s.telemetryQuery(ctx, `
WITH e AS (
  SELECT COALESCE(p.service, '') AS src, COALESCE(c.service, '') AS dst, p.duration_ms,
    CASE WHEN c.status >= 500 OR p.status = 'error' THEN 1 ELSE 0 END AS failed
  FROM api_logs c JOIN spans p ON p.trace_id = c.trace_id AND p.span_id = c.parent_span_id
  WHERE c.created_at >= {{from}} AND c.created_at < {{to}} AND c.parent_span_id IS NOT NULL
    AND p.created_at >= ? AND p.created_at < {{to}}
  UNION ALL
  SELECT COALESCE(p.service, ''), COALESCE(c.service, ''), c.duration_ms,
    CASE WHEN c.status >= 500 THEN 1 ELSE 0 END
  FROM api_logs c JOIN api_logs p ON p.trace_id = c.trace_id AND p.span_id = c.parent_span_id
  WHERE c.created_at >= {{from}} AND c.created_at < {{to}} AND c.parent_span_id IS NOT NULL
    AND p.created_at >= ? AND p.created_at < {{to}}
),
r AS (
  SELECT src, dst, duration_ms, failed, `+p95Of("src, dst")+`
  FROM e WHERE src <> dst
)
SELECT src, dst, COUNT(*) AS calls, SUM(failed) AS errors, `+p95Pick+` AS p95
FROM r GROUP BY src, dst
ORDER BY calls DESC
LIMIT 200`, tr, tr.requestsFrom(), tr.requestsFrom())
		return
	})
	g.Go(func() (err error) {
		external, err = s.telemetryQuery(ctx, `
WITH e AS (
  SELECT COALESCE(s.service, '') AS src, s.kind, COALESCE(NULLIF(s.target, ''), s.name) AS dst, s.duration_ms,
    CASE WHEN s.status = 'error' THEN 1 ELSE 0 END AS failed
  FROM spans s
  WHERE s.created_at >= {{from}} AND s.created_at < {{to}} AND s.kind IN ('db', 'http', 'cache', 'queue')
    AND (s.trace_id IS NULL OR NOT EXISTS (
      SELECT 1 FROM api_logs c WHERE c.trace_id = s.trace_id AND c.parent_span_id = s.span_id))
),
r AS (
  SELECT src, kind, dst, duration_ms, failed, `+p95Of("src, kind, dst")+`
  FROM e
)
SELECT src, kind, dst, COUNT(*) AS calls, SUM(failed) AS errors, `+p95Pick+` AS p95
FROM r GROUP BY src, kind, dst
ORDER BY calls DESC
LIMIT 200`, tr)
		return
	})
	if err := g.Wait(); err != nil {
		s.fail(w, err)
		return
	}

	minutes := tr.To.Sub(tr.From).Minutes()
	if minutes < 1 {
		minutes = 1
	}
	nodes := map[string]*mapNode{}
	node := func(kind, name string) *mapNode {
		id := kind + ":" + name
		if n := nodes[id]; n != nil {
			return n
		}
		n := &mapNode{ID: id, Kind: kind, Name: name}
		nodes[id] = n
		return n
	}
	for _, m := range rowMaps(services) {
		n := node("service", toString(m["service"]))
		n.Requests, n.Errors, n.P95 = toInt(m["requests"]), toInt(m["errors"]), floatOrNil(m["p95"])
	}
	edges := []mapEdge{}
	addEdge := func(src, dst *mapNode, kind string, m map[string]any) {
		e := mapEdge{Source: src.ID, Target: dst.ID, Kind: kind, Calls: toInt(m["calls"]), Errors: toInt(m["errors"]), P95: floatOrNil(m["p95"])}
		e.PerMin = round2(float64(e.Calls) / minutes)
		if e.Calls > 0 {
			e.ErrorRate = round2(100 * float64(e.Errors) / float64(e.Calls))
		}
		edges = append(edges, e)
	}
	for _, m := range rowMaps(internal) {
		addEdge(node("service", toString(m["src"])), node("service", toString(m["dst"])), "service", m)
	}
	for _, m := range rowMaps(external) {
		kind := toString(m["kind"])
		addEdge(node("service", toString(m["src"])), node(kind, toString(m["dst"])), kind, m)
	}
	list := make([]*mapNode, 0, len(nodes))
	for _, n := range nodes {
		list = append(list, n)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	writeJSON(w, http.StatusOK, map[string]any{"nodes": list, "edges": edges, "from": tr.From, "to": tr.To})
}

func floatOrNil(v any) *float64 {
	if v == nil {
		return nil
	}
	f, ok := asFloat(v)
	if !ok {
		return nil
	}
	return &f
}
