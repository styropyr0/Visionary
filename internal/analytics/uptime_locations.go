package analytics

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	sdk "github.com/styropyr0/visionary/sdks/go"
)

// Locations are the places a check runs from. "server" is Visionary itself;
// every other is a host agent that polls for checks and posts results.
// ok is the check's verdict across locations by its rule; location_ok is one
// location's. Stale locations are left out of the vote.

const (
	serverLocation = "server"
	// locationPollS is how often runners poll; a location that has not
	// polled for locationStaleAfter is offline.
	locationPollS      = 15
	locationLease      = 60 * time.Second
	locationStaleAfter = 2 * time.Minute
	maxLocations       = 50
	maxCheckLocations  = 10
)

var (
	locationNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,47}$`)
	locationRules  = map[string]bool{"any": true, "majority": true, "all": true}
)

var uptimeLocationColumns = map[string][]sdk.Column{
	"uptime_results": {{Name: "location", Kind: "text"}, {Name: "location_ok", Kind: "int"}},
}

func (s *Server) uptimeLocationsSchema() []string {
	id, key, ts, short, num := "INTEGER PRIMARY KEY AUTOINCREMENT", "INTEGER", "TEXT", "TEXT", "INTEGER"
	switch s.q.d {
	case sdk.Postgres:
		id, key = "BIGSERIAL PRIMARY KEY", "BIGINT"
	case sdk.MySQL:
		id, key, ts, short, num = "BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY", "BIGINT", "VARCHAR(32)", "VARCHAR(255)", "INT"
	}
	tail := ""
	if s.q.d == sdk.MySQL {
		tail = " CHARACTER SET utf8mb4"
	}
	return []string{`CREATE TABLE IF NOT EXISTS uptime_locations (
    id              ` + id + `,
    name            ` + short + ` NOT NULL,
    key_id          ` + key + ` NOT NULL DEFAULT 0,
    created_at      ` + ts + ` NOT NULL,
    last_seen_at    ` + ts + `,
    version         ` + short + `,
    host            ` + short + `,
    browser         ` + num + ` NOT NULL DEFAULT 0,
    browser_version ` + short + `,
    kinds           ` + short + `,
    instance        ` + short + `,
    lease_until     ` + ts + `
)` + tail + `;
CREATE TABLE IF NOT EXISTS uptime_check_locations (
    check_id         ` + key + ` NOT NULL PRIMARY KEY,
    locations        TEXT NOT NULL,
    rule             ` + short + ` NOT NULL,
    status_breakdown ` + num + ` NOT NULL DEFAULT 0
)` + tail}
}

type uptimeLocation struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	Builtin        bool   `json:"builtin"`
	KeyID          int64  `json:"-"`
	KeyPrefix      string `json:"key_prefix,omitempty"`
	CreatedAt      string `json:"created_at,omitempty"`
	LastSeenAt     string `json:"last_seen_at,omitempty"`
	Version        string `json:"version,omitempty"`
	Host           string `json:"host,omitempty"`
	Browser        bool   `json:"browser"`
	BrowserVersion string `json:"browser_version,omitempty"`
	Kinds          string `json:"-"`
	// KindList is the check kinds its runner said it can run; empty for the
	// server, which runs every kind.
	KindList   []string `json:"kinds,omitempty"`
	Instance   string   `json:"-"`
	LeaseUntil string   `json:"-"`
	// State is online, offline or new (never polled).
	State  string `json:"state"`
	Checks int    `json:"checks"`
}

func (l uptimeLocation) online(now time.Time) bool {
	if l.Builtin {
		return true
	}
	seen, err := time.Parse(tsLayout, l.LastSeenAt)
	return err == nil && now.Sub(seen) < locationStaleAfter
}

const uptimeLocationCols = `id, name, key_id, created_at, last_seen_at, version, host, browser, browser_version, kinds, instance, lease_until`

func scanLocation(row []any) uptimeLocation {
	return uptimeLocation{ID: toInt(row[0]), Name: toString(row[1]), KeyID: toInt(row[2]), CreatedAt: toString(row[3]),
		LastSeenAt: toString(row[4]), Version: toString(row[5]), Host: toString(row[6]), Browser: toInt(row[7]) == 1,
		BrowserVersion: toString(row[8]), Kinds: toString(row[9]), Instance: toString(row[10]), LeaseUntil: toString(row[11]),
		KindList: strings.FieldsFunc(toString(row[9]), func(r rune) bool { return r == ',' })}
}

func (s *Server) listLocations(ctx context.Context) ([]uptimeLocation, error) {
	rows, err := s.db.Query(ctx, `SELECT `+uptimeLocationCols+` FROM uptime_locations ORDER BY name`)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	out := []uptimeLocation{{Name: serverLocation, Builtin: true, State: "online", Browser: runnerFor(s).browser.Available()}}
	for _, row := range rows.Rows {
		if len(row) != 12 {
			continue
		}
		l := scanLocation(row)
		switch {
		case l.online(now):
			l.State = "online"
		case l.LastSeenAt == "":
			l.State = "new"
		default:
			l.State = "offline"
		}
		out = append(out, l)
	}
	return out, nil
}

func (s *Server) getLocation(ctx context.Context, name string) (uptimeLocation, bool, error) {
	rows, err := s.db.Query(ctx, `SELECT `+uptimeLocationCols+` FROM uptime_locations WHERE name = ?`, name)
	if err != nil || len(rows.Rows) == 0 || len(rows.Rows[0]) != 12 {
		return uptimeLocation{}, false, err
	}
	return scanLocation(rows.Rows[0]), true, nil
}

// normalizeLocations fills the defaults of a check's locations and rule and
// checks their form; whether the locations exist is checkLocations' job.
func (c *UptimeCheck) normalizeLocations() error {
	seen := map[string]bool{}
	var locs []string
	for _, l := range c.Locations {
		l = strings.ToLower(strings.TrimSpace(l))
		if l == "" || seen[l] {
			continue
		}
		if !locationNameRe.MatchString(l) {
			return fmt.Errorf("location %q is not a valid name", l)
		}
		seen[l] = true
		locs = append(locs, l)
	}
	if len(locs) == 0 {
		locs = []string{serverLocation}
	}
	if len(locs) > maxCheckLocations {
		return fmt.Errorf("a check runs from at most %d locations", maxCheckLocations)
	}
	c.Locations = locs
	c.LocationRule = strings.ToLower(strings.TrimSpace(c.LocationRule))
	if c.LocationRule == "" {
		c.LocationRule = "majority"
	}
	if !locationRules[c.LocationRule] {
		return errors.New("location rule must be any, majority or all")
	}
	if !c.OnStatusPage || len(c.Locations) < 2 {
		c.StatusShowLocations = false
	}
	return nil
}

func (c UptimeCheck) runsAt(location string) bool {
	if len(c.Locations) == 0 {
		return location == serverLocation
	}
	return slices.Contains(c.Locations, location)
}

func (s *Server) checkLocations(ctx context.Context, c UptimeCheck) error {
	if len(c.Locations) == 1 && c.Locations[0] == serverLocation {
		return nil
	}
	locs, err := s.listLocations(ctx)
	if err != nil {
		return err
	}
	for _, l := range c.Locations {
		if !slices.ContainsFunc(locs, func(x uptimeLocation) bool { return x.Name == l }) {
			return badInput("no location named " + strconv.Quote(l) + "; add it under Uptime → Locations first")
		}
	}
	return nil
}

// loadLocationConfigs fills in each check's locations; a check without a row
// runs from the server alone.
func (s *Server) loadLocationConfigs(ctx context.Context, checks []UptimeCheck) error {
	for i := range checks {
		checks[i].Locations, checks[i].LocationRule = []string{serverLocation}, "majority"
	}
	if len(checks) == 0 {
		return nil
	}
	rows, err := s.db.Query(ctx, `SELECT check_id, locations, rule, status_breakdown FROM uptime_check_locations`)
	if err != nil {
		if missingTable(err, "uptime_check_locations") {
			return nil
		}
		return err
	}
	byID := map[int64][]any{}
	for _, row := range rows.Rows {
		if len(row) == 4 {
			byID[toInt(row[0])] = row
		}
	}
	for i := range checks {
		row, ok := byID[checks[i].ID]
		if !ok {
			continue
		}
		if locs := splitLocations(toString(row[1])); len(locs) > 0 {
			checks[i].Locations = locs
		}
		checks[i].LocationRule = fallback(toString(row[2]), "majority")
		checks[i].StatusShowLocations = toInt(row[3]) == 1
	}
	return nil
}

func splitLocations(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// saveLocationConfig stores a check's locations; the default (the server,
// majority, no breakdown) needs no row.
func (s *Server) saveLocationConfig(ctx context.Context, c UptimeCheck) error {
	if err := s.db.Exec(ctx, `DELETE FROM uptime_check_locations WHERE check_id = ?`, c.ID); err != nil {
		return err
	}
	if (len(c.Locations) == 0 || len(c.Locations) == 1 && c.Locations[0] == serverLocation) && !c.StatusShowLocations &&
		(c.LocationRule == "" || c.LocationRule == "majority") {
		return nil
	}
	return s.db.Exec(ctx, `INSERT INTO uptime_check_locations (check_id, locations, rule, status_breakdown) VALUES (?, ?, ?, ?)`,
		c.ID, strings.Join(c.Locations, ","), fallback(c.LocationRule, "majority"), boolInt(c.StatusShowLocations))
}

// locationVerdict is a check's state across its locations, given that loc
// just returned ok: whether it is up, and which locations fail.
func (s *Server) locationVerdict(ctx context.Context, c UptimeCheck, loc string, ok bool) (bool, []string, error) {
	now := time.Now().UTC()
	since := now.Add(-time.Duration(2*c.IntervalS)*time.Second - 90*time.Second)
	rows, err := s.db.Query(ctx, `SELECT location, location_ok, ok FROM uptime_results
WHERE check_id = ? AND created_at >= ? ORDER BY created_at DESC LIMIT 200`, c.ID, since.Format(tsLayout))
	if err != nil {
		return ok, nil, err
	}
	locs, err := s.listLocations(ctx)
	if err != nil {
		return ok, nil, err
	}
	live := map[string]bool{}
	for _, l := range locs {
		live[l.Name] = l.online(now)
	}
	latest := map[string]bool{loc: ok}
	for _, row := range rows.Rows {
		if len(row) != 3 {
			continue
		}
		name := fallback(toString(row[0]), serverLocation)
		if _, seen := latest[name]; seen {
			continue
		}
		v := row[1]
		if toString(v) == "" {
			v = row[2]
		}
		latest[name] = toInt(v) == 1
	}
	var failing []string
	n := 0
	for _, name := range c.Locations {
		pass, reported := latest[name]
		if !reported || (name != loc && !live[name]) {
			continue
		}
		n++
		if !pass {
			failing = append(failing, name)
		}
	}
	return !locationsDown(c.LocationRule, len(failing), n), failing, nil
}

func locationsDown(rule string, failed, n int) bool {
	switch {
	case failed == 0:
		return false
	case n <= 1 || rule == "any":
		return true
	case rule == "all":
		return failed == n
	}
	return 2*failed > n
}

// ownError is a location's own reason from a row's error, without the
// "down in a, b: " the vote put in front.
func ownError(msg string) string {
	if rest, ok := strings.CutPrefix(msg, "down in "); ok {
		_, own, _ := strings.Cut(rest, ": ")
		return own
	}
	return msg
}

type locationState struct {
	Name           string  `json:"name"`
	State          string  `json:"state"`
	LastAt         string  `json:"last_at,omitempty"`
	LastStatus     int64   `json:"last_status"`
	LastDurationMs float64 `json:"last_duration_ms"`
	LastError      string  `json:"last_error,omitempty"`
}

// locationStates reads each check's latest result per location, for checks
// that run from more than the server.
func (s *Server) locationStates(ctx context.Context, checks []UptimeCheck) (map[int64][]locationState, error) {
	out := map[int64][]locationState{}
	var multi []UptimeCheck
	for _, c := range checks {
		if !(len(c.Locations) == 1 && c.Locations[0] == serverLocation) {
			multi = append(multi, c)
		}
	}
	if len(multi) == 0 {
		return out, nil
	}
	now := time.Now().UTC()
	loc := "COALESCE(location, '" + serverLocation + "')"
	rows, err := s.db.Query(ctx, `SELECT r.check_id, `+strings.ReplaceAll(loc, "location", "r.location")+`, r.created_at, COALESCE(r.location_ok, r.ok),
  r.degraded, r.status, r.duration_ms, r.error
FROM uptime_results r JOIN (
  SELECT check_id, `+loc+` AS loc, MAX(created_at) AS latest FROM uptime_results WHERE created_at >= ? GROUP BY check_id, `+loc+`
) l ON r.check_id = l.check_id AND `+strings.ReplaceAll(loc, "location", "r.location")+` = l.loc AND r.created_at = l.latest`,
		now.Add(-48*time.Hour).Format(tsLayout))
	if err != nil {
		return nil, err
	}
	type key struct {
		id  int64
		loc string
	}
	latest := map[key][]any{}
	for _, row := range rows.Rows {
		if len(row) == 8 {
			latest[key{toInt(row[0]), toString(row[1])}] = row
		}
	}
	locs, err := s.listLocations(ctx)
	if err != nil {
		return nil, err
	}
	byName := map[string]uptimeLocation{}
	for _, l := range locs {
		byName[l.Name] = l
	}
	for _, c := range multi {
		list := make([]locationState, 0, len(c.Locations))
		for _, name := range c.Locations {
			ls := locationState{Name: name, State: "pending"}
			l, known := byName[name]
			if row, ok := latest[key{c.ID, name}]; ok {
				ls.LastAt, ls.LastStatus = toString(row[2]), toInt(row[5])
				if toInt(row[3]) != 1 {
					ls.LastError = ownError(toString(row[7]))
				}
				ls.LastDurationMs, _ = asFloat(row[6])
				switch {
				case toInt(row[3]) != 1:
					ls.State = "down"
				case toInt(row[4]) == 1:
					ls.State = "degraded"
				default:
					ls.State = "up"
				}
				at, _ := time.Parse(tsLayout, ls.LastAt)
				if now.Sub(at) > time.Duration(3*c.IntervalS)*time.Second+time.Minute {
					ls.State = "stale"
				}
			}
			switch {
			case !c.Enabled:
				ls.State = "paused"
			case !known || !l.online(now) && l.LastSeenAt != "":
				ls.State = "stale"
			case c.Kind == "browser" && !l.Browser:
				ls.State = "no_browser"
			}
			list = append(list, ls)
		}
		out[c.ID] = list
	}
	return out, nil
}
