package analytics

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// recording is a Recorder export as Chrome 131 saves it, trimmed to one
// sign-in and checkout.
const recording = `{
  "title": "Sign in and check out",
  "steps": [
    { "type": "setViewport", "width": 390, "height": 844, "deviceScaleFactor": 3, "isMobile": true, "hasTouch": true, "isLandscape": false },
    {
      "type": "navigate",
      "url": "https://shop.example.com/login",
      "assertedEvents": [{ "type": "navigation", "url": "https://shop.example.com/login", "title": "Sign in" }]
    },
    {
      "type": "click",
      "target": "main",
      "selectors": [["aria/Email"], ["#email"], ["xpath///*[@id=\"email\"]"], ["pierce/#email"]],
      "offsetY": 18.5,
      "offsetX": 120
    },
    {
      "type": "change",
      "value": "probe@example.com",
      "selectors": [["aria/Email"], ["#email"], ["xpath///*[@id=\"email\"]"], ["pierce/#email"]],
      "target": "main"
    },
    { "type": "keyDown", "target": "main", "key": "Tab" },
    { "type": "keyUp", "key": "Tab", "target": "main" },
    {
      "type": "change",
      "value": "Germany",
      "selectors": [["aria/Country[role=\"combobox\"]"], ["select#country"], ["xpath///*[@id=\"country\"]"]],
      "target": "main"
    },
    {
      "type": "click",
      "target": "main",
      "selectors": [["aria/Sign in[role=\"button\"]"], ["form > button"], ["xpath//html/body/form/button"], ["pierce/form > button"], ["text/Sign in"]],
      "offsetY": 12,
      "offsetX": 40,
      "assertedEvents": [{ "type": "navigation", "url": "https://shop.example.com/account?session=abc", "title": "Account" }]
    },
    { "type": "scroll", "target": "main", "x": 0, "y": 600 },
    {
      "type": "waitForElement",
      "selectors": [[".cart-item"]],
      "count": 2,
      "operator": ">="
    },
    {
      "type": "click",
      "target": "main",
      "selectors": [["xpath///*[@id=\"promo\"]/div[2]"]],
      "offsetY": 5,
      "offsetX": 5
    },
    {
      "type": "click",
      "target": "main",
      "frame": [0],
      "selectors": [["#card-number"]],
      "offsetY": 5,
      "offsetX": 5
    },
    { "type": "doubleClick", "target": "main", "selectors": [["#qty"]], "offsetX": 3, "offsetY": 3 },
    { "type": "waitForExpression", "expression": "document.title.includes('Thanks')" },
    { "type": "keyDown", "target": "main", "key": "Meta" },
    { "type": "click", "target": "popup-1", "selectors": [["#ok"]], "offsetX": 1, "offsetY": 1 }
  ]
}`

func TestConvertRecording(t *testing.T) {
	got, err := convertRecording([]byte(recording))
	if err != nil {
		t.Fatal(err)
	}
	want := []BrowserStep{
		{Action: "goto", Value: "https://shop.example.com/login"},
		{Action: "click", Selector: "#email"},
		{Action: "type", Selector: "#email", Value: "probe@example.com"},
		{Action: "press", Value: "Tab"},
		{Action: "select", Selector: "select#country", Value: "Germany"},
		{Action: "click", Text: "Sign in"},
		{Action: "wait_navigation", Value: "/account"},
		{Action: "assert_count", Selector: ".cart-item", Count: 2, Op: "gte"},
		{Action: "frame", Selector: "iframe:nth-of-type(1)"},
		{Action: "click", Selector: "#card-number"},
		{Action: "frame"},
		{Action: "script", Value: "for (let i = 0; i < 100; i++) { if (await (document.title.includes('Thanks'))) return true; await new Promise((r) => setTimeout(r, 100)); } return false;"},
	}
	if !reflect.DeepEqual(got.Steps, want) {
		t.Fatalf("steps:\n%+v\nwant\n%+v", got.Steps, want)
	}
	if got.Title != "Sign in and check out" || got.Device != "phone" {
		t.Errorf("title %q device %q", got.Title, got.Device)
	}
	for _, w := range []string{"step 9 (scroll)", "step 11 (click): Chrome gave only selectors", "step 13 (doubleClick)", "step 15 (keyDown): key combinations", "step 16 (click): it happens in another tab"} {
		if !strings.Contains(strings.Join(got.Unsupported, "\n"), w) {
			t.Errorf("unsupported lacks %q: %q", w, got.Unsupported)
		}
	}
	if len(got.Unsupported) != 5 {
		t.Errorf("unsupported: %q", got.Unsupported)
	}
	// What it converts to saves as a check.
	c := UptimeCheck{Name: got.Title, Kind: "browser", Steps: got.Steps, BrowserOptions: BrowserOptions{Device: got.Device}}
	if err := c.normalize(nil); err != nil {
		t.Fatal(err)
	}

	desk, err := convertRecording([]byte(`{"title":"x","steps":[{"type":"setViewport","width":1000,"height":700,"isMobile":false},{"type":"navigate","url":"https://a.example"}]}`))
	if err != nil || desk.Device != "custom" || desk.ViewportW != 1000 || desk.ViewportH != 700 {
		t.Fatalf("custom viewport: %+v %v", desk, err)
	}
	for _, bad := range []string{`nope`, `{"steps":[]}`} {
		if _, err := convertRecording([]byte(bad)); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}

	s := logsTestServer(t, nil)
	rec := httptest.NewRecorder()
	s.handleRecorderImport(rec, httptest.NewRequest("POST", "/api/uptime/browser/recorder", strings.NewReader(recording)))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"unsupported"`) {
		t.Fatalf("handler: %d %s", rec.Code, rec.Body)
	}
}
