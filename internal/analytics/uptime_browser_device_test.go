package analytics

import (
	"bytes"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBrowserDeviceNormalize(t *testing.T) {
	steps := []BrowserStep{{Action: "goto", Value: "https://example.com"}}
	c := UptimeCheck{Name: "x", Kind: "browser", Steps: steps}
	if err := c.normalize(nil); err != nil || c.Device != "desktop" || c.device().W != 1280 {
		t.Fatalf("default: %v %+v", err, c.BrowserOptions)
	}
	c = UptimeCheck{Name: "x", Kind: "browser", Steps: steps, BrowserOptions: BrowserOptions{Device: "phone", ViewportW: 999}}
	if err := c.normalize(nil); err != nil || c.ViewportW != 0 || !c.device().Mobile {
		t.Fatalf("phone: %v %+v", err, c.BrowserOptions)
	}
	for _, o := range []BrowserOptions{{Device: "watch"}, {Device: "custom", ViewportW: 100, ViewportH: 800}} {
		c := UptimeCheck{Name: "x", Kind: "browser", Steps: steps, BrowserOptions: o}
		if err := c.normalize(nil); err == nil {
			t.Errorf("%+v accepted", o)
		}
	}
}

func TestBrowserDeviceRun(t *testing.T) {
	_, u := browserRunner(t)
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<!doctype html><meta name="viewport" content="width=device-width">
<button onclick="this.textContent='tapped'">Tap me</button>`)
	}))
	defer site.Close()
	phone := []BrowserStep{
		{Action: "goto", Value: site.URL},
		{Action: "script", Value: `innerWidth === 390 && devicePixelRatio === 3 && navigator.userAgent.includes("Mobile") && navigator.maxTouchPoints > 0`},
		{Action: "click", Text: "Tap me"},
		{Action: "assert_text", Text: "tapped"},
		{Action: "assert_text", Text: "never there"},
	}
	_, run := playCheck(t, u, UptimeCheck{Steps: phone, BrowserOptions: BrowserOptions{Device: "phone"}}, true)
	if run.FailedStep != 4 {
		t.Fatalf("phone run: %+v", run.Steps)
	}
	img, err := jpeg.DecodeConfig(bytes.NewReader(run.Screenshot))
	if err != nil || img.Width != 390 || img.Height != 844 {
		t.Fatalf("screenshot %dx%d %v", img.Width, img.Height, err)
	}
	custom := []BrowserStep{{Action: "goto", Value: site.URL}, {Action: "script", Value: `innerWidth === 1000 && innerHeight === 700 && devicePixelRatio === 1`}}
	playCheck(t, u, UptimeCheck{Steps: custom, BrowserOptions: BrowserOptions{Device: "custom", ViewportW: 1000, ViewportH: 700}}, false)
}
