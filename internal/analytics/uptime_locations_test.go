package analytics

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/styropyr0/visionary/internal/agentapi"
	"github.com/styropyr0/visionary/internal/uptimecore"
)

func TestLocationsDown(t *testing.T) {
	cases := []struct {
		rule      string
		failed, n int
		down      bool
	}{
		{"majority", 0, 3, false}, {"majority", 1, 3, false}, {"majority", 2, 3, true}, {"majority", 1, 2, false},
		{"majority", 1, 1, true}, {"any", 1, 3, true}, {"all", 2, 3, false}, {"all", 3, 3, true}, {"all", 1, 1, true},
	}
	for _, c := range cases {
		if got := locationsDown(c.rule, c.failed, c.n); got != c.down {
			t.Errorf("%s %d/%d: down=%v", c.rule, c.failed, c.n, got)
		}
	}
}

func TestCheckLocationsNormalize(t *testing.T) {
	c := UptimeCheck{Name: "x", URL: "https://example.com", Locations: []string{" Mumbai ", "mumbai", "server"}}
	if err := c.normalize(nil); err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.Locations, ",") != "mumbai,server" || c.LocationRule != "majority" {
		t.Fatalf("got %v %q", c.Locations, c.LocationRule)
	}
	c = UptimeCheck{Name: "x", URL: "https://example.com", LocationRule: "most"}
	if err := c.normalize(nil); err == nil {
		t.Fatal("bad rule accepted")
	}
	c = UptimeCheck{Name: "x", URL: "https://example.com"}
	if err := c.normalize(nil); err != nil || len(c.Locations) != 1 || c.Locations[0] != serverLocation {
		t.Fatalf("default: %v %v", err, c.Locations)
	}
}

// Two runner locations and the server vote on a check by majority; a
// location that stops polling drops out of the vote.
func TestLocationRunnersVote(t *testing.T) {
	s := logsTestServer(t, nil)
	ctx := context.Background()
	admin := httptest.NewRequest("POST", "/api/uptime/locations", nil)
	_, keyA, err := s.createLocation(admin, "loc-a", true)
	if err != nil {
		t.Fatal(err)
	}
	_, keyB, err := s.createLocation(admin, "loc-b", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.createLocation(admin, "server", true); err == nil {
		t.Fatal("a location named server was made")
	}
	c := UptimeCheck{Name: "Shop", URL: "https://shop.example.com", Enabled: true, FollowRedirects: true,
		Locations: []string{"loc-a", "loc-b", "server"}}
	if err := c.normalize(nil); err != nil {
		t.Fatal(err)
	}
	if err := s.checkLocations(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := s.saveUptimeCheck(ctx, &c); err != nil {
		t.Fatal(err)
	}
	other := UptimeCheck{Name: "Server only", URL: "https://example.com", Enabled: true}
	_ = other.normalize(nil)
	if err := s.saveUptimeCheck(ctx, &other); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST "+agentapi.LocationPollPath, s.handleLocationPoll)
	mux.HandleFunc("POST "+agentapi.LocationResultsPath, s.handleLocationResults)
	post := func(path, key string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest("POST", path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+key)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	poll := func(key, loc, instance string) agentapi.LocationChecks {
		t.Helper()
		rec := post(agentapi.LocationPollPath, key, agentapi.LocationPoll{Location: loc, Instance: instance, Version: "test", Host: "h", Kinds: []string{"http"}})
		var out agentapi.LocationChecks
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatalf("poll %s/%s: %d %s", loc, instance, rec.Code, rec.Body)
		}
		return out
	}
	if got := poll(keyA, "loc-a", "a1"); !got.Active || len(got.Checks) != 1 || got.Checks[0].ID != c.ID {
		t.Fatalf("first runner: %+v", got)
	}
	if got := poll(keyA, "loc-a", "a2"); got.Active || len(got.Checks) != 0 {
		t.Fatalf("second runner of one location: %+v", got)
	}
	poll(keyB, "loc-b", "b1")
	if rec := post(agentapi.LocationPollPath, keyA, agentapi.LocationPoll{Location: "loc-b", Instance: "x"}); rec.Code != http.StatusForbidden {
		t.Fatalf("key of another location: %d", rec.Code)
	}
	if rec := post(agentapi.LocationPollPath, "vis_nope", agentapi.LocationPoll{Location: "loc-a", Instance: "x"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad key: %d", rec.Code)
	}

	at := time.Now().UTC()
	send := func(key, loc, instance string, ok bool) int {
		at = at.Add(time.Second)
		res := uptimecore.Result{CreatedAt: at.Format(tsLayout), OK: ok, Status: 200, DurationMs: 12}
		if !ok {
			res.Status, res.Error = 503, "unexpected status 503 (expected 200-299)"
		}
		rec := post(agentapi.LocationResultsPath, key, agentapi.LocationResults{Location: loc, Instance: instance,
			Results: []agentapi.LocationResult{{CheckID: c.ID, Result: res}, {CheckID: other.ID, Result: res}}})
		return rec.Code
	}
	latest := func() (int64, string) {
		t.Helper()
		rows, err := s.db.Query(ctx, `SELECT ok, error FROM uptime_results WHERE check_id = ? ORDER BY created_at DESC LIMIT 1`, c.ID)
		if err != nil || len(rows.Rows) != 1 {
			t.Fatalf("latest: %v", err)
		}
		return toInt(rows.Rows[0][0]), toString(rows.Rows[0][1])
	}
	at = at.Add(time.Second)
	if err := runnerFor(s).record(ctx, c, uptimecore.Result{CreatedAt: at.Format(tsLayout), OK: true, Status: 200}); err != nil {
		t.Fatal(err)
	}
	if code := send(keyA, "loc-a", "a1", false); code != 200 {
		t.Fatalf("results: %d", code)
	}
	if ok, _ := latest(); ok != 1 {
		t.Fatal("one of three locations failing took the check down")
	}
	send(keyB, "loc-b", "b1", false)
	if ok, msg := latest(); ok != 0 || !strings.HasPrefix(msg, "down in loc-a, loc-b") {
		t.Fatalf("two of three failing: ok=%d %q", ok, msg)
	}
	if code := send(keyA, "loc-a", "a2", true); code != http.StatusConflict {
		t.Fatalf("results from the standby runner: %d", code)
	}
	var n int64
	if rows, _ := s.db.Query(ctx, `SELECT COUNT(*) FROM uptime_results WHERE check_id = ?`, other.ID); len(rows.Rows) == 1 {
		n = toInt(rows.Rows[0][0])
	}
	if n != 0 {
		t.Fatalf("results stored for a check the location does not run: %d", n)
	}

	incidents, _, err := s.checkIncidents(ctx, c.ID, timeRange{From: time.Now().UTC().Add(-time.Hour), To: time.Now().UTC().Add(time.Hour), Bucket: 60})
	if err != nil || len(incidents) != 1 || strings.Join(incidents[0].Locations, ",") != "loc-a,loc-b" {
		t.Fatalf("incidents: %v %+v", err, incidents)
	}

	// loc-b stops polling: it is stale and out of the vote.
	old := time.Now().UTC().Add(-10 * time.Minute).Format(tsLayout)
	if err := s.db.Exec(ctx, `UPDATE uptime_locations SET last_seen_at = ? WHERE name = 'loc-b'`, old); err != nil {
		t.Fatal(err)
	}
	send(keyA, "loc-a", "a1", false)
	if ok, msg := latest(); ok != 1 {
		t.Fatalf("a stale location still counted: %q", msg)
	}
	states, err := s.locationStates(ctx, []UptimeCheck{c})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, l := range states[c.ID] {
		got[l.Name] = l.State
	}
	if got["loc-a"] != "down" || got["loc-b"] != "stale" || got["server"] != "up" {
		t.Fatalf("location states: %v", got)
	}
	obs, err := s.observeLocations(ctx, alertRule{Spec: ruleSpec{Op: ">", Threshold: 5}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	firing := map[string]bool{}
	for _, o := range obs {
		firing[o.Key] = o.Breach
	}
	if !firing["loc-b"] || firing["loc-a"] {
		t.Fatalf("offline alert: %v", firing)
	}
}
