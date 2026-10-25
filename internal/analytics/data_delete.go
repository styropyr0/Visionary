package analytics

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	sdk "github.com/styropyr0/visionary/sdks/go"
)

// Deleting collected data from Settings → Data: chosen kinds of data, within
// a time range, optionally for one service. Admins only, audited. The audit
// log, settings, keys, dashboards, alert rules and uptime checks are never
// touched: only collected rows are.

type dataTable struct {
	name    string
	service bool // has a service column the service filter applies to
}

type dataKind struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Service     bool   `json:"service"` // the service filter applies
	tables      []dataTable
}

var dataKinds = []dataKind{
	{Key: "requests", Label: "Request logs", Description: "Every request with its headers and bodies, and the request charts' rollups.", Service: true,
		tables: []dataTable{{"api_logs", true}}},
	{Key: "spans", Label: "Calls inside requests", Description: "Database queries, outbound calls and custom spans.", Service: true,
		tables: []dataTable{{"spans", true}}},
	{Key: "errors", Label: "Errors", Description: "Captured exceptions and panics.", Service: true,
		tables: []dataTable{{"errors", true}}},
	{Key: "system", Label: "System metrics", Description: "CPU, memory, disk and runtime samples from SDKs and host agents, and their rollups.", Service: true,
		tables: []dataTable{{"system_metrics", true}}},
	{Key: "profiles", Label: "CPU profiles", Description: "Sampled CPU profiles and their flame graphs.", Service: true,
		tables: []dataTable{{"profiles", true}}},
	{Key: "releases", Label: "Deploys", Description: "Version starts: the deploy marks on charts.", Service: true,
		tables: []dataTable{{"releases", true}}},
	{Key: "frontend", Label: "Frontend events", Description: "Web Vitals, page and screen loads, client errors.",
		tables: []dataTable{{"client_events", false}}},
	{Key: "uptime", Label: "Uptime results", Description: "Check results and their timings. The checks themselves stay.",
		tables: []dataTable{{"uptime_results", false}}},
	{Key: "alerts", Label: "Alert history", Description: "Past alert firings. The rules stay.",
		tables: []dataTable{{"alert_events", false}, {"alert_deliveries", false}}},
	{Key: "security", Label: "Security findings", Description: "Findings from the detectors and host events.", Service: true,
		tables: []dataTable{{"security_events", true}}},
	{Key: "hosts", Label: "Host agent data", Description: "Processes, mounts, services, containers and their metrics and events.",
		tables: []dataTable{{"host_processes", false}, {"host_mounts", false}, {"host_units", false}, {"host_containers", false}, {"container_metrics", false}, {"container_events", false}}},
	{Key: "plans", Label: "Query plans", Description: "EXPLAIN plans asked for from the Queries page.",
		tables: []dataTable{{"explain_requests", false}}},
}

func (s *Server) dataRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/data/kinds", s.authed(s.handleDataKinds))
	mux.Handle("POST /api/data/preview", s.authed(s.handleDataPreview))
	mux.Handle("POST /api/data/delete", s.authed(s.handleDataDelete))
}

func (s *Server) handleDataKinds(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"kinds": dataKinds})
}

type dataRequest struct {
	Kinds   []string `json:"kinds"`
	From    string   `json:"from"` // empty: from the beginning
	To      string   `json:"to"`   // empty: up to now
	Service *string  `json:"service"`
}

// where is the condition shared by every table of a kind, with its args.
func (d dataRequest) where(t dataTable) (string, []any, bool) {
	var conds []string
	var args []any
	if d.From != "" {
		conds, args = append(conds, "created_at >= ?"), append(args, d.From)
	}
	if d.To != "" {
		conds, args = append(conds, "created_at < ?"), append(args, d.To)
	}
	if d.Service != nil {
		if !t.service {
			return "", nil, false // a service was chosen and this table has none
		}
		conds, args = append(conds, "COALESCE(service, '') = ?"), append(args, *d.Service)
	}
	if len(conds) == 0 {
		return "1 = 1", nil, true
	}
	return strings.Join(conds, " AND "), args, true
}

func (s *Server) readDataRequest(w http.ResponseWriter, r *http.Request) (dataRequest, []dataKind, bool) {
	if !s.require(w, r, "admin") {
		return dataRequest{}, nil, false
	}
	var d dataRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&d); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return d, nil, false
	}
	for _, ts := range []*string{&d.From, &d.To} {
		if *ts == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, *ts)
		if err != nil {
			writeError(w, http.StatusBadRequest, "from and to must be RFC 3339 times")
			return d, nil, false
		}
		*ts = t.UTC().Format(tsLayout)
	}
	if d.From != "" && d.To != "" && d.From >= d.To {
		writeError(w, http.StatusBadRequest, "the range ends before it starts")
		return d, nil, false
	}
	var kinds []dataKind
	for _, k := range dataKinds {
		if slices.Contains(d.Kinds, k.Key) {
			kinds = append(kinds, k)
		}
	}
	if len(kinds) == 0 || len(kinds) != len(d.Kinds) {
		writeError(w, http.StatusBadRequest, "choose the data to delete")
		return d, nil, false
	}
	return d, kinds, true
}

type dataCount struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	Rows  int64  `json:"rows"`
	// Skipped says why nothing of this kind is counted (no service column).
	Skipped string `json:"skipped,omitempty"`
}

func (s *Server) countData(ctx context.Context, d dataRequest, kinds []dataKind) ([]dataCount, error) {
	var out []dataCount
	for _, k := range kinds {
		c := dataCount{Kind: k.Key, Label: k.Label}
		if d.Service != nil && !k.Service {
			c.Skipped = "not kept per service"
			out = append(out, c)
			continue
		}
		for _, t := range k.tables {
			cond, args, ok := d.where(t)
			if !ok {
				continue
			}
			res, err := s.db.Query(ctx, `SELECT COUNT(*) FROM `+t.name+` WHERE `+cond, args...)
			if err != nil {
				if missingTable(err, t.name) {
					continue
				}
				return nil, dbErr(err)
			}
			if len(res.Rows) > 0 {
				c.Rows += toInt(res.Rows[0][0])
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// handleDataPreview counts the rows a delete would remove.
func (s *Server) handleDataPreview(w http.ResponseWriter, r *http.Request) {
	d, kinds, ok := s.readDataRequest(w, r)
	if !ok {
		return
	}
	counts, err := s.countData(r.Context(), d, kinds)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"counts": counts})
}

// handleDataDelete removes the rows, 5000 at a time so no statement hits
// D1's limits, along with the rollups built from them in the same range.
func (s *Server) handleDataDelete(w http.ResponseWriter, r *http.Request) {
	d, kinds, ok := s.readDataRequest(w, r)
	if !ok {
		return
	}
	// A delete can take a while; it finishes even if the browser goes away.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Minute)
	defer cancel()
	counts, err := s.countData(ctx, d, kinds)
	if err != nil {
		s.fail(w, err)
		return
	}
	for _, k := range kinds {
		if d.Service != nil && !k.Service {
			continue
		}
		var instances []string
		if k.Key == "system" && d.Service != nil {
			// rollup_system is per instance: find the service's instances first.
			if instances, err = s.serviceInstances(ctx, d); err != nil {
				s.fail(w, err)
				return
			}
		}
		for _, t := range k.tables {
			if err := s.deleteChunked(ctx, d, t); err != nil {
				s.fail(w, err)
				return
			}
		}
		if err := s.deleteRollups(ctx, d, k.Key, instances); err != nil {
			s.fail(w, err)
			return
		}
	}
	var total int64
	for _, c := range counts {
		total += c.Rows
	}
	service := "every service"
	if d.Service != nil {
		service = *d.Service
	}
	s.audit(r, "data.delete", strings.Join(d.Kinds, ","), map[string]any{"from": d.From, "to": d.To, "service": service, "rows": total})
	writeJSON(w, http.StatusOK, map[string]any{"counts": counts, "rows": total})
}

func (s *Server) deleteChunked(ctx context.Context, d dataRequest, t dataTable) error {
	cond, args, ok := d.where(t)
	if !ok {
		return nil
	}
	stmt := `DELETE FROM ` + t.name + ` WHERE id IN (SELECT id FROM ` + t.name + ` WHERE ` + cond + ` LIMIT 5000)`
	if s.q.d == sdk.MySQL {
		stmt = `DELETE FROM ` + t.name + ` WHERE ` + cond + ` ORDER BY id LIMIT 5000`
	}
	for {
		// The database layer does not report affected rows: look for one more.
		res, err := s.db.Query(ctx, `SELECT id FROM `+t.name+` WHERE `+cond+` LIMIT 1`, args...)
		if err != nil {
			if missingTable(err, t.name) {
				return nil
			}
			return dbErr(err)
		}
		if len(res.Rows) == 0 {
			return nil
		}
		if err := s.db.Exec(ctx, stmt, args...); err != nil {
			return dbErr(err)
		}
	}
}

// serviceInstances lists the instances only this service reported from in
// the range. An instance name shared with another service (two services on
// one machine) keeps its rollups: they mix both services' samples.
func (s *Server) serviceInstances(ctx context.Context, d dataRequest) ([]string, error) {
	all := d
	all.Service = nil
	cond, args, _ := all.where(dataTable{"system_metrics", true})
	res, err := s.db.Query(ctx, `SELECT instance, MIN(COALESCE(service, '')), MAX(COALESCE(service, '')) FROM system_metrics WHERE `+cond+` GROUP BY instance`, args...)
	if err != nil {
		if missingTable(err, "system_metrics") {
			return nil, nil
		}
		return nil, dbErr(err)
	}
	var out []string
	for _, row := range res.Rows {
		if toString(row[1]) == *d.Service && toString(row[2]) == *d.Service {
			out = append(out, toString(row[0]))
		}
	}
	return out, nil
}

// deleteRollups removes the pre-aggregated rows of the range, so charts over
// long ranges agree with the raw data that is left. They are not rebuilt:
// the rollup job only moves forward and fills in history it never covered.
func (s *Server) deleteRollups(ctx context.Context, d dataRequest, kind string, instances []string) error {
	var table, extra string
	var extraArgs []any
	switch kind {
	case "requests":
		table = "rollup_requests"
		if d.Service != nil {
			extra, extraArgs = " AND service = ?", []any{*d.Service}
		}
	case "system":
		table = "rollup_system"
		if d.Service != nil {
			if len(instances) == 0 {
				return nil
			}
			extra = " AND instance IN (?" + strings.Repeat(", ?", len(instances)-1) + ")"
			for _, i := range instances {
				extraArgs = append(extraArgs, i)
			}
		}
	default:
		return nil
	}
	cond, args := "1 = 1", []any{}
	if d.From != "" {
		cond, args = cond+" AND created_at >= ?", append(args, d.From)
	}
	if d.To != "" {
		cond, args = cond+" AND created_at < ?", append(args, d.To)
	}
	err := s.db.Exec(ctx, `DELETE FROM `+table+` WHERE `+cond+extra, append(args, extraArgs...)...)
	if err != nil && !missingTable(err, table) {
		return dbErr(err)
	}
	return nil
}
