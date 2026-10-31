package analytics

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBrowserCheckNormalize(t *testing.T) {
	ok := UptimeCheck{Name: "Login", Kind: "browser", Secrets: map[string]string{"PASS": "hunter22"}, Steps: []BrowserStep{
		{Action: "goto", Value: "https://shop.example.com/login"},
		{Action: "type", Selector: "#pass", Value: "{{secret.PASS}}"},
		{Action: "click", Text: "Sign in"},
		{Action: "assert_count", Selector: ".row", Count: 2},
	}}
	c := ok
	if err := c.normalize(nil); err != nil {
		t.Fatal(err)
	}
	if c.URL != "https://shop.example.com/login" || c.IntervalS != 300 || c.TimeoutMs != 30000 || c.Steps[3].Op != "eq" {
		t.Fatalf("defaults: %+v", c)
	}
	m := c.masked()
	if m.Secrets["PASS"] != uptimeMask || c.Secrets["PASS"] != "hunter22" {
		t.Fatalf("masking: %v %v", m.Secrets, c.Secrets)
	}
	// The mask sent back keeps the stored secret.
	again := m
	again.Steps = append([]BrowserStep(nil), m.Steps...)
	if err := again.normalize(&c); err != nil || again.Secrets["PASS"] != "hunter22" {
		t.Fatalf("round trip: %v %v", err, again.Secrets)
	}

	bad := []func(c *UptimeCheck){
		func(c *UptimeCheck) { c.Steps = nil },
		func(c *UptimeCheck) { c.Steps = c.Steps[1:] },
		func(c *UptimeCheck) { c.Steps = append(c.Steps, BrowserStep{Action: "hover"}) },
		func(c *UptimeCheck) { c.Steps = append(c.Steps, BrowserStep{Action: "teleport"}) },
		func(c *UptimeCheck) {
			c.Steps = append(c.Steps, BrowserStep{Action: "type", Selector: "#x", Value: "{{secret.NOPE}}"})
		},
		func(c *UptimeCheck) { c.Steps = append(c.Steps, BrowserStep{Action: "click"}) },
		func(c *UptimeCheck) { c.Steps = append(c.Steps, BrowserStep{Action: "wait", Ms: 60000}) },
		func(c *UptimeCheck) { c.Steps = []BrowserStep{{Action: "goto", Value: "file:///etc/passwd"}} },
		func(c *UptimeCheck) { c.Secrets = map[string]string{"bad name": "x"} },
		func(c *UptimeCheck) { c.TimeoutMs = 200000 },
		func(c *UptimeCheck) { c.MaxLCPMs, c.WarnLCPMs = 2000, 2500 },
		func(c *UptimeCheck) { c.MaxCLS = 20 },
		func(c *UptimeCheck) { c.Steps[1].MaxMs = -1 },
	}
	for i, f := range bad {
		c := ok
		c.Steps = append([]BrowserStep(nil), ok.Steps...)
		c.Secrets = map[string]string{"PASS": "hunter22"}
		f(&c)
		if err := c.normalize(nil); err == nil {
			t.Errorf("case %d: accepted", i)
		}
	}
}

func TestBrowserMasker(t *testing.T) {
	m := masker(map[string]string{"A": "s3cr3t value", "B": "ab"})
	got := m("typed s3cr3t value and s3cr3t+value, ab")
	if strings.Contains(got, "s3cr3t") || !strings.Contains(got, "ab") {
		t.Fatalf("masked: %q", got)
	}
}

// The guard proxy refuses loopback, as the HTTP checks' dialer does.
func TestGuardProxyRefusesPrivate(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "hi") }))
	defer site.Close()
	g, err := startGuardProxy()
	if err != nil {
		t.Fatal(err)
	}
	defer g.close()
	pu, _ := url.Parse(g.addr())
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}, Timeout: 5 * time.Second}
	resp, err := client.Get(site.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway || len(g.refused()) == 0 {
		t.Fatalf("status %d, refused %v", resp.StatusCode, g.refused())
	}
}

// testSite is a login form that leads to a dashboard.
func testSite() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `<!doctype html><title>Sign in</title><h1>Sign in</h1><form method="post" action="/login">
<input id="user" name="user"><input id="pass" name="pass" type="password">
<select id="team" name="team"><option value="a">Alpha</option><option value="b">Beta</option></select>
<button type="submit">Sign in</button></form>`)
	})
	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("pass") != "hunter22" || r.FormValue("team") != "b" {
			http.Redirect(w, r, "/login?failed=1", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/dashboard?user="+url.QueryEscape(r.FormValue("user")), http.StatusSeeOther)
	})
	mux.HandleFunc("GET /dashboard", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `<!doctype html><title>Dashboard</title><h1>Welcome `+r.URL.Query().Get("user")+`</h1>
<ul><li class="order">1</li><li class="order">2</li><li class="order">3</li></ul>
<img src="/missing.png"><script>console.error("widget failed")</script>`)
	})
	return httptest.NewServer(mux)
}

func TestBrowserCheckRun(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a browser")
	}
	if _, _, err := findBrowser(""); err != nil {
		t.Skip("no Chrome or Chromium on this machine")
	}
	s := logsTestServer(t, nil)
	s.live().Config["uptime_allow_private"] = "true"
	site := testSite()
	defer site.Close()
	u := newUptimeRunner(s, s.uptimeSettings())
	defer u.browser.close()
	ctx := context.Background()

	login := []BrowserStep{
		{Action: "goto", Value: site.URL + "/login"},
		{Action: "type", Selector: "#user", Value: "ann"},
		{Action: "type", Selector: "#pass", Value: "{{secret.PASS}}"},
		{Action: "select", Selector: "#team", Value: "Beta"},
		{Action: "click", Text: "Sign in"},
		{Action: "wait_navigation", Value: "/dashboard"},
		{Action: "assert_url", Value: "/dashboard"},
	}
	pass := UptimeCheck{Name: "Login works", Kind: "browser", Enabled: true, Secrets: map[string]string{"PASS": "hunter22"},
		Steps: append(append([]BrowserStep(nil), login...), BrowserStep{Action: "assert_text", Text: "Welcome ann"}, BrowserStep{Action: "assert_count", Selector: ".order", Count: 3})}
	if err := pass.normalize(nil); err != nil {
		t.Fatal(err)
	}
	pass.TimeoutMs = 30000
	if err := s.saveUptimeCheck(ctx, &pass); err != nil {
		t.Fatal(err)
	}
	res, run, err := u.runBrowser(ctx, pass)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || run.FailedStep != -1 || len(run.Steps) != len(pass.Steps) {
		t.Fatalf("passing check: %+v %+v", res, run.Steps)
	}
	if res.Status != 200 || res.TTFBMs == nil || res.LCPMs == nil || res.CLS == nil {
		t.Errorf("status %d ttfb %v lcp %v cls %v", res.Status, res.TTFBMs, res.LCPMs, res.CLS)
	}
	joined, _ := json.Marshal(run)
	if strings.Contains(string(joined), "hunter22") {
		t.Fatalf("secret in the run: %s", joined)
	}
	if len(run.Console) == 0 || len(run.Network) == 0 {
		t.Errorf("console %v network %v", run.Console, run.Network)
	}
	if err := u.record(ctx, pass, res); err != nil {
		t.Fatal(err)
	}
	if err := u.recordBrowser(ctx, pass, res, run); err != nil {
		t.Fatal(err)
	}

	fail := pass
	fail.ID = 0
	fail.Name = "Wrong text"
	fail.TimeoutMs = 15000
	fail.Steps = append(append([]BrowserStep(nil), login...), BrowserStep{Action: "assert_text", Text: "Goodbye"})
	if err := s.saveUptimeCheck(ctx, &fail); err != nil {
		t.Fatal(err)
	}
	res, run, err = u.playOnce(ctx, fail)
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || run.FailedStep != len(login) || run.Screenshot == nil || !strings.Contains(res.Error, "Goodbye") {
		t.Fatalf("failing check: %+v failed=%d shot=%d", res, run.FailedStep, len(run.Screenshot))
	}
	if err := u.record(ctx, fail, res); err != nil {
		t.Fatal(err)
	}
	if err := u.recordBrowser(ctx, fail, res, run); err != nil {
		t.Fatal(err)
	}

	// The runs list and one run in full.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/uptime/checks/{id}/runs", s.handleBrowserRuns)
	mux.HandleFunc("GET /api/uptime/checks/{id}/runs/{run}", s.handleBrowserRun)
	q := "?from=" + url.QueryEscape(time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)) + "&to=" + url.QueryEscape(time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/uptime/checks/"+strconv.FormatInt(fail.ID, 10)+"/runs"+q, nil))
	var list struct {
		Runs []struct {
			ID         int64 `json:"id"`
			FailedStep int   `json:"failed_step"`
			Screenshot bool  `json:"screenshot"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Runs) != 1 || !list.Runs[0].Screenshot {
		t.Fatalf("runs: %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/uptime/checks/"+strconv.FormatInt(fail.ID, 10)+"/runs/"+strconv.FormatInt(list.Runs[0].ID, 10), nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "data:image/jpeg;base64,") {
		t.Fatalf("run: %d %.300s", rec.Code, rec.Body)
	}

	// With the guard on, the browser may not open a loopback page.
	s.live().Config["uptime_allow_private"] = "false"
	guarded := newUptimeRunner(s, uptimeSettings{workers: 1, browserWorkers: 1})
	defer guarded.browser.close()
	res, _, err = guarded.playOnce(ctx, pass)
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || !res.blocked {
		t.Fatalf("guarded run: %+v", res)
	}
}
