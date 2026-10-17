package analytics

import (
	"bytes"
	"context"
	"net/http"
	"sort"
	"sync"
	"time"
)

// The public status page (/status, and /status.json for widgets and
// scripts). It is off unless VISIONARY_STATUS_PAGE=true, needs no sign-in,
// and shows only checks marked for it: their state, daily bars and incidents.
// URLs appear only for checks that opt in; error texts never do.

const statusCacheTTL = 30 * time.Second

type statusPage struct {
	Title     string           `json:"title"`
	State     string           `json:"state"` // operational, degraded, partial_outage, major_outage, unknown
	Label     string           `json:"label"`
	Checks    []statusCheck    `json:"checks"`
	Incidents []statusIncident `json:"incidents"`
	UpdatedAt string           `json:"updated_at"`
}

type statusCheck struct {
	Name     string      `json:"name"`
	URL      string      `json:"url,omitempty"`
	State    string      `json:"state"` // operational, degraded, outage, paused, unknown
	Uptime90 *float64    `json:"uptime_90d"`
	Days     []statusDay `json:"days"`
}

type statusDay struct {
	Day    string   `json:"day"`
	Uptime *float64 `json:"uptime"`
	Level  string   `json:"level"` // ok, minor, major, none
}

type statusIncident struct {
	Check     string  `json:"check"`
	StartedAt string  `json:"started_at"`
	EndedAt   *string `json:"ended_at"`
	DurationS int64   `json:"duration_s"`
	Ongoing   bool    `json:"ongoing"`
}

// dayLevel colors a day: any failure is at least minor; below 99% major.
func dayLevel(b dayBar) string {
	switch {
	case b.Checks == 0:
		return "none"
	case b.Failed == 0:
		return "ok"
	case b.Uptime != nil && *b.Uptime >= 99:
		return "minor"
	}
	return "major"
}

var statusCache struct {
	sync.Mutex
	at   map[*Server]time.Time
	page map[*Server]*statusPage
}

func statusEnabled() bool { return getBool("VISIONARY_STATUS_PAGE", false) }

func (s *Server) statusTitle() string {
	if t := getEnv("VISIONARY_STATUS_TITLE", ""); t != "" {
		return t
	}
	if s.cfg.AppName != "" && s.cfg.AppName != "the application" {
		return s.cfg.AppName
	}
	return "Service status"
}

// cachedStatus builds the page at most once per TTL however many visitors
// arrive, so the public endpoint cannot be used to load the database.
func (s *Server) cachedStatus(ctx context.Context) (*statusPage, error) {
	statusCache.Lock()
	defer statusCache.Unlock()
	if statusCache.page == nil {
		statusCache.at, statusCache.page = map[*Server]time.Time{}, map[*Server]*statusPage{}
	}
	if p := statusCache.page[s]; p != nil && time.Since(statusCache.at[s]) < statusCacheTTL {
		return p, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	p, err := s.buildStatus(ctx)
	if err != nil {
		return nil, err
	}
	statusCache.at[s], statusCache.page[s] = time.Now(), p
	return p, nil
}

func (s *Server) buildStatus(ctx context.Context) (*statusPage, error) {
	all, err := s.listUptimeChecks(ctx)
	if err != nil {
		return nil, err
	}
	var checks []UptimeCheck
	for _, c := range all {
		if c.OnStatusPage {
			checks = append(checks, c)
		}
	}
	states, err := s.uptimeStates(ctx, checks, 90)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	page := &statusPage{Title: s.statusTitle(), Checks: []statusCheck{}, Incidents: []statusIncident{}, UpdatedAt: now.Format(tsLayout)}
	var down, slow, live int
	recent := timeRange{From: now.AddDate(0, 0, -7), To: now.Add(time.Minute), Bucket: 60}
	for _, c := range checks {
		st := states[c.ID]
		sc := statusCheck{Name: c.Name, Days: make([]statusDay, len(st.Days))}
		if c.StatusShowURL {
			sc.URL = redactURL(c.URL)
		}
		var n, failed int64
		for i, b := range st.Days {
			sc.Days[i] = statusDay{Day: b.Day, Uptime: b.Uptime, Level: dayLevel(b)}
			n, failed = n+b.Checks, failed+b.Failed
		}
		sc.Uptime90 = pct(n-failed, n)
		switch st.State {
		case "up":
			sc.State = "operational"
		case "degraded":
			sc.State = "degraded"
			slow++
		case "down":
			sc.State = "outage"
			down++
		case "paused":
			sc.State = "paused"
		default:
			sc.State = "unknown"
		}
		if sc.State != "paused" && sc.State != "unknown" {
			live++
		}
		page.Checks = append(page.Checks, sc)

		incidents, _, err := s.checkIncidents(ctx, c.ID, recent)
		if err != nil {
			return nil, err
		}
		for _, in := range incidents {
			page.Incidents = append(page.Incidents, statusIncident{Check: c.Name, StartedAt: in.StartedAt, EndedAt: in.EndedAt, DurationS: in.DurationS, Ongoing: in.Ongoing})
		}
	}
	switch {
	case live == 0:
		page.State, page.Label = "unknown", "No status to report yet"
	case down > 0 && down == live:
		page.State, page.Label = "major_outage", "Major outage"
	case down > 0:
		page.State, page.Label = "partial_outage", "Partial outage"
	case slow > 0:
		page.State, page.Label = "degraded", "Degraded performance"
	default:
		page.State, page.Label = "operational", "All systems operational"
	}
	sortIncidents(page.Incidents)
	return page, nil
}

// sortIncidents puts ongoing incidents first, then the most recent.
func sortIncidents(list []statusIncident) {
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Ongoing != list[j].Ongoing {
			return list[i].Ongoing
		}
		return list[i].StartedAt > list[j].StartedAt
	})
}

func (s *Server) statusFor(w http.ResponseWriter, r *http.Request) (*statusPage, bool) {
	if !statusEnabled() {
		http.NotFound(w, r)
		return nil, false
	}
	p, err := s.cachedStatus(r.Context())
	if err != nil {
		s.logger.Error("status page failed", "err", err)
		http.Error(w, "status is temporarily unavailable", http.StatusServiceUnavailable)
		return nil, false
	}
	return p, true
}

func (s *Server) handleStatusJSON(w http.ResponseWriter, r *http.Request) {
	p, ok := s.statusFor(w, r)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=30")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleStatusPage(w http.ResponseWriter, r *http.Request) {
	p, ok := s.statusFor(w, r)
	if !ok {
		return
	}
	var buf bytes.Buffer
	if err := statusTemplate.Execute(&buf, p); err != nil {
		s.logger.Error("status page render failed", "err", err)
		http.Error(w, "status is temporarily unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=30")
	_, _ = w.Write(buf.Bytes())
}
