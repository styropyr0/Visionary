package analytics

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBrowserMoreStepsNormalize(t *testing.T) {
	file := BrowserFile{Name: "avatar.png", Type: "image/png", Data: base64.StdEncoding.EncodeToString([]byte("png!"))}
	ok := UptimeCheck{Name: "More", Kind: "browser", BrowserOptions: BrowserOptions{Files: []BrowserFile{file}}, Steps: []BrowserStep{
		{Action: "goto", Value: "https://shop.example.com"},
		{Action: "extract", Selector: "#order", Name: "ORDER", Regex: `#(\d+)`},
		{Action: "type", Selector: "#q", Value: "{{var.ORDER}}"},
		{Action: "upload", Selector: "input[type=file]", Name: "avatar.png"},
		{Action: "drag", Selector: "#a", Target: "#b"},
		{Action: "set_storage", Name: "k", Value: "v"},
		{Action: "grant_permission", Permission: "geolocation", Lat: 52.5, Lon: 13.4},
		{Action: "frame", Selector: "iframe"},
		{Action: "frame"},
	}}
	c := ok
	if err := c.normalize(nil); err != nil {
		t.Fatal(err)
	}
	if c.Steps[1].Attr != "text" || c.Steps[5].Scope != "local" || c.Files[0].Size != 4 {
		t.Fatalf("defaults: %+v %+v", c.Steps, c.Files)
	}
	m := c.masked()
	if m.Files[0].Data != "" || c.Files[0].Data == "" {
		t.Fatal("file data shown")
	}
	again := m
	again.Steps = append([]BrowserStep(nil), m.Steps...)
	if err := again.normalize(&c); err != nil || again.Files[0].Data != file.Data {
		t.Fatalf("round trip: %v", err)
	}
	bad := []func(c *UptimeCheck){
		func(c *UptimeCheck) { c.Steps[2].Value = "{{var.NOPE}}" },
		func(c *UptimeCheck) { c.Steps[1], c.Steps[2] = c.Steps[2], c.Steps[1] },
		func(c *UptimeCheck) { c.Steps[1].Regex = "(" },
		func(c *UptimeCheck) { c.Steps[1].Name = "bad name" },
		func(c *UptimeCheck) { c.Steps[3].Name = "other.png" },
		func(c *UptimeCheck) { c.Steps[4].Target = "" },
		func(c *UptimeCheck) { c.Steps[5].Scope = "cookie" },
		func(c *UptimeCheck) { c.Steps[6].Lat = 100 },
		func(c *UptimeCheck) { c.Steps[6].Permission = "camera" },
		func(c *UptimeCheck) {
			c.Steps = append(c.Steps, BrowserStep{Action: "set_cookie", Name: "a b", Value: "x"})
		},
		func(c *UptimeCheck) {
			c.Files = []BrowserFile{{Name: "big", Data: base64.StdEncoding.EncodeToString(make([]byte, 256<<10+1))}}
		},
		func(c *UptimeCheck) { c.Files = []BrowserFile{{Name: "../x", Data: "eA=="}} },
	}
	for i, f := range bad {
		c := ok
		c.Steps = append([]BrowserStep(nil), ok.Steps...)
		c.Files = append([]BrowserFile(nil), ok.Files...)
		f(&c)
		if err := c.normalize(nil); err == nil {
			t.Errorf("case %d: accepted", i)
		}
	}
}

func TestBrowserMoreStepsRun(t *testing.T) {
	_, u := browserRunner(t)
	inner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<!doctype html><button onclick="document.getElementById('out').textContent='paid '+location.host">Pay</button><p id="out"></p>`)
	}))
	defer inner.Close()
	// localhost and 127.0.0.1 are different sites, so the frame is cross-site
	frameURL := strings.Replace(inner.URL, "127.0.0.1", "localhost", 1)
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/cookie" {
			c, _ := r.Cookie("probe")
			v := ""
			if c != nil {
				v = c.Value
			}
			_, _ = io.WriteString(w, `<p>cookie is `+v+`</p>`)
			return
		}
		_, _ = io.WriteString(w, `<!doctype html>
<p id="order">Order #4821 confirmed</p><input id="q">
<span id="tip-host" onmouseover="document.getElementById('tip').textContent='Tooltip shown'">hover me</span><p id="tip"></p>
<div id="card" draggable="true" style="width:80px;height:40px;background:#ccc" ondragstart="event.dataTransfer.setData('text/plain','card')">card</div>
<div id="lane" style="width:200px;height:80px;margin-top:20px;background:#eee" ondragover="event.preventDefault()" ondrop="event.preventDefault();this.textContent='dropped '+event.dataTransfer.getData('text/plain')">lane</div>
<input type="file" id="file" onchange="document.getElementById('fname').textContent=this.files[0].name+' '+this.files[0].size">
<p id="fname"></p>
<iframe id="pay" src="`+frameURL+`/" style="width:300px;height:100px"></iframe>`)
	}))
	defer site.Close()
	steps := []BrowserStep{
		{Action: "goto", Value: site.URL + "/"},
		{Action: "extract", Selector: "#order", Name: "ORDER", Regex: `#(\d+)`},
		{Action: "type", Selector: "#q", Value: "{{var.ORDER}}"},
		{Action: "script", Value: `document.querySelector("#q").value === "4821"`},
		{Action: "hover", Text: "hover me"},
		{Action: "assert_text", Text: "Tooltip shown"},
		{Action: "drag", Selector: "#card", Target: "#lane"},
		{Action: "assert_text", Text: "dropped card"},
		{Action: "upload", Selector: "#file", Name: "note.txt"},
		{Action: "assert_text", Text: "note.txt 5"},
		{Action: "frame", Selector: "#pay"},
		{Action: "click", Text: "Pay"},
		{Action: "assert_text", Text: "paid localhost"},
		{Action: "frame"},
		{Action: "assert_text", Text: "Order #4821"},
		{Action: "set_storage", Name: "theme", Value: "dark", Scope: "session"},
		{Action: "script", Value: `sessionStorage.getItem("theme") === "dark"`},
		{Action: "grant_permission", Permission: "geolocation", Lat: 52.52, Lon: 13.405},
		{Action: "script", Value: `return await new Promise((ok) => navigator.geolocation.getCurrentPosition((p) => ok(Math.abs(p.coords.latitude - 52.52) < 0.001), () => ok(false)))`},
		{Action: "grant_permission", Permission: "notifications"},
		{Action: "script", Value: `Notification.permission === "granted"`},
		{Action: "grant_permission", Permission: "clipboard"},
		{Action: "script", Value: `return (await navigator.permissions.query({ name: "clipboard-read" })).state === "granted"`},
		{Action: "set_cookie", Name: "probe", Value: "v{{var.ORDER}}"},
		{Action: "goto", Value: site.URL + "/cookie"},
		{Action: "assert_text", Text: "cookie is v4821"},
	}
	c := UptimeCheck{Steps: steps, BrowserOptions: BrowserOptions{Files: []BrowserFile{{Name: "note.txt", Type: "text/plain", Data: base64.StdEncoding.EncodeToString([]byte("hello"))}}}}
	c.TimeoutMs = 60000
	_, run := playCheck(t, u, c, false)
	if run.Steps[1].Detail != "ORDER = 4821" {
		t.Errorf("extract detail %q", run.Steps[1].Detail)
	}
}
