package analytics

import "github.com/styropyr0/visionary/internal/uptimecore"

type (
	uptimeResult  = uptimecore.Result
	browserRun    = uptimecore.BrowserRun
	stepResult    = uptimecore.StepResult
	netFailure    = uptimecore.NetFailure
	browserStatus = uptimecore.BrowserStatus
	// Stored as one JSON column in uptime_browser_checks
	BrowserOptions = uptimecore.BrowserOptions
	BrowserLimits  = uptimecore.BrowserLimits
	BrowserFile    = uptimecore.BrowserFile
	netRequest     = uptimecore.NetRequest
)

var errBlockedAddress = uptimecore.ErrBlockedAddress

const browserHelp = "Install Chrome or Chromium on the server (apt install chromium, dnf install chromium, apk add chromium, or Google Chrome from google.com/chrome), " +
	"or point Settings → Server → Other → Browser for browser checks (or VISIONARY_CHROME) at its executable. " +
	"The default Docker image has no browser; build the browser variant with docker build --target browser."

func (c UptimeCheck) core() uptimecore.Check {
	out := uptimecore.Check{ID: c.ID, Name: c.Name, Kind: c.Kind, URL: c.URL, Method: c.Method, ExpectStatus: c.ExpectStatus,
		BodyContains: c.BodyContains, Headers: c.Headers, IntervalS: c.IntervalS, TimeoutMs: c.TimeoutMs, DegradedMs: c.DegradedMs,
		FollowRedirects: c.FollowRedirects, Secrets: c.Secrets, FinalScreenshot: c.FinalScreenshot, BrowserOptions: c.BrowserOptions,
		API: c.API, TCP: c.TCP, DNS: c.DNS, Ping: c.Ping, SSL: c.SSL}
	for _, s := range c.Steps {
		out.Steps = append(out.Steps, uptimecore.BrowserStep(s))
	}
	return out
}
