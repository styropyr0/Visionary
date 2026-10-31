package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/styropyr0/visionary/internal/agentapi"
	"github.com/styropyr0/visionary/internal/uptimecore"
)

// The Locations tab's API, and the two endpoints location runners call
// (agentapi): poll for checks, post results. A location's key is an API key
// of scope "location", made with the location and tied to it by key_id.

const defaultLocationRuleName = "Uptime location offline"

func (s *Server) uptimeLocationRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/uptime/locations", s.authed(s.handleLocationList))
	mux.Handle("POST /api/uptime/locations", s.authed(s.handleLocationCreate))
	mux.Handle("POST /api/uptime/locations/{name}/key", s.authed(s.handleLocationKey))
	mux.Handle("DELETE /api/uptime/locations/{name}", s.authed(s.handleLocationDelete))
	mux.HandleFunc("POST "+agentapi.LocationPollPath, s.handleLocationPoll)
	mux.HandleFunc("POST "+agentapi.LocationResultsPath, s.handleLocationResults)
}

func (s *Server) handleLocationList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	locs, err := s.listLocations(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	checks, err := s.listUptimeChecks(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	prefixes := map[int64]string{}
	if rows, err := s.db.Query(ctx, `SELECT id, prefix FROM visionary_api_keys WHERE scope = 'location' AND revoked_at IS NULL`); err == nil {
		for _, row := range rows.Rows {
			if len(row) == 2 {
				prefixes[toInt(row[0])] = toString(row[1])
			}
		}
	}
	for i := range locs {
		for _, c := range checks {
			if c.Enabled && c.runsAt(locs[i].Name) {
				locs[i].Checks++
			}
		}
		locs[i].KeyPrefix = prefixes[locs[i].KeyID]
	}
	writeJSON(w, http.StatusOK, map[string]any{"locations": locs, "can_admin": s.can(r, "admin"), "poll_s": locationPollS,
		"stale_after_s": int(locationStaleAfter.Seconds())})
}

// newLocationKey makes a location key and returns its id and secret.
func (s *Server) newLocationKey(r *http.Request, name string) (int64, string, error) {
	secret, err := newKeySecret()
	if err != nil {
		return 0, "", err
	}
	by := "admin"
	if sess := sessionFrom(r); sess != nil {
		by = fallback(sess.Name, sess.UserID)
	}
	prefix := secret[:keyVisibleLen]
	id, err := s.db.InsertID(r.Context(), `INSERT INTO visionary_api_keys (name, scope, prefix, key_hash, created_by, created_at, expires_at)
VALUES (?, 'location', ?, ?, ?, ?, NULL)`, "Uptime location "+name, prefix, hashKey(secret), by, uptimeNow())
	if err != nil {
		return 0, "", err
	}
	s.invalidateKeys()
	s.audit(r, "api_key.create", strconv.FormatInt(id, 10), map[string]any{"name": "Uptime location " + name, "scope": "location", "prefix": prefix})
	return id, secret, nil
}

func (s *Server) revokeLocationKey(ctx context.Context, id int64) error {
	if id == 0 {
		return nil
	}
	err := s.db.Exec(ctx, `UPDATE visionary_api_keys SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, uptimeNow(), id)
	s.invalidateKeys()
	return err
}

func (s *Server) handleLocationCreate(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	l, secret, err := s.createLocation(r, body.Name, true)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"location": l, "secret": secret})
}

// createLocation adds a location, with a key unless withKey is false (a
// config import, whose key is made later in the Locations tab).
func (s *Server) createLocation(r *http.Request, name string, withKey bool) (uptimeLocation, string, error) {
	ctx := r.Context()
	name = strings.ToLower(strings.TrimSpace(name))
	if !locationNameRe.MatchString(name) {
		return uptimeLocation{}, "", badInput("a location name is 1–48 lowercase letters, digits, dots, dashes or underscores, starting with a letter or digit")
	}
	if name == serverLocation {
		return uptimeLocation{}, "", badInput(`"server" is the Visionary server itself and always exists`)
	}
	locs, err := s.listLocations(ctx)
	if err != nil {
		return uptimeLocation{}, "", err
	}
	if slices.ContainsFunc(locs, func(l uptimeLocation) bool { return l.Name == name }) {
		return uptimeLocation{}, "", badInput("there is already a location named " + strconv.Quote(name))
	}
	if len(locs) > maxLocations {
		return uptimeLocation{}, "", badInput(fmt.Sprintf("at most %d locations", maxLocations))
	}
	var keyID int64
	secret := ""
	if withKey {
		if keyID, secret, err = s.newLocationKey(r, name); err != nil {
			return uptimeLocation{}, "", err
		}
	}
	l := uptimeLocation{Name: name, KeyID: keyID, CreatedAt: uptimeNow(), State: "new"}
	if l.ID, err = s.db.InsertID(ctx, `INSERT INTO uptime_locations (name, key_id, created_at, browser) VALUES (?, ?, ?, 0)`, name, keyID, l.CreatedAt); err != nil {
		return uptimeLocation{}, "", err
	}
	if secret != "" {
		l.KeyPrefix = secret[:keyVisibleLen]
	}
	s.audit(r, "uptime.location.create", name, map[string]any{"id": l.ID})
	if len(locs) == 1 {
		if err := s.ensureLocationOfflineRule(r); err != nil {
			s.logger.Warn("location offline rule not created", "err", err)
		}
	}
	return l, secret, nil
}

// handleLocationKey replaces a location's key; the old one stops working.
func (s *Server) handleLocationKey(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	l, ok, err := s.getLocation(r.Context(), r.PathValue("name"))
	if err != nil {
		s.fail(w, err)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "location not found")
		return
	}
	id, secret, err := s.newLocationKey(r, l.Name)
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.db.Exec(r.Context(), `UPDATE uptime_locations SET key_id = ?, instance = NULL, lease_until = NULL WHERE id = ?`, id, l.ID); err != nil {
		s.fail(w, err)
		return
	}
	if err := s.revokeLocationKey(r.Context(), l.KeyID); err != nil {
		s.logger.Warn("old location key not revoked", "location", l.Name, "err", err)
	}
	s.audit(r, "uptime.location.key", l.Name, map[string]any{"id": l.ID})
	writeJSON(w, http.StatusOK, map[string]any{"secret": secret, "key_prefix": secret[:keyVisibleLen]})
}

func (s *Server) handleLocationDelete(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	ctx := r.Context()
	l, ok, err := s.getLocation(ctx, r.PathValue("name"))
	if err != nil {
		s.fail(w, err)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "location not found")
		return
	}
	checks, err := s.listUptimeChecks(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	var using []string
	for _, c := range checks {
		if c.runsAt(l.Name) {
			using = append(using, c.Name)
		}
	}
	if len(using) > 0 {
		writeError(w, http.StatusConflict, fmt.Sprintf("%d check(s) run from %s (%s); take it out of them first", len(using), l.Name, strings.Join(using, ", ")))
		return
	}
	if err := s.db.Exec(ctx, `DELETE FROM uptime_locations WHERE id = ?`, l.ID); err != nil {
		s.fail(w, err)
		return
	}
	if err := s.revokeLocationKey(ctx, l.KeyID); err != nil {
		s.logger.Warn("location key not revoked", "location", l.Name, "err", err)
	}
	s.audit(r, "uptime.location.delete", l.Name, map[string]any{"id": l.ID})
	w.WriteHeader(http.StatusNoContent)
}

// ensureLocationOfflineRule makes the rule that tells admins a location
// stopped reporting, with the first location.
func (s *Server) ensureLocationOfflineRule(r *http.Request) error {
	ctx := r.Context()
	rules, err := s.listRules(ctx, false)
	if err != nil {
		return err
	}
	for _, rule := range rules {
		if rule.Kind == "threshold" && rule.Spec.Metric == "uptime_location_offline" {
			return nil
		}
	}
	chans, err := s.listChannels(ctx)
	if err != nil {
		return err
	}
	rule := alertRule{Name: defaultLocationRuleName, Kind: "threshold", Severity: "warning", Channels: []int64{}, Enabled: true,
		Runbook: "Created with the first uptime location. Check that the location's runner (visionary-agent with VISIONARY_AGENT_LOCATION) is running and can reach the server; its checks are left out of the vote until it reports again.",
		Spec:    ruleSpec{Metric: "uptime_location_offline", Op: ">", Threshold: 5, Window: "10m", For: "0s"}}
	if len(chans) > 0 {
		rule.Channels = []int64{chans[0].ID}
	}
	if err := s.insertRule(ctx, &rule); err != nil {
		return err
	}
	s.alertEngine().invalidate(rule.ID)
	s.recordHistory(r, "create", nil, &rule)
	s.audit(r, "alert.rule.create", rule.Name, map[string]any{"id": rule.ID, "kind": rule.Kind, "metric": rule.Spec.Metric, "default": true})
	return nil
}

// observeLocations is the uptime_location_offline metric: minutes since each
// location's runner last polled.
func (s *Server) observeLocations(ctx context.Context, r alertRule, now time.Time) ([]observation, error) {
	locs, err := s.listLocations(ctx)
	if err != nil {
		if missingTable(err, "uptime_locations") {
			return nil, nil
		}
		return nil, err
	}
	var out []observation
	for _, l := range locs {
		if l.Builtin || l.LastSeenAt == "" {
			continue
		}
		seen, err := time.Parse(tsLayout, l.LastSeenAt)
		if err != nil {
			continue
		}
		mins := math.Floor(now.Sub(seen).Minutes()*10) / 10
		ob := observation{Key: l.Name, Label: "Location " + l.Name, Value: ptr(mins), Threshold: ptr(r.Spec.Threshold),
			Breach: compare(r.Spec.Op, mins, r.Spec.Threshold)}
		if ob.Breach {
			ob.Message = fmt.Sprintf("Uptime location %s has not reported for %s (threshold %s %s). Its checks are left out of the vote until it is back.",
				l.Name, humanDuration(now.Sub(seen)), r.Spec.Op, formatUnit(r.Spec.Threshold, "minutes"))
		} else {
			ob.Message = fmt.Sprintf("Uptime location %s is reporting.", l.Name)
		}
		out = append(out, ob)
	}
	return out, nil
}

// incidentLocations names the locations that failed during each incident:
// those the vote counted as failing (error "down in a, b: ..."), or the
// location of a check that runs from one place.
func (s *Server) incidentLocations(ctx context.Context, id int64, tr timeRange, list []incident) error {
	if len(list) == 0 {
		return nil
	}
	rows, err := s.db.Query(ctx, `SELECT created_at, COALESCE(location, 'server'), error FROM uptime_results
WHERE check_id = ? AND ok = 0 AND created_at >= ? AND created_at < ? ORDER BY created_at LIMIT 20000`,
		id, tr.From.UTC().Format(tsLayout), time.Now().UTC().Add(time.Minute).Format(tsLayout))
	if err != nil {
		return err
	}
	for i := range list {
		in := &list[i]
		end := "9999"
		if in.EndedAt != nil {
			end = *in.EndedAt
		}
		seen := map[string]bool{}
		for _, row := range rows.Rows {
			if len(row) != 3 {
				continue
			}
			at := toString(row[0])
			if at < in.StartedAt || at >= end {
				continue
			}
			names := []string{toString(row[1])}
			if msg, ok := strings.CutPrefix(toString(row[2]), "down in "); ok {
				list, _, _ := strings.Cut(msg, ":")
				names = strings.Split(list, ", ")
			}
			for _, loc := range names {
				if loc = strings.TrimSpace(loc); loc != "" && !seen[loc] {
					seen[loc] = true
					in.Locations = append(in.Locations, loc)
				}
			}
		}
		if len(in.Locations) == 1 && in.Locations[0] == serverLocation {
			in.Locations = nil
		}
		slices.Sort(in.Locations)
	}
	return nil
}

// --- location runners ---

// locationFor finds the location whose key the request carries.
func (s *Server) locationFor(w http.ResponseWriter, r *http.Request, name string) (uptimeLocation, bool) {
	k, ok := s.lookupKey(r.Context(), otlpKey(r), "location")
	if !ok {
		writeError(w, http.StatusUnauthorized, "missing, revoked or invalid location key")
		return uptimeLocation{}, false
	}
	rows, err := s.db.Query(r.Context(), `SELECT `+uptimeLocationCols+` FROM uptime_locations WHERE key_id = ?`, k.id)
	if err != nil {
		s.fail(w, err)
		return uptimeLocation{}, false
	}
	if len(rows.Rows) == 0 || len(rows.Rows[0]) != 12 {
		writeError(w, http.StatusUnauthorized, "this key belongs to no location")
		return uptimeLocation{}, false
	}
	l := scanLocation(rows.Rows[0])
	if name != l.Name {
		writeError(w, http.StatusForbidden, fmt.Sprintf("this key is for location %q, not %q", l.Name, name))
		return uptimeLocation{}, false
	}
	return l, true
}

func decodeAgentJSON(w http.ResponseWriter, r *http.Request, limit int64, dst any) bool {
	if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

func (s *Server) handleLocationPoll(w http.ResponseWriter, r *http.Request) {
	var body agentapi.LocationPoll
	if !decodeAgentJSON(w, r, 64<<10, &body) {
		return
	}
	l, ok := s.locationFor(w, r, body.Location)
	if !ok {
		return
	}
	if body.Instance == "" || len(body.Instance) > 64 {
		writeError(w, http.StatusBadRequest, "instance must be 1–64 characters")
		return
	}
	ctx := r.Context()
	now := time.Now().UTC()
	nowS, until := now.Format(tsLayout), now.Add(locationLease).Format(tsLayout)
	// The lease goes to whichever instance holds it, or to anyone once it
	// lapsed; one conditional update, so two runners cannot both take it.
	err := s.db.Exec(ctx, `UPDATE uptime_locations SET instance = ?, lease_until = ?, last_seen_at = ?, version = ?, host = ?, browser = ?,
  browser_version = ?, kinds = ?
WHERE id = ? AND (instance IS NULL OR instance = '' OR instance = ? OR lease_until IS NULL OR lease_until < ?)`,
		body.Instance, until, nowS, clip(body.Version, 64), clip(body.Host, 128), boolInt(body.Browser), clip(body.BrowserVersion, 128),
		clip(strings.Join(body.Kinds, ","), 255), l.ID, body.Instance, nowS)
	if err != nil {
		s.fail(w, err)
		return
	}
	cur, _, err := s.getLocation(ctx, l.Name)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := agentapi.LocationChecks{Active: cur.Instance == body.Instance, Checks: []uptimecore.Check{}, PollS: locationPollS,
		AllowPrivate: s.confBool("uptime_allow_private")}
	if !out.Active {
		out.Message = "another runner (" + fallback(cur.Host, "unknown host") + ") is running this location's checks; this one stands by"
		writeJSON(w, http.StatusOK, out)
		return
	}
	if l.Instance != body.Instance {
		s.logger.Info("uptime location runner connected", "location", l.Name, "host", body.Host, "version", body.Version)
	}
	checks, err := s.listUptimeChecks(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	for _, c := range checks {
		if c.Enabled && c.runsAt(l.Name) {
			out.Checks = append(out.Checks, c.core())
		}
	}
	writeJSON(w, http.StatusOK, out)
}

const locationMaxResults = 500

func (s *Server) handleLocationResults(w http.ResponseWriter, r *http.Request) {
	var body agentapi.LocationResults
	if !decodeAgentJSON(w, r, 32<<20, &body) {
		return
	}
	l, ok := s.locationFor(w, r, body.Location)
	if !ok {
		return
	}
	if body.Instance == "" || body.Instance != l.Instance {
		writeError(w, http.StatusConflict, "this runner does not hold the location's lease")
		return
	}
	if len(body.Results) > locationMaxResults {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("at most %d results per batch", locationMaxResults))
		return
	}
	ctx := r.Context()
	checks, err := s.listUptimeChecks(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	byID := map[int64]UptimeCheck{}
	for _, c := range checks {
		if c.runsAt(l.Name) {
			byID[c.ID] = c
		}
	}
	now := time.Now().UTC()
	stored := 0
	for _, in := range body.Results {
		c, ok := byID[in.CheckID]
		if !ok {
			continue
		}
		res := in.Result
		// The runner's clock may drift; a time outside the last day or in the
		// future becomes now.
		if at, err := time.Parse(tsLayout, res.CreatedAt); err != nil || at.After(now.Add(time.Minute)) || at.Before(now.Add(-24*time.Hour)) {
			res.CreatedAt = now.Format(tsLayout)
		}
		res.Error = clip(res.Error, 300)
		res.CertExpiresAt = clip(res.CertExpiresAt, 32)
		if err := s.recordResult(ctx, c, res, l.Name); err != nil {
			s.fail(w, err)
			return
		}
		if c.Kind == "browser" && in.Browser != nil {
			run := *in.Browser
			run.Screenshot, run.Final = in.Screenshot, in.FinalScreenshot
			if err := runnerFor(s).recordBrowser(ctx, c, res, run); err != nil {
				s.logger.Warn("browser run not saved", "check_id", c.ID, "location", l.Name, "err", err)
			}
		}
		stored++
	}
	if err := s.db.Exec(ctx, `UPDATE uptime_locations SET last_seen_at = ? WHERE id = ?`, now.Format(tsLayout), l.ID); err != nil {
		s.logger.Warn("location not marked seen", "location", l.Name, "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"stored": stored})
}
