package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Auto entries aren't stored, they're read from the data at request time
type incidentEntry struct {
	ID     int64          `json:"id,omitempty"`
	At     string         `json:"at"`
	Kind   string         `json:"kind"`
	Actor  string         `json:"actor,omitempty"`
	Body   string         `json:"body"`
	Detail map[string]any `json:"detail,omitempty"`
	Auto   bool           `json:"auto"`
}

// Auto entries look a little before the start and after the end, where the
// cause and the recovery usually are
const (
	incidentLeadIn  = 30 * time.Minute
	incidentLeadOut = 15 * time.Minute
	maxAutoEntries  = 200
)

func incidentWindow(in *declaredIncident) (time.Time, time.Time) {
	from, _ := time.Parse(tsLayout, in.StartedAt)
	if d, err := time.Parse(tsLayout, in.DeclaredAt); err == nil && d.Before(from) {
		from = d
	}
	to := time.Now().UTC()
	if t, err := time.Parse(tsLayout, in.ResolvedAt); err == nil {
		to = t.Add(incidentLeadOut)
	}
	return from.Add(-incidentLeadIn), to
}

func (s *Server) incidentTimeline(ctx context.Context, in *declaredIncident, links []incidentLink) ([]incidentEntry, error) {
	res, err := s.db.Query(ctx, `SELECT id, at, kind, actor, body, detail FROM incident_timeline WHERE incident_id = ? ORDER BY at, id`, in.ID)
	if err != nil {
		return nil, err
	}
	out := make([]incidentEntry, 0, len(res.Rows))
	var deliveryIDs []int64
	for _, row := range res.Rows {
		e := incidentEntry{ID: toInt(row[0]), At: toString(row[1]), Kind: toString(row[2]), Actor: toString(row[3]), Body: toString(row[4])}
		_ = json.Unmarshal([]byte(toString(row[5])), &e.Detail)
		if len(e.Detail) == 0 {
			e.Detail = nil
		}
		deliveryIDs = append(deliveryIDs, detailIDs(e.Detail, "deliveries")...)
		out = append(out, e)
	}
	if len(deliveryIDs) > 0 {
		views, err := s.deliveriesByID(ctx, deliveryIDs)
		if err != nil {
			return nil, err
		}
		for i := range out {
			var list []deliveryView
			for _, id := range detailIDs(out[i].Detail, "deliveries") {
				if v, ok := views[id]; ok {
					list = append(list, v)
				}
			}
			if list != nil {
				out[i].Detail["delivery"] = list
			}
		}
	}
	from, to := incidentWindow(in)
	auto := s.autoEntries(ctx, in, links, from, to)
	out = append(out, auto...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At })
	return out, nil
}

func detailIDs(d map[string]any, key string) []int64 {
	raw, _ := d[key].([]any)
	out := make([]int64, 0, len(raw))
	for _, v := range raw {
		if f, ok := v.(float64); ok {
			out = append(out, int64(f))
		}
	}
	return out
}

func (s *Server) deliveriesByID(ctx context.Context, ids []int64) (map[int64]deliveryView, error) {
	out := map[int64]deliveryView{}
	ids = head(ids, 500)
	for len(ids) > 0 {
		chunk := head(ids, maxInArgs)
		ids = ids[len(chunk):]
		args := make([]any, 0, len(chunk))
		for _, id := range chunk {
			args = append(args, id)
		}
		res, err := s.db.Query(ctx, `SELECT id, channel_id, channel_name, channel_kind, status, attempts, error, created_at, sent_at, next_at
FROM alert_deliveries WHERE id IN (`+placeholders(len(args))+`)`, args...)
		if err != nil {
			if missingTable(err, "alert_deliveries") {
				return out, nil
			}
			return nil, err
		}
		for _, row := range res.Rows {
			d := deliveryView{ID: toInt(row[0]), ChannelID: toInt(row[1]), ChannelName: toString(row[2]), ChannelKind: toString(row[3]), Event: "incident",
				Status: toString(row[4]), Attempts: int(toInt(row[5])), Error: toString(row[6]), CreatedAt: toString(row[7]), SentAt: toString(row[8]), NextAt: toString(row[9])}
			out[d.ID] = d
		}
	}
	return out, nil
}

func (s *Server) autoEntries(ctx context.Context, in *declaredIncident, links []incidentLink, from, to time.Time) []incidentEntry {
	var out []incidentEntry
	lo, hi := from.UTC().Format(tsLayout), to.UTC().Format(tsLayout)
	series := map[int64][]string{}
	var ruleIDs []any
	for _, l := range links {
		switch l.Kind {
		case "alert":
			id, key := splitRef(l.Ref)
			if id <= 0 {
				continue
			}
			if _, ok := series[id]; !ok {
				ruleIDs = append(ruleIDs, id)
			}
			series[id] = append(series[id], key)
		case "uptime":
			id, _ := splitRef(l.Ref)
			c, err := s.getUptimeCheck(ctx, id)
			if err != nil {
				continue
			}
			list, _, err := s.checkIncidents(ctx, id, timeRange{From: from, To: to, Bucket: 60})
			if err != nil {
				continue
			}
			for _, ci := range list {
				out = append(out, incidentEntry{At: ci.StartedAt, Kind: "check", Auto: true, Body: c.Name + " started failing.",
					Detail: map[string]any{"check_id": id, "state": "down", "link": "#/uptime?check=" + strconv.FormatInt(id, 10)}})
				if ci.EndedAt != nil {
					out = append(out, incidentEntry{At: *ci.EndedAt, Kind: "check", Auto: true, Body: c.Name + " recovered after " + humanDuration(time.Duration(ci.DurationS)*time.Second) + ".",
						Detail: map[string]any{"check_id": id, "state": "up", "link": "#/uptime?check=" + strconv.FormatInt(id, 10)}})
				}
			}
		}
	}
	if len(ruleIDs) > 0 {
		res, err := s.db.Query(ctx, `SELECT created_at, rule_id, rule_name, state, series_key, message FROM alert_events
WHERE rule_id IN (`+placeholders(len(ruleIDs))+`) AND created_at >= ? AND created_at <= ? ORDER BY created_at LIMIT `+strconv.Itoa(maxAutoEntries),
			append(ruleIDs, lo, hi)...)
		if err == nil {
			for _, row := range res.Rows {
				rid, key := toInt(row[1]), toString(row[4])
				if keys := series[rid]; !slices.Contains(keys, "") && !slices.Contains(keys, key) {
					continue
				}
				state := toString(row[3])
				verb := map[string]string{"firing": " fired", "resolved": " resolved"}[state]
				if verb == "" {
					verb = ": " + state
				}
				body := toString(row[2]) + verb + "."
				if msg := toString(row[5]); msg != "" && state == "firing" {
					body = toString(row[2]) + verb + ": " + truncate(msg, 300)
				}
				out = append(out, incidentEntry{At: toString(row[0]), Kind: "alert", Auto: true, Body: body,
					Detail: map[string]any{"rule_id": rid, "state": state, "link": "#/alerts?rule=" + strconv.FormatInt(rid, 10)}})
			}
		}
	}
	sql := `SELECT service, version, MIN(created_at) AS deployed_at FROM releases WHERE created_at >= ? AND created_at <= ?`
	args := []any{lo, hi}
	if len(in.Services) > 0 {
		sql += ` AND service IN (` + placeholders(len(in.Services)) + `)`
		for _, sv := range in.Services {
			args = append(args, sv)
		}
	}
	if res, err := s.db.Query(ctx, sql+` GROUP BY service, version ORDER BY deployed_at LIMIT 50`, args...); err == nil {
		for _, row := range res.Rows {
			svc, ver := toString(row[0]), toString(row[1])
			out = append(out, incidentEntry{At: toString(row[2]), Kind: "deploy", Auto: true, Body: "Deployed " + svc + " " + ver + ".",
				Detail: map[string]any{"service": svc, "version": ver}})
		}
	}
	return head(out, maxAutoEntries)
}

func splitRef(ref string) (int64, string) {
	a, b, _ := strings.Cut(ref, "|")
	id, err := strconv.ParseInt(a, 10, 64)
	if err != nil {
		return 0, ""
	}
	return id, b
}

type incidentSource struct {
	Kind      string   `json:"kind"`
	Ref       string   `json:"ref"`
	Label     string   `json:"label"`
	Title     string   `json:"title"`
	Severity  string   `json:"severity"`
	Services  []string `json:"services"`
	Impact    string   `json:"impact"`
	StartedAt string   `json:"started_at,omitempty"`
}

var errNoSource = errors.New("nothing found to declare an incident from")

func (s *Server) resolveIncidentSource(ctx context.Context, kind, ref string) (*incidentSource, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" || len(ref) > 500 {
		return nil, errors.New("say what to declare the incident from")
	}
	src := &incidentSource{Kind: kind, Ref: ref, Severity: "sev3", Services: []string{}}
	switch kind {
	case "alert":
		id, key := splitRef(ref)
		rule, err := s.getRule(ctx, id)
		if err != nil || rule == nil {
			return nil, errors.New("alert rule not found")
		}
		src.Label, src.Title = rule.Name, rule.Name
		src.Severity = map[string]string{"critical": "sev2", "warning": "sev3"}[rule.Severity]
		if src.Severity == "" {
			src.Severity = "sev4"
		}
		if sv := fallback(rule.Spec.Scope.Service, rule.Spec.Labels["service"]); sv != "" {
			src.Services = []string{sv}
		}
		if open, err := s.openIncidents(ctx, id); err == nil {
			for k, inc := range open[id] {
				if (key == "" || k == key) && (src.StartedAt == "" || inc.OpenedAt < src.StartedAt) {
					src.StartedAt = inc.OpenedAt
				}
			}
		}
		if states, err := s.ruleStates(ctx, id); err == nil {
			for _, st := range states[id] {
				if st.State == "firing" && (key == "" || st.Key == key) {
					if key != "" {
						src.Label += " · " + seriesName(st.Key, st.Label)
					}
					src.Impact = st.Message
					break
				}
			}
		}
		if src.Impact == "" {
			src.Impact = "The alert " + rule.Name + " is firing."
		}
	case "uptime":
		id, at := splitRef(ref)
		c, err := s.getUptimeCheck(ctx, id)
		if err != nil {
			return nil, errors.New("uptime check not found")
		}
		now := time.Now().UTC()
		list, _, err := s.checkIncidents(ctx, id, timeRange{From: now.AddDate(0, 0, -30), To: now.Add(time.Minute), Bucket: 60})
		if err != nil {
			return nil, err
		}
		var pick *incident
		for i := range list {
			if (at != "" && list[i].StartedAt == at) || (at == "" && pick == nil) {
				pick = &list[i]
			}
		}
		src.Label, src.Title, src.Ref = c.Name, c.Name+" is down", strconv.FormatInt(id, 10)
		if pick != nil {
			src.Ref += "|" + pick.StartedAt
			src.StartedAt = pick.StartedAt
			if pick.Ongoing {
				src.Severity = "sev2"
			} else {
				src.Title = c.Name + " was down"
			}
			src.Impact = fmt.Sprintf("%s failed %d checks in a row", c.Name, pick.Failures)
			if pick.Status > 0 {
				src.Impact += fmt.Sprintf(", answering %d", pick.Status)
			}
			src.Impact += "."
		}
	case "error":
		if !errorFingerprint.MatchString(ref) {
			return nil, errors.New("invalid error fingerprint")
		}
		since := time.Now().UTC().AddDate(0, 0, -7).Format(tsLayout)
		res, err := s.db.Query(ctx, `SELECT type, message, service, created_at FROM errors WHERE fingerprint = ? ORDER BY created_at DESC LIMIT 1`, ref)
		if err != nil || len(res.Rows) == 0 {
			return nil, errNoSource
		}
		row := res.Rows[0]
		src.Label = toString(row[0])
		src.Title = truncate(strings.TrimSpace(toString(row[0])+": "+toString(row[1])), maxIncidentTitle)
		if sv := toString(row[2]); sv != "" {
			src.Services = []string{sv}
		}
		src.StartedAt = toString(row[3])
		if c, err := s.db.Query(ctx, `SELECT MIN(created_at), COUNT(*), COUNT(DISTINCT user_id) FROM errors WHERE fingerprint = ? AND created_at >= ?`, ref, since); err == nil && len(c.Rows) == 1 {
			if first := toString(c.Rows[0][0]); first != "" {
				src.StartedAt = first
			}
			src.Impact = fmt.Sprintf("%d occurrences in the last 7 days, affecting %d users.", toInt(c.Rows[0][1]), toInt(c.Rows[0][2]))
		}
	case "log":
		id, err := strconv.ParseInt(ref, 10, 64)
		if err != nil {
			return nil, errors.New("invalid log line id")
		}
		res, err := s.db.Query(ctx, `SELECT service, level, message, created_at FROM app_logs WHERE id = ?`, id)
		if err != nil || len(res.Rows) == 0 {
			return nil, errNoSource
		}
		row := res.Rows[0]
		msg := strings.TrimSpace(toString(row[2]))
		src.Label = truncate(msg, 120)
		src.Title = truncate(msg, maxIncidentTitle)
		if sv := toString(row[0]); sv != "" {
			src.Services = []string{sv}
		}
		if toString(row[1]) == "fatal" {
			src.Severity = "sev2"
		}
		src.StartedAt = toString(row[3])
		src.Impact = "Logged at " + strings.ToUpper(toString(row[1])) + ": " + truncate(msg, 500)
	default:
		return nil, errors.New("declare from an alert, uptime, error or log")
	}
	return src, nil
}

func incidentNotice(in *declaredIncident, by, body string) notice {
	title := fmt.Sprintf("[%s · %s] %s", severityLabel(in.Severity), incidentStatusLabels[in.Status], in.Title)
	event := "incident"
	if in.Status == "resolved" {
		event = "resolved"
	}
	n := notice{Event: event, Title: title, Severity: noticeSeverity(in.Severity), Lines: []string{body}, At: time.Now().UTC(),
		Key: "visionary-incident-" + strconv.FormatInt(in.ID, 10)}
	n.Fields = [][2]string{{"Severity", severityLabel(in.Severity)}, {"Status", incidentStatusLabels[in.Status]}, {"Commander", fallback(in.Commander, "nobody yet")}}
	if len(in.Services) > 0 {
		n.Fields = append(n.Fields, [2]string{"Services", strings.Join(in.Services, ", ")})
	}
	n.Fields = append(n.Fields, [2]string{"Posted by", by})
	n.Payload = map[string]any{
		"event": "incident.update",
		"incident": map[string]any{"id": in.ID, "title": in.Title, "severity": in.Severity, "status": in.Status, "services": in.Services,
			"commander": in.Commander, "started_at": in.StartedAt, "declared_at": in.DeclaredAt, "resolved_at": in.ResolvedAt},
		"message": body, "by": by, "key": n.Key, "at": n.At.Format(tsLayout),
	}
	return n
}

func (s *Server) postIncidentUpdate(ctx context.Context, in *declaredIncident, by, body string, channels []int64, public bool) ([]string, error) {
	n := incidentNotice(in, by, body)
	q, err := s.enqueue(ctx, 0, nil, channels, n)
	if err != nil {
		return nil, err
	}
	names := []string{}
	ids := []int64{}
	for _, d := range q {
		names, ids = append(names, d.ch.Name), append(ids, d.id)
	}
	detail := map[string]any{"status": in.Status, "channels": names, "deliveries": ids, "public": public}
	if _, err := s.addIncidentEntry(ctx, in.ID, "update", by, body, detail); err != nil {
		return nil, err
	}
	if public {
		dropStatusCache(s)
	}
	if len(q) > 0 {
		e := s.alertEngine()
		go func() {
			defer e.recoverLog("incident update")
			sctx, cancel := context.WithTimeout(e.ctx, 60*time.Second)
			defer cancel()
			s.sendQueued(sctx, q, n, nil)
		}()
	}
	return names, nil
}

type statusNotice struct {
	Title       string         `json:"title"`
	Status      string         `json:"status"`
	StatusLabel string         `json:"status_label"`
	StartedAt   string         `json:"started_at"`
	ResolvedAt  string         `json:"resolved_at,omitempty"`
	Updates     []statusUpdate `json:"updates"`
}

type statusUpdate struct {
	At     string `json:"at"`
	Status string `json:"status"`
	Body   string `json:"body"`
}

func (s *Server) statusNotices(ctx context.Context) []statusNotice {
	out := []statusNotice{}
	since := time.Now().UTC().AddDate(0, 0, -7).Format(tsLayout)
	list, err := s.loadIncidents(ctx, "on_status_page = 1 AND (resolved_at = '' OR resolved_at >= ?)", []any{since}, 20)
	if err != nil {
		s.logger.Warn("status page: incidents", "err", err)
		return out
	}
	for _, in := range list {
		n := statusNotice{Title: in.Title, Status: in.Status, StatusLabel: incidentStatusLabels[in.Status], StartedAt: in.StartedAt, ResolvedAt: in.ResolvedAt, Updates: []statusUpdate{}}
		res, err := s.db.Query(ctx, `SELECT at, body, detail FROM incident_timeline WHERE incident_id = ? AND kind = 'update' ORDER BY at DESC, id DESC LIMIT 50`, in.ID)
		if err == nil {
			for _, row := range res.Rows {
				var d struct {
					Public bool   `json:"public"`
					Status string `json:"status"`
				}
				if json.Unmarshal([]byte(toString(row[2])), &d) == nil && d.Public {
					n.Updates = append(n.Updates, statusUpdate{At: toString(row[0]), Status: incidentStatusLabels[d.Status], Body: toString(row[1])})
				}
			}
		}
		out = append(out, n)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ResolvedAt == "" && out[j].ResolvedAt != "" })
	return out
}

func applyNotices(p *statusPage) {
	for _, n := range p.Notices {
		if n.ResolvedAt != "" {
			continue
		}
		if p.State == "operational" || p.State == "unknown" {
			p.State = "degraded"
			p.Label = map[string]string{
				"investigating": "Investigating an incident",
				"identified":    "Issue identified; a fix is under way",
				"monitoring":    "A fix is in place; monitoring",
			}[n.Status]
		}
		return
	}
}

func dropStatusCache(s *Server) {
	s.announce("status")
	dropStatusCacheHere(s)
}

func dropStatusCacheHere(s *Server) {
	statusCache.Lock()
	defer statusCache.Unlock()
	delete(statusCache.page, s)
}
