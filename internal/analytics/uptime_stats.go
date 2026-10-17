package analytics

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// checkState is a check's current status with the history the list, the
// detail header and the status page draw: daily bars and a 24h sparkline.
type checkState struct {
	State          string     `json:"state"` // up, degraded, down, paused, pending
	LastAt         string     `json:"last_at,omitempty"`
	LastStatus     int64      `json:"last_status"`
	LastDurationMs float64    `json:"last_duration_ms"`
	LastError      string     `json:"last_error,omitempty"`
	CertExpiresAt  string     `json:"cert_expires_at,omitempty"`
	IncidentSince  string     `json:"incident_since,omitempty"`
	Uptime24h      *float64   `json:"uptime_24h"`
	Days           []dayBar   `json:"days"`
	Spark          []*float64 `json:"spark"` // hourly average response time, last 24h
}

type dayBar struct {
	Day      string   `json:"day"` // YYYY-MM-DD, UTC
	Checks   int64    `json:"checks"`
	Failed   int64    `json:"failed"`
	Degraded int64    `json:"degraded"`
	Uptime   *float64 `json:"uptime"` // nil when the check did not run that day
}

func pct(up, n int64) *float64 {
	if n == 0 {
		return nil
	}
	v := float64(int64(100000*float64(up)/float64(n))) / 1000 // floor to 3 decimals: 99.9996 is not 100
	return &v
}

// uptimeStates reads the state of many checks with three grouped queries,
// run in parallel because each is a round trip on D1.
func (s *Server) uptimeStates(ctx context.Context, checks []UptimeCheck, days int) (map[int64]*checkState, error) {
	now := time.Now().UTC()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -(days - 1))
	hourStart := now.Truncate(time.Hour).Add(-23 * time.Hour)
	since24 := now.Add(-24 * time.Hour).Format(tsLayout)

	queries := []struct {
		sql  string
		args []any
	}{
		{`SELECT check_id, SUBSTR(created_at, 1, 10) AS day, COUNT(*) AS n, ` + countIf("ok = 0") + ` AS failed, ` + countIf("degraded = 1") + ` AS slow
FROM uptime_results WHERE created_at >= ? GROUP BY check_id, SUBSTR(created_at, 1, 10)`, []any{dayStart.Format(tsLayout)}},
		{`SELECT check_id, SUBSTR(created_at, 1, 13) AS hour, AVG(CASE WHEN ok = 1 THEN duration_ms END) AS avg_ms
FROM uptime_results WHERE created_at >= ? GROUP BY check_id, SUBSTR(created_at, 1, 13)`, []any{hourStart.Format(tsLayout)}},
		{`SELECT check_id, COUNT(*) AS n, ` + countIf("ok = 1") + ` AS up FROM uptime_results WHERE created_at >= ? GROUP BY check_id`, []any{since24}},
		{`SELECT r.check_id, r.created_at, r.ok, r.status, r.duration_ms, r.error, r.degraded, r.cert_expires_at
FROM uptime_results r JOIN (
  SELECT check_id, MAX(created_at) AS latest FROM uptime_results WHERE created_at >= ? GROUP BY check_id
) l ON r.check_id = l.check_id AND r.created_at = l.latest`, []any{now.Add(-48 * time.Hour).Format(tsLayout)}},
	}
	results := make([][][]any, len(queries))
	errs := make([]error, len(queries))
	var wg sync.WaitGroup
	for i, q := range queries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows, err := s.db.Query(ctx, q.sql, q.args...)
			if err == nil {
				results[i] = rows.Rows
			}
			errs[i] = err
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}

	out := make(map[int64]*checkState, len(checks))
	dayIndex := map[string]int{}
	for i := range days {
		dayIndex[dayStart.AddDate(0, 0, i).Format("2006-01-02")] = i
	}
	hourIndex := map[string]int{}
	for i := range 24 {
		hourIndex[hourStart.Add(time.Duration(i)*time.Hour).Format("2006-01-02T15")] = i
	}
	for _, c := range checks {
		st := &checkState{State: "pending", Days: make([]dayBar, days), Spark: make([]*float64, 24)}
		for d, i := range dayIndex {
			st.Days[i].Day = d
		}
		if !c.Enabled {
			st.State = "paused"
		}
		out[c.ID] = st
	}
	for _, row := range results[0] {
		st, i, ok := out[toInt(row[0])], 0, false
		if st != nil {
			i, ok = dayIndex[toString(row[1])]
		}
		if !ok {
			continue
		}
		b := &st.Days[i]
		b.Checks, b.Failed, b.Degraded = toInt(row[2]), toInt(row[3]), toInt(row[4])
		b.Uptime = pct(b.Checks-b.Failed, b.Checks)
	}
	for _, row := range results[1] {
		st, i, ok := out[toInt(row[0])], 0, false
		if st != nil {
			i, ok = hourIndex[toString(row[1])]
		}
		if v, isNum := asFloat(row[2]); ok && isNum {
			st.Spark[i] = &v
		}
	}
	for _, row := range results[2] {
		if st := out[toInt(row[0])]; st != nil {
			st.Uptime24h = pct(toInt(row[2]), toInt(row[1]))
		}
	}
	for _, row := range results[3] {
		st := out[toInt(row[0])]
		if st == nil {
			continue
		}
		st.LastAt = toString(row[1])
		st.LastStatus = toInt(row[3])
		st.LastDurationMs, _ = asFloat(row[4])
		st.LastError = toString(row[5])
		st.CertExpiresAt = toString(row[7])
		if st.State == "paused" {
			continue
		}
		switch {
		case toInt(row[2]) != 1:
			st.State = "down"
		case toInt(row[6]) == 1:
			st.State = "degraded"
		default:
			st.State = "up"
		}
	}
	for id, st := range out {
		if st.State != "down" {
			continue
		}
		since, err := s.incidentSince(ctx, id, now.AddDate(0, 0, -days))
		if err != nil {
			return nil, err
		}
		st.IncidentSince = since
	}
	return out, nil
}

// incidentSince is when the current run of failures began: the first failure
// after the last success.
func (s *Server) incidentSince(ctx context.Context, id int64, floor time.Time) (string, error) {
	rows, err := s.db.Query(ctx, `SELECT MIN(created_at) FROM uptime_results
WHERE check_id = ? AND ok = 0 AND created_at > COALESCE((
  SELECT MAX(created_at) FROM uptime_results WHERE check_id = ? AND ok = 1 AND created_at >= ?
), ?)`, id, id, floor.Format(tsLayout), floor.Format(tsLayout))
	if err != nil || len(rows.Rows) == 0 {
		return "", err
	}
	return toString(rows.Rows[0][0]), nil
}

var uptimeWindows = []struct {
	key string
	d   time.Duration
}{{"24h", 24 * time.Hour}, {"7d", 7 * 24 * time.Hour}, {"30d", 30 * 24 * time.Hour}, {"90d", 90 * 24 * time.Hour}}

// handleUptimeSummary: uptime over the standard windows, 24h latency, the
// current state and 90 daily bars.
func (s *Server) handleUptimeSummary(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadCheck(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	now := time.Now().UTC()
	sel := ""
	for _, win := range uptimeWindows {
		since := "'" + now.Add(-win.d).Format(tsLayout) + "'"
		sel += countIf("created_at >= "+since) + " AS n_" + win.key + ", " + countIf("created_at >= "+since+" AND ok = 1") + " AS up_" + win.key + ", "
	}
	counts, err := s.db.Query(ctx, `SELECT `+sel+countIf("ok = 0")+` AS failed FROM uptime_results WHERE check_id = ? AND created_at >= ?`,
		c.ID, now.Add(-90*24*time.Hour).Format(tsLayout))
	if err != nil {
		s.fail(w, err)
		return
	}
	uptime := map[string]any{}
	checksRun := map[string]int64{}
	if len(counts.Rows) == 1 {
		row := counts.Rows[0]
		for i, win := range uptimeWindows {
			n, up := toInt(row[2*i]), toInt(row[2*i+1])
			uptime[win.key] = pct(up, n)
			checksRun[win.key] = n
		}
	}
	lat, err := s.db.Query(ctx, `WITH o AS (
  SELECT duration_ms, ROW_NUMBER() OVER (ORDER BY duration_ms) AS rn, COUNT(*) OVER () AS n
  FROM uptime_results WHERE check_id = ? AND ok = 1 AND created_at >= ?
)
SELECT AVG(duration_ms) AS avg_ms,
  MIN(CASE WHEN rn >= 0.50 * n THEN duration_ms END) AS p50,
  MIN(CASE WHEN rn >= 0.95 * n THEN duration_ms END) AS p95
FROM o`, c.ID, now.Add(-24*time.Hour).Format(tsLayout))
	if err != nil {
		s.fail(w, err)
		return
	}
	latency := map[string]any{}
	if len(lat.Rows) == 1 {
		for i, k := range []string{"avg_ms", "p50", "p95"} {
			latency[k] = lat.Rows[0][i]
		}
	}
	states, err := s.uptimeStates(ctx, []UptimeCheck{c}, 90)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"check":      c.masked(),
		"state":      states[c.ID],
		"uptime":     uptime,
		"checks_run": checksRun,
		"latency":    latency,
		"can_edit":   s.can(r, "edit"),
	})
}

// uptimePanels are per-check time series and tables over the dashboard range.
// {{check}} is the check id, an integer parsed by the handler.
var uptimePanels = map[string]string{
	"latency": `WITH b AS (
  SELECT {{t}} AS t, duration_ms FROM uptime_results
  WHERE check_id = {{check}} AND ok = 1 AND created_at >= {{from}} AND created_at < {{to}}
),
r AS (
  SELECT t, duration_ms,
    ROW_NUMBER() OVER (PARTITION BY t ORDER BY duration_ms) AS rn,
    COUNT(*) OVER (PARTITION BY t) AS n
  FROM b
)
SELECT t,
  MIN(CASE WHEN rn >= 0.50 * n THEN duration_ms END) AS p50,
  MIN(CASE WHEN rn >= 0.95 * n THEN duration_ms END) AS p95
FROM r GROUP BY t ORDER BY t`,
	"phases": `SELECT {{t}} AS t, AVG(dns_ms) AS dns, AVG(connect_ms) AS connect, AVG(tls_ms) AS tls, AVG(ttfb_ms) AS ttfb
FROM uptime_results WHERE check_id = {{check}} AND ok = 1 AND created_at >= {{from}} AND created_at < {{to}}
GROUP BY 1 ORDER BY 1`,
	"availability": `SELECT {{t}} AS t, COUNT(*) AS checks, ` + countIf("ok = 0") + ` AS failed
FROM uptime_results WHERE check_id = {{check}} AND created_at >= {{from}} AND created_at < {{to}}
GROUP BY 1 ORDER BY 1`,
	"results": `SELECT created_at, ok, status, duration_ms, dns_ms, connect_ms, tls_ms, ttfb_ms, degraded, retried, error
FROM uptime_results WHERE check_id = {{check}} AND created_at >= {{from}} AND created_at < {{to}}
ORDER BY created_at DESC LIMIT 500`,
}

func (s *Server) handleUptimePanel(w http.ResponseWriter, r *http.Request) {
	tpl, ok := uptimePanels[r.PathValue("panel")]
	if !ok {
		writeError(w, http.StatusNotFound, "unknown panel")
		return
	}
	id, ok := checkID(w, r)
	if !ok {
		return
	}
	tr, err := parseRange(r.URL.Query(), s.cfg.MaxRangeDays)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := s.runTemplate(r.Context(), withCheck(tpl, id), tr, true)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func withCheck(tpl string, id int64) string {
	return strings.ReplaceAll(tpl, "{{check}}", strconv.FormatInt(id, 10))
}
