package uptimecore

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestBrowserMasker(t *testing.T) {
	m := masker(map[string]string{"A": "s3cr3t value", "B": "ab"})
	got := m("typed s3cr3t value and s3cr3t+value, ab")
	if strings.Contains(got, "s3cr3t") || !strings.Contains(got, "ab") {
		t.Fatalf("masked: %q", got)
	}
}

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

func TestProbe(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Fail") != "" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, "all good")
	}))
	defer site.Close()
	allow := func() bool { return true }
	c := Check{ID: 1, Kind: "http", URL: site.URL, Method: "GET", ExpectStatus: "200-299", BodyContains: "good", TimeoutMs: 5000}
	if res := Probe(context.Background(), c, allow); !res.OK || res.Status != 200 || res.TTFBMs == nil {
		t.Fatalf("passing probe: %+v", res)
	}
	c.Headers = map[string]string{"X-Fail": "1"}
	if res := Probe(context.Background(), c, allow); res.OK || res.Status != 503 || !strings.Contains(res.Error, "unexpected status 503") {
		t.Fatalf("failing probe: %+v", res)
	}
	c.Headers = nil
	res := ProbeWithRetry(context.Background(), c, func() bool { return false })
	if res.OK || !res.Blocked() || res.Retried {
		t.Fatalf("guarded probe: %+v", res)
	}
}

func TestParseExpectStatus(t *testing.T) {
	rs, err := ParseExpectStatus("2xx,301,400-404")
	if err != nil {
		t.Fatal(err)
	}
	for code, want := range map[int]bool{200: true, 299: true, 301: true, 302: false, 404: true, 405: false} {
		if StatusAccepted(rs, code) != want {
			t.Errorf("%d: want %v", code, want)
		}
	}
	if _, err := ParseExpectStatus("700"); err == nil {
		t.Error("700 accepted")
	}
}
