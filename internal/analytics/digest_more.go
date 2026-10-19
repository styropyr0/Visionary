package analytics

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The digest's wider sections: requests and 5xx per day, the routes that got
// slower or faster, application errors, uptime checks, machines, containers
// and Web Vitals. Each reads its own tables and is left out when they are
// missing or empty, so a project that only sends requests still gets a
// digest.

type digestDay struct {
	Day      string `json:"day"` // the day's start, tsLayout
	Requests int64  `json:"requests"`
	Errors   int64  `json:"errors_5xx"`
}

type digestChange struct {
	Route     string  `json:"route"`
	Requests  int64   `json:"requests"`
	P95       float64 `json:"p95_ms"`
	PrevP95   float64 `json:"previous_p95_ms"`
	ChangePct float64 `json:"change_pct"`
}

type digestBusy struct {
	Route        string `json:"route"`
	Requests     int64  `json:"requests"`
	PrevRequests int64  `json:"previous_requests"`
}

type digestErrors struct {
	Total     int64         `json:"total"`
	PrevTotal int64         `json:"previous_total"`
	NewIssues int64         `json:"new_issues"`
	Top       []digestIssue `json:"top"`
}

type digestIssue struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Route   string `json:"route"`
	Count   int64  `json:"count"`
	New     bool   `json:"new"` // first seen this week
}

type digestCheck struct {
	Check        string   `json:"check"`
	Availability float64  `json:"availability"`
	Prev         *float64 `json:"previous_availability"`
	Failures     int64    `json:"failures"`
	AvgMs        float64  `json:"avg_ms"`
}

type digestMachine struct {
	Instance string   `json:"instance"`
	AvgCPU   *float64 `json:"avg_cpu"`
	PeakCPU  *float64 `json:"peak_cpu"`
	PeakMem  *float64 `json:"peak_memory"`
	PeakDisk *float64 `json:"peak_disk"`
}

type digestContainers struct {
	Restarts  int64                   `json:"restarts"`
	OOMKills  int64                   `json:"oom_kills"`
	Exits     int64                   `json:"exits"`
	Unhealthy int64                   `json:"unhealthy"`
	Worst     []digestContainerEvents `json:"worst"`
}

type digestContainerEvents struct {
	Container string `json:"container"`
	Restarts  int64  `json:"restarts"`
	OOMKills  int64  `json:"oom_kills"`
	Unhealthy int64  `json:"unhealthy"`
}

type digestVital struct {
	Name    string   `json:"name"`
	P75     float64  `json:"p75"`
	PrevP75 *float64 `json:"previous_p75"`
	Rating  string   `json:"rating"` // good, needs_improvement, poor
}

// routeChanges splits the per-route p95s of both weeks into the biggest
// regressions and improvements (both weeks with enough traffic, at least 10%
// and 20 ms apart) and the busiest routes.
func routeChanges(cur, prev map[string]digestRoute) (worse, better []digestChange, busy []digestBusy) {
	for route, c := range cur {
		p, ok := prev[route]
		busy = append(busy, digestBusy{Route: route, Requests: c.Requests, PrevRequests: p.Requests})
		if !ok || c.Requests < 50 || p.Requests < 50 || p.P95 <= 0 {
			continue
		}
		pct := 100 * (c.P95 - p.P95) / p.P95
		if math.Abs(pct) < 10 || math.Abs(c.P95-p.P95) < 20 {
			continue
		}
		ch := digestChange{Route: route, Requests: c.Requests, P95: c.P95, PrevP95: p.P95, ChangePct: math.Round(pct)}
		if pct > 0 {
			worse = append(worse, ch)
		} else {
			better = append(better, ch)
		}
	}
	sort.Slice(worse, func(i, j int) bool { return worse[i].ChangePct > worse[j].ChangePct })
	sort.Slice(better, func(i, j int) bool { return better[i].ChangePct < better[j].ChangePct })
	sort.Slice(busy, func(i, j int) bool { return busy[i].Requests > busy[j].Requests })
	return head(worse, 5), head(better, 5), head(busy, 5)
}

// digestDays counts requests and 5xx per day of [mid, now).
func (s *Server) digestDays(ctx context.Context, mid, now time.Time) []digestDay {
	t := s.q.bucketOf("3600")
	res, err := s.db.Query(ctx, `SELECT `+t+` AS t, COUNT(*), `+countIf("status >= 500")+`
FROM api_logs WHERE created_at >= ? AND created_at < ? GROUP BY `+t, mid.UTC().Format(tsLayout), now.UTC().Format(tsLayout))
	if err != nil {
		s.logger.Info("digest: days unavailable", "err", err)
		return nil
	}
	days := make([]digestDay, 7)
	for i := range days {
		days[i].Day = mid.Add(time.Duration(i) * 24 * time.Hour).UTC().Format(tsLayout)
	}
	for _, row := range res.Rows {
		i := int((toInt(row[0]) - mid.Unix()) / 86400)
		if i < 0 || i >= len(days) {
			continue
		}
		days[i].Requests += toInt(row[1])
		days[i].Errors += toInt(row[2])
	}
	return days
}

// digestErrorsData reads application errors (the errors table) of both weeks.
func (s *Server) digestErrorsData(ctx context.Context, start, mid, now time.Time) *digestErrors {
	midTS := mid.UTC().Format(tsLayout)
	res, err := s.db.Query(ctx, `SELECT `+countIf("created_at >= ?")+`, `+countIf("created_at < ?")+`
FROM errors WHERE created_at >= ? AND created_at < ?`, midTS, midTS, start.UTC().Format(tsLayout), now.UTC().Format(tsLayout))
	if err != nil {
		if !missingTable(err, "errors") {
			s.logger.Info("digest: errors unavailable", "err", err)
		}
		return nil
	}
	d := &digestErrors{}
	if len(res.Rows) > 0 {
		d.Total, d.PrevTotal = toInt(res.Rows[0][0]), toInt(res.Rows[0][1])
	}
	if d.Total == 0 && d.PrevTotal == 0 {
		return nil
	}
	// The week's issues by fingerprint; new ones have no earlier occurrence
	// within the retention window.
	res, err = s.db.Query(ctx, `SELECT e.fingerprint, MIN(e.type), MIN(e.message), MIN(COALESCE(e.route, e.path, '')), COUNT(*),
  (SELECT MIN(o.created_at) FROM errors AS o WHERE o.fingerprint = e.fingerprint) AS first_seen
FROM errors AS e WHERE e.created_at >= ? AND e.created_at < ?
GROUP BY e.fingerprint ORDER BY COUNT(*) DESC LIMIT 200`, midTS, now.UTC().Format(tsLayout))
	if err != nil {
		s.logger.Info("digest: error issues unavailable", "err", err)
		return d
	}
	for _, row := range res.Rows {
		isNew := toString(row[5]) >= midTS
		if isNew {
			d.NewIssues++
		}
		if len(d.Top) < 5 {
			d.Top = append(d.Top, digestIssue{Type: toString(row[1]), Message: truncate(toString(row[2]), 160), Route: toString(row[3]), Count: toInt(row[4]), New: isNew})
		}
	}
	return d
}

// digestUptimeData is each check's availability this week and the week before.
func (s *Server) digestUptimeData(ctx context.Context, start, mid, now time.Time) []digestCheck {
	week := "CASE WHEN created_at >= '" + mid.UTC().Format(tsLayout) + "' THEN 1 ELSE 0 END"
	res, err := s.db.Query(ctx, `SELECT check_name, `+week+` AS wk, COUNT(*), `+countIf("ok = 0")+`, AVG(duration_ms)
FROM uptime_results WHERE created_at >= ? AND created_at < ? GROUP BY check_name, `+week, start.UTC().Format(tsLayout), now.UTC().Format(tsLayout))
	if err != nil {
		if !missingTable(err, "uptime_results") {
			s.logger.Info("digest: uptime unavailable", "err", err)
		}
		return nil
	}
	cur, prev := map[string]digestCheck{}, map[string]float64{}
	for _, row := range res.Rows {
		n, fails := toInt(row[2]), toInt(row[3])
		if n == 0 {
			continue
		}
		avail := math.Round(10000*float64(n-fails)/float64(n)) / 100
		if toInt(row[1]) == 1 {
			avg, _ := cellFloat(row[4])
			cur[toString(row[0])] = digestCheck{Check: toString(row[0]), Availability: avail, Failures: fails, AvgMs: math.Round(avg)}
		} else {
			prev[toString(row[0])] = avail
		}
	}
	out := make([]digestCheck, 0, len(cur))
	for name, c := range cur {
		if p, ok := prev[name]; ok {
			c.Prev = ptr(p)
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Availability < out[j].Availability })
	return head(out, 10)
}

// digestMachinesData is each instance's CPU, memory and disk over the week,
// busiest first.
func (s *Server) digestMachinesData(ctx context.Context, mid, now time.Time) []digestMachine {
	res, err := s.db.Query(ctx, `SELECT instance, AVG(host_cpu_percent), MAX(host_cpu_percent), MAX(host_mem_percent), MAX(disk_used_percent)
FROM system_metrics WHERE created_at >= ? AND created_at < ? GROUP BY instance ORDER BY MAX(host_cpu_percent) DESC LIMIT 8`,
		mid.UTC().Format(tsLayout), now.UTC().Format(tsLayout))
	if err != nil {
		if !missingTable(err, "system_metrics") {
			s.logger.Info("digest: machines unavailable", "err", err)
		}
		return nil
	}
	var out []digestMachine
	for _, row := range res.Rows {
		r := func(i int) *float64 {
			if v, ok := cellFloat(row[i]); ok {
				return ptr(math.Round(v*10) / 10)
			}
			return nil
		}
		out = append(out, digestMachine{Instance: toString(row[0]), AvgCPU: r(1), PeakCPU: r(2), PeakMem: r(3), PeakDisk: r(4)})
	}
	return out
}

// digestContainersData counts the week's container events.
func (s *Server) digestContainersData(ctx context.Context, mid, now time.Time) *digestContainers {
	res, err := s.db.Query(ctx, `SELECT container, kind, SUM(count) FROM container_events
WHERE created_at >= ? AND created_at < ? GROUP BY container, kind`, mid.UTC().Format(tsLayout), now.UTC().Format(tsLayout))
	if err != nil {
		if !missingTable(err, "container_events") {
			s.logger.Info("digest: containers unavailable", "err", err)
		}
		return nil
	}
	if len(res.Rows) == 0 {
		return nil
	}
	d := &digestContainers{}
	by := map[string]*digestContainerEvents{}
	for _, row := range res.Rows {
		name, n := toString(row[0]), toInt(row[2])
		c := by[name]
		if c == nil {
			c = &digestContainerEvents{Container: name}
			by[name] = c
		}
		switch toString(row[1]) {
		case "restarted":
			d.Restarts += n
			c.Restarts += n
		case "oom_killed":
			d.OOMKills += n
			c.OOMKills += n
		case "exited":
			d.Exits += n
		case "unhealthy":
			d.Unhealthy += n
			c.Unhealthy += n
		}
	}
	for _, c := range by {
		if c.Restarts+c.OOMKills+c.Unhealthy > 0 {
			d.Worst = append(d.Worst, *c)
		}
	}
	sort.Slice(d.Worst, func(i, j int) bool {
		a, b := d.Worst[i], d.Worst[j]
		return a.OOMKills*10+a.Restarts+a.Unhealthy > b.OOMKills*10+b.Restarts+b.Unhealthy
	})
	d.Worst = head(d.Worst, 5)
	return d
}

// digestVitalsData is the 75th percentile of LCP, INP and CLS in both weeks.
func (s *Server) digestVitalsData(ctx context.Context, start, mid, now time.Time) []digestVital {
	week := "CASE WHEN created_at >= '" + mid.UTC().Format(tsLayout) + "' THEN 1 ELSE 0 END"
	res, err := s.db.Query(ctx, `WITH v AS (
  SELECT `+week+` AS wk, name, value,
    ROW_NUMBER() OVER (PARTITION BY `+week+`, name ORDER BY value) AS rn,
    COUNT(*) OVER (PARTITION BY `+week+`, name) AS n
  FROM client_events WHERE kind = 'web_vital' AND name IN ('LCP', 'INP', 'CLS') AND value IS NOT NULL
    AND created_at >= ? AND created_at < ?
)
SELECT wk, name, MIN(CASE WHEN rn >= 0.75 * n THEN value END) FROM v GROUP BY wk, name`, start.UTC().Format(tsLayout), now.UTC().Format(tsLayout))
	if err != nil {
		if !missingTable(err, "client_events") {
			s.logger.Info("digest: web vitals unavailable", "err", err)
		}
		return nil
	}
	cur, prev := map[string]float64{}, map[string]float64{}
	for _, row := range res.Rows {
		v, ok := cellFloat(row[2])
		if !ok {
			continue
		}
		if toInt(row[0]) == 1 {
			cur[toString(row[1])] = v
		} else {
			prev[toString(row[1])] = v
		}
	}
	var out []digestVital
	for _, name := range []string{"LCP", "INP", "CLS"} {
		v, ok := cur[name]
		if !ok {
			continue
		}
		prec := 0
		if name == "CLS" {
			prec = 3
		}
		r := math.Pow(10, float64(prec))
		vt := digestVital{Name: name, P75: math.Round(v*r) / r, Rating: "good"}
		if p, ok := prev[name]; ok {
			vt.PrevP75 = ptr(math.Round(p*r) / r)
		}
		for _, th := range vitalThresholds {
			good, _ := strconv.ParseFloat(th.good, 64)
			poor, _ := strconv.ParseFloat(th.poor, 64)
			switch {
			case th.name != name:
			case v > poor:
				vt.Rating = "poor"
			case v > good:
				vt.Rating = "needs_improvement"
			}
		}
		out = append(out, vt)
	}
	return out
}

// moreDigestLines are the text lines of these sections, for Slack, Discord
// and the plain-text part of the email.
func moreDigestLines(d *digestData) []string {
	var lines []string
	if e := d.Errors; e != nil {
		l := fmt.Sprintf("Errors: %d, was %d", e.Total, e.PrevTotal)
		if e.NewIssues > 0 {
			l += fmt.Sprintf("; %d new issue(s)", e.NewIssues)
		}
		lines = append(lines, l)
		for _, i := range head(e.Top, 3) {
			tag := ""
			if i.New {
				tag = " [new]"
			}
			lines = append(lines, fmt.Sprintf("  %s: %s (%d)%s", i.Type, truncate(i.Message, 80), i.Count, tag))
		}
	}
	if len(d.Uptime) > 0 {
		lines = append(lines, "Uptime:")
		for _, c := range d.Uptime {
			lines = append(lines, fmt.Sprintf("  %s: %.2f%% (%d failed checks)", c.Check, c.Availability, c.Failures))
		}
	}
	for _, m := range d.Machines {
		if (m.PeakCPU != nil && *m.PeakCPU >= 90) || (m.PeakMem != nil && *m.PeakMem >= 90) {
			lines = append(lines, fmt.Sprintf("Machine %s peaked at %s CPU and %s memory", m.Instance, pctOrNA(m.PeakCPU), pctOrNA(m.PeakMem)))
		}
	}
	if c := d.Containers; c != nil && c.Restarts+c.OOMKills+c.Unhealthy > 0 {
		lines = append(lines, fmt.Sprintf("Containers: %d restart(s), %d out-of-memory kill(s), %d failed health check(s)", c.Restarts, c.OOMKills, c.Unhealthy))
	}
	if len(d.Vitals) > 0 {
		var v []string
		for _, x := range d.Vitals {
			v = append(v, fmt.Sprintf("%s %s (%s)", x.Name, vitalText(x.Name, x.P75), strings.ReplaceAll(x.Rating, "_", " ")))
		}
		lines = append(lines, "Web Vitals p75: "+strings.Join(v, ", "))
	}
	return lines
}

func pctOrNA(v *float64) string {
	if v == nil {
		return "n/a"
	}
	return formatUnit(*v, "percent")
}

func vitalText(name string, v float64) string {
	if name == "CLS" {
		return strconv.FormatFloat(v, 'f', 3, 64)
	}
	return formatUnit(v, "ms")
}
