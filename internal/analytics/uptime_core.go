package analytics

import "github.com/styropyr0/visionary/internal/uptimecore"

// Running a check lives in internal/uptimecore, shared with the host agent's
// location runner; these are its types under the names this package uses.
type (
	uptimeResult  = uptimecore.Result
	browserRun    = uptimecore.BrowserRun
	stepResult    = uptimecore.StepResult
	netFailure    = uptimecore.NetFailure
	browserStatus = uptimecore.BrowserStatus
)

var errBlockedAddress = uptimecore.ErrBlockedAddress

// browserHelp says how to get a browser, for the dashboard and the API.
const browserHelp = "Install Chrome or Chromium on the server (apt install chromium, dnf install chromium, apk add chromium, or Google Chrome from google.com/chrome), " +
	"or point Settings → Server → Other → Browser for browser checks (or VISIONARY_CHROME) at its executable. " +
	"The default Docker image has no browser; build the browser variant with docker build --target browser."

// core is what running the check needs.
func (c UptimeCheck) core() uptimecore.Check {
	out := uptimecore.Check{ID: c.ID, Name: c.Name, Kind: c.Kind, URL: c.URL, Method: c.Method, ExpectStatus: c.ExpectStatus,
		BodyContains: c.BodyContains, Headers: c.Headers, IntervalS: c.IntervalS, TimeoutMs: c.TimeoutMs, DegradedMs: c.DegradedMs,
		FollowRedirects: c.FollowRedirects, Secrets: c.Secrets, FinalScreenshot: c.FinalScreenshot,
		API: c.API, TCP: c.TCP, DNS: c.DNS, Ping: c.Ping, SSL: c.SSL}
	for _, s := range c.Steps {
		out.Steps = append(out.Steps, uptimecore.BrowserStep(s))
	}
	return out
}
