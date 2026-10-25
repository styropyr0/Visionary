package analytics

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAdvanceClearFor(t *testing.T) {
	t0 := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)
	v := 5.0
	st := &seriesState{State: "ok", Since: t0.Format(tsLayout)}
	timing := ruleTiming{clearFor: 5 * time.Minute}
	if tr := advance(st, observation{Value: &v, Breach: true}, t0, timing); tr != "firing" {
		t.Fatalf("breach: %q", tr)
	}
	// Clear, but not for long enough.
	for i := 1; i <= 4; i++ {
		if tr := advance(st, observation{Value: &v}, t0.Add(time.Duration(i)*time.Minute), timing); tr != "" || st.State != "firing" {
			t.Fatalf("minute %d: %q %s", i, tr, st.State)
		}
	}
	// A breach again restarts the clear clock.
	advance(st, observation{Value: &v, Breach: true}, t0.Add(5*time.Minute), timing)
	if st.ClearSince != "" {
		t.Fatalf("clear since kept: %q", st.ClearSince)
	}
	advance(st, observation{Value: &v}, t0.Add(6*time.Minute), timing)
	if tr := advance(st, observation{Value: &v}, t0.Add(10*time.Minute), timing); tr != "" {
		t.Fatalf("resolved early: %q", tr)
	}
	if tr := advance(st, observation{Value: &v}, t0.Add(11*time.Minute), timing); tr != "resolved" {
		t.Fatalf("not resolved after clear for: %q", tr)
	}
}

func TestApplyNoData(t *testing.T) {
	firing := &seriesState{State: "firing"}
	ok := &seriesState{State: "ok"}
	none := observation{Message: "no data."}
	if ob := applyNoData("", firing, none); ob.Breach {
		t.Fatal("default policy breached")
	}
	if ob := applyNoData("alerting", ok, none); !ob.Breach || !strings.Contains(ob.Message, "No data counts as alerting") {
		t.Fatalf("alerting: %+v", ob)
	}
	if ob := applyNoData("keep", firing, none); !ob.Breach {
		t.Fatal("keep did not hold a firing series")
	}
	if ob := applyNoData("keep", ok, none); ob.Breach {
		t.Fatal("keep fired an ok series")
	}
	v := 1.0
	if ob := applyNoData("alerting", ok, observation{Value: &v}); ob.Breach {
		t.Fatal("a reading with a value is not no data")
	}
}

func TestApplyClearThreshold(t *testing.T) {
	ct := 80.0
	r := alertRule{Kind: "threshold", Spec: ruleSpec{Op: ">", Threshold: 90, ClearThreshold: &ct}}
	firing := &seriesState{State: "firing"}
	v := 85.0
	if ob := applyClearThreshold(r, firing, observation{Value: &v}); !ob.Breach {
		t.Fatal("85 is above the clear threshold; it should keep firing")
	}
	v = 79
	if ob := applyClearThreshold(r, firing, observation{Value: &v}); ob.Breach {
		t.Fatal("79 is below the clear threshold")
	}
	v = 85
	if ob := applyClearThreshold(r, &seriesState{State: "ok"}, observation{Value: &v}); ob.Breach {
		t.Fatal("the clear threshold only holds a firing series")
	}
}

func TestSilenceSchedule(t *testing.T) {
	once := silenceSchedule{Kind: "once", StartsAt: "2026-03-01T10:00:00Z", EndsAt: "2026-03-01T12:00:00Z"}
	at := func(s string) time.Time { v, _ := time.Parse(time.RFC3339, s); return v }
	for _, c := range []struct {
		t    string
		want bool
	}{{"2026-03-01T09:59:59Z", false}, {"2026-03-01T10:00:00Z", true}, {"2026-03-01T11:59:00Z", true}, {"2026-03-01T12:00:00Z", false}} {
		if got := once.active(at(c.t)); got != c.want {
			t.Errorf("once at %s: %v", c.t, got)
		}
	}
	if !once.ended(at("2026-03-02T00:00:00Z")) || once.ended(at("2026-03-01T11:00:00Z")) {
		t.Error("ended")
	}
	// Weeknights 22:00 to 07:00 in Kolkata (UTC+5:30); Friday night runs
	// into Saturday morning.
	night := silenceSchedule{Kind: "weekly", Days: []int{1, 2, 3, 4, 5}, From: "22:00", To: "07:00", TZ: "Asia/Kolkata"}
	for _, c := range []struct {
		t    string
		want bool
	}{
		{"2026-03-06T17:00:00Z", true},  // Fri 22:30 local
		{"2026-03-07T01:00:00Z", true},  // Sat 06:30 local, from Friday
		{"2026-03-07T02:00:00Z", false}, // Sat 07:30 local
		{"2026-03-07T17:00:00Z", false}, // Sat 22:30 local
		{"2026-03-08T01:00:00Z", false}, // Sun 06:30 local, Saturday is off
		{"2026-03-09T01:00:00Z", false}, // Mon 06:30 local, Sunday is off
		{"2026-03-09T17:00:00Z", true},  // Mon 22:30 local
	} {
		if got := night.active(at(c.t)); got != c.want {
			t.Errorf("weekly at %s: %v", c.t, got)
		}
	}
	day := silenceSchedule{Kind: "weekly", Days: []int{0, 6}, From: "00:00", To: "23:59"}
	if !day.active(at("2026-03-07T12:00:00Z")) || day.active(at("2026-03-09T12:00:00Z")) {
		t.Error("weekend")
	}
}

func TestSilenceMatchers(t *testing.T) {
	r := alertRule{ID: 7, Severity: "warning", Spec: ruleSpec{Labels: map[string]string{"team": "payments"}}}
	cases := []struct {
		m      silenceMatchers
		series string
		want   bool
	}{
		{silenceMatchers{}, "", true},
		{silenceMatchers{RuleIDs: []int64{7}}, "", true},
		{silenceMatchers{RuleIDs: []int64{8}}, "", false},
		{silenceMatchers{Severities: []string{"info", "warning"}}, "", true},
		{silenceMatchers{Severities: []string{"critical"}}, "", false},
		{silenceMatchers{Labels: map[string]string{"team": "payments"}}, "", true},
		{silenceMatchers{Labels: map[string]string{"team": "search"}}, "", false},
		{silenceMatchers{Series: "web-*"}, "web-2", true},
		{silenceMatchers{Series: "web-*"}, "db-1", false},
		{silenceMatchers{RuleIDs: []int64{7}, Series: "db-*"}, "web-2", false},
	}
	for i, c := range cases {
		if got := c.m.match(r, c.series); got != c.want {
			t.Errorf("case %d: %v", i, got)
		}
	}
}

func TestSilenceCheck(t *testing.T) {
	bad := []alertSilence{
		{Name: "", Schedule: silenceSchedule{Kind: "once", StartsAt: "2026-03-01T10:00:00Z", EndsAt: "2026-03-01T12:00:00Z"}},
		{Name: "x", Schedule: silenceSchedule{Kind: "once", StartsAt: "2026-03-01T12:00:00Z", EndsAt: "2026-03-01T10:00:00Z"}},
		{Name: "x", Schedule: silenceSchedule{Kind: "weekly", Days: []int{}, From: "22:00", To: "07:00"}},
		{Name: "x", Schedule: silenceSchedule{Kind: "weekly", Days: []int{1}, From: "25:00", To: "07:00"}},
		{Name: "x", Schedule: silenceSchedule{Kind: "weekly", Days: []int{1}, From: "22:00", To: "07:00", TZ: "Mars/Base"}},
		{Name: "x", Matchers: silenceMatchers{Severities: []string{"urgent"}}, Schedule: silenceSchedule{Kind: "weekly", Days: []int{1}, From: "22:00", To: "07:00"}},
		{Name: "x", Matchers: silenceMatchers{Series: "[unclosed"}, Schedule: silenceSchedule{Kind: "weekly", Days: []int{1}, From: "22:00", To: "07:00"}},
	}
	for i, sl := range bad {
		if err := sl.check(); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
	good := alertSilence{Name: " Deploy ", Schedule: silenceSchedule{Kind: "once", StartsAt: "2026-03-01T15:30:00+05:30", EndsAt: "2026-03-01T16:00:00+05:30"}}
	if err := good.check(); err != nil {
		t.Fatal(err)
	}
	if good.Name != "Deploy" || good.Schedule.StartsAt != "2026-03-01T10:00:00Z" {
		t.Fatalf("not normalized: %+v", good)
	}
}

func TestRenderTemplate(t *testing.T) {
	v := 12.345
	r := alertRule{Name: "Checkout errors", Severity: "critical", Spec: ruleSpec{Metric: "error_rate_5xx", Labels: map[string]string{"team": "payments"}}}
	ev := &alertEvent{SeriesKey: "web-1", Value: &v, Message: "5xx is 12%"}
	got := renderTemplate("{{state}} {{ rule }} on {{series}}: {{value}} ({{labels.team}}) {{nope}}", templateVars(r, ev, "FIRING"))
	if got != "FIRING Checkout errors on web-1: 12.35 (payments) {{nope}}" {
		t.Fatalf("got %q", got)
	}
}

func TestDiffRules(t *testing.T) {
	a := alertRule{Name: "a", Severity: "warning", Channels: []int64{1}, Spec: ruleSpec{Metric: "apdex", Threshold: 0.8, Window: "5m"}}
	b := a
	b.Channels = []int64{1, 2}
	b.Spec.Threshold, b.Spec.ClearFor = 0.7, "5m"
	got := strings.Join(diffRules(&a, &b), ",")
	if got != "channels,clear for,threshold" {
		t.Fatalf("got %q", got)
	}
	if len(diffRules(&a, &a)) != 0 {
		t.Fatal("no changes expected")
	}
}

func TestConditionKey(t *testing.T) {
	a := ruleSpec{Metric: "apdex", Op: "<", Threshold: 0.8, Window: "5m"}
	b := a
	b.Labels, b.Mention, b.Repeat, b.ClearFor = map[string]string{"x": "y"}, "@here", "1h", "10m"
	b.Escalation = []escalationStep{{After: "10m", Channels: []int64{1}}}
	if a.conditionKey() != b.conditionKey() {
		t.Fatal("notification options changed the condition")
	}
	b.Threshold = 0.9
	if a.conditionKey() == b.conditionKey() {
		t.Fatal("threshold did not change the condition")
	}
}

func TestValidateServiceConfig(t *testing.T) {
	ok := []struct {
		kind string
		c    channelConfig
	}{
		{"pagerduty", channelConfig{Secret: "R0123456789abcdef0123456789abcdef"}},
		{"opsgenie", channelConfig{Secret: "0123456789abcdef-0123-4567", Region: "eu"}},
		{"telegram", channelConfig{Secret: "123456789:AAH-abcdefghijklmnopqrstuvwxyz012", ChatID: "-1001234567890"}},
		{"telegram", channelConfig{Secret: "123456789:AAH-abcdefghijklmnopqrstuvwxyz012", ChatID: "@ops_alerts"}},
		{"sms", channelConfig{AccountSID: testTwilioSID, Secret: "0123456789abcdef0123", From: "+14155550100", To: []string{"+919876543210"}}},
	}
	for _, c := range ok {
		if err := validateChannelConfig(c.kind, c.c); err != nil {
			t.Errorf("%s: %v", c.kind, err)
		}
	}
	bad := []struct {
		kind string
		c    channelConfig
	}{
		{"pagerduty", channelConfig{Secret: "short"}},
		{"opsgenie", channelConfig{Secret: "0123456789abcdef-0123-4567", Region: "mars"}},
		{"telegram", channelConfig{Secret: "nope", ChatID: "1"}},
		{"telegram", channelConfig{Secret: "123456789:AAH-abcdefghijklmnopqrstuvwxyz012", ChatID: "ops"}},
		{"sms", channelConfig{AccountSID: "AC01", Secret: "0123456789abcdef0123", From: "+14155550100", To: []string{"+919876543210"}}},
		{"sms", channelConfig{AccountSID: testTwilioSID, Secret: "0123456789abcdef0123", From: "+14155550100", To: []string{"98765"}}},
		{"teams", channelConfig{URL: "ftp://x"}},
		{"carrier-pigeon", channelConfig{}},
	}
	for _, c := range bad {
		if err := validateChannelConfig(c.kind, c.c); err == nil {
			t.Errorf("%s %+v accepted", c.kind, c.c)
		}
	}
}

// recorder is a stand-in for a service: it keeps what it was sent and
// answers with the next status in its list (200 once they run out).
type recorder struct {
	mu       sync.Mutex
	statuses []int
	reqs     []recorded
	got      chan struct{}
}

type recorded struct {
	path, auth, ctype string
	body              []byte
}

func newRecorder(statuses ...int) (*recorder, *httptest.Server) {
	rec := &recorder{statuses: statuses, got: make(chan struct{}, 100)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.reqs = append(rec.reqs, recorded{path: r.URL.RequestURI(), auth: r.Header.Get("Authorization"), ctype: r.Header.Get("Content-Type"), body: b})
		code := 200
		if len(rec.statuses) > 0 {
			code, rec.statuses = rec.statuses[0], rec.statuses[1:]
		}
		rec.mu.Unlock()
		w.WriteHeader(code)
		rec.got <- struct{}{}
	}))
	return rec, srv
}

func (r *recorder) wait(t *testing.T, n int) []recorded {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		r.mu.Lock()
		have := len(r.reqs)
		r.mu.Unlock()
		if have >= n {
			r.mu.Lock()
			defer r.mu.Unlock()
			return append([]recorded(nil), r.reqs...)
		}
		select {
		case <-r.got:
		case <-deadline:
			t.Fatalf("waited for %d requests, got %d", n, have)
		}
	}
}

func TestServiceBodies(t *testing.T) {
	rec, srv := newRecorder()
	defer srv.Close()
	old := []string{pagerDutyURL, opsgenieURL["us"], telegramAPI, twilioAPI}
	pagerDutyURL, opsgenieURL["us"], telegramAPI, twilioAPI = srv.URL+"/pd", srv.URL+"/og", srv.URL+"/tg", srv.URL+"/tw"
	defer func() { pagerDutyURL, opsgenieURL["us"], telegramAPI, twilioAPI = old[0], old[1], old[2], old[3] }()
	ctx := context.Background()
	n := notice{Event: "firing", Title: "[FIRING] Checkout errors", Severity: "critical", Lines: []string{"5xx is 12%"}, Key: "visionary-rule-3", At: time.Now().UTC()}

	if err := sendPagerDuty(ctx, channelConfig{Secret: "routing-key-0123456789"}, n); err != nil {
		t.Fatal(err)
	}
	resolved := n
	resolved.Event = "resolved"
	if err := sendPagerDuty(ctx, channelConfig{Secret: "routing-key-0123456789"}, resolved); err != nil {
		t.Fatal(err)
	}
	if err := sendOpsgenie(ctx, channelConfig{Secret: "genie-key"}, n); err != nil {
		t.Fatal(err)
	}
	if err := sendOpsgenie(ctx, channelConfig{Secret: "genie-key"}, resolved); err != nil {
		t.Fatal(err)
	}
	if err := sendTelegram(ctx, channelConfig{Secret: "123:tok", ChatID: "42"}, n); err != nil {
		t.Fatal(err)
	}
	if err := sendSMS(ctx, channelConfig{AccountSID: "AC1", Secret: "tok", From: "+14155550100", To: []string{"+15550001", "+15550002"}}, n); err != nil {
		t.Fatal(err)
	}
	reqs := rec.wait(t, 7)

	var pd map[string]any
	_ = json.Unmarshal(reqs[0].body, &pd)
	if reqs[0].path != "/pd" || pd["event_action"] != "trigger" || pd["dedup_key"] != "visionary-rule-3" || pd["payload"].(map[string]any)["severity"] != "critical" {
		t.Fatalf("pagerduty trigger: %s %s", reqs[0].path, reqs[0].body)
	}
	_ = json.Unmarshal(reqs[1].body, &pd)
	if pd["event_action"] != "resolve" || pd["dedup_key"] != "visionary-rule-3" {
		t.Fatalf("pagerduty resolve: %s", reqs[1].body)
	}
	var og map[string]any
	_ = json.Unmarshal(reqs[2].body, &og)
	if reqs[2].path != "/og/v2/alerts" || reqs[2].auth != "GenieKey genie-key" || og["alias"] != "visionary-rule-3" || og["priority"] != "P1" {
		t.Fatalf("opsgenie create: %s %s %s", reqs[2].path, reqs[2].auth, reqs[2].body)
	}
	if reqs[3].path != "/og/v2/alerts/visionary-rule-3/close?identifierType=alias" {
		t.Fatalf("opsgenie close: %s", reqs[3].path)
	}
	if reqs[4].path != "/tg/bot123:tok/sendMessage" || !strings.Contains(string(reqs[4].body), `"chat_id":"42"`) {
		t.Fatalf("telegram: %s %s", reqs[4].path, reqs[4].body)
	}
	form, _ := url.ParseQuery(string(reqs[5].body))
	if reqs[5].path != "/tw/2010-04-01/Accounts/AC1/Messages.json" || form.Get("To") != "+15550001" || form.Get("From") != "+14155550100" ||
		!strings.HasPrefix(reqs[5].auth, "Basic ") || reqs[5].ctype != "application/x-www-form-urlencoded" {
		t.Fatalf("sms: %s %s %v", reqs[5].path, reqs[5].auth, form)
	}
}

func TestNoticeRoundTrip(t *testing.T) {
	n := notice{Event: "firing", Title: "t", Fields: [][2]string{{"a", "b"}}, Payload: map[string]any{"x": 1.0}, At: time.Now().UTC().Truncate(time.Second), Key: "k"}
	b, err := json.Marshal(outMessage{Notice: n, EventIDs: []int64{4}})
	if err != nil {
		t.Fatal(err)
	}
	var back outMessage
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Notice.Title != "t" || back.Notice.Fields[0][1] != "b" || back.Notice.Key != "k" || !back.Notice.At.Equal(n.At) || back.EventIDs[0] != 4 {
		t.Fatalf("%+v", back)
	}
}

func TestCSVCell(t *testing.T) {
	for in, want := range map[string]string{"=HYPERLINK(1)": "'=HYPERLINK(1)", "+1": "'+1", "-2": "'-2", "@x": "'@x", "fine": "fine", "": ""} {
		if got := csvCell(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

// openTestDB is a server on a fresh SQLite file whose alert tables have the
// shape of the first release, so Init has to add the newer columns.
func openTestDB(t *testing.T) *Server {
	t.Helper()
	path := filepath.ToSlash(filepath.Join(t.TempDir(), "alerts.db"))
	s, err := New(Config{DatabaseURL: "sqlite:///" + path}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	old := `CREATE TABLE alert_state (
    rule_id INTEGER NOT NULL, series_key TEXT NOT NULL, label TEXT NOT NULL DEFAULT '', state TEXT NOT NULL,
    state_since TEXT NOT NULL, pending_since TEXT NOT NULL DEFAULT '', notified_at TEXT NOT NULL DEFAULT '',
    evaluated_at TEXT NOT NULL DEFAULT '', value REAL, threshold REAL, message TEXT NOT NULL DEFAULT '',
    recent TEXT NOT NULL DEFAULT '', PRIMARY KEY (rule_id, series_key));
CREATE TABLE alert_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT, created_at TEXT NOT NULL, rule_id INTEGER NOT NULL, rule_name TEXT NOT NULL,
    kind TEXT NOT NULL, severity TEXT NOT NULL, state TEXT NOT NULL, series_key TEXT NOT NULL DEFAULT '',
    value REAL, threshold REAL, message TEXT NOT NULL, explanation TEXT NOT NULL DEFAULT '', notified INTEGER NOT NULL DEFAULT 0)`
	if err := s.db.ExecScript(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.db.Close() })
	return s
}

func addRequests(t *testing.T, s *Server, at time.Time, n, status int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := s.db.Exec(context.Background(), `INSERT INTO api_logs (request_id, created_at, method, path, route, status, duration_ms) VALUES (?, ?, 'GET', '/pay', '/pay', ?, 20)`,
			"r", at.Add(time.Duration(i)*time.Millisecond).Format(tsLayout), status); err != nil {
			t.Fatal(err)
		}
	}
}

func eventsOf(t *testing.T, s *Server, ruleID int64) []alertEvent {
	t.Helper()
	evs, err := s.listEvents(context.Background(), eventFilter{From: time.Now().UTC().Add(-time.Hour), To: time.Now().UTC().Add(2 * time.Hour), RuleID: ruleID})
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func TestAlertsEndToEnd(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	e := s.alertEngine()

	// The first send fails, so the delivery has to be retried.
	rec, srv := newRecorder(500)
	defer srv.Close()
	ch := alertChannel{Name: "hook", Kind: "webhook", Config: channelConfig{URL: srv.URL + "/main"}}
	if err := s.saveChannel(ctx, &ch); err != nil {
		t.Fatal(err)
	}
	esc := alertChannel{Name: "pager", Kind: "webhook", Config: channelConfig{URL: srv.URL + "/escalation"}}
	if err := s.saveChannel(ctx, &esc); err != nil {
		t.Fatal(err)
	}
	rule := alertRule{Name: "Pay errors", Kind: "threshold", Severity: "critical", Channels: []int64{ch.ID}, Enabled: true,
		Spec: ruleSpec{Metric: "error_rate_5xx", Op: ">", Threshold: 2, Window: "5m", For: "0s", Mention: "@oncall",
			Escalation: []escalationStep{{After: "10m", Channels: []int64{esc.ID}}}}}
	if err := s.insertRule(ctx, &rule); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	addRequests(t, s, now.Add(-2*time.Minute), 20, 500)
	if err := e.evaluate(ctx, rule, now); err != nil {
		t.Fatal(err)
	}
	reqs := rec.wait(t, 1)
	if !strings.Contains(string(reqs[0].body), `"event":"alert.firing"`) {
		t.Fatalf("first send: %s", reqs[0].body)
	}
	evs := eventsOf(t, s, rule.ID)
	if len(evs) != 1 || evs[0].State != "firing" || evs[0].Suppressed != "" {
		t.Fatalf("events: %+v", evs)
	}
	waitFor(t, func() bool {
		d, _ := s.eventDeliveries(ctx, []int64{evs[0].ID})
		return len(d[evs[0].ID]) == 1 && d[evs[0].ID][0].Status == "pending" && d[evs[0].ID][0].Attempts == 1
	})

	// The retry loop sends it again once the backoff has passed.
	s.retryDue(ctx, time.Now().UTC().Add(time.Minute))
	rec.wait(t, 2)
	d, _ := s.eventDeliveries(ctx, []int64{evs[0].ID})
	if got := d[evs[0].ID][0]; got.Status != "sent" || got.Attempts != 2 {
		t.Fatalf("after retry: %+v", got)
	}
	if evs = eventsOf(t, s, rule.ID); !evs[0].Notified {
		t.Fatal("event not marked notified after the retry")
	}

	// Nobody acknowledged it: after 10 minutes it escalates, once.
	addRequests(t, s, now.Add(9*time.Minute), 20, 500)
	if err := e.evaluate(ctx, rule, now.Add(11*time.Minute)); err != nil {
		t.Fatal(err)
	}
	reqs = rec.wait(t, 3)
	if reqs[2].path != "/escalation" || !strings.Contains(string(reqs[2].body), "alert.escalation") {
		t.Fatalf("escalation: %s %s", reqs[2].path, reqs[2].body)
	}
	if err := e.evaluate(ctx, rule, now.Add(12*time.Minute)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if n := len(rec.wait(t, 3)); n != 3 {
		t.Fatalf("escalated twice: %d requests", n)
	}

	// Traffic recovers: it resolves and says so.
	addRequests(t, s, now.Add(19*time.Minute), 50, 200)
	if err := e.evaluate(ctx, rule, now.Add(20*time.Minute)); err != nil {
		t.Fatal(err)
	}
	reqs = rec.wait(t, 4)
	if !strings.Contains(string(reqs[3].body), "alert.resolved") {
		t.Fatalf("resolve: %s", reqs[3].body)
	}
	open, _ := s.openIncidents(ctx, rule.ID)
	if len(open[rule.ID]) != 0 {
		t.Fatalf("incident still open: %+v", open)
	}

	// A silence holds the next firing back and says which one.
	sl := alertSilence{Name: "Deploy window", Enabled: true, Matchers: silenceMatchers{RuleIDs: []int64{rule.ID}},
		Schedule: silenceSchedule{Kind: "once", StartsAt: now.Add(25 * time.Minute).Format(time.RFC3339), EndsAt: now.Add(40 * time.Minute).Format(time.RFC3339)}}
	if err := s.saveSilence(ctx, &sl); err != nil {
		t.Fatal(err)
	}
	e.invalidateSilences()
	addRequests(t, s, now.Add(29*time.Minute), 20, 500)
	if err := e.evaluate(ctx, rule, now.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	evs = eventsOf(t, s, rule.ID)
	if evs[0].State != "firing" || evs[0].Suppressed != "silence: Deploy window" {
		t.Fatalf("silenced event: %+v", evs[0])
	}
	// Its resolution is not sent either, since its firing was not.
	addRequests(t, s, now.Add(44*time.Minute), 50, 200)
	if err := e.evaluate(ctx, rule, now.Add(45*time.Minute)); err != nil {
		t.Fatal(err)
	}
	evs = eventsOf(t, s, rule.ID)
	if evs[0].State != "resolved" || evs[0].Suppressed != "its firing was not sent" {
		t.Fatalf("resolution of a silenced firing: %+v", evs[0])
	}
	time.Sleep(200 * time.Millisecond)
	if n := len(rec.wait(t, 4)); n != 4 {
		t.Fatalf("silenced alert was sent: %d requests", n)
	}
}

// No data fires a rule set to alerting on it, and the dependency holds back
// a rule while the one it depends on fires.
func TestNoDataAndDependencies(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	e := s.alertEngine()
	now := time.Now().UTC().Truncate(time.Second)

	quiet := alertRule{Name: "Pay quiet", Kind: "threshold", Severity: "warning", Channels: []int64{}, Enabled: true,
		Spec: ruleSpec{Metric: "latency_p95", Op: ">", Threshold: 1000, Window: "5m", For: "0s", NoData: "alerting"}}
	if err := s.insertRule(ctx, &quiet); err != nil {
		t.Fatal(err)
	}
	if err := e.evaluate(ctx, quiet, now); err != nil {
		t.Fatal(err)
	}
	evs := eventsOf(t, s, quiet.ID)
	if len(evs) != 1 || evs[0].State != "firing" || !strings.Contains(evs[0].Message, "No data counts as alerting") {
		t.Fatalf("no data: %+v", evs)
	}

	child := alertRule{Name: "Pay slow", Kind: "threshold", Severity: "warning", Channels: []int64{}, Enabled: true,
		Spec: ruleSpec{Metric: "requests_per_min", Op: "<", Threshold: 1, Window: "5m", For: "0s", DependsOn: []int64{quiet.ID}}}
	if err := s.insertRule(ctx, &child); err != nil {
		t.Fatal(err)
	}
	e.invalidate(0)
	if err := e.evaluate(ctx, child, now); err != nil {
		t.Fatal(err)
	}
	evs = eventsOf(t, s, child.ID)
	if len(evs) != 1 || evs[0].Suppressed != "depends on: Pay quiet" {
		t.Fatalf("dependency: %+v", evs)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// testTwilioSID is shaped like a Twilio Account SID; it is split so secret
// scanners don't take it for a real one.
const testTwilioSID = "AC" + "0123456789abcdef0123456789abcdef"
