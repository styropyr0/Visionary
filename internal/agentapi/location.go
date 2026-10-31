package agentapi

import "github.com/styropyr0/visionary/internal/uptimecore"

// A location runner is the agent started with VISIONARY_AGENT_LOCATION: it
// runs the uptime checks assigned to that location instead of reporting the
// host. It authenticates with the location's key (Authorization: Bearer).
//
// Every PollS seconds it posts a LocationPoll and gets back the checks to
// run. Only one runner per location runs checks: the server leases the
// location to the instance that polled first and answers Active false to
// the others until that instance stops polling. Results go to
// LocationResultsPath in batches; 409 means the instance lost its lease and
// should drop them.
const (
	LocationPollPath    = "/api/agent/v1/location/poll"
	LocationResultsPath = "/api/agent/v1/location/results"
)

// LocationPoll says who is asking and what this machine can run.
type LocationPoll struct {
	Location string `json:"location"`
	// Instance is random per process, so two runners started with the same
	// key do not both run checks.
	Instance string `json:"instance"`
	Version  string `json:"version"`
	Host     string `json:"host"`
	// Browser is whether a Chrome or Chromium was found, so browser checks
	// can run here.
	Browser        bool     `json:"browser"`
	BrowserVersion string   `json:"browser_version,omitempty"`
	Kinds          []string `json:"kinds"` // check kinds this runner can run
}

// LocationChecks is the answer to a poll.
type LocationChecks struct {
	Active bool `json:"active"`
	// Checks are every enabled check assigned to the location, secrets
	// included; empty unless Active.
	Checks []uptimecore.Check `json:"checks"`
	PollS  int                `json:"poll_s"`
	// AllowPrivate is the server's setting for reaching private addresses,
	// which the runner follows unless VISIONARY_AGENT_ALLOW_PRIVATE is set.
	AllowPrivate bool   `json:"allow_private"`
	Message      string `json:"message,omitempty"`
}

// LocationResults is a batch of runs.
type LocationResults struct {
	Location string           `json:"location"`
	Instance string           `json:"instance"`
	Results  []LocationResult `json:"results"`
}

// LocationResult is one run of one check. Browser is set for browser
// checks, with their screenshots as JPEG bytes.
type LocationResult struct {
	CheckID         int64                  `json:"check_id"`
	Result          uptimecore.Result      `json:"result"`
	Browser         *uptimecore.BrowserRun `json:"browser,omitempty"`
	Screenshot      []byte                 `json:"screenshot,omitempty"`
	FinalScreenshot []byte                 `json:"final_screenshot,omitempty"`
}
