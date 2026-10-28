package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// The weekly digest: last week against the week before (traffic, p95, 5xx,
// slowest routes, incidents from alert_events, new versions from releases),
// plus the sections of digest_more.go and security_digest.go.
// With a model configured the analyst writes the summary; otherwise it is a
// plain list of the numbers. Each channel opts in with its own weekday and
// hour, in the digest time zone from Settings → Server (or the server's zone).

type digestWeek struct {
	Requests  int64    `json:"requests"`
	P95       *float64 `json:"p95_ms"`
	ErrorRate *float64 `json:"error_rate"`
	Incidents int64    `json:"incidents"`
}

type digestRoute struct {
	Route    string   `json:"route"`
	Requests int64    `json:"requests"`
	P95      float64  `json:"p95_ms"`
	PrevP95  *float64 `json:"previous_p95_ms"`
}

type digestIncident struct {
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Count    int64  `json:"count"`
}

type digestRelease struct {
	Version   string `json:"version"`
	Service   string `json:"service"`
	FirstSeen string `json:"first_seen"`
}

type digestData struct {
	From      string           `json:"from"`
	To        string           `json:"to"`
	This      digestWeek       `json:"this_week"`
	Last      digestWeek       `json:"previous_week"`
	Slowest   []digestRoute    `json:"slowest_routes"`
	Incidents []digestIncident `json:"incidents"`
	Releases  []digestRelease  `json:"releases"`
	Security  *securityDigest  `json:"security,omitempty"` // security_digest.go

	// digest_more.go
	Days       []digestDay       `json:"days"`
	Regressed  []digestChange    `json:"regressed_routes"`
	Improved   []digestChange    `json:"improved_routes"`
	Busiest    []digestBusy      `json:"busiest_routes"`
	Errors     *digestErrors     `json:"errors,omitempty"`
	Uptime     []digestCheck     `json:"uptime"`
	Machines   []digestMachine   `json:"machines"`
	Containers *digestContainers `json:"containers,omitempty"`
	Vitals     []digestVital     `json:"web_vitals"`
	Logs       *digestLogs       `json:"logs,omitempty"`
}

func (s *Server) digestData(ctx context.Context, now time.Time) (*digestData, error) {
	mid := now.Add(-7 * 24 * time.Hour)
	start := now.Add(-14 * 24 * time.Hour)
	d := &digestData{From: mid.UTC().Format(tsLayout), To: now.UTC().Format(tsLayout)}
	midTS := "'" + mid.UTC().Format(tsLayout) + "'"
	week := "CASE WHEN created_at >= " + midTS + " THEN 1 ELSE 0 END"
	span := []any{start.UTC().Format(tsLayout), now.UTC().Format(tsLayout)}

	res, err := s.db.Query(ctx, `WITH o AS (
  SELECT `+week+` AS wk, status, duration_ms,
    ROW_NUMBER() OVER (PARTITION BY `+week+` ORDER BY duration_ms) AS rn,
    COUNT(*) OVER (PARTITION BY `+week+`) AS n
  FROM api_logs WHERE created_at >= ? AND created_at < ?
)
SELECT wk, COUNT(*) AS requests, MIN(CASE WHEN rn >= 0.95 * n THEN duration_ms END) AS p95,
  100.0 * `+countIf("status >= 500")+` / NULLIF(COUNT(*), 0) AS error_rate
FROM o GROUP BY wk`, span...)
	if err != nil {
		return nil, err
	}
	for _, row := range res.Rows {
		w := digestWeek{Requests: toInt(row[1]), P95: cellPtr(row[2]), ErrorRate: cellPtr(row[3])}
		if toInt(row[0]) == 1 {
			d.This = w
		} else {
			d.Last = w
		}
	}

	res, err = s.db.Query(ctx, `WITH o AS (
  SELECT `+week+` AS wk, COALESCE(route, path) AS route, duration_ms,
    ROW_NUMBER() OVER (PARTITION BY `+week+`, COALESCE(route, path) ORDER BY duration_ms) AS rn,
    COUNT(*) OVER (PARTITION BY `+week+`, COALESCE(route, path)) AS n
  FROM api_logs WHERE created_at >= ? AND created_at < ?
)
SELECT wk, route, COUNT(*) AS requests, MIN(CASE WHEN rn >= 0.95 * n THEN duration_ms END) AS p95
FROM o GROUP BY wk, route HAVING COUNT(*) >= 20`, span...)
	if err != nil {
		return nil, err
	}
	cur, prev := map[string]digestRoute{}, map[string]digestRoute{}
	for _, row := range res.Rows {
		p, _ := cellFloat(row[3])
		r := digestRoute{Route: toString(row[1]), Requests: toInt(row[2]), P95: p}
		if toInt(row[0]) == 1 {
			cur[r.Route] = r
			d.Slowest = append(d.Slowest, r)
		} else {
			prev[r.Route] = r
		}
	}
	sort.Slice(d.Slowest, func(i, j int) bool { return d.Slowest[i].P95 > d.Slowest[j].P95 })
	d.Slowest = head(d.Slowest, 5)
	for i := range d.Slowest {
		if p, ok := prev[d.Slowest[i].Route]; ok {
			d.Slowest[i].PrevP95 = ptr(p.P95)
		}
	}
	d.Regressed, d.Improved, d.Busiest = routeChanges(cur, prev)

	res, err = s.db.Query(ctx, `SELECT `+week+` AS wk, rule_name, severity, COUNT(*) FROM alert_events
WHERE created_at >= ? AND created_at < ? AND state = 'firing' GROUP BY `+week+`, rule_name, severity ORDER BY COUNT(*) DESC`, span...)
	if err != nil {
		return nil, err
	}
	for _, row := range res.Rows {
		n := toInt(row[3])
		if toInt(row[0]) == 1 {
			d.This.Incidents += n
			d.Incidents = append(d.Incidents, digestIncident{Rule: toString(row[1]), Severity: toString(row[2]), Count: n})
		} else {
			d.Last.Incidents += n
		}
	}
	d.Incidents = head(d.Incidents, 8)

	// Releases are optional: older databases may not have the table yet.
	res, err = s.db.Query(ctx, `SELECT version, COALESCE(service, ''), MIN(created_at) AS first_seen FROM releases
GROUP BY version, COALESCE(service, '') HAVING MIN(created_at) >= ? ORDER BY first_seen LIMIT 20`, mid.UTC().Format(tsLayout))
	if err == nil {
		for _, row := range res.Rows {
			d.Releases = append(d.Releases, digestRelease{Version: toString(row[0]), Service: toString(row[1]), FirstSeen: toString(row[2])})
		}
	} else if !missingTable(err, "releases") {
		s.logger.Info("digest: releases unavailable", "err", err)
	}
	d.Security = s.securityDigestData(ctx, mid, now)
	d.Days = s.digestDays(ctx, mid, now)
	d.Errors = s.digestErrorsData(ctx, start, mid, now)
	d.Uptime = s.digestUptimeData(ctx, start, mid, now)
	d.Machines = s.digestMachinesData(ctx, mid, now)
	d.Containers = s.digestContainersData(ctx, mid, now)
	d.Vitals = s.digestVitalsData(ctx, start, mid, now)
	d.Logs = s.digestLogsData(ctx, start, mid, now)
	return d, nil
}

// digestNotice renders the digest, asking the model for the summary when
// one is configured.
func (s *Server) digestNotice(ctx context.Context, d *digestData) notice {
	lines := digestLines(d)
	n := notice{Event: "digest", Title: "Weekly performance digest · " + s.conf("app_name"), Severity: "info", Lines: lines, At: time.Now().UTC()}
	if s.llm() != nil {
		if text := s.digestSummary(ctx, d); text != "" {
			n.Note = text
		}
	}
	n.HTML = s.digestHTML(d, n.Note)
	n.Payload = map[string]any{"event": "digest", "data": d, "summary": n.Note}
	// The same digest as a PDF, to keep or forward; the text goes out without it if it fails.
	if pdf, err := s.digestPDF(d, n.Note); err != nil {
		s.logger.Warn("digest: pdf skipped", "err", err)
	} else {
		name := "visionary-digest-" + time.Now().In(s.digestTZ()).Format("2006-01-02") + ".pdf"
		n.Files = append(n.Files, attachment{Name: name, Type: "application/pdf", Data: pdf})
	}
	return n
}

func digestLines(d *digestData) []string {
	ms := func(v *float64) string {
		if v == nil {
			return "n/a"
		}
		return formatUnit(*v, "ms")
	}
	pct := func(v *float64) string {
		if v == nil {
			return "n/a"
		}
		return formatUnit(*v, "percent")
	}
	change := func(a, b float64) string {
		if b == 0 {
			return ""
		}
		return fmt.Sprintf(" (%+.0f%%)", 100*(a-b)/b)
	}
	from, _ := time.Parse(tsLayout, d.From)
	to, _ := time.Parse(tsLayout, d.To)
	lines := []string{
		fmt.Sprintf("Week of %s to %s, compared with the week before.", from.Format("Jan 2"), to.Format("Jan 2")),
		fmt.Sprintf("Requests: %s, was %s%s", fnum(float64(d.This.Requests)), fnum(float64(d.Last.Requests)), change(float64(d.This.Requests), float64(d.Last.Requests))),
		fmt.Sprintf("p95 latency: %s, was %s", ms(d.This.P95), ms(d.Last.P95)),
		fmt.Sprintf("5xx rate: %s, was %s", pct(d.This.ErrorRate), pct(d.Last.ErrorRate)),
		fmt.Sprintf("Alerts fired: %d, was %d", d.This.Incidents, d.Last.Incidents),
	}
	if len(d.Slowest) > 0 {
		lines = append(lines, "Slowest routes (p95):")
		for _, r := range d.Slowest {
			l := fmt.Sprintf("  %s: %s over %d requests", r.Route, formatUnit(r.P95, "ms"), r.Requests)
			if r.PrevP95 != nil {
				l += ", was " + formatUnit(*r.PrevP95, "ms")
			}
			lines = append(lines, l)
		}
	}
	if len(d.Regressed) > 0 {
		lines = append(lines, "Slower than last week (p95):")
		for _, r := range d.Regressed {
			lines = append(lines, fmt.Sprintf("  %s: %s, was %s (%+.0f%%)", r.Route, formatUnit(r.P95, "ms"), formatUnit(r.PrevP95, "ms"), r.ChangePct))
		}
	}
	if len(d.Improved) > 0 {
		lines = append(lines, "Faster than last week (p95):")
		for _, r := range d.Improved {
			lines = append(lines, fmt.Sprintf("  %s: %s, was %s (%+.0f%%)", r.Route, formatUnit(r.P95, "ms"), formatUnit(r.PrevP95, "ms"), r.ChangePct))
		}
	}
	if len(d.Incidents) > 0 {
		lines = append(lines, "Incidents:")
		for _, i := range d.Incidents {
			lines = append(lines, fmt.Sprintf("  %s (%s): fired %d time(s)", i.Rule, i.Severity, i.Count))
		}
	}
	if len(d.Releases) > 0 {
		var v []string
		for _, r := range d.Releases {
			name := r.Version
			if r.Service != "" {
				name = r.Service + " " + name
			}
			v = append(v, name)
		}
		lines = append(lines, "New versions: "+strings.Join(v, ", "))
	}
	lines = append(lines, moreDigestLines(d)...)
	return append(lines, securityDigestLines(d.Security)...)
}

func (s *Server) digestSummary(ctx context.Context, d *digestData) string {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	data, _ := json.Marshal(d.forModel())
	content, err := s.llm().chatJSON(ctx, []chatMessage{
		{Role: "system", Content: `You are Visionary, the performance analyst for ` + s.conf("app_name") + `'s API. Write the weekly digest from the numbers given: this week against the week before.
Reply with one JSON object: {"summary": "<4 to 7 short lines separated by \n>"}.
- First line: the one thing worth knowing this week.
- Then traffic, p95 and 5xx changes with numbers, the routes that got slower ("regressed_routes"), incidents, and new versions that line up with changes. Say "may be related" for releases, never certain.
- Mention errors (new issues), uptime checks below 99.9%, machines with peak CPU or memory above 90%, container restarts or OOM kills, and Web Vitals rated poor, only when they are there and notable.
- If "security" has findings, failed logins or hosts with pending updates, end with one line on them, with numbers.
- Plain text with "- " bullets after the first line. No headings, no SQL.`},
		{Role: "user", Content: string(data)},
	})
	if err != nil {
		s.logger.Info("digest: summary skipped", "err", userMessage(err))
		return ""
	}
	var out struct {
		Summary string `json:"summary"`
	}
	if json.Unmarshal([]byte(content), &out) != nil {
		return ""
	}
	return truncate(strings.TrimSpace(out.Summary), 2500)
}

func (s *Server) sendDigest(ctx context.Context, c alertChannel, now time.Time) error {
	d, err := s.digestData(ctx, now)
	if err != nil {
		return err
	}
	if err := s.sendNotice(ctx, c, s.digestNotice(ctx, d)); err != nil {
		return err
	}
	return s.db.Exec(ctx, `UPDATE alert_channels SET digest_sent_at = ? WHERE id = ?`, now.UTC().Format(tsLayout), c.ID)
}

// digestDue reports whether a channel's weekly slot is now and it has not
// been sent in this slot yet.
func digestDue(c alertChannel, now time.Time, loc *time.Location) bool {
	if !c.DigestEnabled {
		return false
	}
	local := now.In(loc)
	if int(local.Weekday()) != c.DigestWeekday || local.Hour() != c.DigestHour {
		return false
	}
	slot := time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), 0, 0, 0, loc)
	last, err := time.Parse(tsLayout, c.DigestSentAt)
	return err != nil || last.Before(slot)
}

func (s *Server) digestLoop(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			s.sendDueDigests(ctx, now.UTC())
		}
	}
}

func (s *Server) sendDueDigests(ctx context.Context, now time.Time) {
	defer func() {
		if p := recover(); p != nil {
			s.logger.Error("digest: panicked", "panic", fmt.Sprint(p))
		}
	}()
	chans, err := s.listChannels(ctx)
	if err != nil {
		return
	}
	loc := s.digestTZ()
	for _, c := range chans {
		if !digestDue(c, now, loc) {
			continue
		}
		dctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		if err := s.sendDigest(dctx, c, now); err != nil {
			s.logger.Warn("digest: send failed", "channel_id", c.ID, "err", err)
		}
		cancel()
	}
}

// tzOffsetMinutes is the zone's current offset from UTC, in minutes.
func tzOffsetMinutes(tz *time.Location) int {
	_, off := time.Now().In(tz).Zone()
	return off / 60
}
