package agentapi

import "github.com/styropyr0/visionary/internal/uptimecore"

// Only one runner per location runs checks, and 409 on results means it lost its lease
const (
	LocationPollPath    = "/api/agent/v1/location/poll"
	LocationResultsPath = "/api/agent/v1/location/results"
)

type LocationPoll struct {
	Location string `json:"location"`
	// Instance is random per process so two runners on one key don't both run checks
	Instance       string   `json:"instance"`
	Version        string   `json:"version"`
	Host           string   `json:"host"`
	Browser        bool     `json:"browser"`
	BrowserVersion string   `json:"browser_version,omitempty"`
	Kinds          []string `json:"kinds"`
}

type LocationChecks struct {
	Active bool `json:"active"`
	// Checks include secrets and stay empty unless Active
	Checks       []uptimecore.Check `json:"checks"`
	PollS        int                `json:"poll_s"`
	AllowPrivate bool               `json:"allow_private"`
	Message      string             `json:"message,omitempty"`
}

type LocationResults struct {
	Location string           `json:"location"`
	Instance string           `json:"instance"`
	Results  []LocationResult `json:"results"`
}

type LocationResult struct {
	CheckID         int64                  `json:"check_id"`
	Result          uptimecore.Result      `json:"result"`
	Browser         *uptimecore.BrowserRun `json:"browser,omitempty"`
	Screenshot      []byte                 `json:"screenshot,omitempty"`
	FinalScreenshot []byte                 `json:"final_screenshot,omitempty"`
	StepShots       [][]byte               `json:"step_shots,omitempty"`
}
