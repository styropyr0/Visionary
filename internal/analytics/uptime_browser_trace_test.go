package analytics

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestNormalizeOrigin(t *testing.T) {
	for in, want := range map[string]string{
		"https://API.example.com/": "https://api.example.com",
		"http://localhost:8080":    "http://localhost:8080",
		"https://*.example.com":    "https://*.example.com",
	} {
		if got, err := normalizeOrigin(in); err != nil || got != want {
			t.Errorf("%s: %q %v", in, got, err)
		}
	}
	for _, in := range []string{"example.com", "https://example.com/api", "ftp://example.com", "https://u:p@example.com"} {
		if _, err := normalizeOrigin(in); err == nil {
			t.Errorf("%s accepted", in)
		}
	}
}

// browserRunner is a runner that may open loopback pages, or a skip when
// there is no browser.
func browserRunner(t *testing.T) (*Server, *uptimeRunner) {
	t.Helper()
	if testing.Short() {
		t.Skip("starts a browser")
	}
	if _, _, err := findBrowser(""); err != nil {
		t.Skip("no Chrome or Chromium on this machine")
	}
	s := logsTestServer(t, nil)
	s.live().Config["uptime_allow_private"] = "true"
	u := newUptimeRunner(s, s.uptimeSettings())
	t.Cleanup(u.browser.close)
	return s, u
}

// playCheck normalizes c and plays it once; it must pass unless fail is set.
func playCheck(t *testing.T, u *uptimeRunner, c UptimeCheck, fail bool) (uptimeResult, browserRun) {
	t.Helper()
	if c.Name == "" {
		c.Name = "Test"
	}
	c.Kind = "browser"
	if err := c.normalize(nil); err != nil {
		t.Fatal(err)
	}
	res, run, err := u.playOnce(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if res.OK == fail {
		t.Fatalf("ok=%v: %+v %+v", res.OK, res, run.Steps)
	}
	return res, run
}

// The run's traceparent reaches the first page's origin and the origins the
// check allows, and no other.
func TestBrowserTraceHeader(t *testing.T) {
	_, u := browserRunner(t)
	var mu sync.Mutex
	seen := map[string]string{}
	record := func(name string, w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if tp := r.Header.Get("traceparent"); tp != "" {
			seen[name] = tp
		}
		mu.Unlock()
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "traceparent")
	}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record("other", w, r)
		_, _ = io.WriteString(w, "ok")
	}))
	defer other.Close()
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api" {
			record("api", w, r)
			_, _ = io.WriteString(w, "ok")
			return
		}
		record("page", w, r)
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<!doctype html><p id="done"></p><script>
Promise.all([fetch("/api"), fetch("`+other.URL+`/x")]).then(() => document.getElementById("done").textContent = "loaded");
</script>`)
	}))
	defer site.Close()
	steps := []BrowserStep{{Action: "goto", Value: site.URL + "/"}, {Action: "wait_text", Text: "loaded"}}

	play := func(o BrowserOptions) (browserRun, map[string]string) {
		t.Helper()
		mu.Lock()
		clear(seen)
		mu.Unlock()
		_, run := playCheck(t, u, UptimeCheck{Steps: steps, BrowserOptions: o}, false)
		mu.Lock()
		defer mu.Unlock()
		out := map[string]string{}
		for k, v := range seen {
			out[k] = v
		}
		return run, out
	}
	run, got := play(BrowserOptions{})
	want := "00-" + run.TraceID + "-"
	if len(run.TraceID) != 32 || !strings.HasPrefix(got["page"], want) || !strings.HasPrefix(got["api"], want) || got["other"] != "" {
		t.Fatalf("trace %q, headers %v", run.TraceID, got)
	}
	run, got = play(BrowserOptions{TraceOrigins: []string{site.URL, other.URL}})
	if !strings.HasPrefix(got["other"], "00-"+run.TraceID+"-") {
		t.Fatalf("allowed origin: %v", got)
	}
	run, got = play(BrowserOptions{NoTrace: true})
	if run.TraceID != "" || len(got) != 0 {
		t.Fatalf("no trace: %q %v", run.TraceID, got)
	}
}
